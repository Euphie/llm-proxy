package stats

import (
	"bytes"
	"encoding/json"
)

// AnthropicParser parses token usage from Anthropic Messages API responses.
// It handles both streaming SSE bodies and plain JSON objects.
//
// Streaming events parsed:
//   - message_start  → message.usage.input_tokens, message.model
//   - message_delta  → usage.output_tokens (accumulated)
//
// Non-streaming fields parsed:
//   - usage.input_tokens, usage.output_tokens, model
type AnthropicParser struct{}

func (AnthropicParser) Parse(data []byte) (Usage, bool) {
	var u Usage

	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)

		var jsonData []byte
		switch {
		case bytes.HasPrefix(line, []byte("data: ")):
			jsonData = bytes.TrimPrefix(line, []byte("data: "))
		case len(line) > 0 && line[0] == '{':
			jsonData = line
		default:
			continue
		}

		var obj map[string]json.RawMessage
		if err := json.Unmarshal(jsonData, &obj); err != nil {
			continue
		}

		// Root-level "model" (non-streaming response or message_delta)
		if raw, ok := obj["model"]; ok && u.Model == "" {
			_ = json.Unmarshal(raw, &u.Model)
		}

		// Root-level "usage" (non-streaming response and message_delta)
		if raw, ok := obj["usage"]; ok {
			var us struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			}
			if json.Unmarshal(raw, &us) == nil {
				if us.InputTokens > 0 {
					u.InputTokens = us.InputTokens
				}
				if us.OutputTokens > 0 {
					u.OutputTokens += us.OutputTokens
				}
			}
		}

		// Streaming message_start: model and input_tokens nested under "message"
		if raw, ok := obj["message"]; ok {
			var msg struct {
				Model string `json:"model"`
				Usage struct {
					InputTokens int `json:"input_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(raw, &msg) == nil {
				if msg.Model != "" && u.Model == "" {
					u.Model = msg.Model
				}
				if msg.Usage.InputTokens > 0 {
					u.InputTokens = msg.Usage.InputTokens
				}
			}
		}
	}

	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return Usage{}, false
	}
	return u, true
}
