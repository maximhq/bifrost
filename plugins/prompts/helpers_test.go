package prompts

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	tables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
)

// ============================================================
// MockLogger — captures log output per level for assertions.
// Follows the same pattern as plugins/governance/test_utils.go.
// ============================================================

type MockLogger struct {
	mu       sync.Mutex
	debugs   []string
	infos    []string
	warnings []string
	errors   []string
}

func NewMockLogger() *MockLogger {
	return &MockLogger{
		debugs:   make([]string, 0),
		infos:    make([]string, 0),
		warnings: make([]string, 0),
		errors:   make([]string, 0),
	}
}

func (l *MockLogger) Debug(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.debugs = append(l.debugs, format)
}

func (l *MockLogger) Info(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos = append(l.infos, format)
}

func (l *MockLogger) Warn(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warnings = append(l.warnings, format)
}

func (l *MockLogger) Error(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, format)
}

func (l *MockLogger) Fatal(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, format)
}

func (l *MockLogger) SetLevel(_ schemas.LogLevel)              {}
func (l *MockLogger) SetOutputType(_ schemas.LoggerOutputType) {}
func (l *MockLogger) LogHTTPRequest(_ schemas.LogLevel, _ string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// Warned returns true if at least one warning was logged.
func (l *MockLogger) Warned() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.warnings) > 0
}

// ============================================================
// staticResolver — returns fixed IDs; decouples PreLLMHook
// tests from HTTP header / context mechanics.
// ============================================================

type staticResolver struct {
	promptID      string
	versionNumber int
	err           error
}

func (r *staticResolver) Resolve(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) (string, int, error) {
	return r.promptID, r.versionNumber, r.err
}

// ============================================================
// Plugin builders
// ============================================================

// newTestPlugin builds a Plugin over a live store loaded with the prompts and versions the maps
// hold, bypassing Init.
func newTestPlugin(resolver PromptResolver, promptMap map[string]*tables.TablePrompt, versionMap map[string]map[int]*tables.TablePromptVersion) *Plugin {
	return newTestPluginWithLogger(resolver, promptMap, versionMap, NewMockLogger())
}

// newTestPluginWithLogger is like newTestPlugin but accepts a caller-provided logger
// so tests can inspect logged warnings.
func newTestPluginWithLogger(resolver PromptResolver, promptMap map[string]*tables.TablePrompt, versionMap map[string]map[int]*tables.TablePromptVersion, log schemas.Logger) *Plugin {
	var prompts []tables.TablePrompt
	for _, prompt := range promptMap {
		if prompt != nil {
			prompts = append(prompts, *prompt)
		}
	}
	var versions []tables.TablePromptVersion
	for _, byNumber := range versionMap {
		for _, version := range byNumber {
			if version != nil {
				versions = append(versions, *version)
			}
		}
	}
	return newPluginWithRows(resolver, prompts, versions, log)
}

// newPluginWithRows builds a Plugin over a live store holding prompts and versions as rows load
// into it. A prompt's latest version is the one its LatestVersion names, added when versions lack
// it; version record IDs are assigned afresh, since tests reuse version numbers across prompts.
func newPluginWithRows(resolver PromptResolver, prompts []tables.TablePrompt, versions []tables.TablePromptVersion, log schemas.Logger) *Plugin {
	if resolver == nil {
		resolver = &staticResolver{}
	}
	live, err := livestore.New()
	if err != nil {
		panic(err)
	}
	cols := live.Collections
	latest := make(map[string]int)
	var recs []livestore.Record
	for _, prompt := range prompts {
		recs = append(recs, livestore.Rec(cols.Prompts, &registry.Prompt{ID: prompt.ID, Name: prompt.Name}))
		if prompt.LatestVersion != nil {
			latest[prompt.ID] = prompt.LatestVersion.VersionNumber
			versions = append(versions, *prompt.LatestVersion)
		}
	}
	seen := make(map[string]bool)
	var nextID uint
	for _, version := range versions {
		key := registry.PromptVersionKey(version.PromptID, version.VersionNumber)
		if seen[key] {
			continue
		}
		seen[key] = true
		nextID++
		version.ID = nextID
		number, ok := latest[version.PromptID]
		version.IsLatest = ok && number == version.VersionNumber
		recs = append(recs, livestore.Rec(cols.PromptVersions, &registry.PromptVersion{TablePromptVersion: version}))
	}
	if err := live.LoadRecords(context.Background(), recs); err != nil {
		panic(err)
	}
	return &Plugin{live: live, logger: log, resolver: resolver}
}

