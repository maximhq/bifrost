package openai

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

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

// recordResponsesServerToolUsage records what pricing needs from the request onto the response's usage.
func recordResponsesServerToolUsage(response *schemas.BifrostResponsesResponse, reqBody []byte) {
	if response == nil || response.Usage == nil {
		return
	}
	schemas.SetContainerSessions(&response.Usage.ToolUsage, countContainerSessions(reqBody, response.Output, int64(response.CreatedAt)))
	recordImageGenToolModel(response.Usage.ToolUsage, reqBody)
}

// recordImageGenToolModel saves the image tool's model for pricing. Absent means the API's default model.
func recordImageGenToolModel(toolUsage *schemas.ToolUsage, reqBody []byte) {
	if toolUsage == nil || !toolUsage.ImageGen.HasTokens() || len(reqBody) == 0 {
		return
	}
	var req struct {
		Tools []struct {
			Type  string `json:"type"`
			Model string `json:"model"`
		} `json:"tools"`
	}
	if sonic.Unmarshal(reqBody, &req) != nil {
		return
	}
	for i := range req.Tools {
		if req.Tools[i].Type == string(schemas.ResponsesToolTypeImageGeneration) {
			toolUsage.ImageGenModel = req.Tools[i].Model
			return
		}
	}
}

// recordPassthroughContainerSessions counts container sessions from a raw body or SSE event.
func recordPassthroughContainerSessions(usage *schemas.BifrostPassthroughUsage, reqBody, body []byte) {
	if usage == nil || usage.LLMUsage == nil {
		return
	}
	if !bytes.Contains(body, []byte(`"code_interpreter_call"`)) && !bytes.Contains(body, []byte(`"shell_call"`)) {
		return
	}
	var w struct {
		CreatedAt float64                    `json:"created_at"`
		Output    []schemas.ResponsesMessage `json:"output"`
		Response  *struct {
			CreatedAt float64                    `json:"created_at"`
			Output    []schemas.ResponsesMessage `json:"output"`
		} `json:"response"`
	}
	if err := sonic.Unmarshal(body, &w); err != nil {
		return
	}
	output, createdAt := w.Output, w.CreatedAt
	if w.Response != nil {
		output, createdAt = w.Response.Output, w.Response.CreatedAt
	}
	schemas.SetContainerSessions(&usage.LLMUsage.ToolUsage, countContainerSessions(reqBody, output, int64(createdAt)))
}

// countContainerSessions returns the new auto containers in output by memory tier. Only tool calls count,
// since unused containers are free. Explicit containers were billed at create. Any request we cannot read
// is treated as reuse, and context stored upstream counts only containers started during this response:
// under-billing beats billing a session twice.
func countContainerSessions(reqBody []byte, output []schemas.ResponsesMessage, responseCreatedAt int64) map[string]int {
	if !hasContainerToolCall(output) || len(reqBody) == 0 {
		return nil
	}
	var req containerSessionsRequest
	if err := sonic.Unmarshal(reqBody, &req); err != nil {
		return nil
	}
	storedContext := (req.PreviousResponseID != nil && *req.PreviousResponseID != "") ||
		(len(req.Conversation) > 0 && !bytes.Equal(req.Conversation, []byte("null")))

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
		if storedContext && !containerStartedDuring(id, responseCreatedAt) {
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

// maxContainerStartDelaySeconds bounds a container's start after its response's, so an id format change bills nothing.
const maxContainerStartDelaySeconds = 24 * 60 * 60

// containerStartedDuring reads the creation time OpenAI encodes as the first 8 hex digits of a container id.
func containerStartedDuring(id string, responseCreatedAt int64) bool {
	hex, ok := strings.CutPrefix(id, "cntr_")
	if !ok || len(hex) < 8 || responseCreatedAt <= 0 {
		return false
	}
	startedAt, err := strconv.ParseInt(hex[:8], 16, 64)
	return err == nil && startedAt >= responseCreatedAt && startedAt-responseCreatedAt <= maxContainerStartDelaySeconds
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
