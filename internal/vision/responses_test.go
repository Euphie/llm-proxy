package vision

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/llmrequest"
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
	      "call_id": "call-123",
	      "output": [
	        {"type": "input_text", "text": "tool output"},
	        {"type": "input_image", "image_url": "https://example.test/nested.png"}
	      ],
	      "tool_extra": true
	    }
	  ]
	}`)

	doc, err := parseResponses(body)
	if err != nil {
		t.Fatal(err)
	}
	images := doc.images()
	if len(images) != 4 {
		t.Fatalf("image count=%d, want 4", len(images))
	}
	wantSourceTypes := []string{"url", "data_url", "file", "url"}
	for i, want := range wantSourceTypes {
		if images[i].sourceType != want {
			t.Fatalf("image %d source type=%q, want %q", i, images[i].sourceType, want)
		}
	}
	for i, image := range images[:3] {
		if image.taskContext != "before\nafter" {
			t.Fatalf("image %d taskContext=%q, want %q", i, image.taskContext, "before\nafter")
		}
	}
	if images[3].taskContext != "" {
		t.Fatalf("function output taskContext=%q, want empty", images[3].taskContext)
	}

	rewritten, err := doc.rewrite([]string{
		"url description",
		"data description",
		"file description",
		"tool description",
	})
	if err != nil {
		t.Fatal(err)
	}
	type part struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL string `json:"image_url"`
	}
	var got struct {
		Metadata map[string]string `json:"metadata"`
		Input    []struct {
			Extra     bool   `json:"extra"`
			ToolExtra bool   `json:"tool_extra"`
			CallID    string `json:"call_id"`
			Content   []part `json:"content"`
			Output    []part `json:"output"`
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
		if text != evidencePrefix+want {
			t.Fatalf("content %d text=%q, want %q", i+1, text, evidencePrefix+want)
		}
	}
	if got.Input[1].CallID != "call-123" || !got.Input[1].ToolExtra {
		t.Fatalf("function output fields were not preserved: %+v", got.Input[1])
	}
	if got.Input[1].Output[0].Text != "tool output" ||
		got.Input[1].Output[1].Type != "input_text" ||
		got.Input[1].Output[1].Text != descriptionPrefix+"tool description" {
		t.Fatalf("function output image was not rewritten: %+v", got.Input[1].Output)
	}
}

func TestParseResponsesProcessesMessageImagesWithoutNonUserContext(t *testing.T) {
	body := []byte(`{
	  "input": [
	    {
	      "type": "message",
	      "role": "assistant",
	      "content": [
	        {"type": "input_text", "text": "assistant context"},
	        {"type": "input_image", "image_url": "https://example.test/assistant.png"}
	      ]
	    },
	    {
	      "type": "message",
	      "role": 42,
	      "content": [
	        {"type": "input_text", "text": "non-string role context"},
	        {"type": "input_image", "file_id": "file-non-string-role"}
	      ]
	    },
	    {
	      "type": "message",
	      "content": [
	        {"type": "input_text", "text": "missing role context"},
	        {"type": "input_image", "image_url": "https://example.test/missing-role.png"}
	      ]
	    },
	    {
	      "type": "function_call_output",
	      "call_id": "call-tool",
	      "output": [
	        {"type": "input_text", "text": "tool context"},
	        {"type": "input_image", "image_url": "https://example.test/tool.png"}
	      ]
	    },
	    {"type": "function_call", "arguments": "{}"},
	    {
	      "role": "user",
	      "content": [
	        {"type": "input_text", "text": "user context"},
	        {"type": "input_image", "file_id": "file-123"}
	      ]
	    }
	  ]
	}`)

	doc, err := parseResponses(body)
	if err != nil {
		t.Fatal(err)
	}
	images := doc.images()
	if len(images) != 5 {
		t.Fatalf("image count=%d, want 5", len(images))
	}
	wantMessageIndexes := []int{0, 1, 2, 3, 5}
	wantContexts := []string{"", "", "", "", "user context"}
	for i, image := range images {
		if image.messageIndex != wantMessageIndexes[i] || image.taskContext != wantContexts[i] {
			t.Fatalf(
				"image %d messageIndex=%d taskContext=%q, want messageIndex=%d taskContext=%q",
				i, image.messageIndex, image.taskContext, wantMessageIndexes[i], wantContexts[i],
			)
		}
	}

	rewritten, err := doc.rewrite([]string{
		"assistant description",
		"non-string role description",
		"missing role description",
		"tool description",
		"user description",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Input []struct {
			CallID  string `json:"call_id"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				FileID string `json:"file_id"`
			} `json:"content"`
			Output []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"output"`
		} `json:"input"`
	}
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatal(err)
	}
	if got.Input[0].Content[0].Text != "assistant context" ||
		got.Input[0].Content[1].Type != "input_text" ||
		got.Input[0].Content[1].Text != descriptionPrefix+"assistant description" {
		t.Fatalf("assistant image was not generically rewritten: %+v", got.Input[0].Content)
	}
	if got.Input[1].Content[0].Text != "non-string role context" ||
		got.Input[1].Content[1].Type != "input_text" ||
		got.Input[1].Content[1].Text != descriptionPrefix+"non-string role description" {
		t.Fatalf("non-string role image was not generically rewritten: %+v", got.Input[1].Content)
	}
	if got.Input[2].Content[0].Text != "missing role context" ||
		got.Input[2].Content[1].Type != "input_text" ||
		got.Input[2].Content[1].Text != descriptionPrefix+"missing role description" {
		t.Fatalf("missing role image was not generically rewritten: %+v", got.Input[2].Content)
	}
	if got.Input[3].CallID != "call-tool" ||
		got.Input[3].Output[0].Text != "tool context" ||
		got.Input[3].Output[1].Type != "input_text" ||
		got.Input[3].Output[1].Text != descriptionPrefix+"tool description" {
		t.Fatalf("function output image was not rewritten: %+v", got.Input[3])
	}
	if got.Input[5].Content[1].Type != "input_text" || got.Input[5].Content[1].Text != evidencePrefix+"user description" {
		t.Fatalf("user image was not rewritten with evidence prefix: %+v", got.Input[5].Content[1])
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
	rewritten, err := doc.rewrite(nil)
	if err != nil || !strings.Contains(string(rewritten), `"content":[]`) {
		t.Fatalf("rewritten=%s err=%v", rewritten, err)
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

func TestParseResponsesRejectsInvalidInputImageSourceInSupportedContainers(t *testing.T) {
	items := []string{
		`{"type":"message","role":"assistant","content":[{"type":"input_image"}]}`,
		`{"type":"message","role":42,"content":[{"type":"input_image"}]}`,
		`{"type":"message","content":[{"type":"input_image"}]}`,
		`{"type":"function_call_output","call_id":"call-1","output":[{"type":"input_image"}]}`,
	}
	for _, item := range items {
		t.Run(item, func(t *testing.T) {
			_, err := parseResponses([]byte(`{"input":[` + item + `]}`))
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
			Transport:           profile.VisionTransportOpenAIResponses,
			UnlistedModelPolicy: profile.UnlistedModelEnhance,
			Prompt:              "describe",
			Timeout:             time.Second,
			MaxConcurrency:      2,
		},
		fake,
		newResultCache(32, time.Minute),
	)
	body := []byte(`{
		"model":"main",
		"stream":true,
		"input":[
			{"role":"user","content":[
				{"type":"input_text","text":"inspect"},
				{"type":"input_image","file_id":"file-123"}
			]},
			{"type":"function_call_output","call_id":"call-1","output":[
				{"type":"input_image","image_url":"data:image/png;base64,aW1hZ2U="}
			]}
		]
	}`)

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
			Output []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"output"`
		} `json:"input"`
	}
	if err := json.Unmarshal(got, &request); err != nil {
		t.Fatal(err)
	}
	if !request.Stream {
		t.Fatal("stream setting was not preserved")
	}
	if request.Input[0].Content[1].Type != "input_text" ||
		request.Input[0].Content[1].Text != evidencePrefix+"a diagram" {
		t.Fatalf("rewritten content=%+v", request.Input[0].Content[1])
	}
	if request.Input[1].Output[0].Type != "input_text" ||
		request.Input[1].Output[0].Text != descriptionPrefix+"a diagram" {
		t.Fatalf("rewritten function output=%+v", request.Input[1].Output[0])
	}
}

