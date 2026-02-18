package sapaicore

import (
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
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
