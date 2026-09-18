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
				Type:           schemas.ResponsesToolTypeShell,
				AllowedCallers: []string{"direct"},
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
		`"allowed_callers":["direct"]`,
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

// TestOpenAIAllowedCallersTranslation covers the caller vocabulary map: Anthropic
// names the sandbox caller by code execution tool version, OpenAI calls it
// "programmatic", and both know "direct". Unknown values stay put so OpenAI can
// reject them (it answers "Supported values are: 'direct' and 'programmatic'").
func TestOpenAIAllowedCallersTranslation(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "direct is shared", in: []string{"direct"}, want: []string{"direct"}},
		{name: "programmatic passes through", in: []string{"programmatic"}, want: []string{"programmatic"}},
		{name: "code execution caller", in: []string{"code_execution_20260120"}, want: []string{"programmatic"}},
		{name: "older code execution caller", in: []string{"code_execution_20250825"}, want: []string{"programmatic"}},
		{name: "newest code execution caller", in: []string{"code_execution_20260521"}, want: []string{"programmatic"}},
		{name: "both callers", in: []string{"direct", "code_execution_20260120"}, want: []string{"direct", "programmatic"}},
		{name: "collapses to one programmatic", in: []string{"code_execution_20250825", "code_execution_20260120"}, want: []string{"programmatic"}},
		{name: "unknown value is left for OpenAI to reject", in: []string{"bogus_value"}, want: []string{"bogus_value"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := openAIAllowedCallers(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestShellToolAllowedCallersReachOpenAITranslated pins the translation to the wire.
func TestShellToolAllowedCallersReachOpenAITranslated(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "gpt-5.2",
		Input: OpenAIResponsesRequestInput{
			OpenAIResponsesRequestInputArray: []schemas.ResponsesMessage{{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
			}},
		},
		ResponsesParameters: schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{{
				Type:               schemas.ResponsesToolTypeShell,
				AllowedCallers:     []string{"direct", "code_execution_20260120"},
				ResponsesToolShell: &schemas.ResponsesToolShell{},
			}},
		},
	}

	jsonBytes, err := req.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	raw := string(jsonBytes)
	if !strings.Contains(raw, `"allowed_callers":["direct","programmatic"]`) {
		t.Errorf("allowed_callers must reach OpenAI translated; raw=%s", raw)
	}
	if strings.Contains(raw, "code_execution") {
		t.Errorf("Anthropic caller vocabulary must not reach OpenAI; raw=%s", raw)
	}
}

// TestFilterUnsupportedToolsKeepsBareOpenAITools locks the two bare tool types in
// OpenAI's Tool union that carry no fields of their own. Both used to be stripped
// before the request left Bifrost: apply_patch is accepted by OpenAI today, and
// programmatic_tool_calling is what turns allowed_callers: ["programmatic"] on.
func TestFilterUnsupportedToolsKeepsBareOpenAITools(t *testing.T) {
	req := &OpenAIResponsesRequest{
		ResponsesParameters: schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeProgrammaticToolCalling},
				{Type: schemas.ResponsesToolTypeApplyPatch, AllowedCallers: []string{"programmatic"}},
			},
		},
	}

	req.filterUnsupportedTools(true)

	if len(req.Tools) != 2 {
		t.Fatalf("both bare tools must survive the filter; got %+v", req.Tools)
	}
	if req.Tools[0].Type != schemas.ResponsesToolTypeProgrammaticToolCalling ||
		req.Tools[1].Type != schemas.ResponsesToolTypeApplyPatch {
		t.Fatalf("tool types changed: %+v", req.Tools)
	}
}

// TestBareOpenAIToolsSerialize checks the wire shape: a bare type emits just its
// discriminator, and apply_patch keeps allowed_callers (OpenAI accepts it there).
func TestBareOpenAIToolsSerialize(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "gpt-5.1",
		Input: OpenAIResponsesRequestInput{
			OpenAIResponsesRequestInputArray: []schemas.ResponsesMessage{{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
			}},
		},
		ResponsesParameters: schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{Type: schemas.ResponsesToolTypeProgrammaticToolCalling},
				{Type: schemas.ResponsesToolTypeApplyPatch, AllowedCallers: []string{"code_execution_20260120"}},
				{
					Type:                         schemas.ResponsesToolTypeCodeInterpreter,
					AllowedCallers:               []string{"direct"},
					ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{Container: "auto"},
				},
			},
		},
	}

	jsonBytes, err := req.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	raw := string(jsonBytes)

	for _, want := range []string{
		`{"type":"programmatic_tool_calling"}`,
		`{"type":"apply_patch","allowed_callers":["programmatic"]}`, // Anthropic's caller vocabulary is translated
		`"type":"code_interpreter","allowed_callers":["direct"]`,    // allowed_callers is no longer stripped here
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("missing %s in request body; raw=%s", want, raw)
		}
	}
}

// TestResponsesToolBareTypesRoundTrip guards the codec for tool types that have no
// embedded struct: the discriminator and the common fields must survive.
func TestResponsesToolBareTypesRoundTrip(t *testing.T) {
	for _, input := range []string{
		`{"type":"programmatic_tool_calling"}`,
		`{"type":"apply_patch"}`,
		`{"type":"apply_patch","allowed_callers":["direct"]}`,
	} {
		var tool schemas.ResponsesTool
		if err := schemas.Unmarshal([]byte(input), &tool); err != nil {
			t.Fatalf("unmarshal %s: %v", input, err)
		}
		encoded, err := schemas.Marshal(tool)
		if err != nil {
			t.Fatalf("marshal %s: %v", input, err)
		}
		if string(encoded) != input {
			t.Errorf("round trip changed %s -> %s", input, string(encoded))
		}
	}
}
