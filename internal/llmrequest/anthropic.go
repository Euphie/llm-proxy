package llmrequest

import (
	"encoding/json"
	"fmt"
)

func parseAnthropicImage(
	block map[string]json.RawMessage,
	rawBlock json.RawMessage,
	messageIndex int,
	blockIndex int,
) (Image, error) {
	var source map[string]json.RawMessage
	if !isJSONType(block["source"], '{') || json.Unmarshal(block["source"], &source) != nil {
		return Image{}, fmt.Errorf("message %d content block %d image source must be an object", messageIndex, blockIndex)
	}
	sourceType, present, err := requiredString(source, "type")
	if err != nil || !present {
		return Image{}, fmt.Errorf("message %d content block %d image source type is required", messageIndex, blockIndex)
	}
	image := Image{Block: rawBlock, NodeIndex: messageIndex, BlockIndex: blockIndex, Detail: "auto"}
	switch sourceType {
	case "base64":
		mediaType, err := nonEmptyString(source, "media_type")
		if err != nil {
			return Image{}, imageFieldError(messageIndex, blockIndex, sourceType, err)
		}
		data, err := nonEmptyString(source, "data")
		if err != nil {
			return Image{}, imageFieldError(messageIndex, blockIndex, sourceType, err)
		}
		image.SourceKind = ImageSourceBase64
		image.CachePayload = mediaType + "\x00" + data
	case "url":
		value, err := nonEmptyString(source, "url")
		if err != nil {
			return Image{}, imageFieldError(messageIndex, blockIndex, sourceType, err)
		}
		image.SourceKind = ImageSourceURL
		image.ImageURL = value
		image.CachePayload = value
	case "file":
		value, err := nonEmptyString(source, "file_id")
		if err != nil {
			return Image{}, imageFieldError(messageIndex, blockIndex, sourceType, err)
		}
		image.SourceKind = ImageSourceFileID
		image.FileID = value
		image.CachePayload = value
	default:
		return Image{}, fmt.Errorf("message %d content block %d has unsupported image source type %q", messageIndex, blockIndex, sourceType)
	}
	return image, nil
}

func imageFieldError(messageIndex, blockIndex int, sourceType string, err error) error {
	return fmt.Errorf("message %d content block %d image source %s: %w", messageIndex, blockIndex, sourceType, err)
}
