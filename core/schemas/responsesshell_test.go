package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// gjsonRaw returns the raw JSON of key, or "" when it is absent.
func gjsonRaw(payload, key string) string {
	result := gjson.Get(payload, key)
	if !result.Exists() {
		return ""
	}
	return result.Raw
}

// =============================================================================
// OpenAI shell tool (the successor to local_shell)
// =============================================================================

// TestResponsesToolShellRoundTrip covers the three environment shapes of the
// shell tool definition. Every field has to survive: OpenAI validates the
// environment union, so a dropped key either changes where commands run or 400s.
func TestResponsesToolShellRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "local",
			input: `{"type":"shell","environment":{"type":"local"}}`,
		},
		{
			name:  "local with skills",
			input: `{"type":"shell","environment":{"type":"local","skills":[{"name":"pdf","description":"Fill PDFs","path":"/skills/pdf"}]}}`,
		},
		{
			name:  "container_auto with network allowlist and domain secrets",
			input: `{"type":"shell","environment":{"type":"container_auto","file_ids":["file-1"],"memory_limit":"4g","network_policy":{"type":"allowlist","allowed_domains":["example.com"],"domain_secrets":[{"domain":"example.com","name":"API_KEY","value":"sk-test"}]}}}`,
		},
		{
			name:  "container_auto with disabled network and skills",
			input: `{"type":"shell","environment":{"type":"container_auto","network_policy":{"type":"disabled"},"skills":[{"type":"skill_reference","skill_id":"skill_123","version":"latest"},{"type":"inline","name":"csv","description":"CSV tools","source":{"type":"base64","media_type":"application/zip","data":"UEsDBA=="}}]}}`,
		},
		{
			name:  "container_reference",
			input: `{"type":"shell","environment":{"type":"container_reference","container_id":"cntr_123"}}`,
		},
		{
			name:  "allowed_callers",
			input: `{"type":"shell","allowed_callers":["direct"],"environment":{"type":"local"}}`,
		},
		{
			name:  "no environment",
			input: `{"type":"shell"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tool ResponsesTool
			require.NoError(t, Unmarshal([]byte(tt.input), &tool))
			assert.Equal(t, ResponsesToolTypeShell, tool.Type)

			encoded, err := Marshal(tool)
			require.NoError(t, err)
			assert.JSONEq(t, tt.input, string(encoded))
		})
	}
}

// TestResponsesShellCallRoundTrip locks the shell_call item. Its action carries no
// "type" discriminator, so it is probed by shape — without that it decoded as a
// computer action and the commands were lost.
func TestResponsesShellCallRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "local call",
			input: `{"action":{"commands":["ls -la"]},"call_id":"call_1","id":"shc_1","status":"in_progress","type":"shell_call"}`,
		},
		{
			name:  "limits, environment and created_by",
			input: `{"action":{"commands":["sleep 1","echo hi"],"max_output_length":2000,"timeout_ms":5000},"call_id":"call_2","created_by":"asst_1","environment":{"type":"container_reference","container_id":"cntr_1"},"id":"shc_2","status":"completed","type":"shell_call"}`,
		},
		{
			name:  "program caller",
			input: `{"action":{"commands":["pwd"]},"call_id":"call_3","caller":{"type":"program","caller_id":"call_parent"},"id":"shc_3","status":"completed","type":"shell_call"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg ResponsesMessage
			require.NoError(t, Unmarshal([]byte(tt.input), &msg))
			require.NotNil(t, msg.Type)
			assert.Equal(t, ResponsesMessageTypeShellCall, *msg.Type)
			require.NotNil(t, msg.ResponsesToolMessage)
			require.NotNil(t, msg.Action)
			require.NotNil(t, msg.Action.ResponsesShellToolCallAction)
			assert.NotEmpty(t, msg.Action.ResponsesShellToolCallAction.Commands)

			encoded, err := Marshal(msg)
			require.NoError(t, err)
			assert.JSONEq(t, tt.input, string(encoded))
		})
	}
}

