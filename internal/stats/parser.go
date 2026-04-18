package stats

// Usage holds the token counts and model name extracted from an API response.
type Usage struct {
	InputTokens          int
	OutputTokens         int
	CacheReadTokens      int
	CacheCreationTokens  int
	Model                string
}

// Parser extracts token usage from a raw API response body.
// The body may be a streaming SSE payload or a plain JSON object,
// depending on the upstream protocol.
type Parser interface {
	Parse(data []byte) (Usage, bool)
}

// NewParser returns the Parser for the given protocol name.
// Supported values: "anthropic" (default), "openai".
// Unknown protocols fall back to AnthropicParser.
func NewParser(protocol string) Parser {
	switch protocol {
	case "openai":
		return OpenAIParser{}
	default:
		return AnthropicParser{}
	}
}
