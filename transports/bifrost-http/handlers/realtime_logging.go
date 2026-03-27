package handlers

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"
)

type realtimeTurnSource string

const (
	realtimeTurnSourceEI realtimeTurnSource = "ei"
	realtimeTurnSourceLM realtimeTurnSource = "lm"
)

const (
	realtimeTurnObject            = "realtime.turn"
	maxRealtimeTurnSummaryLength  = 1024
	maxRealtimeTurnRawFieldLength = 16 * 1024
	realtimeMissingTranscriptText = "[Audio transcription unavailable]"
)

type realtimeTurnLogger struct {
	enabled        bool
	store          logstore.LogStore
	pricingManager *modelcatalog.ModelCatalog
	notify         func(*logstore.Log)
}

func newRealtimeTurnLogger(config *lib.Config, notify func(*logstore.Log)) *realtimeTurnLogger {
	enabled := config != nil &&
		config.ClientConfig.EnableLogging != nil && *config.ClientConfig.EnableLogging &&
		config.LogsStore != nil

	return &realtimeTurnLogger{
		enabled:        enabled,
		store:          config.LogsStore,
		pricingManager: config.ModelCatalog,
		notify:         notify,
	}
}

func (l *realtimeTurnLogger) logTurn(
	bifrostCtx *schemas.BifrostContext,
	session *bfws.Session,
	source realtimeTurnSource,
	rtProvider schemas.RealtimeProvider,
	provider schemas.ModelProvider,
	model string,
	key schemas.Key,
	event *schemas.BifrostRealtimeEvent,
	rawMessage []byte,
	inputOverride string,
	inputRawOverride string,
	toolOutputs []bfws.RealtimeToolOutput,
	contentOverride string,
) {
	if !l.enabled || bifrostCtx == nil || session == nil || event == nil || !shouldLogRealtimeTurn(source, event) {
		return
	}

	now := time.Now().UTC()
	sessionID := session.ID()
	contentSummary := truncateRealtimeLogField(extractRealtimeTurnSummary(event, contentOverride), maxRealtimeTurnSummaryLength)
	if contentSummary == "" && !shouldLogRealtimeTurnWithoutSummary(source, event) {
		return
	}
	entry := &logstore.Log{
		ID:              uuid.NewString(),
		Timestamp:       now,
		CreatedAt:       now,
		Object:          realtimeTurnObject,
		Provider:        string(provider),
		Model:           model,
		Status:          realtimeTurnStatus(event),
		Stream:          true,
		SelectedKeyID:   key.ID,
		SelectedKeyName: key.Name,
		ContentSummary:  contentSummary,
		MetadataParsed: map[string]interface{}{
			"realtime":            true,
			"realtime_source":     string(source),
			"realtime_event_type": string(event.Type),
			"realtime_event_id":   event.EventID,
			"realtime_session_id": sessionID,
		},
	}
	if sessionID != "" {
		entry.ParentRequestID = &sessionID
	}

	if source == realtimeTurnSourceEI {
		entry.RawRequest = truncateRealtimeLogField(string(rawMessage), maxRealtimeTurnRawFieldLength)
	} else {
		if rawRequest := combineRealtimeInputRaw(inputRawOverride, toolOutputs); rawRequest != "" {
			entry.RawRequest = truncateRealtimeLogField(rawRequest, maxRealtimeTurnRawFieldLength)
		}
		entry.RawResponse = truncateRealtimeLogField(string(rawMessage), maxRealtimeTurnRawFieldLength)
	}
	if contentSummary != "" {
		content := &schemas.ChatMessageContent{ContentStr: &contentSummary}
		if source == realtimeTurnSourceEI {
			entry.InputHistoryParsed = []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: content,
			}}
		} else {
			entry.OutputMessageParsed = extractRealtimeTurnOutputMessage(rtProvider, rawMessage, contentSummary)
		}
	}
	if source == realtimeTurnSourceLM && entry.OutputMessageParsed == nil {
		entry.OutputMessageParsed = extractRealtimeTurnOutputMessage(rtProvider, rawMessage, contentSummary)
	}
	if source == realtimeTurnSourceLM {
		var inputHistory []schemas.ChatMessage
		for _, toolOutput := range toolOutputs {
			if strings.TrimSpace(toolOutput.Summary) == "" {
				continue
			}
			toolContent := &schemas.ChatMessageContent{ContentStr: schemas.Ptr(strings.TrimSpace(toolOutput.Summary))}
			inputHistory = append(inputHistory, schemas.ChatMessage{
				Role:            schemas.ChatMessageRoleTool,
				Content:         toolContent,
				ChatToolMessage: &schemas.ChatToolMessage{},
			})
		}
		if strings.TrimSpace(inputOverride) != "" {
			inputContent := &schemas.ChatMessageContent{ContentStr: schemas.Ptr(strings.TrimSpace(inputOverride))}
			inputHistory = append(inputHistory, schemas.ChatMessage{
				Role:    schemas.ChatMessageRoleUser,
				Content: inputContent,
			})
		}
		if len(inputHistory) > 0 {
			entry.InputHistoryParsed = inputHistory
		}
		if usage := extractRealtimeTurnUsage(rtProvider, rawMessage); usage != nil {
			entry.TokenUsageParsed = usage
			if l.pricingManager != nil {
				pricingScopes := modelcatalog.PricingLookupScopesFromContext(bifrostCtx, string(provider))
				if cost := l.pricingManager.CalculateCost(buildRealtimeUsageResponse(provider, model, usage), pricingScopes); cost > 0 {
					entry.Cost = &cost
				}
			}
		}
	}

	if providerSessionID := strings.TrimSpace(session.ProviderSessionID()); providerSessionID != "" {
		entry.MetadataParsed["provider_session_id"] = providerSessionID
	}
	if event.Item != nil {
		if event.Item.ID != "" {
			entry.MetadataParsed["realtime_item_id"] = event.Item.ID
		}
		if event.Item.Type != "" {
			entry.MetadataParsed["realtime_item_type"] = event.Item.Type
		}
		if event.Item.Role != "" {
			entry.MetadataParsed["realtime_item_role"] = event.Item.Role
		}
		if event.Item.Status != "" {
			entry.MetadataParsed["realtime_item_status"] = event.Item.Status
		}
	}
	if event.Session != nil && event.Session.ID != "" {
		entry.MetadataParsed["provider_session_id"] = event.Session.ID
	}
	if event.Error != nil {
		statusCode := 400
		errorType := event.Error.Type
		entry.ErrorDetailsParsed = &schemas.BifrostError{
			StatusCode: &statusCode,
			Type:       &errorType,
			Error: &schemas.ErrorField{
				Type:    &errorType,
				Message: event.Error.Message,
				Param:   event.Error.Param,
			},
			ExtraFields: schemas.BifrostErrorExtraFields{
				RequestType:    schemas.RealtimeRequest,
				Provider:       provider,
				ModelRequested: model,
			},
		}
	}

	if virtualKeyID := bifrost.GetStringFromContext(bifrostCtx, schemas.BifrostContextKeyGovernanceVirtualKeyID); virtualKeyID != "" {
		entry.VirtualKeyID = &virtualKeyID
	}
	if virtualKeyName := bifrost.GetStringFromContext(bifrostCtx, schemas.BifrostContextKeyGovernanceVirtualKeyName); virtualKeyName != "" {
		entry.VirtualKeyName = &virtualKeyName
	}
	if routingRuleID := bifrost.GetStringFromContext(bifrostCtx, schemas.BifrostContextKeyGovernanceRoutingRuleID); routingRuleID != "" {
		entry.RoutingRuleID = &routingRuleID
	}
	if routingRuleName := bifrost.GetStringFromContext(bifrostCtx, schemas.BifrostContextKeyGovernanceRoutingRuleName); routingRuleName != "" {
		entry.RoutingRuleName = &routingRuleName
	}

	if err := l.store.CreateIfNotExists(bifrostCtx, entry); err != nil {
		logger.Warn(
			"failed to persist realtime turn log for session %s (source=%s event=%s ctx_err=%v): %v",
			sessionID,
			source,
			event.Type,
			bifrostCtx.Err(),
			err,
		)
		return
	}

	if l.notify != nil {
		l.notify(entry)
	}
}

