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
	operation       Operation
	root            map[string]json.RawMessage
	collectionField string
	nodes           []documentNode
	images          []Image
	texts           []string
}

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
	switch operation {
	case OperationAnthropicMessages:
		return parseMessageCollection(operation, root, "image", "text", parseAnthropicImage)
	case OperationOpenAIChatCompletions:
		return parseMessageCollection(operation, root, "image_url", "text", parseChatImage)
	case OperationOpenAIResponses:
		return parseResponses(root)
	default:
		return nil, fmt.Errorf("unsupported inference operation %q", operation)
	}
}

func (d *Document) Images() []Image {
	return append([]Image(nil), d.images...)
}

func (d *Document) Texts() []string {
	return append([]string(nil), d.texts...)
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
				if replacement, ok := encodedReplacements[[2]int{nodeIndex, blockIndex}]; ok {
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
