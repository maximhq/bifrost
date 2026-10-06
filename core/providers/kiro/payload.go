package kiro

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// Proxy-authored turn texts. They keep the conversation structurally valid for Kiro, which
// requires strictly alternating, non-empty turns ending with a user turn.
const (
	continuationMessage     = "Continue from the prior conversation. Do not quote or mention this instruction."
	emptyToolResultMessage  = "The tool completed without textual output."
	toolResultCarrierMesage = "The requested tool result is attached."
)

const (
	maxToolCount             = 48
	maxToolCatalogBytes      = 96_000
	maxToolDescription       = 1024
	maxToolDescriptionGPTSol = 9_216
	maxImagesPerMessage      = 20
	maxImagesPerRequest      = 100
	maxImageBase64Budget     = 18 * 1024 * 1024
	defaultMaxOutputTokens   = 4096

	originIDE = "AI_EDITOR"
	originCLI = "KIRO_CLI"
)

var (
	toolNamePattern    = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	toolNameUnsafeChar = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
)

// rejectedSchemaKeys are JSON Schema keywords Kiro's tool-spec validator refuses. They are
// advisory for the model, so dropping them does not change tool behaviour.
var rejectedSchemaKeys = map[string]bool{
	"additionalProperties": true, "pattern": true, "format": true, "minLength": true, "maxLength": true,
	"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true, "multipleOf": true,
	"minItems": true, "maxItems": true, "uniqueItems": true, "minProperties": true, "maxProperties": true,
	"contentEncoding": true, "contentMediaType": true, "$schema": true, "patternProperties": true,
	"propertyNames": true, "dependentSchemas": true, "dependentRequired": true, "if": true, "then": true,
	"else": true, "contains": true, "unevaluatedProperties": true, "unevaluatedItems": true, "encrypted": true,
}

// schemaMapKeys hold name->schema maps whose child keys are names, never keywords.
var schemaMapKeys = map[string]bool{"properties": true, "$defs": true, "definitions": true}

// thinkingBudgetRatios maps a reasoning effort to the share of max output tokens given to the
// emulated thinking budget.
var thinkingBudgetRatios = map[string]float64{
	"minimal": 0.10, "low": 0.20, "medium": 0.50, "high": 0.80, "xhigh": 0.90, "max": 0.95,
}

// Wire types for the GenerateAssistantResponse request.
type kiroPayload struct {
	ConversationState            kiroConversationState `json:"conversationState"`
	ProfileArn                   string                `json:"profileArn,omitempty"`
	AdditionalModelRequestFields map[string]any        `json:"additionalModelRequestFields,omitempty"`
}

type kiroConversationState struct {
	ChatTriggerType     string             `json:"chatTriggerType"`
	AgentContinuationID string             `json:"agentContinuationId,omitempty"`
	AgentTaskType       string             `json:"agentTaskType,omitempty"`
	ConversationID      string             `json:"conversationId"`
	CurrentMessage      kiroCurrentMessage `json:"currentMessage"`
	History             []kiroHistoryEntry `json:"history,omitempty"`
}

type kiroCurrentMessage struct {
	UserInputMessage *kiroUserInputMessage `json:"userInputMessage"`
}

type kiroHistoryEntry struct {
	UserInputMessage         *kiroUserInputMessage `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *kiroAssistantMessage `json:"assistantResponseMessage,omitempty"`
}

type kiroUserInputMessage struct {
	Content                 string                `json:"content"`
	ModelID                 string                `json:"modelId"`
	Origin                  string                `json:"origin"`
	Images                  []kiroImage           `json:"images,omitempty"`
	UserInputMessageContext *kiroUserInputContext `json:"userInputMessageContext,omitempty"`
}

type kiroUserInputContext struct {
	Tools       []kiroTool       `json:"tools,omitempty"`
	ToolResults []kiroToolResult `json:"toolResults,omitempty"`
}

type kiroTool struct {
	ToolSpecification kiroToolSpecification `json:"toolSpecification"`
}

type kiroToolSpecification struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema kiroInputSchema `json:"inputSchema"`
}

type kiroInputSchema struct {
	JSON *schemas.OrderedMap `json:"json"`
}

type kiroToolResult struct {
	Content   []kiroText `json:"content"`
	Status    string     `json:"status"`
	ToolUseID string     `json:"toolUseId"`
}

type kiroText struct {
	Text string `json:"text"`
}

type kiroAssistantMessage struct {
	Content  string        `json:"content"`
	ToolUses []kiroToolUse `json:"toolUses,omitempty"`
}

type kiroToolUse struct {
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"toolUseId"`
}

