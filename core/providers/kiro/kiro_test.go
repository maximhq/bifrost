package kiro

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

type testLogger struct{}

func (testLogger) Debug(string, ...any)                   {}
func (testLogger) Info(string, ...any)                    {}
func (testLogger) Warn(string, ...any)                    {}
func (testLogger) Error(string, ...any)                   {}
func (testLogger) Fatal(string, ...any)                   {}
func (testLogger) SetLevel(schemas.LogLevel)              {}
func (testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func passthroughPostHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return resp, err
}

// testFrame is one eventstream message to encode.
type testFrame struct {
	messageType   string
	eventType     string
	exceptionType string
	payload       string
}

func event(eventType, payload string) testFrame {
	return testFrame{messageType: "event", eventType: eventType, payload: payload}
}

func encodeFrames(t *testing.T, frames ...testFrame) []byte {
	t.Helper()
	var buf bytes.Buffer
	encoder := eventstream.NewEncoder()
	for _, frame := range frames {
		var headers eventstream.Headers
		headers.Set(":message-type", eventstream.StringValue(frame.messageType))
		if frame.eventType != "" {
			headers.Set(":event-type", eventstream.StringValue(frame.eventType))
		}
		if frame.exceptionType != "" {
			headers.Set(":exception-type", eventstream.StringValue(frame.exceptionType))
		}
		headers.Set(":content-type", eventstream.StringValue("application/json"))
		if err := encoder.Encode(&buf, eventstream.Message{Headers: headers, Payload: []byte(frame.payload)}); err != nil {
			t.Fatalf("encode frame: %v", err)
		}
	}
	return buf.Bytes()
}

func decodeAll(t *testing.T, parser *streamParser, data []byte) ([]streamOutput, *schemas.BifrostError) {
	t.Helper()
	decoder := eventstream.NewDecoder()
	reader := bytes.NewReader(data)
	var outputs []streamOutput
	for {
		msg, err := decoder.Decode(reader, nil)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		out, bErr := parser.handle(msg)
		if bErr != nil {
			return outputs, bErr
		}
		outputs = append(outputs, out...)
	}
	out, bErr := parser.finish()
	return append(outputs, out...), bErr
}

func joinKind(outputs []streamOutput, kind outputKind) string {
	var sb strings.Builder
	for _, out := range outputs {
		if out.kind == kind {
			sb.WriteString(out.text)
		}
	}
	return sb.String()
}

func futureExpiry() string {
	return time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
}

const testProfileArn = "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABCDEF123"

func testKey(t *testing.T, id string, creds Credentials) schemas.Key {
	t.Helper()
	value, err := creds.Encode()
	if err != nil {
		t.Fatalf("encode credential: %v", err)
	}
	return schemas.Key{ID: id, Value: *schemas.NewSecretVar(value), Models: schemas.WhiteList{"*"}}
}

func userMessage(text string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(text)}}
}

func decodePayload(t *testing.T, built *builtPayload) kiroPayload {
	t.Helper()
	var payload kiroPayload
	if err := json.Unmarshal(built.body, &payload); err != nil {
		t.Fatalf("payload is not valid JSON: %v\n%s", err, built.body)
	}
	return payload
}

func TestParseCredentials(t *testing.T) {
	t.Run("bare refresh token", func(t *testing.T) {
		creds, err := ParseCredentials("  aorAAAAexampleRefresh  ")
		if err != nil {
			t.Fatal(err)
		}
		if creds.RefreshToken != "aorAAAAexampleRefresh" || creds.usesOIDC() || creds.ssoRegion() != "us-east-1" {
			t.Fatalf("unexpected credential: %+v", creds)
		}
	})
	t.Run("kiro-auth-token.json", func(t *testing.T) {
		creds, err := ParseCredentials(`{"accessToken":"aoa","refreshToken":"aor","expiresAt":"2030-01-02T03:04:05.000Z","profileArn":"arn:aws:codewhisperer:eu-central-1:123456789012:profile/XYZ","authMethod":"social","provider":"Google","clientIdHash":"ignored"}`)
		if err != nil {
			t.Fatal(err)
		}
		if creds.AccessToken != "aoa" || creds.RefreshToken != "aor" || creds.AuthMethod != "social" {
			t.Fatalf("unexpected credential: %+v", creds)
		}
		if got := creds.accessTokenExpiry(); got.Year() != 2030 || got.Second() != 5 {
			t.Fatalf("expiresAt not parsed: %v (%q)", got, creds.ExpiresAt)
		}
		if creds.apiRegion() != "eu-central-1" || creds.ssoRegion() != "us-east-1" {
			t.Fatalf("regions: api=%s sso=%s", creds.apiRegion(), creds.ssoRegion())
		}
	})
	t.Run("snake case and epoch seconds", func(t *testing.T) {
		creds, err := ParseCredentials(`{"access_token":"a","refresh_token":"r","expires_at":1893456000,"client_id":"cid","client_secret":"sec","region":"eu-west-1","api_region":"us-east-1"}`)
		if err != nil {
			t.Fatal(err)
		}
		if !creds.usesOIDC() || creds.ssoRegion() != "eu-west-1" || creds.apiRegion() != "us-east-1" {
			t.Fatalf("unexpected credential: %+v", creds)
		}
		if got := creds.accessTokenExpiry().Unix(); got != 1893456000 {
			t.Fatalf("epoch seconds: got %d", got)
		}
		if arn, fallback := creds.requestProfile(); arn != builderIDServiceProfileArn || !fallback {
			t.Fatalf("OIDC credential without profile must use the Builder ID fallback, got %q %v", arn, fallback)
		}
	})
	t.Run("epoch milliseconds string", func(t *testing.T) {
		creds, err := ParseCredentials(`{"refreshToken":"r","expiresAt":"1893456000123"}`)
		if err != nil {
			t.Fatal(err)
		}
		if got := creds.accessTokenExpiry().UnixMilli(); got != 1893456000123 {
			t.Fatalf("epoch ms: got %d", got)
		}
	})
	t.Run("unparseable expiry is treated as expired", func(t *testing.T) {
		creds, err := ParseCredentials(`{"refreshToken":"r","accessToken":"a","expiresAt":"soon"}`)
		if err != nil {
			t.Fatal(err)
		}
		if !creds.accessTokenExpiry().IsZero() {
			t.Fatalf("expected zero expiry")
		}
	})
	for name, value := range map[string]string{
		"empty":               "  ",
		"missing refresh":     `{"accessToken":"a"}`,
		"bad region":          `{"refreshToken":"r","region":"evil.com/x"}`,
		"bad api region":      `{"refreshToken":"r","apiRegion":"us-east-1.attacker"}`,
		"bad profile":         `{"refreshToken":"r","profileArn":"arn:aws:s3:::bucket"}`,
		"half client":         `{"refreshToken":"r","clientId":"c"}`,
		"invalid json":        `{"refreshToken":`,
		"whitespace in token": "abc def",
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, err := ParseCredentials(value); err == nil {
				t.Fatalf("expected an error for %q", value)
			}
		})
	}
	t.Run("encode round trip", func(t *testing.T) {
		in := &Credentials{AccessToken: "a", RefreshToken: "r", ExpiresAt: "2030-01-01T00:00:00Z", ProfileArn: testProfileArn, Region: "us-east-1", ClientID: "c", ClientSecret: "s"}
		encoded, err := in.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(encoded, "\n") || !strings.HasPrefix(encoded, `{"accessToken":"a","refreshToken":"r"`) {
			t.Fatalf("unexpected encoding %s", encoded)
		}
		out, err := ParseCredentials(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if *out != *in {
			t.Fatalf("round trip mismatch:\n%+v\n%+v", in, out)
		}
	})
}