// TestResponsesShellCallOutputRoundTrip locks the shell_call_output item, whose
// "output" is an array of {stdout, stderr, outcome} — a shape that would
// otherwise be swallowed by the content-block decode.
func TestResponsesShellCallOutputRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "exit outcome",
			input: `{"call_id":"call_1","id":"shco_1","output":[{"outcome":{"type":"exit","exit_code":0},"stderr":"","stdout":"hello\n"}],"status":"completed","type":"shell_call_output"}`,
		},
		{
			name:  "timeout outcome with max_output_length",
			input: `{"call_id":"call_2","id":"shco_2","max_output_length":1000,"output":[{"outcome":{"type":"timeout"},"stderr":"killed","stdout":""}],"status":"completed","type":"shell_call_output"}`,
		},
		{
			name:  "multiple chunks with caller and created_by",
			input: `{"call_id":"call_3","caller":{"type":"direct"},"created_by":"asst_1","id":"shco_3","output":[{"created_by":"asst_1","outcome":{"type":"exit","exit_code":0},"stderr":"","stdout":"one"},{"outcome":{"type":"exit","exit_code":1},"stderr":"boom","stdout":"two"}],"status":"completed","type":"shell_call_output"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg ResponsesMessage
			require.NoError(t, Unmarshal([]byte(tt.input), &msg))
			require.NotNil(t, msg.ResponsesToolMessage)
			require.NotNil(t, msg.Output)
			require.NotEmpty(t, msg.Output.ResponsesShellCallOutput)
			assert.Nil(t, msg.Output.ResponsesFunctionToolCallOutputBlocks)

			encoded, err := Marshal(msg)
			require.NoError(t, err)
			assert.JSONEq(t, tt.input, string(encoded))
		})
	}
}

// TestResponsesToolOutputArrayStillDecodesAsContentBlocks guards the shell probe:
// a function_call_output array must not be claimed by the shell variant.
func TestResponsesToolOutputArrayStillDecodesAsContentBlocks(t *testing.T) {
	var output ResponsesToolMessageOutputStruct
	require.NoError(t, Unmarshal([]byte(`[{"type":"output_text","text":"done"}]`), &output))
	assert.Nil(t, output.ResponsesShellCallOutput)
	require.Len(t, output.ResponsesFunctionToolCallOutputBlocks, 1)

	// An array whose items only look shell-ish (no outcome) is not shell output either.
	var partial ResponsesToolMessageOutputStruct
	require.NoError(t, Unmarshal([]byte(`[{"stdout":"x"}]`), &partial))
	assert.Nil(t, partial.ResponsesShellCallOutput)
}

// TestResponsesShellStreamEvents locks the five shell stream events. The
// shell_call_output_content.delta event is the only one whose `delta` is an object:
// the plain decode rejects it (which used to drop the event) and the provider falls
// back to UnmarshalResponsesStreamObjectDelta, as this test does.
func TestResponsesShellStreamEvents(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		assert func(t *testing.T, resp BifrostResponsesStreamResponse)
	}{
		{
			name:  "command added",
			input: `{"type":"response.shell_call_command.added","command":"ls -la","command_index":0,"output_index":1,"sequence_number":7}`,
			assert: func(t *testing.T, resp BifrostResponsesStreamResponse) {
				require.NotNil(t, resp.Command)
				assert.Equal(t, "ls -la", *resp.Command)
				require.NotNil(t, resp.CommandIndex)
				assert.Equal(t, 0, *resp.CommandIndex)
			},
		},
		{
			name:  "command delta keeps a string delta",
			input: `{"type":"response.shell_call_command.delta","command_index":1,"delta":"ls ","output_index":1,"sequence_number":8}`,
			assert: func(t *testing.T, resp BifrostResponsesStreamResponse) {
				require.NotNil(t, resp.Delta)
				assert.Equal(t, "ls ", *resp.Delta)
				assert.Nil(t, resp.ShellOutputDelta)
			},
		},
		{
			name:  "command done",
			input: `{"type":"response.shell_call_command.done","command":"ls -la","command_index":1,"output_index":1,"sequence_number":9}`,
			assert: func(t *testing.T, resp BifrostResponsesStreamResponse) {
				require.NotNil(t, resp.Command)
				assert.Equal(t, "ls -la", *resp.Command)
			},
		},
		{
			name:  "output content delta carries an object delta",
			input: `{"type":"response.shell_call_output_content.delta","command_index":0,"delta":{"stdout":"hello"},"item_id":"shco_1","output_index":1,"sequence_number":10}`,
			assert: func(t *testing.T, resp BifrostResponsesStreamResponse) {
				assert.Nil(t, resp.Delta)
				require.NotNil(t, resp.ShellOutputDelta)
				require.NotNil(t, resp.ShellOutputDelta.Stdout)
				assert.Equal(t, "hello", *resp.ShellOutputDelta.Stdout)
			},
		},
		{
			name:  "output content done",
			input: `{"type":"response.shell_call_output_content.done","command_index":0,"item_id":"shco_1","output":[{"outcome":{"type":"exit","exit_code":0},"stderr":"","stdout":"hello"}],"output_index":1,"sequence_number":11}`,
			assert: func(t *testing.T, resp BifrostResponsesStreamResponse) {
				require.Len(t, resp.Output, 1)
				assert.Equal(t, "hello", resp.Output[0].Stdout)
				require.NotNil(t, resp.Output[0].Outcome.ExitCode)
				assert.Equal(t, 0, *resp.Output[0].Outcome.ExitCode)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resp BifrostResponsesStreamResponse
			if err := Unmarshal([]byte(tt.input), &resp); err != nil {
				resp = BifrostResponsesStreamResponse{}
				require.NoError(t, UnmarshalResponsesStreamObjectDelta([]byte(tt.input), &resp))
			}
			tt.assert(t, resp)

			// The /openai route re-emits the event through WithDefaults.
			encoded, err := Marshal(resp.WithDefaults())
			require.NoError(t, err)
			for _, key := range []string{"command", "command_index", "delta", "output"} {
				expected := gjsonRaw(tt.input, key)
				if expected == "" {
					continue
				}
				assert.JSONEq(t, expected, gjsonRaw(string(encoded), key), "field %q", key)
			}
		})
	}
}

// TestUnmarshalResponsesStreamObjectDeltaRejectsStringDelta keeps the fallback from
// claiming ordinary events: only an object `delta` belongs to it.
func TestUnmarshalResponsesStreamObjectDeltaRejectsStringDelta(t *testing.T) {
	var resp BifrostResponsesStreamResponse
	err := UnmarshalResponsesStreamObjectDelta([]byte(`{"type":"response.output_text.delta","delta":"hi"}`), &resp)
	require.Error(t, err)
	assert.Nil(t, resp.ShellOutputDelta)
}

// TestShellCallOutputText renders the chunks the way summaries and Anthropic see them.
func TestShellCallOutputText(t *testing.T) {
	text := ShellCallOutputText([]ResponsesShellCallOutputContent{
		{Stdout: "one", Stderr: ""},
		{Stdout: "", Stderr: "boom"},
	})
	assert.Equal(t, "one\nboom", text)
	assert.Equal(t, "", ShellCallOutputText(nil))
}

// TestResponsesApplyPatchCallRoundTrip locks the apply_patch items. OpenAI requires
// `operation` on a replayed call — without it the turn is rejected with
// "Missing required parameter: 'input[1].operation'".
func TestResponsesApplyPatchCallRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "create_file",
			input: `{"call_id":"call_1","id":"apc_1","operation":{"type":"create_file","path":"hello.txt","diff":"+hi\n"},"status":"completed","type":"apply_patch_call"}`,
		},
		{
			name:  "update_file",
			input: `{"call_id":"call_2","id":"apc_2","operation":{"type":"update_file","path":"main.go","diff":"@@\n-old\n+new\n"},"status":"in_progress","type":"apply_patch_call"}`,
		},
		{
			name:  "delete_file carries no diff",
			input: `{"call_id":"call_3","id":"apc_3","operation":{"type":"delete_file","path":"stale.txt"},"status":"completed","type":"apply_patch_call"}`,
		},
		{
			name:  "caller and created_by",
			input: `{"call_id":"call_4","caller":{"type":"program","caller_id":"call_parent"},"created_by":"asst_1","id":"apc_4","operation":{"type":"create_file","path":"a.txt","diff":"+a\n"},"status":"completed","type":"apply_patch_call"}`,
		},
		{
			name:  "output item",
			input: `{"call_id":"call_1","id":"apco_1","output":"done","status":"completed","type":"apply_patch_call_output"}`,
		},
		{
			name:  "failed output item",
			input: `{"call_id":"call_2","id":"apco_2","status":"failed","type":"apply_patch_call_output"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var msg ResponsesMessage
			require.NoError(t, Unmarshal([]byte(tt.input), &msg))
			require.NotNil(t, msg.Type)
			require.NotNil(t, msg.ResponsesToolMessage)

			encoded, err := Marshal(msg)
			require.NoError(t, err)
			assert.JSONEq(t, tt.input, string(encoded))
		})
	}
}