type kiroImage struct {
	Format string          `json:"format"`
	Source kiroImageSource `json:"source"`
}

type kiroImageSource struct {
	Bytes string `json:"bytes"`
}

// kiroWireClient selects the header profile and origin.
type kiroWireClient int

const (
	wireClientIDE kiroWireClient = iota
	wireClientCLI
)

// builtPayload is a serialized request plus what the response side needs to decode it.
type builtPayload struct {
	body        []byte
	modelID     string
	nameMap     map[string]string // kiro tool name -> caller tool name
	inputTokens int
}

// turn is one merged conversation turn before conversion to wire entries.
type turn struct {
	assistant   bool
	content     string
	images      []kiroImage
	toolResults []kiroToolResult
	toolUses    []kiroToolUse
}

// toolNameRegistry keeps one collision domain for advertised tools and replayed tool calls, and
// remembers how to restore the caller's names on responses.
type toolNameRegistry struct {
	used       map[string]bool
	wireToKiro map[string]string
	nameMap    map[string]string
}

func newToolNameRegistry() *toolNameRegistry {
	return &toolNameRegistry{used: map[string]bool{}, wireToKiro: map[string]string{}, nameMap: map[string]string{}}
}

// alias returns the Kiro-safe name for a caller tool name. Conforming names pass through;
// anything rewritten, too long, empty or colliding becomes a 55-char prefix plus 8 hex chars of
// sha256 of the original name, salted until unique.
func (r *toolNameRegistry) alias(name string) string {
	if existing, ok := r.wireToKiro[name]; ok {
		return existing
	}
	alias := kiroToolName(name, r.used)
	r.wireToKiro[name] = alias
	if alias != name {
		r.nameMap[alias] = name
	}
	return alias
}

func kiroToolName(name string, used map[string]bool) string {
	cleaned := toolNameUnsafeChar.ReplaceAllString(name, "_")
	if cleaned == name && toolNamePattern.MatchString(cleaned) && !used[cleaned] {
		used[cleaned] = true
		return cleaned
	}
	base := cleaned
	if len(base) > 55 {
		base = base[:55]
	}
	if base == "" {
		base = "tool"
	}
	for salt := 0; ; salt++ {
		hashInput := name
		if salt > 0 {
			hashInput = name + "#" + strconv.Itoa(salt)
		}
		sum := sha256.Sum256([]byte(hashInput))
		candidate := base + "_" + hex.EncodeToString(sum[:])[:8]
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
	}
}

