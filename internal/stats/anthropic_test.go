package stats

import "testing"

func TestAnthropicParserParsesPrettyPrintedJSONOnce(t *testing.T) {
	body := []byte(`{
	  "model": "sonnet",
	  "content": [{"type": "text", "text": "description"}],
	  "usage": {
	    "input_tokens": 10,
	    "output_tokens": 20,
	    "cache_read_input_tokens": 3,
	    "cache_creation_input_tokens": 4
	  }
	}`)

	got, ok := (AnthropicParser{}).Parse(body)
	if !ok {
		t.Fatal("pretty-printed usage was not parsed")
	}
	want := Usage{
		Present: true, InputPresent: true, OutputPresent: true,
		Model:               "sonnet",
		InputTokens:         10,
		OutputTokens:        20,
		CacheReadTokens:     3,
		CacheCreationTokens: 4,
	}
	if got != want {
		t.Fatalf("usage=%+v, want %+v", got, want)
	}
}

func TestAnthropicParserRecognizesExplicitZeroUsageAndRejectsNegativeTokens(t *testing.T) {
	usage, ok := (AnthropicParser{}).Parse([]byte(`{"usage":{"input_tokens":0,"output_tokens":0}}`))
	if !ok || !usage.Present || !usage.InputPresent || !usage.OutputPresent ||
		usage.InputTokens != 0 || usage.OutputTokens != 0 {
		t.Fatalf("usage=%+v ok=%v", usage, ok)
	}
	if _, ok := (AnthropicParser{}).Parse([]byte(`{"usage":{"input_tokens":3,"output_tokens":-1}}`)); ok {
		t.Fatal("negative token usage was accepted")
	}
}

func TestAnthropicParserKeepsPartialSSEUsageIncomplete(t *testing.T) {
	usage, ok := (AnthropicParser{}).Parse([]byte(
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}` + "\n\n",
	))
	if !ok || !usage.InputPresent || usage.OutputPresent {
		t.Fatalf("usage=%+v ok=%v", usage, ok)
	}
}

func TestAnthropicParserFallsBackToSSEWithoutDoubleCounting(t *testing.T) {
	body := []byte("event: message_start\n" +
		`data: {"type":"message_start","message":{"model":"sonnet","usage":{"input_tokens":10}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":20}}` + "\n")

	got, ok := (AnthropicParser{}).Parse(body)
	if !ok {
		t.Fatal("SSE usage was not parsed")
	}
	if got.Model != "sonnet" || got.InputTokens != 10 || got.OutputTokens != 20 {
		t.Fatalf("usage=%+v", got)
	}
}

func TestAnthropicParserUsesCumulativeMessageDeltaOutputTokens(t *testing.T) {
	body := []byte("event: message_start\n" +
		`data: {"type":"message_start","message":{"model":"sonnet","usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":20}}` + "\n")

	got, ok := (AnthropicParser{}).Parse(body)
	if !ok {
		t.Fatal("SSE usage was not parsed")
	}
	if got.InputTokens != 10 || got.OutputTokens != 20 ||
		got.CacheReadTokens != 3 || got.CacheCreationTokens != 4 {
		t.Fatalf("usage=%+v", got)
	}
}
