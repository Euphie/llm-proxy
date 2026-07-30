package vision

import (
	"encoding/json"
	"fmt"
)

func parseRequestRoot(body []byte) (map[string]json.RawMessage, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("request must be an object")
	}
	return root, nil
}

func requestModel(root map[string]json.RawMessage) (string, bool) {
	raw, ok := root["model"]
	if !ok {
		return "", false
	}
	var model string
	if err := json.Unmarshal(raw, &model); err != nil || model == "" {
		return "", false
	}
	return model, true
}
