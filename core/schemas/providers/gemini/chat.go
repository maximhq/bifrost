package gemini

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func (r *GeminiGenerationRequest) ToBifrostRequest() *schemas.BifrostChatRequest {
	provider, model := schemas.ParseModelString(r.Model, schemas.Gemini)

	if provider == schemas.Vertex && !r.IsEmbedding {
		// Add google/ prefix for Bifrost if not already present
		if !strings.HasPrefix(model, "google/") {
			model = "google/" + model
		}
	}

	// Handle chat completion requests
	bifrostReq := &schemas.BifrostChatRequest{
		Provider: provider,
		Model:    model,
		Input:    []schemas.ChatMessage{},
	}

	messages := []schemas.ChatMessage{}

	allGenAiMessages := []Content{}
	if r.SystemInstruction != nil {
		allGenAiMessages = append(allGenAiMessages, r.SystemInstruction.ToGenAIContent())
	}
	for _, content := range r.Contents {
		allGenAiMessages = append(allGenAiMessages, content.ToGenAIContent())
	}

	for _, content := range allGenAiMessages {
		if len(content.Parts) == 0 {
			continue
		}

		// Handle multiple parts - collect all content and tool calls
		var toolCalls []schemas.ChatAssistantMessageToolCall
		var contentBlocks []schemas.ChatContentBlock
		var thoughtStr string // Track thought content for assistant/model

		for _, part := range content.Parts {
			switch {
			case part.Text != "":
				// Handle thought content specially for assistant messages
				if part.Thought &&
					(content.Role == string(schemas.ChatMessageRoleAssistant) || content.Role == string(RoleModel)) {
					thoughtStr = thoughtStr + part.Text + "\n"
				} else {
					contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
						Type: schemas.ChatContentBlockTypeText,
						Text: &part.Text,
					})
				}

			case part.FunctionCall != nil:
				// Only add function calls for assistant messages
				if content.Role == string(schemas.ChatMessageRoleAssistant) || content.Role == string(RoleModel) {
					jsonArgs, err := json.Marshal(part.FunctionCall.Args)
					if err != nil {
						jsonArgs = []byte(fmt.Sprintf("%v", part.FunctionCall.Args))
					}
					name := part.FunctionCall.Name // create local copy
					// Gemini primarily works with function names for correlation
					// Use ID if provided, otherwise fallback to name for stable correlation
					callID := name
					if strings.TrimSpace(part.FunctionCall.ID) != "" {
						callID = part.FunctionCall.ID
					}
					toolCall := schemas.ChatAssistantMessageToolCall{
						ID:   schemas.Ptr(callID),
						Type: schemas.Ptr(string(schemas.ChatToolChoiceTypeFunction)),
						Function: schemas.ChatAssistantMessageToolCallFunction{
							Name:      &name,
							Arguments: string(jsonArgs),
						},
					}
					toolCalls = append(toolCalls, toolCall)
				}

			case part.FunctionResponse != nil:
				// Create a separate tool response message
				responseContent, err := json.Marshal(part.FunctionResponse.Response)
				if err != nil {
					responseContent = []byte(fmt.Sprintf("%v", part.FunctionResponse.Response))
				}

				// Correlate with the function call: prefer ID if available, otherwise use name
				callID := part.FunctionResponse.Name
				if strings.TrimSpace(part.FunctionResponse.ID) != "" {
					callID = part.FunctionResponse.ID
				} else {
					// Fallback: correlate with the prior function call by name to reuse its ID
					for _, tc := range toolCalls {
						if tc.Function.Name != nil && *tc.Function.Name == part.FunctionResponse.Name &&
							tc.ID != nil && *tc.ID != "" {
							callID = *tc.ID
							break
						}
					}
				}

				toolResponseMsg := schemas.ChatMessage{
					Role: schemas.ChatMessageRoleTool,
					Content: &schemas.ChatMessageContent{
						ContentStr: schemas.Ptr(string(responseContent)),
					},
					ChatToolMessage: &schemas.ChatToolMessage{
						ToolCallID: &callID,
					},
				}

				messages = append(messages, toolResponseMsg)

			case part.InlineData != nil:
				// Handle inline images/media - only append if it's actually an image
				if isImageMimeType(part.InlineData.MIMEType) {
					contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
						Type: schemas.ChatContentBlockTypeImage,
						ImageURLStruct: &schemas.ChatInputImage{
							URL: fmt.Sprintf("data:%s;base64,%s", part.InlineData.MIMEType, base64.StdEncoding.EncodeToString(part.InlineData.Data)),
						},
					})
				}

			case part.FileData != nil:
				// Handle file data - only append if it's actually an image
				if isImageMimeType(part.FileData.MIMEType) {
					contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
						Type: schemas.ChatContentBlockTypeImage,
						ImageURLStruct: &schemas.ChatInputImage{
							URL: part.FileData.FileURI,
						},
					})
				}

			case part.ExecutableCode != nil:
				// Handle executable code as text content
				codeText := fmt.Sprintf("```%s\n%s\n```", part.ExecutableCode.Language, part.ExecutableCode.Code)
				contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
					Type: schemas.ChatContentBlockTypeText,
					Text: &codeText,
				})

			case part.CodeExecutionResult != nil:
				// Handle code execution results as text content
				resultText := fmt.Sprintf("Code execution result (%s):\n%s", part.CodeExecutionResult.Outcome, part.CodeExecutionResult.Output)
				contentBlocks = append(contentBlocks, schemas.ChatContentBlock{
					Type: schemas.ChatContentBlockTypeText,
					Text: &resultText,
				})
			}
		}

		// Only create message if there's actual content, tool calls, or thought content
		if len(contentBlocks) > 0 || len(toolCalls) > 0 || thoughtStr != "" {
			// Create main message with content blocks
			bifrostMsg := schemas.ChatMessage{
				Role: func(r string) schemas.ChatMessageRole {
					if r == string(RoleModel) { // GenAI's internal alias
						return schemas.ChatMessageRoleAssistant
					}
					return schemas.ChatMessageRole(r)
				}(content.Role),
			}

			// Set content only if there are content blocks
			if len(contentBlocks) > 0 {
				bifrostMsg.Content = &schemas.ChatMessageContent{
					ContentBlocks: contentBlocks,
				}
			}

			// Set assistant-specific fields for assistant/model messages
			if content.Role == string(schemas.ChatMessageRoleAssistant) || content.Role == string(RoleModel) {
				if len(toolCalls) > 0 || thoughtStr != "" {
					bifrostMsg.ChatAssistantMessage = &schemas.ChatAssistantMessage{}
					if len(toolCalls) > 0 {
						bifrostMsg.ChatAssistantMessage.ToolCalls = toolCalls
					}
				}
			}

			messages = append(messages, bifrostMsg)
		}
	}

	bifrostReq.Input = messages

	// Convert generation config to parameters
	if params := r.convertGenerationConfigToChatParameters(); params != nil {
		bifrostReq.Params = params
	}

	// Convert safety settings
	if len(r.SafetySettings) > 0 {
		ensureExtraParams(bifrostReq)
		bifrostReq.Params.ExtraParams["safety_settings"] = r.SafetySettings
	}

	// Convert additional request fields
	if r.CachedContent != "" {
		ensureExtraParams(bifrostReq)
		bifrostReq.Params.ExtraParams["cached_content"] = r.CachedContent
	}

	// Convert response modalities
	if len(r.ResponseModalities) > 0 {
		ensureExtraParams(bifrostReq)
		bifrostReq.Params.ExtraParams["response_modalities"] = r.ResponseModalities
	}

	// Convert labels
	if len(r.Labels) > 0 {
		ensureExtraParams(bifrostReq)
		bifrostReq.Params.ExtraParams["labels"] = r.Labels
	}

	// Convert tools and tool config
	if len(r.Tools) > 0 {
		ensureExtraParams(bifrostReq)

		tools := make([]schemas.ChatTool, 0, len(r.Tools))
		for _, tool := range r.Tools {
			if len(tool.FunctionDeclarations) > 0 {
				for _, fn := range tool.FunctionDeclarations {
					bifrostTool := schemas.ChatTool{
						Type: schemas.ChatToolTypeFunction,
						Function: &schemas.ChatToolFunction{
							Name:        fn.Name,
							Description: schemas.Ptr(fn.Description),
						},
					}
					// Convert parameters schema if present
					if fn.Parameters != nil {
						params := r.convertSchemaToFunctionParameters(fn.Parameters)
						bifrostTool.Function.Parameters = &params
					}
					tools = append(tools, bifrostTool)
				}
			}
			// Handle other tool types (Retrieval, GoogleSearch, etc.) as ExtraParams
			if tool.Retrieval != nil {
				bifrostReq.Params.ExtraParams["retrieval"] = tool.Retrieval
			}
			if tool.GoogleSearch != nil {
				bifrostReq.Params.ExtraParams["google_search"] = tool.GoogleSearch
			}
			if tool.CodeExecution != nil {
				bifrostReq.Params.ExtraParams["code_execution"] = tool.CodeExecution
			}
		}

		if len(tools) > 0 {
			bifrostReq.Params.Tools = tools
		}
	}

	// Convert tool config
	if r.ToolConfig.FunctionCallingConfig != nil || r.ToolConfig.RetrievalConfig != nil {
		ensureExtraParams(bifrostReq)
		bifrostReq.Params.ExtraParams["tool_config"] = r.ToolConfig
	}

	return bifrostReq
}

