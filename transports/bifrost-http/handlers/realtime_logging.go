package handlers

import (
	"encoding/json"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"
)

type realtimeTurnSource string

const (
	realtimeTurnSourceEI realtimeTurnSource = "ei"
	realtimeTurnSourceLM realtimeTurnSource = "lm"
)

const (
	realtimeMissingTranscriptText = "[Audio transcription unavailable]"
)

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

func finalizedRealtimeInputSummary(event *schemas.BifrostRealtimeEvent) string {
	if event == nil {
		return ""
	}

	switch event.Type {
	case schemas.RTEventInputAudioTransCompleted:
		if transcript := extractRealtimeExtraParamString(event, "transcript"); transcript != "" {
			return transcript
		}
		return realtimeMissingTranscriptText
	default:
		if event != nil && event.Type == schemas.RTEventConversationItemDone && schemas.IsRealtimeUserInputEvent(event) {
			if summary := extractRealtimeItemSummary(event.Item); summary != "" {
				return summary
			}
			if realtimeItemHasMissingAudioTranscript(event.Item) {
				return realtimeMissingTranscriptText
			}
		}
		if schemas.IsRealtimeUserInputEvent(event) {
			return extractRealtimeItemSummary(event.Item)
		}
	}

	return ""
}

func pendingRealtimeInputUpdate(event *schemas.BifrostRealtimeEvent) (string, string) {
	if event == nil {
		return "", ""
	}

	switch event.Type {
	case schemas.RTEventConversationItemRetrieved:
		return "", ""
	case schemas.RTEventInputAudioTransCompleted:
		return realtimeEventItemID(event), finalizedRealtimeInputSummary(event)
	default:
		if schemas.IsRealtimeUserInputEvent(event) {
			return realtimeEventItemID(event), finalizedRealtimeInputSummary(event)
		}
	}

	return "", ""
}

func realtimeItemHasMissingAudioTranscript(item *schemas.RealtimeItem) bool {
	if item == nil || len(item.Content) == 0 {
		return false
	}

	var decoded []map[string]any
	if err := sonic.Unmarshal(item.Content, &decoded); err != nil {
		return false
	}

	for _, part := range decoded {
		partType, _ := part["type"].(string)
		if partType != "input_audio" {
			continue
		}
		if transcript, exists := part["transcript"]; !exists || transcript == nil {
			return true
		}
	}

	return false
}

func finalizedRealtimeToolOutputSummary(event *schemas.BifrostRealtimeEvent) string {
	if !schemas.IsRealtimeToolOutputEvent(event) {
		return ""
	}
	return extractRealtimeItemSummary(event.Item)
}

func pendingRealtimeToolOutputUpdate(event *schemas.BifrostRealtimeEvent) (string, string) {
	if event == nil || event.Type == schemas.RTEventConversationItemRetrieved || !schemas.IsRealtimeToolOutputEvent(event) {
		return "", ""
	}
	return realtimeEventItemID(event), finalizedRealtimeToolOutputSummary(event)
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

func realtimeEventItemID(event *schemas.BifrostRealtimeEvent) string {
	if event == nil {
		return ""
	}
	if event.Item != nil && strings.TrimSpace(event.Item.ID) != "" {
		return strings.TrimSpace(event.Item.ID)
	}
	if event.Delta != nil && strings.TrimSpace(event.Delta.ItemID) != "" {
		return strings.TrimSpace(event.Delta.ItemID)
	}
	return extractRealtimeExtraParamString(event, "item_id")
}

func combineRealtimeInputRaw(turnInputs []bfws.RealtimeTurnInput) string {
	var parts []string
	for _, turnInput := range turnInputs {
		if trimmed := strings.TrimSpace(turnInput.Raw); trimmed != "" {
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
	var parts []string
	for _, output := range outputs {
		if output.Type != "message" {
			continue
		}
		for _, block := range output.Content {
			switch {
			case strings.TrimSpace(block.Text) != "":
				parts = append(parts, strings.TrimSpace(block.Text))
			case strings.TrimSpace(block.Transcript) != "":
				parts = append(parts, strings.TrimSpace(block.Transcript))
			case strings.TrimSpace(block.Refusal) != "":
				parts = append(parts, strings.TrimSpace(block.Refusal))
			}
		}
	}
	return strings.Join(parts, " ")
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

	totalTokens := parsed.Response.Usage.TotalTokens
	if totalTokens == 0 && (parsed.Response.Usage.InputTokens > 0 || parsed.Response.Usage.OutputTokens > 0) {
		totalTokens = parsed.Response.Usage.InputTokens + parsed.Response.Usage.OutputTokens
	}

	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     parsed.Response.Usage.InputTokens,
		CompletionTokens: parsed.Response.Usage.OutputTokens,
		TotalTokens:      totalTokens,
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
