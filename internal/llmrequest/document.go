package llmrequest

import (
	"encoding/json"
	"fmt"
	"strings"
)

type documentNode struct {
	raw        json.RawMessage
	fields     map[string]json.RawMessage
	blocks     []json.RawMessage
	blockField string
}

type Document struct {
	operation            Operation
	root                 map[string]json.RawMessage
	collectionField      string
	nodes                []documentNode
	images               []Image
	texts                []string
	actualToolOperations []string
}

const (
	maxActualToolOperations = 64
	maxActualToolNameRunes  = 128
	imageEstimationMarker   = "[image-data]"
)

func Parse(operation Operation, body []byte) (*Document, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("request must be an object")
	}
	return parseRoot(operation, root)
}

func ParseRoot(operation Operation, root map[string]json.RawMessage) (*Document, error) {
	if root == nil {
		return nil, fmt.Errorf("request must be an object")
	}
	return parseRoot(operation, root)
}

func parseRoot(operation Operation, root map[string]json.RawMessage) (*Document, error) {
	var document *Document
	var err error
	switch operation {
	case OperationAnthropicMessages:
		document, err = parseMessageCollection(operation, root, "image", "text", parseAnthropicImage)
	case OperationOpenAIChatCompletions:
		document, err = parseMessageCollection(operation, root, "image_url", "text", parseChatImage)
	case OperationOpenAIResponses:
		document, err = parseResponses(root)
	default:
		return nil, fmt.Errorf("unsupported inference operation %q", operation)
	}
	if err != nil {
		return nil, err
	}
	document.collectForcedToolOperation()
	return document, nil
}

func (d *Document) Images() []Image {
	return append([]Image(nil), d.images...)
}

func (d *Document) Texts() []string {
	return append([]string(nil), d.texts...)
}

func (d *Document) ActualToolOperations() []string {
	return append([]string(nil), d.actualToolOperations...)
}

func (d *Document) CanonicalEstimationJSON() ([]byte, error) {
	overrides := make(map[[2]int]json.RawMessage, len(d.images))
	for _, image := range d.images {
		block, err := d.sanitizedImageBlock(image)
		if err != nil {
			return nil, err
		}
		if block != nil {
			overrides[[2]int{image.NodeIndex, image.BlockIndex}] = block
		}
	}
	if len(overrides) == 0 {
		return json.Marshal(d.root)
	}
	return d.marshalWithBlockOverrides(overrides)
}

func (d *Document) RewriteImages(replacements []string) ([]byte, error) {
	if len(replacements) != len(d.images) {
		return nil, fmt.Errorf(
			"image replacements count %d does not match images count %d",
			len(replacements), len(d.images),
		)
	}
	blockType := "text"
	if d.operation == OperationOpenAIResponses {
		blockType = "input_text"
	}
	encodedReplacements := make(map[[2]int]json.RawMessage, len(d.images))
	for index, image := range d.images {
		replacement, err := json.Marshal(map[string]string{
			"type": blockType,
			"text": replacements[index],
		})
		if err != nil {
			return nil, fmt.Errorf("marshal image replacement %d: %w", index, err)
		}
		encodedReplacements[[2]int{image.NodeIndex, image.BlockIndex}] = replacement
	}

	return d.marshalWithBlockOverrides(encodedReplacements)
}

func (d *Document) marshalWithBlockOverrides(overrides map[[2]int]json.RawMessage) ([]byte, error) {
	rawNodes := make([]json.RawMessage, len(d.nodes))
	for nodeIndex, node := range d.nodes {
		if node.fields == nil {
			rawNodes[nodeIndex] = node.raw
			continue
		}
		fields := node.fields
		if node.blockField != "" {
			blocks := append([]json.RawMessage(nil), node.blocks...)
			changed := false
			for blockIndex := range blocks {
				if replacement, ok := overrides[[2]int{nodeIndex, blockIndex}]; ok {
					blocks[blockIndex] = replacement
					changed = true
				}
			}
			if changed {
				fields = cloneRawFields(node.fields)
				var err error
				fields[node.blockField], err = json.Marshal(blocks)
				if err != nil {
					return nil, fmt.Errorf("marshal item %d %s: %w", nodeIndex, node.blockField, err)
				}
			}
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			return nil, fmt.Errorf("marshal item %d: %w", nodeIndex, err)
		}
		rawNodes[nodeIndex] = encoded
	}

	root := cloneRawFields(d.root)
	encodedNodes, err := json.Marshal(rawNodes)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", d.collectionField, err)
	}
	root[d.collectionField] = encodedNodes
	result, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("marshal rewritten request: %w", err)
	}
	return result, nil
}