// normalizeToolID applies the CodeWhisperer toolUseId constraint ^[a-zA-Z0-9_-]{1,64}$.
func normalizeToolID(id string) string {
	s := toolNameUnsafeChar.ReplaceAllString(id, "_")
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// buildKiroPayload converts a Bifrost chat request into a Kiro conversationState request.
// Errors are caller faults (400).
func buildKiroPayload(request *schemas.BifrostChatRequest, profileArn string, client kiroWireClient) (*builtPayload, *schemas.BifrostError) {
	if request == nil {
		return nil, providerUtils.NewBifrostBadRequestError("kiro: chat request is empty")
	}
	params := request.Params
	if params == nil {
		params = &schemas.ChatParameters{}
	}
	if bErr := validateCapabilities(params); bErr != nil {
		return nil, bErr
	}

	modelID := normalizeKiroModelId(request.Model)
	if modelID == "" {
		return nil, providerUtils.NewBifrostBadRequestError("kiro: model is required")
	}
	origin := originIDE
	if client == wireClientCLI {
		origin = originCLI
	}

	registry := newToolNameRegistry()
	tools, systemAdditions, bErr := convertTools(params, modelID, registry)
	if bErr != nil {
		return nil, bErr
	}

	var systemParts []string
	var turns []*turn
	pushUser := func(content string, images []kiroImage, results []kiroToolResult) {
		if n := len(turns); n > 0 && !turns[n-1].assistant {
			last := turns[n-1]
			last.content = appendTurnText(last.content, content)
			last.images = append(last.images, images...)
			last.toolResults = append(last.toolResults, results...)
			return
		}
		turns = append(turns, &turn{content: content, images: images, toolResults: results})
	}
	pushAssistant := func(content string, uses []kiroToolUse) {
		if n := len(turns); n > 0 && turns[n-1].assistant {
			last := turns[n-1]
			last.content = appendTurnText(last.content, content)
			last.toolUses = append(last.toolUses, uses...)
			return
		}
		turns = append(turns, &turn{assistant: true, content: content, toolUses: uses})
	}

	priorCalls := map[string]string{} // normalized tool use id -> raw tool call id
	var adjacent *kiroToolResult      // the last tool result, for merging same-id neighbours
	adjacentRawID := ""

	for i := range request.Input {
		msg := &request.Input[i]
		if msg.Role != schemas.ChatMessageRoleTool {
			adjacent = nil
		}
		switch msg.Role {
		case schemas.ChatMessageRoleSystem, schemas.ChatMessageRoleDeveloper:
			if text := strings.TrimSpace(contentText(msg.Content, "\n")); text != "" {
				systemParts = append(systemParts, text)
			}
		case schemas.ChatMessageRoleUser:
			text, images := userContent(msg.Content)
			pushUser(text, images, nil)
		case schemas.ChatMessageRoleAssistant:
			text := contentText(msg.Content, "")
			var uses []kiroToolUse
			if msg.ChatAssistantMessage != nil {
				for _, call := range msg.ChatAssistantMessage.ToolCalls {
					rawID := ""
					if call.ID != nil {
						rawID = *call.ID
					}
					toolUseID := normalizeToolID(rawID)
					if toolUseID == "" {
						return nil, providerUtils.NewBifrostBadRequestError("kiro: conversation contains a tool call with an empty id")
					}
					if _, dup := priorCalls[toolUseID]; dup {
						return nil, providerUtils.NewBifrostBadRequestError(fmt.Sprintf("kiro: conversation contains duplicate tool call id %q", rawID))
					}
					priorCalls[toolUseID] = rawID
					name := ""
					if call.Function.Name != nil {
						name = *call.Function.Name
					}
					uses = append(uses, kiroToolUse{
						Name:      registry.alias(name),
						Input:     toolInputObject(call.Function.Arguments),
						ToolUseID: toolUseID,
					})
				}
			}
			if strings.TrimSpace(text) == "" && len(uses) == 0 {
				// A reasoning-only or empty assistant message carries no turn Kiro can accept.
				continue
			}
			pushAssistant(text, uses)
		case schemas.ChatMessageRoleTool:
			rawID := ""
			isError := false
			if msg.ChatToolMessage != nil {
				if msg.ChatToolMessage.ToolCallID != nil {
					rawID = *msg.ChatToolMessage.ToolCallID
				}
				isError = msg.ChatToolMessage.IsError != nil && *msg.ChatToolMessage.IsError
			}
			toolUseID := normalizeToolID(rawID)
			if prior, ok := priorCalls[toolUseID]; !ok || prior != rawID {
				return nil, providerUtils.NewBifrostBadRequestError(fmt.Sprintf("kiro: conversation contains a tool result for unknown tool call %q", rawID))
			}
			text, images := userContent(msg.Content)
			if adjacent != nil && adjacentRawID == rawID && len(turns) > 0 && !turns[len(turns)-1].assistant {
				if strings.TrimSpace(text) != "" {
					if len(adjacent.Content) == 1 && adjacent.Content[0].Text == emptyToolResultMessage {
						adjacent.Content = adjacent.Content[:0]
					}
					adjacent.Content = append(adjacent.Content, kiroText{Text: text})
				}
				if isError {
					adjacent.Status = "error"
				}
				turns[len(turns)-1].images = append(turns[len(turns)-1].images, images...)
				continue
			}
			if strings.TrimSpace(text) == "" {
				text = emptyToolResultMessage
			}
			status := "success"
			if isError {
				status = "error"
			}
			pushUser("", images, []kiroToolResult{{Content: []kiroText{{Text: text}}, Status: status, ToolUseID: toolUseID}})
			last := turns[len(turns)-1]
			adjacent = &last.toolResults[len(last.toolResults)-1]
			adjacentRawID = rawID
		}
	}

	if len(turns) == 0 || turns[0].assistant {
		turns = append([]*turn{{content: continuationMessage}}, turns...)
	}
	if turns[len(turns)-1].assistant {
		turns = append(turns, &turn{content: continuationMessage})
	}
	for _, t := range turns {
		if !t.assistant && strings.TrimSpace(t.content) == "" && len(t.toolResults) > 0 {
			t.content = toolResultCarrierMesage
		}
	}
	applyImageCaps(turns)

	current := turns[len(turns)-1]
	historyTurns := turns[:len(turns)-1]
	toUser := func(t *turn) *kiroUserInputMessage {
		uim := &kiroUserInputMessage{Content: t.content, ModelID: modelID, Origin: origin, Images: t.images}
		if len(t.toolResults) > 0 {
			uim.UserInputMessageContext = &kiroUserInputContext{ToolResults: t.toolResults}
		}
		return uim
	}
	history := make([]kiroHistoryEntry, 0, len(historyTurns))
	for _, t := range historyTurns {
		if t.assistant {
			history = append(history, kiroHistoryEntry{AssistantResponseMessage: &kiroAssistantMessage{Content: t.content, ToolUses: t.toolUses}})
		} else {
			history = append(history, kiroHistoryEntry{UserInputMessage: toUser(t)})
		}
	}
	currentUIM := toUser(current)

	systemParts = append(systemParts, systemAdditions...)
	if len(systemParts) > 0 {
		prefix := strings.Join(systemParts, "\n\n") + "\n\n"
		placed := false
		for _, entry := range history {
			if entry.UserInputMessage != nil {
				entry.UserInputMessage.Content = prefix + entry.UserInputMessage.Content
				placed = true
				break
			}
		}
		if !placed {
			currentUIM.Content = prefix + currentUIM.Content
		}
	}
	if len(tools) > 0 {
		if currentUIM.UserInputMessageContext == nil {
			currentUIM.UserInputMessageContext = &kiroUserInputContext{}
		}
		currentUIM.UserInputMessageContext.Tools = tools
	}

	effort, budget := reasoningSettings(params)
	nativeField := ""
	if effort != "" {
		nativeField = kiroNativeEffortField(request.Model, effort)
		if nativeField != "" && !kiroNativeEfforts[effort] {
			return nil, providerUtils.NewBifrostBadRequestError(fmt.Sprintf("kiro: model %s does not support reasoning effort %q", modelID, effort))
		}
	}
	if nativeField == "" && budget > 0 && len(current.toolResults) == 0 && current.content != continuationMessage {
		currentUIM.Content = injectThinkingTags(currentUIM.Content, budget)
	}

	if bErr := validateConversation(history, currentUIM); bErr != nil {
		return nil, bErr
	}

	payload := &kiroPayload{
		ConversationState: kiroConversationState{
			ChatTriggerType: "MANUAL",
			ConversationID:  uuid.NewString(),
			CurrentMessage:  kiroCurrentMessage{UserInputMessage: currentUIM},
			History:         history,
		},
		ProfileArn: profileArn,
	}
	if client == wireClientCLI {
		payload.ConversationState.AgentContinuationID = uuid.NewString()
		payload.ConversationState.AgentTaskType = "vibe"
	}
	if nativeField != "" {
		payload.AdditionalModelRequestFields = map[string]any{nativeField: map[string]string{"effort": effort}}
	}

	body, err := sonic.Marshal(payload)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
	}
	return &builtPayload{
		body:        body,
		modelID:     modelID,
		nameMap:     registry.nameMap,
		inputTokens: estimatePayloadInputTokens(payload),
	}, nil
}

