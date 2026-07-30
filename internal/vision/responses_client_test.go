package vision

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/stats"
)

func TestOpenAIVisionClientUsesResponsesAPI(t *testing.T) {
	responseBody := []byte(`{
	  "model":"gpt-5.4-mini",
	  "output":[
	    {"type":"message","role":"assistant","content":[
	      {"type":"output_text","text":"first","annotations":[]},
	      {"type":"output_text","text":"second","annotations":[]}
	    ]}
	  ],
	  "usage":{"input_tokens":12,"output_tokens":8}
	}`)
	var handlerErr error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			handlerErr = errors.New("wrong request path: " + r.URL.Path)
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		var request struct {
			Model           string `json:"model"`
			MaxOutputTokens int    `json:"max_output_tokens"`
			Stream          *bool  `json:"stream"`
			Store           *bool  `json:"store"`
			Input           []struct {
				Role    string            `json:"role"`
				Content []json.RawMessage `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			handlerErr = err
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if request.Model != "gpt-5.4-mini" || request.MaxOutputTokens != 512 {
			handlerErr = errors.New("wrong model or max_output_tokens")
		}
		if request.Stream == nil || *request.Stream || request.Store == nil || *request.Store {
			handlerErr = errors.New("stream and store must be explicitly false")
		}
		if len(request.Input) != 1 || request.Input[0].Role != "user" ||
			len(request.Input[0].Content) != 2 {
			handlerErr = errors.New("wrong input message")
		} else {
			if !bytes.Equal(request.Input[0].Content[0], testResponsesImage().block) {
				handlerErr = errors.New("input image block was not preserved")
			}
			var prompt struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(request.Input[0].Content[1], &prompt); err != nil ||
				prompt.Type != "input_text" || prompt.Text != defaultPrompt {
				handlerErr = errors.New("wrong prompt block")
			}
		}
		for name, want := range map[string]string{
			"Authorization":       "Bearer secret",
			"OpenAI-Organization": "org-123",
			"OpenAI-Project":      "proj-123",
		} {
			if got := r.Header.Get(name); got != want {
				handlerErr = errors.New("wrong allowed header: " + name)
			}
		}
		if got := r.Header.Get("Cookie"); got != "" {
			handlerErr = errors.New("copied disallowed Cookie header")
		}
		if got := r.Header.Get("Anthropic-Version"); got != "" {
			handlerErr = errors.New("copied disallowed Anthropic-Version header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseBody)
	}))
	defer server.Close()

	cfg := testVisionConfig(server.URL)
	cfg.Protocol = profile.ProtocolOpenAI
	cfg.Vision.Model = "gpt-5.4-mini"
	cfg.Vision.MaxTokens = 512
	client := newVisionClient(cfg, server.Client(), nil)
	recorded := make(chan []byte, 1)
	client.recordUsage = func(body []byte) {
		recorded <- append([]byte(nil), body...)
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer secret")
	headers.Set("OpenAI-Organization", "org-123")
	headers.Set("OpenAI-Project", "proj-123")
	headers.Set("Anthropic-Version", "must-not-copy")
	headers.Set("Cookie", "must-not-copy")

	got, err := client.Describe(context.Background(), headers, testResponsesImage())
	if err != nil {
		t.Fatal(err)
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	if got != "first\nsecond" {
		t.Fatalf("description=%q, want %q", got, "first\nsecond")
	}
	select {
	case got := <-recorded:
		if !bytes.Equal(got, responseBody) {
			t.Fatalf("recorded body=%q, want %q", got, responseBody)
		}
	default:
		t.Fatal("usage body was not recorded")
	}
}

func TestOpenAIVisionClientRequestBodyUsesContextPrompt(t *testing.T) {
	cfg := testVisionConfig("https://upstream.example")
	cfg.Protocol = profile.ProtocolOpenAI
	client := newVisionClient(cfg, nil, nil)
	image := testResponsesImage()
	image.taskContext = "能打多少分"

	body, err := client.requestBody(image)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Input []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	if len(request.Input) != 1 || len(request.Input[0].Content) != 2 {
		t.Fatalf("input=%+v", request.Input)
	}
	if got := request.Input[0].Content[1]; got.Type != "input_text" ||
		got.Text != promptForImage(client.cfg.Prompt, image) {
		t.Fatalf("prompt=%q", got.Text)
	}
}

func TestOpenAIVisionClientRecordsUsageBeforeRejectingMissingDescription(t *testing.T) {
	responseBody := []byte(`{
	  "model":"gpt-5.4-mini",
	  "output":[{"type":"message","content":[{"type":"refusal","refusal":"cannot describe"}]}],
	  "usage":{"input_tokens":12,"output_tokens":3}
	}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(responseBody)
	}))
	defer server.Close()

	cfg := testVisionConfig(server.URL)
	cfg.Protocol = profile.ProtocolOpenAI
	client := newVisionClient(cfg, server.Client(), nil)
	recorded := make(chan []byte, 1)
	client.recordUsage = func(body []byte) {
		recorded <- append([]byte(nil), body...)
	}

	if _, err := client.Describe(context.Background(), nil, testResponsesImage()); err == nil {
		t.Fatal("Describe succeeded without output_text")
	}
	select {
	case got := <-recorded:
		if !bytes.Equal(got, responseBody) {
			t.Fatalf("recorded body=%q, want %q", got, responseBody)
		}
	default:
		t.Fatal("billable 2xx usage was not recorded")
	}
}