func shouldLogRealtimeTurn(source realtimeTurnSource, event *schemas.BifrostRealtimeEvent) bool {
	if event == nil {
		return false
	}

	switch source {
	case realtimeTurnSourceEI:
		switch event.Type {
		case schemas.RTEventConversationItemCreate,
			schemas.RTEventInputAudioCommit,
			schemas.RTEventResponseCreate:
			return shouldLogRealtimeEITurn(event)
		default:
			return false
		}
	case realtimeTurnSourceLM:
		switch event.Type {
		case schemas.RTEventResponseDone,
			schemas.RTEventError:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func shouldLogRealtimeEITurn(event *schemas.BifrostRealtimeEvent) bool {
	if event == nil {
		return false
	}
	switch event.Type {
	case schemas.RTEventConversationItemCreate:
		return event.Item != nil && event.Item.Role == "user" && event.Item.Type != "function_call_output" && event.Item.Type != "function_call"
	case schemas.RTEventInputAudioCommit, schemas.RTEventResponseCreate:
		return true
	default:
		return false
	}
}

func shouldLogRealtimeTurnWithoutSummary(source realtimeTurnSource, event *schemas.BifrostRealtimeEvent) bool {
	if event == nil {
		return false
	}
	return source == realtimeTurnSourceLM && event.Type == schemas.RTEventError
}

func realtimeTurnStatus(event *schemas.BifrostRealtimeEvent) string {
	if event != nil && event.Type == schemas.RTEventError {
		return "error"
	}
	return "success"
}

func extractRealtimeTurnSummary(event *schemas.BifrostRealtimeEvent, contentOverride string) string {
	if strings.TrimSpace(contentOverride) != "" {
		return strings.TrimSpace(contentOverride)
	}
	if event == nil {
		return ""
	}
	if event.Error != nil && strings.TrimSpace(event.Error.Message) != "" {
		return strings.TrimSpace(event.Error.Message)
	}
	if event.Delta != nil {
		if text := strings.TrimSpace(event.Delta.Text); text != "" {
			return text
		}
		if transcript := strings.TrimSpace(event.Delta.Transcript); transcript != "" {
			return transcript
		}
	}
	if event.Item != nil {
		if summary := extractRealtimeItemSummary(event.Item); summary != "" {
			return summary
		}
	}
	if event.Session != nil && strings.TrimSpace(event.Session.Instructions) != "" {
		return strings.TrimSpace(event.Session.Instructions)
	}
	if len(event.RawData) > 0 {
		return strings.TrimSpace(string(event.RawData))
	}
	return ""
}

func extractRealtimeItemSummary(item *schemas.RealtimeItem) string {
	if item == nil {
		return ""
	}
	if summary := extractRealtimeContentSummary(item.Content); summary != "" {
		return summary
	}
	switch {
	case strings.TrimSpace(item.Output) != "":
		return strings.TrimSpace(item.Output)
	case strings.TrimSpace(item.Arguments) != "":
		return strings.TrimSpace(item.Arguments)
	case strings.TrimSpace(item.Name) != "":
		return strings.TrimSpace(item.Name)
	default:
		return ""
	}
}

func extractRealtimeContentSummary(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}

	var decoded any
	if err := sonic.Unmarshal(raw, &decoded); err != nil {
		return strings.TrimSpace(string(raw))
	}

	var parts []string
	collectRealtimeTextFragments(decoded, &parts)
	return strings.Join(parts, " ")
}

func collectRealtimeTextFragments(value any, parts *[]string) {
	switch v := value.(type) {
	case map[string]any:
		for key, field := range v {
			switch key {
			case "text", "transcript", "input_text", "output_text", "output", "arguments":
				if text, ok := field.(string); ok {
					text = strings.TrimSpace(text)
					if text != "" {
						*parts = append(*parts, text)
					}
					continue
				}
			}
			collectRealtimeTextFragments(field, parts)
		}
	case []any:
		for _, item := range v {
			collectRealtimeTextFragments(item, parts)
		}
	}
}

func truncateRealtimeLogField(value string, maxLen int) string {
	value = strings.TrimSpace(value)
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	return value[:maxLen] + "..."
}

func finalizedRealtimeInputSummary(event *schemas.BifrostRealtimeEvent) string {
	if event == nil {
		return ""
	}

	switch event.Type {
	case schemas.RTEventConversationItemCreate:
		if event.Item != nil && event.Item.Role == "user" {
			return extractRealtimeTurnSummary(event, "")
		}
	case schemas.RTEventInputAudioTransCompleted:
		if transcript := extractRealtimeExtraParamString(event, "transcript"); transcript != "" {
			return transcript
		}
		return realtimeMissingTranscriptText
	}

	return ""
}

func finalizedRealtimeToolOutputSummary(event *schemas.BifrostRealtimeEvent) string {
	if event == nil || event.Type != schemas.RTEventConversationItemCreate || event.Item == nil {
		return ""
	}
	if event.Item.Type != "function_call_output" {
		return ""
	}
	return extractRealtimeTurnSummary(event, "")
}

func extractRealtimeExtraParamString(event *schemas.BifrostRealtimeEvent, key string) string {
	if event == nil || event.ExtraParams == nil {
		return ""
	}
	raw, ok := event.ExtraParams[key]
	if !ok || len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func combineRealtimeInputRaw(inputRawOverride string, toolOutputs []bfws.RealtimeToolOutput) string {
	var parts []string
	if trimmed := strings.TrimSpace(inputRawOverride); trimmed != "" {
		parts = append(parts, trimmed)
	}
	for _, toolOutput := range toolOutputs {
		if trimmed := strings.TrimSpace(toolOutput.Raw); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n\n")
}

type realtimeResponseDoneEnvelope struct {
	Response struct {
		Output []realtimeResponseDoneOutput `json:"output"`
		Usage  *realtimeResponseDoneUsage   `json:"usage"`
	} `json:"response"`
}

type realtimeResponseDoneOutput struct {
	ID        string                        `json:"id"`
	Type      string                        `json:"type"`
	Name      string                        `json:"name"`
	CallID    string                        `json:"call_id"`
	Arguments string                        `json:"arguments"`
	Content   []realtimeResponseDoneContent `json:"content"`
}

type realtimeResponseDoneContent struct {
	Type       string `json:"type"`
	Text       string `json:"text"`
	Transcript string `json:"transcript"`
	Refusal    string `json:"refusal"`
}

type realtimeResponseDoneUsage struct {
	TotalTokens        int                                   `json:"total_tokens"`
	InputTokens        int                                   `json:"input_tokens"`
	OutputTokens       int                                   `json:"output_tokens"`
	InputTokenDetails  *realtimeResponseDoneInputTokenUsage  `json:"input_token_details"`
	OutputTokenDetails *realtimeResponseDoneOutputTokenUsage `json:"output_token_details"`
}

type realtimeResponseDoneInputTokenUsage struct {
	TextTokens   int `json:"text_tokens"`
	AudioTokens  int `json:"audio_tokens"`
	ImageTokens  int `json:"image_tokens"`
	CachedTokens int `json:"cached_tokens"`
}

type realtimeResponseDoneOutputTokenUsage struct {
	TextTokens               int  `json:"text_tokens"`
	AudioTokens              int  `json:"audio_tokens"`
	ReasoningTokens          int  `json:"reasoning_tokens"`
	ImageTokens              *int `json:"image_tokens"`
	CitationTokens           *int `json:"citation_tokens"`
	NumSearchQueries         *int `json:"num_search_queries"`
	AcceptedPredictionTokens int  `json:"accepted_prediction_tokens"`
	RejectedPredictionTokens int  `json:"rejected_prediction_tokens"`
}

func extractRealtimeTurnUsage(provider schemas.RealtimeProvider, rawMessage []byte) *schemas.BifrostLLMUsage {
	if extractor, ok := provider.(schemas.RealtimeUsageExtractor); ok {
		if usage := extractor.ExtractRealtimeTurnUsage(rawMessage); usage != nil {
			return usage
		}
	}
	return extractRealtimeResponseDoneUsage(rawMessage)
}

func extractRealtimeTurnOutputMessage(provider schemas.RealtimeProvider, rawMessage []byte, contentSummary string) *schemas.ChatMessage {
	if extractor, ok := provider.(schemas.RealtimeUsageExtractor); ok {
		if message := extractor.ExtractRealtimeTurnOutput(rawMessage); message != nil {
			if strings.TrimSpace(contentSummary) != "" && (message.Content == nil || message.Content.ContentStr == nil || strings.TrimSpace(*message.Content.ContentStr) == "") {
				message.Content = &schemas.ChatMessageContent{ContentStr: schemas.Ptr(strings.TrimSpace(contentSummary))}
			}
			return message
		}
	}
	return buildRealtimeAssistantLogMessage(rawMessage, contentSummary)
}

func buildRealtimeAssistantLogMessage(rawMessage []byte, contentSummary string) *schemas.ChatMessage {
	contentSummary = strings.TrimSpace(contentSummary)
	var parsed realtimeResponseDoneEnvelope
	if len(rawMessage) > 0 && sonic.Unmarshal(rawMessage, &parsed) == nil {
		message := &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant}
		if contentSummary == "" {
			contentSummary = extractRealtimeResponseDoneAssistantText(parsed.Response.Output)
		}
		if contentSummary != "" {
			message.Content = &schemas.ChatMessageContent{ContentStr: schemas.Ptr(contentSummary)}
		}

		toolCalls := extractRealtimeResponseDoneToolCalls(parsed.Response.Output)
		if len(toolCalls) > 0 {
			message.ChatAssistantMessage = &schemas.ChatAssistantMessage{
				ToolCalls: toolCalls,
			}
		}

		if message.Content != nil || message.ChatAssistantMessage != nil {
			return message
		}
	}

	if contentSummary == "" {
		return nil
	}

	return &schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleAssistant,
		Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(contentSummary)},
	}
}

func extractRealtimeResponseDoneAssistantText(outputs []realtimeResponseDoneOutput) string {
	for _, output := range outputs {
		if output.Type != "message" {
			continue
		}
		for _, block := range output.Content {
			switch {
			case strings.TrimSpace(block.Text) != "":
				return strings.TrimSpace(block.Text)
			case strings.TrimSpace(block.Transcript) != "":
				return strings.TrimSpace(block.Transcript)
			case strings.TrimSpace(block.Refusal) != "":
				return strings.TrimSpace(block.Refusal)
			}
		}
	}
	return ""
}

func extractRealtimeResponseDoneToolCalls(outputs []realtimeResponseDoneOutput) []schemas.ChatAssistantMessageToolCall {
	toolCalls := make([]schemas.ChatAssistantMessageToolCall, 0)
	for _, output := range outputs {
		if output.Type != "function_call" {
			continue
		}

		name := strings.TrimSpace(output.Name)
		if name == "" {
			continue
		}

		toolType := "function"
		id := strings.TrimSpace(output.CallID)
		if id == "" {
			id = strings.TrimSpace(output.ID)
		}

		toolCall := schemas.ChatAssistantMessageToolCall{
			Index: uint16(len(toolCalls)),
			Type:  &toolType,
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr(name),
				Arguments: output.Arguments,
			},
		}
		if id != "" {
			toolCall.ID = schemas.Ptr(id)
		}

		toolCalls = append(toolCalls, toolCall)
	}
	return toolCalls
}