// validateCapabilities refuses request features the Kiro wire cannot honour.
func validateCapabilities(params *schemas.ChatParameters) *schemas.BifrostError {
	if choice := params.ToolChoice; choice != nil {
		kind := ""
		if choice.ChatToolChoiceStr != nil {
			kind = *choice.ChatToolChoiceStr
		} else if choice.ChatToolChoiceStruct != nil {
			kind = string(choice.ChatToolChoiceStruct.Type)
		}
		if kind != "" && kind != string(schemas.ChatToolChoiceTypeAuto) && kind != string(schemas.ChatToolChoiceTypeNone) {
			return providerUtils.NewBifrostBadRequestError("kiro: only tool_choice \"auto\" or \"none\" is supported")
		}
	}
	if params.ResponseFormat != nil {
		if format := responseFormatType(*params.ResponseFormat); format != "" && format != "text" {
			return providerUtils.NewBifrostBadRequestError("kiro: structured output (response_format " + format + ") is not supported")
		}
	}
	return nil
}

func responseFormatType(format any) string {
	switch v := format.(type) {
	case json.RawMessage:
		var parsed struct {
			Type string `json:"type"`
		}
		_ = sonic.Unmarshal(v, &parsed)
		return parsed.Type
	case map[string]any:
		s, _ := v["type"].(string)
		return s
	}
	return ""
}

func toolChoiceIsNone(params *schemas.ChatParameters) bool {
	choice := params.ToolChoice
	if choice == nil {
		return false
	}
	if choice.ChatToolChoiceStr != nil {
		return *choice.ChatToolChoiceStr == string(schemas.ChatToolChoiceTypeNone)
	}
	return choice.ChatToolChoiceStruct != nil && choice.ChatToolChoiceStruct.Type == schemas.ChatToolChoiceTypeNone
}

