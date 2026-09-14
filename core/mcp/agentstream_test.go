package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// newStreamRelayToolsManager builds the minimal ToolsManager the stream relay needs.
func newStreamRelayToolsManager(clientManager ClientManager, maxDepth int32) *ToolsManager {
	manager := &ToolsManager{
		clientManager:     clientManager,
		agentModeExecutor: &AgentModeExecutor{logger: &MockLogger{}},
	}
	manager.maxAgentDepth.Store(maxDepth)
	return manager
}

func streamChatChunk(choice schemas.BifrostResponseChoice) *schemas.BifrostStreamChunk {
	return &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{choice},
		},
	}
}

func chatRoleChunk() *schemas.BifrostStreamChunk {
	return streamChatChunk(schemas.BifrostResponseChoice{
		ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
			Delta: &schemas.ChatStreamResponseChoiceDelta{Role: schemas.Ptr("assistant")},
		},
	})
}

func chatTextChunk(text string) *schemas.BifrostStreamChunk {
	return streamChatChunk(schemas.BifrostResponseChoice{
		ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
			Delta: &schemas.ChatStreamResponseChoiceDelta{Content: schemas.Ptr(text)},
		},
	})
}

func chatToolCallChunk(index uint16, id, name, arguments string) *schemas.BifrostStreamChunk {
	call := schemas.ChatAssistantMessageToolCall{
		Index:    index,
		Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: arguments},
	}
	if id != "" {
		call.ID = schemas.Ptr(id)
	}
	if name != "" {
		call.Function.Name = schemas.Ptr(name)
	}
	return streamChatChunk(schemas.BifrostResponseChoice{
		ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
			Delta: &schemas.ChatStreamResponseChoiceDelta{
				ToolCalls: []schemas.ChatAssistantMessageToolCall{call},
			},
		},
	})
}

func chatFinishChunk(promptTokens, completionTokens int) *schemas.BifrostStreamChunk {
	chunk := streamChatChunk(schemas.BifrostResponseChoice{
		FinishReason:             schemas.Ptr("stop"),
		ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{}},
	})
	chunk.BifrostChatResponse.Usage = &schemas.BifrostLLMUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}
	return chunk
}

func streamOf(chunks ...*schemas.BifrostStreamChunk) chan *schemas.BifrostStreamChunk {
	ch := make(chan *schemas.BifrostStreamChunk, len(chunks))
	for _, chunk := range chunks {
		ch <- chunk
	}
	close(ch)
	return ch
}

func collectStream(t *testing.T, ch chan *schemas.BifrostStreamChunk) []*schemas.BifrostStreamChunk {
	t.Helper()
	var collected []*schemas.BifrostStreamChunk
	timeout := time.After(5 * time.Second)
	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				return collected
			}
			collected = append(collected, chunk)
		case <-timeout:
			t.Fatal("timed out waiting for the relayed stream to close")
		}
	}
}

// streamedText concatenates the content deltas a client would have rendered.
func streamedText(chunks []*schemas.BifrostStreamChunk) string {
	text := ""
	for _, chunk := range chunks {
		if chunk == nil || chunk.BifrostChatResponse == nil {
			continue
		}
		for _, choice := range chunk.BifrostChatResponse.Choices {
			if choice.ChatStreamResponseChoice == nil || choice.ChatStreamResponseChoice.Delta == nil {
				continue
			}
			if content := choice.ChatStreamResponseChoice.Delta.Content; content != nil {
				text += *content
			}
		}
	}
	return text
}

func countToolCallChunks(chunks []*schemas.BifrostStreamChunk) int {
	count := 0
	for _, chunk := range chunks {
		if chatChunkHasToolCall(chunk) {
			count++
		}
	}
	return count
}

func streamRelayRequest(toolNames ...string) *schemas.BifrostChatRequest {
	tools := make([]schemas.ChatTool, 0, len(toolNames))
	for _, name := range toolNames {
		tools = append(tools, schemas.ChatTool{
			Type:     schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{Name: name},
		})
	}
	return &schemas.BifrostChatRequest{
		Provider: "openai",
		Model:    "gpt-test",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser}},
		Params:   &schemas.ChatParameters{Tools: tools},
	}
}

func okToolExecutor(result string) MCPToolExecutor {
	return func(_ *schemas.BifrostContext, _ *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		return &schemas.BifrostMCPResponse{
			ChatMessage: &schemas.ChatMessage{
				Role:    schemas.ChatMessageRoleTool,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(result)},
			},
		}, nil
	}
}

