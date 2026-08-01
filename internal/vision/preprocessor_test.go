package vision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/profile"
)

type fakeDescriber struct {
	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
	describe  func(imageRef) (string, error)
}

type incompleteRewriteDocument struct {
	body  []byte
	image imageRef
}

func (d incompleteRewriteDocument) images() []imageRef {
	return []imageRef{d.image}
}

func (d incompleteRewriteDocument) rewrite([]string) ([]byte, error) {
	return d.body, nil
}

func (f *fakeDescriber) DescribeTarget(
	_ context.Context,
	_ http.Header,
	_ string,
	image imageRef,
) (string, error) {
	f.mu.Lock()
	f.calls++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	return f.describe(image)
}

func (f *fakeDescriber) snapshot() (calls, active, maxActive int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.active, f.maxActive
}

func TestPreprocessorPassesThroughRequestWithoutImages(t *testing.T) {
	body := []byte("{\n  \"model\":\"main\", \"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]\n}")
	fake := &fakeDescriber{describe: func(imageRef) (string, error) {
		t.Fatal("Describe called for request without images")
		return "", nil
	}}
	p := testPreprocessor(2, fake)

	got, err := p.Process(context.Background(), nil, body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body changed:\n got: %s\nwant: %s", got, body)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("Describe calls=%d, want 0", calls)
	}
}

func TestPreprocessorReservesBudgetOnlyForActualVisionCalls(t *testing.T) {
	fake := &fakeDescriber{describe: func(imageRef) (string, error) {
		return "description", nil
	}}
	p := testPreprocessor(2, fake)
	var reservations atomic.Int32
	reserve := func(context.Context) error {
		reservations.Add(1)
		return nil
	}
	body := imageRequest("cached")

	if _, err := p.ProcessTargetWithBudget(context.Background(), nil, body, "https://example.test/v1/messages", reserve); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ProcessTargetWithBudget(context.Background(), nil, body, "https://example.test/v1/messages", reserve); err != nil {
		t.Fatal(err)
	}

	if reservations.Load() != 1 {
		t.Fatalf("budget reservations=%d, want 1", reservations.Load())
	}
	if calls, _, _ := fake.snapshot(); calls != 1 {
		t.Fatalf("vision calls=%d, want 1", calls)
	}
}

func TestPreprocessorStopsBeforeVisionCallWhenBudgetReservationFails(t *testing.T) {
	fake := &fakeDescriber{describe: func(imageRef) (string, error) {
		t.Fatal("DescribeTarget called after budget failure")
		return "", nil
	}}
	p := testPreprocessor(1, fake)
	want := errors.New("budget exhausted")

	_, err := p.ProcessTargetWithBudget(
		context.Background(),
		nil,
		imageRequest("blocked"),
		"https://example.test/v1/messages",
		func(context.Context) error { return want },
	)
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want budget error", err)
	}
	if calls, _, _ := fake.snapshot(); calls != 0 {
		t.Fatalf("vision calls=%d, want 0", calls)
	}
}

func TestPreprocessorReservesEveryRetriedVisionCall(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":"overloaded"}`)
			return
		}
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"description"}]}`)
	}))
	defer server.Close()

	p := budgetedRetryPreprocessor(server)
	var reservations atomic.Int32
	_, err := p.ProcessTargetWithBudget(
		context.Background(),
		nil,
		imageRequest("retry-budget"),
		server.URL+"/v1/messages",
		func(context.Context) error {
			reservations.Add(1)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || reservations.Load() != 2 {
		t.Fatalf("calls=%d reservations=%d, want 2 each", calls.Load(), reservations.Load())
	}
}

func TestPreprocessorStopsBeforeUnbudgetedVisionRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":"overloaded"}`)
	}))
	defer server.Close()

	p := budgetedRetryPreprocessor(server)
	want := errors.New("vision retry budget exhausted")
	var reservations atomic.Int32
	_, err := p.ProcessTargetWithBudget(
		context.Background(),
		nil,
		imageRequest("blocked-retry-budget"),
		server.URL+"/v1/messages",
		func(context.Context) error {
			if reservations.Add(1) > 1 {
				return want
			}
			return nil
		},
	)
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want retry budget error", err)
	}
	if calls.Load() != 1 || reservations.Load() != 2 {
		t.Fatalf("calls=%d reservations=%d, want 1 call and 2 reservations", calls.Load(), reservations.Load())
	}
}

func budgetedRetryPreprocessor(server *httptest.Server) *Preprocessor {
	cfg := testVisionConfig(server.URL)
	cfg.Models = profile.ModelCatalog{
		"main": {ID: "main", SupportsVision: false},
	}
	cfg.Vision.MaxConcurrency = 1
	p := New(cfg, server.Client(), nil)
	p.cache = newResultCache(8, time.Minute)
	p.describer.(*visionClient).sleep = func(context.Context, time.Duration) error {
		return nil
	}
	return p
}

