package evaluation

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"log/slog"
	"sync"
	"time"
)

var ErrInvalidJob = errors.New("invalid routing evaluation job")

type SubmitResult string

const (
	SubmitAccepted   SubmitResult = "accepted"
	SubmitSampledOut SubmitResult = "sampled_out"
	SubmitQueueFull  SubmitResult = "queue_full"
	SubmitExpired    SubmitResult = "expired"
	SubmitClosed     SubmitResult = "closed"
	SubmitInvalid    SubmitResult = "invalid"
)

type Job struct {
	ProfileID             int64
	SampleRateBPS         int
	DailyBudgetMicroUSD   int64
	EstimatedCostMicroUSD int64
	MaxConcurrency        int
	QueueCapacity         int
	Timeout               time.Duration
	ExpiresAt             time.Time
	Run                   func(context.Context) (Result, error)
}

type Result struct {
	SpentMicroUSD int64
	Evidence      *Evidence
}

type Status struct {
	Accepted       int64 `json:"accepted"`
	SampledOut     int64 `json:"sampled_out"`
	QueueDropped   int64 `json:"queue_dropped"`
	Expired        int64 `json:"expired"`
	BudgetRejected int64 `json:"budget_rejected"`
	Completed      int64 `json:"completed"`
	Failed         int64 `json:"failed"`
	Running        int64 `json:"running"`
	Queued         int64 `json:"queued"`
}

type ServiceOptions struct {
	Now    func() time.Time
	Sample func(rateBPS int) bool
}

type Service struct {
	store  *Store
	now    func() time.Time
	sample func(int) bool
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	closed   bool
	profiles map[int64]*profileQueue
	status   Status
	wg       sync.WaitGroup

	closeOnce sync.Once
	closeErr  error
}

type profileQueue struct {
	running int
	jobs    []Job
}

func NewService(store *Store, options ServiceOptions) (*Service, error) {
	if store == nil {
		return nil, ErrInvalidJob
	}
	if err := store.ResetReservations(context.Background()); err != nil {
		return nil, err
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	sample := options.Sample
	if sample == nil {
		sample = randomSample
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		store: store, now: now, sample: sample,
		ctx: ctx, cancel: cancel, profiles: make(map[int64]*profileQueue),
	}, nil
}

func (s *Service) Submit(job Job) SubmitResult {
	if err := validateJob(job); err != nil {
		return SubmitInvalid
	}
	if !job.ExpiresAt.After(s.now()) {
		s.mu.Lock()
		s.status.Expired++
		s.mu.Unlock()
		return SubmitExpired
	}
	if !s.sample(job.SampleRateBPS) {
		s.mu.Lock()
		s.status.SampledOut++
		s.mu.Unlock()
		return SubmitSampledOut
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return SubmitClosed
	}
	state := s.profiles[job.ProfileID]
	if state == nil {
		state = &profileQueue{}
		s.profiles[job.ProfileID] = state
	}
	if state.running == 0 && len(state.jobs) == 0 {
		s.status.Accepted++
		s.startLocked(state, job)
		return SubmitAccepted
	}
	if state.running < job.MaxConcurrency && len(state.jobs) == 0 {
		s.status.Accepted++
		s.startLocked(state, job)
		return SubmitAccepted
	}
	if len(state.jobs) >= job.QueueCapacity {
		s.status.QueueDropped++
		return SubmitQueueFull
	}
	state.jobs = append(state.jobs, job)
	s.status.Accepted++
	s.status.Queued++
	return SubmitAccepted
}

func (s *Service) startLocked(state *profileQueue, job Job) {
	state.running++
	s.status.Running++
	s.wg.Add(1)
	go s.run(job)
}

func (s *Service) run(job Job) {
	defer s.wg.Done()
	defer s.finish(job.ProfileID)

	if !job.ExpiresAt.After(s.now()) {
		s.increment(func(status *Status) { status.Expired++ })
		return
	}
	reservation, err := s.store.ReserveBudget(
		s.ctx, job.ProfileID, job.DailyBudgetMicroUSD, job.EstimatedCostMicroUSD,
	)
	if err != nil {
		if errors.Is(err, ErrBudgetExceeded) {
			s.increment(func(status *Status) { status.BudgetRejected++ })
		} else {
			s.increment(func(status *Status) { status.Failed++ })
			slog.Warn("routing.evaluation.budget_failed", "profile_id", job.ProfileID, "error", err)
		}
		return
	}

	timeout := minDuration(job.Timeout, job.ExpiresAt.Sub(s.now()))
	if timeout <= 0 {
		_ = reservation.Release(context.Background())
		s.increment(func(status *Status) { status.Expired++ })
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	result, runErr := job.Run(ctx)
	cancel()
	if result.SpentMicroUSD < 0 || result.SpentMicroUSD > job.EstimatedCostMicroUSD {
		runErr = ErrBudgetExceeded
		result.SpentMicroUSD = min(max(result.SpentMicroUSD, int64(0)), job.EstimatedCostMicroUSD)
	}
	if result.SpentMicroUSD == 0 {
		err = reservation.Release(context.Background())
	} else {
		err = reservation.Commit(context.Background(), result.SpentMicroUSD)
	}
	if err != nil {
		runErr = errors.Join(runErr, err)
	}
	if runErr == nil && result.Evidence != nil {
		runErr = s.store.RecordEvidence(context.Background(), *result.Evidence)
	}
	if runErr != nil {
		s.increment(func(status *Status) { status.Failed++ })
		slog.Warn("routing.evaluation.failed", "profile_id", job.ProfileID, "error", runErr)
		return
	}
	s.increment(func(status *Status) { status.Completed++ })
}

func (s *Service) finish(profileID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.profiles[profileID]
	if state == nil {
		return
	}
	state.running--
	s.status.Running--
	if s.closed {
		return
	}
	for len(state.jobs) > 0 {
		next := state.jobs[0]
		if state.running >= next.MaxConcurrency {
			break
		}
		state.jobs = state.jobs[1:]
		s.status.Queued--
		s.startLocked(state, next)
	}
	if state.running == 0 && len(state.jobs) == 0 {
		delete(s.profiles, profileID)
	}
}

func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		for _, state := range s.profiles {
			s.status.QueueDropped += int64(len(state.jobs))
			s.status.Queued -= int64(len(state.jobs))
			state.jobs = nil
		}
		s.cancel()
		s.mu.Unlock()
		s.wg.Wait()
	})
	return s.closeErr
}

func (s *Service) increment(update func(*Status)) {
	s.mu.Lock()
	update(&s.status)
	s.mu.Unlock()
}

func validateJob(job Job) error {
	if job.ProfileID <= 0 || job.SampleRateBPS <= 0 || job.SampleRateBPS > 10_000 ||
		job.DailyBudgetMicroUSD <= 0 || job.EstimatedCostMicroUSD <= 0 ||
		job.EstimatedCostMicroUSD > job.DailyBudgetMicroUSD || job.MaxConcurrency <= 0 ||
		job.MaxConcurrency > 32 || job.QueueCapacity <= 0 || job.QueueCapacity > 4096 ||
		job.Timeout <= 0 || job.Timeout > 10*time.Minute || job.ExpiresAt.IsZero() || job.Run == nil {
		return ErrInvalidJob
	}
	return nil
}

func randomSample(rateBPS int) bool {
	var value [2]byte
	if _, err := rand.Read(value[:]); err != nil {
		return false
	}
	return int(binary.BigEndian.Uint16(value[:])%10_000) < rateBPS
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
