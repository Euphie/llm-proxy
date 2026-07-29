package vision

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/stats"
)

type processError struct {
	status int
	err    error
}

type processFailures struct {
	mu               sync.Mutex
	first            error
	firstNonCanceled error
}

func (f *processFailures) record(err error) {
	if err == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.first == nil {
		f.first = err
	}
	if f.firstNonCanceled == nil && !errors.Is(err, context.Canceled) {
		f.firstNonCanceled = err
	}
}

func (f *processFailures) err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.firstNonCanceled != nil {
		return f.firstNonCanceled
	}
	return f.first
}

func (e *processError) Error() string {
	return e.err.Error()
}

func (e *processError) Unwrap() error {
	return e.err
}

func HTTPStatus(err error) int {
	var target *processError
	if errors.As(err, &target) {
		return target.status
	}
	return http.StatusBadGateway
}

type Preprocessor struct {
	profile   string
	model     string
	prompt    string
	describer describer
	cache     *resultCache
	slots     chan struct{}
	timeout   time.Duration
	cacheKey  []byte
}

type processLoader struct {
	source  resultSource
	started chan context.Context
	done    chan struct{}
}

func New(cfg profile.Runtime, httpClient *http.Client, sdb *stats.DB) *Preprocessor {
	return newPreprocessor(
		cfg.Slug,
		cfg.Vision,
		newVisionClient(cfg, httpClient, sdb),
		newResultCache(cfg.Vision.CacheMaxEntries, cfg.Vision.CacheTTL),
	)
}

func newPreprocessor(
	profileSlug string,
	cfg profile.VisionRuntime,
	d describer,
	cache *resultCache,
) *Preprocessor {
	return &Preprocessor{
		profile:   profileSlug,
		model:     cfg.Model,
		prompt:    effectivePrompt(cfg.Prompt),
		describer: d,
		cache:     cache,
		slots:     make(chan struct{}, cfg.MaxConcurrency),
		timeout:   cfg.Timeout,
		cacheKey:  newCacheKey(),
	}
}

func newCacheKey() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(fmt.Sprintf("vision: generate cache key: %v", err))
	}
	return key
}