func TestPreprocessorRejectsIncompleteRewrite(t *testing.T) {
	body := imageRequest("unhandled")
	fake := &fakeDescriber{describe: func(imageRef) (string, error) {
		return "description", nil
	}}
	p := testPreprocessor(1, fake)
	p.parse = func(map[string]json.RawMessage) (requestDocument, error) {
		return incompleteRewriteDocument{
			body: body,
			image: imageRef{
				sourceType:   "url",
				imageURL:     "https://example.test/unhandled.png",
				cachePayload: "unhandled",
			},
		}, nil
	}

	_, err := p.Process(context.Background(), nil, body)
	if err == nil || HTTPStatus(err) != http.StatusBadGateway ||
		!strings.Contains(err.Error(), "left 1 image blocks unhandled") {
		t.Fatalf("error=%v, want incomplete rewrite bad gateway", err)
	}
}

func TestPreprocessorGatesEnhancementByMainModel(t *testing.T) {
	nativeCatalog := profile.ModelCatalog{
		"Kimi-K2.5": {ID: "Kimi-K2.5", SupportsVision: true},
	}
	textCatalog := profile.ModelCatalog{
		"GLM-5": {ID: "GLM-5", SupportsVision: false},
	}
	tests := []struct {
		name          string
		requestModel  any
		models        profile.ModelCatalog
		policy        profile.UnlistedModelPolicy
		wantDescribe  int
		wantSameBytes bool
	}{
		{"listed native vision", "Kimi-K2.5", nativeCatalog, profile.UnlistedModelEnhance, 0, true},
		{"listed needs enhancement", "GLM-5", textCatalog, profile.UnlistedModelBypass, 1, false},
		{"unlisted bypass", "unknown", textCatalog, profile.UnlistedModelBypass, 0, true},
		{"unlisted enhance", "unknown", textCatalog, profile.UnlistedModelEnhance, 1, false},
		{"case mismatch follows unlisted policy", "glm-5", textCatalog, profile.UnlistedModelBypass, 0, true},
		{"missing model bypasses", nil, textCatalog, profile.UnlistedModelEnhance, 0, true},
		{"non-string model bypasses", 42, textCatalog, profile.UnlistedModelEnhance, 0, true},
		{"empty model bypasses", "", textCatalog, profile.UnlistedModelEnhance, 0, true},
	}
	protocols := []struct {
		name     string
		protocol profile.Protocol
		body     func(any) []byte
	}{
		{"messages", profile.ProtocolAnthropic, anthropicGateRequest},
		{"responses", profile.ProtocolOpenAI, responsesGateRequest},
	}

	for _, protocol := range protocols {
		t.Run(protocol.name, func(t *testing.T) {
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					fake := &fakeDescriber{describe: func(imageRef) (string, error) {
						return "description", nil
					}}
					p := New(profile.Runtime{
						Slug:     "provider",
						Protocol: protocol.protocol,
						Models:   test.models,
						Vision: profile.VisionRuntime{
							Model:               "vision-model",
							UnlistedModelPolicy: test.policy,
							Prompt:              "describe",
							Timeout:             time.Second,
							MaxConcurrency:      2,
						},
					}, nil, nil)
					p.describer = fake
					p.cache = newResultCache(32, time.Minute)
					body := protocol.body(test.requestModel)

					got, err := p.Process(context.Background(), nil, body)
					if err != nil {
						t.Fatal(err)
					}
					if calls, _, _ := fake.snapshot(); calls != test.wantDescribe {
						t.Fatalf("Describe calls=%d, want %d", calls, test.wantDescribe)
					}
					if same := bytes.Equal(got, body); same != test.wantSameBytes {
						t.Fatalf("byte-identical=%v, want %v\ngot:  %s\nwant: %s", same, test.wantSameBytes, got, body)
					}
				})
			}
		})
	}
}

func TestPreprocessorBypassDoesNotValidateImages(t *testing.T) {
	tests := []struct {
		name     string
		protocol profile.Protocol
		body     []byte
	}{
		{
			"messages",
			profile.ProtocolAnthropic,
			[]byte(`{ "model":"native", "messages":[{"content":[{"type":"image","source":{"type":"url"}}]}] }`),
		},
		{
			"responses",
			profile.ProtocolOpenAI,
			[]byte(`{ "model":"native", "input":[{"role":"user","content":[{"type":"input_image"}]}] }`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeDescriber{describe: func(imageRef) (string, error) {
				t.Fatal("Describe called for bypassed request")
				return "", nil
			}}
			p := New(profile.Runtime{
				Slug:     "provider",
				Protocol: test.protocol,
				Models: profile.ModelCatalog{
					"native": {ID: "native", SupportsVision: true},
				},
				Vision: profile.VisionRuntime{
					Model:               "vision-model",
					UnlistedModelPolicy: profile.UnlistedModelEnhance,
					Timeout:             time.Second,
					MaxConcurrency:      1,
				},
			}, nil, nil)
			p.describer = fake
			p.cache = nil

			got, err := p.Process(context.Background(), nil, test.body)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, test.body) {
				t.Fatalf("body changed:\n got: %s\nwant: %s", got, test.body)
			}
		})
	}
}

