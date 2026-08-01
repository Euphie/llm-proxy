package vision

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Euphie/llm-proxy/internal/profile"
)

func TestShadowTarget(t *testing.T) {
	tests := []struct {
		name      string
		main      string
		transport profile.VisionTransport
		want      string
		wantErr   bool
	}{
		{
			name: "messages", main: "https://example.test/v1/messages",
			transport: profile.VisionTransportAnthropicMessages,
			want:      "https://example.test/v1/messages",
		},
		{
			name: "responses", main: "https://example.test/v1/responses",
			transport: profile.VisionTransportOpenAIResponses,
			want:      "https://example.test/v1/responses",
		},
		{
			name: "chat", main: "https://example.test/v1/responses",
			transport: profile.VisionTransportOpenAIChatCompletions,
			want:      "https://example.test/v1/chat/completions",
		},
		{
			name: "chat main request", main: "https://example.test/v1/chat/completions",
			transport: profile.VisionTransportOpenAIChatCompletions,
			want:      "https://example.test/v1/chat/completions",
		},
		{
			name: "chat invalid main", main: "https://example.test/v1/messages",
			transport: profile.VisionTransportOpenAIChatCompletions,
			wantErr:   true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := shadowTarget(test.main, test.transport)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("target=%q, want %q", got, test.want)
			}
		})
	}
}

func TestOpenAIVisionClientUsesChatCompletionsTransport(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path=%q", r.URL.Path)
		}
		var request struct {
			Model               string `json:"model"`
			MaxCompletionTokens int    `json:"max_completion_tokens"`
			Messages            []struct {
				Content []struct {
					Type     string `json:"type"`
					ImageURL struct {
						URL    string `json:"url"`
						Detail string `json:"detail"`
					} `json:"image_url"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Model != "vision-model" ||
			request.MaxCompletionTokens != 512 ||
			len(request.Messages) != 1 ||
			len(request.Messages[0].Content) != 2 ||
			request.Messages[0].Content[0].Type != "image_url" ||
			request.Messages[0].Content[0].ImageURL.URL != "https://private.example/image.png" ||
			request.Messages[0].Content[0].ImageURL.Detail != "high" {
			t.Errorf("request=%+v", request)
		}
		_, _ = io.WriteString(w, `{
			"choices":[{"message":{"content":"chat description"}}],
			"usage":{"prompt_tokens":10,"completion_tokens":20}
		}`)
	}))
	defer server.Close()

	cfg := testVisionConfig(server.URL)
	cfg.Protocol = profile.ProtocolOpenAI
	cfg.Vision.Transport = profile.VisionTransportOpenAIChatCompletions
	cfg.Vision.Model = "vision-model"
	cfg.Vision.MaxTokens = 512
	client := newVisionClient(cfg, server.Client(), nil)

	got, err := client.DescribeTarget(
		context.Background(),
		nil,
		server.URL+"/v1/responses",
		testResponsesImage(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "chat description" || calls.Load() != 1 {
		t.Fatalf("description=%q calls=%d", got, calls.Load())
	}
}

func TestChatCompletionsTransportRejectsFileID(t *testing.T) {
	cfg := testVisionConfig("https://example.test")
	cfg.Protocol = profile.ProtocolOpenAI
	cfg.Vision.Transport = profile.VisionTransportOpenAIChatCompletions
	client := newVisionClient(cfg, nil, nil)
	image := imageRef{
		block:        json.RawMessage(`{"type":"input_image","file_id":"file-123"}`),
		sourceType:   "file",
		cachePayload: "auto\x00file-123",
		fileID:       "file-123",
		detail:       "auto",
	}

	_, err := client.DescribeTarget(
		context.Background(),
		nil,
		"https://example.test/v1/responses",
		image,
	)
	if err == nil || !strings.Contains(err.Error(), "file_id") {
		t.Fatalf("error=%v", err)
	}
}
