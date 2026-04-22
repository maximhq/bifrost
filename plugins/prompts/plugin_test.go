package prompts

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	tables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================
// InitWithResolver
// ============================================================

func TestInitWithResolver_NilStore(t *testing.T) {
	_, err := InitWithResolver(context.Background(), nil, &staticResolver{}, NewMockLogger())
	require.Error(t, err, "expected error for nil store")
}

func TestInitWithResolver_NilResolverFallsBackToHeader(t *testing.T) {
	ms := &mockStore{}
	p, err := InitWithResolver(context.Background(), ms, nil, NewMockLogger())
	require.NoError(t, err)
	require.NotNil(t, p)
	_, ok := p.resolver.(*headerResolver)
	assert.True(t, ok, "expected headerResolver, got %T", p.resolver)
}

// ============================================================
// loadCache
// ============================================================

func TestLoadCache_EmptyStore(t *testing.T) {
	p := newPluginWithStore(&mockStore{})
	require.NoError(t, p.loadCache(context.Background()))
	assert.Empty(t, p.promptsByID)
	assert.Empty(t, p.versionsByPromptAndNumber)
}

func TestLoadCache_PopulatesMaps(t *testing.T) {
	v1 := makeVersion(1, "p1", true, versionMsg(schemas.ChatMessageRoleSystem, "Hello"))
	v2 := makeVersion(2, "p2", true)
	p1 := makePrompt("p1", &v1)
	p2 := makePrompt("p2", &v2)

	p := newPluginWithStore(&mockStore{
		prompts:  []tables.TablePrompt{p1, p2},
		versions: []tables.TablePromptVersion{v1, v2},
	})

	require.NoError(t, p.loadCache(context.Background()))
	assert.Len(t, p.promptsByID, 2)
	assert.Len(t, p.versionsByPromptAndNumber, 2)
	assert.NotNil(t, p.promptsByID["p1"])
	assert.NotNil(t, p.versionsByPromptAndNumber["p1"][1])
}

func TestLoadCache_GetPromptsError(t *testing.T) {
	p := newPluginWithStore(&mockStore{err: errTest("boom")})
	err := p.loadCache(context.Background())
	require.Error(t, err)
}

func TestLoadCache_GetVersionsError(t *testing.T) {
	p := newPluginWithStore(&versionsErrStore{
		prompts: []tables.TablePrompt{makePrompt("p1", nil)},
		err:     errTest("versions boom"),
	})
	err := p.loadCache(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "versions boom")
}

// ============================================================
// PreLLMHook
// ============================================================

func TestPreLLMHook_NoPromptID(t *testing.T) {
	p := newTestPlugin(&staticResolver{promptID: ""}, nil, nil)
	out, sc, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("hello")))
	require.NoError(t, err)
	assert.Nil(t, sc)
	assert.Len(t, out.ChatRequest.Input, 1)
}

func TestPreLLMHook_PromptNotFound(t *testing.T) {
	log := NewMockLogger()
	p := newTestPluginWithLogger(&staticResolver{promptID: "missing"}, nil, nil, log)

	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("hello")))
	require.NoError(t, err)
	assert.Len(t, out.ChatRequest.Input, 1, "input should be unchanged")
	assert.True(t, log.Warned(), "expected a warning for unknown prompt")
}

func TestPreLLMHook_UseLatestVersion(t *testing.T) {
	v := makeVersion(1, "p1", true,
		versionMsg(schemas.ChatMessageRoleSystem, "Be helpful"),
	)
	prompt := makePrompt("p1", &v)

	p := newTestPlugin(
		&staticResolver{promptID: "p1"},
		map[string]*tables.TablePrompt{"p1": &prompt},
		map[string]map[int]*tables.TablePromptVersion{"p1": {1: &v}},
	)

	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("hello")))
	require.NoError(t, err)
	require.Len(t, out.ChatRequest.Input, 2, "expected system prompt + user message")

	assert.Equal(t, schemas.ChatMessageRoleSystem, out.ChatRequest.Input[0].Role)
	assert.Equal(t, "Be helpful", msgText(out.ChatRequest.Input[0]))

	assert.Equal(t, schemas.ChatMessageRoleUser, out.ChatRequest.Input[1].Role)
	assert.Equal(t, "hello", msgText(out.ChatRequest.Input[1]))
}