func TestPreprocessorRejectsMalformedTopLevelJSONBeforeModelGate(t *testing.T) {
	for _, protocol := range []profile.Protocol{
		profile.ProtocolAnthropic,
		profile.ProtocolOpenAI,
	} {
		t.Run(string(protocol), func(t *testing.T) {
			p := New(profile.Runtime{
				Slug:     "provider",
				Protocol: protocol,
				Vision: profile.VisionRuntime{
					Model:               "vision-model",
					UnlistedModelPolicy: profile.UnlistedModelBypass,
					Timeout:             time.Second,
					MaxConcurrency:      1,
				},
			}, nil, nil)
			got, err := p.Process(context.Background(), nil, []byte(`{"model":`))
			if got != nil {
				t.Fatalf("body=%s, want nil", got)
			}
			if status := HTTPStatus(err); status != http.StatusBadRequest {
				t.Fatalf("status=%d err=%v", status, err)
			}
		})
	}
}

func TestPreprocessorPreservesImageOrderAcrossConcurrentCompletion(t *testing.T) {
	thirdDone := make(chan struct{})
	secondDone := make(chan struct{})
	fake := &fakeDescriber{describe: func(image imageRef) (string, error) {
		switch image.cachePayload {
		case "first":
			<-secondDone
			return "description one", nil
		case "second":
			<-thirdDone
			close(secondDone)
			return "description two", nil
		case "third":
			close(thirdDone)
			return "description three", nil
		default:
			return "", errors.New("unexpected image")
		}
	}}
	p := testPreprocessor(3, fake)

	got, err := p.Process(context.Background(), nil, imageRequest("first", "second", "third"))
	if err != nil {
		t.Fatal(err)
	}
	if descriptions := rewrittenDescriptions(t, got); !equalStrings(descriptions, []string{
		"description one",
		"description two",
		"description three",
	}) {
		t.Fatalf("descriptions=%q", descriptions)
	}
}

func TestPreprocessorLimitsConcurrency(t *testing.T) {
	started := make(chan struct{}, 5)
	release := make(chan struct{})
	fake := &fakeDescriber{describe: func(imageRef) (string, error) {
		started <- struct{}{}
		<-release
		return "description", nil
	}}
	p := testPreprocessor(2, fake)
	result := make(chan error, 1)
	go func() {
		_, err := p.Process(context.Background(), nil, imageRequest("1", "2", "3", "4", "5"))
		result <- err
	}()

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for two active descriptions")
		}
	}
	select {
	case <-started:
		t.Fatal("third description started before a slot was released")
	default:
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if calls, _, maxActive := fake.snapshot(); calls != 5 || maxActive > 2 {
		t.Fatalf("calls=%d maxActive=%d", calls, maxActive)
	}
}

func TestPreprocessorSharesConcurrencyLimitAcrossProcessCalls(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	fake := &fakeDescriber{describe: func(imageRef) (string, error) {
		started <- struct{}{}
		<-release
		return "description", nil
	}}
	p := testPreprocessor(2, fake)
	results := make(chan error, 2)
	for _, body := range [][]byte{
		imageRequest("a", "b"),
		imageRequest("c", "d"),
	} {
		go func() {
			_, err := p.Process(context.Background(), nil, body)
			results <- err
		}()
	}

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for two active descriptions")
		}
	}
	select {
	case <-started:
		t.Fatal("global concurrency limit was exceeded")
	default:
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if _, _, maxActive := fake.snapshot(); maxActive > 2 {
		t.Fatalf("maxActive=%d", maxActive)
	}
}

func TestPreprocessorCoalescesIdenticalImages(t *testing.T) {
	fake := &fakeDescriber{describe: func(imageRef) (string, error) {
		return "same description", nil
	}}
	p := testPreprocessor(2, fake)

	got, err := p.Process(context.Background(), nil, imageRequest("same", "same"))
	if err != nil {
		t.Fatal(err)
	}
	if calls, _, _ := fake.snapshot(); calls != 1 {
		t.Fatalf("Describe calls=%d, want 1", calls)
	}
	if descriptions := rewrittenDescriptions(t, got); !equalStrings(descriptions, []string{
		"same description",
		"same description",
	}) {
		t.Fatalf("descriptions=%q", descriptions)
	}
}

func TestPreprocessorContextPartitionsSequentialCache(t *testing.T) {
	var (
		mu       sync.Mutex
		contexts []string
	)
	fake := &fakeDescriber{describe: func(image imageRef) (string, error) {
		mu.Lock()
		contexts = append(contexts, image.taskContext)
		mu.Unlock()
		return "description for " + image.taskContext, nil
	}}
	p := testPreprocessor(2, fake)
	first := contextualFileImageRequest("file-shared", "问题 A")
	second := contextualFileImageRequest("file-shared", "问题 B")

	for _, body := range [][]byte{first, first, second} {
		if _, err := p.Process(context.Background(), nil, body); err != nil {
			t.Fatal(err)
		}
	}

	if calls, _, _ := fake.snapshot(); calls != 2 {
		t.Fatalf("Describe calls=%d, want 2", calls)
	}
	mu.Lock()
	defer mu.Unlock()
	if !equalStrings(contexts, []string{"问题 A", "问题 B"}) {
		t.Fatalf("Describe task contexts=%q", contexts)
	}
}