func TestNormalizeKiroModelId(t *testing.T) {
	cases := map[string]string{
		"kiro-auto":                  "auto",
		"kiro/auto":                  "auto",
		"auto":                       "auto",
		"claude-sonnet-4-5":          "claude-sonnet-4.5",
		"claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
		"Claude-Opus-4.6":            "claude-opus-4.6",
		"claude-3-7-sonnet":          "claude-sonnet-3.7",
		"kiro-claude-haiku-4-5":      "claude-haiku-4.5",
		"gpt-5.6-sol-high":           "gpt-5.6-sol",
		"kiro/qwen3-coder-next":      "qwen3-coder-next",
		"deepseek-3-2":               "deepseek-3.2",
	}
	for in, want := range cases {
		if got := normalizeKiroModelId(in); got != want {
			t.Errorf("normalizeKiroModelId(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListModelsFiltersByKey(t *testing.T) {
	all := listModelsForKey(schemas.Key{Models: schemas.WhiteList{"*"}}, false)
	if len(all.Data) != len(kiroModels) || all.Data[0].ID != "kiro/kiro-auto" {
		t.Fatalf("unexpected catalog: %d entries, first %v", len(all.Data), all.Data[0].ID)
	}
	restricted := listModelsForKey(schemas.Key{Models: schemas.WhiteList{"claude-sonnet-4.5"}, BlacklistedModels: schemas.BlackList{"kiro-auto"}}, false)
	if len(restricted.Data) != 1 || restricted.Data[0].ID != "kiro/claude-sonnet-4.5" {
		t.Fatalf("unexpected filtered catalog: %+v", restricted.Data)
	}
	blocked := listModelsForKey(schemas.Key{Models: schemas.WhiteList{"*"}, BlacklistedModels: schemas.BlackList{"claude-sonnet-4.5"}}, false)
	for _, m := range blocked.Data {
		if m.ID == "kiro/claude-sonnet-4.5" {
			t.Fatal("blacklisted model listed")
		}
	}
}

func TestBuildPayloadSystemPromptAndAlternation(t *testing.T) {
	request := &schemas.BifrostChatRequest{
		Model: "claude-sonnet-4-5",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Be terse.")}},
			userMessage("first"),
			userMessage("second"),
			{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("answer")}},
			userMessage("third"),
		},
	}
	built, bErr := buildKiroPayload(request, testProfileArn, wireClientIDE)
	if bErr != nil {
		t.Fatalf("build: %v", bErr.Error.Message)
	}
	payload := decodePayload(t, built)
	state := payload.ConversationState
	if payload.ProfileArn != testProfileArn || state.ChatTriggerType != "MANUAL" || state.ConversationID == "" {
		t.Fatalf("unexpected envelope: %+v", payload)
	}
	if state.AgentTaskType != "" || state.AgentContinuationID != "" {
		t.Fatal("IDE payload must not carry CLI agent fields")
	}
	if len(state.History) != 2 {
		t.Fatalf("expected merged history of 2 entries, got %d", len(state.History))
	}
	first := state.History[0].UserInputMessage
	if first == nil || first.Content != "Be terse.\n\nfirst\n\nsecond" || first.ModelID != "claude-sonnet-4.5" || first.Origin != originIDE {
		t.Fatalf("system prompt must be prepended to the first merged user turn: %+v", first)
	}
	if state.History[1].AssistantResponseMessage.Content != "answer" {
		t.Fatalf("unexpected assistant turn %+v", state.History[1])
	}
	if got := state.CurrentMessage.UserInputMessage.Content; got != "third" {
		t.Fatalf("current message = %q", got)
	}
	if built.inputTokens <= 2*entryFramingTokens {
		t.Fatalf("input estimate too small: %d", built.inputTokens)
	}
}

func TestBuildPayloadContinuationTurns(t *testing.T) {
	request := &schemas.BifrostChatRequest{
		Model: "kiro-auto",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}},
		},
	}
	built, bErr := buildKiroPayload(request, "", wireClientCLI)
	if bErr != nil {
		t.Fatalf("build: %v", bErr.Error.Message)
	}
	state := decodePayload(t, built).ConversationState
	if len(state.History) != 2 || state.History[0].UserInputMessage.Content != continuationMessage {
		t.Fatalf("a leading assistant turn needs a continuation user turn: %+v", state.History)
	}
	current := state.CurrentMessage.UserInputMessage
	if current.Content != continuationMessage || current.ModelID != "auto" || current.Origin != originCLI {
		t.Fatalf("a trailing assistant turn needs a continuation current message: %+v", current)
	}
	if state.AgentTaskType != "vibe" || state.AgentContinuationID == "" {
		t.Fatal("CLI payload must carry the agent fields")
	}
}

func TestBuildPayloadToolsAndResults(t *testing.T) {
	longName := strings.Repeat("mcp__server__", 6) + "do thing"
	params := &schemas.ToolFunctionParameters{
		Type:                 "object",
		Properties:           schemas.NewOrderedMapFromPairs(schemas.KV("path", map[string]any{"type": "string", "pattern": "^/"})),
		Required:             []string{"path"},
		AdditionalProperties: &schemas.AdditionalPropertiesStruct{AdditionalPropertiesBool: schemas.Ptr(false)},
	}
	request := &schemas.BifrostChatRequest{
		Model: "claude-sonnet-4.5",
		Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{
			{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "read_file", Description: schemas.Ptr("Read a file"), Parameters: params}},
			{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: longName}},
		}},
		Input: []schemas.ChatMessage{
			userMessage("read it"),
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{
					{ID: schemas.Ptr("call.1"), Type: schemas.Ptr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("read_file"), Arguments: `{"path":"/etc/hosts"}`}},
					{ID: schemas.Ptr("call_2"), Type: schemas.Ptr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr(longName), Arguments: ""}},
				}},
			},
			{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("127.0.0.1 localhost")}, ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call.1")}},
			{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("")}, ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call_2"), IsError: schemas.Ptr(true)}},
		},
	}
	built, bErr := buildKiroPayload(request, testProfileArn, wireClientIDE)
	if bErr != nil {
		t.Fatalf("build: %v", bErr.Error.Message)
	}
	state := decodePayload(t, built).ConversationState
	assistant := state.History[1].AssistantResponseMessage
	if len(assistant.ToolUses) != 2 {
		t.Fatalf("expected 2 tool uses, got %+v", assistant)
	}
	if assistant.ToolUses[0].ToolUseID != "call_1" || string(assistant.ToolUses[0].Input) != `{"path":"/etc/hosts"}` {
		t.Fatalf("tool use not converted: %+v", assistant.ToolUses[0])
	}
	if string(assistant.ToolUses[1].Input) != "{}" {
		t.Fatalf("empty arguments must become {}: %s", assistant.ToolUses[1].Input)
	}
	aliased := assistant.ToolUses[1].Name
	if len(aliased) > 64 || !toolNamePattern.MatchString(aliased) || built.nameMap[aliased] != longName {
		t.Fatalf("long tool name not normalized/reversible: %q -> %q", aliased, built.nameMap[aliased])
	}

	current := state.CurrentMessage.UserInputMessage
	if current.Content != toolResultCarrierMesage {
		t.Fatalf("tool-result-only turn needs the carrier text, got %q", current.Content)
	}
	results := current.UserInputMessageContext.ToolResults
	if len(results) != 2 || results[0].Status != "success" || results[0].Content[0].Text != "127.0.0.1 localhost" ||
		results[1].Status != "error" || results[1].Content[0].Text != emptyToolResultMessage {
		t.Fatalf("unexpected tool results: %+v", results)
	}
	tools := current.UserInputMessageContext.Tools
	if len(tools) != 2 || tools[0].ToolSpecification.Name != "read_file" || tools[1].ToolSpecification.Name != aliased {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	if tools[1].ToolSpecification.Description != "Tool: "+longName {
		t.Fatalf("missing default description: %q", tools[1].ToolSpecification.Description)
	}
	schemaJSON, _ := json.Marshal(tools[0].ToolSpecification.InputSchema.JSON)
	if got := string(schemaJSON); strings.Contains(got, "additionalProperties") || strings.Contains(got, "pattern") ||
		!strings.Contains(got, `"required":["path"]`) || !strings.Contains(got, `"type":"object"`) {
		t.Fatalf("schema not sanitized: %s", got)
	}
	for _, entry := range state.History {
		if entry.UserInputMessage != nil && entry.UserInputMessage.UserInputMessageContext != nil &&
			len(entry.UserInputMessage.UserInputMessageContext.Tools) > 0 {
			t.Fatal("tools must only be sent on the current message")
		}
	}

	request.Params.ToolChoice = &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("none")}
	built, bErr = buildKiroPayload(request, testProfileArn, wireClientIDE)
	if bErr != nil {
		t.Fatalf("build with tool_choice none: %v", bErr.Error.Message)
	}
	if ctx := decodePayload(t, built).ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext; len(ctx.Tools) != 0 {
		t.Fatal("tool_choice none must send no tools")
	}
}