// convertTools builds the tool catalog for the current message. Every tool name is registered
// (so replayed calls stay consistent) even when tool_choice is none and no tools are sent.
func convertTools(params *schemas.ChatParameters, modelID string, registry *toolNameRegistry) ([]kiroTool, []string, *schemas.BifrostError) {
	for i := range params.Tools {
		tool := &params.Tools[i]
		if tool.Function == nil || tool.Function.Name == "" {
			return nil, nil, providerUtils.NewBifrostBadRequestError("kiro: only function tools are supported")
		}
		registry.alias(tool.Function.Name)
	}
	if toolChoiceIsNone(params) || len(params.Tools) == 0 {
		return nil, nil, nil
	}
	descriptionLimit := maxToolDescription
	if modelID == "gpt-5.6-sol" {
		descriptionLimit = maxToolDescriptionGPTSol
	}
	tools := make([]kiroTool, 0, len(params.Tools))
	catalogBytes := 2
	var omitted []string
	for i := range params.Tools {
		fn := params.Tools[i].Function
		description := ""
		if fn.Description != nil {
			description = strings.TrimSpace(*fn.Description)
		}
		if description == "" {
			description = "Tool: " + fn.Name
		}
		schema, err := sanitizedInputSchema(fn.Parameters)
		if err != nil {
			return nil, nil, providerUtils.NewBifrostBadRequestError(fmt.Sprintf("kiro: tool %q has an invalid parameters schema: %v", fn.Name, err))
		}
		converted := kiroTool{ToolSpecification: kiroToolSpecification{
			Name:        registry.alias(fn.Name),
			Description: truncateRunes(description, descriptionLimit),
			InputSchema: kiroInputSchema{JSON: schema},
		}}
		encoded, err := sonic.Marshal(converted)
		if err != nil {
			return nil, nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err)
		}
		if len(tools) >= maxToolCount || catalogBytes+len(encoded)+1 > maxToolCatalogBytes {
			omitted = append(omitted, converted.ToolSpecification.Name)
			continue
		}
		catalogBytes += len(encoded) + 1
		tools = append(tools, converted)
	}
	var additions []string
	if len(omitted) > 0 {
		names := omitted
		suffix := ""
		if len(names) > 12 {
			suffix = fmt.Sprintf(", and %d more", len(names)-12)
			names = names[:12]
		}
		additions = append(additions, fmt.Sprintf(
			"[bifrost] Kiro's tool catalog budget allows %d of %d client tools this turn. Omitted and unavailable this turn: %s%s.",
			len(tools), len(tools)+len(omitted), strings.Join(names, ", "), suffix))
	}
	return tools, additions, nil
}

// sanitizedInputSchema converts tool parameters into the schema subset Kiro accepts: rejected
// keywords removed, empty required dropped, and a root of type object with any root
// oneOf/anyOf/allOf flattened into properties.
func sanitizedInputSchema(params *schemas.ToolFunctionParameters) (*schemas.OrderedMap, error) {
	root := schemas.NewOrderedMap()
	if params != nil {
		raw, err := sonic.Marshal(params)
		if err != nil {
			return nil, err
		}
		if err := root.UnmarshalJSON(raw); err != nil {
			return nil, err
		}
	}
	sanitized, _ := sanitizeSchema(root).(*schemas.OrderedMap)
	if sanitized == nil {
		sanitized = schemas.NewOrderedMap()
	}
	return ensureRootObject(sanitized), nil
}

func sanitizeSchema(value any) any {
	switch v := value.(type) {
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = sanitizeSchema(item)
		}
		return out
	case *schemas.OrderedMap:
		if v == nil {
			return v
		}
		out := schemas.NewOrderedMapWithCapacity(v.Len())
		v.Range(func(key string, child any) bool {
			if rejectedSchemaKeys[key] {
				return true
			}
			if key == "required" {
				if items, ok := child.([]any); ok && len(items) == 0 {
					return true
				}
			}
			if schemaMapKeys[key] {
				out.Set(key, sanitizeSchemaMap(child))
			} else {
				out.Set(key, sanitizeSchema(child))
			}
			return true
		})
		return out
	default:
		return value
	}
}

func sanitizeSchemaMap(value any) any {
	m, ok := value.(*schemas.OrderedMap)
	if !ok || m == nil {
		return sanitizeSchema(value)
	}
	out := schemas.NewOrderedMapWithCapacity(m.Len())
	m.Range(func(name string, child any) bool {
		out.Set(name, sanitizeSchema(child))
		return true
	})
	return out
}