func TestPreprocessorPartitionsSequentialCacheByForwardedHeaders(t *testing.T) {
	for _, headerName := range anthropicForwardedHeaders {
		t.Run(headerName, func(t *testing.T) {
			var calls atomic.Int32
			d := describerFunc(func(_ context.Context, headers http.Header, _ imageRef) (string, error) {
				calls.Add(1)
				return strings.Join(headers.Values(headerName), "|"), nil
			})
			p := testPreprocessor(2, d)
			firstHeaders := testVisionHeaders()
			secondHeaders := firstHeaders.Clone()
			secondHeaders[headerName] = []string{"domain-two"}
			body := fileImageRequest("file_shared")

			first, err := p.Process(context.Background(), firstHeaders, body)
			if err != nil {
				t.Fatal(err)
			}
			second, err := p.Process(context.Background(), secondHeaders, body)
			if err != nil {
				t.Fatal(err)
			}

			if calls.Load() != 2 {
				t.Fatalf("Describe calls=%d, want 2", calls.Load())
			}
			if got := rewrittenDescriptions(t, first); !equalStrings(got, []string{
				strings.Join(firstHeaders.Values(headerName), "|"),
			}) {
				t.Fatalf("first descriptions=%q", got)
			}
			if got := rewrittenDescriptions(t, second); !equalStrings(got, []string{"domain-two"}) {
				t.Fatalf("second descriptions=%q", got)
			}
		})
	}
}

func TestPreprocessorReusesCacheWithinSameForwardedHeaderDomain(t *testing.T) {
	var calls atomic.Int32
	d := describerFunc(func(_ context.Context, _ http.Header, _ imageRef) (string, error) {
		calls.Add(1)
		return "same domain", nil
	})
	p := testPreprocessor(2, d)
	headers := testVisionHeaders()
	body := fileImageRequest("file_shared")

	for range 2 {
		got, err := p.Process(context.Background(), headers, body)
		if err != nil {
			t.Fatal(err)
		}
		if descriptions := rewrittenDescriptions(t, got); !equalStrings(descriptions, []string{"same domain"}) {
			t.Fatalf("descriptions=%q", descriptions)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("Describe calls=%d, want 1", calls.Load())
	}
}

func TestPreprocessorDoesNotCoalesceConcurrentDifferentHeaderDomains(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	d := describerFunc(func(_ context.Context, headers http.Header, _ imageRef) (string, error) {
		calls.Add(1)
		domain := headers.Get("Authorization") + "|" + headers.Get("Anthropic-Version")
		started <- domain
		<-release
		return domain, nil
	})
	p := testPreprocessor(2, d)
	body := fileImageRequest("file_shared")
	firstHeaders := testVisionHeaders()
	secondHeaders := firstHeaders.Clone()
	secondHeaders.Set("Authorization", "Bearer domain-two")
	secondHeaders.Set("Anthropic-Version", "version-two")

	type domainResult struct {
		body []byte
		err  error
		want string
	}
	results := make(chan domainResult, 2)
	for _, headers := range []http.Header{firstHeaders, secondHeaders} {
		go func() {
			got, err := p.Process(context.Background(), headers, body)
			results <- domainResult{
				body: got,
				err:  err,
				want: headers.Get("Authorization") + "|" + headers.Get("Anthropic-Version"),
			}
		}()
	}

	<-started
	secondStarted := false
	select {
	case <-started:
		secondStarted = true
	case <-time.After(100 * time.Millisecond):
	}
	close(release)

	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if got := rewrittenDescriptions(t, result.body); !equalStrings(got, []string{result.want}) {
			t.Fatalf("descriptions=%q, want %q", got, result.want)
		}
	}
	if !secondStarted || calls.Load() != 2 {
		t.Fatalf("concurrent domains shared one loader: started=%v calls=%d", secondStarted, calls.Load())
	}
}

func TestPreprocessorTimeoutIncludesSemaphoreQueue(t *testing.T) {
	holderStarted := make(chan struct{})
	releaseHolder := make(chan struct{})
	var calls atomic.Int32
	d := describerFunc(func(ctx context.Context, _ http.Header, image imageRef) (string, error) {
		calls.Add(1)
		if image.cachePayload == "holder" {
			close(holderStarted)
			<-ctx.Done()
			<-releaseHolder
			return "", ctx.Err()
		}
		return "queued description", nil
	})
	p := testPreprocessorWithTimeout(1, 30*time.Millisecond, d)
	holderResult := make(chan error, 1)
	go func() {
		_, err := p.Process(context.Background(), nil, imageRequest("holder"))
		holderResult <- err
	}()
	<-holderStarted

	queuedResult := make(chan error, 1)
	go func() {
		_, err := p.Process(context.Background(), nil, imageRequest("queued"))
		queuedResult <- err
	}()

	select {
	case err := <-queuedResult:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queued error=%v, want deadline exceeded", err)
		}
	case <-time.After(100 * time.Millisecond):
		close(releaseHolder)
		<-holderResult
		<-queuedResult
		t.Fatal("queued image exceeded vision timeout while waiting for a slot")
	}
	if calls.Load() != 1 {
		t.Fatalf("Describe calls=%d, want holder only", calls.Load())
	}
	close(releaseHolder)
	if err := <-holderResult; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("holder error=%v, want deadline exceeded", err)
	}
}

