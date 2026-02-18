package sapaicore

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
)

// Response pools for Bedrock and Vertex response objects
var (
	bedrockResponsePool = sync.Pool{
		New: func() interface{} {
			return &BedrockResponse{}
		},
	}

	vertexResponsePool = sync.Pool{
		New: func() interface{} {
			return &VertexGenerateContentResponse{}
		},
	}
)

// acquireBedrockResponse gets a BedrockResponse from the pool and resets it.
func acquireBedrockResponse() *BedrockResponse {
	resp := bedrockResponsePool.Get().(*BedrockResponse)
	*resp = BedrockResponse{} // Reset the struct
	return resp
}

// releaseBedrockResponse returns a BedrockResponse to the pool.
func releaseBedrockResponse(resp *BedrockResponse) {
	if resp != nil {
		bedrockResponsePool.Put(resp)
	}
}

// acquireVertexResponse gets a VertexGenerateContentResponse from the pool and resets it.
func acquireVertexResponse() *VertexGenerateContentResponse {
	resp := vertexResponsePool.Get().(*VertexGenerateContentResponse)
	*resp = VertexGenerateContentResponse{} // Reset the struct
	return resp
}

// releaseVertexResponse returns a VertexGenerateContentResponse to the pool.
func releaseVertexResponse(resp *VertexGenerateContentResponse) {
	if resp != nil {
		vertexResponsePool.Put(resp)
	}
}

// extractMediaType extracts the media type from a base64 data URL or returns a default.
// Handles formats like "data:image/png;base64,..." or plain base64 data.
func extractMediaType(url string) string {
	if strings.HasPrefix(url, "data:") {
		// Extract media type from data URL: data:image/png;base64,...
		if idx := strings.Index(url, ";"); idx > 5 {
			return url[5:idx] // Skip "data:" prefix
		}
		if idx := strings.Index(url, ","); idx > 5 {
			return url[5:idx]
		}
	}
	// Default to JPEG for unknown formats
	return "image/jpeg"
}

// BedrockStreamEvent represents a streaming event from Bedrock
type BedrockStreamEvent struct {
	Delta      *BedrockDelta `json:"delta,omitempty"`
	StopReason *string       `json:"stop_reason,omitempty"`
	Usage      *BedrockUsage `json:"usage,omitempty"`
}

// BedrockDelta represents delta content in streaming
type BedrockDelta struct {
	Type string  `json:"type,omitempty"`
	Text *string `json:"text,omitempty"`
}

// BedrockUsage represents usage information from Bedrock
type BedrockUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens,omitempty"`
}

// BedrockRequest represents a request to Bedrock-compatible API
type BedrockRequest struct {
	AnthropicVersion string           `json:"anthropic_version,omitempty"`
	MaxTokens        int              `json:"max_tokens"`
	Messages         []BedrockMessage `json:"messages"`
	System           string           `json:"system,omitempty"`
	Temperature      *float64         `json:"temperature,omitempty"`
	TopP             *float64         `json:"top_p,omitempty"`
	TopK             *int             `json:"top_k,omitempty"`
	StopSequences    []string         `json:"stop_sequences,omitempty"`
}

// BedrockMessage represents a message in Bedrock format
type BedrockMessage struct {
	Role    string                `json:"role"`
	Content []BedrockContentBlock `json:"content"`
}

// BedrockContentBlock represents a content block in Bedrock format
type BedrockContentBlock struct {
	Type   string              `json:"type"`
	Text   string              `json:"text,omitempty"`
	Source *BedrockImageSource `json:"source,omitempty"`
}

// BedrockImageSource represents an image source in Bedrock format
type BedrockImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// BedrockResponse represents a response from Bedrock-compatible API
type BedrockResponse struct {
	ID           string                `json:"id"`
	Type         string                `json:"type"`
	Role         string                `json:"role"`
	Content      []BedrockContentBlock `json:"content"`
	Model        string                `json:"model"`
	StopReason   string                `json:"stop_reason"`
	StopSequence *string               `json:"stop_sequence,omitempty"`
	Usage        *BedrockUsage         `json:"usage,omitempty"`
}

