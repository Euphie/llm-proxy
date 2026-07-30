package vision

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/profile"
)

func TestParseResponsesRewritesInputImagesInPlace(t *testing.T) {
	body := []byte(`{
	  "model": "gpt-5.4",
	  "metadata": {"trace": "keep"},
	  "input": [
	    {
	      "role": "user",
	      "content": [
	        {"type": "input_text", "text": "before"},
	        {"type": "input_image", "image_url": "https://example.test/a.png", "detail": "high"},
	        {"type": "input_image", "image_url": "data:image/png;base64,AAAA"},
	        {"type": "input_image", "file_id": "file-123"},
	        {"type": "input_text", "text": "after"}
	      ],
	      "extra": true
	    },
	    {
	      "type": "function_call_output",
	      "content": [
	        {"type": "input_image", "image_url": "https://example.test/nested.png"}
	      ]
	    }
	  ]
	}`)

	doc, err := parseResponses(body)
	if err != nil {
		t.Fatal(err)
	}
	images := doc.images()
	if len(images) != 3 {
		t.Fatalf("image count=%d, want 3", len(images))
	}
	if images[0].sourceType != "url" || images[1].sourceType != "data_url" || images[2].sourceType != "file" {
		t.Fatalf("source types=%q, %q, %q", images[0].sourceType, images[1].sourceType, images[2].sourceType)
	}

	rewritten, err := doc.rewrite([]string{"url description", "data description", "file description"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Metadata map[string]string `json:"metadata"`
		Input    []struct {
			Extra   bool `json:"extra"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL string `json:"image_url"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatal(err)
	}
	if got.Metadata["trace"] != "keep" || !got.Input[0].Extra {
		t.Fatalf("unknown fields were not preserved: %+v", got)
	}
	wantTypes := []string{"input_text", "input_text", "input_text", "input_text", "input_text"}
	for i, want := range wantTypes {
		if got.Input[0].Content[i].Type != want {
			t.Fatalf("content %d type=%q, want %q", i, got.Input[0].Content[i].Type, want)
		}
	}
	wantDescriptions := []string{"url description", "data description", "file description"}
	for i, want := range wantDescriptions {
		text := got.Input[0].Content[i+1].Text
		if text != descriptionPrefix+want {
			t.Fatalf("content %d text=%q, want %q", i+1, text, descriptionPrefix+want)
		}
	}
	if got.Input[1].Content[0].Type != "input_image" ||
		got.Input[1].Content[0].ImageURL != "https://example.test/nested.png" {
		t.Fatalf("non-message item was rewritten: %+v", got.Input[1].Content[0])
	}
}

func TestResponsesImageDetailSeparatesCacheKeys(t *testing.T) {
	parseImage := func(detail string) imageRef {
		t.Helper()
		doc, err := parseResponses([]byte(`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/a.png","detail":"` + detail + `"}]}]}`))
		if err != nil {
			t.Fatal(err)
		}
		return doc.images()[0]
	}

	low := imageCacheKey("openai", "vision-model", "describe", parseImage("low"))
	high := imageCacheKey("openai", "vision-model", "describe", parseImage("high"))
	original := imageCacheKey("openai", "vision-model", "describe", parseImage("original"))
	if low == high || low == original || high == original {
		t.Fatal("different image detail levels shared a cache key")
	}
}

func TestParseResponsesRootReusesDecodedRequest(t *testing.T) {
	root, err := parseRequestRoot([]byte(`{"model":"main","input":[{"role":"user","content":[]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseResponsesRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.items) != 1 {
		t.Fatalf("items=%d, want 1", len(doc.items))
	}
}

func TestParseResponsesRejectsInvalidInputImageSource(t *testing.T) {
	tests := []string{
		`{"type":"input_image"}`,
		`{"type":"input_image","image_url":""}`,
		`{"type":"input_image","file_id":""}`,
		`{"type":"input_image","image_url":"https://example.test/a.png","file_id":"file-123"}`,
	}
	for _, block := range tests {
		t.Run(block, func(t *testing.T) {
			_, err := parseResponses([]byte(`{"input":[{"role":"user","content":[` + block + `]}]}`))
			if err == nil || !strings.Contains(err.Error(), "input image") {
				t.Fatalf("error=%v, want input image validation error", err)
			}
		})
	}
}

func TestParseResponsesDescriptionCollectsOutputText(t *testing.T) {
	body := []byte(`{
	  "output": [
	    {"type":"reasoning","content":[]},
	    {"type":"message","content":[
	      {"type":"output_text","text":"first"},
	      {"type":"refusal","refusal":"no"},
	      {"type":"output_text","text":"second"}
	    ]}
	  ]
	}`)

	got, err := parseResponsesDescription(body)
	if err != nil {
		t.Fatal(err)
	}
	if got != "first\nsecond" {
		t.Fatalf("description=%q, want %q", got, "first\nsecond")
	}
}

func TestOpenAIPreprocessorRewritesResponsesRequest(t *testing.T) {
	fake := &fakeDescriber{describe: func(image imageRef) (string, error) {
		if !strings.Contains(string(image.block), `"type":"input_image"`) {
			t.Fatalf("describer received %s", image.block)
		}
		return "a diagram", nil
	}}
	p := newPreprocessorForProtocol(
		profile.ProtocolOpenAI,
		"provider",
		nil,
		profile.VisionRuntime{
			Model:               "vision-model",
			UnlistedModelPolicy: profile.UnlistedModelEnhance,
			Prompt:              "describe",
			Timeout:             time.Second,
			MaxConcurrency:      2,
		},
		fake,
		newResultCache(32, time.Minute),
	)
	body := []byte(`{"model":"main","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"inspect"},{"type":"input_image","file_id":"file-123"}]}]}`)

	got, err := p.Process(context.Background(), nil, body)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Stream bool `json:"stream"`
		Input  []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(got, &request); err != nil {
		t.Fatal(err)
	}
	if !request.Stream {
		t.Fatal("stream setting was not preserved")
	}
	if request.Input[0].Content[1].Type != "input_text" ||
		request.Input[0].Content[1].Text != descriptionPrefix+"a diagram" {
		t.Fatalf("rewritten content=%+v", request.Input[0].Content[1])
	}
}

func TestScopedImageCacheKeySeparatesOpenAIProjects(t *testing.T) {
	image := testResponsesImage()
	first := make(http.Header)
	first.Set("Authorization", "Bearer secret")
	first.Set("OpenAI-Organization", "org-123")
	first.Set("OpenAI-Project", "project-a")
	second := first.Clone()
	second.Set("OpenAI-Project", "project-b")

	firstKey := scopedImageCacheKey(
		[]byte("cache-secret"), first, openAIForwardedHeaders[:],
		"openai", "vision-model", "describe", image,
	)
	secondKey := scopedImageCacheKey(
		[]byte("cache-secret"), second, openAIForwardedHeaders[:],
		"openai", "vision-model", "describe", image,
	)
	if firstKey == secondKey {
		t.Fatal("different OpenAI projects shared a scoped cache key")
	}
}

func TestAnthropicCacheScopeIgnoresOpenAIHeaders(t *testing.T) {
	var calls int
	fake := &fakeDescriber{describe: func(imageRef) (string, error) {
		calls++
		return "description", nil
	}}
	p := testPreprocessor(2, fake)
	first := testVisionHeaders()
	first.Set("OpenAI-Project", "project-a")
	second := first.Clone()
	second.Set("OpenAI-Project", "project-b")
	body := fileImageRequest("file-shared")

	if _, err := p.Process(context.Background(), first, body); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Process(context.Background(), second, body); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("Describe calls=%d, want one shared Anthropic cache entry", calls)
	}
}

func TestOpenAICacheScopeIgnoresAnthropicHeaders(t *testing.T) {
	var calls int
	fake := &fakeDescriber{describe: func(imageRef) (string, error) {
		calls++
		return "description", nil
	}}
	p := newPreprocessorForProtocol(
		profile.ProtocolOpenAI,
		"provider",
		nil,
		profile.VisionRuntime{
			Model:               "vision-model",
			UnlistedModelPolicy: profile.UnlistedModelEnhance,
			Prompt:              "describe",
			Timeout:             time.Second,
			MaxConcurrency:      2,
		},
		fake,
		newResultCache(32, time.Minute),
	)
	first := make(http.Header)
	first.Set("Authorization", "Bearer secret")
	first.Set("OpenAI-Project", "project-a")
	first.Set("Anthropic-Version", "2023-06-01")
	second := first.Clone()
	second.Set("Anthropic-Version", "2024-01-01")
	body := []byte(`{"model":"main","input":[{"role":"user","content":[{"type":"input_image","file_id":"file-shared"}]}]}`)

	if _, err := p.Process(context.Background(), first, body); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Process(context.Background(), second, body); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("Describe calls=%d, want one shared OpenAI cache entry", calls)
	}
}