// TestDeepCopyResponsesMessageCopiesApplyPatchOperation keeps the copy from sharing
// the operation pointers with the original.
func TestDeepCopyResponsesMessageCopiesApplyPatchOperation(t *testing.T) {
	original := ResponsesMessage{
		Type: Ptr(ResponsesMessageTypeApplyPatchCall),
		ResponsesToolMessage: &ResponsesToolMessage{
			CallID: Ptr("call_1"),
			ResponsesToolCallEnvelope: &ResponsesToolCallEnvelope{
				Operation: &ResponsesApplyPatchOperation{Type: "create_file", Path: "hello.txt", Diff: Ptr("+hi\n")},
			},
		},
	}

	copied := DeepCopyResponsesMessage(original)
	require.NotNil(t, copied.ResponsesToolMessage)
	require.NotNil(t, copied.ResponsesToolCallEnvelope)
	operation := copied.ResponsesToolCallEnvelope.Operation
	require.NotNil(t, operation)
	assert.Equal(t, "hello.txt", operation.Path)
	require.NotNil(t, operation.Diff)
	assert.Equal(t, "+hi\n", *operation.Diff)

	*original.ResponsesToolCallEnvelope.Operation.Diff = "mutated"
	original.ResponsesToolCallEnvelope.Operation.Path = "mutated"
	assert.Equal(t, "+hi\n", *copied.ResponsesToolCallEnvelope.Operation.Diff)
	assert.Equal(t, "hello.txt", copied.ResponsesToolCallEnvelope.Operation.Path)
}