func TestPreLLMHook_UseSpecificVersion(t *testing.T) {
	vLatest := makeVersion(1, "p1", true,
		versionMsg(schemas.ChatMessageRoleSystem, "latest system prompt"),
	)
	vOld := makeVersion(2, "p1", false,
		versionMsg(schemas.ChatMessageRoleSystem, "old system prompt"),
	)
	prompt := makePrompt("p1", &vLatest)

	p := newTestPlugin(
		&staticResolver{promptID: "p1", versionNumber: 2},
		map[string]*tables.TablePrompt{"p1": &prompt},
		map[string]map[int]*tables.TablePromptVersion{"p1": {1: &vLatest, 2: &vOld}},
	)

	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("hello")))
	require.NoError(t, err)
	require.Len(t, out.ChatRequest.Input, 2)

	// Must use vOld, not vLatest.
	assert.Equal(t, schemas.ChatMessageRoleSystem, out.ChatRequest.Input[0].Role)
	assert.Equal(t, "old system prompt", msgText(out.ChatRequest.Input[0]))
}

func TestPreLLMHook_VersionNotFound(t *testing.T) {
	v := makeVersion(1, "p1", true, versionMsg(schemas.ChatMessageRoleSystem, "hello"))
	prompt := makePrompt("p1", &v)
	log := NewMockLogger()

	p := newTestPluginWithLogger(
		&staticResolver{promptID: "p1", versionNumber: 99},
		map[string]*tables.TablePrompt{"p1": &prompt},
		map[string]map[int]*tables.TablePromptVersion{"p1": {1: &v}},
		log,
	)

	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("hi")))
	require.NoError(t, err)
	assert.Len(t, out.ChatRequest.Input, 1, "input should be unchanged")
	assert.True(t, log.Warned(), "expected warning for missing version")
}

func TestPreLLMHook_VersionBelongsToDifferentPrompt(t *testing.T) {
	v := makeVersion(1, "p2", true, versionMsg(schemas.ChatMessageRoleSystem, "wrong"))
	prompt := makePrompt("p1", nil)
	log := NewMockLogger()

	p := newTestPluginWithLogger(
		&staticResolver{promptID: "p1", versionNumber: 1},
		map[string]*tables.TablePrompt{"p1": &prompt},
		map[string]map[int]*tables.TablePromptVersion{"p2": {1: &v}},
		log,
	)

	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("hi")))
	require.NoError(t, err)
	assert.Len(t, out.ChatRequest.Input, 1, "input should be unchanged")
	assert.True(t, log.Warned(), "expected warning for version/prompt mismatch")
}

func TestPreLLMHook_NoLatestVersion(t *testing.T) {
	prompt := makePrompt("p1", nil) // LatestVersion is nil
	log := NewMockLogger()

	p := newTestPluginWithLogger(
		&staticResolver{promptID: "p1"},
		map[string]*tables.TablePrompt{"p1": &prompt},
		nil,
		log,
	)

	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("hi")))
	require.NoError(t, err)
	assert.Len(t, out.ChatRequest.Input, 1, "input should be unchanged")
	assert.True(t, log.Warned(), "expected warning for missing latest version")
}

func TestPreLLMHook_EmptyTemplate(t *testing.T) {
	v := makeVersion(1, "p1", true) // no messages
	prompt := makePrompt("p1", &v)

	p := newTestPlugin(
		&staticResolver{promptID: "p1"},
		map[string]*tables.TablePrompt{"p1": &prompt},
		map[string]map[int]*tables.TablePromptVersion{"p1": {1: &v}},
	)

	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("hi")))
	require.NoError(t, err)
	assert.Len(t, out.ChatRequest.Input, 1)
}

func TestPreLLMHook_MultipleTemplateMessages(t *testing.T) {
	v := makeVersion(1, "p1", true,
		versionMsg(schemas.ChatMessageRoleSystem, "sys prompt"),
		versionMsg(schemas.ChatMessageRoleUser, "example input"),
		versionMsg(schemas.ChatMessageRoleAssistant, "example output"),
	)
	prompt := makePrompt("p1", &v)

	p := newTestPlugin(
		&staticResolver{promptID: "p1"},
		map[string]*tables.TablePrompt{"p1": &prompt},
		map[string]map[int]*tables.TablePromptVersion{"p1": {1: &v}},
	)

	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("actual question")))
	require.NoError(t, err)
	require.Len(t, out.ChatRequest.Input, 4, "expected 3 template messages + 1 original")

	assert.Equal(t, schemas.ChatMessageRoleSystem, out.ChatRequest.Input[0].Role)
	assert.Equal(t, "sys prompt", msgText(out.ChatRequest.Input[0]))

	assert.Equal(t, schemas.ChatMessageRoleUser, out.ChatRequest.Input[1].Role)
	assert.Equal(t, "example input", msgText(out.ChatRequest.Input[1]))

	assert.Equal(t, schemas.ChatMessageRoleAssistant, out.ChatRequest.Input[2].Role)
	assert.Equal(t, "example output", msgText(out.ChatRequest.Input[2]))

	// Original user message must be last, content preserved.
	assert.Equal(t, schemas.ChatMessageRoleUser, out.ChatRequest.Input[3].Role)
	assert.Equal(t, "actual question", msgText(out.ChatRequest.Input[3]))
}

