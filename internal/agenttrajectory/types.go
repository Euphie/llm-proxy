package agenttrajectory

import (
	"encoding/json"
	"errors"
)

var ErrInvalidProtocol = errors.New("invalid agent protocol payload")

type EventKind string

const (
	EventUserMessage   EventKind = "user_message"
	EventAssistantText EventKind = "assistant_text"
	EventToolCall      EventKind = "tool_call"
	EventToolResult    EventKind = "tool_result"
)

type CompletionState string

const (
	CompletionWaitingForTool CompletionState = "waiting_for_tool"
	CompletionComplete       CompletionState = "complete"
	CompletionFailed         CompletionState = "failed"
)

type Event struct {
	Role      string          `json:"role"`
	Kind      EventKind       `json:"kind"`
	CallID    string          `json:"call_id,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Text      string          `json:"text,omitempty"`
}

type Turn struct {
	History    []Event         `json:"history"`
	Response   []Event         `json:"response"`
	Completion CompletionState `json:"completion"`
	ToolCalls  int             `json:"tool_calls"`
	FinalText  string          `json:"final_text,omitempty"`
}
