package vision

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestParseAndRewriteImages(t *testing.T) {
	body := []byte(`{
	  "model":"main-model",
	  "metadata":{"user_id":"u-1"},
	  "messages":[{"role":"user","custom":"keep","content":[
	    {"type":"text","text":"before"},
	    {"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1n"}},
	    {"type":"image","source":{"type":"url","url":"https://example.test/a.png"}},
	    {"type":"image","source":{"type":"file","file_id":"file_123"}},
	    {"type":"text","text":"after"}
	  ]}]
	}`)

	doc, err := parseMessages(body)
	if err != nil {
		t.Fatal(err)
	}
	images := doc.images()
	if len(images) != 3 {
		t.Fatalf("images=%d", len(images))
	}
	if images[0].sourceType != "base64" || images[1].sourceType != "url" || images[2].sourceType != "file" {
		t.Fatalf("wrong order: %#v", images)
	}

	rewritten, err := doc.rewrite([]string{"base64 desc", "url desc", "file desc"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatal(err)
	}
	if got["metadata"].(map[string]any)["user_id"] != "u-1" {
		t.Fatal("unknown top-level field was lost")
	}
	if got["messages"].([]any)[0].(map[string]any)["custom"] != "keep" {
		t.Fatal("unknown message field was lost")
	}
	blocks := got["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(blocks) != 5 {
		t.Fatalf("blocks=%d", len(blocks))
	}
	for i, want := range []string{"base64 desc", "url desc", "file desc"} {
		text := blocks[i+1].(map[string]any)["text"].(string)
		if text != evidencePrefix+want {
			t.Fatalf("block %d text=%q", i+1, text)
		}
	}
	if blocks[0].(map[string]any)["text"] != "before" || blocks[4].(map[string]any)["text"] != "after" {
		t.Fatal("original text blocks changed")
	}
}

func TestParseMessagesAttachesOnlySameUserMessageContext(t *testing.T) {
	doc, err := parseMessages([]byte(`{
	  "messages":[
	    {"role":"assistant","content":[
	      {"type":"text","text":"ignore assistant"},
	      {"type":"image","source":{"type":"url","url":"https://example.test/a.png"}}
	    ]},
	    {"role":"user","content":[
	      {"type":"text","text":"before"},
	      {"type":"image","source":{"type":"url","url":"https://example.test/b.png"}},
	      {"type":"text","text":"after"},
	      {"type":"image","source":{"type":"url","url":"https://example.test/c.png"}}
	    ]}
	  ]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	images := doc.images()
	if images[0].taskContext != "" ||
		images[1].taskContext != "before\nafter" ||
		images[2].taskContext != "before\nafter" {
		t.Fatalf("contexts=%q, %q, %q",
			images[0].taskContext, images[1].taskContext, images[2].taskContext)
	}

	rewritten, err := doc.rewrite([]string{"assistant", "first user", "second user"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatal(err)
	}
	if got.Messages[0].Content[0].Text != "ignore assistant" ||
		got.Messages[1].Content[0].Text != "before" ||
		got.Messages[1].Content[2].Text != "after" {
		t.Fatal("original text blocks changed")
	}
	if got.Messages[0].Content[1].Text != descriptionPrefix+"assistant" ||
		got.Messages[1].Content[1].Text != evidencePrefix+"first user" ||
		got.Messages[1].Content[3].Text != evidencePrefix+"second user" {
		t.Fatalf("rewritten messages=%#v", got.Messages)
	}
}

func TestParseMessagesDoesNotAttachContextForNonStringUserRole(t *testing.T) {
	doc, err := parseMessages([]byte(`{"messages":[{"role":true,"content":[
		{"type":"text","text":"ignore"},
		{"type":"image","source":{"type":"url","url":"https://example.test/a.png"}}
	]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if images := doc.images(); len(images) != 1 || images[0].taskContext != "" {
		t.Fatalf("images=%#v", images)
	}
}

func TestParseMessagesIgnoresNestedToolResultContextForUserMessage(t *testing.T) {
	doc, err := parseMessages([]byte(`{"messages":[{"role":"user","content":[
		{"type":"text","text":"top-level question"},
		{"type":"tool_result","content":[
			{"type":"text","text":"nested instruction"},
			{"type":"image","source":{"type":"url","url":"https://example.test/nested.png"}}
		]},
		{"type":"image","source":{"type":"url","url":"https://example.test/top-level.png"}}
	]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	images := doc.images()
	if len(images) != 1 || images[0].cachePayload != "https://example.test/top-level.png" {
		t.Fatalf("images=%#v", images)
	}
	if images[0].taskContext != "top-level question" {
		t.Fatalf("taskContext=%q", images[0].taskContext)
	}
}

func TestParseMessagesRootReusesDecodedRequest(t *testing.T) {
	root, err := parseRequestRoot([]byte(`{"model":"main","messages":[{"content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseMessagesRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	rewritten, err := doc.rewrite(nil)
	if err != nil || !strings.Contains(string(rewritten), `"content":"hello"`) {
		t.Fatalf("rewritten=%s err=%v", rewritten, err)
	}
}

func TestParseMessagesIgnoresTextContent(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"messages":[{"content":"plain text"}]}`),
		[]byte(`{"messages":[{"content":[{"type":"text","text":"plain text"}]}]}`),
	} {
		doc, err := parseMessages(body)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.images()) != 0 {
			t.Fatal("unexpected image")
		}
	}
}

func TestParseMessagesDoesNotRecurseIntoToolResultContent(t *testing.T) {
	body := []byte(`{"messages":[{"content":[
        {"type":"tool_result","content":[{"type":"image","source":{"type":"url","url":"https://example.test/nested.png"}}]},
        {"type":"image","source":{"type":"url","url":"https://example.test/top-level.png"}}
    ]}]}`)
	doc, err := parseMessages(body)
	if err != nil {
		t.Fatal(err)
	}
	images := doc.images()
	if len(images) != 1 || images[0].cachePayload != "https://example.test/top-level.png" {
		t.Fatalf("unexpected images: %#v", images)
	}
}

func TestParseMessagesRejectsInvalidImageSources(t *testing.T) {
	tests := []string{
		`{"type":"unknown"}`,
		`{"type":"base64","data":"x"}`,
		`{"type":"base64","media_type":"image/png"}`,
		`{"type":"url"}`,
		`{"type":"file"}`,
	}
	for _, source := range tests {
		t.Run(source, func(t *testing.T) {
			_, err := parseMessages([]byte(`{"messages":[{"content":[{"type":"image","source":` + source + `}]}]}`))
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestRewriteRejectsMismatchedDescriptions(t *testing.T) {
	doc, err := parseMessages([]byte(`{"messages":[{"content":[{"type":"image","source":{"type":"url","url":"https://example.test/a.png"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.rewrite(nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseDescription(t *testing.T) {
	got, err := parseDescription([]byte(`{"content":[{"type":"text","text":"first"},{"type":"tool_use","text":"ignored"},{"type":"text","text":" second "}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "first\n second " {
		t.Fatalf("description=%q", got)
	}
	if _, err := parseDescription([]byte(`{"content":[{"type":"text","text":" \n "}]}`)); err == nil {
		t.Fatal("expected error for empty response")
	}
}

func TestImageCacheKey(t *testing.T) {
	image := imageRef{sourceType: "url", cachePayload: "https://example.test/a.png"}
	key := imageCacheKey("provider", "model", "prompt", image)
	if key != imageCacheKey("provider", "model", "prompt", image) {
		t.Fatal("identical inputs must have identical keys")
	}
	for _, changed := range []string{
		imageCacheKey("other", "model", "prompt", image),
		imageCacheKey("provider", "other", "prompt", image),
		imageCacheKey("provider", "model", "other", image),
		imageCacheKey("provider", "model", "prompt", imageRef{sourceType: "file", cachePayload: image.cachePayload}),
		imageCacheKey("provider", "model", "prompt", imageRef{sourceType: image.sourceType, cachePayload: "other"}),
	} {
		if key == changed {
			t.Fatal("distinct inputs must have distinct keys")
		}
	}
}

func TestScopedImageCacheKeyFramesMultiValueHeaders(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	image := imageRef{sourceType: "file", cachePayload: "file_shared"}
	keyFor := func(values ...string) string {
		headers := make(http.Header)
		headers["Authorization"] = values
		return scopedImageCacheKey(
			secret, headers, anthropicForwardedHeaders[:],
			"provider", "model", "prompt", image,
		)
	}

	base := keyFor("ab", "c")
	for _, changed := range []string{
		keyFor("a", "bc"),
		keyFor("c", "ab"),
		keyFor("abc"),
		keyFor(),
		keyFor(""),
	} {
		if base == changed {
			t.Fatal("ambiguous multi-value header encoding produced the same scoped key")
		}
	}
	if base != keyFor("ab", "c") {
		t.Fatal("identical multi-value header domains must produce the same scoped key")
	}
}

func TestScopedImageCacheKeyIgnoresAnthropicBeta(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	image := imageRef{sourceType: "url", cachePayload: "https://example.test/a.png"}
	first := http.Header{"Anthropic-Beta": {"agent-teams"}}
	second := http.Header{"Anthropic-Beta": {"files-api-2025-04-14"}}

	firstKey := scopedImageCacheKey(
		secret, first, anthropicForwardedHeaders[:],
		"provider", "model", "prompt", image,
	)
	secondKey := scopedImageCacheKey(
		secret, second, anthropicForwardedHeaders[:],
		"provider", "model", "prompt", image,
	)
	if firstKey != secondKey {
		t.Fatal("non-forwarded Anthropic-Beta must not partition the cache")
	}
}