func (d *Document) sanitizedImageBlock(image Image) (json.RawMessage, error) {
	if image.SourceKind != ImageSourceBase64 && image.SourceKind != ImageSourceDataURL {
		return nil, nil
	}
	var block map[string]json.RawMessage
	if err := json.Unmarshal(image.Block, &block); err != nil {
		return nil, fmt.Errorf("parse image block for estimation: %w", err)
	}
	switch d.operation {
	case OperationAnthropicMessages:
		var source map[string]json.RawMessage
		if err := json.Unmarshal(block["source"], &source); err != nil {
			return nil, fmt.Errorf("parse Anthropic image source for estimation: %w", err)
		}
		source["data"] = json.RawMessage(`"` + imageEstimationMarker + `"`)
		encoded, err := json.Marshal(source)
		if err != nil {
			return nil, err
		}
		block["source"] = encoded
	case OperationOpenAIChatCompletions:
		var imageURL map[string]json.RawMessage
		if err := json.Unmarshal(block["image_url"], &imageURL); err != nil {
			return nil, fmt.Errorf("parse Chat Completions image URL for estimation: %w", err)
		}
		var value string
		if json.Unmarshal(imageURL["url"], &value) != nil {
			return nil, fmt.Errorf("parse Chat Completions image URL for estimation")
		}
		imageURL["url"], _ = json.Marshal(sanitizeBase64DataURL(value))
		encoded, err := json.Marshal(imageURL)
		if err != nil {
			return nil, err
		}
		block["image_url"] = encoded
	case OperationOpenAIResponses:
		var value string
		if json.Unmarshal(block["image_url"], &value) != nil {
			return nil, fmt.Errorf("parse Responses image URL for estimation")
		}
		block["image_url"], _ = json.Marshal(sanitizeBase64DataURL(value))
	}
	encoded, err := json.Marshal(block)
	if err != nil {
		return nil, fmt.Errorf("marshal image block for estimation: %w", err)
	}
	return encoded, nil
}

func sanitizeBase64DataURL(value string) string {
	lower := strings.ToLower(value)
	marker := ";base64,"
	index := strings.Index(lower, marker)
	if index < 0 {
		return value
	}
	return value[:index+len(marker)] + imageEstimationMarker
}

