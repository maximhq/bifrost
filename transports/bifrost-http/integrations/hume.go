package integrations

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

const (
	humeMaxSessionIDBytes  = 255
	humeProsodyInstruction = "Hume may append <hume_vocal_expression> JSON to user messages. " +
		"Values are confidence scores for perceived vocal expression, not claims about the user's internal state. " +
		"Use them subtly to shape empathy and phrasing; do not mention the tag, labels, or scores unless the user asks."
)

type humeSessionContextKey struct{}

// HumeRouter exposes the OpenAI-compatible SSE endpoint used by Hume EVI custom language models.
type HumeRouter struct {
	*GenericRouter
}

type humeConfigProvider interface {
	GetHumeConfig() *lib.HumeConfig
}

// HumeChatRequest preserves the OpenAI chat request while separately retaining
// Hume's per-message timing and prosody metadata.
type HumeChatRequest struct {
	OpenAIRequest   openai.OpenAIChatRequest
	Fallbacks       []string `json:"fallbacks,omitempty"`
	messageMetadata map[int]schemas.HumeMessageMetadata
	customSessionID string
}

// UnmarshalJSON parses both OpenAI chat fields and Hume's message annotations.
func (r *HumeChatRequest) UnmarshalJSON(data []byte) error {
	var openAIRequest openai.OpenAIChatRequest
	if err := sonic.Unmarshal(data, &openAIRequest); err != nil {
		return err
	}

	metadata, err := parseHumeMessageMetadata(data)
	if err != nil {
		return err
	}

	*r = HumeChatRequest{
		OpenAIRequest:   openAIRequest,
		Fallbacks:       openAIRequest.Fallbacks,
		messageMetadata: metadata,
	}
	return nil
}

func parseHumeMessageMetadata(data []byte) (map[int]schemas.HumeMessageMetadata, error) {
	messages := providerUtils.GetJSONField(data, "messages").Array()
	metadata := make(map[int]schemas.HumeMessageMetadata, len(messages))
	for i, message := range messages {
		var item schemas.HumeMessageMetadata
		if timeNode := message.Get("time"); timeNode.Exists() && timeNode.Raw != "null" {
			messageTime := &schemas.HumeMessageTime{}
			if err := sonic.UnmarshalString(timeNode.Raw, messageTime); err != nil {
				return nil, fmt.Errorf("failed to parse Hume message %d time: %w", i, err)
			}
			item.Time = messageTime
		}
		if scoresNode := message.Get("models.prosody.scores"); scoresNode.Exists() && scoresNode.Raw != "null" {
			if err := sonic.UnmarshalString(scoresNode.Raw, &item.ProsodyScores); err != nil {
				return nil, fmt.Errorf("failed to parse Hume message %d prosody scores: %w", i, err)
			}
		}
		if item.Time != nil || len(item.ProsodyScores) > 0 {
			metadata[i] = item
		}
	}
	return metadata, nil
}

// IsStreamingRequested implements StreamingRequest.
func (r *HumeChatRequest) IsStreamingRequested() bool {
	return r != nil && r.OpenAIRequest.IsStreamingRequested()
}

// SetExtraParams implements RequestWithSettableExtraParams.
func (r *HumeChatRequest) SetExtraParams(params map[string]interface{}) {
	r.OpenAIRequest.SetExtraParams(params)
}

type humeStreamChunk struct {
	ID                string             `json:"id"`
	Choices           []humeStreamChoice `json:"choices"`
	Created           int                `json:"created"`
	Model             string             `json:"model"`
	Object            string             `json:"object"`
	SystemFingerprint string             `json:"system_fingerprint"`
	Usage             *humeUsage         `json:"usage,omitempty"`
}

type humeStreamChoice struct {
	Index        int             `json:"index"`
	Delta        humeStreamDelta `json:"delta"`
	FinishReason *string         `json:"finish_reason,omitempty"`
}

type humeStreamDelta struct {
	Role      *string        `json:"role,omitempty"`
	Content   *string        `json:"content,omitempty"`
	Refusal   *string        `json:"refusal,omitempty"`
	ToolCalls []humeToolCall `json:"tool_calls,omitempty"`
}