// VertexGenerateContentRequest represents a request to Vertex AI
type VertexGenerateContentRequest struct {
	Contents          []VertexContent         `json:"contents"`
	SystemInstruction *VertexContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *VertexGenerationConfig `json:"generationConfig,omitempty"`
}

// VertexContent represents content in Vertex format
type VertexContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []VertexPart `json:"parts"`
}

// VertexPart represents a part in Vertex content
type VertexPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *VertexInlineData `json:"inlineData,omitempty"`
}

// VertexInlineData represents inline data (images) in Vertex format
type VertexInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// VertexGenerationConfig represents generation config for Vertex
type VertexGenerationConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	TopK            *int     `json:"topK,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

// VertexGenerateContentResponse represents a response from Vertex AI
type VertexGenerateContentResponse struct {
	Candidates    []VertexCandidate    `json:"candidates"`
	UsageMetadata *VertexUsageMetadata `json:"usageMetadata,omitempty"`
}

// VertexCandidate represents a candidate in Vertex response
type VertexCandidate struct {
	Content      VertexContent `json:"content"`
	FinishReason string        `json:"finishReason,omitempty"`
	Index        int           `json:"index"`
}

// VertexUsageMetadata represents usage metadata from Vertex
type VertexUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// convertToBedrock converts a Bifrost chat request to Bedrock format
func convertToBedrock(request *schemas.BifrostChatRequest) *BedrockRequest {
	bedrockReq := &BedrockRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        4096, // Default
	}

	// Get model config for max tokens
	config := GetModelConfig(request.Model)
	bedrockReq.MaxTokens = config.MaxTokens

	// Convert messages from Input field
	var systemMessage string
	for _, msg := range request.Input {
		if msg.Role == schemas.ChatMessageRoleSystem {
			// Extract system message
			if msg.Content != nil && msg.Content.ContentStr != nil {
				systemMessage = *msg.Content.ContentStr
			}
			continue
		}

		bedrockMsg := BedrockMessage{
			Role: string(msg.Role),
		}

		// Convert content
		if msg.Content != nil {
			if msg.Content.ContentStr != nil {
				bedrockMsg.Content = []BedrockContentBlock{
					{Type: "text", Text: *msg.Content.ContentStr},
				}
			} else if msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil {
						bedrockMsg.Content = append(bedrockMsg.Content, BedrockContentBlock{
							Type: "text",
							Text: *block.Text,
						})
					} else if block.Type == schemas.ChatContentBlockTypeImage && block.ImageURLStruct != nil {
						// Handle image URL - extract base64 data
						bedrockMsg.Content = append(bedrockMsg.Content, BedrockContentBlock{
							Type: "image",
							Source: &BedrockImageSource{
								Type:      "base64",
								MediaType: extractMediaType(block.ImageURLStruct.URL),
								Data:      block.ImageURLStruct.URL,
							},
						})
					}
				}
			}
		}

		bedrockReq.Messages = append(bedrockReq.Messages, bedrockMsg)
	}

	bedrockReq.System = systemMessage

	// Copy generation parameters from Params
	if request.Params != nil {
		if request.Params.Temperature != nil {
			bedrockReq.Temperature = request.Params.Temperature
		}
		if request.Params.TopP != nil {
			bedrockReq.TopP = request.Params.TopP
		}
		if request.Params.MaxCompletionTokens != nil {
			bedrockReq.MaxTokens = *request.Params.MaxCompletionTokens
		}
		if request.Params.Stop != nil {
			bedrockReq.StopSequences = request.Params.Stop
		}
	}

	return bedrockReq
}

// parseBedrockResponse parses a Bedrock response into Bifrost format.
// Uses object pooling for efficient memory reuse.
func parseBedrockResponse(body []byte, model string) (*schemas.BifrostChatResponse, error) {
	bedrockResp := acquireBedrockResponse()
	defer releaseBedrockResponse(bedrockResp)

	if err := sonic.Unmarshal(body, bedrockResp); err != nil {
		return nil, err
	}

	// Extract text content
	var content string
	for _, block := range bedrockResp.Content {
		if block.Type == "text" {
			content += block.Text
		}
	}

	// Map stop reason
	finishReason := mapBedrockStopReason(bedrockResp.StopReason)

	// Create ChatMessage for the response
	assistantRole := schemas.ChatMessageRoleAssistant
	responseMessage := &schemas.ChatMessage{
		Role: assistantRole,
		Content: &schemas.ChatMessageContent{
			ContentStr: &content,
		},
	}

	response := &schemas.BifrostChatResponse{
		ID:      bedrockResp.ID,
		Object:  "chat.completion",
		Created: int(time.Now().Unix()),
		Model:   model,
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: responseMessage,
				},
				FinishReason: &finishReason,
			},
		},
	}

	if bedrockResp.Usage != nil {
		response.Usage = &schemas.BifrostLLMUsage{
			PromptTokens:     bedrockResp.Usage.InputTokens,
			CompletionTokens: bedrockResp.Usage.OutputTokens,
			TotalTokens:      bedrockResp.Usage.InputTokens + bedrockResp.Usage.OutputTokens,
		}
	}

	return response, nil
}

// mapBedrockStopReason maps Bedrock stop reasons to OpenAI format
func mapBedrockStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	default:
		return reason
	}
}

// convertToVertex converts a Bifrost chat request to Vertex AI format
func convertToVertex(request *schemas.BifrostChatRequest) *VertexGenerateContentRequest {
	vertexReq := &VertexGenerateContentRequest{}

	// Convert messages from Input field
	for _, msg := range request.Input {
		if msg.Role == schemas.ChatMessageRoleSystem {
			// Handle system message
			if msg.Content != nil && msg.Content.ContentStr != nil {
				vertexReq.SystemInstruction = &VertexContent{
					Parts: []VertexPart{{Text: *msg.Content.ContentStr}},
				}
			}
			continue
		}

		vertexContent := VertexContent{
			Role: mapToVertexRole(string(msg.Role)),
		}

		// Convert content
		if msg.Content != nil {
			if msg.Content.ContentStr != nil {
				vertexContent.Parts = []VertexPart{{Text: *msg.Content.ContentStr}}
			} else if msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil {
						vertexContent.Parts = append(vertexContent.Parts, VertexPart{
							Text: *block.Text,
						})
					} else if block.Type == schemas.ChatContentBlockTypeImage && block.ImageURLStruct != nil {
						vertexContent.Parts = append(vertexContent.Parts, VertexPart{
							InlineData: &VertexInlineData{
								MimeType: extractMediaType(block.ImageURLStruct.URL),
								Data:     block.ImageURLStruct.URL,
							},
						})
					}
				}
			}
		}

		vertexReq.Contents = append(vertexReq.Contents, vertexContent)
	}

	// Set generation config
	config := GetModelConfig(request.Model)
	vertexReq.GenerationConfig = &VertexGenerationConfig{
		MaxOutputTokens: &config.MaxTokens,
	}

	// Copy generation parameters from Params
	if request.Params != nil {
		if request.Params.Temperature != nil {
			vertexReq.GenerationConfig.Temperature = request.Params.Temperature
		}
		if request.Params.TopP != nil {
			vertexReq.GenerationConfig.TopP = request.Params.TopP
		}
		if request.Params.MaxCompletionTokens != nil {
			vertexReq.GenerationConfig.MaxOutputTokens = request.Params.MaxCompletionTokens
		}
		if request.Params.Stop != nil {
			vertexReq.GenerationConfig.StopSequences = request.Params.Stop
		}
	}

	return vertexReq
}

// mapToVertexRole maps OpenAI roles to Vertex AI roles
func mapToVertexRole(role string) string {
	switch role {
	case "assistant":
		return "model"
	case "user":
		return "user"
	default:
		return role
	}
}

// parseVertexResponse parses a Vertex AI response into Bifrost format.
// Uses object pooling for efficient memory reuse.
func parseVertexResponse(body []byte, model string) (*schemas.BifrostChatResponse, error) {
	vertexResp := acquireVertexResponse()
	defer releaseVertexResponse(vertexResp)

	if err := sonic.Unmarshal(body, vertexResp); err != nil {
		return nil, err
	}

	// Extract content from first candidate
	var content string
	var finishReason string
	if len(vertexResp.Candidates) > 0 {
		candidate := vertexResp.Candidates[0]
		for _, part := range candidate.Content.Parts {
			content += part.Text
		}
		finishReason = mapVertexFinishReason(candidate.FinishReason)
	}

	// Create ChatMessage for the response
	assistantRole := schemas.ChatMessageRoleAssistant
	responseMessage := &schemas.ChatMessage{
		Role: assistantRole,
		Content: &schemas.ChatMessageContent{
			ContentStr: &content,
		},
	}

	response := &schemas.BifrostChatResponse{
		ID:      "",
		Object:  "chat.completion",
		Created: int(time.Now().Unix()),
		Model:   model,
		Choices: []schemas.BifrostResponseChoice{
			{
				Index: 0,
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: responseMessage,
				},
				FinishReason: &finishReason,
			},
		},
	}

	if vertexResp.UsageMetadata != nil {
		response.Usage = &schemas.BifrostLLMUsage{
			PromptTokens:     vertexResp.UsageMetadata.PromptTokenCount,
			CompletionTokens: vertexResp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      vertexResp.UsageMetadata.TotalTokenCount,
		}
	}

	return response, nil
}

// mapVertexFinishReason maps Vertex AI finish reasons to OpenAI format
func mapVertexFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY":
		return "content_filter"
	case "RECITATION":
		return "content_filter"
	default:
		return "stop"
	}
}

// convertResponsesToBedrock converts a Bifrost Responses request to Bedrock format
func convertResponsesToBedrock(request *schemas.BifrostResponsesRequest) *BedrockRequest {
	bedrockReq := &BedrockRequest{
		AnthropicVersion: "bedrock-2023-05-31",
		MaxTokens:        4096, // Default
	}

	// Get model config for max tokens
	config := GetModelConfig(request.Model)
	bedrockReq.MaxTokens = config.MaxTokens

	// Convert messages from Input field
	var systemMessage string
	for _, msg := range request.Input {
		// Handle system messages
		if msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleSystem {
			if msg.Content != nil {
				if msg.Content.ContentStr != nil {
					systemMessage = *msg.Content.ContentStr
				} else if msg.Content.ContentBlocks != nil {
					// Extract text from content blocks
					for _, block := range msg.Content.ContentBlocks {
						if block.Text != nil {
							systemMessage += *block.Text
						}
					}
				}
			}
			continue
		}

		// Skip messages without role
		if msg.Role == nil {
			continue
		}

		bedrockMsg := BedrockMessage{
			Role: string(*msg.Role),
		}

		// Convert content
		if msg.Content != nil {
			if msg.Content.ContentStr != nil {
				bedrockMsg.Content = []BedrockContentBlock{
					{Type: "text", Text: *msg.Content.ContentStr},
				}
			} else if msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					switch block.Type {
					case schemas.ResponsesInputMessageContentBlockTypeText,
						schemas.ResponsesOutputMessageContentTypeText:
						if block.Text != nil {
							bedrockMsg.Content = append(bedrockMsg.Content, BedrockContentBlock{
								Type: "text",
								Text: *block.Text,
							})
						}
					case schemas.ResponsesInputMessageContentBlockTypeImage:
						if block.ImageURL != nil {
							bedrockMsg.Content = append(bedrockMsg.Content, BedrockContentBlock{
								Type: "image",
								Source: &BedrockImageSource{
									Type:      "base64",
									MediaType: extractMediaType(*block.ImageURL),
									Data:      extractBase64Data(*block.ImageURL),
								},
							})
						}
					}
				}
			}
		}

		bedrockReq.Messages = append(bedrockReq.Messages, bedrockMsg)
	}

	bedrockReq.System = systemMessage

	// Copy generation parameters from Params
	if request.Params != nil {
		if request.Params.Temperature != nil {
			bedrockReq.Temperature = request.Params.Temperature
		}
		if request.Params.TopP != nil {
			bedrockReq.TopP = request.Params.TopP
		}
		if request.Params.MaxOutputTokens != nil {
			bedrockReq.MaxTokens = *request.Params.MaxOutputTokens
		}
	}

	return bedrockReq
}

// extractBase64Data extracts the base64 data from a data URL or returns the URL as-is if it's already base64
func extractBase64Data(url string) string {
	if strings.HasPrefix(url, "data:") {
		// Extract base64 data from data URL: data:image/png;base64,...
		if idx := strings.Index(url, ","); idx > 0 {
			return url[idx+1:]
		}
	}
	// Return as-is (assume it's already base64 encoded)
	return url
}

// parseBedrockToResponsesResponse parses a Bedrock response into Bifrost Responses format.
// Uses object pooling for efficient memory reuse.
func parseBedrockToResponsesResponse(body []byte, model string) (*schemas.BifrostResponsesResponse, error) {
	bedrockResp := acquireBedrockResponse()
	defer releaseBedrockResponse(bedrockResp)

	if err := sonic.Unmarshal(body, bedrockResp); err != nil {
		return nil, err
	}

	// Build output messages from Bedrock response
	var outputMessages []schemas.ResponsesMessage

	// Extract text content and build output message
	var textContent string
	for _, block := range bedrockResp.Content {
		if block.Type == "text" {
			textContent += block.Text
		}
	}

	if textContent != "" {
		outputType := schemas.ResponsesMessageTypeMessage
		role := schemas.ResponsesInputMessageRoleAssistant
		contentBlockType := schemas.ResponsesOutputMessageContentTypeText

		outputMessages = append(outputMessages, schemas.ResponsesMessage{
			Type:   &outputType,
			Role:   &role,
			Status: schemas.Ptr("completed"),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{
						Type: contentBlockType,
						Text: &textContent,
						ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
							Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
							LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
						},
					},
				},
			},
		})
	}

	// Map stop reason
	stopReason := mapBedrockStopReasonToResponses(bedrockResp.StopReason)

	response := &schemas.BifrostResponsesResponse{
		ID:         &bedrockResp.ID,
		Object:     "response",
		CreatedAt:  int(time.Now().Unix()),
		Model:      model,
		Output:     outputMessages,
		Status:     schemas.Ptr("completed"),
		StopReason: &stopReason,
	}

	if bedrockResp.Usage != nil {
		response.Usage = &schemas.ResponsesResponseUsage{
			InputTokens:  bedrockResp.Usage.InputTokens,
			OutputTokens: bedrockResp.Usage.OutputTokens,
			TotalTokens:  bedrockResp.Usage.InputTokens + bedrockResp.Usage.OutputTokens,
		}
	}

	return response, nil
}

// mapBedrockStopReasonToResponses maps Bedrock stop reasons to Responses API format
func mapBedrockStopReasonToResponses(reason string) string {
	switch reason {
	case "end_turn":
		return "end_turn"
	case "max_tokens":
		return "max_output_tokens"
	case "stop_sequence":
		return "stop_sequence"
	case "tool_use":
		return "tool_calls"
	default:
		return reason
	}
}

// BedrockResponsesStreamState tracks state during streaming conversion for responses API
type BedrockResponsesStreamState struct {
	MessageID            *string
	Model                *string
	CreatedAt            int
	SequenceNumber       int
	HasEmittedCreated    bool
	HasEmittedInProgress bool
	TextItemAdded        bool
	ContentPartAdded     bool
	AccumulatedText      string
	ItemID               string
}

// newBedrockResponsesStreamState creates a new stream state for Bedrock responses streaming
func newBedrockResponsesStreamState() *BedrockResponsesStreamState {
	return &BedrockResponsesStreamState{
		CreatedAt:      int(time.Now().Unix()),
		SequenceNumber: 0,
	}
}

// processBedrockResponsesEventStream processes Bedrock event stream and sends chunks to the channel
// for Responses API format
func processBedrockResponsesEventStream(
	ctx *schemas.BifrostContext,
	bodyStream io.Reader,
	responseChan chan *schemas.BifrostStreamChunk,
	postHookRunner schemas.PostHookRunner,
	providerName schemas.ModelProvider,
	model string,
	logger schemas.Logger,
) {
	scanner := bufio.NewScanner(bodyStream)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	state := newBedrockResponsesStreamState()
	state.Model = &model
	state.MessageID = schemas.Ptr(fmt.Sprintf("resp_%d", time.Now().UnixNano()))
	state.ItemID = fmt.Sprintf("msg_%s_item_0", *state.MessageID)

	usage := &schemas.ResponsesResponseUsage{}
	var stopReason *string
	startTime := time.Now()
	chunkIndex := 0

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}

		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		// Parse Bedrock event stream format
		if strings.HasPrefix(line, "data: ") {
			jsonData := strings.TrimPrefix(line, "data: ")

			var bedrockEvent BedrockStreamEvent
			if err := sonic.Unmarshal([]byte(jsonData), &bedrockEvent); err != nil {
				logger.Warn("Failed to parse Bedrock stream event: %v", err)
				continue
			}

			// Convert to Bifrost Responses stream responses
			responses := convertBedrockEventToResponses(bedrockEvent, state, providerName, model)

			// Update usage
			if bedrockEvent.Usage != nil {
				usage.InputTokens = bedrockEvent.Usage.InputTokens
				usage.OutputTokens = bedrockEvent.Usage.OutputTokens
				usage.TotalTokens = bedrockEvent.Usage.InputTokens + bedrockEvent.Usage.OutputTokens
			}

			// Update stop reason
			if bedrockEvent.StopReason != nil {
				stopReason = bedrockEvent.StopReason
			}

			// Send each response
			for _, response := range responses {
				if response != nil {
					response.ExtraFields = schemas.BifrostResponseExtraFields{
						RequestType:    schemas.ResponsesStreamRequest,
						Provider:       providerName,
						ModelRequested: model,
						ChunkIndex:     chunkIndex,
					}
					chunkIndex++
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil, nil), responseChan)
				}
			}
		}
	}

	// Emit final events: content_part.done, output_item.done, response.completed
	finalResponses := emitFinalResponseEvents(state, stopReason, usage, providerName, model, startTime)
	for _, response := range finalResponses {
		if response != nil {
			response.ExtraFields = schemas.BifrostResponseExtraFields{
				RequestType:    schemas.ResponsesStreamRequest,
				Provider:       providerName,
				ModelRequested: model,
				ChunkIndex:     chunkIndex,
				Latency:        time.Since(startTime).Milliseconds(),
			}
			chunkIndex++
			if response.Type == schemas.ResponsesStreamResponseTypeCompleted {
				ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			}
			providerUtils.ProcessAndSendResponse(ctx, postHookRunner, providerUtils.GetBifrostResponseForStreamResponse(nil, nil, response, nil, nil, nil), responseChan)
		}
	}
}

// convertBedrockEventToResponses converts a Bedrock stream event to Bifrost Responses stream responses
func convertBedrockEventToResponses(
	event BedrockStreamEvent,
	state *BedrockResponsesStreamState,
	providerName schemas.ModelProvider,
	model string,
) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Emit lifecycle events if not already done
	if !state.HasEmittedCreated {
		// Emit response.created
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeCreated,
			SequenceNumber: state.SequenceNumber,
			Response: &schemas.BifrostResponsesResponse{
				ID:        state.MessageID,
				Object:    "response",
				CreatedAt: state.CreatedAt,
				Model:     model,
				Status:    schemas.Ptr("in_progress"),
			},
		})
		state.SequenceNumber++
		state.HasEmittedCreated = true

		// Emit response.in_progress
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeInProgress,
			SequenceNumber: state.SequenceNumber,
			Response: &schemas.BifrostResponsesResponse{
				ID:        state.MessageID,
				Object:    "response",
				CreatedAt: state.CreatedAt,
				Model:     model,
				Status:    schemas.Ptr("in_progress"),
			},
		})
		state.SequenceNumber++
		state.HasEmittedInProgress = true
	}

	// Handle text delta
	if event.Delta != nil && event.Delta.Text != nil {
		text := *event.Delta.Text
		state.AccumulatedText += text

		// Add output item if not already added
		if !state.TextItemAdded {
			messageType := schemas.ResponsesMessageTypeMessage
			role := schemas.ResponsesInputMessageRoleAssistant

			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
				SequenceNumber: state.SequenceNumber,
				OutputIndex:    schemas.Ptr(0),
				Item: &schemas.ResponsesMessage{
					ID:   &state.ItemID,
					Type: &messageType,
					Role: &role,
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{},
					},
				},
			})
			state.SequenceNumber++
			state.TextItemAdded = true
		}

		// Add content part if not already added
		if !state.ContentPartAdded {
			emptyText := ""
			responses = append(responses, &schemas.BifrostResponsesStreamResponse{
				Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
				SequenceNumber: state.SequenceNumber,
				OutputIndex:    schemas.Ptr(0),
				ContentIndex:   schemas.Ptr(0),
				ItemID:         &state.ItemID,
				Part: &schemas.ResponsesMessageContentBlock{
					Type: schemas.ResponsesOutputMessageContentTypeText,
					Text: &emptyText,
					ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
						LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
						Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
					},
				},
			})
			state.SequenceNumber++
			state.ContentPartAdded = true
		}

		// Emit text delta
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
			SequenceNumber: state.SequenceNumber,
			OutputIndex:    schemas.Ptr(0),
			ContentIndex:   schemas.Ptr(0),
			ItemID:         &state.ItemID,
			Delta:          &text,
		})
		state.SequenceNumber++
	}

	return responses
}

// emitFinalResponseEvents emits the final events to complete the stream
func emitFinalResponseEvents(
	state *BedrockResponsesStreamState,
	stopReason *string,
	usage *schemas.ResponsesResponseUsage,
	providerName schemas.ModelProvider,
	model string,
	startTime time.Time,
) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Map stop reason
	var mappedStopReason string
	if stopReason != nil {
		mappedStopReason = mapBedrockStopReasonToResponses(*stopReason)
	} else {
		mappedStopReason = "end_turn"
	}

	// Emit output_text.done with full accumulated text
	if state.TextItemAdded {
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputTextDone,
			SequenceNumber: state.SequenceNumber,
			OutputIndex:    schemas.Ptr(0),
			ContentIndex:   schemas.Ptr(0),
			ItemID:         &state.ItemID,
			Text:           &state.AccumulatedText,
		})
		state.SequenceNumber++
	}

	// Emit content_part.done
	if state.ContentPartAdded {
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
			SequenceNumber: state.SequenceNumber,
			OutputIndex:    schemas.Ptr(0),
			ContentIndex:   schemas.Ptr(0),
			ItemID:         &state.ItemID,
			Part: &schemas.ResponsesMessageContentBlock{
				Type: schemas.ResponsesOutputMessageContentTypeText,
				Text: &state.AccumulatedText,
				ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
					LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
					Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
				},
			},
		})
		state.SequenceNumber++
	}

	// Emit output_item.done
	if state.TextItemAdded {
		messageType := schemas.ResponsesMessageTypeMessage
		role := schemas.ResponsesInputMessageRoleAssistant
		contentBlockType := schemas.ResponsesOutputMessageContentTypeText

		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
			SequenceNumber: state.SequenceNumber,
			OutputIndex:    schemas.Ptr(0),
			Item: &schemas.ResponsesMessage{
				ID:     &state.ItemID,
				Type:   &messageType,
				Role:   &role,
				Status: schemas.Ptr("completed"),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: contentBlockType,
							Text: &state.AccumulatedText,
							ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
								LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
								Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
							},
						},
					},
				},
			},
		})
		state.SequenceNumber++
	}

	// Emit response.completed
	completedAt := int(time.Now().Unix())
	messageType := schemas.ResponsesMessageTypeMessage
	role := schemas.ResponsesInputMessageRoleAssistant
	contentBlockType := schemas.ResponsesOutputMessageContentTypeText

	var outputMessages []schemas.ResponsesMessage
	if state.TextItemAdded {
		outputMessages = []schemas.ResponsesMessage{
			{
				ID:     &state.ItemID,
				Type:   &messageType,
				Role:   &role,
				Status: schemas.Ptr("completed"),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: contentBlockType,
							Text: &state.AccumulatedText,
							ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
								LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
								Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
							},
						},
					},
				},
			},
		}
	}

	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeCompleted,
		SequenceNumber: state.SequenceNumber,
		Response: &schemas.BifrostResponsesResponse{
			ID:          state.MessageID,
			Object:      "response",
			CreatedAt:   state.CreatedAt,
			CompletedAt: &completedAt,
			Model:       model,
			Status:      schemas.Ptr("completed"),
			StopReason:  &mappedStopReason,
			Output:      outputMessages,
			Usage:       usage,
		},
	})
	state.SequenceNumber++

	return responses
}