func TestBuildPayloadRejectsInvalidConversations(t *testing.T) {
	orphan := &schemas.BifrostChatRequest{Model: "auto", Input: []schemas.ChatMessage{
		userMessage("hi"),
		{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("x")}, ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("nope")}},
	}}
	if _, bErr := buildKiroPayload(orphan, "", wireClientCLI); bErr == nil || *bErr.StatusCode != 400 {
		t.Fatalf("orphan tool result must be a 400, got %+v", bErr)
	}
	unanswered := &schemas.BifrostChatRequest{Model: "auto", Input: []schemas.ChatMessage{
		userMessage("hi"),
		{Role: schemas.ChatMessageRoleAssistant, ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{
			{ID: schemas.Ptr("c1"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("f"), Arguments: "{}"}},
		}}},
		userMessage("never mind"),
	}}
	if _, bErr := buildKiroPayload(unanswered, "", wireClientCLI); bErr == nil || *bErr.StatusCode != 400 {
		t.Fatalf("unanswered tool call must be a 400, got %+v", bErr)
	}
	forced := &schemas.BifrostChatRequest{Model: "auto", Input: []schemas.ChatMessage{userMessage("hi")},
		Params: &schemas.ChatParameters{ToolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("required")}}}
	if _, bErr := buildKiroPayload(forced, "", wireClientCLI); bErr == nil || *bErr.StatusCode != 400 {
		t.Fatalf("forced tool choice must be a 400, got %+v", bErr)
	}
}

func TestBuildPayloadImagesAndReasoning(t *testing.T) {
	request := &schemas.BifrostChatRequest{
		Model: "claude-sonnet-4.5",
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("what is this")},
				{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: "data:image/jpg;base64,QUJD"}},
				{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: "https://example.com/cat.png"}},
			}},
		}},
		Params: &schemas.ChatParameters{Reasoning: &schemas.ChatReasoning{Effort: schemas.Ptr("medium")}, MaxCompletionTokens: schemas.Ptr(1000)},
	}
	built, bErr := buildKiroPayload(request, testProfileArn, wireClientIDE)
	if bErr != nil {
		t.Fatalf("build: %v", bErr.Error.Message)
	}
	payload := decodePayload(t, built)
	current := payload.ConversationState.CurrentMessage.UserInputMessage
	if len(current.Images) != 1 || current.Images[0].Format != "jpeg" || current.Images[0].Source.Bytes != "QUJD" {
		t.Fatalf("unexpected images: %+v", current.Images)
	}
	if !strings.HasPrefix(current.Content, "<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>500</max_thinking_length>") ||
		!strings.HasSuffix(current.Content, "what is this\n[1 attachment omitted: remote image references are not supported by this provider]") {
		t.Fatalf("unexpected current content: %q", current.Content)
	}
	if payload.AdditionalModelRequestFields != nil {
		t.Fatal("emulated reasoning must not set additionalModelRequestFields")
	}

	request.Model = "claude-opus-5"
	request.Input = []schemas.ChatMessage{userMessage("think")}
	built, bErr = buildKiroPayload(request, testProfileArn, wireClientIDE)
	if bErr != nil {
		t.Fatalf("build: %v", bErr.Error.Message)
	}
	payload = decodePayload(t, built)
	if strings.Contains(payload.ConversationState.CurrentMessage.UserInputMessage.Content, "thinking_mode") {
		t.Fatal("native-effort models must not get emulated thinking tags")
	}
	field, _ := payload.AdditionalModelRequestFields["output_config"].(map[string]any)
	if field["effort"] != "medium" {
		t.Fatalf("native effort not set: %+v", payload.AdditionalModelRequestFields)
	}
}