// ============================================================
// Message builders
// ============================================================

// versionMsg creates a TablePromptVersionMessage in the production envelope
// format {"payload": <chat_message_json>}, matching what the frontend writes
// to the DB and what AfterFind populates into the Message field.
func versionMsg(role schemas.ChatMessageRole, text string) tables.TablePromptVersionMessage {
	content := text
	inner := schemas.ChatMessage{
		Role:    role,
		Content: &schemas.ChatMessageContent{ContentStr: &content},
	}
	innerJSON, err := json.Marshal(inner)
	if err != nil {
		panic(fmt.Sprintf("versionMsg: marshal inner failed: %v", err))
	}
	envelope := fmt.Sprintf(`{"payload":%s}`, string(innerJSON))
	return tables.TablePromptVersionMessage{
		Message: tables.PromptMessage(envelope),
	}
}

// versionMsgViaJSON creates a TablePromptVersionMessage that has an empty Message
// field but a populated MessageJSON field, exercising the fallback branch in
// chatMessagesFromVersionMessages.
func versionMsgViaJSON(role schemas.ChatMessageRole, text string) tables.TablePromptVersionMessage {
	content := text
	inner := schemas.ChatMessage{
		Role:    role,
		Content: &schemas.ChatMessageContent{ContentStr: &content},
	}
	innerJSON, err := json.Marshal(inner)
	if err != nil {
		panic(fmt.Sprintf("versionMsgViaJSON: marshal failed: %v", err))
	}
	envelope := fmt.Sprintf(`{"payload":%s}`, string(innerJSON))
	return tables.TablePromptVersionMessage{
		Message:     nil, // empty — triggers MessageJSON fallback
		MessageJSON: envelope,
	}
}

// makeVersion returns a TablePromptVersion with the supplied messages.
// VersionNumber is set to int(id) so tests can reference versions by their number.
func makeVersion(id uint, promptID string, isLatest bool, msgs ...tables.TablePromptVersionMessage) tables.TablePromptVersion {
	return tables.TablePromptVersion{
		ID:            id,
		PromptID:      promptID,
		IsLatest:      isLatest,
		VersionNumber: int(id),
		Messages:      msgs,
	}
}

// makePrompt returns a TablePrompt, optionally linked to a latest version.
func makePrompt(id string, latest *tables.TablePromptVersion) tables.TablePrompt {
	return tables.TablePrompt{ID: id, Name: id, LatestVersion: latest}
}

// ============================================================
// Request / context builders
// ============================================================

// chatRequest returns a BifrostRequest wrapping a ChatRequest with the given messages.
func chatRequest(msgs ...schemas.ChatMessage) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		ChatRequest: &schemas.BifrostChatRequest{
			Input: append([]schemas.ChatMessage{}, msgs...),
		},
	}
}

// userMsg returns a user-role ChatMessage with plain text content.
func userMsg(text string) schemas.ChatMessage {
	t := text
	return schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{ContentStr: &t},
	}
}

// systemMsg returns a system-role ChatMessage with plain text content.
func systemMsg(text string) schemas.ChatMessage {
	t := text
	return schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleSystem,
		Content: &schemas.ChatMessageContent{ContentStr: &t},
	}
}

// bfCtx returns a fresh BifrostContext with no deadline.
func bfCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