func noStreamStarter(t *testing.T) ChatStreamStarter {
	t.Helper()
	return func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		t.Fatal("no follow-up iteration was expected")
		return nil, nil
	}
}

// TestExecuteAgentForChatStreamPassthrough pins the fast path: a stream with nothing Bifrost
// would execute is handed back untouched, so ordinary streaming pays no relay cost.
func TestExecuteAgentForChatStreamPassthrough(t *testing.T) {
	manager := newStreamRelayToolsManager(&MockClientManager{}, 5)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream := streamOf(chatTextChunk("hello"))

	got := manager.ExecuteAgentForChatStream(ctx, streamRelayRequest("client_side"), stream, noStreamStarter(t), okToolExecutor("unused"))
	if got != stream {
		t.Fatal("expected the provider's own channel to be returned unwrapped")
	}
}

// TestChatStreamRelayForwardsIterationWithoutToolCalls pins that a plain answer streams through
// the relay unchanged.
func TestChatStreamRelayForwardsIterationWithoutToolCalls(t *testing.T) {
	manager := newStreamRelayToolsManager(&MockAutoClientManager{}, 5)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream := streamOf(chatRoleChunk(), chatTextChunk("hello "), chatTextChunk("world"), chatFinishChunk(5, 3))

	out := manager.ExecuteAgentForChatStream(ctx, streamRelayRequest("weather"), stream, noStreamStarter(t), okToolExecutor("unused"))
	chunks := collectStream(t, out)

	if text := streamedText(chunks); text != "hello world" {
		t.Fatalf("expected the answer to stream through intact, got %q", text)
	}
	if len(chunks) != 4 {
		t.Fatalf("expected all 4 chunks forwarded, got %d", len(chunks))
	}
}

// TestChatStreamRelayExecutesAndContinues is the core case: the tool call never reaches the
// client, the tool runs, and the follow-up answer continues in the same stream.
func TestChatStreamRelayExecutesAndContinues(t *testing.T) {
	manager := newStreamRelayToolsManager(&MockAutoClientManager{}, 5)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	iteration1 := streamOf(
		chatRoleChunk(),
		chatTextChunk("let me check"),
		chatToolCallChunk(0, "call-1", "weather", `{"ci`),
		chatToolCallChunk(0, "", "", `ty":"Pune"}`),
		chatFinishChunk(10, 4),
	)

	var followUp *schemas.BifrostChatRequest
	startStream := func(_ *schemas.BifrostContext, req *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		followUp = req
		return streamOf(chatRoleChunk(), chatTextChunk(" 28C and clear"), chatFinishChunk(20, 6)), nil
	}

	var executedArgs string
	executor := func(_ *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		executedArgs = req.ChatAssistantMessageToolCall.Function.Arguments
		return okToolExecutor("28C, clear")(nil, req)
	}

	out := manager.ExecuteAgentForChatStream(ctx, streamRelayRequest("weather"), iteration1, startStream, executor)
	chunks := collectStream(t, out)

	if executedArgs != `{"city":"Pune"}` {
		t.Fatalf("expected argument fragments to be reassembled, got %q", executedArgs)
	}
	if text := streamedText(chunks); text != "let me check 28C and clear" {
		t.Fatalf("expected both iterations to stream as one answer, got %q", text)
	}
	if n := countToolCallChunks(chunks); n != 0 {
		t.Fatalf("expected the tool call to stay hidden from the client, saw %d chunk(s)", n)
	}
	if followUp == nil {
		t.Fatal("expected a follow-up iteration")
	}
	if len(followUp.Input) != 3 {
		t.Fatalf("expected the follow-up to carry user, assistant and tool messages, got %d", len(followUp.Input))
	}
	assistant := followUp.Input[1]
	if assistant.ChatAssistantMessage == nil || len(assistant.ChatAssistantMessage.ToolCalls) != 1 {
		t.Fatal("expected the assistant turn to replay the executed tool call")
	}
	if followUp.Input[2].Role != schemas.ChatMessageRoleTool {
		t.Fatalf("expected a tool result message, got role %q", followUp.Input[2].Role)
	}
}

