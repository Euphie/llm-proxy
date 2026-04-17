package stats

import (
	"bytes"
	"encoding/json"
)

// OpenAIParser parses token usage from OpenAI Chat Completions API responses.
//
// Non-streaming response (always available):
//
//	{"model":"gpt-4o","usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}
//
// Streaming SSE response: usage is only present when the request includes
//
//	"stream_options": {"include_usage": true}
//
// In that case the final SSE chunk carries:
//
//	data: {"choices":[],"model":"gpt-4o","usage":{"prompt_tokens":10,"completion_tokens":20}}
type OpenAIParser struct{}

func (OpenAIParser) Parse(data []byte) (Usage, bool) {
	var u Usage

	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)

		var jsonData []byte
		switch {
		case bytes.HasPrefix(line, []byte("data: ")):
			payload := bytes.TrimPrefix(line, []byte("data: "))
			// Skip the [DONE] sentinel
			if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
				continue
			}
			jsonData = payload
		case len(line) > 0 && line[0] == '{':
			jsonData = line
		default:
			continue
		}

		var obj map[string]json.RawMessage
		if err := json.Unmarshal(jsonData, &obj); err != nil {
			continue
		}

		// "model" at root level
		if raw, ok := obj["model"]; ok && u.Model == "" {
			_ = json.Unmarshal(raw, &u.Model)
		}

		// "usage" at root level (non-streaming or final streaming chunk)
		if raw, ok := obj["usage"]; ok {
			var us struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			}
			if json.Unmarshal(raw, &us) == nil {
				if us.PromptTokens > 0 {
					u.InputTokens = us.PromptTokens
				}
				if us.CompletionTokens > 0 {
					u.OutputTokens = us.CompletionTokens
				}
			}
		}
	}

	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return Usage{}, false
	}
	return u, true
}
