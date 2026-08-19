package complexity

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
)

type complexityHarness uint8

const (
	complexityHarnessUnknown complexityHarness = iota
	complexityHarnessClaudeCode
	complexityHarnessCodex
)

type complexityTextKind uint8

const (
	complexityTextInvalid complexityTextKind = iota
	complexityTextHuman
	// Context-only text can be ignored while an earlier human turn remains the
	// routing subject for the current request.
	complexityTextContextOnly
	// Housekeeping text drives an internal request. When it is the newest
	// relevant user turn, complexity routing must not inherit an older prompt.
	complexityTextHousekeeping
)

type complexityTagPair struct {
	open  string
	close string
}

var (
	// Claude Code emits these protocol wrappers as user-role text. Keep this
	// list client-gated and explicit: arbitrary XML-like user text is valid.
	claudeContextTags = [...]complexityTagPair{
		{open: "<system-reminder>", close: "</system-reminder>"},
	}
	claudeHousekeepingTags = [...]complexityTagPair{
		{open: "<local-command-caveat>", close: "</local-command-caveat>"},
		{open: "<local-command-stdout>", close: "</local-command-stdout>"},
		{open: "<local-command-stderr>", close: "</local-command-stderr>"},
		{open: "<command-name>", close: "</command-name>"},
		{open: "<command-message>", close: "</command-message>"},
		{open: "<command-args>", close: "</command-args>"},
	}
	// These fixed pairs mirror Codex contextual user fragments. Dynamic
	// <external_*> fragments are intentionally excluded to avoid stripping
	// arbitrary caller-owned XML.
	codexContextTags = [...]complexityTagPair{
		{open: "# AGENTS.md instructions", close: "</INSTRUCTIONS>"},
		{open: "<environment_context>", close: "</environment_context>"},
		{open: "<skill>", close: "</skill>"},
		{open: "<turn_aborted>", close: "</turn_aborted>"},
		{open: "<subagent_notification>", close: "</subagent_notification>"},
		{open: "<codex_internal_context", close: "</codex_internal_context>"},
		{open: "<goal_context>", close: "</goal_context>"},
		{open: "<recommended_plugins>", close: "</recommended_plugins>"},
	}
	codexHousekeepingTags = [...]complexityTagPair{
		{open: "<user_shell_command>", close: "</user_shell_command>"},
	}
)

const (
	codexTurnMetadataHeader      = "x-codex-turn-metadata"
	claudeCodeSessionIDHeader    = "x-claude-code-session-id"
	maxComplexitySessionIDLength = 255
	claudeSessionEnvelopeOpen    = "<session>"
	claudeResumeRecapPrefix      = "The user stepped away and is coming back."
)

type codexTurnMetadata struct {
	RequestKind string
	SessionID   string
}

// InputDisposition describes how one request participates in session-aware
// complexity routing.
type InputDisposition uint8

const (
	// InputBypass means the operation is unsupported or explicitly belongs to a
	// harness background workload. It neither classifies nor refreshes a session.
	InputBypass InputDisposition = iota
	// InputContinuation means a supported conversational request contains no new
	// classifiable human text. It may reuse existing session state but cannot
	// create or escalate it.
	InputContinuation
	// InputClassifiable means the request contains human-authored text that may
	// initialize or escalate a session tier.
	InputClassifiable
)

