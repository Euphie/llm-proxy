package llmrequest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
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

func TestChatAssistantToolCallHistoryAllowsNullContent(t *testing.T) {
	body := []byte(`{"model":"main","messages":[{"role":"user","content":"check weather"},{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"weather","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call-1","content":"sunny"}]}`)
	document, err := Parse(OperationOpenAIChatCompletions, body)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(document.Texts(), "\n"); got != "check weather" {
		t.Fatalf("texts=%q", got)
	}
	rewritten, err := document.RewriteImages(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]byte{[]byte(`"content":null`), []byte(`"tool_calls"`), []byte(`"tool_call_id":"call-1"`)} {
		if !bytes.Contains(rewritten, want) {
			t.Fatalf("rewritten request lost %s: %s", want, rewritten)
		}
	}
}

func TestChatRejectsNullContentWithoutAssistantToolCalls(t *testing.T) {
	tests := []string{
		`{"messages":[{"role":"user","content":null}]}`,
		`{"messages":[{"role":"assistant","content":null}]}`,
		`{"messages":[{"role":"assistant","content":null,"tool_calls":[]}]}`,
	}
	for _, body := range tests {
		if _, err := Parse(OperationOpenAIChatCompletions, []byte(body)); err == nil {
			t.Fatalf("Parse(%s) error=nil", body)
		}
	}
}

func TestResponsesEasyInputMessageAllowsStringContent(t *testing.T) {
	body := []byte(`{"model":"main","input":[{"role":"user","content":"first question"},{"type":"message","role":"user","content":"second question","custom":"keep"}]}`)
	document, err := Parse(OperationOpenAIResponses, body)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(document.Texts(), "\n"); got != "first question\nsecond question" {
		t.Fatalf("texts=%q", got)
	}
	rewritten, err := document.RewriteImages(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]byte{[]byte(`"content":"first question"`), []byte(`"content":"second question"`), []byte(`"custom":"keep"`)} {
		if !bytes.Contains(rewritten, want) {
			t.Fatalf("rewritten request lost %s: %s", want, rewritten)
		}
	}
}

func TestResponsesEasyInputMessageRejectsMalformedContent(t *testing.T) {
	for _, content := range []string{`null`, `{}`, `42`, `true`} {
		body := `{"input":[{"role":"user","content":` + content + `}]}`
		if _, err := Parse(OperationOpenAIResponses, []byte(body)); err == nil {
			t.Fatalf("Parse(%s) error=nil", body)
		}
	}
}