// ToGeminiChatGenerationRequest converts a BifrostChatRequest to Gemini's generation request format for chat completion
func ToGeminiChatGenerationRequest(bifrostReq *schemas.BifrostChatRequest, responseModalities []string) *GeminiGenerationRequest {
	if bifrostReq == nil {
		return nil
	}

	// Create the base Gemini generation request
	geminiReq := &GeminiGenerationRequest{
		Model: bifrostReq.Model,
	}

	// Convert parameters to generation config
	if bifrostReq.Params != nil {
		geminiReq.GenerationConfig = convertParamsToGenerationConfig(bifrostReq.Params, responseModalities)

		// Handle tool-related parameters
		if len(bifrostReq.Params.Tools) > 0 {
			geminiReq.Tools = convertBifrostToolsToGemini(bifrostReq.Params.Tools)

			// Convert tool choice to tool config
			if bifrostReq.Params.ToolChoice != nil {
				geminiReq.ToolConfig = convertToolChoiceToToolConfig(bifrostReq.Params.ToolChoice)
			}
		}

		// Handle extra parameters
		if bifrostReq.Params.ExtraParams != nil {
			// Safety settings
			if safetySettings, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "safety_settings"); ok {
				if settings, ok := safetySettings.([]SafetySetting); ok {
					geminiReq.SafetySettings = settings
				}
			}

			// Cached content
			if cachedContent, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["cached_content"]); ok {
				geminiReq.CachedContent = cachedContent
			}

			// Response modalities
			if modalities, ok := schemas.SafeExtractStringSlice(bifrostReq.Params.ExtraParams["response_modalities"]); ok {
				geminiReq.ResponseModalities = modalities
			}

			// Labels
			if labels, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "labels"); ok {
				if labelMap, ok := labels.(map[string]string); ok {
					geminiReq.Labels = labelMap
				}
			}
		}
	}

	// Convert chat completion messages to Gemini format
	geminiReq.Contents = convertBifrostMessagesToGemini(bifrostReq.Input)

	return geminiReq
}