type humeToolCall struct {
	Index    uint16                                       `json:"index"`
	Type     *string                                      `json:"type,omitempty"`
	ID       *string                                      `json:"id,omitempty"`
	Function schemas.ChatAssistantMessageToolCallFunction `json:"function"`
}

type humeUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type humeErrorEnvelope struct {
	Error *schemas.ErrorField `json:"error"`
}

type humePromptScore struct {
	Label string  `json:"label"`
	Score float64 `json:"score"`
}

// NewHumeRouter creates the dedicated Hume EVI custom language model router.
func NewHumeRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *HumeRouter {
	config := lib.NewDefaultHumeConfig()
	if provider, ok := handlerStore.(humeConfigProvider); ok {
		if configured := provider.GetHumeConfig(); configured != nil {
			config = configured
		}
	}
	return &HumeRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, CreateHumeRouteConfigs(config), nil, logger),
	}
}

// CreateHumeRouteConfigs returns the two Hume-compatible chat completion routes.
func CreateHumeRouteConfigs(config *lib.HumeConfig) []RouteConfig {
	if config == nil {
		config = lib.NewDefaultHumeConfig()
	}
	routes := make([]RouteConfig, 0, 2)
	for _, path := range []string{"/hume/v1/chat/completions", "/hume/chat/completions"} {
		routes = append(routes, RouteConfig{
			Type:                    RouteConfigTypeHume,
			Path:                    path,
			Method:                  fasthttp.MethodPost,
			DisableLargePayloadMode: true,
			PreCallback:             humePreCallback(config),
			GetHTTPRequestType: func(_ *fasthttp.RequestCtx) schemas.RequestType {
				return schemas.ChatCompletionRequest
			},
			GetRequestTypeInstance: func(_ context.Context) interface{} {
				return &HumeChatRequest{}
			},
			RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
				humeRequest, ok := req.(*HumeChatRequest)
				if !ok {
					return nil, errors.New("invalid Hume chat request type")
				}
				chatRequest := humeRequest.OpenAIRequest.ToBifrostChatRequest(ctx)
				metadata := humeRequest.messageMetadata
				chatRequest.Input, metadata = injectHumeProsody(chatRequest.Input, metadata, config.ProsodyPrompt)
				chatRequest.IntegrationMetadata = &schemas.HumeChatRequestMetadata{
					CustomSessionID: humeRequest.customSessionID,
					Messages:        metadata,
				}
				return &schemas.BifrostRequest{ChatRequest: chatRequest}, nil
			},
			ErrorConverter: humeErrorConverter,
			StreamConfig: &StreamConfig{
				ChatStreamResponseConverter: humeChatStreamResponseConverter,
				ErrorConverter:              humeErrorConverter,
			},
		})
	}
	return routes
}

func humePreCallback(config *lib.HumeConfig) PreRequestCallback {
	return func(httpCtx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		humeRequest, ok := req.(*HumeChatRequest)
		if !ok {
			return errors.New("invalid Hume chat request type")
		}
		openAIRequest := &humeRequest.OpenAIRequest
		if openAIRequest.Stream != nil && !*openAIRequest.Stream {
			return errors.New("Hume custom language model requests must use streaming")
		}
		// Hume consumes one conversational branch and cannot process parallel tool
		// calls. Set the provider-facing values explicitly even when Hume omits them.
		openAIRequest.Stream = schemas.Ptr(true)
		if openAIRequest.N != nil && *openAIRequest.N != 1 {
			return errors.New("Hume custom language model requests must request exactly one completion")
		}
		openAIRequest.N = schemas.Ptr(1)
		if len(openAIRequest.Tools) > 0 {
			openAIRequest.ParallelToolCalls = schemas.Ptr(false)
		}

		openAIRequest.Model = strings.TrimSpace(openAIRequest.Model)
		if openAIRequest.Model == "" {
			openAIRequest.Model = config.DefaultModel
		}
		if openAIRequest.Model == "" {
			return errors.New("Hume custom language model identifier is missing and hume.default_model is not configured")
		}

		sessionID := string(httpCtx.QueryArgs().Peek("custom_session_id"))
		if sessionID != "" {
			if !utf8.ValidString(sessionID) {
				return errors.New("custom_session_id must be valid UTF-8")
			}
			if len(sessionID) > humeMaxSessionIDBytes {
				return fmt.Errorf("custom_session_id must not exceed %d bytes", humeMaxSessionIDBytes)
			}
		} else {
			sessionID = uuid.NewString()
		}
		humeRequest.customSessionID = sessionID
		bifrostCtx.SetValue(humeSessionContextKey{}, sessionID)
		schemas.ExtractAndSetUserAgentFromHeaders(extractHeadersFromRequest(httpCtx), bifrostCtx)
		return nil
	}
}

