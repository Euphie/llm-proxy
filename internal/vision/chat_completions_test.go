package vision

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseChatCompletionsRewritesImageURLsInPlace(t *testing.T) {
	body := []byte(`{
	  "model": "main-model",
	  "stream": true,
	  "metadata": {"trace": "keep"},
	  "messages": [
	    {"role":"system","content":"You are helpful."},
	    {
	      "role": "user",
	      "name": "operator",
	      "content": [
	        {"type":"text","text":"before"},
	        {"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA","detail":"high"}},
	        {"type":"custom","value":"keep"},
	        {"type":"image_url","image_url":{"url":"https://example.test/a.png"}},
	        {"type":"text","text":"after"}
	      ]
	    },
	    {
	      "role": "assistant",
	      "content": [
	        {"type":"image_url","image_url":{"url":"https://example.test/assistant.png","detail":"low"}}
	      ]
	    }
	  ]
	}`)

	doc, err := parseChatCompletions(body)
	if err != nil {
		t.Fatal(err)
	}
	images := doc.images()
	if len(images) != 3 {
		t.Fatalf("image count=%d, want 3", len(images))
	}
	if images[0].sourceType != "data_url" || images[0].detail != "high" ||
		images[1].sourceType != "url" || images[1].detail != "auto" ||
		images[2].sourceType != "url" || images[2].detail != "low" {
		t.Fatalf("images=%+v", images)
	}
	if images[0].taskContext != "before\nafter" || images[1].taskContext != "before\nafter" {
		t.Fatalf("user task contexts=%q, %q", images[0].taskContext, images[1].taskContext)
	}
	if images[2].taskContext != "" {
		t.Fatalf("assistant task context=%q, want empty", images[2].taskContext)
	}

	rewritten, err := doc.rewrite([]string{"first description", "second description", "assistant description"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Stream   bool              `json:"stream"`
		Metadata map[string]string `json:"metadata"`
		Messages []struct {
			Role    string `json:"role"`
			Name    string `json:"name"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Stream || got.Metadata["trace"] != "keep" || got.Messages[1].Name != "operator" {
		t.Fatalf("root or message fields were not preserved: %+v", got)
	}

	userContent, ok := got.Messages[1].Content.([]any)
	if !ok || len(userContent) != 5 {
		t.Fatalf("user content=%#v", got.Messages[1].Content)
	}
	userImage, ok := userContent[1].(map[string]any)
	if !ok || userImage["type"] != "text" || userImage["text"] != evidencePrefix+"first description" {
		t.Fatalf("first rewritten image=%#v", userContent[1])
	}
	custom, ok := userContent[2].(map[string]any)
	if !ok || custom["type"] != "custom" || custom["value"] != "keep" {
		t.Fatalf("custom block=%#v", userContent[2])
	}
	secondImage, ok := userContent[3].(map[string]any)
	if !ok || secondImage["type"] != "text" || secondImage["text"] != evidencePrefix+"second description" {
		t.Fatalf("second rewritten image=%#v", userContent[3])
	}

	assistantContent, ok := got.Messages[2].Content.([]any)
	if !ok || len(assistantContent) != 1 {
		t.Fatalf("assistant content=%#v", got.Messages[2].Content)
	}
	assistantImage, ok := assistantContent[0].(map[string]any)
	if !ok || assistantImage["type"] != "text" ||
		assistantImage["text"] != descriptionPrefix+"assistant description" {
		t.Fatalf("assistant rewritten image=%#v", assistantContent[0])
	}
	if strings.Contains(string(rewritten), `"type":"image_url"`) {
		t.Fatalf("rewritten request still contains image blocks: %s", rewritten)
	}
}

func TestParseChatCompletionsRejectsInvalidImageURL(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "image url must be object",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":"https://example.test/a.png"}]}]}`,
		},
		{
			name: "url is required",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"detail":"high"}}]}]}`,
		},
		{
			name: "detail is validated",
			body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/a.png","detail":"maximum"}}]}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseChatCompletions([]byte(test.body))
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