func TestStreamParserTextThinkingAndTools(t *testing.T) {
	data := encodeFrames(t,
		event("messageMetadataEvent", `{"conversationId":"conv-1"}`),
		event("assistantResponseEvent", `{"content":"  <thin"}`),
		event("assistantResponseEvent", `{"content":"king>step one, "}`),
		event("assistantResponseEvent", `{"content":"step two</thin"}`),
		event("assistantResponseEvent", `{"content":"king>\n\nHello"}`),
		event("assistantResponseEvent", `{"content":" world"}`),
		event("toolUseEvent", `{"name":"read_file_ab12cd34","toolUseId":"tooluse_1","input":"{\"pa"}`),
		event("toolUseEvent", `{"name":"read_file_ab12cd34","toolUseId":"tooluse_1","input":"th\":\"/x\"}"}`),
		event("toolUseEvent", `{"name":"read_file_ab12cd34","toolUseId":"tooluse_1","stop":true}`),
		event("toolUseEvent", `{"name":"noargs","toolUseId":"tooluse_2","stop":true}`),
		event("contextUsageEvent", `{"contextUsagePercentage":1.5}`),
		event("meteringEvent", `{"unit":"credit","usage":0.3}`),
		event("someFutureEvent", `{"whatever":true}`),
		event("metadataEvent", `{"stopReason":"TOOL_USE"}`),
	)
	parser := newStreamParser("claude-sonnet-4.5", map[string]string{"read_file_ab12cd34": "read file"})
	outputs, bErr := decodeAll(t, parser, data)
	if bErr != nil {
		t.Fatalf("parse: %s", bErr.Error.Message)
	}
	if got := joinKind(outputs, outputReasoning); got != "step one, step two" {
		t.Fatalf("reasoning = %q", got)
	}
	if got := joinKind(outputs, outputText); got != "Hello world" {
		t.Fatalf("text = %q", got)
	}
	var starts []streamOutput
	args := map[int]string{}
	for _, out := range outputs {
		switch out.kind {
		case outputToolStart:
			starts = append(starts, out)
		case outputToolArgs:
			args[out.toolIndex] += out.text
		}
	}
	if len(starts) != 2 || starts[0].toolName != "read file" || starts[0].toolID != "tooluse_1" || starts[1].toolIndex != 1 {
		t.Fatalf("unexpected tool starts: %+v", starts)
	}
	if args[0] != `{"path":"/x"}` || args[1] != "{}" {
		t.Fatalf("unexpected tool args: %+v", args)
	}
	if parser.finishReason() != "tool_calls" {
		t.Fatalf("finish reason = %s", parser.finishReason())
	}
	usage := parser.usage(100)
	// 1.5% of a 200k window is 3000 tokens, which outweighs the 100-token estimate.
	if usage.CompletionTokens <= 0 || usage.PromptTokens != 3000-usage.CompletionTokens || usage.TotalTokens != 3000 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestStreamParserErrors(t *testing.T) {
	t.Run("throttling exception frame", func(t *testing.T) {
		data := encodeFrames(t, testFrame{messageType: "exception", exceptionType: "ThrottlingException", payload: `{"message":"Too many requests"}`})
		_, bErr := decodeAll(t, newStreamParser("auto", nil), data)
		if bErr == nil || *bErr.StatusCode != 429 || bErr.ExtraFields.RetryAfter != 0 {
			t.Fatalf("expected a 429 without a fabricated retry hint, got %+v", bErr)
		}
	})
	t.Run("context window exceeded", func(t *testing.T) {
		data := encodeFrames(t, event("assistantResponseEvent", `{"content":"x"}`), event("metadataEvent", `{"stopReason":"MODEL_CONTEXT_WINDOW_EXCEEDED"}`))
		_, bErr := decodeAll(t, newStreamParser("auto", nil), data)
		if bErr == nil || *bErr.StatusCode != 400 || *bErr.Error.Code != "context_length_exceeded" {
			t.Fatalf("expected context_length_exceeded, got %+v", bErr)
		}
	})
	t.Run("truncated tool input", func(t *testing.T) {
		data := encodeFrames(t, event("toolUseEvent", `{"name":"f","toolUseId":"t1","input":"{\"a\":"}`))
		_, bErr := decodeAll(t, newStreamParser("auto", nil), data)
		if bErr == nil || *bErr.StatusCode != 502 {
			t.Fatalf("expected a truncation error, got %+v", bErr)
		}
	})
	t.Run("empty stream", func(t *testing.T) {
		data := encodeFrames(t, event("metadataEvent", `{}`))
		_, bErr := decodeAll(t, newStreamParser("auto", nil), data)
		if bErr == nil || *bErr.Error.Code != "empty_kiro_stream" {
			t.Fatalf("expected empty_kiro_stream, got %+v", bErr)
		}
	})
	t.Run("max tokens", func(t *testing.T) {
		parser := newStreamParser("auto", nil)
		data := encodeFrames(t, event("assistantResponseEvent", `{"content":"partial"}`), event("metadataEvent", `{"stopReason":"MAX_TOKENS"}`))
		if _, bErr := decodeAll(t, parser, data); bErr != nil {
			t.Fatal(bErr.Error.Message)
		}
		if parser.finishReason() != "length" {
			t.Fatalf("finish reason = %s", parser.finishReason())
		}
	})
	t.Run("truncation marker", func(t *testing.T) {
		parser := newStreamParser("auto", nil)
		data := encodeFrames(t,
			event("assistantResponseEvent", `{"content":"the reason is simple"}`),
			event("assistantResponseEvent", `{"content":"cut","truncated":true}`),
		)
		outputs, bErr := decodeAll(t, parser, data)
		if bErr != nil {
			t.Fatal(bErr.Error.Message)
		}
		if joinKind(outputs, outputText) != "the reason is simple" || parser.finishReason() != "length" {
			t.Fatalf("text=%q finish=%s", joinKind(outputs, outputText), parser.finishReason())
		}
	})
}

func TestHTTPErrorClassification(t *testing.T) {
	header := &fasthttp.ResponseHeader{}
	header.Set("Retry-After", "7")
	cases := []struct {
		name      string
		status    int
		body      string
		header    *fasthttp.ResponseHeader
		wantCode  int
		wantType  string
		wantRetry int64
	}{
		{"monthly quota", 429, `{"reason":"MONTHLY_REQUEST_COUNT","message":"limit"}`, nil, 429, "insufficient_quota", 0},
		{"monthly quota on 400", 400, `{"reason":"MONTHLY_REQUEST_COUNT"}`, nil, 429, "insufficient_quota", 0},
		{"suspended", 403, `{"reason":"TEMPORARILY_SUSPENDED","message":"x"}`, nil, 403, "account_suspended", 0},
		{"rate limit with header", 429, `{"message":"slow down"}`, header, 429, "rate_limit_error", 7000},
		{"rate limit without header", 429, `{}`, nil, 429, "rate_limit_error", 0},
		{"auth", 401, `{"message":"The bearer token included in the request is invalid"}`, nil, 401, "authentication_error", 0},
		{"context", 400, `{"message":"Input is too long.","reason":"CONTENT_LENGTH_EXCEEDS_THRESHOLD"}`, nil, 400, "invalid_request_error", 0},
		{"server", 500, `oops`, nil, 500, "server_error", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bErr := newHTTPError(tc.status, []byte(tc.body), tc.header)
			if *bErr.StatusCode != tc.wantCode || *bErr.Error.Type != tc.wantType || bErr.ExtraFields.RetryAfter != tc.wantRetry || bErr.IsBifrostError {
				t.Fatalf("got status=%d type=%s retry=%d", *bErr.StatusCode, *bErr.Error.Type, bErr.ExtraFields.RetryAfter)
			}
		})
	}
	if !shouldRefreshAfter(403, []byte(`{"message":"The security token included in the request is expired"}`)) {
		t.Error("an expired-token 403 must trigger a refresh")
	}
	if shouldRefreshAfter(403, []byte(`{"reason":"TEMPORARILY_SUSPENDED","message":"token is expired"}`)) {
		t.Error("a suspension must not trigger a refresh")
	}
	if !shouldFallbackEndpoint(400, []byte(`{"__type":"UnknownOperationException"}`)) || shouldFallbackEndpoint(400, []byte(`{"message":"bad"}`)) {
		t.Error("unexpected endpoint fallback decision")
	}
}

// authServer records refresh calls and answers them.
type authServer struct {
	*httptest.Server
	mu    sync.Mutex
	calls []authCall
	reply func(path string, body map[string]any) (int, string)
}

type authCall struct {
	path string
	body map[string]any
}

func newAuthServer(t *testing.T, reply func(path string, body map[string]any) (int, string)) *authServer {
	t.Helper()
	s := &authServer{reply: reply}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.calls = append(s.calls, authCall{path: r.URL.Path, body: body})
		s.mu.Unlock()
		status, payload := s.reply(r.URL.Path, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(s.Close)
	originalSocial, originalOIDC := kiroSocialAuthBaseURL, kiroOIDCBaseURL
	kiroSocialAuthBaseURL = func(region string) string { return s.URL + "/social/" + region }
	kiroOIDCBaseURL = func(region string) string { return s.URL + "/oidc/" + region }
	t.Cleanup(func() { kiroSocialAuthBaseURL, kiroOIDCBaseURL = originalSocial, originalOIDC })
	return s
}

func (s *authServer) recorded() []authCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]authCall(nil), s.calls...)
}

func TestRefreshRouting(t *testing.T) {
	server := newAuthServer(t, func(path string, body map[string]any) (int, string) {
		if strings.HasSuffix(path, "/refreshToken") {
			return 200, `{"accessToken":"social-access","refreshToken":"social-rotated","expiresIn":1800,"profileArn":"` + testProfileArn + `"}`
		}
		if body["refreshToken"] == "dead" {
			return 400, `{"error":"invalid_grant","error_description":"revoked"}`
		}
		return 200, `{"accessToken":"oidc-access","expiresIn":3600}`
	})
	client := &fasthttp.Client{}

	grant, bErr := refreshAccessToken(context.Background(), client, &Credentials{RefreshToken: "social-refresh", Region: "eu-central-1"})
	if bErr != nil {
		t.Fatal(bErr.Error.Message)
	}
	calls := server.recorded()
	if calls[0].path != "/social/eu-central-1/refreshToken" || calls[0].body["refreshToken"] != "social-refresh" || len(calls[0].body) != 1 {
		t.Fatalf("social refresh misrouted: %+v", calls[0])
	}
	if grant.accessToken != "social-access" || grant.refreshToken != "social-rotated" || grant.profileArn != testProfileArn {
		t.Fatalf("unexpected social grant: %+v", grant)
	}
	if remaining := time.Until(grant.expiresAt); remaining < 29*time.Minute || remaining > 31*time.Minute {
		t.Fatalf("expiresIn not honoured: %v", remaining)
	}

	grant, bErr = refreshAccessToken(context.Background(), client, &Credentials{RefreshToken: "oidc-refresh", ClientID: "cid", ClientSecret: "csecret"})
	if bErr != nil {
		t.Fatal(bErr.Error.Message)
	}
	call := server.recorded()[1]
	if call.path != "/oidc/us-east-1/token" || call.body["grantType"] != "refresh_token" || call.body["clientId"] != "cid" ||
		call.body["clientSecret"] != "csecret" || call.body["refreshToken"] != "oidc-refresh" {
		t.Fatalf("OIDC refresh misrouted: %+v", call)
	}
	if grant.refreshToken != "oidc-refresh" || grant.profileArn != "" {
		t.Fatalf("an OIDC grant without a rotated token keeps the old one: %+v", grant)
	}

	_, bErr = refreshAccessToken(context.Background(), client, &Credentials{RefreshToken: "dead", ClientID: "cid", ClientSecret: "csecret"})
	if bErr == nil || *bErr.StatusCode != 401 || !isTerminalRefreshError(bErr) {
		t.Fatalf("invalid_grant must be a terminal 401, got %+v", bErr)
	}
}

