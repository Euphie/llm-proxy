package llmrequest

import (
	"bytes"
	"testing"
)

func TestDocumentExposesCanonicalImageSourcesAndRewritesProtocolBlocks(t *testing.T) {
	tests := []struct {
		name       string
		operation  Operation
		body       string
		wantKinds  []ImageSourceKind
		imageTypes [][]byte
	}{
		{
			name: "anthropic messages", operation: OperationAnthropicMessages,
			body:       `{"model":"main","metadata":{"type":"image"},"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1n"}},{"type":"image","source":{"type":"url","url":"https://example.test/a.png"}},{"type":"image","source":{"type":"file","file_id":"file-1"}}]}]}`,
			wantKinds:  []ImageSourceKind{ImageSourceBase64, ImageSourceURL, ImageSourceFileID},
			imageTypes: [][]byte{[]byte(`"type":"image"`)},
		},
		{
			name: "openai chat completions", operation: OperationOpenAIChatCompletions,
			body:       `{"model":"main","metadata":{"type":"image_url"},"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1n"}},{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}]}]}`,
			wantKinds:  []ImageSourceKind{ImageSourceDataURL, ImageSourceURL},
			imageTypes: [][]byte{[]byte(`"type":"image_url"`)},
		},
		{
			name: "openai responses", operation: OperationOpenAIResponses,
			body:       `{"model":"main","metadata":{"type":"input_image"},"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/a.png"},{"type":"input_image","image_url":"data:image/png;base64,aW1n"},{"type":"input_image","file_id":"file-1"}]}]}`,
			wantKinds:  []ImageSourceKind{ImageSourceURL, ImageSourceDataURL, ImageSourceFileID},
			imageTypes: [][]byte{[]byte(`"type":"input_image"`)},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document, err := Parse(test.operation, []byte(test.body))
			if err != nil {
				t.Fatal(err)
			}
			images := document.Images()
			if len(images) != len(test.wantKinds) {
				t.Fatalf("images=%d, want %d", len(images), len(test.wantKinds))
			}
			replacements := make([]string, len(images))
			for index, want := range test.wantKinds {
				if images[index].SourceKind != want {
					t.Fatalf("image %d source=%q, want %q", index, images[index].SourceKind, want)
				}
				replacements[index] = "description"
			}
			rewritten, err := document.RewriteImages(replacements)
			if err != nil {
				t.Fatal(err)
			}
			for _, imageType := range test.imageTypes {
				if bytes.Count(rewritten, imageType) != 1 {
					t.Fatalf("rewritten protocol image type count=%d, want metadata only: %s", bytes.Count(rewritten, imageType), rewritten)
				}
			}
			if bytes.Count(rewritten, []byte("description")) != len(images) {
				t.Fatalf("rewritten descriptions=%d, want %d: %s", bytes.Count(rewritten, []byte("description")), len(images), rewritten)
			}
		})
	}
}
