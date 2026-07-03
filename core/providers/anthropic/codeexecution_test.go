package anthropic

import (
	"strings"
	"time"
	"unicode/utf8"

	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"

	"github.com/bytedance/sonic"
	"github.com/tidwall/gjson"
)

// rawBashCodeExecResponse is the user-reported Anthropic response for a bash
// code_execution_20250825 run (server_tool_use: bash_code_execution + result).
var rawBashCodeExecResponse = `{
  "model": "claude-opus-4-8",
  "id": "msg_01NoFSPs32EMqtmR2g4e1bKa",
  "type": "message",
  "role": "assistant",
  "content": [
    {
      "type": "server_tool_use",
      "id": "srvtoolu_01D6DLT2QGNjMqGAai9EgcrM",
      "name": "bash_code_execution",
      "input": { "command": "python3 -c \"import statistics; print(statistics.mean([1,2,3]))\"" }
    },
    {
      "type": "bash_code_execution_tool_result",
      "tool_use_id": "srvtoolu_01D6DLT2QGNjMqGAai9EgcrM",
      "content": {
        "type": "bash_code_execution_result",
        "stdout": "Mean: 5.5\nPopulation SD: 2.8722813232690143\n",
        "stderr": "",
        "return_code": 0,
        "content": []
      }
    },
    { "type": "text", "text": "Here are the results." }
  ],
  "container": { "id": "container_018jVZJnpVtdyWij4ULp3qTN", "expires_at": "2026-06-18T18:46:48.969255Z" },
  "stop_reason": "end_turn",
  "usage": { "input_tokens": 6021, "output_tokens": 424 }
}`

// TestCodeExecution_BashResponseRoundTrip verifies the bash code-execution
// blocks survive Anthropic -> Bifrost -> Anthropic on the non-streaming path,
// preserving both the neutral code_interpreter_call view and the container.
func TestCodeExecution_BashResponseRoundTrip(t *testing.T) {
	var resp AnthropicMessageResponse
	if err := sonic.Unmarshal([]byte(rawBashCodeExecResponse), &resp); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})

	bifrostResp := resp.ToBifrostResponsesResponse(ctx)
	if bifrostResp == nil {
		t.Fatal("ToBifrostResponsesResponse returned nil")
	}

	var found bool
	for _, out := range bifrostResp.Output {
		if out.Type == nil || *out.Type != schemas.ResponsesMessageTypeCodeInterpreterCall {
			continue
		}
		found = true
		tm := out.ResponsesToolMessage
		if tm == nil || tm.ResponsesCodeInterpreterToolCall == nil || tm.ResponsesCodeExecutionCall == nil {
			t.Fatal("code_interpreter_call present but payloads missing")
		}
		ci := tm.ResponsesCodeInterpreterToolCall
		cec := tm.ResponsesCodeExecutionCall

		if ci.ContainerID != "container_018jVZJnpVtdyWij4ULp3qTN" {
			t.Errorf("container_id = %q, want container_018jVZJnpVtdyWij4ULp3qTN", ci.ContainerID)
		}
		if ci.Code == nil || *ci.Code == "" {
			t.Error("neutral code not populated from bash command")
		}
		if len(ci.Outputs) == 0 || ci.Outputs[0].ResponsesCodeInterpreterOutputLogs == nil {
			t.Fatal("neutral logs output not populated from stdout")
		}
		if ci.Outputs[0].ResponsesCodeInterpreterOutputLogs.Logs == "" {
			t.Error("logs output empty")
		}
		if cec.ToolName != "bash_code_execution" {
			t.Errorf("carry tool_name = %q, want bash_code_execution", cec.ToolName)
		}
		if cec.ResultType != "bash_code_execution_result" {
			t.Errorf("carry result_type = %q, want bash_code_execution_result", cec.ResultType)
		}
		if cec.ReturnCode == nil || *cec.ReturnCode != 0 {
			t.Errorf("carry return_code = %v, want 0", cec.ReturnCode)
		}
		if cec.ContainerExpiresAt == nil || *cec.ContainerExpiresAt != "2026-06-18T18:46:48.969255Z" {
			t.Errorf("carry container expiry not preserved: %v", cec.ContainerExpiresAt)
		}
	}
	if !found {
		t.Fatal("neutral output has no code_interpreter_call — code execution blocks dropped on parse")
	}

	// Reverse back to Anthropic and assert the wire shape.
	back := ToAnthropicResponsesResponse(ctx, bifrostResp)
	if back == nil {
		t.Fatal("ToAnthropicResponsesResponse returned nil")
	}
	out, err := sonic.Marshal(back)
	if err != nil {
		t.Fatalf("marshal back: %v", err)
	}

	var types []string
	var serverToolName, resultToolUseID, innerType, stdout string
	gjson.GetBytes(out, "content").ForEach(func(_, b gjson.Result) bool {
		bt := b.Get("type").String()
		types = append(types, bt)
		switch bt {
		case "server_tool_use":
			serverToolName = b.Get("name").String()
		case "bash_code_execution_tool_result":
			resultToolUseID = b.Get("tool_use_id").String()
			if content := b.Get("content"); !content.IsObject() {
				t.Errorf("result content = %q, want a single object", content.Raw)
			}
			innerType = b.Get("content.type").String()
			stdout = b.Get("content.stdout").String()
		}
		return true
	})

	if len(types) != 3 || types[0] != "server_tool_use" || types[1] != "bash_code_execution_tool_result" || types[2] != "text" {
		t.Fatalf("converted blocks = %v, want [server_tool_use bash_code_execution_tool_result text]\n%s", types, out)
	}
	if serverToolName != "bash_code_execution" {
		t.Errorf("server_tool_use name = %q, want bash_code_execution", serverToolName)
	}
	if resultToolUseID != "srvtoolu_01D6DLT2QGNjMqGAai9EgcrM" {
		t.Errorf("result tool_use_id = %q", resultToolUseID)
	}
	if innerType != "bash_code_execution_result" {
		t.Errorf("inner result type = %q, want bash_code_execution_result", innerType)
	}
	if stdout == "" {
		t.Error("stdout not preserved through round-trip")
	}

	// Container must be restored at the response top level.
	if got := gjson.GetBytes(out, "container.id").String(); got != "container_018jVZJnpVtdyWij4ULp3qTN" {
		t.Errorf("response container.id = %q, want container_018jVZJnpVtdyWij4ULp3qTN", got)
	}
	if got := gjson.GetBytes(out, "container.expires_at").String(); got != "2026-06-18T18:46:48.969255Z" {
		t.Errorf("response container.expires_at = %q", got)
	}

	// The verbatim command input must round-trip exactly.
	cmd := gjson.GetBytes(out, `content.0.input.command`).String()
	if cmd == "" || cmd != gjson.Get(rawBashCodeExecResponse, "content.0.input.command").String() {
		t.Errorf("server_tool_use input.command not preserved: %q", cmd)
	}
}