// runtimeServer serves GenerateAssistantResponse with a canned event stream.
func runtimeServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body map[string]any)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		handler(w, r, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func writeEventStream(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// resetTokenCache clears the process-wide token cache so a test (and a repeated -count run) starts
// from a cold cache.
func resetTokenCache() {
	tokenPool.Clear()
	anonymousEntries.mu.Lock()
	clear(anonymousEntries.entries)
	anonymousEntries.mu.Unlock()
}

func newTestProvider(t *testing.T, baseURL string, updater schemas.KeyCredentialUpdater) *KiroProvider {
	t.Helper()
	resetTokenCache()
	t.Cleanup(resetTokenCache)
	provider, err := NewKiroProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL, AllowPrivateNetwork: true},
	}, testLogger{}, updater)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func collectChunks(t *testing.T, stream chan *schemas.BifrostStreamChunk) []*schemas.BifrostStreamChunk {
	t.Helper()
	var chunks []*schemas.BifrostStreamChunk
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case chunk, ok := <-stream:
			if !ok {
				return chunks
			}
			chunks = append(chunks, chunk)
		case <-timeout.C:
			t.Fatal("timed out waiting for the stream to close")
		}
	}
}

func cannedResponse(t *testing.T) []byte {
	return encodeFrames(t,
		event("assistantResponseEvent", `{"content":"<thinking>plan</thinking>Sure."}`),
		event("toolUseEvent", `{"name":"lookup","toolUseId":"tu-1","input":"{\"q\":"}`),
		event("toolUseEvent", `{"name":"lookup","toolUseId":"tu-1","input":"\"kiro\"}","stop":true}`),
		event("metadataEvent", `{"stopReason":"TOOL_USE"}`),
	)
}

func streamRequest() *schemas.BifrostChatRequest {
	return &schemas.BifrostChatRequest{
		Provider: schemas.Kiro,
		Model:    "claude-sonnet-4.5",
		Input:    []schemas.ChatMessage{userMessage("hi")},
		Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{
			Name: "lookup", Parameters: &schemas.ToolFunctionParameters{Type: "object"},
		}}}},
	}
}

func TestChatCompletionStream(t *testing.T) {
	var seen atomic.Int32
	server := runtimeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		seen.Add(1)
		if r.Header.Get("Authorization") != "Bearer stream-access" || r.Header.Get("X-Amz-Target") != amzTarget ||
			r.Header.Get("Content-Type") != "application/x-amz-json-1.0" || r.Header.Get("X-Amzn-Kiro-Profile-Arn") != testProfileArn ||
			r.Header.Get("Accept") != "application/vnd.amazon.eventstream" || !strings.Contains(r.Header.Get("User-Agent"), "KiroIDE-1.0.0-") {
			t.Errorf("unexpected headers: %v", r.Header)
		}
		if body["profileArn"] != testProfileArn {
			t.Errorf("profileArn missing from body: %v", body["profileArn"])
		}
		writeEventStream(w, cannedResponse(t))
	})
	provider := newTestProvider(t, server.URL, nil)
	key := testKey(t, "k-stream", Credentials{AccessToken: "stream-access", RefreshToken: "stream-refresh", ExpiresAt: futureExpiry(), ProfileArn: testProfileArn})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, key, streamRequest())
	if bErr != nil {
		t.Fatalf("stream: %s", bErr.Error.Message)
	}
	chunks := collectChunks(t, stream)
	if seen.Load() != 1 {
		t.Fatalf("expected one upstream call, got %d", seen.Load())
	}

	var role, content, reasoning, args, toolName, toolID, finish string
	var usage *schemas.BifrostLLMUsage
	for i, chunk := range chunks {
		if chunk.BifrostError != nil {
			t.Fatalf("stream error: %s", chunk.BifrostError.Error.Message)
		}
		resp := chunk.BifrostChatResponse
		if resp == nil || len(resp.Choices) != 1 {
			t.Fatalf("chunk %d is not a chat chunk", i)
		}
		if resp.ExtraFields.ChunkIndex != i || resp.Object != "chat.completion.chunk" || resp.Model != "claude-sonnet-4.5" {
			t.Fatalf("chunk %d metadata: %+v", i, resp)
		}
		delta := resp.Choices[0].Delta
		if delta.Role != nil {
			role = *delta.Role
		}
		if delta.Content != nil {
			content += *delta.Content
		}
		if delta.Reasoning != nil {
			reasoning += *delta.Reasoning
		}
		for _, call := range delta.ToolCalls {
			if call.Index != 0 {
				t.Fatalf("unexpected tool index %d", call.Index)
			}
			if call.ID != nil {
				toolID = *call.ID
			}
			if call.Function.Name != nil {
				toolName = *call.Function.Name
			}
			args += call.Function.Arguments
		}
		if resp.Choices[0].FinishReason != nil {
			finish = *resp.Choices[0].FinishReason
			usage = resp.Usage
		}
	}
	if role != "assistant" || content != "Sure." || reasoning != "plan" {
		t.Fatalf("role=%q content=%q reasoning=%q", role, content, reasoning)
	}
	if toolID != "tu-1" || toolName != "lookup" || args != `{"q":"kiro"}` {
		t.Fatalf("tool call id=%q name=%q args=%q", toolID, toolName, args)
	}
	if finish != "tool_calls" || usage == nil || usage.PromptTokens <= 0 || usage.CompletionTokens <= 0 ||
		usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens {
		t.Fatalf("finish=%q usage=%+v", finish, usage)
	}
	if last := chunks[len(chunks)-1].BifrostChatResponse; last.Choices[0].FinishReason == nil {
		t.Fatal("the final chunk must carry the finish reason")
	}
}

func TestChatCompletionDrainsStream(t *testing.T) {
	server := runtimeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		writeEventStream(w, cannedResponse(t))
	})
	provider := newTestProvider(t, server.URL, nil)
	key := testKey(t, "k-unary", Credentials{AccessToken: "unary-access", RefreshToken: "unary-refresh", ExpiresAt: futureExpiry(), ProfileArn: testProfileArn})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bErr := provider.ChatCompletion(ctx, key, streamRequest())
	if bErr != nil {
		t.Fatalf("chat: %s", bErr.Error.Message)
	}
	choice := resp.Choices[0]
	msg := choice.Message
	if *choice.FinishReason != "tool_calls" || msg.Content == nil || *msg.Content.ContentStr != "Sure." || *msg.Reasoning != "plan" {
		t.Fatalf("unexpected message: %+v", msg)
	}
	if len(msg.ToolCalls) != 1 || *msg.ToolCalls[0].ID != "tu-1" || msg.ToolCalls[0].Function.Arguments != `{"q":"kiro"}` {
		t.Fatalf("unexpected tool calls: %+v", msg.ToolCalls)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens == 0 || resp.Object != "chat.completion" {
		t.Fatalf("unexpected response envelope: %+v", resp)
	}
}

