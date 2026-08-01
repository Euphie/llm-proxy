package vision

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
