package llmrequest

import (
	"encoding/json"
	"fmt"
	"strings"
)

func parseResponses(root map[string]json.RawMessage) (*Document, error) {
	document := &Document{
		operation: OperationOpenAIResponses, root: root, collectionField: "input",
	}
	rawInput, ok := root["input"]
	if !ok {
		return document, nil
	}
	var direct string
	if isJSONType(rawInput, '"') && json.Unmarshal(rawInput, &direct) == nil {
		document.texts = append(document.texts, nonEmptyText(direct)...)
		return document, nil
	}
	if !isJSONType(rawInput, '[') {
		return nil, fmt.Errorf("request input must be a string or array")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawInput, &items); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}
	document.nodes = make([]documentNode, 0, len(items))
	for itemIndex, rawItem := range items {
		node := documentNode{raw: rawItem}
		if !isJSONType(rawItem, '{') {
			return nil, fmt.Errorf("input item %d must be an object", itemIndex)
		}
		if err := json.Unmarshal(rawItem, &node.fields); err != nil {
			return nil, fmt.Errorf("parse input item %d: %w", itemIndex, err)
		}
		if rawString(node.fields["type"]) == "function_call" {
			document.addActualToolOperation(rawString(node.fields["name"]))
		}
		node.blockField = responsesBlockField(node.fields)
		if node.blockField == "" {
			var itemType, text string
			_ = json.Unmarshal(node.fields["type"], &itemType)
			_ = json.Unmarshal(node.fields["text"], &text)
			if itemType == "input_text" {
				document.texts = append(document.texts, nonEmptyText(text)...)
			}
			document.nodes = append(document.nodes, node)
			continue
		}
		rawBlocks, ok := node.fields[node.blockField]
		if node.blockField == "content" {
			var text string
			if ok && isJSONType(rawBlocks, '"') && json.Unmarshal(rawBlocks, &text) == nil {
				if isUserRole(node.fields) {
					document.texts = append(document.texts, nonEmptyText(text)...)
				}
				document.nodes = append(document.nodes, node)
				continue
			}
			if !ok || !isJSONType(rawBlocks, '[') {
				return nil, fmt.Errorf("input item %d content must be a string or array", itemIndex)
			}
		}
		if !ok || !isJSONType(rawBlocks, '[') {
			document.nodes = append(document.nodes, node)
			continue
		}
		if err := json.Unmarshal(rawBlocks, &node.blocks); err != nil {
			return nil, fmt.Errorf("parse input item %d %s: %w", itemIndex, node.blockField, err)
		}
		user := node.blockField == "content" && isUserRole(node.fields)
		taskContext := ""
		if user {
			taskContext = collectTextBlocks(node.blocks, "input_text")
			document.texts = append(document.texts, splitCollectedText(taskContext)...)
		}
		for blockIndex, rawBlock := range node.blocks {
			var block map[string]json.RawMessage
			if json.Unmarshal(rawBlock, &block) != nil {
				continue
			}
			blockType, _, _ := requiredString(block, "type")
			if blockType != "input_image" {
				continue
			}
			image, err := parseResponsesImage(block, rawBlock, itemIndex, blockIndex, node.blockField)
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

func parseResponsesImage(
	block map[string]json.RawMessage,
	rawBlock json.RawMessage,
	itemIndex int,
	blockIndex int,
	blockField string,
) (Image, error) {
	imageURL, hasImageURL, imageURLErr := requiredString(block, "image_url")
	fileID, hasFileID, fileIDErr := requiredString(block, "file_id")
	if imageURLErr != nil || fileIDErr != nil || hasImageURL == hasFileID ||
		(hasImageURL && imageURL == "") || (hasFileID && fileID == "") {
		return Image{}, fmt.Errorf(
			"input item %d %s block %d input image must contain exactly one non-empty image_url or file_id",
			itemIndex, blockField, blockIndex,
		)
	}
	detail, err := imageDetail(
		block,
		"input item %d %s block %d input image detail must be auto, low, high, or original",
		itemIndex, blockField, blockIndex,
	)
	if err != nil {
		return Image{}, err
	}
	image := Image{Block: rawBlock, Detail: detail, NodeIndex: itemIndex, BlockIndex: blockIndex}
	if hasFileID {
		image.SourceKind = ImageSourceFileID
		image.FileID = fileID
		image.CachePayload = detail + "\x00" + fileID
		return image, nil
	}
	image.SourceKind = ImageSourceURL
	image.ImageURL = imageURL
	if strings.HasPrefix(strings.ToLower(imageURL), "data:") {
		image.SourceKind = ImageSourceDataURL
	}
	image.CachePayload = detail + "\x00" + imageURL
	return image, nil
}

func responsesBlockField(fields map[string]json.RawMessage) string {
	rawType, ok := fields["type"]
	if !ok {
		if _, hasRole := fields["role"]; hasRole {
			return "content"
		}
		return ""
	}
	var itemType string
	if json.Unmarshal(rawType, &itemType) != nil {
		return ""
	}
	switch itemType {
	case "message":
		return "content"
	case "function_call_output":
		return "output"
	default:
		return ""
	}
}
