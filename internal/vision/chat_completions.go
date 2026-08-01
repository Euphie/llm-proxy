package vision

import (
	"encoding/json"
	"fmt"
	"strings"
)

func parseChatCompletions(body []byte) (*messageDocument, error) {
	root, err := parseRequestRoot(body)
	if err != nil {
		return nil, err
	}
	return parseChatCompletionsRoot(root)
}

func parseChatCompletionsRoot(root map[string]json.RawMessage) (*messageDocument, error) {
	messagesRaw, ok := root["messages"]
	if !ok || !isJSONType(messagesRaw, '[') {
		return nil, fmt.Errorf("request messages must be an array")
	}
	var rawMessages []json.RawMessage
	if err := json.Unmarshal(messagesRaw, &rawMessages); err != nil {
		return nil, fmt.Errorf("parse messages: %w", err)
	}

	doc := &messageDocument{root: root, messages: make([]messageNode, 0, len(rawMessages))}
	for messageIndex, rawMessage := range rawMessages {
		if !isJSONType(rawMessage, '{') {
			return nil, fmt.Errorf("message %d must be an object", messageIndex)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawMessage, &fields); err != nil {
			return nil, fmt.Errorf("parse message %d: %w", messageIndex, err)
		}
		node := messageNode{fields: fields}
		content, exists := fields["content"]
		if !exists || isJSONType(content, '"') {
			doc.messages = append(doc.messages, node)
			continue
		}
		if !isJSONType(content, '[') {
			return nil, fmt.Errorf("message %d content must be a string or array", messageIndex)
		}
		if err := json.Unmarshal(content, &node.content); err != nil {
			return nil, fmt.Errorf("parse message %d content: %w", messageIndex, err)
		}
		node.arrayContent = true
		var role string
		_ = json.Unmarshal(fields["role"], &role)
		taskContext := ""
		if role == "user" {
			taskContext = collectTaskContext(node.content, "text")
		}
		for blockIndex, block := range node.content {
			image, found, err := parseChatImageBlock(block, messageIndex, blockIndex)
			if err != nil {
				return nil, err
			}
			if found {
				image.taskContext = taskContext
				doc.imageRefs = append(doc.imageRefs, image)
			}
		}
		doc.messages = append(doc.messages, node)
	}
	return doc, nil
}

func parseChatImageBlock(
	block json.RawMessage,
	messageIndex, blockIndex int,
) (imageRef, bool, error) {
	if !isJSONType(block, '{') {
		return imageRef{}, false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(block, &fields); err != nil {
		return imageRef{}, false, fmt.Errorf("parse message %d content block %d: %w", messageIndex, blockIndex, err)
	}
	blockType, present, err := requiredString(fields, "type")
	if err != nil || !present || blockType != "image_url" {
		return imageRef{}, false, nil
	}
	var imageURL map[string]json.RawMessage
	if !isJSONType(fields["image_url"], '{') || json.Unmarshal(fields["image_url"], &imageURL) != nil {
		return imageRef{}, false, fmt.Errorf("message %d content block %d image_url must be an object", messageIndex, blockIndex)
	}
	url, err := nonEmptyString(imageURL, "url")
	if err != nil {
		return imageRef{}, false, fmt.Errorf("message %d content block %d image_url: %w", messageIndex, blockIndex, err)
	}
	detail := "auto"
	if raw, ok := imageURL["detail"]; ok {
		if json.Unmarshal(raw, &detail) != nil ||
			(detail != "auto" && detail != "low" && detail != "high" && detail != "original") {
			return imageRef{}, false, fmt.Errorf("message %d content block %d image detail is invalid", messageIndex, blockIndex)
		}
	}
	sourceType := "url"
	if strings.HasPrefix(strings.ToLower(url), "data:") {
		sourceType = "data_url"
	}
	return imageRef{
		block: block, sourceType: sourceType, imageURL: url, detail: detail,
		cachePayload: detail + "\x00" + url,
		messageIndex: messageIndex, blockIndex: blockIndex,
	}, true, nil
}