// TestResponsesApplyPatchStreamEvents keeps the assembled patch on the done event:
// the diff deltas are ordinary string deltas, but `diff` had no field to land in.
func TestResponsesApplyPatchStreamEvents(t *testing.T) {
	deltaEvent := `{"type":"response.apply_patch_call_operation_diff.delta","delta":"+hello","item_id":"apc_1","output_index":0,"sequence_number":3}`
	doneEvent := `{"type":"response.apply_patch_call_operation_diff.done","diff":"+hello\n","item_id":"apc_1","output_index":0,"sequence_number":4}`

	var delta BifrostResponsesStreamResponse
	require.NoError(t, Unmarshal([]byte(deltaEvent), &delta))
	require.NotNil(t, delta.Delta)
	assert.Equal(t, "+hello", *delta.Delta)
	assert.Nil(t, delta.Diff)

	var done BifrostResponsesStreamResponse
	require.NoError(t, Unmarshal([]byte(doneEvent), &done))
	require.NotNil(t, done.Diff)
	assert.Equal(t, "+hello\n", *done.Diff)

	// The /openai route re-emits through WithDefaults.
	encoded, err := Marshal(done.WithDefaults())
	require.NoError(t, err)
	assert.Equal(t, `"+hello\n"`, gjsonRaw(string(encoded), "diff"))
}