func injectHumeProsody(messages []schemas.ChatMessage, metadata map[int]schemas.HumeMessageMetadata, config *lib.HumeProsodyPromptConfig) ([]schemas.ChatMessage, map[int]schemas.HumeMessageMetadata) {
	if config == nil || !config.Enabled {
		return messages, metadata
	}

	selected := make(map[int]string)
	if config.Scope == lib.HumeProsodyPromptScopeAllUserMessages {
		for i := range messages {
			if tag := humeProsodyTag(messages[i], metadata, i, config.MaxEmotions); tag != "" {
				selected[i] = tag
			}
		}
	} else {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == schemas.ChatMessageRoleUser {
				if tag := humeProsodyTag(messages[i], metadata, i, config.MaxEmotions); tag != "" {
					selected[i] = tag
				}
				break
			}
		}
	}
	if len(selected) == 0 {
		return messages, metadata
	}

	insertAt := 0
	for insertAt < len(messages) && (messages[insertAt].Role == schemas.ChatMessageRoleSystem || messages[insertAt].Role == schemas.ChatMessageRoleDeveloper) {
		insertAt++
	}
	instruction := humeProsodyInstruction
	instructionMessage := schemas.ChatMessage{
		Role: schemas.ChatMessageRoleSystem,
		Content: &schemas.ChatMessageContent{
			ContentStr: &instruction,
		},
	}

	result := make([]schemas.ChatMessage, 0, len(messages)+1)
	for i, message := range messages {
		if i == insertAt {
			result = append(result, instructionMessage)
		}
		if tag, ok := selected[i]; ok {
			message = appendHumeProsodyTag(message, tag)
		}
		result = append(result, message)
	}
	shiftedMetadata := make(map[int]schemas.HumeMessageMetadata, len(metadata))
	for messageIndex, item := range metadata {
		if messageIndex >= insertAt {
			messageIndex++
		}
		shiftedMetadata[messageIndex] = item
	}
	return result, shiftedMetadata
}

func humeProsodyTag(message schemas.ChatMessage, metadata map[int]schemas.HumeMessageMetadata, messageIndex int, maxEmotions *int) string {
	if message.Role != schemas.ChatMessageRoleUser {
		return ""
	}
	messageMetadata, ok := metadata[messageIndex]
	if !ok || len(messageMetadata.ProsodyScores) == 0 {
		return ""
	}

	scores := make([]humePromptScore, 0, len(messageMetadata.ProsodyScores))
	for label, score := range messageMetadata.ProsodyScores {
		scores = append(scores, humePromptScore{Label: label, Score: score})
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].Score == scores[j].Score {
			return scores[i].Label < scores[j].Label
		}
		return scores[i].Score > scores[j].Score
	})
	if maxEmotions != nil && *maxEmotions > 0 && len(scores) > *maxEmotions {
		scores = scores[:*maxEmotions]
	}
	for i := range scores {
		scores[i].Score = math.Round(scores[i].Score*1000) / 1000
	}
	encoded, err := schemas.MarshalSorted(scores)
	if err != nil {
		return ""
	}
	return "<hume_vocal_expression>" + string(encoded) + "</hume_vocal_expression>"
}