func TestVisionLogsStagesWithoutSensitiveValues(t *testing.T) {
	const (
		base64Secret   = "BASE64_SECRET_c2Vuc2l0aXZl"
		urlSecret      = "https://private.example/URL_SECRET.png"
		fileSecret     = "FILE_SECRET_success"
		failSecret     = "FILE_SECRET_failure"
		authSecret     = "Bearer AUTH_SECRET"
		apiKeySecret   = "API_KEY_SECRET"
		promptSecret   = "PROMPT_SECRET"
		responseSecret = "UPSTREAM_RESPONSE_SECRET"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var requestBody json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if bytes.Contains(requestBody, []byte(failSecret)) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(responseSecret))
			return
		}
		_, _ = w.Write([]byte(`{
			"model":"sonnet",
			"content":[{"type":"text","text":"DESCRIPTION_SECRET"}],
			"usage":{"input_tokens":1,"output_tokens":1}
		}`))
	}))
	defer server.Close()

	cfg := testVisionConfig(server.URL)
	cfg.Vision.UnlistedModelPolicy = profile.UnlistedModelEnhance
	cfg.Vision.Prompt = promptSecret
	cfg.Vision.MaxConcurrency = 3
	cfg.Vision.CacheTTL = time.Minute
	cfg.Vision.CacheMaxEntries = 16
	p := New(cfg, server.Client(), nil)
	headers := http.Header{
		"Authorization":     {authSecret},
		"X-Api-Key":         {apiKeySecret},
		"Anthropic-Version": {"VERSION_SECRET"},
		"Anthropic-Beta":    {"BETA_SECRET"},
	}

	var logs bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	successBody := secretImageRequest(base64Secret, urlSecret, fileSecret)
	if _, err := p.Process(context.Background(), headers, successBody); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Process(context.Background(), headers, successBody); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Process(context.Background(), headers, fileImageRequest(failSecret)); err == nil {
		t.Fatal("expected failed vision request")
	}

	logText := logs.String()
	for _, secret := range []string{
		base64Secret,
		urlSecret,
		fileSecret,
		failSecret,
		authSecret,
		apiKeySecret,
		promptSecret,
		"VERSION_SECRET",
		"BETA_SECRET",
	} {
		if strings.Contains(logText, secret) {
			t.Fatalf("vision logs leaked %q: %s", secret, logText)
		}
	}

	wantedEvents := map[string]bool{
		"vision.images.discovered":       false,
		"vision.image.cache":             false,
		"vision.image.completed":         false,
		"vision.image.failed":            false,
		"vision.rewrite.completed":       false,
		"vision.shadow.attempt":          false,
		"vision.debug.description":       false,
		"vision.debug.upstream_response": false,
	}
	allowedFields := map[string]bool{
		"time":                  true,
		"level":                 true,
		"msg":                   true,
		"profile":               true,
		"transport":             true,
		"image_count":           true,
		"unhandled_image_count": true,
		"image_index":           true,
		"source_type":           true,
		"cache_source":          true,
		"attempt":               true,
		"duration_ms":           true,
		"error_class":           true,
		"model":                 true,
		"status_code":           true,
		"response_bytes":        true,
		"retry_matched":         true,
		"description":           true,
		"response_body":         true,
		"truncated":             true,
	}
	for _, line := range strings.Split(strings.TrimSpace(logText), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid JSON log %q: %v", line, err)
		}
		message, _ := record["msg"].(string)
		if _, ok := wantedEvents[message]; !ok {
			continue
		}
		wantedEvents[message] = true
		for field := range record {
			if !allowedFields[field] {
				t.Fatalf("vision log field %q is not allowed: %v", field, record)
			}
		}
		if class, ok := record["error_class"].(string); ok && !boundedVisionErrorClass(class) {
			t.Fatalf("unbounded error_class=%q", class)
		}
	}
	for event, seen := range wantedEvents {
		if !seen {
			t.Fatalf("missing %s event in logs: %s", event, logText)
		}
	}
	if !strings.Contains(logText, `"cache_source":"loaded"`) ||
		!strings.Contains(logText, `"cache_source":"cache"`) {
		t.Fatalf("logs do not expose load and cache sources: %s", logText)
	}
}

