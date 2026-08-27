package evalcatalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/Euphie/llm-proxy/internal/modelcatalog"
)

var (
	ErrUpdateInProgress = errors.New("evaluation catalog update is already running")
	ErrUpdaterClosed    = errors.New("evaluation catalog updater is closed")
)

type UpdateState string

const (
	UpdateStateIdle      UpdateState = "idle"
	UpdateStateRunning   UpdateState = "running"
	UpdateStateSucceeded UpdateState = "succeeded"
	UpdateStateFailed    UpdateState = "failed"
	UpdateStateCancelled UpdateState = "cancelled"
)

type SourceProgress struct {
	ID              string      `json:"id"`
	State           UpdateState `json:"state"`
	Stage           string      `json:"stage"`
	Imported        int         `json:"imported"`
	SkippedUnmapped int         `json:"skipped_unmapped"`
	Error           string      `json:"error,omitempty"`
}

type UpdateJob struct {
	ID               string           `json:"id,omitempty"`
	State            UpdateState      `json:"state"`
	PreviousRevision string           `json:"previous_revision,omitempty"`
	Revision         string           `json:"revision,omitempty"`
	Error            string           `json:"error,omitempty"`
	StartedAt        time.Time        `json:"started_at,omitempty"`
	FinishedAt       time.Time        `json:"finished_at,omitempty"`
	Changed          bool             `json:"changed"`
	Sources          []SourceProgress `json:"sources"`
}

type ModelCatalogProvider interface {
	Current() modelcatalog.Catalog
}

type SourceAdapter interface {
	ID() string
	Load(context.Context, Downloader, modelcatalog.Catalog, time.Time, string) (ImportedSource, error)
}

type Updater struct {
	mu         sync.RWMutex
	service    *Service
	models     ModelCatalogProvider
	downloader Downloader
	adapters   []SourceAdapter
	now        func() time.Time
	job        UpdateJob
	cancel     context.CancelFunc
	wait       sync.WaitGroup
	sequence   uint64
	closed     bool
}

func NewUpdater(
	service *Service,
	models ModelCatalogProvider,
	downloader Downloader,
	adapters []SourceAdapter,
	now func() time.Time,
) *Updater {
	if now == nil {
		now = time.Now
	}
	return &Updater{
		service: service, models: models, downloader: downloader,
		adapters: append([]SourceAdapter(nil), adapters...), now: now,
		job: UpdateJob{State: UpdateStateIdle},
	}
}

