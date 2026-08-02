package evaluation

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSubmitNeverWaitsForEvaluationAndDropsWhenProfileQueueIsFull(t *testing.T) {
	db := openEvaluationDB(t)
	profileID := insertEvaluationProfile(t, db)
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	service, err := NewService(NewStore(db, func() time.Time { return now }), ServiceOptions{
		Now:    func() time.Time { return now },
		Sample: func(int) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })

	started := make(chan struct{})
	release := make(chan struct{})
	job := Job{
		ProfileID: profileID, SampleRateBPS: 10_000, DailyBudgetMicroUSD: 1_000,
		EstimatedCostMicroUSD: 10, MaxConcurrency: 1, QueueCapacity: 1,
		Timeout: time.Minute, ExpiresAt: now.Add(10 * time.Minute),
		Run: func(context.Context) (Result, error) {
			select {
			case <-started:
			default:
				close(started)
			}
			<-release
			return Result{SpentMicroUSD: 10}, nil
		},
	}

	begin := time.Now()
	if got := service.Submit(job); got != SubmitAccepted {
		t.Fatalf("first Submit()=%q", got)
	}
	<-started
	if got := service.Submit(job); got != SubmitAccepted {
		t.Fatalf("second Submit()=%q", got)
	}
	if got := service.Submit(job); got != SubmitQueueFull {
		t.Fatalf("third Submit()=%q, want queue_full", got)
	}
	if elapsed := time.Since(begin); elapsed > 100*time.Millisecond {
		t.Fatalf("Submit blocked for %s", elapsed)
	}
	close(release)
}