func ensureRootObject(schema *schemas.OrderedMap) *schemas.OrderedMap {
	hasComposition := false
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		if v, ok := schema.Get(key); ok {
			if _, isList := v.([]any); isList {
				hasComposition = true
			}
		}
	}
	if !hasComposition {
		if t, ok := schema.Get("type"); !ok || t != "object" {
			schema.Set("type", "object")
		}
		return schema
	}

	props := schemas.NewOrderedMap()
	var required []string
	seenRequired := map[string]bool{}
	addRequired := func(list any) {
		items, _ := list.([]any)
		for _, item := range items {
			if s, ok := item.(string); ok && !seenRequired[s] {
				seenRequired[s] = true
				required = append(required, s)
			}
		}
	}
	mergeProps := func(source any) {
		if m, ok := source.(*schemas.OrderedMap); ok && m != nil {
			m.Range(func(name string, child any) bool {
				props.Set(name, child)
				return true
			})
		}
	}
	if p, ok := schema.Get("properties"); ok {
		mergeProps(p)
	}
	if r, ok := schema.Get("required"); ok {
		addRequired(r)
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		variants, _ := schema.Get(key)
		list, ok := variants.([]any)
		if !ok {
			continue
		}
		for _, variant := range list {
			vm, ok := variant.(*schemas.OrderedMap)
			if !ok || vm == nil {
				continue
			}
			if p, ok := vm.Get("properties"); ok {
				mergeProps(sanitizeSchemaMap(p))
			}
			if key == "allOf" {
				if r, ok := vm.Get("required"); ok {
					addRequired(r)
				}
			}
		}
	}

	merged := schemas.NewOrderedMap()
	merged.Set("type", "object")
	schema.Range(func(key string, child any) bool {
		switch key {
		case "oneOf", "anyOf", "allOf", "type", "properties", "required":
		default:
			merged.Set(key, child)
		}
		return true
	})
	if props.Len() > 0 {
		merged.Set("properties", props)
	}
	if len(required) > 0 {
		list := make([]any, len(required))
		for i, r := range required {
			list[i] = r
		}
		merged.Set("required", list)
	}
	return merged
}

// toolInputObject returns a tool call's arguments as a JSON object, which is what Kiro's toolUses
// carry; the caller's bytes (and key order) are kept. Empty or non-object arguments become {}.
func toolInputObject(arguments string) json.RawMessage {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" || trimmed[0] != '{' || !json.Valid([]byte(trimmed)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(trimmed)
}

// truncateRunes caps s at limit runes, marking a cut with an ellipsis.
func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	if limit <= 1 {
		return string([]rune(s)[:max(limit, 0)])
	}
	return string([]rune(s)[:limit-1]) + "…"
}

// contentText joins a message's text parts with sep.
func contentText(content *schemas.ChatMessageContent, sep string) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return *content.ContentStr
	}
	parts := make([]string, 0, len(content.ContentBlocks))
	for _, block := range content.ContentBlocks {
		if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil {
			parts = append(parts, *block.Text)
		}
	}
	return strings.Join(parts, sep)
}

// userContent extracts text and inline images from user or tool content. Parts Kiro cannot carry
// (remote image URLs, files, audio) leave a bounded marker instead of disappearing silently.
func userContent(content *schemas.ChatMessageContent) (string, []kiroImage) {
	if content == nil {
		return "", nil
	}
	if content.ContentStr != nil {
		return *content.ContentStr, nil
	}
	texts := make([]string, 0, len(content.ContentBlocks))
	var images []kiroImage
	remote, malformed, files, audio := 0, 0, 0, 0
	for _, block := range content.ContentBlocks {
		switch block.Type {
		case schemas.ChatContentBlockTypeText:
			if block.Text != nil {
				texts = append(texts, *block.Text)
			}
		case schemas.ChatContentBlockTypeImage:
			if block.ImageURLStruct == nil {
				continue
			}
			url := block.ImageURLStruct.URL
			if !strings.HasPrefix(url, "data:") {
				remote++
				continue
			}
			img, ok := parseDataURLImage(url)
			if !ok {
				malformed++
				continue
			}
			images = append(images, img)
		case schemas.ChatContentBlockTypeFile:
			files++
		case schemas.ChatContentBlockTypeInputAudio:
			audio++
		}
	}
	text := strings.Join(texts, "\n")
	var markers []string
	if remote > 0 {
		markers = append(markers, omissionMarker(remote, "remote image references are not supported by this provider"))
	}
	if malformed > 0 {
		markers = append(markers, omissionMarker(malformed, "malformed inline image data URL"))
	}
	if files > 0 {
		markers = append(markers, omissionMarker(files, "file attachments are not supported by this provider"))
	}
	if audio > 0 {
		markers = append(markers, omissionMarker(audio, "audio input is not supported by this provider"))
	}
	if len(images) > maxImagesPerMessage {
		markers = append(markers, "[image omitted: exceeded the 20-image per-message cap; oldest images in this message were dropped]")
		images = images[len(images)-maxImagesPerMessage:]
	}
	if len(markers) > 0 {
		text = appendLine(text, strings.Join(markers, "\n"))
	}
	return text, images
}