// versionMsgWithToolCall creates a TablePromptVersionMessage for an assistant
// message that contains a single tool call (role=assistant, tool_calls=[...]).
func versionMsgWithToolCall(callID, funcName, funcArgs string) tables.TablePromptVersionMessage {
	name := funcName
	id := callID
	inner := schemas.ChatMessage{
		Role: schemas.ChatMessageRoleAssistant,
		ChatAssistantMessage: &schemas.ChatAssistantMessage{
			ToolCalls: []schemas.ChatAssistantMessageToolCall{
				{
					ID: &id,
					Function: schemas.ChatAssistantMessageToolCallFunction{
						Name:      &name,
						Arguments: funcArgs,
					},
				},
			},
		},
	}
	innerJSON, err := json.Marshal(inner)
	if err != nil {
		panic(fmt.Sprintf("versionMsgWithToolCall: marshal failed: %v", err))
	}
	envelope := fmt.Sprintf(`{"payload":%s}`, string(innerJSON))
	return tables.TablePromptVersionMessage{
		Message: tables.PromptMessage(envelope),
	}
}

// versionMsgToolResult creates a TablePromptVersionMessage for a tool-result
// message (role=tool) with the given tool_call_id and result text.
func versionMsgToolResult(callID, result string) tables.TablePromptVersionMessage {
	id := callID
	inner := schemas.ChatMessage{
		Role:            schemas.ChatMessageRoleTool,
		Content:         &schemas.ChatMessageContent{ContentStr: &result},
		ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: &id},
	}
	innerJSON, err := json.Marshal(inner)
	if err != nil {
		panic(fmt.Sprintf("versionMsgToolResult: marshal failed: %v", err))
	}
	envelope := fmt.Sprintf(`{"payload":%s}`, string(innerJSON))
	return tables.TablePromptVersionMessage{
		Message: tables.PromptMessage(envelope),
	}
}

// makeVersionWithParams returns a TablePromptVersion with explicit ModelParams and messages.
// VersionNumber is set to int(id) so tests can reference versions by their number.
func makeVersionWithParams(id uint, promptID string, isLatest bool, params tables.ModelParams, msgs ...tables.TablePromptVersionMessage) tables.TablePromptVersion {
	return tables.TablePromptVersion{
		ID:            id,
		PromptID:      promptID,
		IsLatest:      isLatest,
		VersionNumber: int(id),
		ModelParams:   params,
		Messages:      msgs,
	}
}

// chatRequestWithParams returns a BifrostRequest with Params pre-set.
func chatRequestWithParams(params *schemas.ChatParameters, msgs ...schemas.ChatMessage) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		ChatRequest: &schemas.BifrostChatRequest{
			Input:  append([]schemas.ChatMessage{}, msgs...),
			Params: params,
		},
	}
}

// chatRequestWithModel returns a BifrostRequest with the Model field pre-set.
func chatRequestWithModel(model string, msgs ...schemas.ChatMessage) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		ChatRequest: &schemas.BifrostChatRequest{
			Model: model,
			Input: append([]schemas.ChatMessage{}, msgs...),
		},
	}
}

// versionMsgAssistantUIFormat creates a TablePromptVersionMessage in the format
// the Bifrost UI writes for assistant (completion_result) messages.
// The message is nested at payload.choices[0].message, matching SerializedMessage.
func versionMsgAssistantUIFormat(text string) tables.TablePromptVersionMessage {
	content := text
	inner := schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleAssistant,
		Content: &schemas.ChatMessageContent{ContentStr: &content},
	}
	innerJSON, err := json.Marshal(inner)
	if err != nil {
		panic(fmt.Sprintf("versionMsgAssistantUIFormat: marshal failed: %v", err))
	}
	payload := fmt.Sprintf(`{"id":"resp-1","choices":[{"index":0,"message":%s,"finish_reason":"stop"}]}`, string(innerJSON))
	envelope := fmt.Sprintf(`{"originalType":"completion_result","payload":%s}`, payload)
	return tables.TablePromptVersionMessage{
		Message: tables.PromptMessage(envelope),
	}
}

// ============================================================
// errTest — minimal error type for test use
// ============================================================

type errTest string

func (e errTest) Error() string { return string(e) }

// ============================================================
// Assertion helpers
// ============================================================

// msgText extracts the ContentStr from a ChatMessage, returning "" if absent.
func msgText(msg schemas.ChatMessage) string {
	if msg.Content == nil || msg.Content.ContentStr == nil {
		return ""
	}
	return *msg.Content.ContentStr
}
