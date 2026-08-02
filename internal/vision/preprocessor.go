package vision

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Euphie/llm-proxy/internal/llmrequest"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/provider"
	"github.com/Euphie/llm-proxy/internal/stats"
)

type processError struct {
	status int
	err    error
}

type processFailures struct {
	mu       sync.Mutex
	selected processFailure
	hasError bool
}

type processFailure struct {
	err      error
	image    int
	priority int
}

const (
	processFailureCanceled = iota
	processFailureTransient
	processFailureHard
)

func (f *processFailures) record(image int, err error) bool {
	if err == nil {
		return false
	}
	priority := processFailurePriority(err)
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hasError || priority > f.selected.priority ||
		(priority == f.selected.priority && image < f.selected.image) {
		f.selected = processFailure{err: err, image: image, priority: priority}
		f.hasError = true
	}
	return priority == processFailureHard
}

func (f *processFailures) err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hasError {
		return nil
	}
	return f.selected.err
}

func processFailurePriority(err error) int {
	if class, ok := provider.FailureClassOf(err); ok {
		switch class {
		case provider.FailureOverloadTransient,
			provider.FailureOperationTimeout,
			provider.FailureUnknownTransport,
			provider.FailureMalformedResponse:
			return processFailureTransient
		default:
			return processFailureHard
		}
	}
	if errors.Is(err, context.Canceled) {
		return processFailureCanceled
	}
	return processFailureHard
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

type requestDocument interface {
	images() []imageRef
	rewrite([]string) ([]byte, error)
}

type Preprocessor struct {
	profile             string
	transport           profile.VisionTransport
	model               string
	models              profile.ModelCatalog
	unlistedModelPolicy profile.UnlistedModelPolicy
	prompt              string
	defaultTarget       string
	parse               func(map[string]json.RawMessage) (requestDocument, error)
	headers             []string
	describer           describer
	cache               *resultCache
	slots               chan struct{}
	timeout             time.Duration
	cacheKey            []byte
}

type processLoader struct {
	source  resultSource
	started chan context.Context
	done    chan struct{}
}

func New(cfg profile.Runtime, httpClient *http.Client, sdb *stats.DB) *Preprocessor {
	client := newVisionClient(cfg, httpClient, sdb)
	preprocessor := newPreprocessorForProtocol(
		cfg.Protocol,
		cfg.Slug,
		cfg.Models,
		cfg.Vision,
		client,
		newResultCache(cfg.Vision.CacheMaxEntries, cfg.Vision.CacheTTL),
	)
	preprocessor.defaultTarget = client.upstream
	return preprocessor
}

func newPreprocessor(
	profileSlug string,
	cfg profile.VisionRuntime,
	d describer,
	cache *resultCache,
) *Preprocessor {
	return newPreprocessorForProtocol(
		profile.ProtocolAnthropic,
		profileSlug,
		nil,
		cfg,
		d,
		cache,
	)
}

func newPreprocessorForProtocol(
	protocol profile.Protocol,
	profileSlug string,
	models profile.ModelCatalog,
	cfg profile.VisionRuntime,
	d describer,
	cache *resultCache,
) *Preprocessor {
	if cfg.Transport == "" {
		cfg.Transport = profile.DefaultVisionTransport(protocol)
	}
	parse := func(root map[string]json.RawMessage) (requestDocument, error) {
		return parseMessagesRoot(root)
	}
	if protocol == profile.ProtocolOpenAI {
		parse = func(root map[string]json.RawMessage) (requestDocument, error) {
			return parseResponsesRoot(root)
		}
	}
	return &Preprocessor{
		profile:             profileSlug,
		transport:           cfg.Transport,
		model:               cfg.Model,
		models:              models,
		unlistedModelPolicy: cfg.UnlistedModelPolicy,
		prompt:              effectivePrompt(cfg.Prompt),
		defaultTarget:       "https://vision.invalid/v1/messages",
		parse:               parse,
		headers:             forwardedHeadersForProtocol(protocol),
		describer:           d,
		cache:               cache,
		slots:               make(chan struct{}, cfg.MaxConcurrency),
		timeout:             cfg.Timeout,
		cacheKey:            newCacheKey(),
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
	return p.ProcessTarget(ctx, headers, body, p.defaultTarget)
}

func (p *Preprocessor) ProcessTarget(
	ctx context.Context,
	headers http.Header,
	body []byte,
	mainTarget string,
) ([]byte, error) {
	return p.ProcessTargetWithBudget(ctx, headers, body, mainTarget, nil)
}

func (p *Preprocessor) ProcessTargetWithBudget(
	ctx context.Context,
	headers http.Header,
	body []byte,
	mainTarget string,
	reserveCall func(context.Context) error,
) ([]byte, error) {
	return p.processTargetWithBudget(
		ctx, headers, body, mainTarget, legacyCallReservation(reserveCall), p.parse,
	)
}

func (p *Preprocessor) ProcessOperationTargetWithBudget(
	ctx context.Context,
	headers http.Header,
	operation llmrequest.Operation,
	body []byte,
	mainTarget string,
	reserveCall func(context.Context) error,
) ([]byte, error) {
	return p.ProcessOperationTargetWithTickets(
		ctx, headers, operation, body, mainTarget, legacyCallReservation(reserveCall),
	)
}

func (p *Preprocessor) ProcessOperationTargetWithTickets(
	ctx context.Context,
	headers http.Header,
	operation llmrequest.Operation,
	body []byte,
	mainTarget string,
	reserveCall CallReservation,
) ([]byte, error) {
	parse, err := requestParser(operation)
	if err != nil {
		return nil, &processError{status: http.StatusBadRequest, err: err}
	}
	return p.processTargetWithBudget(ctx, headers, body, mainTarget, reserveCall, parse)
}

func legacyCallReservation(reserve func(context.Context) error) CallReservation {
	if reserve == nil {
		return nil
	}
	return func(ctx context.Context, _, _ int) (CallCompletion, error) {
		if err := reserve(ctx); err != nil {
			return nil, err
		}
		return func(int, string, []byte) {}, nil
	}
}

func requestParser(
	operation llmrequest.Operation,
) (func(map[string]json.RawMessage) (requestDocument, error), error) {
	switch operation {
	case llmrequest.OperationAnthropicMessages:
		return func(root map[string]json.RawMessage) (requestDocument, error) {
			return parseMessagesRoot(root)
		}, nil
	case llmrequest.OperationOpenAIChatCompletions:
		return func(root map[string]json.RawMessage) (requestDocument, error) {
			return parseChatCompletionsRoot(root)
		}, nil
	case llmrequest.OperationOpenAIResponses:
		return func(root map[string]json.RawMessage) (requestDocument, error) {
			return parseResponsesRoot(root)
		}, nil
	default:
		return nil, fmt.Errorf("unsupported inference operation %q", operation)
	}
}

func (p *Preprocessor) processTargetWithBudget(
	ctx context.Context,
	headers http.Header,
	body []byte,
	mainTarget string,
	reserveCall CallReservation,
	parse func(map[string]json.RawMessage) (requestDocument, error),
) ([]byte, error) {
	ctx = ensureRequestTrace(ctx)
	requestTrace := requestTraceID(ctx)
	root, err := parseRequestRoot(body)
	if err != nil {
		return nil, &processError{
			status: http.StatusBadRequest,
			err:    err,
		}
	}
	model, ok := requestModel(root)
	if !ok {
		slog.Debug("vision.skipped", "profile", p.profile,
			"request_trace_id", requestTrace, "reason", "invalid_model")
		return body, nil
	}
	enhance, reason := p.shouldEnhance(model)
	if !enhance {
		slog.Debug("vision.skipped", "profile", p.profile,
			"request_trace_id", requestTrace, "model", model, "reason", reason)
		return body, nil
	}
	doc, err := parse(root)
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
	if p.transport == profile.VisionTransportOpenAIChatCompletions {
		for _, image := range images {
			if image.sourceType == "file" {
				cause := unsupportedImageSourceError{
					transport: p.transport,
					source:    "file_id",
				}
				return nil, &processError{
					status: http.StatusBadRequest,
					err:    cause,
				}
			}
		}
	}
	processStarted := time.Now()
	slog.Info("vision.images.discovered",
		"profile", p.profile,
		"transport", p.transport,
		"request_trace_id", requestTrace,
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
			imagePrompt := promptForImage(p.prompt, image)

			description, source, loadTrace, err := p.cache.getOrLoadTrace(
				workCtx,
				scopedImageCacheKey(
					p.cacheKey,
					headers,
					p.headers,
					p.profile,
					string(p.transport)+"\x00"+p.model,
					imagePrompt,
					image,
				),
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
					operationCtx = withVisionCallReservation(operationCtx, i, reserveCall)
					var complete CallCompletion
					if reserveCall != nil {
						if _, manages := p.describer.(interface{ managesCallReservation() }); !manages {
							var reserveErr error
							complete, reserveErr = reserveCall(operationCtx, i, 0)
							if reserveErr != nil {
								failures.record(i, reserveErr)
								cancel()
								return "", reserveErr
							}
						}
					}
					description, err := p.describer.DescribeTarget(
						operationCtx,
						headers,
						mainTarget,
						image,
					)
					if complete != nil {
						if err != nil {
							complete(0, "failed", nil)
						} else {
							complete(http.StatusOK, "success", nil)
						}
					}
					if err != nil {
						if failures.record(i, err) {
							cancel()
						}
					}
					return description, err
				},
			)
			loader.source = source
			cacheSource := cacheSourceName(source)
			slog.Info("vision.image.cache",
				"profile", p.profile,
				"transport", p.transport,
				"request_trace_id", requestTrace,
				"owner_trace_id", loadTrace.ownerTraceID,
				"call_id", loadTrace.callID,
				"image_index", i,
				"source_type", image.sourceType,
				"cache_source", cacheSource)
			if err == nil {
				descriptions[i] = description
				slog.Info("vision.image.completed",
					"profile", p.profile,
					"transport", p.transport,
					"request_trace_id", requestTrace,
					"owner_trace_id", loadTrace.ownerTraceID,
					"call_id", loadTrace.callID,
					"image_index", i,
					"source_type", image.sourceType,
					"cache_source", cacheSource,
					"duration_ms", time.Since(imageStarted).Milliseconds(),
					"error_class", "none")
				return
			}

			slog.Warn("vision.image.failed",
				"profile", p.profile,
				"transport", p.transport,
				"request_trace_id", requestTrace,
				"owner_trace_id", loadTrace.ownerTraceID,
				"call_id", loadTrace.callID,
				"image_index", i,
				"source_type", image.sourceType,
				"cache_source", cacheSource,
				"duration_ms", time.Since(imageStarted).Milliseconds(),
				"error_class", visionErrorClass(err))
			if failures.record(i, err) {
				cancel()
			}
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
	unhandledImageCount, err := p.countImages(rewritten, parse)
	if err != nil {
		return nil, &processError{
			status: http.StatusBadGateway,
			err:    fmt.Errorf("validate rewritten request: %w", err),
		}
	}
	if unhandledImageCount > 0 {
		slog.Warn("vision.rewrite.incomplete",
			"profile", p.profile,
			"transport", p.transport,
			"request_trace_id", requestTrace,
			"unhandled_image_count", unhandledImageCount)
		return nil, &processError{
			status: http.StatusBadGateway,
			err: fmt.Errorf(
				"rewrite request left %d image blocks unhandled",
				unhandledImageCount,
			),
		}
	}
	slog.Info("vision.rewrite.completed",
		"profile", p.profile,
		"transport", p.transport,
		"request_trace_id", requestTrace,
		"image_count", len(images),
		"unhandled_image_count", unhandledImageCount,
		"duration_ms", time.Since(processStarted).Milliseconds())
	return rewritten, nil
}

func (p *Preprocessor) countImages(
	body []byte,
	parse func(map[string]json.RawMessage) (requestDocument, error),
) (int, error) {
	root, err := parseRequestRoot(body)
	if err != nil {
		return 0, err
	}
	doc, err := parse(root)
	if err != nil {
		return 0, err
	}
	return len(doc.images()), nil
}

func (p *Preprocessor) shouldEnhance(model string) (bool, string) {
	if capability, ok := p.models.Lookup(model); ok {
		if capability.SupportsVision {
			return false, "native_vision"
		}
		return true, "listed_text_only"
	}
	if p.unlistedModelPolicy == profile.UnlistedModelEnhance {
		return true, "unlisted_enhance"
	}
	return false, "unlisted_bypass"
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