func TestSubmitSkipsSamplingAndExpiredCredentialsWithoutRunning(t *testing.T) {
	db := openEvaluationDB(t)
	profileID := insertEvaluationProfile(t, db)
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	runs := 0
	service, err := NewService(NewStore(db, func() time.Time { return now }), ServiceOptions{
		Now:    func() time.Time { return now },
		Sample: func(int) bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	job := Job{
		ProfileID: profileID, SampleRateBPS: 1000, DailyBudgetMicroUSD: 100,
		EstimatedCostMicroUSD: 10, MaxConcurrency: 1, QueueCapacity: 1,
		Timeout: time.Minute, ExpiresAt: now.Add(time.Minute),
		Run: func(context.Context) (Result, error) {
			runs++
			return Result{}, nil
		},
	}

	if got := service.Submit(job); got != SubmitSampledOut {
		t.Fatalf("sampled Submit()=%q", got)
	}
	job.ExpiresAt = now.Add(-time.Second)
	if got := service.Submit(job); got != SubmitExpired {
		t.Fatalf("expired Submit()=%q", got)
	}
	if runs != 0 {
		t.Fatalf("runs=%d, want 0", runs)
	}
}

func TestServiceEnforcesDailyBudgetAndPersistsSuccessfulEvidence(t *testing.T) {
	db := openEvaluationDB(t)
	profileID := insertEvaluationProfile(t, db)
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	service, err := NewService(NewStore(db, func() time.Time { return now }), ServiceOptions{
		Now:    func() time.Time { return now },
		Sample: func(int) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	completed := make(chan struct{}, 2)
	job := Job{
		ProfileID: profileID, SampleRateBPS: 10_000, DailyBudgetMicroUSD: 100,
		EstimatedCostMicroUSD: 60, MaxConcurrency: 1, QueueCapacity: 2,
		Timeout: time.Minute, ExpiresAt: now.Add(time.Minute),
		Run: func(context.Context) (Result, error) {
			completed <- struct{}{}
			return Result{
				SpentMicroUSD: 50,
				Evidence: &Evidence{
					ProfileID: profileID, Strategy: "20260802-001", Route: "balanced",
					TaskType: "simple", CandidateModel: "fast", ReferenceModel: "strong",
					ReviewerModel: "judge", Outcome: OutcomeCandidateWin,
					CandidateCostMicroUSD: 10, ReferenceCostMicroUSD: 35,
					ReviewerCostMicroUSD: 5,
				},
			}, nil
		},
	}

	if got := service.Submit(job); got != SubmitAccepted {
		t.Fatalf("first Submit()=%q", got)
	}
	if got := service.Submit(job); got != SubmitAccepted {
		t.Fatalf("second Submit()=%q", got)
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("first evaluation did not run")
	}
	deadline := time.Now().Add(time.Second)
	for {
		status := service.Status()
		if status.Completed == 1 && status.BudgetRejected == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("service status=%+v", status)
		}
		time.Sleep(time.Millisecond)
	}

	rows, err := service.store.ListEvidence(context.Background(), profileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].CandidateWins != 1 {
		t.Fatalf("evidence=%+v", rows)
	}
	budget, err := service.store.Budget(context.Background(), profileID, now)
	if err != nil {
		t.Fatal(err)
	}
	if budget.SpentMicroUSD != 50 || budget.ReservedMicroUSD != 0 {
		t.Fatalf("budget=%+v", budget)
	}
}

func TestServiceRejectsWorstCaseCostBeforeEvaluationOutboundCalls(t *testing.T) {
	db := openEvaluationDB(t)
	profileID := insertEvaluationProfile(t, db)
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	store := NewStore(db, func() time.Time { return now })
	spent, err := store.ReserveBudget(context.Background(), profileID, 100, 39)
	if err != nil {
		t.Fatal(err)
	}
	if err := spent.Commit(context.Background(), 39); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, ServiceOptions{
		Now:    func() time.Time { return now },
		Sample: func(int) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	blocker := Job{
		ProfileID: profileID, SampleRateBPS: 10_000, DailyBudgetMicroUSD: 100,
		EstimatedCostMicroUSD: 1, MaxConcurrency: 1, QueueCapacity: 3,
		Timeout: time.Minute, ExpiresAt: now.Add(time.Minute),
		Run: func(context.Context) (Result, error) {
			close(started)
			<-release
			return Result{}, nil
		},
	}
	if got := service.Submit(blocker); got != SubmitAccepted {
		t.Fatalf("blocker Submit()=%q", got)
	}
	<-started

	var outboundCalls int
	worstCase := blocker
	worstCase.EstimatedCostMicroUSD = 90
	worstCase.Run = func(context.Context) (Result, error) {
		outboundCalls++
		return Result{SpentMicroUSD: 60}, nil
	}
	if got := service.Submit(worstCase); got != SubmitAccepted {
		t.Fatalf("evaluation Submit()=%q", got)
	}
	finished := make(chan struct{})
	sentinel := blocker
	sentinel.Run = func(context.Context) (Result, error) {
		close(finished)
		return Result{}, nil
	}
	if got := service.Submit(sentinel); got != SubmitAccepted {
		t.Fatalf("sentinel Submit()=%q", got)
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("sentinel did not run")
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if outboundCalls != 0 || service.Status().BudgetRejected != 1 {
		t.Fatalf("outbound=%d status=%+v", outboundCalls, service.Status())
	}
	snapshot, err := store.Budget(context.Background(), profileID, now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ReservedMicroUSD != 0 || snapshot.SpentMicroUSD != 39 {
		t.Fatalf("budget=%+v", snapshot)
	}
}

func TestCloseCancelsRunningEvaluationsAndRejectsNewJobs(t *testing.T) {
	db := openEvaluationDB(t)
	profileID := insertEvaluationProfile(t, db)
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	service, err := NewService(NewStore(db, func() time.Time { return now }), ServiceOptions{
		Now:    func() time.Time { return now },
		Sample: func(int) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	job := Job{
		ProfileID: profileID, SampleRateBPS: 10_000, DailyBudgetMicroUSD: 100,
		EstimatedCostMicroUSD: 10, MaxConcurrency: 1, QueueCapacity: 1,
		Timeout: time.Minute, ExpiresAt: now.Add(time.Minute),
		Run: func(ctx context.Context) (Result, error) {
			close(started)
			<-ctx.Done()
			return Result{}, ctx.Err()
		},
	}
	if got := service.Submit(job); got != SubmitAccepted {
		t.Fatalf("Submit()=%q", got)
	}
	<-started
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if got := service.Submit(job); got != SubmitClosed {
		t.Fatalf("Submit after Close=%q", got)
	}
	if status := service.Status(); status.Failed != 1 {
		t.Fatalf("status=%+v", status)
	}
}

func TestServiceRejectsJobsWithoutACompleteBoundedContract(t *testing.T) {
	db := openEvaluationDB(t)
	service, err := NewService(NewStore(db, time.Now), ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })

	if got := service.Submit(Job{}); got != SubmitInvalid {
		t.Fatalf("Submit()=%q", got)
	}
	if !errors.Is(validateJob(Job{}), ErrInvalidJob) {
		t.Fatalf("validateJob() error=%v", validateJob(Job{}))
	}
}
