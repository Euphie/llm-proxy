package agenttrajectory

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

const redactedValue = "[REDACTED]"

var (
	bearerPattern      = regexp.MustCompile(`(?i)\bbearer[ \t]+[a-z0-9._~+/=-]+`)
	secretPattern      = regexp.MustCompile(`(?i)\b(?:sk|rk|pk)-(?:live-|test-|proj-)?[a-z0-9_-]{8,}`)
	namedSecretPattern = regexp.MustCompile(`(?i)\b(password|passwd|token|access[_-]?token|refresh[_-]?token|api[_-]?key|secret|authorization|cookie|set-cookie)\b([ \t]*[:=][ \t]*)([^,;\s]+)`)
)

func Redact(events []Event) []Event {
	redacted := make([]Event, len(events))
	for index, event := range events {
		redacted[index] = event
		redacted[index].Text = redactText(event.Text)
		redacted[index].Arguments = redactJSON(event.Arguments)
		redacted[index].Result = redactJSON(event.Result)
	}
	return redacted
}

func redactJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		encoded, _ := json.Marshal(redactText(string(raw)))
		return encoded
	}
	redactValue(value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`"[REDACTED]"`)
	}
	return encoded
}

func redactValue(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if sensitiveKey(key) {
				typed[key] = redactedValue
				continue
			}
			switch nested := child.(type) {
			case string:
				typed[key] = redactText(nested)
			default:
				redactValue(nested)
			}
		}
	case []any:
		for index, child := range typed {
			switch nested := child.(type) {
			case string:
				typed[index] = redactText(nested)
			default:
				redactValue(nested)
			}
		}
	}
}

func sensitiveKey(key string) bool {
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, key)
	for _, marker := range []string{
		"password", "passwd", "token", "secret", "apikey", "authorization", "cookie", "privatekey",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func redactText(value string) string {
	if value == "" {
		return ""
	}
	value = bearerPattern.ReplaceAllString(value, "Bearer "+redactedValue)
	value = secretPattern.ReplaceAllString(value, redactedValue)
	return namedSecretPattern.ReplaceAllString(value, "${1}${2}"+redactedValue)
}