func (r *GenerateContentResponse) ToBifrostResponse() *schemas.BifrostResponse {
	// Create base response structure
	response := &schemas.BifrostResponse{
		ID:     r.ResponseID,
		Model:  r.ModelVersion,
		Object: "generate_content", // Default object type, will be overridden based on content type
	}

	// Set creation timestamp if available
	if !r.CreateTime.IsZero() {
		response.Created = int(r.CreateTime.Unix())
	}

	// Extract usage metadata
	inputTokens, outputTokens, totalTokens := r.extractUsageMetadata()

	// Process candidates to determine response type and extract content
	if len(r.Candidates) > 0 {
		candidate := r.Candidates[0]
		if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
			// Check what type of content we have
			hasAudio := false
			hasText := false
			var audioData []byte
			var textContent string

			// Process all parts to determine content type
			for _, part := range candidate.Content.Parts {
				if part.InlineData != nil && part.InlineData.Data != nil {
					// Check if this is audio data
					if strings.HasPrefix(part.InlineData.MIMEType, "audio/") {
						hasAudio = true
						audioData = append(audioData, part.InlineData.Data...)
					}
				}
				if part.Text != "" {
					hasText = true
					textContent += part.Text
				}
			}

			// Set response type based on content
			if hasAudio && len(audioData) > 0 {
				// This is a speech response
				response.Object = "audio.speech"
				response.Speech = &schemas.BifrostSpeech{
					Audio: audioData,
					Usage: &schemas.AudioLLMUsage{
						InputTokens:  inputTokens,
						OutputTokens: outputTokens,
						TotalTokens:  totalTokens,
					},
				}
			} else if hasText && textContent != "" {
				// Check if this is actually a transcription response by looking for transcription context
				// Only treat as transcription if we have explicit transcription metadata or context
				isTranscription := r.isTranscriptionResponse()

				if isTranscription {
					// This is a transcription response
					response.Object = "audio.transcription"
					response.Transcribe = &schemas.BifrostTranscribe{
						Text: textContent,
						Usage: &schemas.TranscriptionUsage{
							Type:         "tokens",
							InputTokens:  &inputTokens,
							OutputTokens: &outputTokens,
							TotalTokens:  &totalTokens,
						},
						BifrostTranscribeNonStreamResponse: &schemas.BifrostTranscribeNonStreamResponse{
							Task: schemas.Ptr("transcribe"),
						},
					}
				} else {
					// This is a regular chat completion response
					response.Object = "chat.completion"

					// Create choice from the candidate
					choice := schemas.BifrostChatResponseChoice{
						Index: 0,
						BifrostNonStreamResponseChoice: &schemas.BifrostNonStreamResponseChoice{
							Message: &schemas.ChatMessage{
								Role: schemas.ChatMessageRoleAssistant,
								Content: &schemas.ChatMessageContent{
									ContentStr: &textContent,
								},
							},
						},
					}

					// Set finish reason if available
					if candidate.FinishReason != "" {
						finishReason := string(candidate.FinishReason)
						choice.FinishReason = &finishReason
					}

					response.Choices = []schemas.BifrostChatResponseChoice{choice}

					// Set usage information
					response.Usage = &schemas.LLMUsage{
						PromptTokens:     inputTokens,
						CompletionTokens: outputTokens,
						TotalTokens:      totalTokens,
					}
				}
			}
		}
	}

	return response
}