func TestOpenAIVisionClientRecordsResponsesUsageThroughStatsDB(t *testing.T) {
	responseBody := []byte(`{
	  "model": "gpt-5.4-mini",
	  "output": [{"type":"message","content":[{"type":"output_text","text":"description"}]}],
	  "usage": {
	    "input_tokens": 14,
	    "input_tokens_details": {"cached_tokens": 5},
	    "output_tokens": 9
	  }
	}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(responseBody)
	}))
	defer server.Close()

	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`
		INSERT INTO profiles (
			id, slug, display_name, enabled, config_json, created_at, updated_at
		) VALUES (7, 'openai', 'OpenAI', 1, '{}', ?, ?)
	`, now, now); err != nil {
		t.Fatal(err)
	}

	sdb := stats.New(db)
	cfg := testVisionConfig(server.URL)
	cfg.Slug = "openai"
	cfg.Protocol = profile.ProtocolOpenAI
	cfg.Vision.Model = "gpt-5.4-mini"
	client := newVisionClient(cfg, server.Client(), sdb)
	if _, err := client.Describe(context.Background(), nil, testResponsesImage()); err != nil {
		t.Fatal(err)
	}
	if err := sdb.Close(); err != nil {
		t.Fatal(err)
	}

	var profileID sql.NullInt64
	var profileSlug, protocol, kind, model, path string
	var input, output, cacheRead, cacheCreation int
	err = db.QueryRow(`
		SELECT profile_id, profile_slug, protocol, request_kind, model, path,
		       input_tokens, output_tokens,
		       cache_read_tokens, cache_creation_tokens
		FROM usage ORDER BY id DESC LIMIT 1
	`).Scan(
		&profileID, &profileSlug, &protocol, &kind, &model, &path,
		&input, &output, &cacheRead, &cacheCreation,
	)
	if err != nil {
		t.Fatalf("vision usage row not recorded: %v", err)
	}
	if !profileID.Valid || profileID.Int64 != 7 ||
		profileSlug != "openai" || protocol != "openai" || kind != "vision" ||
		model != "gpt-5.4-mini" || path != "/v1/responses" ||
		input != 14 || output != 9 || cacheRead != 5 || cacheCreation != 0 {
		t.Fatalf("usage row=%v %q %q %q %q %q %d %d %d %d",
			profileID, profileSlug, protocol, kind, model, path,
			input, output, cacheRead, cacheCreation)
	}
}

func testResponsesImage() imageRef {
	return imageRef{
		block:        json.RawMessage(`{"type":"input_image","image_url":"https://private.example/image.png","detail":"high"}`),
		sourceType:   "url",
		cachePayload: "high\x00https://private.example/image.png",
	}
}
