package llmrequest

import (
	"encoding/json"
	"fmt"
	"strings"
)

func parseChatImage(
	block map[string]json.RawMessage,
	rawBlock json.RawMessage,
	messageIndex int,
	blockIndex int,
) (Image, error) {
	var imageURL map[string]json.RawMessage
	if !isJSONType(block["image_url"], '{') || json.Unmarshal(block["image_url"], &imageURL) != nil {
		return Image{}, fmt.Errorf("message %d content block %d image_url must be an object", messageIndex, blockIndex)
	}
	value, err := nonEmptyString(imageURL, "url")
	if err != nil {
		return Image{}, fmt.Errorf("message %d content block %d image_url: %w", messageIndex, blockIndex, err)
	}
	detail, err := imageDetail(imageURL, "message %d content block %d image detail is invalid", messageIndex, blockIndex)
	if err != nil {
		return Image{}, err
	}
	kind := ImageSourceURL
	if strings.HasPrefix(strings.ToLower(value), "data:") {
		kind = ImageSourceDataURL
	}
	return Image{
		Block: rawBlock, SourceKind: kind, CachePayload: detail + "\x00" + value,
		ImageURL: value, Detail: detail, NodeIndex: messageIndex, BlockIndex: blockIndex,
	}, nil
}