// TestChatStreamRelayReleasesHoldForClientTools pins the mixed-turn rule: if any call belongs to
// the client, the whole turn is handed back exactly as the blocking loop hands it back.
func TestChatStreamRelayReleasesHoldForClientTools(t *testing.T) {
	tests := []struct {
		name      string
		autoTools map[string]bool
		requested []string
	}{
		{name: "client owned only", autoTools: map[string]bool{"weather": true}, requested: []string{"weather", "client_side"}},
		{name: "mixed batch", autoTools: map[string]bool{"weather": true}, requested: []string{"weather"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := newStreamRelayToolsManager(&mockPartitionClientManager{
				MockAutoClientManager: &MockAutoClientManager{},
				autoTools:             tt.autoTools,
			}, 5)
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

			chunks := []*schemas.BifrostStreamChunk{chatRoleChunk(), chatTextChunk("thinking")}
			if tt.name == "mixed batch" {
				chunks = append(chunks, chatToolCallChunk(0, "call-1", "weather", "{}"))
			}
			chunks = append(chunks, chatToolCallChunk(1, "call-2", "client_side", "{}"), chatFinishChunk(4, 2))

			out := manager.ExecuteAgentForChatStream(ctx, streamRelayRequest(tt.requested...), streamOf(chunks...), noStreamStarter(t), okToolExecutor("unused"))
			relayed := collectStream(t, out)

			if n := countToolCallChunks(relayed); n == 0 {
				t.Fatal("expected the held tool calls to be released to the client")
			}
			if len(relayed) != len(chunks) {
				t.Fatalf("expected every chunk released, got %d of %d", len(relayed), len(chunks))
			}
		})
	}
}

// TestChatStreamRelayStopsAtDepthLimit pins that an exhausted loop hands the turn back rather
// than running another tool.
func TestChatStreamRelayStopsAtDepthLimit(t *testing.T) {
	manager := newStreamRelayToolsManager(&MockAutoClientManager{}, 1)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	iterations := 0
	startStream := func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		iterations++
		return streamOf(chatToolCallChunk(0, "call-2", "weather", "{}"), chatFinishChunk(1, 1)), nil
	}

	iteration1 := streamOf(chatToolCallChunk(0, "call-1", "weather", "{}"), chatFinishChunk(1, 1))
	out := manager.ExecuteAgentForChatStream(ctx, streamRelayRequest("weather"), iteration1, startStream, okToolExecutor("done"))
	relayed := collectStream(t, out)

	if iterations != 1 {
		t.Fatalf("expected exactly one follow-up iteration at depth 1, got %d", iterations)
	}
	if n := countToolCallChunks(relayed); n == 0 {
		t.Fatal("expected the final iteration's tool call to be released once the depth limit is reached")
	}
}

// TestChatStreamRelaySkipsGatedStream pins the guardrail valve: when a plugin is holding the
// stream behind the pause gate, no tool runs and the turn is handed back untouched.
func TestChatStreamRelaySkipsGatedStream(t *testing.T) {
	manager := newStreamRelayToolsManager(&MockAutoClientManager{}, 5)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyStreamGated, true)

	executed := false
	executor := func(_ *schemas.BifrostContext, _ *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		executed = true
		return nil, nil
	}

	stream := streamOf(chatToolCallChunk(0, "call-1", "weather", "{}"), chatFinishChunk(2, 1))
	out := manager.ExecuteAgentForChatStream(ctx, streamRelayRequest("weather"), stream, noStreamStarter(t), executor)
	relayed := collectStream(t, out)

	if executed {
		t.Fatal("a stream held by a plugin must not have its tools executed")
	}
	if n := countToolCallChunks(relayed); n == 0 {
		t.Fatal("expected the held chunks to be released untouched")
	}
}

// TestChatStreamRelaySurfacesToolAuthError pins that a per-user auth failure ends the stream with
// an in-band error rather than silently continuing.
func TestChatStreamRelaySurfacesToolAuthError(t *testing.T) {
	manager := newStreamRelayToolsManager(&MockAutoClientManager{}, 5)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	executor := func(_ *schemas.BifrostContext, _ *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		return nil, &schemas.MCPAuthRequiredError{Kind: "oauth", MCPClientName: "test-client", Message: "reauthorize"}
	}

	stream := streamOf(chatToolCallChunk(0, "call-1", "weather", "{}"), chatFinishChunk(2, 1))
	out := manager.ExecuteAgentForChatStream(ctx, streamRelayRequest("weather"), stream, noStreamStarter(t), executor)
	relayed := collectStream(t, out)

	if len(relayed) == 0 {
		t.Fatal("expected an error chunk")
	}
	last := relayed[len(relayed)-1]
	if last.BifrostError == nil {
		t.Fatal("expected the auth failure to arrive as an in-band error chunk")
	}
	if last.BifrostError.ExtraFields.MCPAuthRequired == nil {
		t.Fatal("expected the auth details to survive onto the stream")
	}
}

