package replicate

import (
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func ToReplicateResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) (*ReplicatePredictionRequest, error) {
	if bifrostReq == nil {
		return nil, fmt.Errorf("bifrost request is nil")
	}

	input := &ReplicatePredictionRequestInput{}

	if strings.HasPrefix(bifrostReq.Model, "openai/") && strings.Contains(bifrostReq.Model, "gpt-5-structured") {
		// handle responses style request
		if len(bifrostReq.Input) > 0 {
			input.InputItemList = bifrostReq.Input
		}
		if bifrostReq.Params != nil {
			if bifrostReq.Params.Instructions != nil {
				input.Instructions = bifrostReq.Params.Instructions
			}
			if bifrostReq.Params.Tools != nil {
				input.Tools = bifrostReq.Params.Tools
			}
			if bifrostReq.Params.MaxOutputTokens != nil {
				input.MaxOutputTokens = bifrostReq.Params.MaxOutputTokens
			}
			if bifrostReq.Params.Text != nil {
				input.JsonSchema = bifrostReq.Params.Text
			}
		}
	} else {
		// handle chat style request (same logic as chat converter)
		if len(bifrostReq.Input) > 0 {
			// if model is from openai family, use messages
			if strings.HasPrefix(bifrostReq.Model, string(schemas.OpenAI)) {
				input.Messages = schemas.ToChatMessages(bifrostReq.Input)
			} else {
				// convert input to prompt and system prompt
				var systemPrompt string
				var conversationParts []string
				var imageInput []string

				for _, msg := range bifrostReq.Input {
					if msg.Content == nil {
						continue
					}

					// Get message content as string
					var contentStr string
					if msg.Content.ContentStr != nil {
						contentStr = *msg.Content.ContentStr
					} else if msg.Content.ContentBlocks != nil {
						// Concatenate text blocks only
						var textParts []string
						for _, block := range msg.Content.ContentBlocks {
							if block.Text != nil && *block.Text != "" {
								textParts = append(textParts, *block.Text)
							}
							if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil && *block.ResponsesInputMessageContentBlockImage.ImageURL != "" {
								// add only non base64 image urls
								if !strings.HasPrefix(*block.ResponsesInputMessageContentBlockImage.ImageURL, "data:") {
									imageInput = append(imageInput, *block.ResponsesInputMessageContentBlockImage.ImageURL)
								}
							}
						}
						contentStr = strings.Join(textParts, "\n")
					}

					if contentStr == "" {
						continue
					}

					// Handle different roles
					if msg.Role != nil {
						switch *msg.Role {
						case schemas.ResponsesInputMessageRoleSystem:
							if systemPrompt == "" {
								systemPrompt = contentStr
							} else {
								systemPrompt += "\n" + contentStr
							}
						case schemas.ResponsesInputMessageRoleUser:
							conversationParts = append(conversationParts, contentStr)
						case schemas.ResponsesInputMessageRoleAssistant:
							// For assistant messages, we can include them in the conversation context
							conversationParts = append(conversationParts, contentStr)
						}
					}
				}

				// Set system prompt if present and model supports it
				modelSupportsSystemPrompt := supportsSystemPrompt(bifrostReq.Model)

				if systemPrompt != "" {
					if modelSupportsSystemPrompt {
						// Model supports system_prompt field
						input.SystemPrompt = &systemPrompt
					} else {
						// Model doesn't support system_prompt - prepend to prompt
						if len(conversationParts) > 0 {
							// Prepend system prompt to conversation
							conversationParts = append([]string{systemPrompt}, conversationParts...)
						} else {
							// No conversation parts, use system prompt as the prompt
							conversationParts = []string{systemPrompt}
						}
					}
				}

				// Build the final prompt from conversation parts
				if len(conversationParts) > 0 {
					prompt := strings.Join(conversationParts, "\n\n")
					input.Prompt = &prompt
				}

				if len(imageInput) > 0 {
					input.ImageInput = imageInput
				}
			}
		}

		// Map parameters if present
		if bifrostReq.Params != nil {
			params := bifrostReq.Params

			// Temperature
			if params.Temperature != nil {
				input.Temperature = params.Temperature
			}

			// Top P
			if params.TopP != nil {
				input.TopP = params.TopP
			}

			// Max tokens - use max_completion_tokens if available
			if params.MaxOutputTokens != nil {
				if strings.HasPrefix(bifrostReq.Model, string(schemas.OpenAI)) {
					input.MaxCompletionTokens = params.MaxOutputTokens
				} else {
					input.MaxTokens = params.MaxOutputTokens
				}
			}

			// Reasoning effort
			if params.Reasoning != nil {
				if params.Reasoning.Effort != nil {
					input.ReasoningEffort = params.Reasoning.Effort
				}
			}

			if params.Instructions != nil && *params.Instructions != "" {
				if supportsSystemPrompt(bifrostReq.Model) {
					if input.SystemPrompt == nil {
						input.SystemPrompt = params.Instructions
					}
				} else {
					if input.Prompt != nil && *input.Prompt != "" {
						prefixed := *params.Instructions + "\n\n" + *input.Prompt
						input.Prompt = schemas.Ptr(prefixed)
					} else if input.Prompt == nil {
						input.Prompt = params.Instructions
					}
				}
			}
		}
	}

	// Check if model is a version ID and set version field accordingly
	req := &ReplicatePredictionRequest{
		Input: input,
	}

	if isVersionID(bifrostReq.Model) {
		req.Version = &bifrostReq.Model
	}

	return req, nil
}

