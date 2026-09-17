package openai

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestFilterUnsupportedToolsKeepsShell locks the shell tool into the OpenAI
// allow list. Before this it was dropped before the request left Bifrost, so the
// model never saw the tool it was asked to use.
func TestFilterUnsupportedToolsKeepsShell(t *testing.T) {
	req := &OpenAIResponsesRequest{
		ResponsesParameters: schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{
					Type: schemas.ResponsesToolTypeShell,
					ResponsesToolShell: &schemas.ResponsesToolShell{
						Environment: &schemas.ResponsesToolShellEnvironment{Type: "local"},
					},
				},
			},
		},
	}

	req.filterUnsupportedTools(true)

	if len(req.Tools) != 1 || req.Tools[0].Type != schemas.ResponsesToolTypeShell {
		t.Fatalf("shell tool must survive the filter; got %+v", req.Tools)
	}
	if req.Tools[0].ResponsesToolShell == nil || req.Tools[0].ResponsesToolShell.Environment == nil {
		t.Fatalf("shell environment must survive the filter; got %+v", req.Tools[0])
	}
}

// TestShellToolSerializesForOpenAI checks the whole request body a shell turn
// produces: the tool definition, and a replayed shell_call / shell_call_output pair.
func TestShellToolSerializesForOpenAI(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "gpt-5.1",
		Input: OpenAIResponsesRequestInput{
			OpenAIResponsesRequestInputArray: []schemas.ResponsesMessage{
				{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeShellCall),
					ID:   schemas.Ptr("shc_1"),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: schemas.Ptr("call_1"),
						Action: &schemas.ResponsesToolMessageActionStruct{
							ResponsesShellToolCallAction: &schemas.ResponsesShellToolCallAction{
								Commands:  []string{"echo hi"},
								TimeoutMS: schemas.Ptr(5000),
							},
						},
						ResponsesToolCallEnvelope: &schemas.ResponsesToolCallEnvelope{
							Environment: &schemas.ResponsesShellCallEnvironment{Type: "local"},
						},
					},
				},
				{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeShellCallOutput),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: schemas.Ptr("call_1"),
						Output: &schemas.ResponsesToolMessageOutputStruct{
							ResponsesShellCallOutput: []schemas.ResponsesShellCallOutputContent{{
								Stdout:  "hi\n",
								Outcome: schemas.ResponsesShellCallOutcome{Type: "exit", ExitCode: schemas.Ptr(0)},
							}},
						},
						ResponsesToolCallEnvelope: &schemas.ResponsesToolCallEnvelope{MaxOutputLength: schemas.Ptr(1000)},
					},
				},
			},
		},
		ResponsesParameters: schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{{
				Type: schemas.ResponsesToolTypeShell,
				ResponsesToolShell: &schemas.ResponsesToolShell{
					Environment: &schemas.ResponsesToolShellEnvironment{
						Type:          "container_auto",
						MemoryLimit:   schemas.Ptr("4g"),
						NetworkPolicy: &schemas.ResponsesToolShellNetworkPolicy{Type: "allowlist", AllowedDomains: []string{"example.com"}},
					},
				},
			}},
		},
	}

	jsonBytes, err := req.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	raw := string(jsonBytes)

	for _, want := range []string{
		`"type":"shell"`,
		`"type":"container_auto"`,
		`"memory_limit":"4g"`,
		`"allowed_domains":["example.com"]`,
		`"commands":["echo hi"]`,
		`"timeout_ms":5000`,
		`"shell_call_output"`,
		`"exit_code":0`,
		`"max_output_length":1000`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("missing %s in request body; raw=%s", want, raw)
		}
	}
}