// TestChatStreamRelayMergesUsageAcrossIterations pins that a dropped iteration's usage is not lost, so
// the client's totals still cover the whole turn.
func TestChatStreamRelayMergesUsageAcrossIterations(t *testing.T) {
	manager := newStreamRelayToolsManager(&MockAutoClientManager{}, 5)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	startStream := func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return streamOf(chatTextChunk("done"), chatFinishChunk(20, 6)), nil
	}
	iteration1 := streamOf(chatToolCallChunk(0, "call-1", "weather", "{}"), chatFinishChunk(10, 4))

	out := manager.ExecuteAgentForChatStream(ctx, streamRelayRequest("weather"), iteration1, startStream, okToolExecutor("28C"))
	relayed := collectStream(t, out)

	var usage *schemas.BifrostLLMUsage
	for _, chunk := range relayed {
		if chatChunkHasUsage(chunk) {
			usage = chunk.BifrostChatResponse.Usage
		}
	}
	if usage == nil {
		t.Fatal("expected the final chunk to report usage")
	}
	if usage.PromptTokens != 30 || usage.CompletionTokens != 10 || usage.TotalTokens != 40 {
		t.Fatalf("expected both iterations counted, got prompt=%d completion=%d total=%d",
			usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
	}
}

// TestChatStreamRelaySurfacesFollowUpFailure pins that a failed follow-up iteration ends the stream
// with the provider's error instead of closing silently.
func TestChatStreamRelaySurfacesFollowUpFailure(t *testing.T) {
	manager := newStreamRelayToolsManager(&MockAutoClientManager{}, 5)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	startStream := func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "provider unavailable"}}
	}
	stream := streamOf(chatToolCallChunk(0, "call-1", "weather", "{}"), chatFinishChunk(2, 1))

	out := manager.ExecuteAgentForChatStream(ctx, streamRelayRequest("weather"), stream, startStream, okToolExecutor("28C"))
	relayed := collectStream(t, out)

	if len(relayed) == 0 || relayed[len(relayed)-1].BifrostError == nil {
		t.Fatal("expected the follow-up failure to arrive as an in-band error chunk")
	}
}

// TestReassembleChatToolCalls pins the delta merge: chat streams split one call across chunks,
// carrying the id and name first and the arguments in fragments tied together by index.
func TestReassembleChatToolCalls(t *testing.T) {
	held := []*schemas.BifrostStreamChunk{
		chatToolCallChunk(0, "call-a", "alpha", `{"x`),
		chatToolCallChunk(1, "call-b", "beta", `{"y`),
		chatToolCallChunk(0, "", "", `":1}`),
		chatToolCallChunk(1, "", "", `":2}`),
		chatFinishChunk(1, 1),
	}

	calls := reassembleChatToolCalls(held)
	if len(calls) != 2 {
		t.Fatalf("expected 2 reassembled calls, got %d", len(calls))
	}
	for i, want := range []struct {
		id, name, arguments string
	}{
		{"call-a", "alpha", `{"x":1}`},
		{"call-b", "beta", `{"y":2}`},
	} {
		got := calls[i]
		if got.ID == nil || *got.ID != want.id {
			t.Fatalf("call %d: expected id %q, got %v", i, want.id, got.ID)
		}
		if got.Function.Name == nil || *got.Function.Name != want.name {
			t.Fatalf("call %d: expected name %q, got %v", i, want.name, got.Function.Name)
		}
		if got.Function.Arguments != want.arguments {
			t.Fatalf("call %d: expected arguments %q, got %q", i, want.arguments, got.Function.Arguments)
		}
	}

	if calls := reassembleChatToolCalls([]*schemas.BifrostStreamChunk{chatTextChunk("no calls"), nil}); len(calls) != 0 {
		t.Fatalf("expected no calls from a plain iteration, got %d", len(calls))
	}
}
