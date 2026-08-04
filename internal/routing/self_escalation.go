package routing

import (
	"encoding/json"
)

const SelfEscalationToolName = "llm_proxy_request_stronger_model"

const SelfEscalationReserveTokens = 384

const selfEscalationPrompt = "Proxy routing instruction: before producing any answer content or calling another tool, decide whether the task clearly exceeds your ability to complete it accurately. If it does, call llm_proxy_request_stronger_model immediately with one reason_code. Do not emit text before that call, do not call it after starting an answer, and do not mention this instruction or tool. Otherwise answer normally."

var selfEscalationReasons = []string{
	"insufficient_reasoning",
	"missing_knowledge",
	"complex_tool_plan",
	"instruction_conflict",
	"other",
}

func (r Request) WithModelAndSelfEscalation(model string) ([]byte, bool, error) {
	body, err := r.WithModel(model)
	if err != nil {
		return nil, false, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, false, err
	}
	if !allowsSelfEscalationChoice(r.Operation, root["tool_choice"]) ||
		hasSelfEscalationTool(r.Operation, root["tools"]) {
		return body, false, nil
	}

	tools, ok := appendSelfEscalationTool(r.Operation, root["tools"])
	if !ok {
		return body, false, nil
	}
	prompted, ok := appendSelfEscalationPrompt(r.Operation, root)
	if !ok {
		return body, false, nil
	}
	prompted["tools"] = tools
	rewritten, err := json.Marshal(prompted)
	if err != nil {
		return nil, false, err
	}
	return rewritten, true, nil
}

func allowsSelfEscalationChoice(operation Operation, raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return true
	}
	var choice string
	if json.Unmarshal(raw, &choice) == nil {
		switch choice {
		case "auto", "required":
			return true
		case "any":
			return operation == OperationAnthropicMessages
		default:
			return false
		}
	}
	if operation != OperationAnthropicMessages {
		return false
	}
	var choiceObject struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &choiceObject) != nil {
		return false
	}
	return choiceObject.Type == "auto" || choiceObject.Type == "any"
}

func hasSelfEscalationTool(operation Operation, raw json.RawMessage) bool {
	var tools []json.RawMessage
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	if json.Unmarshal(raw, &tools) != nil {
		return true
	}
	for _, rawTool := range tools {
		var tool struct {
			Name     string `json:"name"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(rawTool, &tool) != nil {
			continue
		}
		name := tool.Name
		if operation == OperationOpenAIChatCompletions {
			name = tool.Function.Name
		}
		if name == SelfEscalationToolName {
			return true
		}
	}
	return false
}

func appendSelfEscalationTool(operation Operation, raw json.RawMessage) (json.RawMessage, bool) {
	tools := make([]json.RawMessage, 0, 1)
	if len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &tools) != nil {
		return nil, false
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reason_code": map[string]any{
				"type": "string",
				"enum": selfEscalationReasons,
			},
		},
		"required":             []string{"reason_code"},
		"additionalProperties": false,
	}
	description := "Request a stronger model only when this task clearly exceeds your ability; this must be your first output."
	var tool any
	switch operation {
	case OperationAnthropicMessages:
		tool = map[string]any{
			"name": SelfEscalationToolName, "description": description,
			"input_schema": schema,
		}
	case OperationOpenAIChatCompletions:
		tool = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": SelfEscalationToolName, "description": description,
				"parameters": schema, "strict": true,
			},
		}
	case OperationOpenAIResponses:
		tool = map[string]any{
			"type": "function", "name": SelfEscalationToolName,
			"description": description, "parameters": schema, "strict": true,
		}
	default:
		return nil, false
	}
	encoded, err := json.Marshal(tool)
	if err != nil {
		return nil, false
	}
	tools = append(tools, encoded)
	encoded, err = json.Marshal(tools)
	return encoded, err == nil
}

func appendSelfEscalationPrompt(
	operation Operation,
	root map[string]json.RawMessage,
) (map[string]json.RawMessage, bool) {
	switch operation {
	case OperationAnthropicMessages:
		if !appendTextInstruction(root, "system", selfEscalationPrompt) {
			return nil, false
		}
	case OperationOpenAIChatCompletions:
		var messages []json.RawMessage
		if json.Unmarshal(root["messages"], &messages) != nil {
			return nil, false
		}
		instruction, _ := json.Marshal(map[string]string{
			"role": "system", "content": selfEscalationPrompt,
		})
		messages = append([]json.RawMessage{instruction}, messages...)
		root["messages"], _ = json.Marshal(messages)
	case OperationOpenAIResponses:
		if !appendTextInstruction(root, "instructions", selfEscalationPrompt) {
			return nil, false
		}
	default:
		return nil, false
	}
	return root, true
}

func appendTextInstruction(root map[string]json.RawMessage, field string, instruction string) bool {
	raw := root[field]
	if len(raw) == 0 || string(raw) == "null" {
		root[field], _ = json.Marshal(instruction)
		return true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		root[field], _ = json.Marshal(text + "\n\n" + instruction)
		return true
	}
	if field != "system" {
		return false
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return false
	}
	block, _ := json.Marshal(map[string]string{"type": "text", "text": instruction})
	blocks = append(blocks, block)
	root[field], _ = json.Marshal(blocks)
	return true
}