// isTranscriptionResponse determines if this response is from a transcription request
// by checking for transcription-specific context and metadata
func (r *GenerateContentResponse) isTranscriptionResponse() bool {
	// Check if any candidates contain audio input data in their parts
	// This would indicate the original request included audio for transcription
	for _, candidate := range r.Candidates {
		if candidate.Content != nil {
			for _, part := range candidate.Content.Parts {
				if part.InlineData != nil && part.InlineData.MIMEType != "" {
					// If we have audio data in the response parts, it's likely a transcription
					if strings.HasPrefix(part.InlineData.MIMEType, "audio/") {
						return true
					}
				}
			}
		}
	}

	// Default to false - assume it's a regular chat completion
	// This is safer than incorrectly classifying chat responses as transcriptions
	return false
}

// FromBifrostResponse converts a BifrostResponse back to Gemini's GenerateContentResponse
func ToGeminiGenerationResponse(bifrostResp *schemas.BifrostResponse) interface{} {
	if bifrostResp == nil {
		return nil
	}

	genaiResp := &GenerateContentResponse{
		ResponseID:   bifrostResp.ID,
		ModelVersion: bifrostResp.Model,
	}

	// Set creation time if available
	if bifrostResp.Created > 0 {
		genaiResp.CreateTime = time.Unix(int64(bifrostResp.Created), 0)
	}

	// Handle different response types
	if len(bifrostResp.Data) > 0 {
		return ToGeminiEmbeddingResponse(bifrostResp)
	} else if bifrostResp.Speech != nil {
		// This is a speech response - convert audio data back to Gemini format
		candidate := &Candidate{
			Content: &Content{
				Parts: []*Part{
					{
						InlineData: &Blob{
							Data:     bifrostResp.Speech.Audio,
							MIMEType: "audio/wav", // Default audio MIME type
						},
					},
				},
				Role: string(RoleModel),
			},
		}

		// Set usage metadata from speech usage
		if bifrostResp.Speech.Usage != nil {
			genaiResp.UsageMetadata = &GenerateContentResponseUsageMetadata{
				PromptTokenCount:     int32(bifrostResp.Speech.Usage.InputTokens),
				CandidatesTokenCount: int32(bifrostResp.Speech.Usage.OutputTokens),
				TotalTokenCount:      int32(bifrostResp.Speech.Usage.TotalTokens),
			}
		}

		genaiResp.Candidates = []*Candidate{candidate}

	} else if bifrostResp.Transcribe != nil {
		// This is a transcription response - convert text back to Gemini format
		candidate := &Candidate{
			Content: &Content{
				Parts: []*Part{
					{
						Text: bifrostResp.Transcribe.Text,
					},
				},
				Role: string(RoleModel),
			},
		}

		// Set usage metadata from transcription usage
		if bifrostResp.Transcribe.Usage != nil {
			var promptTokens, candidatesTokens, totalTokens int32
			if bifrostResp.Transcribe.Usage.InputTokens != nil {
				promptTokens = int32(*bifrostResp.Transcribe.Usage.InputTokens)
			}
			if bifrostResp.Transcribe.Usage.OutputTokens != nil {
				candidatesTokens = int32(*bifrostResp.Transcribe.Usage.OutputTokens)
			}
			if bifrostResp.Transcribe.Usage.TotalTokens != nil {
				totalTokens = int32(*bifrostResp.Transcribe.Usage.TotalTokens)
			}

			genaiResp.UsageMetadata = &GenerateContentResponseUsageMetadata{
				PromptTokenCount:     promptTokens,
				CandidatesTokenCount: candidatesTokens,
				TotalTokenCount:      totalTokens,
			}
		}

		genaiResp.Candidates = []*Candidate{candidate}

	} else if len(bifrostResp.Choices) > 0 {
		// This is a chat completion response
		candidates := make([]*Candidate, len(bifrostResp.Choices))

		for i, choice := range bifrostResp.Choices {
			candidate := &Candidate{
				Index: int32(choice.Index),
			}

			if choice.FinishReason != nil {
				candidate.FinishReason = FinishReason(*choice.FinishReason)
			}

			// Convert message content to Gemini parts
			var parts []*Part
			if choice.Message.Content.ContentStr != nil && *choice.Message.Content.ContentStr != "" {
				parts = append(parts, &Part{Text: *choice.Message.Content.ContentStr})
			} else if choice.Message.Content.ContentBlocks != nil {
				for _, block := range choice.Message.Content.ContentBlocks {
					if block.Text != nil {
						parts = append(parts, &Part{Text: *block.Text})
					}
				}
			}

			// Handle tool calls
			if choice.Message.ChatAssistantMessage != nil && choice.Message.ChatAssistantMessage.ToolCalls != nil {
				for _, toolCall := range choice.Message.ChatAssistantMessage.ToolCalls {
					argsMap := make(map[string]interface{})
					if toolCall.Function.Arguments != "" {
						json.Unmarshal([]byte(toolCall.Function.Arguments), &argsMap)
					}
					if toolCall.Function.Name != nil {
						fc := &FunctionCall{
							Name: *toolCall.Function.Name,
							Args: argsMap,
						}
						if toolCall.ID != nil {
							fc.ID = *toolCall.ID
						}
						parts = append(parts, &Part{FunctionCall: fc})
					}
				}
			}

			if len(parts) > 0 {
				candidate.Content = &Content{
					Parts: parts,
					Role:  string(choice.Message.Role),
				}
			}

			candidates[i] = candidate
		}

		genaiResp.Candidates = candidates

		// Set usage metadata from LLM usage
		if bifrostResp.Usage != nil {
			genaiResp.UsageMetadata = &GenerateContentResponseUsageMetadata{
				PromptTokenCount:     int32(bifrostResp.Usage.PromptTokens),
				CandidatesTokenCount: int32(bifrostResp.Usage.CompletionTokens),
				TotalTokenCount:      int32(bifrostResp.Usage.TotalTokens),
			}
		}
	}

	return genaiResp
}

