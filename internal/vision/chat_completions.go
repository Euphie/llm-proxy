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
	if !ok {
		return nil, fmt.Errorf("request messages is required")
	}
	if !isJSONType(messagesRaw, '[') {
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
		content, ok := fields["content"]
		if !ok || isJSONType(content, 'n') || isJSONType(content, '"') {
			doc.messages = append(doc.messages, node)
			continue
		}
		if !isJSONType(content, '[') {
			return nil, fmt.Errorf("message %d content must be a string, array, or null", messageIndex)
		}
		if err := json.Unmarshal(content, &node.content); err != nil {
			return nil, fmt.Errorf("parse message %d content: %w", messageIndex, err)
		}
		node.arrayContent = true

		var role string
		if rawRole, ok := fields["role"]; ok {
			_ = json.Unmarshal(rawRole, &role)
		}
		taskContext := ""
		if role == "user" {
			taskContext = collectTaskContext(node.content, "text")
		}
		for blockIndex, block := range node.content {
			image, found, err := parseChatCompletionsImageBlock(block, messageIndex, blockIndex)
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

func parseChatCompletionsImageBlock(
	block json.RawMessage,
	messageIndex, blockIndex int,
) (imageRef, bool, error) {
	if !isJSONType(block, '{') {
		return imageRef{}, false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(block, &fields); err != nil {
		return imageRef{}, false, fmt.Errorf(
			"parse message %d content block %d: %w",
			messageIndex,
			blockIndex,
			err,
		)
	}
	blockType, present, err := requiredString(fields, "type")
	if err != nil || !present || blockType != "image_url" {
		return imageRef{}, false, nil
	}

	imageURLRaw, ok := fields["image_url"]
	if !ok || !isJSONType(imageURLRaw, '{') {
		return imageRef{}, false, fmt.Errorf(
			"message %d content block %d image_url must be an object",
			messageIndex,
			blockIndex,
		)
	}
	var imageURLFields map[string]json.RawMessage
	if err := json.Unmarshal(imageURLRaw, &imageURLFields); err != nil {
		return imageRef{}, false, fmt.Errorf(
			"parse message %d content block %d image_url: %w",
			messageIndex,
			blockIndex,
			err,
		)
	}
	imageURL, err := nonEmptyString(imageURLFields, "url")
	if err != nil {
		return imageRef{}, false, fmt.Errorf(
			"message %d content block %d image_url %w",
			messageIndex,
			blockIndex,
			err,
		)
	}
	detail := "auto"
	if raw, ok := imageURLFields["detail"]; ok {
		if err := json.Unmarshal(raw, &detail); err != nil ||
			(detail != "auto" && detail != "low" && detail != "high" && detail != "original") {
			return imageRef{}, false, fmt.Errorf(
				"message %d content block %d image detail must be auto, low, high, or original",
				messageIndex,
				blockIndex,
			)
		}
	}

	sourceType := "url"
	if strings.HasPrefix(strings.ToLower(imageURL), "data:") {
		sourceType = "data_url"
	}
	return imageRef{
		block:        block,
		sourceType:   sourceType,
		cachePayload: detail + "\x00" + imageURL,
		imageURL:     imageURL,
		detail:       detail,
		messageIndex: messageIndex,
		blockIndex:   blockIndex,
	}, true, nil
}