// rawTextEditorViewResponse exercises the text_editor_code_execution view variant.
var rawTextEditorViewResponse = `{
  "model": "claude-opus-4-8",
  "id": "msg_textedit",
  "type": "message",
  "role": "assistant",
  "content": [
    {
      "type": "server_tool_use",
      "id": "srvtoolu_view1",
      "name": "text_editor_code_execution",
      "input": { "command": "view", "path": "config.json" }
    },
    {
      "type": "text_editor_code_execution_tool_result",
      "tool_use_id": "srvtoolu_view1",
      "content": {
        "type": "text_editor_code_execution_result",
        "file_type": "text",
        "content": "{\n  \"debug\": true\n}",
        "num_lines": 3,
        "start_line": 1,
        "total_lines": 3
      }
    }
  ],
  "stop_reason": "end_turn",
  "usage": { "input_tokens": 10, "output_tokens": 20 }
}`

// TestCodeExecution_TextEditorViewRoundTrip verifies the text_editor view result
// (file_type + file contents) round-trips faithfully.
func TestCodeExecution_TextEditorViewRoundTrip(t *testing.T) {
	var resp AnthropicMessageResponse
	if err := sonic.Unmarshal([]byte(rawTextEditorViewResponse), &resp); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	ctx := schemas.NewBifrostContext(nil, time.Time{})

	bifrostResp := resp.ToBifrostResponsesResponse(ctx)
	var cec *schemas.ResponsesCodeExecutionCall
	for _, out := range bifrostResp.Output {
		if out.Type != nil && *out.Type == schemas.ResponsesMessageTypeCodeInterpreterCall && out.ResponsesToolMessage != nil {
			cec = out.ResponsesToolMessage.ResponsesCodeExecutionCall
		}
	}
	if cec == nil {
		t.Fatal("text_editor code_interpreter_call not produced")
	}
	if cec.ToolName != "text_editor_code_execution" {
		t.Errorf("tool_name = %q", cec.ToolName)
	}
	if cec.FileType == nil || *cec.FileType != "text" {
		t.Errorf("file_type not carried: %v", cec.FileType)
	}
	if cec.FileContent == nil || *cec.FileContent == "" {
		t.Errorf("view file content not carried: %v", cec.FileContent)
	}

	back := ToAnthropicResponsesResponse(ctx, bifrostResp)
	out, err := sonic.Marshal(back)
	if err != nil {
		t.Fatalf("marshal back: %v", err)
	}
	if got := gjson.GetBytes(out, "content.1.type").String(); got != "text_editor_code_execution_tool_result" {
		t.Fatalf("result block type = %q\n%s", got, out)
	}
	if got := gjson.GetBytes(out, "content.1.content.file_type").String(); got != "text" {
		t.Errorf("inner file_type = %q", got)
	}
	if got := gjson.GetBytes(out, "content.1.content.content").String(); got == "" {
		t.Error("inner view content not preserved")
	}
}

