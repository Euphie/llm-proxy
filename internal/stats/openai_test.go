package stats

import "testing"

func TestOpenAIParserParsesResponsesJSON(t *testing.T) {
	body := []byte(`{
	  "id": "resp_123",
	  "model": "gpt-5.4",
	  "output": [],
	  "usage": {
	    "input_tokens": 41,
	    "input_tokens_details": {"cached_tokens": 17, "cache_write_tokens": 13},
	    "output_tokens": 23,
	    "output_tokens_details": {"reasoning_tokens": 11},
	    "total_tokens": 64
	  }
	}`)

	got, ok := (OpenAIParser{}).Parse(body)
	if !ok {
		t.Fatal("Responses usage was not parsed")
	}
	want := Usage{
		Present: true, InputPresent: true, OutputPresent: true,
		Model:               "gpt-5.4",
		InputTokens:         41,
		OutputTokens:        23,
		CacheReadTokens:     17,
		CacheCreationTokens: 13,
	}
	if got != want {
		t.Fatalf("usage=%+v, want %+v", got, want)
	}
}

func TestOpenAIParserParsesCompletedResponsesSSE(t *testing.T) {
	body := []byte("event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_123","model":"gpt-5.4","usage":null}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_123","model":"gpt-5.4","usage":{"input_tokens":31,"input_tokens_details":{"cached_tokens":9,"cache_write_tokens":7},"output_tokens":19,"total_tokens":50}}}` + "\n")

	got, ok := (OpenAIParser{}).Parse(body)
	if !ok {
		t.Fatal("streaming Responses usage was not parsed")
	}
	want := Usage{
		Present: true, InputPresent: true, OutputPresent: true,
		Model:               "gpt-5.4",
		InputTokens:         31,
		OutputTokens:        19,
		CacheReadTokens:     9,
		CacheCreationTokens: 7,
	}
	if got != want {
		t.Fatalf("usage=%+v, want %+v", got, want)
	}
}

func TestOpenAIParserRecognizesExplicitZeroUsageAndRejectsNegativeTokens(t *testing.T) {
	usage, ok := (OpenAIParser{}).Parse([]byte(`{"usage":{"prompt_tokens":0,"completion_tokens":0}}`))
	if !ok || !usage.Present || !usage.InputPresent || !usage.OutputPresent ||
		usage.InputTokens != 0 || usage.OutputTokens != 0 {
		t.Fatalf("usage=%+v ok=%v", usage, ok)
	}
	if _, ok := (OpenAIParser{}).Parse([]byte(`{"usage":{"input_tokens":3,"output_tokens":-1}}`)); ok {
		t.Fatal("negative token usage was accepted")
	}
}