// BuildInput extracts text from normalized BifrostRequest values for
// complexity_tier routing. It preserves the original boolean contract while
// BuildInputWithDisposition exposes the finer session-aware outcome.
func BuildInput(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (ComplexityInput, bool) {
	input, disposition := BuildInputWithDisposition(ctx, req)
	return input, disposition == InputClassifiable
}

// BuildInputWithDisposition extracts normalized classifier input and reports
// whether the request should be classified, should reuse existing session state,
// or should bypass session handling. Extraction runs after provider-specific
// transport conversion, so routing never reparses raw request payloads.
func BuildInputWithDisposition(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (ComplexityInput, InputDisposition) {
	if req == nil {
		return ComplexityInput{}, InputBypass
	}

	harness := detectComplexityHarness(ctx)
	if harness == complexityHarnessCodex && isCodexBackgroundRequest(ctx) {
		return ComplexityInput{}, InputBypass
	}

	switch req.RequestType {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		if req.ChatRequest == nil {
			return ComplexityInput{}, InputBypass
		}
		input, ok := extractFromChatMessages(req.ChatRequest.Input, harness)
		if !ok {
			return ComplexityInput{}, InputContinuation
		}
		if chatHasTrailingContinuation(req.ChatRequest.Input, harness) {
			return ComplexityInput{}, InputContinuation
		}
		return input, InputClassifiable
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		if req.TextCompletionRequest == nil {
			return ComplexityInput{}, InputBypass
		}
		input, ok := extractFromTextCompletionRequest(req.TextCompletionRequest)
		if !ok {
			return ComplexityInput{}, InputBypass
		}
		return input, InputClassifiable
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		if req.ResponsesRequest == nil {
			return ComplexityInput{}, InputBypass
		}
		input, ok := extractFromResponsesRequest(req.ResponsesRequest, harness)
		if !ok {
			return ComplexityInput{}, InputContinuation
		}
		if responsesHasTrailingContinuation(req.ResponsesRequest.Input, harness) {
			return ComplexityInput{}, InputContinuation
		}
		return input, InputClassifiable
	default:
		return ComplexityInput{}, InputBypass
	}
}

// chatHasTrailingContinuation distinguishes a fresh human turn from a tool or
// assistant continuation that merely replays an older human message in the
// request history. Context-only harness fragments and system instructions do
// not change which conversational actor came last.
func chatHasTrailingContinuation(messages []schemas.ChatMessage, harness complexityHarness) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		switch msg.Role {
		case schemas.ChatMessageRoleSystem, schemas.ChatMessageRoleDeveloper:
			continue
		case schemas.ChatMessageRoleUser:
			text, ok := extractChatTextOnly(msg.Content)
			if !ok {
				return true
			}
			_, kind := sanitizeUserText(text, harness)
			if kind == complexityTextContextOnly || kind == complexityTextInvalid {
				continue
			}
			return kind != complexityTextHuman
		default:
			return true
		}
	}
	return false
}

// responsesHasTrailingContinuation is the Responses-API counterpart. Items
// without a role are tool, reasoning, or provider-native history items; when
// one follows the latest user message, this is a continuation rather than a
// new human turn.
func responsesHasTrailingContinuation(messages []schemas.ResponsesMessage, harness complexityHarness) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == nil {
			return true
		}
		switch *msg.Role {
		case schemas.ResponsesInputMessageRoleSystem, schemas.ResponsesInputMessageRoleDeveloper:
			continue
		case schemas.ResponsesInputMessageRoleUser:
			text, ok := extractResponsesTextOnly(msg.Content)
			if !ok {
				return true
			}
			_, kind := sanitizeUserText(text, harness)
			if kind == complexityTextContextOnly || kind == complexityTextInvalid {
				continue
			}
			return kind != complexityTextHuman
		default:
			return true
		}
	}
	return false
}

// extractFromChatMessages builds a complexity input from chat messages by
// preserving system/developer context and tracking only text-only user turns.
func extractFromChatMessages(messages []schemas.ChatMessage, harness complexityHarness) (ComplexityInput, bool) {
	if len(messages) == 0 {
		return ComplexityInput{}, false
	}

	var input ComplexityInput
	var userTexts []string
	lastRelevantUserKind := complexityTextInvalid

	for _, msg := range messages {
		switch msg.Role {
		case schemas.ChatMessageRoleSystem, schemas.ChatMessageRoleDeveloper:
			input.SystemText = appendText(input.SystemText, sanitizeSystemText(extractChatText(msg.Content), harness))
		case schemas.ChatMessageRoleUser:
			text, ok := extractChatTextOnly(msg.Content)
			if !ok {
				return ComplexityInput{}, false
			}
			text, kind := sanitizeUserText(text, harness)
			switch kind {
			case complexityTextHuman:
				userTexts = append(userTexts, text)
				lastRelevantUserKind = kind
			case complexityTextContextOnly:
				continue
			case complexityTextHousekeeping:
				lastRelevantUserKind = kind
			default:
				return ComplexityInput{}, false
			}
		}
	}

	if lastRelevantUserKind == complexityTextHousekeeping || len(userTexts) == 0 {
		return ComplexityInput{}, false
	}

	input.LastUserText = userTexts[len(userTexts)-1]
	if len(userTexts) > 1 {
		input.PriorUserTexts = userTexts[:len(userTexts)-1]
	}
	return input, true
}