// TestCodeExecution_OpenAIOriginToAnthropic verifies that a neutral
// code_interpreter_call WITHOUT the Anthropic carry (as produced by an
// OpenAI-format client) still reconstructs valid Anthropic blocks.
func TestCodeExecution_OpenAIOriginToAnthropic(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	code := "print('hi')"
	bifrostResp := &schemas.BifrostResponsesResponse{
		ID: schemas.Ptr("resp_1"),
		Output: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeCodeInterpreterCall),
				ID:   schemas.Ptr("ci_1"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("ci_1"),
					ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{
						Code:        &code,
						ContainerID: "cntr_1",
						Outputs: []schemas.ResponsesCodeInterpreterOutput{
							{ResponsesCodeInterpreterOutputLogs: &schemas.ResponsesCodeInterpreterOutputLogs{Type: "logs", Logs: "hi\n"}},
						},
					},
				},
			},
		},
	}

	back := ToAnthropicResponsesResponse(ctx, bifrostResp)
	out, err := sonic.Marshal(back)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if got := gjson.GetBytes(out, "content.0.type").String(); got != "server_tool_use" {
		t.Fatalf("block 0 type = %q\n%s", got, out)
	}
	if got := gjson.GetBytes(out, "content.0.name").String(); got != "code_execution" {
		t.Errorf("default sub-tool name = %q, want code_execution", got)
	}
	if got := gjson.GetBytes(out, "content.0.input.code").String(); got != code {
		t.Errorf("input.code = %q, want %q", got, code)
	}
	if got := gjson.GetBytes(out, "content.1.type").String(); got != "code_execution_tool_result" {
		t.Errorf("result block type = %q", got)
	}
	if got := gjson.GetBytes(out, "content.1.content.stdout").String(); got != "hi\n" {
		t.Errorf("stdout synthesized from logs = %q, want \"hi\\n\"", got)
	}
	if got := gjson.GetBytes(out, "container.id").String(); got != "cntr_1" {
		t.Errorf("container.id = %q, want cntr_1", got)
	}
}

// bashCodeExecStreamEvents is a faithful subset of the Anthropic SSE for a
// bash_code_execution run: leading text, the server_tool_use with streamed
// input JSON, the result block, trailing text, and the final message_delta
// carrying the sandbox container.
var bashCodeExecStreamEvents = []string{
	`{"type":"message_start","message":{"model":"claude-opus-4-8","id":"msg_stream1","type":"message","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me calculate the mean."}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu_stream1","name":"bash_code_execution","input":{}}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\": \"py"}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"thon3 -c statistics\"}"}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"bash_code_execution_tool_result","tool_use_id":"srvtoolu_stream1","content":{"type":"bash_code_execution_result","stdout":"Mean: 5.5\n","stderr":"","return_code":0,"content":[]}}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"The mean is 5.5."}}`,
	`{"type":"content_block_stop","index":3}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn","container":{"id":"container_stream1","expires_at":"2026-06-25T13:57:10Z"}},"usage":{"output_tokens":40}}`,
	`{"type":"message_stop"}`,
}