func TestResponsesStreamFallback(t *testing.T) {
	server := runtimeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		writeEventStream(w, encodeFrames(t, event("assistantResponseEvent", `{"content":"Hello"}`)))
	})
	provider := newTestProvider(t, server.URL, nil)
	key := testKey(t, "k-responses", Credentials{AccessToken: "responses-access", RefreshToken: "responses-refresh", ExpiresAt: futureExpiry(), ProfileArn: testProfileArn})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.Kiro,
		Model:    "claude-sonnet-4.5",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
	stream, bErr := provider.ResponsesStream(ctx, passthroughPostHook, nil, key, request)
	if bErr != nil {
		t.Fatalf("responses stream: %s", bErr.Error.Message)
	}
	var types []schemas.ResponsesStreamResponseType
	for _, chunk := range collectChunks(t, stream) {
		if chunk.BifrostResponsesStreamResponse == nil {
			t.Fatalf("expected Responses events, got %+v", chunk)
		}
		types = append(types, chunk.BifrostResponsesStreamResponse.Type)
	}
	if len(types) < 3 || types[0] != schemas.ResponsesStreamResponseTypeCreated || types[len(types)-1] != schemas.ResponsesStreamResponseTypeCompleted {
		t.Fatalf("unexpected event sequence: %v", types)
	}
}

func TestUnauthorizedTriggersRefreshAndPersistsRotation(t *testing.T) {
	auth := newAuthServer(t, func(path string, body map[string]any) (int, string) {
		return 200, `{"accessToken":"fresh-access","refreshToken":"rotated-refresh","expiresIn":3600}`
	})
	var calls atomic.Int32
	runtime := runtimeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer fresh-access" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"message":"The bearer token included in the request is invalid."}`)
			return
		}
		writeEventStream(w, encodeFrames(t, event("assistantResponseEvent", `{"content":"ok"}`)))
	})
	var persistedID, persistedValue string
	updater := func(_ context.Context, provider schemas.ModelProvider, keyID, value string) error {
		if provider != schemas.Kiro {
			t.Errorf("updater called for %s", provider)
		}
		persistedID, persistedValue = keyID, value
		return nil
	}
	provider := newTestProvider(t, runtime.URL, updater)
	key := testKey(t, "k-rotate", Credentials{AccessToken: "stale-access", RefreshToken: "original-refresh", ExpiresAt: futureExpiry(), ProfileArn: testProfileArn})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{Model: "auto", Input: []schemas.ChatMessage{userMessage("hi")}})
	if bErr != nil {
		t.Fatalf("chat: %s", bErr.Error.Message)
	}
	if *resp.Choices[0].Message.Content.ContentStr != "ok" || calls.Load() != 2 || len(auth.recorded()) != 1 {
		t.Fatalf("expected one 401, one refresh and one retry; runtime calls=%d refreshes=%d", calls.Load(), len(auth.recorded()))
	}
	if persistedID != "k-rotate" {
		t.Fatalf("rotated credential not persisted (id=%q)", persistedID)
	}
	persisted, err := ParseCredentials(persistedValue)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.RefreshToken != "rotated-refresh" || persisted.AccessToken != "fresh-access" || persisted.ProfileArn != testProfileArn {
		t.Fatalf("unexpected persisted credential: %+v", persisted)
	}
	entry, ok := tokenPool.Load("k-rotate")
	if !ok || entry.(*tokenEntry).session.Load().accessToken != "fresh-access" || !entry.(*tokenEntry).knows(credentialHash(persistedValue)) {
		t.Fatal("the persisted value must join the key's lineage with the refreshed session")
	}

	// The persisted key reuses the cached token without another refresh.
	persistedKey := key
	persistedKey.Value = *schemas.NewSecretVar(persistedValue)
	if _, bErr := provider.ChatCompletion(ctx, persistedKey, &schemas.BifrostChatRequest{Model: "auto", Input: []schemas.ChatMessage{userMessage("again")}}); bErr != nil {
		t.Fatalf("second chat: %s", bErr.Error.Message)
	}
	if len(auth.recorded()) != 1 {
		t.Fatalf("expected no further refresh, got %d", len(auth.recorded()))
	}
}

