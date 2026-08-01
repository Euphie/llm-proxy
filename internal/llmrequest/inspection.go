// Package llmrequest describes supported inference request operations.
package llmrequest

import "encoding/json"

type Operation string

const (
	OperationAnthropicMessages     Operation = "anthropic_messages"
	OperationOpenAIChatCompletions Operation = "openai_chat_completions"
	OperationOpenAIResponses       Operation = "openai_responses"
)

type ImageSourceKind string

const (
	ImageSourceBase64  ImageSourceKind = "base64"
	ImageSourceURL     ImageSourceKind = "url"
	ImageSourceDataURL ImageSourceKind = "data_url"
	ImageSourceFileID  ImageSourceKind = "file_id"
)

type Image struct {
	Block        json.RawMessage
	SourceKind   ImageSourceKind
	CachePayload string
	TaskContext  string
	ImageURL     string
	FileID       string
	Detail       string
	NodeIndex    int
	BlockIndex   int
}

type Inspection struct {
	Images []Image
	Texts  []string
}

func Inspect(operation Operation, root map[string]json.RawMessage) Inspection {
	document, err := parseRoot(operation, root)
	if err != nil {
		return Inspection{}
	}
	return Inspection{Images: document.Images(), Texts: document.Texts()}
}