func TestPreLLMHook_ResolverError(t *testing.T) {
	log := NewMockLogger()
	p := newTestPluginWithLogger(
		&staticResolver{err: errTest("resolver failed")},
		nil, nil, log,
	)

	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("hi")))
	require.NoError(t, err, "PreLLMHook must not propagate resolver errors")
	assert.Len(t, out.ChatRequest.Input, 1, "input should be unchanged")
	assert.True(t, log.Warned(), "expected warning for resolver error")
}

func TestPreLLMHook_MessageJSON_FallbackPath(t *testing.T) {
	// Messages where Message ([]byte) is nil but MessageJSON is set — the fallback
	// branch in chatMessagesFromVersionMessages. This mirrors rows loaded from
	// an older DB schema before AfterFind was established.
	v := makeVersion(1, "p1", true,
		versionMsgViaJSON(schemas.ChatMessageRoleSystem, "from json field"),
	)
	prompt := makePrompt("p1", &v)

	p := newTestPlugin(
		&staticResolver{promptID: "p1"},
		map[string]*tables.TablePrompt{"p1": &prompt},
		map[string]map[int]*tables.TablePromptVersion{"p1": {1: &v}},
	)

	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("hi")))
	require.NoError(t, err)
	require.Len(t, out.ChatRequest.Input, 2)

	assert.Equal(t, schemas.ChatMessageRoleSystem, out.ChatRequest.Input[0].Role)
	assert.Equal(t, "from json field", msgText(out.ChatRequest.Input[0]))
}

func TestPreLLMHook_ResponsesRequest(t *testing.T) {
	v := makeVersion(1, "p1", true,
		versionMsg(schemas.ChatMessageRoleSystem, "be concise"),
	)
	prompt := makePrompt("p1", &v)

	p := newTestPlugin(
		&staticResolver{promptID: "p1"},
		map[string]*tables.TablePrompt{"p1": &prompt},
		map[string]map[int]*tables.TablePromptVersion{"p1": {1: &v}},
	)

	userRole := schemas.ResponsesMessageRoleType("user")
	req := &schemas.BifrostRequest{
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Input: []schemas.ResponsesMessage{{Role: &userRole}},
		},
	}

	out, _, err := p.PreLLMHook(bfCtx(), req)
	require.NoError(t, err)
	// Template message(s) prepended before the original user input.
	assert.Greater(t, len(out.ResponsesRequest.Input), 1, "expected template prepended before user message")
	// Original user message must still be last.
	last := out.ResponsesRequest.Input[len(out.ResponsesRequest.Input)-1]
	assert.Equal(t, schemas.ResponsesMessageRoleType("user"), *last.Role)
}

// TestPreLLMHook_PromptSystemMsg_PlusUserInputSystemMsg verifies that when the
// prompt template contains a system message and the incoming request also starts
// with a system message, both system messages are forwarded to the model —
// the plugin's only job is prepending, not de-duplicating or filtering roles.
func TestPreLLMHook_PromptSystemMsg_PlusUserInputSystemMsg(t *testing.T) {
	v := makeVersion(1, "p1", true,
		versionMsg(schemas.ChatMessageRoleSystem, "prompt system"),
	)
	prompt := makePrompt("p1", &v)

	p := newTestPlugin(
		&staticResolver{promptID: "p1"},
		map[string]*tables.TablePrompt{"p1": &prompt},
		map[string]map[int]*tables.TablePromptVersion{"p1": {1: &v}},
	)

	// Incoming request already has its own system message before the user turn.
	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(
		systemMsg("user-side system context"),
		userMsg("actual question"),
	))
	require.NoError(t, err)
	require.Len(t, out.ChatRequest.Input, 3, "expected prompt system + user system + user message")

	assert.Equal(t, schemas.ChatMessageRoleSystem, out.ChatRequest.Input[0].Role)
	assert.Equal(t, "prompt system", msgText(out.ChatRequest.Input[0]))

	assert.Equal(t, schemas.ChatMessageRoleSystem, out.ChatRequest.Input[1].Role)
	assert.Equal(t, "user-side system context", msgText(out.ChatRequest.Input[1]))

	assert.Equal(t, schemas.ChatMessageRoleUser, out.ChatRequest.Input[2].Role)
	assert.Equal(t, "actual question", msgText(out.ChatRequest.Input[2]))
}