func parseMessageCollection(
	operation Operation,
	root map[string]json.RawMessage,
	imageType string,
	textType string,
	parseImage func(map[string]json.RawMessage, json.RawMessage, int, int) (Image, error),
) (*Document, error) {
	rawMessages, ok := root["messages"]
	if !ok || !isJSONType(rawMessages, '[') {
		return nil, fmt.Errorf("request messages must be an array")
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(rawMessages, &messages); err != nil {
		return nil, fmt.Errorf("parse messages: %w", err)
	}
	document := &Document{
		operation: operation, root: root, collectionField: "messages",
		nodes: make([]documentNode, 0, len(messages)),
	}
	for messageIndex, rawMessage := range messages {
		if !isJSONType(rawMessage, '{') {
			return nil, fmt.Errorf("message %d must be an object", messageIndex)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawMessage, &fields); err != nil {
			return nil, fmt.Errorf("parse message %d: %w", messageIndex, err)
		}
		collectMessageToolOperations(document, operation, fields)
		node := documentNode{raw: rawMessage, fields: fields}
		content, exists := fields["content"]
		if !exists {
			document.nodes = append(document.nodes, node)
			continue
		}
		if isJSONType(content, '"') {
			var text string
			if err := json.Unmarshal(content, &text); err != nil {
				return nil, fmt.Errorf("parse message %d content: %w", messageIndex, err)
			}
			if isUserRole(fields) {
				document.texts = append(document.texts, nonEmptyText(text)...)
			}
			document.nodes = append(document.nodes, node)
			continue
		}
		if operation == OperationOpenAIChatCompletions &&
			isJSONNull(content) && isAssistantToolCallMessage(fields) {
			document.nodes = append(document.nodes, node)
			continue
		}
		if !isJSONType(content, '[') {
			return nil, fmt.Errorf("message %d content must be a string or array", messageIndex)
		}
		if err := json.Unmarshal(content, &node.blocks); err != nil {
			return nil, fmt.Errorf("parse message %d content: %w", messageIndex, err)
		}
		node.blockField = "content"
		user := isUserRole(fields)
		taskContext := ""
		if user {
			taskContext = collectTextBlocks(node.blocks, textType)
			document.texts = append(document.texts, splitCollectedText(taskContext)...)
		}
		for blockIndex, rawBlock := range node.blocks {
			var block map[string]json.RawMessage
			if json.Unmarshal(rawBlock, &block) != nil {
				continue
			}
			blockType, _, _ := requiredString(block, "type")
			if blockType != imageType {
				continue
			}
			image, err := parseImage(block, rawBlock, messageIndex, blockIndex)
			if err != nil {
				return nil, err
			}
			image.TaskContext = taskContext
			document.images = append(document.images, image)
		}
		document.nodes = append(document.nodes, node)
	}
	return document, nil
}

func collectMessageToolOperations(
	document *Document,
	operation Operation,
	fields map[string]json.RawMessage,
) {
	if !isAssistantRole(fields) {
		return
	}
	if operation == OperationOpenAIChatCompletions {
		var calls []map[string]json.RawMessage
		if json.Unmarshal(fields["tool_calls"], &calls) != nil {
			return
		}
		for _, call := range calls {
			if rawString(call["type"]) != "function" {
				continue
			}
			var function map[string]json.RawMessage
			if json.Unmarshal(call["function"], &function) == nil {
				document.addActualToolOperation(rawString(function["name"]))
			}
		}
		return
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(fields["content"], &blocks) != nil {
		return
	}
	for _, block := range blocks {
		if rawString(block["type"]) == "tool_use" {
			document.addActualToolOperation(rawString(block["name"]))
		}
	}
}

func (d *Document) collectForcedToolOperation() {
	var choice map[string]json.RawMessage
	if json.Unmarshal(d.root["tool_choice"], &choice) != nil {
		return
	}
	switch d.operation {
	case OperationAnthropicMessages:
		if rawString(choice["type"]) == "tool" {
			d.prependActualToolOperation(rawString(choice["name"]))
		}
	case OperationOpenAIChatCompletions:
		if rawString(choice["type"]) != "function" {
			return
		}
		var function map[string]json.RawMessage
		if json.Unmarshal(choice["function"], &function) == nil {
			d.prependActualToolOperation(rawString(function["name"]))
		}
	case OperationOpenAIResponses:
		if rawString(choice["type"]) == "function" {
			d.prependActualToolOperation(rawString(choice["name"]))
		}
	}
}

func (d *Document) prependActualToolOperation(name string) {
	name = boundedActualToolOperation(name)
	if name == "" || len(d.actualToolOperations) >= maxActualToolOperations ||
		d.hasActualToolOperation(name) {
		return
	}
	d.actualToolOperations = append(d.actualToolOperations, "")
	copy(d.actualToolOperations[1:], d.actualToolOperations[:len(d.actualToolOperations)-1])
	d.actualToolOperations[0] = name
}

func (d *Document) addActualToolOperation(name string) {
	name = boundedActualToolOperation(name)
	if name == "" || len(d.actualToolOperations) >= maxActualToolOperations ||
		d.hasActualToolOperation(name) {
		return
	}
	d.actualToolOperations = append(d.actualToolOperations, name)
}

func boundedActualToolOperation(name string) string {
	runes := []rune(name)
	if len(runes) > maxActualToolNameRunes {
		return string(runes[:maxActualToolNameRunes])
	}
	return name
}

func (d *Document) hasActualToolOperation(name string) bool {
	for _, existing := range d.actualToolOperations {
		if strings.EqualFold(existing, name) {
			return true
		}
	}
	return false
}

func rawString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func imageDetail(fields map[string]json.RawMessage, message string, args ...any) (string, error) {
	detail := "auto"
	if raw, ok := fields["detail"]; ok {
		if json.Unmarshal(raw, &detail) != nil ||
			(detail != "auto" && detail != "low" && detail != "high" && detail != "original") {
			return "", fmt.Errorf(message, args...)
		}
	}
	return detail, nil
}

func isUserRole(fields map[string]json.RawMessage) bool {
	var role string
	return json.Unmarshal(fields["role"], &role) == nil && role == "user"
}

func isAssistantRole(fields map[string]json.RawMessage) bool {
	var role string
	return json.Unmarshal(fields["role"], &role) == nil && role == "assistant"
}

func isAssistantToolCallMessage(fields map[string]json.RawMessage) bool {
	var role string
	if json.Unmarshal(fields["role"], &role) != nil || role != "assistant" {
		return false
	}
	var toolCalls []json.RawMessage
	return json.Unmarshal(fields["tool_calls"], &toolCalls) == nil && len(toolCalls) > 0
}

func collectTextBlocks(blocks []json.RawMessage, textType string) string {
	texts := make([]string, 0, len(blocks))
	for _, rawBlock := range blocks {
		var block map[string]json.RawMessage
		if json.Unmarshal(rawBlock, &block) != nil {
			continue
		}
		blockType, _, _ := requiredString(block, "type")
		if blockType != textType {
			continue
		}
		var text string
		if json.Unmarshal(block["text"], &text) == nil {
			texts = append(texts, nonEmptyText(text)...)
		}
	}
	return strings.Join(texts, "\n")
}

func splitCollectedText(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func requiredString(fields map[string]json.RawMessage, name string) (string, bool, error) {
	raw, ok := fields[name]
	if !ok {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true, fmt.Errorf("%s must be a string", name)
	}
	return value, true, nil
}

func nonEmptyString(fields map[string]json.RawMessage, name string) (string, error) {
	value, ok, err := requiredString(fields, name)
	if err != nil {
		return "", err
	}
	if !ok || value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func nonEmptyText(text string) []string {
	if text = strings.TrimSpace(text); text != "" {
		return []string{text}
	}
	return nil
}

func cloneRawFields(fields map[string]json.RawMessage) map[string]json.RawMessage {
	clone := make(map[string]json.RawMessage, len(fields))
	for name, value := range fields {
		clone[name] = value
	}
	return clone
}

func isJSONType(raw json.RawMessage, first byte) bool {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	return len(raw) > 0 && raw[0] == first
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}
