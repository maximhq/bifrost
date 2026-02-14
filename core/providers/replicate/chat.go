package replicate

import (
	"fmt"
	"slices"
	"strings"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// unsupportedSystemPromptModels is a set of models that don't support the system_prompt field.
var unsupportedSystemPromptModels = []string{
	"meta/meta-llama-3-8b",
	"meta/llama-2-70b",
	"openai/gpt-oss-20b",
	"openai/o1-mini",
	"xai/grok-4",
}

func ToReplicateChatRequest(bifrostReq *schemas.BifrostChatRequest) (*ReplicatePredictionRequest, error) {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil, fmt.Errorf("bifrost request is nil or input is nil")
	}

	// Build the input from messages
	input := &ReplicatePredictionRequestInput{}

	isGPT5Structured := strings.HasPrefix(bifrostReq.Model, string(schemas.OpenAI)) && strings.Contains(bifrostReq.Model, "gpt-5-structured")

	// openai models support messages
	if len(bifrostReq.Input) > 0 && strings.HasPrefix(bifrostReq.Model, string(schemas.OpenAI)) {
		if isGPT5Structured {
			responsesMessages := []schemas.ResponsesMessage{}
			for _, msg := range bifrostReq.Input {
				responsesMessages = append(responsesMessages, msg.ToResponsesMessages()...)
			}
			if len(responsesMessages) > 0 {
				input.InputItemList = responsesMessages
			}
		} else {
			input.Messages = bifrostReq.Input
		}
	} else {
		// Extract system prompt and build conversation prompt
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
					if block.ImageURLStruct != nil && block.ImageURLStruct.URL != "" {
						imageInput = append(imageInput, block.ImageURLStruct.URL)
					}
				}
				contentStr = strings.Join(textParts, "\n")
			}

			if contentStr == "" {
				continue
			}

			// Handle different roles
			switch msg.Role {
			case schemas.ChatMessageRoleSystem:
				if systemPrompt == "" {
					systemPrompt = contentStr
				} else {
					systemPrompt += "\n" + contentStr
				}
			case schemas.ChatMessageRoleUser:
				conversationParts = append(conversationParts, contentStr)
			case schemas.ChatMessageRoleAssistant:
				// For assistant messages, we can include them in the conversation context
				conversationParts = append(conversationParts, contentStr)
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

		// Ensure we have at least some content (prompt or system prompt)
		if input.Prompt == nil && input.SystemPrompt == nil {
			return nil, fmt.Errorf("no content found in chat messages - need at least one user or system message")
		}

		if len(imageInput) > 0 {
			input.ImageInput = imageInput
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
		if params.MaxCompletionTokens != nil {
			if isGPT5Structured {
				input.MaxOutputTokens = params.MaxCompletionTokens
			} else if strings.HasPrefix(bifrostReq.Model, string(schemas.OpenAI)) {
				input.MaxCompletionTokens = params.MaxCompletionTokens
			} else {
				input.MaxTokens = params.MaxCompletionTokens
			}
		}

		// Presence penalty
		if params.PresencePenalty != nil {
			input.PresencePenalty = params.PresencePenalty
		}

		// Frequency penalty
		if params.FrequencyPenalty != nil {
			input.FrequencyPenalty = params.FrequencyPenalty
		}

		// Seed
		if params.Seed != nil {
			input.Seed = params.Seed
		}

		if params.Reasoning != nil {
			if params.Reasoning.Effort != nil {
				input.ReasoningEffort = params.Reasoning.Effort
			}
		}

		if isGPT5Structured {
			if len(params.Tools) > 0 {
				responsesTools := []schemas.ResponsesTool{}
				for _, tool := range params.Tools {
					responsesTools = append(responsesTools, *tool.ToResponsesTool())
				}
				if len(responsesTools) > 0 {
					input.Tools = responsesTools
				}
			}
		}

		if params.ExtraParams != nil {
			input.ExtraParams = params.ExtraParams
		}
	}

	// Check if model is a version ID and set version field accordingly
	req := &ReplicatePredictionRequest{
		Input: input,
	}

	if isVersionID(bifrostReq.Model) {
		req.Version = &bifrostReq.Model
	}

	if bifrostReq.Params != nil && bifrostReq.Params.ExtraParams != nil {
		if webhook, ok := schemas.SafeExtractStringPointer(bifrostReq.Params.ExtraParams["webhook"]); ok {
			req.Webhook = webhook
		}
		if webhookEventsFilter, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["webhook_events_filter"]); ok {
			req.WebhookEventsFilter = webhookEventsFilter
		}
	}

	return req, nil
}