func omissionMarker(count int, reason string) string {
	if count == 1 {
		return "[1 attachment omitted: " + reason + "]"
	}
	return fmt.Sprintf("[%d attachments omitted: %s]", count, reason)
}

// parseDataURLImage turns a data: URL into a Kiro image part.
func parseDataURLImage(url string) (kiroImage, bool) {
	header, data, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
	if !ok || data == "" || !strings.Contains(header, ";base64") {
		return kiroImage{}, false
	}
	mediaType, _, _ := strings.Cut(header, ";")
	subtype := "jpeg"
	if _, sub, found := strings.Cut(mediaType, "/"); found && sub != "" {
		subtype = strings.ToLower(sub)
	}
	if subtype == "jpg" {
		subtype = "jpeg"
	}
	switch subtype {
	case "jpeg", "png", "webp", "gif":
	default:
		return kiroImage{}, false
	}
	return kiroImage{Format: subtype, Source: kiroImageSource{Bytes: data}}, true
}

// applyImageCaps enforces the request-wide image count and base64 budget, keeping the newest
// images and noting each turn that lost some.
func applyImageCaps(turns []*turn) {
	count, budget := 0, 0
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		if len(t.images) == 0 {
			continue
		}
		kept := t.images[:0:0]
		dropped := 0
		for j := len(t.images) - 1; j >= 0; j-- {
			size := len(t.images[j].Source.Bytes)
			if count >= maxImagesPerRequest || budget+size > maxImageBase64Budget {
				dropped++
				continue
			}
			count++
			budget += size
			kept = append(kept, t.images[j])
		}
		for l, r := 0, len(kept)-1; l < r; l, r = l+1, r-1 {
			kept[l], kept[r] = kept[r], kept[l]
		}
		t.images = kept
		if dropped > 0 {
			t.content = appendLine(t.content, fmt.Sprintf("[%d images omitted: exceeded the request image budget; oldest images were dropped]", dropped))
		}
	}
}

// reasoningSettings returns the requested effort and the emulated thinking budget (0 = none).
func reasoningSettings(params *schemas.ChatParameters) (string, int) {
	reasoning := params.Reasoning
	if reasoning == nil || (reasoning.Enabled != nil && !*reasoning.Enabled) {
		return "", 0
	}
	effort := ""
	if reasoning.Effort != nil {
		effort = strings.ToLower(strings.TrimSpace(*reasoning.Effort))
	}
	if effort == "none" {
		return "", 0
	}
	if reasoning.MaxTokens != nil && *reasoning.MaxTokens > 0 {
		return effort, *reasoning.MaxTokens
	}
	ratio, ok := thinkingBudgetRatios[effort]
	if !ok {
		return effort, 0
	}
	maxTokens := defaultMaxOutputTokens
	if params.MaxCompletionTokens != nil && *params.MaxCompletionTokens > 0 {
		maxTokens = *params.MaxCompletionTokens
	}
	return effort, max(1, int(math.Floor(float64(maxTokens)*ratio)))
}

// injectThinkingTags prefixes the emulated thinking instructions Kiro models honour.
func injectThinkingTags(content string, budget int) string {
	return strings.Join([]string{
		"<thinking_mode>enabled</thinking_mode>",
		"<max_thinking_length>" + strconv.Itoa(budget) + "</max_thinking_length>",
		"<thinking_instruction>Think in English for better reasoning quality.\n" +
			"Be thorough and systematic, consider edge cases, challenge assumptions, and verify reasoning before answering.\n" +
			"After thinking, respond in the user's language.</thinking_instruction>",
		"",
		content,
	}, "\n")
}

