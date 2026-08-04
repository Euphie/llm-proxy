package routing

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"

	"github.com/Euphie/llm-proxy/internal/llmrequest"
	"github.com/Euphie/llm-proxy/internal/profile"
)

const routingTextRuneLimit = 8192

const AutoModel = "auto"

var (
	ErrUnsupportedOperation = errors.New("intelligent routing operation is not supported")
	ErrInvalidRequest       = errors.New("invalid intelligent routing request")
)

type Operation = llmrequest.Operation

const (
	OperationAnthropicMessages     = llmrequest.OperationAnthropicMessages
	OperationOpenAIChatCompletions = llmrequest.OperationOpenAIChatCompletions
	OperationOpenAIResponses       = llmrequest.OperationOpenAIResponses
)

type RequestFacts struct {
	EstimatedInputTokens     int                          `json:"estimated_input_tokens"`
	RequestedOutputTokens    int                          `json:"requested_output_tokens"`
	ImageCount               int                          `json:"image_count"`
	HasImages                bool                         `json:"has_images"`
	ImageSources             []llmrequest.ImageSourceKind `json:"image_sources,omitempty"`
	HasTools                 bool                         `json:"has_tools"`
	ActualToolOperations     []string                     `json:"actual_tool_operations,omitempty"`
	HistoricalToolOperations []string                     `json:"historical_tool_operations,omitempty"`
	ForcedToolOperation      string                       `json:"forced_tool_operation,omitempty"`
	RequiresStructuredOutput bool                         `json:"requires_structured_output"`
	Stream                   bool                         `json:"stream"`
}

type Request struct {
	Operation Operation
	Model     string
	Facts     RequestFacts

	root           map[string]json.RawMessage
	evaluationText string
	latestUserText string
}

func RequestedModel(body []byte) (string, bool) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		return "", false
	}
	var model string
	if err := json.Unmarshal(root["model"], &model); err != nil || model == "" {
		return "", false
	}
	return model, true
}

func ParseAutoRequest(
	protocol profile.Protocol,
	method string,
	path string,
	contentType string,
	body []byte,
) (Request, error) {
	operation, ok := supportedOperation(protocol, method, path)
	if !ok {
		return Request{}, fmt.Errorf("%w: %s %s", ErrUnsupportedOperation, method, path)
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" {
		return Request{}, fmt.Errorf("%w: Content-Type must be application/json", ErrInvalidRequest)
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		return Request{}, fmt.Errorf("%w: body must be a JSON object", ErrInvalidRequest)
	}
	var model string
	if err := json.Unmarshal(root["model"], &model); err != nil || model != AutoModel {
		return Request{}, fmt.Errorf("%w: model must be %q", ErrInvalidRequest, AutoModel)
	}
	document, err := llmrequest.ParseRoot(operation, root)
	if err != nil {
		return Request{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	images := document.Images()
	estimationJSON, err := document.CanonicalEstimationJSON()
	if err != nil {
		return Request{}, fmt.Errorf("%w: canonical token estimate: %v", ErrInvalidRequest, err)
	}

	return Request{
		Operation: operation,
		Model:     model,
		Facts: extractFacts(
			operation,
			root,
			estimatedTokens(estimationJSON),
			images,
			document.ActualToolOperations(),
			document.HistoricalToolOperations(),
			document.ForcedToolOperation(),
		),
		root:           cloneRoot(root),
		evaluationText: boundedRoutingText(document.Texts()),
		latestUserText: boundedRoutingText(document.LatestUserTexts()),
	}, nil
}

func (r Request) WithModel(model string) ([]byte, error) {
	return r.withModel(model, false)
}

func (r Request) WithModelNonStreaming(model string) ([]byte, error) {
	return r.withModel(model, true)
}

func (r Request) withModel(model string, nonStreaming bool) ([]byte, error) {
	if model == "" || strings.TrimSpace(model) != model || model == AutoModel {
		return nil, fmt.Errorf("%w: selected model is invalid", ErrInvalidRequest)
	}
	root := cloneRoot(r.root)
	encoded, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("encode selected model: %w", err)
	}
	root["model"] = encoded
	if nonStreaming {
		root["stream"] = json.RawMessage("false")
	}
	rewritten, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("encode routed request: %w", err)
	}
	return rewritten, nil
}