// extractFromTextCompletionRequest builds a complexity input from a single text
// completion prompt and deliberately skips batched prompt arrays.
func extractFromTextCompletionRequest(req *schemas.BifrostTextCompletionRequest) (ComplexityInput, bool) {
	if req == nil || req.Input == nil || req.Input.PromptStr == nil || strings.TrimSpace(*req.Input.PromptStr) == "" {
		return ComplexityInput{}, false
	}

	// PromptArray represents batched completions, not one logical prompt. Do not
	// synthesize a single routing input by joining unrelated batch entries.
	return ComplexityInput{LastUserText: *req.Input.PromptStr}, true
}

// extractFromResponsesRequest builds a complexity input from Responses API
// messages while combining instructions with system/developer message text.
func extractFromResponsesRequest(req *schemas.BifrostResponsesRequest, harness complexityHarness) (ComplexityInput, bool) {
	if req == nil || len(req.Input) == 0 {
		return ComplexityInput{}, false
	}

	var input ComplexityInput
	if req.Params != nil && req.Params.Instructions != nil {
		input.SystemText = sanitizeSystemText(*req.Params.Instructions, harness)
	}

	var userTexts []string
	lastRelevantUserKind := complexityTextInvalid
	for _, msg := range req.Input {
		if msg.Role == nil {
			continue
		}

		switch *msg.Role {
		case schemas.ResponsesInputMessageRoleSystem, schemas.ResponsesInputMessageRoleDeveloper:
			input.SystemText = appendText(input.SystemText, sanitizeSystemText(extractResponsesText(msg.Content), harness))
		case schemas.ResponsesInputMessageRoleUser:
			text, ok := extractResponsesTextOnly(msg.Content)
			if !ok {
				return ComplexityInput{}, false
			}
			text, kind := sanitizeUserText(text, harness)
			switch kind {
			case complexityTextHuman:
				userTexts = append(userTexts, text)
				lastRelevantUserKind = kind
			case complexityTextContextOnly:
				continue
			case complexityTextHousekeeping:
				lastRelevantUserKind = kind
			default:
				return ComplexityInput{}, false
			}
		}
	}

	if lastRelevantUserKind == complexityTextHousekeeping || len(userTexts) == 0 {
		return ComplexityInput{}, false
	}

	input.LastUserText = userTexts[len(userTexts)-1]
	if len(userTexts) > 1 {
		input.PriorUserTexts = userTexts[:len(userTexts)-1]
	}
	return input, true
}

// extractChatText returns the text portions of chat content and ignores
// non-text blocks so system/developer context can still be used.
func extractChatText(content *schemas.ChatMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return *content.ContentStr
	}

	var text string
	for _, block := range content.ContentBlocks {
		if isChatTextBlock(block) && block.Text != nil && *block.Text != "" {
			text = appendText(text, *block.Text)
		}
	}
	return text
}

// extractChatTextOnly returns the text portion of a user chat turn, ignoring
// non-text blocks (images, files, audio) so mixed-modality prompts still feed
// their text into complexity routing. It succeeds whenever at least one non-empty
// text block is present; it returns false only when there is no usable text at
// all. The analyzer self-gates on lexical signal, so a text-light turn with no
// signal (e.g. "what's this?" beside an image) still yields no tier and falls
// through to default routing.
func extractChatTextOnly(content *schemas.ChatMessageContent) (string, bool) {
	if content == nil {
		return "", false
	}
	if content.ContentStr != nil {
		return *content.ContentStr, true
	}

	var text string
	for _, block := range content.ContentBlocks {
		if isChatTextBlock(block) && block.Text != nil && *block.Text != "" {
			text = appendText(text, *block.Text)
		}
	}
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	return text, true
}

// extractResponsesText returns the text portions of Responses content and
// ignores non-input-text blocks used by non-user context.
func extractResponsesText(content *schemas.ResponsesMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return *content.ContentStr
	}

	var text string
	for _, block := range content.ContentBlocks {
		if isResponsesInputTextBlock(block) && block.Text != nil && *block.Text != "" {
			text = appendText(text, *block.Text)
		}
	}
	return text
}