// TestCodeExecution_BashStream verifies the streaming converter maps the
// Anthropic bash_code_execution SSE onto the OpenAI code_interpreter_call event
// sequence (in_progress -> code.delta/done -> interpreting -> completed ->
// output_item.done) and folds the container into the final response.completed.
func TestCodeExecution_BashStream(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	// Exercise the production Anthropic-compatible streaming path: the forward
	// converter emits a message_delta event carrying the container, and the state
	// -> ctx bridge (mirrored from anthropic.go) lets the reverse converter skip a
	// duplicate message_delta on response.completed.
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
	state := acquireAnthropicResponsesStreamState()
	defer releaseAnthropicResponsesStreamState(state)

	var emitted []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for _, raw := range bashCodeExecStreamEvents {
		var chunk AnthropicStreamEvent
		if err := sonic.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		responses, bErr, _ := chunk.ToBifrostResponsesStream(ctx, seq, state)
		if bErr != nil {
			t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
		}
		if state.HasEmittedMessageDelta {
			ctx.SetValue(schemas.BifrostContextKeyHasEmittedMessageDelta, true)
		}
		for _, r := range responses {
			seq++
			emitted = append(emitted, r)
		}
	}

	var sawAdded, sawInProgress, sawCodeDelta, sawCodeDone, sawInterpreting, sawCompleted, sawItemDone bool
	var codeDelta, codeDone string

	for _, e := range emitted {
		switch e.Type {
		case schemas.ResponsesStreamResponseTypeOutputItemAdded:
			if e.Item != nil && e.Item.Type != nil && *e.Item.Type == schemas.ResponsesMessageTypeCodeInterpreterCall {
				sawAdded = true
			}
		case schemas.ResponsesStreamResponseTypeCodeInterpreterCallInProgress:
			sawInProgress = true
		case schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDelta:
			sawCodeDelta = true
			if e.Delta != nil {
				codeDelta = *e.Delta
			}
		case schemas.ResponsesStreamResponseTypeCodeInterpreterCallCodeDone:
			sawCodeDone = true
			if e.Code != nil {
				codeDone = *e.Code
			}
		case schemas.ResponsesStreamResponseTypeCodeInterpreterCallInterpreting:
			sawInterpreting = true
		case schemas.ResponsesStreamResponseTypeCodeInterpreterCallCompleted:
			sawCompleted = true
		case schemas.ResponsesStreamResponseTypeOutputItemDone:
			if e.Item == nil || e.Item.Type == nil || *e.Item.Type != schemas.ResponsesMessageTypeCodeInterpreterCall {
				continue
			}
			sawItemDone = true
			tm := e.Item.ResponsesToolMessage
			if tm == nil || tm.ResponsesCodeExecutionCall == nil || tm.ResponsesCodeInterpreterToolCall == nil {
				t.Fatal("output_item.done code_interpreter_call missing payloads")
			}
			if e.Item.Status == nil || *e.Item.Status != "completed" {
				t.Errorf("item status = %v, want completed", e.Item.Status)
			}
			if tm.ResponsesCodeExecutionCall.ToolName != "bash_code_execution" {
				t.Errorf("carry tool_name = %q", tm.ResponsesCodeExecutionCall.ToolName)
			}
			if tm.ResponsesCodeExecutionCall.ReturnCode == nil || *tm.ResponsesCodeExecutionCall.ReturnCode != 0 {
				t.Errorf("carry return_code = %v, want 0", tm.ResponsesCodeExecutionCall.ReturnCode)
			}
			outs := tm.ResponsesCodeInterpreterToolCall.Outputs
			if len(outs) == 0 || outs[0].ResponsesCodeInterpreterOutputLogs == nil ||
				!strings.Contains(outs[0].ResponsesCodeInterpreterOutputLogs.Logs, "Mean: 5.5") {
				t.Errorf("neutral logs output not populated from stdout: %+v", outs)
			}
		}
	}

	if !sawAdded {
		t.Error("STREAM GAP: code_interpreter_call output_item.added not emitted")
	}
	if !sawInProgress {
		t.Error("STREAM GAP: code_interpreter_call.in_progress not emitted")
	}
	if !sawCodeDelta || !strings.Contains(codeDelta, "python3") {
		t.Errorf("STREAM GAP: code.delta not emitted/decoded (got %q)", codeDelta)
	}
	if !sawCodeDone || !strings.Contains(codeDone, "python3") {
		t.Errorf("STREAM GAP: code.done not emitted/decoded (got %q)", codeDone)
	}
	if !sawInterpreting {
		t.Error("STREAM GAP: code_interpreter_call.interpreting not emitted")
	}
	if !sawCompleted {
		t.Error("STREAM GAP: code_interpreter_call.completed not emitted")
	}
	if !sawItemDone {
		t.Error("STREAM GAP: code_interpreter_call output_item.done not emitted")
	}

	// The terminal response.completed must carry the code_interpreter_call with
	// the sandbox container folded in from the final message_delta.
	var containerID string
	for _, e := range emitted {
		if e.Type != schemas.ResponsesStreamResponseTypeCompleted || e.Response == nil {
			continue
		}
		for _, out := range e.Response.Output {
			if out.Type != nil && *out.Type == schemas.ResponsesMessageTypeCodeInterpreterCall &&
				out.ResponsesToolMessage != nil && out.ResponsesToolMessage.ResponsesCodeInterpreterToolCall != nil {
				containerID = out.ResponsesToolMessage.ResponsesCodeInterpreterToolCall.ContainerID
			}
		}
	}
	if containerID != "container_stream1" {
		t.Errorf("response.completed container_id = %q, want container_stream1", containerID)
	}

	// Reverse direction: the emitted Bifrost stream events must reconstruct the
	// Anthropic server_tool_use + bash_code_execution_tool_result blocks.
	var back []*AnthropicStreamEvent
	for _, r := range emitted {
		back = append(back, ToAnthropicResponsesStreamResponse(ctx, r)...)
	}

	var sawServerToolUse, sawResult bool
	var serverToolName, resultStdout, reconstructedCmd string
	for _, e := range back {
		if e.Type != AnthropicStreamEventTypeContentBlockStart || e.ContentBlock == nil {
			continue
		}
		switch e.ContentBlock.Type {
		case AnthropicContentBlockTypeServerToolUse:
			if e.ContentBlock.Name != nil && isAnthropicCodeExecutionToolName(*e.ContentBlock.Name) {
				sawServerToolUse = true
				serverToolName = *e.ContentBlock.Name
			}
		case AnthropicContentBlockTypeBashCodeExecutionToolResult:
			sawResult = true
			if e.ContentBlock.Content != nil && e.ContentBlock.Content.ContentObj != nil &&
				e.ContentBlock.Content.ContentObj.Stdout != nil {
				resultStdout = *e.ContentBlock.Content.ContentObj.Stdout
			}
		}
	}
	// The verbatim command is reconstructed via synthetic input_json_delta events.
	for _, e := range back {
		if e.Type == AnthropicStreamEventTypeContentBlockDelta && e.Delta != nil &&
			e.Delta.Type == AnthropicStreamDeltaTypeInputJSON && e.Delta.PartialJSON != nil {
			reconstructedCmd += *e.Delta.PartialJSON
		}
	}

	if !sawServerToolUse || serverToolName != "bash_code_execution" {
		t.Errorf("REVERSE GAP: server_tool_use not reconstructed (name=%q)", serverToolName)
	}
	if !sawResult || !strings.Contains(resultStdout, "Mean: 5.5") {
		t.Errorf("REVERSE GAP: bash_code_execution_tool_result not reconstructed (stdout=%q)", resultStdout)
	}
	if !strings.Contains(reconstructedCmd, "python3") {
		t.Errorf("REVERSE GAP: server_tool_use input not reconstructed (got %q)", reconstructedCmd)
	}

	// The sandbox container must be re-emitted on message_delta (Anthropic
	// delivers it there natively).
	var deltaContainerID string
	for _, e := range back {
		if e.Type == AnthropicStreamEventTypeMessageDelta && e.Delta != nil && e.Delta.Container != nil {
			deltaContainerID = e.Delta.Container.ID
		}
	}
	if deltaContainerID != "container_stream1" {
		t.Errorf("REVERSE GAP: container not re-emitted on message_delta (got %q)", deltaContainerID)
	}
}