// TestPreLLMHook_PromptWithToolCallMessages_PlusUserMessage verifies that when
// the prompt template contains a full tool-call turn (system → assistant with
// tool_calls → tool result) and the user sends a new message, the entire
// template is prepended and all fields (ToolCalls, ToolCallID) are preserved.
func TestPreLLMHook_PromptWithToolCallMessages_PlusUserMessage(t *testing.T) {
	const callID = "call_abc123"
	v := makeVersion(1, "p1", true,
		versionMsg(schemas.ChatMessageRoleSystem, "you are a weather bot"),
		versionMsgWithToolCall(callID, "get_weather", `{"city":"Paris"}`),
		versionMsgToolResult(callID, "Sunny, 22°C"),
	)
	prompt := makePrompt("p1", &v)

	p := newTestPlugin(
		&staticResolver{promptID: "p1"},
		map[string]*tables.TablePrompt{"p1": &prompt},
		map[string]map[int]*tables.TablePromptVersion{"p1": {1: &v}},
	)

	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("what about tomorrow?")))
	require.NoError(t, err)
	require.Len(t, out.ChatRequest.Input, 4, "expected system + assistant(tool_calls) + tool_result + user")

	// System message from prompt.
	assert.Equal(t, schemas.ChatMessageRoleSystem, out.ChatRequest.Input[0].Role)
	assert.Equal(t, "you are a weather bot", msgText(out.ChatRequest.Input[0]))

	// Assistant message with tool_calls must carry its ToolCalls slice.
	assistantMsg := out.ChatRequest.Input[1]
	assert.Equal(t, schemas.ChatMessageRoleAssistant, assistantMsg.Role)
	require.NotNil(t, assistantMsg.ChatAssistantMessage, "ChatAssistantMessage must be present")
	require.Len(t, assistantMsg.ChatAssistantMessage.ToolCalls, 1)
	tc := assistantMsg.ChatAssistantMessage.ToolCalls[0]
	require.NotNil(t, tc.ID)
	assert.Equal(t, callID, *tc.ID)
	require.NotNil(t, tc.Function.Name)
	assert.Equal(t, "get_weather", *tc.Function.Name)
	assert.Equal(t, `{"city":"Paris"}`, tc.Function.Arguments)

	// Tool result message must carry the ToolCallID.
	toolResultMsg := out.ChatRequest.Input[2]
	assert.Equal(t, schemas.ChatMessageRoleTool, toolResultMsg.Role)
	assert.Equal(t, "Sunny, 22°C", msgText(toolResultMsg))
	require.NotNil(t, toolResultMsg.ChatToolMessage, "ChatToolMessage must be present")
	require.NotNil(t, toolResultMsg.ChatToolMessage.ToolCallID)
	assert.Equal(t, callID, *toolResultMsg.ChatToolMessage.ToolCallID)

	// Original user message is last.
	assert.Equal(t, schemas.ChatMessageRoleUser, out.ChatRequest.Input[3].Role)
	assert.Equal(t, "what about tomorrow?", msgText(out.ChatRequest.Input[3]))
}

// TestPreLLMHook_MultipleSystemMessages_InPromptTemplate verifies that a prompt
// template may itself contain multiple system messages and all of them are
// prepended before the user's input in the original order.
func TestPreLLMHook_MultipleSystemMessages_InPromptTemplate(t *testing.T) {
	v := makeVersion(1, "p1", true,
		versionMsg(schemas.ChatMessageRoleSystem, "first system"),
		versionMsg(schemas.ChatMessageRoleSystem, "second system"),
	)
	prompt := makePrompt("p1", &v)

	p := newTestPlugin(
		&staticResolver{promptID: "p1"},
		map[string]*tables.TablePrompt{"p1": &prompt},
		map[string]map[int]*tables.TablePromptVersion{"p1": {1: &v}},
	)

	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("hello")))
	require.NoError(t, err)
	require.Len(t, out.ChatRequest.Input, 3, "expected 2 system messages + user message")

	assert.Equal(t, schemas.ChatMessageRoleSystem, out.ChatRequest.Input[0].Role)
	assert.Equal(t, "first system", msgText(out.ChatRequest.Input[0]))

	assert.Equal(t, schemas.ChatMessageRoleSystem, out.ChatRequest.Input[1].Role)
	assert.Equal(t, "second system", msgText(out.ChatRequest.Input[1]))

	assert.Equal(t, schemas.ChatMessageRoleUser, out.ChatRequest.Input[2].Role)
	assert.Equal(t, "hello", msgText(out.ChatRequest.Input[2]))
}

// ============================================================
// HTTPTransportPreHook
// ============================================================

func TestHTTPTransportPreHook_NilRequest(t *testing.T) {
	p := newTestPlugin(nil, nil, nil)
	resp, err := p.HTTPTransportPreHook(bfCtx(), nil)
	assert.NoError(t, err)
	assert.Nil(t, resp)
}

func TestHTTPTransportPreHook_SetsPromptID(t *testing.T) {
	p := newTestPlugin(nil, nil, nil)
	ctx := bfCtx()
	req := &schemas.HTTPRequest{
		Headers: map[string]string{PromptIDHeader: "my-prompt"},
	}

	_, _ = p.HTTPTransportPreHook(ctx, req)

	got, _ := ctx.Value(PromptIDKey).(string)
	assert.Equal(t, "my-prompt", got)
}