// validateConversation enforces what Kiro validates server-side: alternating roles, non-empty
// turns, and every tool use answered by exactly one later tool result.
func validateConversation(history []kiroHistoryEntry, current *kiroUserInputMessage) *schemas.BifrostError {
	pending := map[string]bool{}
	checkUser := func(u *kiroUserInputMessage) *schemas.BifrostError {
		hasContext := u.UserInputMessageContext != nil && len(u.UserInputMessageContext.ToolResults) > 0
		if strings.TrimSpace(u.Content) == "" && len(u.Images) == 0 && !hasContext {
			return providerUtils.NewBifrostBadRequestError("kiro: user messages must not be empty")
		}
		if u.UserInputMessageContext == nil {
			return nil
		}
		for _, result := range u.UserInputMessageContext.ToolResults {
			if !pending[result.ToolUseID] {
				return providerUtils.NewBifrostBadRequestError(fmt.Sprintf("kiro: tool result %q has no matching tool call", result.ToolUseID))
			}
			delete(pending, result.ToolUseID)
		}
		return nil
	}
	previousAssistant := true
	for _, entry := range history {
		if entry.UserInputMessage != nil {
			if !previousAssistant {
				return providerUtils.NewBifrostBadRequestError("kiro: conversation roles must alternate")
			}
			if bErr := checkUser(entry.UserInputMessage); bErr != nil {
				return bErr
			}
			previousAssistant = false
			continue
		}
		if previousAssistant {
			return providerUtils.NewBifrostBadRequestError("kiro: conversation roles must alternate")
		}
		previousAssistant = true
		for _, use := range entry.AssistantResponseMessage.ToolUses {
			pending[use.ToolUseID] = true
		}
	}
	if !previousAssistant {
		return providerUtils.NewBifrostBadRequestError("kiro: conversation roles must alternate")
	}
	if bErr := checkUser(current); bErr != nil {
		return bErr
	}
	if len(pending) > 0 {
		return providerUtils.NewBifrostBadRequestError("kiro: conversation contains a tool call without a tool result")
	}
	return nil
}

func appendTurnText(target, next string) string {
	if next == "" {
		return target
	}
	if target == "" {
		return next
	}
	return target + "\n\n" + next
}

func appendLine(target, next string) string {
	if target == "" {
		return next
	}
	return target + "\n" + next
}

// Token estimation. Kiro reports no token usage, so input is estimated from the payload and
// output from the emitted characters, with the OpenCodex heuristics: 2.8 Latin chars per token
// (expanded 1.2x on the wire for input), 1.5 CJK chars per token, and 27 framing tokens per
// conversation entry.
const (
	latinCharsPerToken   = 2.8
	cjkCharsPerToken     = 1.5
	latinWireExpansion   = 1.2
	entryFramingTokens   = 27
	minImageTokens       = 256
	imageBytesPerToken   = 512
	estimateMarshalLimit = 1 << 20
)

func isCJK(r rune) bool {
	return (r >= 0xAC00 && r <= 0xD7A3) || (r >= 0x1100 && r <= 0x11FF) || (r >= 0x3130 && r <= 0x318F) ||
		(r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) || (r >= 0x3040 && r <= 0x30FF)
}

// charCounts splits a string's rune count into Latin and CJK.
func charCounts(s string) (latin, cjk int) {
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		if isCJK(r) {
			cjk++
		} else {
			latin++
		}
	}
	return latin, cjk
}

// estimateOutputTokens estimates tokens for generated text.
func estimateOutputTokens(latin, cjk int) int {
	return int(math.Ceil(float64(latin)/latinCharsPerToken)) + int(math.Ceil(float64(cjk)/cjkCharsPerToken))
}

func estimatePayloadInputTokens(payload *kiroPayload) int {
	var latin, cjk, entries, imageTokens int
	add := func(s string) {
		l, c := charCounts(s)
		latin += l
		cjk += c
	}
	addJSON := func(v any) {
		if encoded, err := sonic.Marshal(v); err == nil && len(encoded) <= estimateMarshalLimit {
			add(string(encoded))
		}
	}
	addUser := func(u *kiroUserInputMessage) {
		entries++
		add(u.Content)
		for _, img := range u.Images {
			decoded := base64.StdEncoding.DecodedLen(len(img.Source.Bytes))
			imageTokens += max(minImageTokens, decoded/imageBytesPerToken)
		}
		if u.UserInputMessageContext != nil {
			if len(u.UserInputMessageContext.Tools) > 0 {
				addJSON(u.UserInputMessageContext.Tools)
			}
			if len(u.UserInputMessageContext.ToolResults) > 0 {
				addJSON(u.UserInputMessageContext.ToolResults)
			}
		}
	}
	for _, entry := range payload.ConversationState.History {
		if entry.UserInputMessage != nil {
			addUser(entry.UserInputMessage)
			continue
		}
		if entry.AssistantResponseMessage != nil {
			entries++
			add(entry.AssistantResponseMessage.Content)
			if len(entry.AssistantResponseMessage.ToolUses) > 0 {
				addJSON(entry.AssistantResponseMessage.ToolUses)
			}
		}
	}
	addUser(payload.ConversationState.CurrentMessage.UserInputMessage)
	latinTokens := int(math.Ceil(float64(latin) * latinWireExpansion / latinCharsPerToken))
	cjkTokens := int(math.Ceil(float64(cjk) / cjkCharsPerToken))
	return latinTokens + cjkTokens + imageTokens + entries*entryFramingTokens
}
