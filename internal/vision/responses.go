package vision

import (
	"encoding/json"
	"fmt"
	"strings"
)

type responsesItemNode struct {
	raw        json.RawMessage
	fields     map[string]json.RawMessage
	blocks     []json.RawMessage
	blockField string
}

type responsesDocument struct {
	root      map[string]json.RawMessage
	items     []responsesItemNode
	imageRefs []imageRef
}

func parseResponses(body []byte) (*responsesDocument, error) {
	root, err := parseRequestRoot(body)
	if err != nil {
		return nil, err
	}
	return parseResponsesRoot(root)
}

func parseResponsesRoot(root map[string]json.RawMessage) (*responsesDocument, error) {
	doc := &responsesDocument{root: root}
	inputRaw, ok := root["input"]
	if !ok || !isJSONType(inputRaw, '[') {
		return doc, nil
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(inputRaw, &rawItems); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}

	doc.items = make([]responsesItemNode, 0, len(rawItems))
	for itemIndex, rawItem := range rawItems {
		node := responsesItemNode{raw: rawItem}
		if !isJSONType(rawItem, '{') {
			doc.items = append(doc.items, node)
			continue
		}
		if err := json.Unmarshal(rawItem, &node.fields); err != nil {
			return nil, fmt.Errorf("parse input item %d: %w", itemIndex, err)
		}
		blockField := responsesImageBlockField(node.fields)
		if blockField == "" {
			doc.items = append(doc.items, node)
			continue
		}
		blocksRaw, ok := node.fields[blockField]
		if !ok || !isJSONType(blocksRaw, '[') {
			doc.items = append(doc.items, node)
			continue
		}
		if err := json.Unmarshal(blocksRaw, &node.blocks); err != nil {
			return nil, fmt.Errorf("parse input item %d %s: %w", itemIndex, blockField, err)
		}
		node.blockField = blockField
		var role string
		if rawRole, ok := node.fields["role"]; ok {
			_ = json.Unmarshal(rawRole, &role)
		}
		taskContext := ""
		if blockField == "content" && role == "user" {
			taskContext = collectTaskContext(node.blocks, "input_text")
		}
		for blockIndex, block := range node.blocks {
			image, found, err := parseResponsesImageBlock(
				block,
				blockField,
				itemIndex,
				blockIndex,
			)
			if err != nil {
				return nil, err
			}
			if found {
				image.taskContext = taskContext
				doc.imageRefs = append(doc.imageRefs, image)
			}
		}
		doc.items = append(doc.items, node)
	}
	return doc, nil
}

func responsesImageBlockField(fields map[string]json.RawMessage) string {
	raw, ok := fields["type"]
	if !ok {
		_, hasRole := fields["role"]
		if hasRole {
			return "content"
		}
		return ""
	}
	var itemType string
	if json.Unmarshal(raw, &itemType) != nil {
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

func parseResponsesImageBlock(
	block json.RawMessage,
	blockField string,
	itemIndex, blockIndex int,
) (imageRef, bool, error) {
	if !isJSONType(block, '{') {
		return imageRef{}, false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(block, &fields); err != nil {
		return imageRef{}, false, fmt.Errorf(
			"parse input item %d %s block %d: %w",
			itemIndex,
			blockField,
			blockIndex,
			err,
		)
	}
	blockType, present, err := requiredString(fields, "type")
	if err != nil || !present || blockType != "input_image" {
		return imageRef{}, false, nil
	}

	imageURL, hasImageURL, imageURLErr := requiredString(fields, "image_url")
	fileID, hasFileID, fileIDErr := requiredString(fields, "file_id")
	if imageURLErr != nil || fileIDErr != nil || hasImageURL == hasFileID ||
		(hasImageURL && imageURL == "") || (hasFileID && fileID == "") {
		return imageRef{}, false, fmt.Errorf(
			"input item %d %s block %d input image must contain exactly one non-empty image_url or file_id",
			itemIndex, blockField, blockIndex,
		)
	}

	detail := "auto"
	if raw, ok := fields["detail"]; ok {
		if err := json.Unmarshal(raw, &detail); err != nil ||
			(detail != "auto" && detail != "low" && detail != "high" && detail != "original") {
			return imageRef{}, false, fmt.Errorf(
				"input item %d %s block %d input image detail must be auto, low, high, or original",
				itemIndex, blockField, blockIndex,
			)
		}
	}

	image := imageRef{
		block:        block,
		detail:       detail,
		messageIndex: itemIndex,
		blockIndex:   blockIndex,
	}
	if hasFileID {
		image.sourceType = "file"
		image.fileID = fileID
		image.cachePayload = detail + "\x00" + fileID
		return image, true, nil
	}
	image.sourceType = "url"
	image.imageURL = imageURL
	if strings.HasPrefix(strings.ToLower(imageURL), "data:") {
		image.sourceType = "data_url"
	}
	image.cachePayload = detail + "\x00" + imageURL
	return image, true, nil
}

func (d *responsesDocument) images() []imageRef {
	return d.imageRefs
}

func (d *responsesDocument) rewrite(descriptions []string) ([]byte, error) {
	if len(descriptions) != len(d.imageRefs) {
		return nil, fmt.Errorf(
			"image descriptions count %d does not match images count %d",
			len(descriptions), len(d.imageRefs),
		)
	}
	replacements := make(map[[2]int]json.RawMessage, len(d.imageRefs))
	for imageIndex, image := range d.imageRefs {
		replacement, err := json.Marshal(map[string]string{
			"type": "input_text",
			"text": replacementPrefix(image) + descriptions[imageIndex],
		})
		if err != nil {
			return nil, fmt.Errorf("marshal image description %d: %w", imageIndex, err)
		}
		replacements[[2]int{image.messageIndex, image.blockIndex}] = replacement
	}

	rawItems := make([]json.RawMessage, len(d.items))
	for itemIndex, node := range d.items {
		if node.fields == nil {
			rawItems[itemIndex] = node.raw
			continue
		}
		fields := node.fields
		if node.blockField != "" {
			blocks := append([]json.RawMessage(nil), node.blocks...)
			changed := false
			for blockIndex := range blocks {
				if replacement, ok := replacements[[2]int{itemIndex, blockIndex}]; ok {
					blocks[blockIndex] = replacement
					changed = true
				}
			}
			if changed {
				fields = cloneRawFields(node.fields)
				var err error
				fields[node.blockField], err = json.Marshal(blocks)
				if err != nil {
					return nil, fmt.Errorf(
						"marshal input item %d %s: %w",
						itemIndex,
						node.blockField,
						err,
					)
				}
			}
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			return nil, fmt.Errorf("marshal input item %d: %w", itemIndex, err)
		}
		rawItems[itemIndex] = encoded
	}

	root := cloneRawFields(d.root)
	var err error
	root["input"], err = json.Marshal(rawItems)
	if err != nil {
		return nil, fmt.Errorf("marshal input: %w", err)
	}
	result, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("marshal rewritten request: %w", err)
	}
	return result, nil
}

func parseResponsesDescription(body []byte) (string, error) {
	var response struct {
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("parse vision response: %w", err)
	}
	var texts []string
	for _, item := range response.Output {
		for _, part := range item.Content {
			if part.Type == "output_text" && strings.TrimSpace(part.Text) != "" {
				texts = append(texts, part.Text)
			}
		}
	}
	if len(texts) == 0 {
		return "", fmt.Errorf("vision response contains no output text")
	}
	return strings.Join(texts, "\n"), nil
}