func TestHTTPTransportPreHook_SetsVersionID(t *testing.T) {
	p := newTestPlugin(nil, nil, nil)
	ctx := bfCtx()
	req := &schemas.HTTPRequest{
		Headers: map[string]string{PromptVersionHeader: "42"},
	}

	_, _ = p.HTTPTransportPreHook(ctx, req)

	got, _ := ctx.Value(PromptVersionKey).(string)
	assert.Equal(t, "42", got)
}

func TestHTTPTransportPreHook_TrimsWhitespace(t *testing.T) {
	p := newTestPlugin(nil, nil, nil)
	ctx := bfCtx()
	req := &schemas.HTTPRequest{
		Headers: map[string]string{PromptIDHeader: "  padded  "},
	}

	_, _ = p.HTTPTransportPreHook(ctx, req)

	got, _ := ctx.Value(PromptIDKey).(string)
	assert.Equal(t, "padded", got)
}

func TestHTTPTransportPreHook_WhitespaceOnlyNotSet(t *testing.T) {
	p := newTestPlugin(nil, nil, nil)
	ctx := bfCtx()
	req := &schemas.HTTPRequest{
		Headers: map[string]string{PromptIDHeader: "   "},
	}

	_, _ = p.HTTPTransportPreHook(ctx, req)

	assert.Nil(t, ctx.Value(PromptIDKey), "whitespace-only header must not be stored in context")
}

func TestHTTPTransportPreHook_CaseInsensitiveHeaders(t *testing.T) {
	p := newTestPlugin(nil, nil, nil)
	ctx := bfCtx()
	// "X-Bf-Prompt-Id" is a title-case variant of the canonical "x-bf-prompt-id".
	req := &schemas.HTTPRequest{
		Headers: map[string]string{"X-Bf-Prompt-Id": "upper-case"},
	}

	_, _ = p.HTTPTransportPreHook(ctx, req)

	got, _ := ctx.Value(PromptIDKey).(string)
	assert.Equal(t, "upper-case", got)
}

// ============================================================
// chatMessageFromStoredJSON
// ============================================================

func TestChatMessageFromStoredJSON(t *testing.T) {
	systemText := "you are helpful"
	directMsg := schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleSystem,
		Content: &schemas.ChatMessageContent{ContentStr: &systemText},
	}
	directJSON, _ := json.Marshal(directMsg)
	envelopeJSON := []byte(`{"payload":` + string(directJSON) + `}`)

	tests := []struct {
		name     string
		input    []byte
		wantErr  bool
		wantRole schemas.ChatMessageRole
		wantText string
	}{
		{
			name:     "direct format",
			input:    directJSON,
			wantRole: schemas.ChatMessageRoleSystem,
			wantText: systemText,
		},
		{
			name:     "envelope format",
			input:    envelopeJSON,
			wantRole: schemas.ChatMessageRoleSystem,
			wantText: systemText,
		},
		{
			// UI format for assistant messages: originalType=completion_result,
			// payload is a BifrostChatResponse; message lives at choices[0].message.
			name:     "completion_result envelope (UI assistant format)",
			input:    []byte(`{"originalType":"completion_result","payload":{"id":"r1","choices":[{"index":0,"message":{"role":"assistant","content":"hi there"},"finish_reason":"stop"}]}}`),
			wantRole: schemas.ChatMessageRoleAssistant,
			wantText: "hi there",
		},
		{
			// completion_result with no choices falls through to direct ChatMessage parse.
			name:     "completion_result envelope with empty choices",
			input:    []byte(`{"originalType":"completion_result","payload":{"id":"r1","choices":[]}}`),
			wantErr:  false,
			wantRole: "",
			wantText: "",
		},
		{
			name:    "empty bytes",
			input:   []byte(""),
			wantErr: true,
		},
		{
			name:    "null bytes",
			input:   []byte("null"),
			wantErr: true,
		},
		{
			name:    "whitespace only",
			input:   []byte("   "),
			wantErr: true,
		},
		{
			name:    "malformed envelope payload",
			input:   []byte(`{"payload":"not-a-chat-message"}`),
			wantErr: true,
		},
		{
			// {"payload":null} — envelope path is skipped (payload is "null"),
			// falls through to direct decode of the outer object as ChatMessage.
			// schemas.Unmarshal succeeds on an unknown-field object → empty ChatMessage, no error.
			name:     "envelope with null payload falls through to direct decode",
			input:    []byte(`{"payload":null}`),
			wantErr:  false,
			wantRole: "",
			wantText: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := convertVersionMessagesToChatMessages(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantRole, got.Role)
			assert.Equal(t, tt.wantText, msgText(got))
		})
	}
}

