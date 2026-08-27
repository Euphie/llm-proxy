package evalcatalog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/modelcatalog"
)

func TestUpdaterRunsFiveSourcesWithMaximumConcurrencyThreeAndReplacesAtomically(t *testing.T) {
	service, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	previous := service.Current().Revision
	release := make(chan struct{})
	started := make(chan struct{}, 5)
	var concurrency struct {
		sync.Mutex
		current int
		maximum int
	}
	adapters := testAdapters(func(ctx context.Context, id string, now time.Time) (ImportedSource, error) {
		concurrency.Lock()
		concurrency.current++
		concurrency.maximum = max(concurrency.maximum, concurrency.current)
		concurrency.Unlock()
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return ImportedSource{}, ctx.Err()
		}
		concurrency.Lock()
		concurrency.current--
		concurrency.Unlock()
		return testImportedSource(id, now), nil
	})
	updater := NewUpdater(service, staticUpdaterModels{}, unusedDownloader{}, adapters, fixedUpdaterTime)
	t.Cleanup(func() { _ = updater.Close() })

	job, err := updater.Start()
	if err != nil {
		t.Fatal(err)
	}
	if job.State != UpdateStateRunning || service.Current().Revision != previous {
		t.Fatalf("job=%+v revision=%s", job, service.Current().Revision)
	}
	if _, err := updater.Start(); !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("second Start error=%v", err)
	}
	for index := 0; index < 3; index++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("three adapters did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("more than three adapters ran concurrently")
	case <-time.After(20 * time.Millisecond):
	}
	if service.Current().Revision != previous {
		t.Fatal("catalog changed before every source succeeded")
	}
	close(release)
	completed := waitForUpdateTerminal(t, updater)
	if completed.State != UpdateStateSucceeded || !completed.Changed || completed.Revision == previous {
		t.Fatalf("completed=%+v", completed)
	}
	concurrency.Lock()
	maximum := concurrency.maximum
	concurrency.Unlock()
	if maximum != 3 || len(service.Current().Sources) != 5 {
		t.Fatalf("maximum=%d catalog=%+v", maximum, service.Current())
	}
}

func TestUpdaterRetainsPreviousCatalogWhenAnySourceFails(t *testing.T) {
	service, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	previous := service.Current().Revision
	adapters := testAdapters(func(_ context.Context, id string, now time.Time) (ImportedSource, error) {
		if id == "source-2" {
			return ImportedSource{}, errors.New("secret upstream response")
		}
		return testImportedSource(id, now), nil
	})
	updater := NewUpdater(service, staticUpdaterModels{}, unusedDownloader{}, adapters, fixedUpdaterTime)
	t.Cleanup(func() { _ = updater.Close() })
	if _, err := updater.Start(); err != nil {
		t.Fatal(err)
	}
	job := waitForUpdateTerminal(t, updater)
	if job.State != UpdateStateFailed || service.Current().Revision != previous {
		t.Fatalf("job=%+v revision=%s", job, service.Current().Revision)
	}
	if job.Error == "" || contains(job.Error, "secret") {
		t.Fatalf("public error leaked details: %q", job.Error)
	}
}

func TestUpdaterReportsIdenticalEvidenceAsUnchanged(t *testing.T) {
	service, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	adapters := testAdapters(func(_ context.Context, id string, now time.Time) (ImportedSource, error) {
		return testImportedSource(id, now), nil
	})
	currentTime := fixedUpdaterTime()
	updater := NewUpdater(service, staticUpdaterModels{}, unusedDownloader{}, adapters, func() time.Time {
		currentTime = currentTime.Add(time.Hour)
		return currentTime
	})
	t.Cleanup(func() { _ = updater.Close() })
	if _, err := updater.Start(); err != nil {
		t.Fatal(err)
	}
	first := waitForUpdateTerminal(t, updater)
	if !first.Changed {
		t.Fatalf("first=%+v", first)
	}
	if _, err := updater.Start(); err != nil {
		t.Fatal(err)
	}
	second := waitForUpdateTerminal(t, updater)
	if second.State != UpdateStateSucceeded || second.Changed || second.Revision != first.Revision {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestUpdaterCloseCancelsBackgroundJob(t *testing.T) {
	service, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 5)
	finished := make(chan struct{}, 5)
	adapters := testAdapters(func(ctx context.Context, _ string, _ time.Time) (ImportedSource, error) {
		started <- struct{}{}
		<-ctx.Done()
		finished <- struct{}{}
		return ImportedSource{}, ctx.Err()
	})
	updater := NewUpdater(service, staticUpdaterModels{}, unusedDownloader{}, adapters, fixedUpdaterTime)
	if _, err := updater.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("adapter did not start")
	}
	if err := updater.Close(); err != nil {
		t.Fatal(err)
	}
	job := updater.Status()
	if job.State != UpdateStateCancelled {
		t.Fatalf("job=%+v", job)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("adapter goroutine did not finish")
	}
}

type testSourceAdapter struct {
	id   string
	load func(context.Context, string, time.Time) (ImportedSource, error)
}

func (adapter testSourceAdapter) ID() string { return adapter.id }

func (adapter testSourceAdapter) Load(ctx context.Context, _ Downloader, _ modelcatalog.Catalog, now time.Time, _ string) (ImportedSource, error) {
	return adapter.load(ctx, adapter.id, now)
}

func testAdapters(load func(context.Context, string, time.Time) (ImportedSource, error)) []SourceAdapter {
	adapters := make([]SourceAdapter, 5)
	for index := range adapters {
		adapters[index] = testSourceAdapter{id: fmt.Sprintf("source-%d", index), load: load}
	}
	return adapters
}

func testImportedSource(id string, now time.Time) ImportedSource {
	digest := fmt.Sprintf("%064x", len(id)+1)
	settings := fmt.Sprintf("%064x", len(id)+100)
	return ImportedSource{
		Source: Source{
			ID: id, Name: id, URL: "https://example.test/" + id, License: "MIT",
			Version: "v1", RetrievedAt: now, SHA256: digest,
		},
		Results: []Result{{
			SourceID: id, Benchmark: "overall", Domain: "general", ModelID: "acme/alpha",
			Metric: MetricBounded, ScoreBPS: 8000, LowerBPS: 7800, UpperBPS: 8200,
			Samples: 100, SettingsSHA256: settings,
		}},
		Imported: 1,
	}
}

type staticUpdaterModels struct{}

func (staticUpdaterModels) Current() modelcatalog.Catalog {
	return modelcatalog.Catalog{Models: []modelcatalog.Model{{ID: "alpha", CanonicalID: "acme/alpha"}}}
}

type unusedDownloader struct{}

func (unusedDownloader) Download(context.Context, string, DownloadRequest) (Download, error) {
	return Download{}, errors.New("unused")
}

func fixedUpdaterTime() time.Time {
	return time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
}

func waitForUpdateTerminal(t *testing.T, updater *Updater) UpdateJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job := updater.Status()
		if job.State != UpdateStateRunning {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("update did not finish: %+v", updater.Status())
	return UpdateJob{}
}

func contains(value, part string) bool {
	for index := 0; index+len(part) <= len(value); index++ {
		if value[index:index+len(part)] == part {
			return true
		}
	}
	return false
}