// textEditorCodeExecStreamEvents is the Anthropic SSE for a
// text_editor_code_execution "view" operation. Unlike python/bash, its input is
// multi-key ({"command","path"}) and cannot be reconstructed from the neutral
// Code field — so the reverse converter must emit it from the verbatim carry
// input at output_item.done, not from code.done.
var textEditorCodeExecStreamEvents = []string{
	`{"type":"message_start","message":{"model":"claude-opus-4-8","id":"msg_te1","type":"message","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_te1","name":"text_editor_code_execution","input":{}}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\": \"view\", \"pa"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"th\": \"/tmp/data.py\"}"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"text_editor_code_execution_tool_result","tool_use_id":"srvtoolu_te1","content":{"type":"text_editor_code_execution_result","file_type":"text","content":"print('hi')\n","num_lines":1,"start_line":1,"total_lines":1}}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn","container":{"id":"container_te1","expires_at":"2026-06-25T13:57:10Z"}},"usage":{"output_tokens":40}}`,
	`{"type":"message_stop"}`,
}

// TestCodeExecution_TextEditorStream guards the regression where the reverse
// streaming converter dropped the text_editor server_tool_use input (it can't be
// rebuilt from the neutral Code, which is empty for text_editor). The verbatim
// multi-key input must round-trip Anthropic -> Bifrost -> Anthropic.
func TestCodeExecution_TextEditorStream(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
	state := acquireAnthropicResponsesStreamState()
	defer releaseAnthropicResponsesStreamState(state)

	var emitted []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for _, raw := range textEditorCodeExecStreamEvents {
		var chunk AnthropicStreamEvent
		if err := sonic.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		responses, bErr, _ := chunk.ToBifrostResponsesStream(ctx, seq, state)
		if bErr != nil {
			t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
		}
		if state.HasEmittedMessageDelta {
			ctx.SetValue(schemas.BifrostContextKeyHasEmittedMessageDelta, true)
		}
		for _, r := range responses {
			seq++
			emitted = append(emitted, r)
		}
	}

	// Reverse: reconstruct the Anthropic stream.
	var back []*AnthropicStreamEvent
	for _, r := range emitted {
		back = append(back, ToAnthropicResponsesStreamResponse(ctx, r)...)
	}

	// The server_tool_use must be reconstructed with its full multi-key input —
	// the regression emitted an empty {} here, losing command/path. Track the
	// server block index and only accumulate its input_json_delta.
	var sawServerToolUse, sawResult bool
	serverIdx := -1
	reconstructedInput := ""
	for _, e := range back {
		switch {
		case e.Type == AnthropicStreamEventTypeContentBlockStart && e.ContentBlock != nil:
			if e.ContentBlock.Type == AnthropicContentBlockTypeServerToolUse &&
				e.ContentBlock.Name != nil && *e.ContentBlock.Name == "text_editor_code_execution" {
				sawServerToolUse = true
				if e.Index != nil {
					serverIdx = *e.Index
				}
			}
			if e.ContentBlock.Type == AnthropicContentBlockTypeTextEditorCodeExecutionToolResult {
				sawResult = true
			}
		case e.Type == AnthropicStreamEventTypeContentBlockDelta && e.Delta != nil &&
			e.Delta.Type == AnthropicStreamDeltaTypeInputJSON && e.Delta.PartialJSON != nil &&
			e.Index != nil && *e.Index == serverIdx:
			reconstructedInput += *e.Delta.PartialJSON
		}
	}

	if !sawServerToolUse {
		t.Fatal("REVERSE GAP: text_editor_code_execution server_tool_use not reconstructed")
	}
	if !sawResult {
		t.Error("REVERSE GAP: text_editor_code_execution_tool_result not reconstructed")
	}
	// The full verbatim input must survive — both keys, not an empty object.
	for _, want := range []string{`"command"`, `"view"`, `"path"`, `/tmp/data.py`} {
		if !strings.Contains(reconstructedInput, want) {
			t.Errorf("REVERSE GAP: text_editor input lost %q (got %q)", want, reconstructedInput)
		}
	}
	if reconstructedInput == "" || reconstructedInput == "{}" {
		t.Errorf("REVERSE GAP: text_editor server_tool_use input is empty (got %q)", reconstructedInput)
	}
}

