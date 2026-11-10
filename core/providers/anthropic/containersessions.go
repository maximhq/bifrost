package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// freeCodeExecutionToolDate is the first web_search and web_fetch version that makes code execution free.
const freeCodeExecutionToolDate = "20260209"

// recordAnthropicContainerSession records a billable code execution session on *toolUsage. The response has
// no execution time, so this is one session at Anthropic's 5-minute minimum: the lowest possible charge.
func recordAnthropicContainerSession(toolUsage **schemas.ToolUsage, reqBody []byte, container *AnthropicResponseContainer) {
	if container == nil || container.ID == "" || !isBillableAnthropicContainer(reqBody, container.ID) {
		return
	}
	schemas.SetContainerSessions(toolUsage, map[string]int{schemas.ContainerTierDefault: 1})
}

// isBillableAnthropicContainer is false when the request carries a free web tool, reuses this container,
// or cannot be read: under-billing beats billing a session twice.
func isBillableAnthropicContainer(reqBody []byte, containerID string) bool {
	if len(reqBody) == 0 {
		return false
	}
	var req struct {
		Container json.RawMessage `json:"container"`
		Tools     []struct {
			Type string `json:"type"`
		} `json:"tools"`
	}
	if err := sonic.Unmarshal(reqBody, &req); err != nil {
		return false
	}
	for i := range req.Tools {
		if grantsFreeCodeExecution(req.Tools[i].Type) {
			return false
		}
	}
	return anthropicRequestContainerID(req.Container) != containerID
}

func grantsFreeCodeExecution(toolType string) bool {
	for _, prefix := range []string{"web_search_", "web_fetch_"} {
		if date, ok := strings.CutPrefix(toolType, prefix); ok && date >= freeCodeExecutionToolDate {
			return true
		}
	}
	return false
}

// anthropicRequestContainerID reads the request's container union: a bare id or an object with an id.
func anthropicRequestContainerID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var id string
	if sonic.Unmarshal(raw, &id) == nil {
		return id
	}
	var obj struct {
		ID string `json:"id"`
	}
	if sonic.Unmarshal(raw, &obj) == nil {
		return obj.ID
	}
	return ""
}