func (p *Preprocessor) Process(
	ctx context.Context,
	headers http.Header,
	body []byte,
) ([]byte, error) {
	doc, err := parseMessages(body)
	if err != nil {
		return nil, &processError{
			status: http.StatusBadRequest,
			err:    err,
		}
	}
	images := doc.images()
	if len(images) == 0 {
		return body, nil
	}
	processStarted := time.Now()
	slog.Info("vision.images.discovered",
		"profile", p.profile,
		"image_count", len(images))

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	descriptions := make([]string, len(images))
	loaders := make([]*processLoader, len(images))
	requestWaitersDone := make(chan struct{})
	var wg sync.WaitGroup
	var failures processFailures
	wg.Add(len(images))
	for i, image := range images {
		loader := &processLoader{
			started: make(chan context.Context, 1),
			done:    make(chan struct{}),
		}
		loaders[i] = loader
		go func() {
			defer wg.Done()
			imageStarted := time.Now()

			description, source, err := p.cache.getOrLoad(
				workCtx,
				scopedImageCacheKey(p.cacheKey, headers, p.profile, p.model, p.prompt, image),
				func(loadCtx context.Context) (string, error) {
					operationCtx, operationCancel := context.WithTimeout(loadCtx, p.timeout)
					defer operationCancel()
					loader.started <- operationCtx
					defer close(loader.done)
					if err := p.acquireSlot(workCtx, operationCtx, requestWaitersDone); err != nil {
						return "", err
					}
					defer func() { <-p.slots }()
					if err := operationCtx.Err(); err != nil {
						return "", err
					}
					description, err := p.describer.Describe(operationCtx, headers, image)
					if err != nil {
						failures.record(err)
						cancel()
					}
					return description, err
				},
			)
			loader.source = source
			cacheSource := cacheSourceName(source)
			slog.Info("vision.image.cache",
				"profile", p.profile,
				"image_index", i,
				"source_type", image.sourceType,
				"cache_source", cacheSource)
			if err == nil {
				descriptions[i] = description
				debugDescription, truncated := truncateDebugContent(description)
				slog.Info("vision.debug.description",
					"profile", p.profile,
					"image_index", i,
					"source_type", image.sourceType,
					"description", debugDescription,
					"truncated", truncated)
				slog.Info("vision.image.completed",
					"profile", p.profile,
					"image_index", i,
					"source_type", image.sourceType,
					"cache_source", cacheSource,
					"duration_ms", time.Since(imageStarted).Milliseconds(),
					"error_class", "none")
				return
			}

			slog.Warn("vision.image.failed",
				"profile", p.profile,
				"image_index", i,
				"source_type", image.sourceType,
				"cache_source", cacheSource,
				"duration_ms", time.Since(imageStarted).Milliseconds(),
				"error_class", visionErrorClass(err))
			failures.record(err)
			cancel()
		}()
	}
	wg.Wait()
	close(requestWaitersDone)
	waitForCanceledLoaders(loaders)

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := failures.err(); err != nil {
		return nil, &processError{
			status: http.StatusBadGateway,
			err:    fmt.Errorf("describe images: %w", err),
		}
	}

	rewritten, err := doc.rewrite(descriptions)
	if err != nil {
		return nil, &processError{
			status: http.StatusBadGateway,
			err:    fmt.Errorf("rewrite request: %w", err),
		}
	}
	slog.Info("vision.rewrite.completed",
		"profile", p.profile,
		"image_count", len(images),
		"duration_ms", time.Since(processStarted).Milliseconds())
	return rewritten, nil
}

func cacheSourceName(source resultSource) string {
	switch source {
	case sourceCache:
		return "cache"
	case sourceLoaded:
		return "loaded"
	case sourceShared:
		return "shared"
	default:
		return "unknown"
	}
}

func visionErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}

	var clientErr safeClientError
	if errors.As(err, &clientErr) {
		switch clientErr.message {
		case "build vision request":
			return "request"
		case "read vision response", "close vision response":
			return "response_io"
		case "invalid vision response":
			return "invalid_response"
		case "vision upstream request failed":
			return "network"
		}
	}
	return "upstream"
}

func waitForCanceledLoaders(loaders []*processLoader) {
	for _, loader := range loaders {
		if loader.source != sourceLoaded {
			continue
		}
		loadCtx := <-loader.started
		select {
		case <-loadCtx.Done():
			<-loader.done
		default:
		}
	}
}

func (p *Preprocessor) acquireSlot(
	requestCtx context.Context,
	loadCtx context.Context,
	requestWaitersDone <-chan struct{},
) error {
	select {
	case p.slots <- struct{}{}:
		return p.validateAcquiredSlot(requestCtx, loadCtx, requestWaitersDone)
	case <-loadCtx.Done():
		return loadCtx.Err()
	case <-requestCtx.Done():
	}

	select {
	case <-loadCtx.Done():
		return loadCtx.Err()
	case <-requestWaitersDone:
	}
	if err := loadCtx.Err(); err != nil {
		return err
	}

	select {
	case p.slots <- struct{}{}:
		if err := loadCtx.Err(); err != nil {
			<-p.slots
			return err
		}
		return nil
	case <-loadCtx.Done():
		return loadCtx.Err()
	}
}

func (p *Preprocessor) validateAcquiredSlot(
	requestCtx context.Context,
	loadCtx context.Context,
	requestWaitersDone <-chan struct{},
) error {
	if requestCtx.Err() != nil {
		select {
		case <-loadCtx.Done():
		case <-requestWaitersDone:
		}
	}
	if err := loadCtx.Err(); err != nil {
		<-p.slots
		return err
	}
	return nil
}