func TestChatMessageFromStoredJSON_EnvelopeWithEmptyStringPayload(t *testing.T) {
	// {"payload":""} — the payload field is a non-null, non-empty JSON string `""`.
	// The envelope path attempts to unmarshal `""` (a JSON string literal) into
	// schemas.ChatMessage (a struct), which fails. The error is returned directly;
	// there is no further fallback.
	input := []byte(`{"payload":""}`)
	_, err := convertVersionMessagesToChatMessages(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decoding prompt message envelope payload")
}

// ============================================================
// parsePromptVersionNumber
// ============================================================

func TestParsePromptVersionNumber(t *testing.T) {
	type want struct {
		num       int
		specified bool
		wantErr   bool
	}

	tests := []struct {
		name  string
		value any // stored in context; nil means don't set
		want  want
	}{
		{name: "nil — not specified", value: nil, want: want{0, false, false}},
		{name: "string valid", value: "99", want: want{99, true, false}},
		{name: "string empty", value: "", want: want{0, false, false}},
		{name: "string whitespace", value: "   ", want: want{0, false, false}},
		{name: "string invalid", value: "abc", want: want{0, true, true}},
		{name: "unknown type", value: struct{}{}, want: want{0, false, false}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := bfCtx()
			if tt.value != nil {
				ctx.SetValue(PromptVersionKey, tt.value)
			}

			num, err := parseNumberFromContext(ctx, PromptVersionKey)

			if tt.want.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want.num, num)
		})
	}
}

// ============================================================
// mergeChatMessages
// ============================================================

func TestMergeChatMessages(t *testing.T) {
	t.Run("prepends prefix before existing messages", func(t *testing.T) {
		dest := []schemas.ChatMessage{userMsg("original")}
		prefix := []schemas.ChatMessage{systemMsg("system")}
		mergeChatMessages(&dest, prefix)

		require.Len(t, dest, 2)
		assert.Equal(t, schemas.ChatMessageRoleSystem, dest[0].Role)
		assert.Equal(t, "system", msgText(dest[0]))
		assert.Equal(t, schemas.ChatMessageRoleUser, dest[1].Role)
		assert.Equal(t, "original", msgText(dest[1]))
	})

	t.Run("nil dest is a no-op", func(t *testing.T) {
		// Must not panic.
		mergeChatMessages(nil, []schemas.ChatMessage{systemMsg("x")})
	})

	t.Run("empty prefix is a no-op", func(t *testing.T) {
		dest := []schemas.ChatMessage{userMsg("only")}
		mergeChatMessages(&dest, nil)
		assert.Len(t, dest, 1)
		assert.Equal(t, "only", msgText(dest[0]))
	})
}

// ============================================================
// chatMessagesFromVersionMessages
// ============================================================

func TestChatMessagesFromVersionMessages_SingleMessage(t *testing.T) {
	msg := versionMsg(schemas.ChatMessageRoleUser, "hello")
	out, err := chatMessagesFromVersionMessages([]tables.TablePromptVersionMessage{msg})
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, schemas.ChatMessageRoleUser, out[0].Role)
	assert.Equal(t, "hello", msgText(out[0]))
}

func TestChatMessagesFromVersionMessages_MessageJSONFallback(t *testing.T) {
	// Row has no Message bytes but has MessageJSON — exercises the fallback branch.
	msg := versionMsgViaJSON(schemas.ChatMessageRoleAssistant, "assistant reply")
	out, err := chatMessagesFromVersionMessages([]tables.TablePromptVersionMessage{msg})
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, schemas.ChatMessageRoleAssistant, out[0].Role)
	assert.Equal(t, "assistant reply", msgText(out[0]))
}

func TestChatMessagesFromVersionMessages_PreservesOrder(t *testing.T) {
	msgs := []tables.TablePromptVersionMessage{
		versionMsg(schemas.ChatMessageRoleSystem, "first"),
		versionMsg(schemas.ChatMessageRoleUser, "second"),
		versionMsg(schemas.ChatMessageRoleAssistant, "third"),
	}
	out, err := chatMessagesFromVersionMessages(msgs)
	require.NoError(t, err)
	require.Len(t, out, 3)
	assert.Equal(t, schemas.ChatMessageRoleSystem, out[0].Role)
	assert.Equal(t, "first", msgText(out[0]))
	assert.Equal(t, schemas.ChatMessageRoleUser, out[1].Role)
	assert.Equal(t, "second", msgText(out[1]))
	assert.Equal(t, schemas.ChatMessageRoleAssistant, out[2].Role)
	assert.Equal(t, "third", msgText(out[2]))
}

func TestChatMessagesFromVersionMessages_InvalidJSON(t *testing.T) {
	bad := tables.TablePromptVersionMessage{Message: []byte(`not-json`)}
	_, err := chatMessagesFromVersionMessages([]tables.TablePromptVersionMessage{bad})
	require.Error(t, err)
}

