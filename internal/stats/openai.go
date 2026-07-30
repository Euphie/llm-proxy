package stats

import (
	"bytes"
	"encoding/json"
)

// OpenAIParser parses token usage from OpenAI Chat Completions and Responses
// API responses.
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
//
// OpenAI also reports cache reads and writes in token detail objects:
//
//	{"usage":{"input_tokens":100,"output_tokens":50,"input_tokens_details":{"cached_tokens":80,"cache_write_tokens":0}}}
type OpenAIParser struct{}

func (OpenAIParser) Parse(data []byte) (Usage, bool) {
	var u Usage
	var whole map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(data), &whole); err == nil && whole != nil {
		accumulateOpenAIUsage(&u, whole)
		return validOpenAIUsage(u)
	}

	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)

		var jsonData []byte
		switch {
		case bytes.HasPrefix(line, []byte("data:")):
			payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			// Skip the [DONE] sentinel
			if bytes.Equal(payload, []byte("[DONE]")) {
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
		accumulateOpenAIUsage(&u, obj)
	}

	return validOpenAIUsage(u)
}

func accumulateOpenAIUsage(u *Usage, obj map[string]json.RawMessage) {
	if raw, ok := obj["model"]; ok && u.Model == "" {
		_ = json.Unmarshal(raw, &u.Model)
	}

	if raw, ok := obj["usage"]; ok {
		var usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
			PromptDetails    struct {
				CachedTokens     int `json:"cached_tokens"`
				CacheWriteTokens int `json:"cache_write_tokens"`
			} `json:"prompt_tokens_details"`
			InputDetails struct {
				CachedTokens     int `json:"cached_tokens"`
				CacheWriteTokens int `json:"cache_write_tokens"`
			} `json:"input_tokens_details"`
		}
		if json.Unmarshal(raw, &usage) == nil {
			if usage.PromptTokens > 0 {
				u.InputTokens = usage.PromptTokens
			}
			if usage.InputTokens > 0 {
				u.InputTokens = usage.InputTokens
			}
			if usage.CompletionTokens > 0 {
				u.OutputTokens = usage.CompletionTokens
			}
			if usage.OutputTokens > 0 {
				u.OutputTokens = usage.OutputTokens
			}
			if usage.PromptDetails.CachedTokens > 0 {
				u.CacheReadTokens = usage.PromptDetails.CachedTokens
			}
			if usage.InputDetails.CachedTokens > 0 {
				u.CacheReadTokens = usage.InputDetails.CachedTokens
			}
			if usage.PromptDetails.CacheWriteTokens > 0 {
				u.CacheCreationTokens = usage.PromptDetails.CacheWriteTokens
			}
			if usage.InputDetails.CacheWriteTokens > 0 {
				u.CacheCreationTokens = usage.InputDetails.CacheWriteTokens
			}
		}
	}

	if raw, ok := obj["response"]; ok {
		var response map[string]json.RawMessage
		if json.Unmarshal(raw, &response) == nil {
			accumulateOpenAIUsage(u, response)
		}
	}
}

func validOpenAIUsage(u Usage) (Usage, bool) {
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return Usage{}, false
	}
	return u, true
}