func (response *ReplicatePredictionResponse) ToBifrostResponsesResponse() *schemas.BifrostResponsesResponse {
	if response == nil {
		return nil
	}

	// Parse timestamps
	createdAt := ParseReplicateTimestamp(response.CreatedAt)
	if createdAt == 0 {
		createdAt = time.Now().Unix()
	}

	var completedAt *int
	if response.CompletedAt != nil {
		completed := int(ParseReplicateTimestamp(*response.CompletedAt))
		if completed > 0 {
			completedAt = &completed
		}
	}

	// Initialize Bifrost response
	bifrostResponse := &schemas.BifrostResponsesResponse{
		ID:          schemas.Ptr(response.ID),
		Model:       response.Model,
		CreatedAt:   int(createdAt),
		CompletedAt: completedAt,
	}

	// Convert output to ResponsesMessage
	var outputMessages []schemas.ResponsesMessage
	if response.Output != nil {
		var contentStr *string

		// Handle different output types
		if response.Output.OutputStr != nil {
			contentStr = response.Output.OutputStr
		} else if response.Output.OutputArray != nil {
			// Join array of strings into a single string
			joined := strings.Join(response.Output.OutputArray, "")
			contentStr = &joined
		} else if response.Output.OutputObject != nil && response.Output.OutputObject.Text != nil {
			// Use text field from OutputObject
			contentStr = response.Output.OutputObject.Text
		}

		if contentStr != nil && *contentStr != "" {
			messageType := schemas.ResponsesMessageTypeMessage
			role := schemas.ResponsesInputMessageRoleAssistant

			outputMsg := schemas.ResponsesMessage{
				Type: &messageType,
				Role: &role,
				Content: &schemas.ResponsesMessageContent{
					ContentStr: contentStr,
				},
			}
			outputMessages = append(outputMessages, outputMsg)
		}
	}

	bifrostResponse.Output = outputMessages

	// Set status based on prediction status
	var status string
	switch response.Status {
	case ReplicatePredictionStatusSucceeded:
		status = "completed"
	case ReplicatePredictionStatusFailed:
		status = "failed"
	case ReplicatePredictionStatusCanceled:
		status = "cancelled"
	case ReplicatePredictionStatusProcessing:
		status = "in_progress"
	case ReplicatePredictionStatusStarting:
		status = "queued"
	default:
		status = string(response.Status)
	}
	bifrostResponse.Status = &status

	// Set error if present
	if response.Error != nil && *response.Error != "" {
		bifrostResponse.Error = &schemas.ResponsesResponseError{
			Code:    "provider_error",
			Message: *response.Error,
		}
	}

	// Convert usage information from metrics
	if response.Metrics != nil {
		usage := &schemas.ResponsesResponseUsage{}

		if response.Metrics.InputTokenCount != nil {
			usage.InputTokens = *response.Metrics.InputTokenCount
		}

		if response.Metrics.OutputTokenCount != nil {
			usage.OutputTokens = *response.Metrics.OutputTokenCount
		}

		usage.TotalTokens = usage.InputTokens + usage.OutputTokens

		bifrostResponse.Usage = usage
	}

	return bifrostResponse
}
