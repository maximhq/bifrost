package openai

import (
	"bytes"
	"encoding/json"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// containerSessionsRequest is the part of a wire Responses request that decides whether a container is new.
type containerSessionsRequest struct {
	PreviousResponseID *string         `json:"previous_response_id"`
	Conversation       json.RawMessage `json:"conversation"`
	Input              json.RawMessage `json:"input"`
	Tools              []struct {
		Type        string          `json:"type"`
		Container   json.RawMessage `json:"container"`
		Environment *struct {
			Type        string `json:"type"`
			MemoryLimit string `json:"memory_limit"`
		} `json:"environment"`
	} `json:"tools"`
}

// containerSessionsInputItem is an earlier tool call replayed in the request input.
type containerSessionsInputItem struct {
	Type        string `json:"type"`
	ContainerID string `json:"container_id"`
	Environment *struct {
		ContainerID string `json:"container_id"`
	} `json:"environment"`
}

// recordResponsesContainerSessions records the auto containers this response started on its usage.
func recordResponsesContainerSessions(response *schemas.BifrostResponsesResponse, reqBody []byte) {
	if response == nil || response.Usage == nil {
		return
	}
	schemas.SetContainerSessions(&response.Usage.ToolUsage, countContainerSessions(reqBody, response.Output))
}

// recordPassthroughContainerSessions is recordResponsesContainerSessions for a raw body or SSE event.
func recordPassthroughContainerSessions(usage *schemas.BifrostPassthroughUsage, reqBody, body []byte) {
	if usage == nil || usage.LLMUsage == nil {
		return
	}
	if !bytes.Contains(body, []byte(`"code_interpreter_call"`)) && !bytes.Contains(body, []byte(`"shell_call"`)) {
		return
	}
	var w struct {
		Output   []schemas.ResponsesMessage `json:"output"`
		Response *struct {
			Output []schemas.ResponsesMessage `json:"output"`
		} `json:"response"`
	}
	if err := sonic.Unmarshal(body, &w); err != nil {
		return
	}
	output := w.Output
	if w.Response != nil {
		output = w.Response.Output
	}
	schemas.SetContainerSessions(&usage.LLMUsage.ToolUsage, countContainerSessions(reqBody, output))
}

// countContainerSessions returns the new auto containers in output by memory tier. Only tool calls count,
// since unused containers are free. Explicit containers were billed at create. Any request we cannot read,
// or whose context is stored upstream, is treated as reuse: under-billing beats billing a session twice.
func countContainerSessions(reqBody []byte, output []schemas.ResponsesMessage) map[string]int {
	if !hasContainerToolCall(output) || len(reqBody) == 0 {
		return nil
	}
	var req containerSessionsRequest
	if err := sonic.Unmarshal(reqBody, &req); err != nil {
		return nil
	}
	if req.PreviousResponseID != nil && *req.PreviousResponseID != "" {
		return nil
	}
	if len(req.Conversation) > 0 && !bytes.Equal(req.Conversation, []byte("null")) {
		return nil
	}

	codeTier, codeAuto, shellTier, shellAuto := "", false, "", false
	for i := range req.Tools {
		tool := &req.Tools[i]
		switch tool.Type {
		case string(schemas.ResponsesToolTypeCodeInterpreter):
			var container struct {
				Type        string `json:"type"`
				MemoryLimit string `json:"memory_limit"`
			}
			if sonic.Unmarshal(tool.Container, &container) == nil && container.Type == "auto" {
				codeTier, codeAuto = container.MemoryLimit, true
			}
		case string(schemas.ResponsesToolTypeShell):
			if tool.Environment != nil && tool.Environment.Type == "container_auto" {
				shellTier, shellAuto = tool.Environment.MemoryLimit, true
			}
		}
	}
	if !codeAuto && !shellAuto {
		return nil
	}

	seen := make(map[string]struct{})
	shellInContext := false
	var input []containerSessionsInputItem
	if sonic.Unmarshal(req.Input, &input) == nil {
		for i := range input {
			item := &input[i]
			switch item.Type {
			case string(schemas.ResponsesMessageTypeCodeInterpreterCall):
				seen[item.ContainerID] = struct{}{}
			case string(schemas.ResponsesMessageTypeShellCall):
				shellInContext = true
				if item.Environment != nil {
					seen[item.Environment.ContainerID] = struct{}{}
				}
			}
		}
	}

	var sessions map[string]int
	add := func(id, tier string) {
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		if tier == "" {
			tier = schemas.ContainerTierDefault
		}
		if sessions == nil {
			sessions = make(map[string]int, 1)
		}
		sessions[tier]++
	}
	for i := range output {
		msg := &output[i]
		if msg.Type == nil || msg.ResponsesToolMessage == nil {
			continue
		}
		switch *msg.Type {
		case schemas.ResponsesMessageTypeCodeInterpreterCall:
			if call := msg.ResponsesToolMessage.ResponsesCodeInterpreterToolCall; codeAuto && call != nil && call.ContainerID != "" {
				add(call.ContainerID, codeTier)
			}
		case schemas.ResponsesMessageTypeShellCall:
			if !shellAuto {
				continue
			}
			call := msg.ResponsesToolMessage.ResponsesShellCall
			if call != nil && call.Environment != nil && call.Environment.ContainerID != nil && *call.Environment.ContainerID != "" {
				add(*call.Environment.ContainerID, shellTier)
			} else if !shellInContext {
				// The shell guide's example shows no container id; every such call shares one session.
				add("", shellTier)
			}
		}
	}
	return sessions
}

// hasContainerToolCall keeps the request parse off responses that used no container tool.
func hasContainerToolCall(output []schemas.ResponsesMessage) bool {
	for i := range output {
		if t := output[i].Type; t != nil && (*t == schemas.ResponsesMessageTypeCodeInterpreterCall || *t == schemas.ResponsesMessageTypeShellCall) {
			return true
		}
	}
	return false
}