// ============================================================
// PreLLMHook — model params merge and override
// ============================================================

// TestPreLLMHook_VersionParamsApplied_WhenRequestHasNoParams verifies that when
// the request carries no Params at all, the version's ModelParams become the
// effective parameters on the outgoing request.
func TestPreLLMHook_VersionParamsApplied_WhenRequestHasNoParams(t *testing.T) {
	v := makeVersionWithParams(1, "p1", true,
		tables.ModelParams{"temperature": float64(0.7)},
		versionMsg(schemas.ChatMessageRoleSystem, "sys"),
	)
	prompt := makePrompt("p1", &v)

	p := newTestPlugin(
		&staticResolver{promptID: "p1"},
		map[string]*tables.TablePrompt{"p1": &prompt},
		map[string]map[int]*tables.TablePromptVersion{"p1": {1: &v}},
	)

	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("hi")))
	require.NoError(t, err)
	require.NotNil(t, out.ChatRequest.Params, "expected Params to be set from version ModelParams")
	require.NotNil(t, out.ChatRequest.Params.Temperature)
	assert.InDelta(t, 0.7, *out.ChatRequest.Params.Temperature, 0.001)
}

// TestPreLLMHook_RequestParamsOverrideVersionParams verifies that when both the
// version and the request specify the same parameter, the request value wins.
func TestPreLLMHook_RequestParamsOverrideVersionParams(t *testing.T) {
	reqTemp := 0.9
	v := makeVersionWithParams(1, "p1", true,
		tables.ModelParams{"temperature": float64(0.3)},
		versionMsg(schemas.ChatMessageRoleSystem, "sys"),
	)
	prompt := makePrompt("p1", &v)

	p := newTestPlugin(
		&staticResolver{promptID: "p1"},
		map[string]*tables.TablePrompt{"p1": &prompt},
		map[string]map[int]*tables.TablePromptVersion{"p1": {1: &v}},
	)

	req := chatRequestWithParams(&schemas.ChatParameters{Temperature: &reqTemp}, userMsg("hello"))
	out, _, err := p.PreLLMHook(bfCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, out.ChatRequest.Params)
	require.NotNil(t, out.ChatRequest.Params.Temperature)
	assert.InDelta(t, reqTemp, *out.ChatRequest.Params.Temperature, 0.001,
		"request temperature must override version default temperature")
}

// TestPreLLMHook_RequestParamsPartialOverride verifies the mixed case: version
// sets temperature and max_completion_tokens; request overrides only temperature.
// The version's max_completion_tokens must still be applied.
func TestPreLLMHook_RequestParamsPartialOverride(t *testing.T) {
	reqTemp := 0.9
	maxTokens := 200
	v := makeVersionWithParams(1, "p1", true,
		tables.ModelParams{
			"temperature":           float64(0.3),
			"max_completion_tokens": float64(maxTokens),
		},
		versionMsg(schemas.ChatMessageRoleSystem, "sys"),
	)
	prompt := makePrompt("p1", &v)

	p := newTestPlugin(
		&staticResolver{promptID: "p1"},
		map[string]*tables.TablePrompt{"p1": &prompt},
		map[string]map[int]*tables.TablePromptVersion{"p1": {1: &v}},
	)

	req := chatRequestWithParams(&schemas.ChatParameters{Temperature: &reqTemp}, userMsg("hello"))
	out, _, err := p.PreLLMHook(bfCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, out.ChatRequest.Params)
	require.NotNil(t, out.ChatRequest.Params.Temperature)
	assert.InDelta(t, reqTemp, *out.ChatRequest.Params.Temperature, 0.001,
		"request temperature must override version temperature")
	require.NotNil(t, out.ChatRequest.Params.MaxCompletionTokens,
		"version max_completion_tokens must be applied when request does not override it")
	assert.Equal(t, maxTokens, *out.ChatRequest.Params.MaxCompletionTokens)
}

// ============================================================
// PreLLMHook — model field preservation
// ============================================================

// TestPreLLMHook_ModelInVersionParams_DoesNotOverrideRequestModel verifies that
// a "model" key inside a version's ModelParams (which the UI may store alongside
// temperature etc.) does NOT replace the model field on the outgoing
// BifrostChatRequest. The model chosen by the caller must always win.
func TestPreLLMHook_ModelInVersionParams_DoesNotOverrideRequestModel(t *testing.T) {
	v := makeVersionWithParams(1, "p1", true,
		tables.ModelParams{
			"model":       "openai/gpt-4o",
			"temperature": float64(0.5),
		},
		versionMsg(schemas.ChatMessageRoleSystem, "sys"),
	)
	prompt := makePrompt("p1", &v)

	p := newTestPlugin(
		&staticResolver{promptID: "p1"},
		map[string]*tables.TablePrompt{"p1": &prompt},
		map[string]map[int]*tables.TablePromptVersion{"p1": {1: &v}},
	)

	req := chatRequestWithModel("openai/gpt-3.5-turbo", userMsg("hi"))
	out, _, err := p.PreLLMHook(bfCtx(), req)
	require.NoError(t, err)
	assert.Equal(t, "openai/gpt-3.5-turbo", out.ChatRequest.Model,
		"request model must not be overridden by model stored in version ModelParams")
}