// rawPTCResponse is a programmatic-tool-calling response: a web_search spawned
// from inside the code_execution sandbox. Anthropic tags the nested web_search
// server_tool_use AND its result with a "caller" pointing back at the code block.
var rawPTCResponse = `{
  "model": "claude-opus-4-8",
  "id": "msg_ptc1",
  "type": "message",
  "role": "assistant",
  "content": [
    {
      "type": "server_tool_use",
      "id": "srvtoolu_code1",
      "name": "code_execution",
      "input": { "code": "r = await web_search({\"query\": \"AAPL\"})" }
    },
    {
      "type": "server_tool_use",
      "id": "srvtoolu_ws1",
      "name": "web_search",
      "input": { "query": "AAPL stock price" },
      "caller": { "type": "code_execution_20260120", "tool_id": "srvtoolu_code1" }
    },
    {
      "type": "web_search_tool_result",
      "tool_use_id": "srvtoolu_ws1",
      "content": [
        { "type": "web_search_result", "title": "Apple", "url": "https://example.com/aapl", "encrypted_content": "enc1" }
      ],
      "caller": { "type": "code_execution_20260120", "tool_id": "srvtoolu_code1" }
    },
    {
      "type": "code_execution_tool_result",
      "tool_use_id": "srvtoolu_code1",
      "content": { "type": "code_execution_result", "stdout": "done\n", "stderr": "", "return_code": 0, "content": [] }
    }
  ],
  "container": { "id": "container_ptc1", "expires_at": "2026-06-26T08:00:00Z" },
  "stop_reason": "end_turn",
  "usage": { "input_tokens": 100, "output_tokens": 50 }
}`