func TestVisionDebugLogsContent(t *testing.T) {
	const (
		description = "debug vision description"
		failureBody = `{"error":{"type":"invalid_request_error","message":"debug upstream failure"}}`
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if bytes.Contains(requestBody, []byte("debug-failure")) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, failureBody)
			return
		}
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"`+description+`"}]}`)
	}))
	defer server.Close()

	cfg := testVisionConfig(server.URL)
	cfg.Vision.UnlistedModelPolicy = profile.UnlistedModelEnhance
	cfg.Vision.MaxConcurrency = 1
	cfg.Vision.CacheTTL = time.Minute
	cfg.Vision.CacheMaxEntries = 8
	p := New(cfg, server.Client(), nil)

	var logs bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	if _, err := p.Process(context.Background(), nil, imageRequest("debug-success")); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Process(context.Background(), nil, imageRequest("debug-failure")); err == nil {
		t.Fatal("expected upstream failure")
	}

	logText := logs.String()
	for _, want := range []string{
		`"msg":"vision.debug.description"`,
		`"description":"` + description + `"`,
		`"msg":"vision.debug.upstream_response"`,
		`"response_body":` + strconv.Quote(failureBody),
		`"model":"sonnet"`,
		`"status_code":200`,
		`"status_code":400`,
		`"retry_matched":false`,
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("debug logs missing %s: %s", want, logText)
		}
	}
}

func boundedVisionErrorClass(class string) bool {
	switch class {
	case "none", "canceled", "timeout", "network", "upstream", "overload",
		"response_io", "invalid_response", "request", "internal":
		return true
	default:
		return false
	}
}

func TestPreprocessorReturnsRealDescriptionErrorAfterCancelingSiblings(t *testing.T) {
	describeErr := errors.New("vision failed")
	failed := make(chan struct{})
	fake := &fakeDescriber{describe: func(image imageRef) (string, error) {
		if image.cachePayload == "bad" {
			close(failed)
			return "", describeErr
		}
		<-failed
		return "", context.Canceled
	}}
	p := testPreprocessor(3, fake)

	got, err := p.Process(context.Background(), nil, imageRequest("slow-1", "bad", "slow-2"))
	if got != nil {
		t.Fatalf("body=%s, want nil", got)
	}
	if HTTPStatus(err) != http.StatusBadGateway {
		t.Fatalf("status=%d err=%v", HTTPStatus(err), err)
	}
	if !errors.Is(err, describeErr) {
		t.Fatalf("err=%v, want wrapped vision error", err)
	}
}

func TestPreprocessorWaitsForActiveDescriptionsAfterFailure(t *testing.T) {
	describeErr := errors.New("vision failed")
	slowStarted := make(chan struct{})
	slowCanceled := make(chan struct{})
	releaseSlow := make(chan struct{})
	d := describerFunc(func(ctx context.Context, _ http.Header, image imageRef) (string, error) {
		if image.cachePayload == "bad" {
			<-slowStarted
			return "", describeErr
		}
		close(slowStarted)
		<-ctx.Done()
		close(slowCanceled)
		<-releaseSlow
		return "", ctx.Err()
	})
	p := testPreprocessor(2, d)
	result := make(chan error, 1)
	go func() {
		_, err := p.Process(context.Background(), nil, imageRequest("slow", "bad"))
		result <- err
	}()

	select {
	case <-slowCanceled:
	case <-time.After(time.Second):
		t.Fatal("active description did not observe sibling failure")
	}
	select {
	case err := <-result:
		t.Fatalf("Process returned before active description exited: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseSlow)
	select {
	case err := <-result:
		if !errors.Is(err, describeErr) {
			t.Fatalf("err=%v, want wrapped vision error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Process did not return after active description exited")
	}
}

func TestPreprocessorCreatorDetachesWhenAnotherRequestSharesLoaderAfterSiblingFailure(t *testing.T) {
	describeErr := errors.New("vision failed")
	sharedStarted := make(chan struct{})
	releaseShared := make(chan struct{})
	failNow := make(chan struct{})
	d := describerFunc(func(_ context.Context, _ http.Header, image imageRef) (string, error) {
		switch image.cachePayload {
		case "shared":
			close(sharedStarted)
			<-releaseShared
			return "shared description", nil
		case "bad":
			<-failNow
			return "", describeErr
		default:
			return "", errors.New("unexpected image")
		}
	})
	p := testPreprocessor(2, d)
	creatorResult := make(chan processResult, 1)
	go func() {
		body, err := p.Process(context.Background(), nil, imageRequest("shared", "bad"))
		creatorResult <- processResult{body: body, err: err}
	}()

	select {
	case <-sharedStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for shared description")
	}
	sharerResult := make(chan processResult, 1)
	go func() {
		body, err := p.Process(context.Background(), nil, imageRequest("shared"))
		sharerResult <- processResult{body: body, err: err}
	}()
	waitForResultCacheWaiters(t, p.cache, testImageKey(p, "shared"), 2)
	close(failNow)

	select {
	case result := <-creatorResult:
		if result.body != nil || !errors.Is(result.err, describeErr) {
			t.Fatalf("creator body=%s err=%v", result.body, result.err)
		}
	case <-time.After(time.Second):
		close(releaseShared)
		t.Fatal("creator waited for loader owned by the sharing request")
	}
	select {
	case result := <-sharerResult:
		t.Fatalf("sharing request returned before shared loader completed: body=%s err=%v", result.body, result.err)
	default:
	}
	close(releaseShared)
	select {
	case result := <-sharerResult:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if got := rewrittenDescriptions(t, result.body); !equalStrings(got, []string{"shared description"}) {
			t.Fatalf("descriptions=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("sharing request did not complete")
	}
}

func TestPreprocessorCreatorDetachesWhenAnotherRequestSharesLoaderAfterCancellation(t *testing.T) {
	sharedStarted := make(chan struct{})
	releaseShared := make(chan struct{})
	d := describerFunc(func(_ context.Context, _ http.Header, image imageRef) (string, error) {
		if image.cachePayload != "shared" {
			return "", errors.New("unexpected image")
		}
		close(sharedStarted)
		<-releaseShared
		return "shared description", nil
	})
	p := testPreprocessor(1, d)
	creatorCtx, cancelCreator := context.WithCancel(context.Background())
	creatorResult := make(chan processResult, 1)
	go func() {
		body, err := p.Process(creatorCtx, nil, imageRequest("shared"))
		creatorResult <- processResult{body: body, err: err}
	}()

	select {
	case <-sharedStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for shared description")
	}
	sharerResult := make(chan processResult, 1)
	go func() {
		body, err := p.Process(context.Background(), nil, imageRequest("shared"))
		sharerResult <- processResult{body: body, err: err}
	}()
	waitForResultCacheWaiters(t, p.cache, testImageKey(p, "shared"), 2)
	cancelCreator()

	select {
	case result := <-creatorResult:
		if result.body != nil || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("creator body=%s err=%v", result.body, result.err)
		}
	case <-time.After(time.Second):
		close(releaseShared)
		t.Fatal("canceled creator waited for loader owned by the sharing request")
	}
	select {
	case result := <-sharerResult:
		t.Fatalf("sharing request returned before shared loader completed: body=%s err=%v", result.body, result.err)
	default:
	}
	close(releaseShared)
	select {
	case result := <-sharerResult:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if got := rewrittenDescriptions(t, result.body); !equalStrings(got, []string{"shared description"}) {
			t.Fatalf("descriptions=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("sharing request did not complete")
	}
}

func TestPreprocessorKeepsFirstImageDeadlineOverLaterFailure(t *testing.T) {
	deadlineErr := &signalingDeadlineError{observed: make(chan struct{})}
	laterErr := errors.New("later failure")
	laterStarted := make(chan struct{})
	d := describerFunc(func(_ context.Context, _ http.Header, image imageRef) (string, error) {
		switch image.cachePayload {
		case "timeout":
			<-laterStarted
			return "", deadlineErr
		case "later":
			close(laterStarted)
			<-deadlineErr.observed
			return "", laterErr
		default:
			return "", errors.New("unexpected image")
		}
	})
	p := testPreprocessor(2, d)

	body, err := p.Process(context.Background(), nil, imageRequest("timeout", "later"))
	if body != nil {
		t.Fatalf("body=%s, want nil", body)
	}
	if status := HTTPStatus(err); status != http.StatusBadGateway {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want image deadline exceeded", err)
	}
	if errors.Is(err, laterErr) {
		t.Fatalf("later error replaced first image timeout: %v", err)
	}
}

func TestProcessFailuresKeepsFirstDeadlineOverLaterFailure(t *testing.T) {
	laterErr := errors.New("later failure")
	var failures processFailures

	failures.record(0, context.DeadlineExceeded)
	failures.record(1, laterErr)

	err := failures.err()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want deadline exceeded", err)
	}
	if errors.Is(err, laterErr) {
		t.Fatalf("later error replaced first deadline: %v", err)
	}
}

func TestPreprocessorClassifiesInvalidRequestsAsBadRequest(t *testing.T) {
	fake := &fakeDescriber{describe: func(imageRef) (string, error) {
		t.Fatal("Describe called for invalid request")
		return "", nil
	}}
	p := testPreprocessor(1, fake)
	tests := map[string][]byte{
		"invalid JSON": []byte(`{"messages":`),
		"invalid source": []byte(`{"model":"main","messages":[{"content":[
			{"type":"image","source":{"type":"url"}}
		]}]}`),
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := p.Process(context.Background(), nil, body)
			if got != nil {
				t.Fatalf("body=%s, want nil", got)
			}
			if status := HTTPStatus(err); status != http.StatusBadRequest {
				t.Fatalf("status=%d err=%v", status, err)
			}
		})
	}
}

func TestPreprocessorPreservesRequestCancellationAndDoesNotStartQueuedDescriptions(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	fake := &fakeDescriber{describe: func(imageRef) (string, error) {
		startedOnce.Do(func() { close(started) })
		<-release
		return "description", nil
	}}
	p := testPreprocessor(1, fake)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan struct {
		body []byte
		err  error
	}, 1)
	go func() {
		body, err := p.Process(ctx, nil, imageRequest("1", "2", "3", "4"))
		result <- struct {
			body []byte
			err  error
		}{body: body, err: err}
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first description")
	}
	cancel()
	close(release)
	got := <-result
	if got.body != nil {
		t.Fatalf("body=%s, want nil", got.body)
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("err=%v, want context canceled", got.err)
	}
	waitForFakeIdle(t, fake)
	if calls, _, _ := fake.snapshot(); calls != 1 {
		t.Fatalf("Describe calls=%d, want 1", calls)
	}
}

func testPreprocessor(maxConcurrency int, d describer) *Preprocessor {
	return testPreprocessorWithTimeout(maxConcurrency, time.Second, d)
}

func testPreprocessorWithTimeout(maxConcurrency int, timeout time.Duration, d describer) *Preprocessor {
	return newPreprocessor("provider", profile.VisionRuntime{
		Model:               "vision-model",
		UnlistedModelPolicy: profile.UnlistedModelEnhance,
		Prompt:              "describe",
		Timeout:             timeout,
		MaxConcurrency:      maxConcurrency,
	}, d, newResultCache(32, time.Minute))
}

type describerFunc func(context.Context, http.Header, imageRef) (string, error)

func (f describerFunc) DescribeTarget(
	ctx context.Context,
	headers http.Header,
	_ string,
	image imageRef,
) (string, error) {
	return f(ctx, headers, image)
}

type processResult struct {
	body []byte
	err  error
}

type signalingDeadlineError struct {
	observed chan struct{}
	once     sync.Once
}

func (e *signalingDeadlineError) Error() string {
	return context.DeadlineExceeded.Error()
}

func (e *signalingDeadlineError) Is(target error) bool {
	e.once.Do(func() { close(e.observed) })
	return target == context.DeadlineExceeded
}

func testImageKey(p *Preprocessor, payload string) string {
	image := imageRef{
		sourceType:   "url",
		cachePayload: payload,
	}
	return scopedImageCacheKey(
		p.cacheKey, nil, p.headers, p.profile,
		string(p.transport)+"\x00"+p.model,
		promptForImage(p.prompt, image), image,
	)
}

func imageRequest(payloads ...string) []byte {
	blocks := make([]map[string]any, len(payloads))
	for i, payload := range payloads {
		blocks[i] = map[string]any{
			"type": "image",
			"source": map[string]string{
				"type": "url",
				"url":  payload,
			},
		}
	}
	body, err := json.Marshal(map[string]any{
		"model": "main",
		"messages": []any{map[string]any{
			"role":    "user",
			"content": blocks,
		}},
	})
	if err != nil {
		panic(err)
	}
	return body
}

func anthropicGateRequest(model any) []byte {
	request := map[string]any{
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{map[string]any{
				"type": "image",
				"source": map[string]any{
					"type": "url",
					"url":  "https://example.test/image.png",
				},
			}},
		}},
	}
	if model != nil {
		request["model"] = model
	}
	body, _ := json.Marshal(request)
	return body
}

func responsesGateRequest(model any) []byte {
	request := map[string]any{
		"input": []any{map[string]any{
			"role": "user",
			"content": []any{map[string]any{
				"type":      "input_image",
				"image_url": "https://example.test/image.png",
			}},
		}},
	}
	if model != nil {
		request["model"] = model
	}
	body, _ := json.Marshal(request)
	return body
}

func fileImageRequest(fileID string) []byte {
	body, err := json.Marshal(map[string]any{
		"model": "main",
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{map[string]any{
				"type": "image",
				"source": map[string]string{
					"type":    "file",
					"file_id": fileID,
				},
			}},
		}},
	})
	if err != nil {
		panic(err)
	}
	return body
}

func contextualFileImageRequest(fileID, question string) []byte {
	body, err := json.Marshal(map[string]any{
		"model": "main",
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]string{
					"type": "text",
					"text": question,
				},
				map[string]any{
					"type": "image",
					"source": map[string]string{
						"type":    "file",
						"file_id": fileID,
					},
				},
			},
		}},
	})
	if err != nil {
		panic(err)
	}
	return body
}

func secretImageRequest(base64Data, imageURL, fileID string) []byte {
	body, err := json.Marshal(map[string]any{
		"model": "main",
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type": "image",
					"source": map[string]string{
						"type":       "base64",
						"media_type": "image/png",
						"data":       base64Data,
					},
				},
				map[string]any{
					"type": "image",
					"source": map[string]string{
						"type": "url",
						"url":  imageURL,
					},
				},
				map[string]any{
					"type": "image",
					"source": map[string]string{
						"type":    "file",
						"file_id": fileID,
					},
				},
			},
		}},
	})
	if err != nil {
		panic(err)
	}
	return body
}

func testVisionHeaders() http.Header {
	return http.Header{
		"Authorization":     {"Bearer domain-one"},
		"X-Api-Key":         {"key-one"},
		"Anthropic-Version": {"version-one"},
		"Anthropic-Beta":    {"beta-one"},
	}
}

func rewrittenDescriptions(t *testing.T, body []byte) []string {
	t.Helper()
	var request struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Messages) != 1 {
		t.Fatalf("messages=%d", len(request.Messages))
	}
	descriptions := make([]string, len(request.Messages[0].Content))
	for i, block := range request.Messages[0].Content {
		if block.Type != "text" {
			t.Fatalf("block %d type=%q", i, block.Type)
		}
		descriptions[i] = block.Text[len(descriptionPrefix):]
	}
	return descriptions
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func waitForFakeIdle(t *testing.T, fake *fakeDescriber) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, active, _ := fake.snapshot()
		if active == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for fake describer to become idle")
}