// ToGeminiEmbeddingResponse converts a BifrostResponse with embedding data to Gemini's embedding response format
func ToGeminiEmbeddingResponse(bifrostResp *schemas.BifrostResponse) *GeminiEmbeddingResponse {
	if bifrostResp == nil || len(bifrostResp.Data) == 0 {
		return nil
	}

	geminiResp := &GeminiEmbeddingResponse{
		Embeddings: make([]GeminiEmbedding, len(bifrostResp.Data)),
	}

	// Convert each embedding from Bifrost format to Gemini format
	for i, embedding := range bifrostResp.Data {
		var values []float32

		// Extract embedding values from BifrostEmbeddingResponse
		if embedding.Embedding.EmbeddingArray != nil {
			values = embedding.Embedding.EmbeddingArray
		} else if len(embedding.Embedding.Embedding2DArray) > 0 {
			// If it's a 2D array, take the first array
			values = embedding.Embedding.Embedding2DArray[0]
		}

		geminiEmbedding := GeminiEmbedding{
			Values: values,
		}

		// Add statistics if available (token count from usage metadata)
		if bifrostResp.Usage != nil {
			geminiEmbedding.Statistics = &ContentEmbeddingStatistics{
				TokenCount: int32(bifrostResp.Usage.PromptTokens),
			}
		}

		geminiResp.Embeddings[i] = geminiEmbedding
	}

	// Set metadata if available (for Vertex API compatibility)
	if bifrostResp.Usage != nil {
		geminiResp.Metadata = &EmbedContentMetadata{
			BillableCharacterCount: int32(bifrostResp.Usage.PromptTokens),
		}
	}

	return geminiResp
}