func appendHumeProsodyTag(message schemas.ChatMessage, tag string) schemas.ChatMessage {
	separatorAndTag := "\n\n" + tag
	if message.Content == nil {
		message.Content = &schemas.ChatMessageContent{ContentStr: &separatorAndTag}
		return message
	}
	content := *message.Content
	if content.ContentStr != nil {
		combined := *content.ContentStr + separatorAndTag
		content.ContentStr = &combined
	} else {
		blocks := append([]schemas.ChatContentBlock(nil), content.ContentBlocks...)
		blocks = append(blocks, schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeText, Text: &separatorAndTag})
		content.ContentBlocks = blocks
	}
	message.Content = &content
	return message
}

func humeChatStreamResponseConverter(ctx *schemas.BifrostContext, resp *schemas.BifrostChatResponse) (string, interface{}, error) {
	if resp == nil {
		return "", nil, errors.New("Hume stream response is nil")
	}
	sessionID, _ := ctx.Value(humeSessionContextKey{}).(string)
	if sessionID == "" {
		return "", nil, errors.New("Hume custom session ID is missing")
	}

	choices := make([]humeStreamChoice, 0, len(resp.Choices))
	for _, choice := range resp.Choices {
		converted := humeStreamChoice{
			Index:        choice.Index,
			FinishReason: choice.FinishReason,
		}
		if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
			delta := choice.ChatStreamResponseChoice.Delta
			converted.Delta = humeStreamDelta{
				Role:      delta.Role,
				Content:   delta.Content,
				Refusal:   delta.Refusal,
				ToolCalls: toHumeToolCalls(delta.ToolCalls),
			}
		} else if choice.ChatNonStreamResponseChoice != nil && choice.ChatNonStreamResponseChoice.Message != nil {
			message := choice.ChatNonStreamResponseChoice.Message
			if message.Role != "" {
				role := string(message.Role)
				converted.Delta.Role = &role
			}
			converted.Delta.Content = humeMessageText(message.Content)
			if message.ChatAssistantMessage != nil {
				converted.Delta.Refusal = message.Refusal
				converted.Delta.ToolCalls = toHumeNonStreamToolCalls(message.ToolCalls)
			}
		}
		choices = append(choices, converted)
	}

	chunk := &humeStreamChunk{
		ID:                resp.ID,
		Choices:           choices,
		Created:           resp.Created,
		Model:             resp.Model,
		Object:            "chat.completion.chunk",
		SystemFingerprint: sessionID,
	}
	if resp.Usage != nil {
		chunk.Usage = &humeUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		}
	}
	return "", chunk, nil
}

func humeMessageText(content *schemas.ChatMessageContent) *string {
	if content == nil {
		return nil
	}
	if content.ContentStr != nil {
		return content.ContentStr
	}
	var text strings.Builder
	for _, block := range content.ContentBlocks {
		if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil {
			text.WriteString(*block.Text)
		}
	}
	if text.Len() == 0 {
		return nil
	}
	value := text.String()
	return &value
}

func toHumeToolCalls(toolCalls []schemas.ChatAssistantMessageToolCall) []humeToolCall {
	if len(toolCalls) == 0 {
		return nil
	}
	converted := make([]humeToolCall, len(toolCalls))
	for i, toolCall := range toolCalls {
		converted[i] = humeToolCall{
			Index:    toolCall.Index,
			Type:     toolCall.Type,
			ID:       toolCall.ID,
			Function: toolCall.Function,
		}
	}
	return converted
}

func toHumeNonStreamToolCalls(toolCalls []schemas.ChatAssistantMessageToolCall) []humeToolCall {
	converted := toHumeToolCalls(toolCalls)
	for i := range converted {
		converted[i].Index = uint16(i)
	}
	return converted
}

func humeErrorConverter(_ *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
	if err == nil || err.Error == nil {
		return &humeErrorEnvelope{Error: &schemas.ErrorField{Message: "An error occurred while processing the Hume request"}}
	}
	return &humeErrorEnvelope{Error: err.Error}
}