func (updater *Updater) Start() (UpdateJob, error) {
	updater.mu.Lock()
	defer updater.mu.Unlock()
	if updater.closed {
		return UpdateJob{}, ErrUpdaterClosed
	}
	if updater.job.State == UpdateStateRunning {
		return cloneUpdateJob(updater.job), ErrUpdateInProgress
	}
	if updater.service == nil || updater.models == nil || updater.downloader == nil || len(updater.adapters) != 5 {
		return UpdateJob{}, errors.New("evaluation catalog updater requires service, catalogs, downloader and five sources")
	}
	startedAt := updater.now().UTC()
	previous := updater.service.Current().Revision
	updater.sequence++
	sources := make([]SourceProgress, len(updater.adapters))
	for index, adapter := range updater.adapters {
		if adapter == nil || adapter.ID() == "" {
			return UpdateJob{}, errors.New("evaluation catalog updater source IDs are required")
		}
		sources[index] = SourceProgress{ID: adapter.ID(), State: UpdateStateIdle, Stage: "queued"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	updater.cancel = cancel
	updater.job = UpdateJob{
		ID:    fmt.Sprintf("evaluation-catalog-%d-%d", startedAt.UnixNano(), updater.sequence),
		State: UpdateStateRunning, PreviousRevision: previous, Revision: previous,
		StartedAt: startedAt, Sources: sources,
	}
	job := cloneUpdateJob(updater.job)
	updater.wait.Add(1)
	go updater.run(ctx, job.ID, startedAt)
	return job, nil
}

func (updater *Updater) Status() UpdateJob {
	updater.mu.RLock()
	defer updater.mu.RUnlock()
	return cloneUpdateJob(updater.job)
}

func (updater *Updater) Close() error {
	updater.mu.Lock()
	if updater.closed {
		updater.mu.Unlock()
		updater.wait.Wait()
		return nil
	}
	updater.closed = true
	if updater.cancel != nil {
		updater.cancel()
	}
	updater.mu.Unlock()
	updater.wait.Wait()
	return nil
}

func (updater *Updater) run(ctx context.Context, jobID string, retrievedAt time.Time) {
	defer updater.wait.Done()
	defer func() {
		updater.mu.Lock()
		if updater.job.ID == jobID {
			updater.cancel = nil
		}
		updater.mu.Unlock()
	}()
	directory, err := os.MkdirTemp("", "evaluation-catalog-update-*")
	if err != nil {
		updater.finish(jobID, UpdateStateFailed, "无法创建公开评测更新工作目录。", updater.job.Revision, false)
		return
	}
	defer os.RemoveAll(directory)

	models := updater.models.Current()
	type outcome struct {
		index int
		item  ImportedSource
		err   error
	}
	outcomes := make(chan outcome, len(updater.adapters))
	semaphore := make(chan struct{}, 3)
	var sources sync.WaitGroup
	for index, adapter := range updater.adapters {
		sources.Add(1)
		go func(index int, adapter SourceAdapter) {
			defer sources.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				updater.updateSource(jobID, index, UpdateStateCancelled, "cancelled", 0, 0, "更新已取消")
				outcomes <- outcome{index: index, err: ctx.Err()}
				return
			}
			updater.updateSource(jobID, index, UpdateStateRunning, "download_and_import", 0, 0, "")
			sourceCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			item, loadErr := adapter.Load(sourceCtx, updater.downloader, models, retrievedAt, directory)
			cancel()
			if loadErr != nil {
				log.Printf("evaluation catalog source %s failed: %v", adapter.ID(), loadErr)
				state := UpdateStateFailed
				message := "下载或解析失败"
				if errors.Is(loadErr, context.Canceled) && ctx.Err() != nil {
					state = UpdateStateCancelled
					message = "更新已取消"
				}
				updater.updateSource(jobID, index, state, "failed", 0, 0, message)
				outcomes <- outcome{index: index, err: loadErr}
				return
			}
			if item.Source.ID != adapter.ID() {
				loadErr = fmt.Errorf("adapter returned source %q", item.Source.ID)
				log.Printf("evaluation catalog source %s failed: %v", adapter.ID(), loadErr)
				updater.updateSource(jobID, index, UpdateStateFailed, "failed", 0, 0, "来源标识无效")
				outcomes <- outcome{index: index, err: loadErr}
				return
			}
			updater.updateSource(jobID, index, UpdateStateSucceeded, "completed", item.Imported, item.SkippedUnmapped, "")
			outcomes <- outcome{index: index, item: item}
		}(index, adapter)
	}
	sources.Wait()
	close(outcomes)

	items := make([]ImportedSource, len(updater.adapters))
	failedSource := ""
	for outcome := range outcomes {
		if outcome.err != nil && failedSource == "" {
			failedSource = updater.adapters[outcome.index].ID()
		}
		items[outcome.index] = outcome.item
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			updater.finish(jobID, UpdateStateCancelled, "公开评测更新已取消，仍在使用之前的数据。", updater.activeRevision(jobID), false)
		} else {
			updater.finish(jobID, UpdateStateFailed, "公开评测更新超时，仍在使用之前的数据。", updater.activeRevision(jobID), false)
		}
		return
	}
	if failedSource != "" {
		updater.finish(jobID, UpdateStateFailed, failedSource+" 更新失败，仍在使用之前的数据。", updater.activeRevision(jobID), false)
		return
	}
	next, _, err := AssembleCatalog(retrievedAt, items)
	if err != nil {
		log.Printf("assemble evaluation catalog: %v", err)
		updater.finish(jobID, UpdateStateFailed, "公开评测目录校验失败，仍在使用之前的数据。", updater.activeRevision(jobID), false)
		return
	}
	var encoded bytes.Buffer
	if err := EncodeCatalog(&encoded, next); err != nil {
		log.Printf("encode evaluation catalog: %v", err)
		updater.finish(jobID, UpdateStateFailed, "公开评测目录校验失败，仍在使用之前的数据。", updater.activeRevision(jobID), false)
		return
	}
	previous := updater.activeRevision(jobID)
	installed, err := updater.service.Replace(encoded.Bytes())
	if err != nil {
		log.Printf("persist evaluation catalog: %v", err)
		updater.finish(jobID, UpdateStateFailed, "公开评测目录保存失败，仍在使用之前的数据。", previous, false)
		return
	}
	updater.finish(jobID, UpdateStateSucceeded, "", installed.Revision, installed.Revision != previous)
}

func (updater *Updater) updateSource(
	jobID string,
	index int,
	state UpdateState,
	stage string,
	imported int,
	skipped int,
	message string,
) {
	updater.mu.Lock()
	defer updater.mu.Unlock()
	if updater.job.ID != jobID || index < 0 || index >= len(updater.job.Sources) {
		return
	}
	progress := &updater.job.Sources[index]
	progress.State = state
	progress.Stage = stage
	progress.Imported = imported
	progress.SkippedUnmapped = skipped
	progress.Error = message
}

func (updater *Updater) activeRevision(jobID string) string {
	updater.mu.RLock()
	defer updater.mu.RUnlock()
	if updater.job.ID != jobID {
		return ""
	}
	return updater.job.Revision
}

func (updater *Updater) finish(jobID string, state UpdateState, message, revision string, changed bool) {
	updater.mu.Lock()
	defer updater.mu.Unlock()
	if updater.job.ID != jobID {
		return
	}
	updater.job.State = state
	updater.job.Error = message
	updater.job.Revision = revision
	updater.job.Changed = changed
	updater.job.FinishedAt = updater.now().UTC()
}

func cloneUpdateJob(job UpdateJob) UpdateJob {
	cloned := job
	cloned.Sources = append([]SourceProgress(nil), job.Sources...)
	return cloned
}