func TestDocumentExtractsOnlyCanonicalActualAndForcedToolOperations(t *testing.T) {
	tests := []struct {
		name      string
		operation Operation
		body      string
		want      []string
	}{
		{
			name: "anthropic", operation: OperationAnthropicMessages,
			body: `{"tools":[{"name":"advertised_delete"}],"tool_choice":{"type":"tool","name":"forced_deploy"},"messages":[{"role":"user","content":[{"type":"tool_use","name":"user_lookalike","input":{}}]},{"role":"assistant","content":[{"type":"tool_use","name":"write_file","input":{}},{"type":"tool_use","name":"WRITE_FILE","input":{}}]}]}`,
			want: []string{"forced_deploy", "write_file"},
		},
		{
			name: "chat completions", operation: OperationOpenAIChatCompletions,
			body: `{"tools":[{"type":"function","function":{"name":"advertised_delete"}}],"tool_choice":{"type":"function","function":{"name":"forced_exec"}},"messages":[{"role":"user","content":"safe","tool_calls":[{"function":{"name":"user_lookalike"}}]},{"role":"assistant","content":null,"tool_calls":[{"type":"function","function":{"name":"apply_patch","arguments":"{}"}}]}]}`,
			want: []string{"forced_exec", "apply_patch"},
		},
		{
			name: "responses", operation: OperationOpenAIResponses,
			body: `{"tools":[{"type":"function","name":"advertised_delete"}],"tool_choice":{"type":"function","name":"forced_transfer"},"input":[{"type":"function_call","name":"database_mutation","arguments":"{}"},{"type":"input_text","text":"safe"}]}`,
			want: []string{"forced_transfer", "database_mutation"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document, err := Parse(tt.operation, []byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if got := document.ActualToolOperations(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("operations=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestDocumentAdvertisedToolDefinitionsDoNotBecomeActualOperations(t *testing.T) {
	document, err := Parse(OperationOpenAIResponses, []byte(`{"tools":[{"type":"function","name":"delete_database"}],"input":"check weather"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := document.ActualToolOperations(); len(got) != 0 {
		t.Fatalf("operations=%v", got)
	}
}

func TestDocumentSeparatesCurrentAndHistoricalEvidence(t *testing.T) {
	tests := []struct {
		name      string
		operation Operation
		body      []byte
	}{
		{
			name: "anthropic", operation: OperationAnthropicMessages,
			body: []byte(`{
				"tool_choice":{"type":"tool","name":"deploy_production"},
				"messages":[
					{"role":"user","content":"旧问题"},
					{"role":"assistant","content":[{"type":"tool_use","name":"shell_exec","input":{}}]},
					{"role":"user","content":"继续分析"}
				]
			}`),
		},
		{
			name: "chat completions", operation: OperationOpenAIChatCompletions,
			body: []byte(`{
				"tool_choice":{"type":"function","function":{"name":"deploy_production"}},
				"messages":[
					{"role":"user","content":"旧问题"},
					{"role":"assistant","content":null,"tool_calls":[{"type":"function","function":{"name":"shell_exec","arguments":"{}"}}]},
					{"role":"user","content":"继续分析"}
				]
			}`),
		},
		{
			name: "responses", operation: OperationOpenAIResponses,
			body: []byte(`{
				"tool_choice":{"type":"function","name":"deploy_production"},
				"input":[
					{"type":"message","role":"user","content":"旧问题"},
					{"type":"function_call","name":"shell_exec","arguments":"{}"},
					{"type":"message","role":"user","content":"继续分析"}
				]
			}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document, err := Parse(tt.operation, tt.body)
			if err != nil {
				t.Fatal(err)
			}
			if got := document.HistoricalToolOperations(); !reflect.DeepEqual(got, []string{"shell_exec"}) {
				t.Fatalf("historical operations=%v", got)
			}
			if got := document.ForcedToolOperation(); got != "deploy_production" {
				t.Fatalf("forced operation=%q", got)
			}
			if got := document.LatestUserTexts(); !reflect.DeepEqual(got, []string{"继续分析"}) {
				t.Fatalf("latest user texts=%v", got)
			}
		})
	}
}

func TestActualToolOperationsAreBoundedAndDeduplicated(t *testing.T) {
	blocks := make([]map[string]string, maxActualToolOperations+10)
	blocks[0] = map[string]string{"type": "tool_use", "name": strings.Repeat("x", maxActualToolNameRunes+20)}
	blocks[1] = map[string]string{"type": "tool_use", "name": strings.Repeat("X", maxActualToolNameRunes+20)}
	for index := 2; index < len(blocks); index++ {
		blocks[index] = map[string]string{"type": "tool_use", "name": fmt.Sprintf("tool_%d", index)}
	}
	body, err := json.Marshal(map[string]any{
		"messages": []any{map[string]any{"role": "assistant", "content": blocks}},
	})
	if err != nil {
		t.Fatal(err)
	}
	document, err := Parse(OperationAnthropicMessages, body)
	if err != nil {
		t.Fatal(err)
	}
	operations := document.ActualToolOperations()
	if len(operations) != maxActualToolOperations || len([]rune(operations[0])) != maxActualToolNameRunes {
		t.Fatalf("operations count=%d first length=%d", len(operations), len([]rune(operations[0])))
	}
}

func TestNamedForcedToolOperationWinsAtCapacityForEveryProtocol(t *testing.T) {
	tests := []struct {
		name      string
		operation Operation
		body      func(string) []byte
	}{
		{
			name: "anthropic", operation: OperationAnthropicMessages,
			body: func(forced string) []byte {
				blocks := make([]map[string]any, maxActualToolOperations)
				for index := range blocks {
					blocks[index] = map[string]any{"type": "tool_use", "name": fmt.Sprintf("operation_%02d", index), "input": map[string]any{}}
				}
				return marshalDocumentTestBody(t, map[string]any{
					"tool_choice": map[string]any{"type": "tool", "name": forced},
					"messages":    []any{map[string]any{"role": "assistant", "content": blocks}},
				})
			},
		},
		{
			name: "chat completions", operation: OperationOpenAIChatCompletions,
			body: func(forced string) []byte {
				calls := make([]map[string]any, maxActualToolOperations)
				for index := range calls {
					calls[index] = map[string]any{"type": "function", "function": map[string]any{"name": fmt.Sprintf("operation_%02d", index), "arguments": "{}"}}
				}
				return marshalDocumentTestBody(t, map[string]any{
					"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": forced}},
					"messages":    []any{map[string]any{"role": "assistant", "content": nil, "tool_calls": calls}},
				})
			},
		},
		{
			name: "responses", operation: OperationOpenAIResponses,
			body: func(forced string) []byte {
				input := make([]map[string]any, maxActualToolOperations)
				for index := range input {
					input[index] = map[string]any{"type": "function_call", "name": fmt.Sprintf("operation_%02d", index), "arguments": "{}"}
				}
				return marshalDocumentTestBody(t, map[string]any{
					"tool_choice": map[string]any{"type": "function", "name": forced},
					"input":       input,
				})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document, err := Parse(tt.operation, tt.body("forced_operation"))
			if err != nil {
				t.Fatal(err)
			}
			operations := document.ActualToolOperations()
			if len(operations) != maxActualToolOperations || operations[0] != "forced_operation" ||
				containsFold(operations, "operation_63") {
				t.Fatalf("unique forced operations=%v", operations)
			}

			document, err = Parse(tt.operation, tt.body("OPERATION_10"))
			if err != nil {
				t.Fatal(err)
			}
			operations = document.ActualToolOperations()
			if len(operations) != maxActualToolOperations || operations[0] != "operation_10" ||
				countFold(operations, "operation_10") != 1 || !containsFold(operations, "operation_63") {
				t.Fatalf("duplicate forced operations=%v", operations)
			}
		})
	}
}

func marshalDocumentTestBody(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func containsFold(values []string, want string) bool {
	return countFold(values, want) > 0
}

func countFold(values []string, want string) int {
	count := 0
	for _, value := range values {
		if strings.EqualFold(value, want) {
			count++
		}
	}
	return count
}

func TestCanonicalEstimationJSONReplacesOnlyProtocolImageBase64Payload(t *testing.T) {
	large := strings.Repeat("A", 1024)
	body := `{"metadata":{"image_url":"data:image/png;base64,` + large + `"},"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + large + `"}},{"type":"text","text":"ordinary text"}]}]}`
	document, err := Parse(OperationOpenAIChatCompletions, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	got, err := document.CanonicalEstimationJSON()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(got, []byte(large)) != 1 || !bytes.Contains(got, []byte("data:image/png;base64,[image-data]")) ||
		!bytes.Contains(got, []byte("ordinary text")) {
		t.Fatalf("canonical estimation JSON=%s", got)
	}
}

func TestCanonicalEstimationJSONSanitizesAllProtocolBase64ImageShapes(t *testing.T) {
	const payload = "QUJDREVGR0hJSktMTU5PUA=="
	tests := []struct {
		operation Operation
		body      string
	}{
		{OperationAnthropicMessages, `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + payload + `"}}]}]}`},
		{OperationOpenAIChatCompletions, `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + payload + `"}}]}]}`},
		{OperationOpenAIResponses, `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,` + payload + `"}]}]}`},
	}
	for _, tt := range tests {
		document, err := Parse(tt.operation, []byte(tt.body))
		if err != nil {
			t.Fatal(err)
		}
		got, err := document.CanonicalEstimationJSON()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(got, []byte(payload)) || !bytes.Contains(got, []byte(imageEstimationMarker)) {
			t.Fatalf("operation=%s envelope=%s", tt.operation, got)
		}
	}
}