func TestExpiredAccessTokenRefreshesOnceAndCachesFailure(t *testing.T) {
	auth := newAuthServer(t, func(path string, body map[string]any) (int, string) {
		return 400, `{"error":"invalid_grant"}`
	})
	provider := newTestProvider(t, "http://127.0.0.1:1", nil)
	key := testKey(t, "k-dead", Credentials{AccessToken: "old", RefreshToken: "dead-refresh", ExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	for range 3 {
		_, bErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{Model: "auto", Input: []schemas.ChatMessage{userMessage("hi")}})
		if bErr == nil || *bErr.StatusCode != 401 {
			t.Fatalf("expected a 401, got %+v", bErr)
		}
	}
	if n := len(auth.recorded()); n != 1 {
		t.Fatalf("a terminal refresh failure must be negative-cached, got %d refreshes", n)
	}
}

func TestEndpointFallback(t *testing.T) {
	var primaryCalls atomic.Int32
	primary := runtimeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		primaryCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"message":"unavailable"}`)
	})
	legacy := runtimeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		writeEventStream(w, encodeFrames(t, event("assistantResponseEvent", `{"content":"from legacy"}`)))
	})
	originalRuntime, originalLegacy := kiroRuntimeURL, kiroLegacyURL
	kiroRuntimeURL = func(string) string { return primary.URL + "/" }
	kiroLegacyURL = func(string) string { return legacy.URL + "/" }
	t.Cleanup(func() { kiroRuntimeURL, kiroLegacyURL = originalRuntime, originalLegacy })

	provider := newTestProvider(t, "", nil)
	key := testKey(t, "k-fallback", Credentials{AccessToken: "fb-access", RefreshToken: "fb-refresh", ExpiresAt: futureExpiry(), ProfileArn: testProfileArn})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, key, &schemas.BifrostChatRequest{Model: "auto", Input: []schemas.ChatMessage{userMessage("hi")}})
	if bErr != nil {
		t.Fatalf("stream: %s", bErr.Error.Message)
	}
	var content string
	for _, chunk := range collectChunks(t, stream) {
		if chunk.BifrostError != nil {
			t.Fatalf("stream error: %s", chunk.BifrostError.Error.Message)
		}
		if delta := chunk.BifrostChatResponse.Choices[0].Delta; delta.Content != nil {
			content += *delta.Content
		}
	}
	if content != "from legacy" || primaryCalls.Load() != 1 {
		t.Fatalf("content=%q primary calls=%d", content, primaryCalls.Load())
	}
}

func TestBuilderIDUsesCLIWire(t *testing.T) {
	server := runtimeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Header.Get("Accept") != "*/*" || !strings.Contains(r.Header.Get("User-Agent"), "app/AmazonQ-For-CLI") ||
			r.Header.Get("X-Amzn-Kiro-Profile-Arn") != builderIDServiceProfileArn || r.Header.Get("Amz-Sdk-Request") != "attempt=1; max=3" {
			t.Errorf("unexpected CLI headers: %v", r.Header)
		}
		state := body["conversationState"].(map[string]any)
		if state["agentTaskType"] != "vibe" || body["profileArn"] != builderIDServiceProfileArn {
			t.Errorf("unexpected CLI body: %v", body)
		}
		writeEventStream(w, encodeFrames(t, event("assistantResponseEvent", `{"content":"cli"}`)))
	})
	provider := newTestProvider(t, server.URL, nil)
	key := testKey(t, "k-builder", Credentials{AccessToken: "b-access", RefreshToken: "b-refresh", ExpiresAt: futureExpiry(), ClientID: "cid", ClientSecret: "sec", AuthMethod: "builder-id"})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, bErr := provider.ChatCompletion(ctx, key, &schemas.BifrostChatRequest{Model: "auto", Input: []schemas.ChatMessage{userMessage("hi")}}); bErr != nil {
		t.Fatalf("chat: %s", bErr.Error.Message)
	}
}

func TestDeviceLoginFlows(t *testing.T) {
	var polls atomic.Int32
	newAuthServer(t, func(path string, body map[string]any) (int, string) {
		switch path {
		case "/social/us-east-1/oauth/device/authorization":
			if body["clientId"] != "kiro-cli" || body["loginProvider"] != "Github" {
				t.Errorf("unexpected social authorization body: %v", body)
			}
			return 200, `{"deviceCode":"dev-1","userCode":"ABCD-1234","verificationUri":"https://app.kiro.dev/device","verificationUriComplete":"https://app.kiro.dev/device?code=ABCD-1234","expiresInMilliseconds":300000,"intervalInMilliseconds":500}`
		case "/social/us-east-1/oauth/device/poll":
			if polls.Add(1) == 1 {
				return 200, `{"status":"authorization_pending"}`
			}
			return 200, `{"accessToken":"g-access","refreshToken":"g-refresh","expiresIn":3600,"profileArn":"` + testProfileArn + `"}`
		case "/oidc/us-east-1/client/register":
			if body["clientName"] != "kiro-cli" || body["clientType"] != "public" {
				t.Errorf("unexpected register body: %v", body)
			}
			return 200, `{"clientId":"reg-id","clientSecret":"reg-secret"}`
		case "/oidc/us-east-1/device_authorization":
			if body["clientId"] != "reg-id" || body["startUrl"] != builderIDStartURL {
				t.Errorf("unexpected device authorization body: %v", body)
			}
			return 200, `{"deviceCode":"dev-2","userCode":"WXYZ-9876","verificationUri":"https://device.sso.us-east-1.amazonaws.com/","expiresIn":600,"interval":5}`
		case "/oidc/us-east-1/token":
			switch polls.Add(1) {
			case 3:
				return 400, `{"error":"authorization_pending"}`
			case 4:
				return 400, `{"error":"slow_down"}`
			}
			if body["grantType"] != "urn:ietf:params:oauth:grant-type:device_code" || body["deviceCode"] != "dev-2" {
				t.Errorf("unexpected token body: %v", body)
			}
			return 200, `{"accessToken":"b-access","refreshToken":"b-refresh","expiresIn":3600}`
		}
		t.Errorf("unexpected path %s", path)
		return 404, `{}`
	})
	ctx := context.Background()

	auth, err := StartDeviceLogin(ctx, "github")
	if err != nil {
		t.Fatal(err)
	}
	if auth.UserCode != "ABCD-1234" || auth.Interval != time.Second || time.Until(auth.ExpiresAt) < 4*time.Minute {
		t.Fatalf("unexpected social authorization: %+v", auth)
	}
	if _, status, err := PollDeviceLogin(ctx, auth); err != nil || status != "pending" {
		t.Fatalf("expected pending, got %q %v", status, err)
	}
	creds, status, err := PollDeviceLogin(ctx, auth)
	if err != nil || status != "complete" || creds.ProfileArn != testProfileArn || creds.RefreshToken != "g-refresh" || creds.usesOIDC() {
		t.Fatalf("unexpected social completion: %q %v %+v", status, err, creds)
	}

	auth, err = StartDeviceLogin(ctx, "builder-id")
	if err != nil {
		t.Fatal(err)
	}
	if auth.ClientID != "reg-id" || auth.ClientSecret != "reg-secret" || auth.Interval != 5*time.Second {
		t.Fatalf("unexpected builder-id authorization: %+v", auth)
	}
	for _, want := range []string{"pending", "slow_down"} {
		if _, status, err := PollDeviceLogin(ctx, auth); err != nil || status != want {
			t.Fatalf("expected %s, got %q %v", want, status, err)
		}
	}
	creds, status, err = PollDeviceLogin(ctx, auth)
	if err != nil || status != "complete" || creds.ClientID != "reg-id" || creds.AuthMethod != "builder-id" || creds.ProfileArn != "" {
		t.Fatalf("unexpected builder-id completion: %q %v %+v", status, err, creds)
	}
	if _, err := ParseCredentials(mustEncode(t, creds)); err != nil {
		t.Fatalf("device-login credential must round trip: %v", err)
	}

	if _, err := StartDeviceLogin(ctx, "facebook"); err == nil {
		t.Fatal("unknown method must fail")
	}
	expired := &DeviceAuthorization{Method: "google", DeviceCode: "d", ExpiresAt: time.Now().Add(-time.Second)}
	if _, status, err := PollDeviceLogin(ctx, expired); err != nil || status != "expired" {
		t.Fatalf("expected expired, got %q %v", status, err)
	}
}

func mustEncode(t *testing.T, creds *Credentials) string {
	t.Helper()
	encoded, err := creds.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestThinkTagParserPassesPlainText(t *testing.T) {
	var parser thinkTagParser
	var outputs []streamOutput
	for _, chunk := range []string{"\n", "<th", "e answer", " is 42"} {
		outputs = append(outputs, parser.feed(chunk)...)
	}
	outputs = append(outputs, parser.flush()...)
	if got := joinKind(outputs, outputText); got != "\n<the answer is 42" || joinKind(outputs, outputReasoning) != "" {
		t.Fatalf("plain text altered: %q", got)
	}
}

func TestStreamExceptionBeforeOutputIsFirstChunk(t *testing.T) {
	server := runtimeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		writeEventStream(w, encodeFrames(t,
			event("messageMetadataEvent", `{"conversationId":"c"}`),
			testFrame{messageType: "exception", exceptionType: "ThrottlingException", payload: `{"message":"Too many requests"}`},
		))
	})
	provider := newTestProvider(t, server.URL, nil)
	key := testKey(t, "k-throttled", Credentials{AccessToken: "t-access", RefreshToken: "t-refresh", ExpiresAt: futureExpiry(), ProfileArn: testProfileArn})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, key, &schemas.BifrostChatRequest{Model: "auto", Input: []schemas.ChatMessage{userMessage("hi")}})
	if bErr != nil {
		t.Fatalf("stream: %s", bErr.Error.Message)
	}
	// Core surfaces only the first chunk as a synchronous, rotatable error.
	_, drained, firstErr := providerUtils.CheckFirstStreamChunkForError(ctx, stream)
	<-drained
	if firstErr == nil || firstErr.StatusCode == nil || *firstErr.StatusCode != 429 || firstErr.IsBifrostError {
		t.Fatalf("the throttling exception must be the first chunk, got %+v", firstErr)
	}
}

func TestStreamFirstOutputCarriesRole(t *testing.T) {
	server := runtimeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		writeEventStream(w, encodeFrames(t, event("assistantResponseEvent", `{"content":"Hi"}`)))
	})
	provider := newTestProvider(t, server.URL, nil)
	key := testKey(t, "k-role", Credentials{AccessToken: "r-access", RefreshToken: "r-refresh", ExpiresAt: futureExpiry(), ProfileArn: testProfileArn})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, key, &schemas.BifrostChatRequest{Model: "auto", Input: []schemas.ChatMessage{userMessage("hi")}})
	if bErr != nil {
		t.Fatalf("stream: %s", bErr.Error.Message)
	}
	chunks := collectChunks(t, stream)
	if len(chunks) != 2 {
		t.Fatalf("expected a content chunk and a final chunk, got %d", len(chunks))
	}
	first := chunks[0].BifrostChatResponse.Choices[0].Delta
	if first.Role == nil || *first.Role != "assistant" || first.Content == nil || *first.Content != "Hi" {
		t.Fatalf("the first chunk must carry the role together with the first output: %+v", first)
	}
	if chunks[1].BifrostChatResponse.Choices[0].Delta.Role != nil {
		t.Fatal("the role must be sent once")
	}
}

func TestFrameReaderDetectsTruncation(t *testing.T) {
	frame := encodeFrames(t, event("assistantResponseEvent", `{"content":"hello"}`))
	for cut := 1; cut < len(frame); cut++ {
		data := append(append([]byte(nil), frame...), frame[:cut]...)
		reader := &frameReader{r: bytes.NewReader(data)}
		decoder := eventstream.NewDecoder()
		if _, err := reader.decode(decoder, nil); err != nil {
			t.Fatalf("cut %d: first frame: %v", cut, err)
		}
		if _, err := reader.decode(decoder, nil); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("cut %d: expected io.ErrUnexpectedEOF, got %v", cut, err)
		}
	}
	reader := &frameReader{r: bytes.NewReader(frame)}
	decoder := eventstream.NewDecoder()
	if _, err := reader.decode(decoder, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.decode(decoder, nil); err != io.EOF {
		t.Fatalf("a clean end must be io.EOF, got %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestFrameReaderCapsFrameSize(t *testing.T) {
	prelude := make([]byte, 12)
	binary.BigEndian.PutUint32(prelude[0:4], 64<<20)
	binary.BigEndian.PutUint32(prelude[4:8], 0)
	binary.BigEndian.PutUint32(prelude[8:12], crc32.ChecksumIEEE(prelude[:8]))
	reader := &frameReader{r: io.MultiReader(bytes.NewReader(prelude), zeroReader{})}
	if _, err := reader.decode(eventstream.NewDecoder(), nil); !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("expected errFrameTooLarge, got %v", err)
	}
}

func TestTruncatedEventStreamIsAnError(t *testing.T) {
	full := encodeFrames(t, event("assistantResponseEvent", `{"content":"partial answer"}`), event("assistantResponseEvent", `{"content":" and more"}`))
	firstLen := len(encodeFrames(t, event("assistantResponseEvent", `{"content":"partial answer"}`)))
	cut := full[:firstLen+20]
	server := runtimeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		writeEventStream(w, cut)
	})
	provider := newTestProvider(t, server.URL, nil)
	key := testKey(t, "k-truncated", Credentials{AccessToken: "tr-access", RefreshToken: "tr-refresh", ExpiresAt: futureExpiry(), ProfileArn: testProfileArn})
	request := &schemas.BifrostChatRequest{Model: "auto", Input: []schemas.ChatMessage{userMessage("hi")}}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, bErr := provider.ChatCompletion(ctx, key, request); bErr == nil || *bErr.StatusCode != 502 || *bErr.Error.Code != "stream_truncated" {
		t.Fatalf("a truncated non-stream body must fail, got %+v", bErr)
	}

	ctx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, key, request)
	if bErr != nil {
		t.Fatalf("stream: %s", bErr.Error.Message)
	}
	chunks := collectChunks(t, stream)
	last := chunks[len(chunks)-1]
	if last.BifrostError == nil || last.BifrostError.Error.Message != schemas.ErrProviderStreamTruncated {
		t.Fatalf("a truncated stream must end with the truncation error, got %+v", last)
	}
	for _, chunk := range chunks {
		if resp := chunk.BifrostChatResponse; resp != nil && resp.Choices[0].FinishReason != nil {
			t.Fatal("a truncated stream must not be finished with a clean final chunk")
		}
	}
}

func TestParseCredentialsDoesNotEchoSecrets(t *testing.T) {
	_, err := ParseCredentials(`{"refreshToken":"SECRET-aorAAAA123","accessToken":"SECRET-aoa",`)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("the parse error must not quote the credential: %v", err)
	}
}

func TestOperatorReplacedCredentialResetsCache(t *testing.T) {
	auth := newAuthServer(t, func(path string, body map[string]any) (int, string) {
		return 200, `{"accessToken":"fresh-` + body["refreshToken"].(string) + `","expiresIn":3600}`
	})
	var mu sync.Mutex
	var seen []string
	runtime := runtimeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		writeEventStream(w, encodeFrames(t, event("assistantResponseEvent", `{"content":"ok"}`)))
	})
	provider := newTestProvider(t, runtime.URL, nil)
	request := &schemas.BifrostChatRequest{Model: "auto", Input: []schemas.ChatMessage{userMessage("hi")}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	original := testKey(t, "k-operator", Credentials{AccessToken: "a-access", RefreshToken: "a-refresh", ExpiresAt: futureExpiry(), ProfileArn: testProfileArn})
	if _, bErr := provider.ChatCompletion(ctx, original, request); bErr != nil {
		t.Fatal(bErr.Error.Message)
	}
	replaced := testKey(t, "k-operator", Credentials{RefreshToken: "c-refresh", ProfileArn: testProfileArn})
	if _, bErr := provider.ChatCompletion(ctx, replaced, request); bErr != nil {
		t.Fatal(bErr.Error.Message)
	}
	calls := auth.recorded()
	if len(calls) != 1 || calls[0].body["refreshToken"] != "c-refresh" {
		t.Fatalf("the replaced credential must be refreshed with its own refresh token: %+v", calls)
	}
	if len(seen) != 2 || seen[0] != "Bearer a-access" || seen[1] != "Bearer fresh-c-refresh" {
		t.Fatalf("unexpected tokens on the wire: %v", seen)
	}
	entry, _ := tokenPool.Load("k-operator")
	if lineage := entry.(*tokenEntry).lineage.Load(); lineage.generation != 1 || len(lineage.hashes) != 1 {
		t.Fatalf("the replacement must start a new lineage: %+v", lineage)
	}
}

func TestPersistSkipsReplacedLineage(t *testing.T) {
	var calls atomic.Int32
	source := &tokenSource{logger: testLogger{}, updater: func(context.Context, schemas.ModelProvider, string, string) error {
		calls.Add(1)
		return nil
	}}
	entry := &tokenEntry{}
	entry.adopt("old")
	entry.adopt("operator-new")
	sess := &kiroSession{accessToken: "a", expiresAt: time.Now().Add(time.Hour), creds: &Credentials{RefreshToken: "rotated"}}
	source.persist(context.Background(), "k", entry, 0, sess)
	if calls.Load() != 0 {
		t.Fatal("a refresh of a replaced credential must not be persisted over the new one")
	}
	source.persist(context.Background(), "k", entry, 1, sess)
	if calls.Load() != 1 {
		t.Fatal("a refresh of the current credential must be persisted")
	}
}

func TestAnonymousEntriesAreBounded(t *testing.T) {
	for i := range maxAnonymousEntries + 16 {
		entryFor("", "anonymous-"+strconv.Itoa(i))
	}
	anonymousEntries.mu.Lock()
	defer anonymousEntries.mu.Unlock()
	if len(anonymousEntries.entries) > maxAnonymousEntries {
		t.Fatalf("anonymous entries grew to %d", len(anonymousEntries.entries))
	}
}

func TestResponsesStreamRawFieldPlacement(t *testing.T) {
	server := runtimeServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		writeEventStream(w, encodeFrames(t, event("assistantResponseEvent", `{"content":"Hello"}`)))
	})
	provider, err := NewKiroProvider(&schemas.ProviderConfig{
		NetworkConfig:       schemas.NetworkConfig{BaseURL: server.URL, AllowPrivateNetwork: true},
		SendBackRawRequest:  true,
		SendBackRawResponse: true,
	}, testLogger{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := testKey(t, "k-raw", Credentials{AccessToken: "raw-access", RefreshToken: "raw-refresh", ExpiresAt: futureExpiry(), ProfileArn: testProfileArn})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.Kiro,
		Model:    "auto",
		Input:    []schemas.ResponsesMessage{{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")}}},
	}
	stream, bErr := provider.ResponsesStream(ctx, passthroughPostHook, nil, key, request)
	if bErr != nil {
		t.Fatalf("responses stream: %s", bErr.Error.Message)
	}
	rawResponses, rawRequests := 0, 0
	for _, chunk := range collectChunks(t, stream) {
		event := chunk.BifrostResponsesStreamResponse
		if event == nil {
			t.Fatalf("expected Responses events, got %+v", chunk)
		}
		if event.ExtraFields.RawResponse != nil {
			rawResponses++
		}
		if event.ExtraFields.RawRequest != nil {
			rawRequests++
			if event.Type != schemas.ResponsesStreamResponseTypeCompleted {
				t.Fatalf("raw request on a non-terminal event %s", event.Type)
			}
		}
	}
	if rawResponses != 1 || rawRequests != 1 {
		t.Fatalf("expected one raw response and one raw request, got %d and %d", rawResponses, rawRequests)
	}
}