func (r Request) EvaluationText() string {
	return r.routingText()
}

func (r Request) LatestUserText() string {
	return r.latestUserText
}

func (r Request) routingText() string {
	return r.evaluationText
}

func supportedOperation(protocol profile.Protocol, method, path string) (Operation, bool) {
	if method != http.MethodPost {
		return "", false
	}
	switch protocol {
	case profile.ProtocolAnthropic:
		if path == "/v1/messages" {
			return OperationAnthropicMessages, true
		}
	case profile.ProtocolOpenAI:
		switch path {
		case "/v1/chat/completions":
			return OperationOpenAIChatCompletions, true
		case "/v1/responses":
			return OperationOpenAIResponses, true
		}
	}
	return "", false
}

func extractFacts(
	operation Operation,
	root map[string]json.RawMessage,
	estimatedInputTokens int,
	images []llmrequest.Image,
	actualToolOperations []string,
	historicalToolOperations []string,
	forcedToolOperation string,
) RequestFacts {
	imageCount := len(images)
	facts := RequestFacts{
		EstimatedInputTokens: estimatedInputTokens,
		ImageCount:           imageCount,
		HasImages:            imageCount > 0,
		ImageSources:         make([]llmrequest.ImageSourceKind, imageCount),
		HasTools:             nonEmptyArray(root["tools"]),
		ActualToolOperations: append([]string(nil), actualToolOperations...),
		HistoricalToolOperations: append(
			[]string(nil), historicalToolOperations...,
		),
		ForcedToolOperation: forcedToolOperation,
		Stream:              boolValue(root["stream"]),
	}
	for index, image := range images {
		facts.ImageSources[index] = image.SourceKind
	}
	switch operation {
	case OperationAnthropicMessages:
		facts.RequestedOutputTokens = positiveInt(root["max_tokens"])
		facts.RequiresStructuredOutput = nestedType(root["output_config"], "format") == "json_schema"
	case OperationOpenAIChatCompletions:
		facts.RequestedOutputTokens = positiveInt(root["max_completion_tokens"])
		if facts.RequestedOutputTokens == 0 {
			facts.RequestedOutputTokens = positiveInt(root["max_tokens"])
		}
		format := directType(root["response_format"])
		facts.RequiresStructuredOutput = format != "" && format != "text"
	case OperationOpenAIResponses:
		facts.RequestedOutputTokens = positiveInt(root["max_output_tokens"])
		facts.RequiresStructuredOutput = nestedType(root["text"], "format") == "json_schema"
	}
	return facts
}

func estimatedTokens(envelope []byte) int {
	return len(envelope)/4 + boolInt(len(envelope)%4 != 0)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func positiveInt(raw json.RawMessage) int {
	var value int
	if err := json.Unmarshal(raw, &value); err != nil || value <= 0 {
		return 0
	}
	return value
}

func boolValue(raw json.RawMessage) bool {
	var value bool
	_ = json.Unmarshal(raw, &value)
	return value
}

func nonEmptyArray(raw json.RawMessage) bool {
	var values []json.RawMessage
	return json.Unmarshal(raw, &values) == nil && len(values) > 0
}

func directType(raw json.RawMessage) string {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return ""
	}
	var value string
	_ = json.Unmarshal(object["type"], &value)
	return value
}

func nestedType(raw json.RawMessage, field string) string {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return ""
	}
	return directType(object[field])
}

func cloneRoot(root map[string]json.RawMessage) map[string]json.RawMessage {
	cloned := make(map[string]json.RawMessage, len(root))
	for key, value := range root {
		cloned[key] = append(json.RawMessage(nil), value...)
	}
	return cloned
}

func boundedRoutingText(texts []string) string {
	joined := strings.TrimSpace(strings.Join(texts, "\n"))
	runes := []rune(joined)
	if len(runes) > routingTextRuneLimit {
		return string(runes[:routingTextRuneLimit])
	}
	return joined
}