// extractResponsesTextOnly returns the text portion of a user Responses turn,
// ignoring non-text blocks (images, files, audio) so mixed-modality requests
// still feed their text into complexity routing. It succeeds whenever at least
// one non-empty input-text block is present, mirroring extractChatTextOnly.
func extractResponsesTextOnly(content *schemas.ResponsesMessageContent) (string, bool) {
	if content == nil {
		return "", false
	}
	if content.ContentStr != nil {
		return *content.ContentStr, true
	}

	var text string
	for _, block := range content.ContentBlocks {
		if isResponsesInputTextBlock(block) && block.Text != nil && *block.Text != "" {
			text = appendText(text, *block.Text)
		}
	}
	if strings.TrimSpace(text) == "" {
		return "", false
	}
	return text, true
}

// isChatTextBlock reports whether a chat content block is plain text, treating
// an empty type as text for compatibility with normalized request payloads.
func isChatTextBlock(block schemas.ChatContentBlock) bool {
	return block.Type == "" || block.Type == schemas.ChatContentBlockTypeText
}

// isResponsesInputTextBlock reports whether a Responses content block is plain
// text, treating an empty type as text for compatibility with normalized input.
// output_text is accepted alongside input_text because the Anthropic→Responses
// conversion tags user text blocks as output_text for bedrock/-prefixed models
// (keepToolsGrouped path); rejecting them would silently disable complexity
// routing for those clients.
func isResponsesInputTextBlock(block schemas.ResponsesMessageContentBlock) bool {
	return block.Type == "" ||
		block.Type == schemas.ResponsesInputMessageContentBlockTypeText ||
		block.Type == schemas.ResponsesOutputMessageContentTypeText
}

func detectComplexityHarness(ctx *schemas.BifrostContext) complexityHarness {
	if ctx == nil {
		return complexityHarnessUnknown
	}

	userAgent, _ := ctx.Value(schemas.BifrostContextKeyUserAgent).(string)
	if strings.TrimSpace(userAgent) == "" {
		headers, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
		userAgent = headers["user-agent"]
	}

	switch {
	case schemas.ClaudeCLI.Matches(userAgent):
		return complexityHarnessClaudeCode
	case schemas.CodexCLI.Matches(userAgent), schemas.CodexDesktop.Matches(userAgent):
		return complexityHarnessCodex
	default:
		return complexityHarnessUnknown
	}
}

// ResolveComplexitySessionID resolves the trusted session identity used only by
// complexity routing. An explicit x-bf-session-id context value wins; otherwise
// native harness metadata is accepted only when the User-Agent identifies the
// corresponding Claude Code or Codex client.
//
// Native identities are not copied into BifrostContextKeySessionID because that
// key also enables core provider-key stickiness, which is a separate feature.
func ResolveComplexitySessionID(ctx *schemas.BifrostContext) (string, bool) {
	if ctx == nil {
		return "", false
	}
	if explicit, exists := ctx.Value(schemas.BifrostContextKeySessionID).(string); exists {
		return normalizeComplexitySessionID(explicit)
	}

	headers, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	switch detectComplexityHarness(ctx) {
	case complexityHarnessClaudeCode:
		return normalizeComplexitySessionID(headers[claudeCodeSessionIDHeader])
	case complexityHarnessCodex:
		metadata, ok := parseCodexTurnMetadata(ctx)
		if !ok {
			return "", false
		}
		return normalizeComplexitySessionID(metadata.SessionID)
	default:
		return "", false
	}
}

func normalizeComplexitySessionID(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > maxComplexitySessionIDLength ||
		!utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return "", false
	}
	return value, true
}

func isCodexBackgroundRequest(ctx *schemas.BifrostContext) bool {
	metadata, ok := parseCodexTurnMetadata(ctx)
	if !ok {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(metadata.RequestKind)) {
	case "prewarm", "compaction", "memory":
		return true
	default:
		return false
	}
}