// ============================================================
// loadCache + PreLLMHook integration (store → cache → injection)
// ============================================================

// TestLoadCacheAndPreLLMHook_EndToEnd verifies the full pipeline:
// mockStore returns TablePrompt/TablePromptVersion structs → loadCache populates
// the in-memory maps → PreLLMHook injects the template messages correctly.
// This catches any mismatch between how loadCache builds the maps and how
// PreLLMHook reads them (e.g. pointer aliasing, LatestVersion linking).
func TestLoadCacheAndPreLLMHook_EndToEnd(t *testing.T) {
	v := makeVersion(1, "p1", true,
		versionMsg(schemas.ChatMessageRoleSystem, "end-to-end system"),
	)
	prompt := makePrompt("p1", &v)

	ms := &mockStore{
		prompts:  []tables.TablePrompt{prompt},
		versions: []tables.TablePromptVersion{v},
	}

	p := newPluginWithStore(ms)
	require.NoError(t, p.loadCache(context.Background()))

	p.resolver = &staticResolver{promptID: "p1"}

	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("user msg")))
	require.NoError(t, err)
	require.Len(t, out.ChatRequest.Input, 2)

	assert.Equal(t, schemas.ChatMessageRoleSystem, out.ChatRequest.Input[0].Role)
	assert.Equal(t, "end-to-end system", msgText(out.ChatRequest.Input[0]))
	assert.Equal(t, schemas.ChatMessageRoleUser, out.ChatRequest.Input[1].Role)
	assert.Equal(t, "user msg", msgText(out.ChatRequest.Input[1]))
}

// TestLoadCacheAndPreLLMHook_SpecificVersion exercises the loadCache → PreLLMHook
// path for a version lookup by ID (not just the LatestVersion pointer).
func TestLoadCacheAndPreLLMHook_SpecificVersion(t *testing.T) {
	vOld := makeVersion(2, "p1", false,
		versionMsg(schemas.ChatMessageRoleSystem, "old via store"),
	)
	vLatest := makeVersion(3, "p1", true,
		versionMsg(schemas.ChatMessageRoleSystem, "latest via store"),
	)
	prompt := makePrompt("p1", &vLatest)

	ms := &mockStore{
		prompts:  []tables.TablePrompt{prompt},
		versions: []tables.TablePromptVersion{vOld, vLatest},
	}

	p := newPluginWithStore(ms)
	require.NoError(t, p.loadCache(context.Background()))

	p.resolver = &staticResolver{promptID: "p1", versionNumber: 2}

	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("question")))
	require.NoError(t, err)
	require.Len(t, out.ChatRequest.Input, 2)
	assert.Equal(t, "old via store", msgText(out.ChatRequest.Input[0]))
}

// TestPreLLMHook_AssistantMessage_UIFormat verifies that assistant messages stored
// in the Bifrost UI's completion_result format (payload.choices[0].message) are
// correctly extracted and prepended to the request.
func TestPreLLMHook_AssistantMessage_UIFormat(t *testing.T) {
	v := makeVersion(1, "p1", true,
		versionMsg(schemas.ChatMessageRoleSystem, "be helpful"),
		versionMsgAssistantUIFormat("sure, how can I help?"),
	)
	prompt := makePrompt("p1", &v)

	p := newTestPlugin(
		&staticResolver{promptID: "p1"},
		map[string]*tables.TablePrompt{"p1": &prompt},
		map[string]map[int]*tables.TablePromptVersion{"p1": {1: &v}},
	)

	out, _, err := p.PreLLMHook(bfCtx(), chatRequest(userMsg("hello")))
	require.NoError(t, err)
	require.Len(t, out.ChatRequest.Input, 3, "expected system + assistant + user")

	assert.Equal(t, schemas.ChatMessageRoleSystem, out.ChatRequest.Input[0].Role)
	assert.Equal(t, "be helpful", msgText(out.ChatRequest.Input[0]))

	assert.Equal(t, schemas.ChatMessageRoleAssistant, out.ChatRequest.Input[1].Role)
	assert.Equal(t, "sure, how can I help?", msgText(out.ChatRequest.Input[1]))

	assert.Equal(t, schemas.ChatMessageRoleUser, out.ChatRequest.Input[2].Role)
	assert.Equal(t, "hello", msgText(out.ChatRequest.Input[2]))
}