func TestOpenAIPreprocessorUsesEntranceOperationInsteadOfTopLevelMessages(t *testing.T) {
	fake := &fakeDescriber{describe: func(imageRef) (string, error) {
		return "response image", nil
	}}
	p := newPreprocessorForProtocol(
		profile.ProtocolOpenAI,
		"provider",
		nil,
		profile.VisionRuntime{
			Model:               "vision-model",
			Transport:           profile.VisionTransportOpenAIResponses,
			UnlistedModelPolicy: profile.UnlistedModelEnhance,
			Prompt:              "describe",
			Timeout:             time.Second,
			MaxConcurrency:      1,
		},
		fake,
		newResultCache(8, time.Minute),
	)
	body := []byte(`{
		"model":"main",
		"messages":[],
		"input":[{"role":"user","content":[
			{"type":"input_image","image_url":"https://example.test/image.png"}
		]}]
	}`)

	got, err := p.ProcessOperationTargetWithBudget(
		context.Background(),
		nil,
		llmrequest.OperationOpenAIResponses,
		body,
		"https://example.test/v1/responses",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte(`"type":"input_image"`)) ||
		!bytes.Contains(got, []byte("response image")) {
		t.Fatalf("rewritten=%s", got)
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
			Transport:           profile.VisionTransportOpenAIResponses,
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
