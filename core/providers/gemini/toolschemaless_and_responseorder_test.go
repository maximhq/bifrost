package gemini

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// A function tool declared without a schema (an Anthropic tool with no input_schema converts
// to a ResponsesTool whose ResponsesToolFunction is nil) must still reach Gemini as a
// FunctionDeclaration; dropping it tells the model it has no tools.
func TestConvertResponsesToolsToGemini_FunctionWithoutSchema(t *testing.T) {
	tests := []struct {
		name           string
		tool           schemas.ResponsesTool
		wantName       string
		wantDesc       string
		wantParameters string
	}{
		{
			name: "nil function struct",
			tool: schemas.ResponsesTool{
				Type:        schemas.ResponsesToolTypeFunction,
				Name:        schemas.Ptr("get_time"),
				Description: schemas.Ptr("Returns the current time"),
			},
			wantName: "get_time",
			wantDesc: "Returns the current time",
		},
		{
			name: "nil function struct and no description",
			tool: schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeFunction,
				Name: schemas.Ptr("ping"),
			},
			wantName: "ping",
		},
		{
			name: "function struct with parameters",
			tool: schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeFunction,
				Name: schemas.Ptr("lookup"),
				ResponsesToolFunction: &schemas.ResponsesToolFunction{
					Parameters: &schemas.ToolFunctionParameters{Type: "object"},
				},
			},
			wantName:       "lookup",
			wantParameters: `{"type":"object","properties":{}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tools, err := convertResponsesToolsToGemini([]schemas.ResponsesTool{tt.tool}, false, schemas.Gemini, "gemini-2.5-pro")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(tools) != 1 || len(tools[0].FunctionDeclarations) != 1 {
				t.Fatalf("expected exactly one function declaration, got %+v", tools)
			}
			decl := tools[0].FunctionDeclarations[0]
			if decl.Name != tt.wantName {
				t.Errorf("name = %q, want %q", decl.Name, tt.wantName)
			}
			if decl.Description != tt.wantDesc {
				t.Errorf("description = %q, want %q", decl.Description, tt.wantDesc)
			}
			got := ""
			if raw, ok := decl.ParametersJSONSchema.(json.RawMessage); ok {
				got = string(raw)
			} else if decl.ParametersJSONSchema != nil {
				t.Fatalf("unexpected parametersJsonSchema type %T", decl.ParametersJSONSchema)
			}
			if got != tt.wantParameters {
				t.Errorf("parametersJsonSchema = %q, want %q", got, tt.wantParameters)
			}
		})
	}
}

// Function responses must follow the order of the preceding function calls. Vertex strips the
// call/response ids and pairs them by position, so results returned in a different order than
// the calls would otherwise be attached to the wrong call.
func TestConvertResponsesMessagesToGeminiContents_FunctionResponsesFollowCallOrder(t *testing.T) {
	call := func(id string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    schemas.Ptr(id),
				Name:      schemas.Ptr("read_file"),
				Arguments: schemas.Ptr(`{}`),
			},
		}
	}
	output := func(id string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: schemas.Ptr(id),
				Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("result " + id)},
			},
		}
	}
	user := schemas.ResponsesMessage{
		Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("continue")},
	}

	tests := []struct {
		name     string
		messages []schemas.ResponsesMessage
		wantIDs  []string
	}{
		{
			name:     "out of order results are reordered (last message)",
			messages: []schemas.ResponsesMessage{call("a"), call("b"), call("c"), output("c"), output("a"), output("b")},
			wantIDs:  []string{"a", "b", "c"},
		},
		{
			name:     "out of order results are reordered (flushed by next message)",
			messages: []schemas.ResponsesMessage{call("a"), call("b"), output("b"), output("a"), user},
			wantIDs:  []string{"a", "b"},
		},
		{
			name:     "unmatched results keep relative order at the end",
			messages: []schemas.ResponsesMessage{call("a"), call("b"), output("x"), output("b"), output("y"), output("a")},
			wantIDs:  []string{"a", "b", "x", "y"},
		},
		{
			name:     "in order results are unchanged",
			messages: []schemas.ResponsesMessage{call("a"), call("b"), output("a"), output("b")},
			wantIDs:  []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contents, _, err := convertResponsesMessagesToGeminiContents(tt.messages, "gemini-2.5-pro", schemas.Vertex)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var gotIDs []string
			for _, c := range contents {
				for _, p := range c.Parts {
					if p.FunctionResponse != nil {
						gotIDs = append(gotIDs, p.FunctionResponse.ID)
					}
				}
			}
			if len(gotIDs) != len(tt.wantIDs) {
				t.Fatalf("function response ids = %v, want %v", gotIDs, tt.wantIDs)
			}
			for i := range gotIDs {
				if gotIDs[i] != tt.wantIDs[i] {
					t.Fatalf("function response ids = %v, want %v", gotIDs, tt.wantIDs)
				}
			}
		})
	}
}