// parseCodexTurnMetadata decodes the structured metadata emitted by Codex.
// Missing or structurally malformed metadata is unavailable rather than an
// error so callers can preserve the existing fail-open request behavior.
func parseCodexTurnMetadata(ctx *schemas.BifrostContext) (codexTurnMetadata, bool) {
	if ctx == nil {
		return codexTurnMetadata{}, false
	}
	headers, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	rawMetadata := strings.TrimSpace(headers[codexTurnMetadataHeader])
	if rawMetadata == "" {
		return codexTurnMetadata{}, false
	}

	var fields struct {
		RequestKind json.RawMessage `json:"request_kind"`
		SessionID   json.RawMessage `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(rawMetadata), &fields); err != nil {
		return codexTurnMetadata{}, false
	}

	// Decode fields independently so an invalid new field cannot change the
	// established behavior of another one.
	var metadata codexTurnMetadata
	if len(fields.RequestKind) > 0 {
		_ = json.Unmarshal(fields.RequestKind, &metadata.RequestKind)
	}
	if len(fields.SessionID) > 0 {
		_ = json.Unmarshal(fields.SessionID, &metadata.SessionID)
	}
	return metadata, true
}

func sanitizeUserText(text string, harness complexityHarness) (string, complexityTextKind) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", complexityTextContextOnly
	}

	switch harness {
	case complexityHarnessClaudeCode:
		if isClaudeCodeHousekeepingText(text) {
			return "", complexityTextHousekeeping
		}
		cleaned, removedContext := stripComplexityTags(text, claudeContextTags[:])
		cleaned, removedHousekeeping := stripComplexityTags(cleaned, claudeHousekeepingTags[:])
		return classifySanitizedText(cleaned, removedContext, removedHousekeeping)
	case complexityHarnessCodex:
		cleaned, removedContext := stripComplexityTags(text, codexContextTags[:])
		cleaned, removedHousekeeping := stripComplexityTags(cleaned, codexHousekeepingTags[:])
		return classifySanitizedText(cleaned, removedContext, removedHousekeeping)
	default:
		return text, complexityTextHuman
	}
}

// isClaudeCodeHousekeepingText recognizes complete user-role messages Claude
// Code injects for background session maintenance. These are not new human
// intent and therefore must not initialize or escalate session complexity.
// Detection is deliberately prefix-based and Claude-client-gated so a human
// request that merely mentions a session XML tag remains classifiable.
func isClaudeCodeHousekeepingText(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, claudeSessionEnvelopeOpen) ||
		strings.HasPrefix(text, claudeResumeRecapPrefix)
}

func sanitizeSystemText(text string, harness complexityHarness) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	switch harness {
	case complexityHarnessClaudeCode:
		text, _ = stripComplexityTags(text, claudeContextTags[:])
		text, _ = stripComplexityTags(text, claudeHousekeepingTags[:])
	case complexityHarnessCodex:
		text, _ = stripComplexityTags(text, codexContextTags[:])
		text, _ = stripComplexityTags(text, codexHousekeepingTags[:])
	}
	return strings.TrimSpace(text)
}

func classifySanitizedText(text string, removedContext, removedHousekeeping bool) (string, complexityTextKind) {
	text = strings.TrimSpace(text)
	if text != "" {
		return text, complexityTextHuman
	}
	if removedHousekeeping {
		return "", complexityTextHousekeeping
	}
	if removedContext {
		return "", complexityTextContextOnly
	}
	return "", complexityTextInvalid
}

func stripComplexityTags(text string, tags []complexityTagPair) (string, bool) {
	removedAny := false
	for _, tag := range tags {
		var cleaned strings.Builder
		remaining := text
		removedTag := false

		// write appends a fragment, inserting one space when dropping a tag would
		// otherwise merge the words that surrounded it.
		write := func(fragment string) {
			if fragment == "" {
				return
			}
			if written := cleaned.String(); written != "" && !endsWithSpace(written) && !startsWithSpace(fragment) {
				cleaned.WriteByte(' ')
			}
			cleaned.WriteString(fragment)
		}

		for {
			start := strings.Index(remaining, tag.open)
			if start < 0 {
				write(remaining)
				break
			}

			afterOpen := remaining[start+len(tag.open):]
			closeOffset := strings.Index(afterOpen, tag.close)
			if closeOffset < 0 {
				// Preserve malformed/unclosed input instead of guessing where a
				// client-owned fragment ends.
				write(remaining)
				break
			}

			write(remaining[:start])
			remaining = afterOpen[closeOffset+len(tag.close):]
			removedTag = true
		}

		if removedTag {
			text = cleaned.String()
			removedAny = true
		}
	}
	return strings.TrimSpace(text), removedAny
}

func startsWithSpace(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsSpace(r)
}

func endsWithSpace(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return unicode.IsSpace(r)
}

// appendText joins adjacent text fragments with one separating space while
// preserving empty existing or next values.
func appendText(existing, next string) string {
	if next == "" {
		return existing
	}
	if existing == "" {
		return next
	}
	return existing + " " + next
}