func extractRealtimeResponseDoneUsage(rawMessage []byte) *schemas.BifrostLLMUsage {
	if len(rawMessage) == 0 {
		return nil
	}

	var parsed realtimeResponseDoneEnvelope
	if err := sonic.Unmarshal(rawMessage, &parsed); err != nil || parsed.Response.Usage == nil {
		return nil
	}

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     parsed.Response.Usage.InputTokens,
		CompletionTokens: parsed.Response.Usage.OutputTokens,
		TotalTokens:      parsed.Response.Usage.TotalTokens,
	}

	if parsed.Response.Usage.InputTokenDetails != nil {
		usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{
			TextTokens:       parsed.Response.Usage.InputTokenDetails.TextTokens,
			AudioTokens:      parsed.Response.Usage.InputTokenDetails.AudioTokens,
			ImageTokens:      parsed.Response.Usage.InputTokenDetails.ImageTokens,
			CachedReadTokens: parsed.Response.Usage.InputTokenDetails.CachedTokens,
		}
	}

	if parsed.Response.Usage.OutputTokenDetails != nil {
		usage.CompletionTokensDetails = &schemas.ChatCompletionTokensDetails{
			TextTokens:               parsed.Response.Usage.OutputTokenDetails.TextTokens,
			AudioTokens:              parsed.Response.Usage.OutputTokenDetails.AudioTokens,
			ReasoningTokens:          parsed.Response.Usage.OutputTokenDetails.ReasoningTokens,
			ImageTokens:              parsed.Response.Usage.OutputTokenDetails.ImageTokens,
			CitationTokens:           parsed.Response.Usage.OutputTokenDetails.CitationTokens,
			NumSearchQueries:         parsed.Response.Usage.OutputTokenDetails.NumSearchQueries,
			AcceptedPredictionTokens: parsed.Response.Usage.OutputTokenDetails.AcceptedPredictionTokens,
			RejectedPredictionTokens: parsed.Response.Usage.OutputTokenDetails.RejectedPredictionTokens,
		}
	}

	return usage
}

func buildRealtimeUsageResponse(provider schemas.ModelProvider, model string, usage *schemas.BifrostLLMUsage) *schemas.BifrostResponse {
	if usage == nil {
		return nil
	}
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Model: model,
			Usage: usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:    schemas.ChatCompletionRequest,
				Provider:       provider,
				ModelRequested: model,
			},
		},
	}
}