// TestCodeExecution_ProgrammaticCallerRoundTrip verifies the "caller" linkage on
// a web_search spawned inside the code execution sandbox survives the
// Anthropic -> Bifrost -> Anthropic round trip on the web_search server_tool_use
// and its result block.
func TestCodeExecution_ProgrammaticCallerRoundTrip(t *testing.T) {
	var resp AnthropicMessageResponse
	if err := sonic.Unmarshal([]byte(rawPTCResponse), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ctx := schemas.NewBifrostContext(nil, time.Time{})

	bif := resp.ToBifrostResponsesResponse(ctx)

	// Neutral view: the web_search_call must carry the caller.
	var sawNeutralCaller bool
	for _, out := range bif.Output {
		if out.Type != nil && *out.Type == schemas.ResponsesMessageTypeWebSearchCall &&
			out.ResponsesToolMessage != nil && out.ResponsesToolMessage.Caller != nil {
			sawNeutralCaller = true
			if out.ResponsesToolMessage.Caller.ToolID == nil || *out.ResponsesToolMessage.Caller.ToolID != "srvtoolu_code1" {
				t.Errorf("neutral caller tool_id = %v, want srvtoolu_code1", out.ResponsesToolMessage.Caller.ToolID)
			}
			if out.ResponsesToolMessage.Caller.Type != "code_execution_20260120" {
				t.Errorf("neutral caller type = %q", out.ResponsesToolMessage.Caller.Type)
			}
		}
	}
	if !sawNeutralCaller {
		t.Fatal("web_search_call lost its caller in the neutral view")
	}

	// Reverse: caller must reappear on the web_search server_tool_use + result.
	back := ToAnthropicResponsesResponse(ctx, bif)
	out, err := sonic.Marshal(back)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var wsCallerToolID, wsResultCallerToolID string
	gjson.GetBytes(out, "content").ForEach(func(_, b gjson.Result) bool {
		switch b.Get("type").String() {
		case "server_tool_use":
			if b.Get("name").String() == "web_search" {
				wsCallerToolID = b.Get("caller.tool_id").String()
			}
		case "web_search_tool_result":
			wsResultCallerToolID = b.Get("caller.tool_id").String()
		}
		return true
	})

	if wsCallerToolID != "srvtoolu_code1" {
		t.Errorf("web_search server_tool_use caller.tool_id = %q, want srvtoolu_code1\n%s", wsCallerToolID, out)
	}
	if wsResultCallerToolID != "srvtoolu_code1" {
		t.Errorf("web_search_tool_result caller.tool_id = %q, want srvtoolu_code1", wsResultCallerToolID)
	}
}

// ptcStreamEvents is a programmatic-tool-calling stream: a code_execution that
// spawns a web_search from inside the sandbox. The web_search server_tool_use and
// its result carry a "caller" pointing back at the code block.
var ptcStreamEvents = []string{
	`{"type":"message_start","message":{"id":"msg_ptc","type":"message","role":"assistant","content":[],"model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I'll compute."}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srv_code","name":"code_execution","input":{}}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"code\": \"r = await web_search({\\\"query\\\": \\\"AAPL\\\"})\"}"}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"server_tool_use","id":"srv_ws","name":"web_search","input":{"query":"AAPL stock price"},"caller":{"type":"code_execution_20260120","tool_id":"srv_code"}}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"content_block_start","index":3,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv_ws","content":[{"type":"web_search_result","title":"Apple","url":"https://example.com/a","encrypted_content":"enc1"}],"caller":{"type":"code_execution_20260120","tool_id":"srv_code"}}}`,
	`{"type":"content_block_stop","index":3}`,
	`{"type":"content_block_start","index":4,"content_block":{"type":"code_execution_tool_result","tool_use_id":"srv_code","content":{"type":"code_execution_result","stdout":"done\n","stderr":"","return_code":0,"content":[]}}}`,
	`{"type":"content_block_stop","index":4}`,
	`{"type":"content_block_start","index":5,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":5,"delta":{"type":"text_delta","text":"AAPL looks pricey."}}`,
	`{"type":"content_block_stop","index":5}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn","container":{"id":"cntr_ptc","expires_at":"2026-06-26T00:00:00Z"}},"usage":{"output_tokens":50}}`,
	`{"type":"message_stop"}`,
}

// TestCodeExecution_ProgrammaticStreamRoundTrip drives the native PTC SSE through
// the forward (Anthropic→Bifrost) then reverse (Bifrost→Anthropic) stream
// converters and asserts the reverse stream is well-formed: valid content-block
// index bookkeeping (no stop-before-start, no reuse), the code_execution input is
// streamed, and the web_search carries its query + caller.
func TestCodeExecution_ToolVersionRoundTrip(t *testing.T) {
	// Every code_execution version must survive request->neutral->upstream
	// verbatim — the capability tier (bash/file vs. PTC+REPL vs. disclosed time
	// limit) is not interchangeable, so the gateway must forward, not substitute.
	versions := []AnthropicToolType{
		AnthropicToolTypeCodeExecution20250522,
		AnthropicToolTypeCodeExecution, // 20250825
		AnthropicToolTypeCodeExecution20260120,
		AnthropicToolTypeCodeExecution20260521,
	}
	for _, v := range versions {
		in := &AnthropicTool{Type: schemas.Ptr(v), Name: string(AnthropicToolNameCodeExecution)}

		neutral := convertAnthropicToolToBifrost(in)
		if neutral.Type != schemas.ResponsesToolTypeCodeInterpreter {
			t.Fatalf("%s: expected code_interpreter, got %s (must not fall through to function tool)", v, neutral.Type)
		}
		if neutral.ResponsesToolCodeInterpreter == nil || neutral.ResponsesToolCodeInterpreter.Version == nil {
			t.Fatalf("%s: version not captured on neutral tool", v)
		}
		if got := *neutral.ResponsesToolCodeInterpreter.Version; got != string(v) {
			t.Errorf("%s: neutral version = %q, want %q", v, got, string(v))
		}

		back := convertBifrostToolToAnthropic("claude-opus-4-8", neutral, schemas.Anthropic, false)
		if back == nil || back.Type == nil {
			t.Fatalf("%s: reverse produced no tool", v)
		}
		if *back.Type != v {
			t.Errorf("%s: round-trip version = %q, want %q", v, *back.Type, v)
		}
	}

	// OpenAI-origin code_interpreter (no version) falls back to 20250825 — the
	// only version supported on every model.
	noVer := &schemas.ResponsesTool{
		Type:                         schemas.ResponsesToolTypeCodeInterpreter,
		ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{},
	}
	back := convertBifrostToolToAnthropic("claude-opus-4-8", noVer, schemas.Anthropic, false)
	if back == nil || back.Type == nil || *back.Type != AnthropicToolTypeCodeExecution {
		t.Errorf("absent-version fallback = %v, want %s", back, AnthropicToolTypeCodeExecution)
	}
}

func TestGenerateSyntheticInputJSONDeltas_UTF8(t *testing.T) {
	// Code with multi-byte runes (em-dash, smart quotes, accent, emoji) must not
	// be split mid-rune across input_json_delta events — that would emit invalid
	// UTF-8 on the wire and crash the SDK's SSE decoder.
	input := `{"code":"print('an em—dash, “smart” quotes, café, and 🚀')"}`
	idx := 0
	events := generateSyntheticInputJSONDeltas(input, &idx)

	var sb strings.Builder
	for _, e := range events {
		if e.Delta == nil || e.Delta.PartialJSON == nil {
			t.Fatal("input_json_delta missing partial_json")
		}
		if !utf8.ValidString(*e.Delta.PartialJSON) {
			t.Errorf("chunk is not valid UTF-8: %q", *e.Delta.PartialJSON)
		}
		sb.WriteString(*e.Delta.PartialJSON)
	}
	if got := sb.String(); got != input {
		t.Errorf("reassembled chunks != original\n got: %q\nwant: %q", got, input)
	}
}

func TestCodeExecution_ProgrammaticStreamRoundTrip(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
	state := acquireAnthropicResponsesStreamState()
	defer releaseAnthropicResponsesStreamState(state)

	var back []*AnthropicStreamEvent
	seq := 0
	for _, raw := range ptcStreamEvents {
		var chunk AnthropicStreamEvent
		if err := sonic.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		responses, bErr, _ := chunk.ToBifrostResponsesStream(ctx, seq, state)
		if bErr != nil {
			t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
		}
		if state.HasEmittedMessageDelta {
			ctx.SetValue(schemas.BifrostContextKeyHasEmittedMessageDelta, true)
		}
		for _, r := range responses {
			seq++
			back = append(back, ToAnthropicResponsesStreamResponse(ctx, r)...)
		}
	}

	// 1. Index bookkeeping: every stop closes a currently-open start, indices are
	// never reused, and nothing is left open.
	open := map[int]bool{}
	started := map[int]bool{}
	// per-index accumulated input_json and captured metadata.
	inputByIndex := map[int]string{}
	nameByIndex := map[int]string{}
	callerByIndex := map[int]string{}
	var wsResultCaller string
	// Ordering: the code server_tool_use block must close before the nested
	// web_search blocks begin (sequential, not interleaved — matches native).
	pos, codeStopPos, firstWsStartPos := 0, -1, -1
	for _, e := range back {
		switch e.Type {
		case AnthropicStreamEventTypeContentBlockStart:
			if e.Index == nil {
				t.Fatal("content_block_start with nil index")
			}
			idx := *e.Index
			if started[idx] {
				t.Errorf("content_block index %d reused (started twice)", idx)
			}
			started[idx] = true
			open[idx] = true
			if e.ContentBlock != nil {
				if e.ContentBlock.Name != nil {
					nameByIndex[idx] = *e.ContentBlock.Name
					if *e.ContentBlock.Name == "web_search" && firstWsStartPos < 0 {
						firstWsStartPos = pos
					}
				}
				// web_search delivers its query whole in content_block_start.input
				// (no input_json_delta), so seed it here.
				if len(e.ContentBlock.Input) > 0 && string(e.ContentBlock.Input) != "{}" {
					inputByIndex[idx] += string(e.ContentBlock.Input)
				}
				if e.ContentBlock.Caller != nil && e.ContentBlock.Caller.ToolID != nil {
					if e.ContentBlock.Type == AnthropicContentBlockTypeWebSearchToolResult {
						wsResultCaller = *e.ContentBlock.Caller.ToolID
					} else {
						callerByIndex[idx] = *e.ContentBlock.Caller.ToolID
					}
				}
			}
		case AnthropicStreamEventTypeContentBlockDelta:
			if e.Index != nil && e.Delta != nil && e.Delta.Type == AnthropicStreamDeltaTypeInputJSON && e.Delta.PartialJSON != nil {
				inputByIndex[*e.Index] += *e.Delta.PartialJSON
			}
		case AnthropicStreamEventTypeContentBlockStop:
			if e.Index == nil {
				t.Fatal("content_block_stop with nil index")
			}
			if !open[*e.Index] {
				t.Errorf("content_block_stop for index %d that is not open (stop-before-start or double-stop)", *e.Index)
			}
			if isAnthropicCodeExecutionToolName(nameByIndex[*e.Index]) && codeStopPos < 0 {
				codeStopPos = pos
			}
			delete(open, *e.Index)
		}
		pos++
	}
	if len(open) != 0 {
		t.Errorf("content blocks left open: %v", open)
	}
	if codeStopPos < 0 || firstWsStartPos < 0 || codeStopPos > firstWsStartPos {
		t.Errorf("code block not closed before nested web_search (codeStop=%d, firstWsStart=%d)", codeStopPos, firstWsStartPos)
	}

	// Regression: the code_execution_tool_result inner result object must carry a
	// content array (native always emits it; the Anthropic SDK's *ResultBlock
	// models require it, else they mis-parse the block).
	sawCodeResult := false
	for _, e := range back {
		if e.Type != AnthropicStreamEventTypeContentBlockStart || e.ContentBlock == nil ||
			e.ContentBlock.Type != AnthropicContentBlockTypeCodeExecutionToolResult {
			continue
		}
		sawCodeResult = true
		j, err := sonic.Marshal(e.ContentBlock)
		if err != nil {
			t.Fatalf("marshal code_execution_tool_result: %v", err)
		}
		if !gjson.GetBytes(j, "content.content").IsArray() {
			t.Errorf("code_execution_tool_result inner result missing content array: %s", j)
		}
	}
	if !sawCodeResult {
		t.Fatal("no code_execution_tool_result block found")
	}

	// 2/3/4. Find the code_execution and web_search server_tool_use blocks and check
	// their streamed input + caller.
	var codeIdx, wsIdx = -1, -1
	for idx, name := range nameByIndex {
		switch {
		case isAnthropicCodeExecutionToolName(name):
			codeIdx = idx
		case name == "web_search":
			wsIdx = idx
		}
	}
	if codeIdx < 0 {
		t.Fatal("no code_execution server_tool_use block emitted")
	}
	if wsIdx < 0 {
		t.Fatal("no web_search server_tool_use block emitted")
	}
	if !strings.Contains(inputByIndex[codeIdx], "web_search") {
		t.Errorf("code_execution input not streamed (got %q)", inputByIndex[codeIdx])
	}
	if !strings.Contains(inputByIndex[wsIdx], "AAPL stock price") {
		t.Errorf("web_search query not streamed (got %q)", inputByIndex[wsIdx])
	}
	if callerByIndex[wsIdx] != "srv_code" {
		t.Errorf("web_search server_tool_use caller = %q, want srv_code", callerByIndex[wsIdx])
	}
	if wsResultCaller != "srv_code" {
		t.Errorf("web_search_tool_result caller = %q, want srv_code", wsResultCaller)
	}
}