// ToBifrostChatResponse converts a Replicate prediction response to Bifrost format
func (response *ReplicatePredictionResponse) ToBifrostChatResponse() *schemas.BifrostChatResponse {
	if response == nil {
		return nil
	}

	// Parse timestamps
	createdAt := ParseReplicateTimestamp(response.CreatedAt)
	if createdAt == 0 {
		createdAt = time.Now().Unix()
	}

	// Initialize Bifrost response
	bifrostResponse := &schemas.BifrostChatResponse{
		ID:      response.ID,
		Model:   response.Model,
		Object:  "chat.completion",
		Created: int(createdAt),
	}

	// Convert output to content
	var contentStr *string
	if response.Output != nil {
		if response.Output.OutputStr != nil {
			contentStr = response.Output.OutputStr
		} else if response.Output.OutputArray != nil {
			// Join array of strings into a single string
			joined := strings.Join(response.Output.OutputArray, "")
			contentStr = &joined
		} else if response.Output.OutputObject != nil && response.Output.OutputObject.Text != nil {
			contentStr = response.Output.OutputObject.Text
		}
	}

	// Create message content
	messageContent := schemas.ChatMessageContent{
		ContentStr: contentStr,
	}

	// Create the assistant message
	message := schemas.ChatMessage{
		Role:    schemas.ChatMessageRoleAssistant,
		Content: &messageContent,
	}

	// Determine finish reason based on status
	var finishReason *string
	switch response.Status {
	case ReplicatePredictionStatusSucceeded:
		reason := "stop"
		finishReason = &reason
	case ReplicatePredictionStatusFailed:
		reason := "error"
		finishReason = &reason
	case ReplicatePredictionStatusCanceled:
		reason := "stop"
		finishReason = &reason
	}

	// Create choice
	choice := schemas.BifrostResponseChoice{
		Index: 0,
		ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
			Message: &message,
		},
		FinishReason: finishReason,
	}

	bifrostResponse.Choices = []schemas.BifrostResponseChoice{choice}

	// Extract usage information from logs
	if response.Logs != nil {
		inputTokens, outputTokens, totalTokens, found := parseTokenUsageFromLogs(response.Logs, schemas.ChatCompletionRequest)
		if found {
			bifrostResponse.Usage = &schemas.BifrostLLMUsage{
				PromptTokens:     inputTokens,
				CompletionTokens: outputTokens,
				TotalTokens:      totalTokens,
			}
		}
	}

	return bifrostResponse
}

// supportsSystemPrompt checks if a model supports the system_prompt field.
func supportsSystemPrompt(model string) bool {
	// Normalize model name to lowercase for comparison
	modelLower := strings.ToLower(model)

	// Extract model identifier (handle both "owner/name" and "owner/name:version" formats)
	modelIdentifier := modelLower
	if idx := strings.Index(modelLower, ":"); idx != -1 {
		modelIdentifier = modelLower[:idx]
	}

	// All deepseek models don't support system prompt
	if strings.HasPrefix(modelIdentifier, "deepseek-ai/deepseek") {
		return false
	}

	isUnsupported := slices.Contains(unsupportedSystemPromptModels, modelIdentifier)
	return !isUnsupported
}
