package schemas

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// This file provides constructors for building OpenAI/Codex's native
// tool_search_call / tool_search_output items programmatically, for
// providers that need to REPLAY a completed tool_search result (e.g. one
// that actually executed on Anthropic's servers) into OpenAI's own wire
// vocabulary when a conversation's backend switches. See the cross-provider
// tool_search mapping doc:
// memory/anthropicschema/gen/fix-execution/expanded-coverage/tool-search-cross-provider-mapping.md
//
// These must go through ResponsesMessage's rawPreserved preservation path
// (only settable from within this package) rather than the struct's normal
// exported fields: tool_search_call.arguments is a JSON OBJECT on the wire
// (unlike function_call's JSON string) — building it via the plain
// ResponsesToolMessage.Arguments *string field would incorrectly re-encode
// it as a string when MarshalJSON runs the default path.

// openAIToolSearchCallWire is the wire shape OpenAI expects for a
// tool_search_call item — see isRawPreservedItem's doc comment for why
// arguments is an object here, unlike function_call.
type openAIToolSearchCallWire struct {
	Type      ResponsesMessageType `json:"type"`
	CallID    string               `json:"call_id"`
	Execution string               `json:"execution"`
	Arguments json.RawMessage      `json:"arguments"`
}

// NewOpenAIToolSearchCallItem builds a ResponsesMessage carrying a
// pre-serialized OpenAI-native tool_search_call item. argumentsJSON must be
// a JSON object (e.g. `{"query":"weather"}`); pass "{}" if unknown.
func NewOpenAIToolSearchCallItem(callID string, argumentsJSON string) (ResponsesMessage, error) {
	if argumentsJSON == "" {
		argumentsJSON = "{}"
	}
	// arguments must be a JSON object on the wire (see the file doc comment) --
	// validate here so malformed/non-object input (e.g. forwarded verbatim
	// from an upstream provider) surfaces as a clear error at construction
	// time instead of silently reaching an OpenAI-compatible backend as
	// invalid raw JSON.
	trimmed := bytes.TrimSpace([]byte(argumentsJSON))
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return ResponsesMessage{}, fmt.Errorf("tool_search_call arguments must be a JSON object, got: %s", argumentsJSON)
	}
	wire := openAIToolSearchCallWire{
		Type:      ResponsesMessageTypeToolSearchCall,
		CallID:    callID,
		Execution: "client",
		Arguments: json.RawMessage(argumentsJSON),
	}
	raw, err := MarshalSorted(wire)
	if err != nil {
		return ResponsesMessage{}, err
	}
	return ResponsesMessage{
		Type:         Ptr(ResponsesMessageTypeToolSearchCall),
		rawPreserved: raw,
	}, nil
}

// OpenAIToolSearchDiscoveredTool is one entry of a tool_search_output's
// tools[] array — OpenAI's own client-executed tool_search returns the full
// function definition for every newly-discovered tool (name, description,
// parameters, defer_loading), unlike Anthropic's tool_search_tool_result
// which only ever carries a bare name. Callers bridging an Anthropic-origin
// result into this shape must backfill Description/Parameters/DeferLoading
// from the tool's original declaration (e.g. the current request's tools[])
// — leave them nil if unavailable rather than fabricating values.
type OpenAIToolSearchDiscoveredTool struct {
	Name         string
	Description  *string
	Parameters   *ToolFunctionParameters
	DeferLoading *bool
}

type openAIToolSearchFunctionDefWire struct {
	Type         string                  `json:"type"`
	Name         string                  `json:"name"`
	Description  *string                 `json:"description,omitempty"`
	Parameters   *ToolFunctionParameters `json:"parameters,omitempty"`
	DeferLoading *bool                   `json:"defer_loading,omitempty"`
}

type openAIToolSearchOutputWire struct {
	Type      ResponsesMessageType              `json:"type"`
	CallID    string                            `json:"call_id"`
	Status    string                            `json:"status"`
	Execution string                            `json:"execution"`
	Tools     []openAIToolSearchFunctionDefWire `json:"tools"`
}

// NewOpenAIToolSearchOutputItem builds a ResponsesMessage carrying a
// pre-serialized OpenAI-native tool_search_output item listing the
// discovered tools. Always emits "tools": [] (never omitted/null) when
// discovered is empty, matching a genuine no-match OpenAI result.
func NewOpenAIToolSearchOutputItem(callID string, discovered []OpenAIToolSearchDiscoveredTool) (ResponsesMessage, error) {
	tools := make([]openAIToolSearchFunctionDefWire, 0, len(discovered))
	for _, d := range discovered {
		tools = append(tools, openAIToolSearchFunctionDefWire{
			Type:         string(ResponsesToolTypeFunction),
			Name:         d.Name,
			Description:  d.Description,
			Parameters:   d.Parameters,
			DeferLoading: d.DeferLoading,
		})
	}
	wire := openAIToolSearchOutputWire{
		Type:      ResponsesMessageTypeToolSearchOutput,
		CallID:    callID,
		Status:    "completed",
		Execution: "client",
		Tools:     tools,
	}
	raw, err := MarshalSorted(wire)
	if err != nil {
		return ResponsesMessage{}, err
	}
	return ResponsesMessage{
		Type:         Ptr(ResponsesMessageTypeToolSearchOutput),
		rawPreserved: raw,
	}, nil
}
