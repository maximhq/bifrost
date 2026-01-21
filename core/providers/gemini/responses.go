package gemini

import (
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

func (request *GeminiGenerationRequest) ToBifrostResponsesRequest(ctx *schemas.BifrostContext) *schemas.BifrostResponsesRequest {
	if request == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(request.Model, providerUtils.CheckAndSetDefaultProvider(ctx, schemas.Gemini))

	// Create the BifrostResponsesRequest
	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: provider,
		Model:    model,
	}

	params := request.convertGenerationConfigToResponsesParameters()

	// Convert SystemInstruction to system messages first
	var inputMessages []schemas.ResponsesMessage
	if request.SystemInstruction != nil && len(request.SystemInstruction.Parts) > 0 {
		systemMsg := convertGeminiSystemInstructionToResponsesMessage(request.SystemInstruction)
		if systemMsg != nil {
			inputMessages = append(inputMessages, *systemMsg)
		}
	}

	// Convert Contents to Input messages
	if len(request.Contents) > 0 {
		contentsMessages := convertGeminiContentsToResponsesMessages(request.Contents)
		if len(contentsMessages) > 0 {
			inputMessages = append(inputMessages, contentsMessages...)
		}
	}

	if len(inputMessages) > 0 {
		bifrostReq.Input = inputMessages
	}

	if len(request.Tools) > 0 {
		params.Tools = convertGeminiToolsToResponsesTools(request.Tools)
	}

	if request.ToolConfig.FunctionCallingConfig != nil {
		params.ToolChoice = convertGeminiToolConfigToToolChoice(request.ToolConfig)
	}

	if request.SafetySettings != nil {
		params.ExtraParams["safety_settings"] = request.SafetySettings
	}

	if request.CachedContent != "" {
		params.ExtraParams["cached_content"] = request.CachedContent
	}

	bifrostReq.Params = params

	return bifrostReq

}

func ToGeminiResponsesRequest(bifrostReq *schemas.BifrostResponsesRequest) *GeminiGenerationRequest {
	if bifrostReq == nil {
		return nil
	}

	// Create the base Gemini generation request
	geminiReq := &GeminiGenerationRequest{
		Model: bifrostReq.Model,
	}

	// Convert parameters to generation config
	if bifrostReq.Params != nil {
		geminiReq.GenerationConfig = geminiReq.convertParamsToGenerationConfigResponses(bifrostReq.Params)

		// Handle tool-related parameters
		if len(bifrostReq.Params.Tools) > 0 {
			geminiReq.Tools = convertResponsesToolsToGemini(bifrostReq.Params.Tools)

			// Convert tool choice if present
			if bifrostReq.Params.ToolChoice != nil {
				geminiReq.ToolConfig = convertResponsesToolChoiceToGemini(bifrostReq.Params.ToolChoice)
			}
		}
	}

	// Convert ResponsesInput messages to Gemini contents
	if bifrostReq.Input != nil {
		contents, systemInstruction, err := convertResponsesMessagesToGeminiContents(bifrostReq.Input)
		if err != nil {
			return nil
		}
		geminiReq.Contents = contents

		if systemInstruction != nil {
			geminiReq.SystemInstruction = systemInstruction
		}
	}

	if bifrostReq.Params != nil {
		if bifrostReq.Params.Instructions != nil {
			// check if system instruction is already set
			if geminiReq.SystemInstruction == nil {
				geminiReq.SystemInstruction = &Content{
					Parts: []*Part{
						{Text: *bifrostReq.Params.Instructions},
					},
				}
			}
		}

		if bifrostReq.Params.ExtraParams != nil {
			if safetySettings, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "safety_settings"); ok {
				if settings, ok := SafeExtractSafetySettings(safetySettings); ok {
					geminiReq.SafetySettings = settings
				}
			}
			if cachedContent, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["cached_content"]); ok {
				geminiReq.CachedContent = cachedContent
			}
		}
	}

	return geminiReq
}

// ToResponsesBifrostResponsesResponse converts a Gemini GenerateContentResponse to a BifrostResponsesResponse
func (response *GenerateContentResponse) ToResponsesBifrostResponsesResponse() *schemas.BifrostResponsesResponse {
	if response == nil {
		return nil
	}

	// Create the BifrostResponse with Responses structure
	bifrostResp := &schemas.BifrostResponsesResponse{
		ID:        schemas.Ptr("resp_" + providerUtils.GetRandomString(50)),
		CreatedAt: int(time.Now().Unix()),
		Model:     response.ModelVersion,
	}

	// Convert usage information
	bifrostResp.Usage = convertGeminiUsageMetadataToResponsesUsage(response.UsageMetadata)

	// Convert candidates to Responses output messages
	if len(response.Candidates) > 0 {
		outputMessages := convertGeminiCandidatesToResponsesOutput(response.Candidates)
		if len(outputMessages) > 0 {
			bifrostResp.Output = outputMessages
		}
	}

	return bifrostResp
}

func ToGeminiResponsesResponse(bifrostResp *schemas.BifrostResponsesResponse) *GenerateContentResponse {
	if bifrostResp == nil {
		return nil
	}

	geminiResp := &GenerateContentResponse{
		ModelVersion: bifrostResp.Model,
	}

	// Set response ID if available
	if bifrostResp.ID != nil {
		geminiResp.ResponseID = *bifrostResp.ID
	}

	// Set creation time
	if bifrostResp.CreatedAt > 0 {
		geminiResp.CreateTime = time.Unix(int64(bifrostResp.CreatedAt), 0)
	}

	// Convert output messages to candidates
	if len(bifrostResp.Output) > 0 {
		candidates := []*Candidate{}

		// Group messages by their role to create candidates
		var currentParts []*Part
		var currentRole string

		// Track which message indices have been consumed as thought signatures
		consumedIndices := make(map[int]bool)

		// Find last web_search_call and collect annotations and rendered_content for grounding metadata
		var lastWebSearchCall *schemas.ResponsesMessage
		var webSearchAnnotations []schemas.ResponsesOutputMessageContentTextAnnotation
		var lastRenderedContent *string
		for i := range bifrostResp.Output {
			msg := &bifrostResp.Output[i]
			if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeWebSearchCall {
				lastWebSearchCall = msg
				consumedIndices[i] = true
			}
			// Collect annotations (typically in message after web search)
			if msg.Content != nil && msg.Content.ContentBlocks != nil {
				for _, block := range msg.Content.ContentBlocks {
					if block.ResponsesOutputMessageContentText != nil && len(block.ResponsesOutputMessageContentText.Annotations) > 0 {
						webSearchAnnotations = append(webSearchAnnotations, block.ResponsesOutputMessageContentText.Annotations...)
					}
					// Collect rendered_content
					if block.Type == schemas.ResponsesOutputMessageContentTypeRenderedContent &&
						block.ResponsesOutputMessageContentRenderedContent != nil &&
						block.ResponsesOutputMessageContentRenderedContent.RenderedContent != "" {
						lastRenderedContent = &block.ResponsesOutputMessageContentRenderedContent.RenderedContent
						consumedIndices[i] = true // Mark this message as consumed
					}
				}
			}
		}

		for i, msg := range bifrostResp.Output {
			// Skip web_search_call messages as they're converted to grounding metadata
			if consumedIndices[i] {
				continue
			}

			// Determine the role
			role := "model" // default
			if msg.Role != nil {
				if *msg.Role == schemas.ResponsesInputMessageRoleUser {
					role = "user"
				}
			}

			// If we're starting a new candidate (role changed), save the previous one
			if currentRole != "" && currentRole != role && len(currentParts) > 0 {
				candidates = append(candidates, &Candidate{
					Index: int32(len(candidates)),
					Content: &Content{
						Parts: currentParts,
						Role:  currentRole,
					},
				})
				currentParts = []*Part{}
			}
			currentRole = role

			// Convert message content to parts
			if msg.Content != nil {
				// Handle string content
				if msg.Content.ContentStr != nil && *msg.Content.ContentStr != "" {
					currentParts = append(currentParts, &Part{
						Text: *msg.Content.ContentStr,
					})
				}

				// Handle content blocks
				if msg.Content.ContentBlocks != nil {
					for _, block := range msg.Content.ContentBlocks {
						part, err := convertContentBlockToGeminiPart(block)
						if err == nil && part != nil {
							currentParts = append(currentParts, part)
						}
					}
				}
			}

			// Handle tool calls (function calls)
			if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCall && msg.ResponsesToolMessage != nil {
				argsMap := make(map[string]any)
				if msg.ResponsesToolMessage.Arguments != nil {
					if err := sonic.Unmarshal([]byte(*msg.ResponsesToolMessage.Arguments), &argsMap); err == nil {
						functionCall := &FunctionCall{
							Args: argsMap,
						}
						if msg.ResponsesToolMessage.Name != nil {
							functionCall.Name = *msg.ResponsesToolMessage.Name
						}

						// Extract thought signature from CallID if present
						var thoughtSignature []byte
						if msg.ResponsesToolMessage.CallID != nil {
							callID := *msg.ResponsesToolMessage.CallID
							// Check if the ID contains a thought signature (format: "ToolName_ts_base64signature")
							if strings.Contains(callID, thoughtSignatureSeparator) {
								parts := strings.SplitN(callID, thoughtSignatureSeparator, 2)
								if len(parts) == 2 {
									// Try to decode the signature part
									if decodedSig, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
										thoughtSignature = decodedSig
									}
								}
							}
							functionCall.ID = callID
						}

						part := &Part{
							FunctionCall: functionCall,
						}

						// Use thought signature from CallID if we extracted one
						if len(thoughtSignature) > 0 {
							part.ThoughtSignature = thoughtSignature
						} else {
							// Otherwise, look ahead to see if the next message is a reasoning message with encrypted content
							// (thought signature for this function call)
							if i+1 < len(bifrostResp.Output) {
								nextMsg := bifrostResp.Output[i+1]
								if nextMsg.Type != nil && *nextMsg.Type == schemas.ResponsesMessageTypeReasoning &&
									nextMsg.ResponsesReasoning != nil && nextMsg.ResponsesReasoning.EncryptedContent != nil {
									decodedSig, err := base64.StdEncoding.DecodeString(*nextMsg.ResponsesReasoning.EncryptedContent)
									if err == nil {
										part.ThoughtSignature = decodedSig
										// Mark this reasoning message as consumed
										consumedIndices[i+1] = true
									}
								}
							}
						}

						currentParts = append(currentParts, part)
					}
				}
			}

			// Handle function responses (function call outputs)
			if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCallOutput && msg.ResponsesToolMessage != nil {
				responseMap := make(map[string]any)

				if msg.ResponsesToolMessage.Output != nil && msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
					responseMap["output"] = *msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
				}
				funcName := ""
				if msg.ResponsesToolMessage.Name != nil && strings.TrimSpace(*msg.ResponsesToolMessage.Name) != "" {
					funcName = *msg.ResponsesToolMessage.Name
				} else if msg.ResponsesToolMessage.CallID != nil {
					funcName = *msg.ResponsesToolMessage.CallID
				}

				functionResponse := &FunctionResponse{
					Name:     funcName,
					Response: responseMap,
				}
				if msg.ResponsesToolMessage.CallID != nil {
					functionResponse.ID = *msg.ResponsesToolMessage.CallID
				}

				currentParts = append(currentParts, &Part{
					FunctionResponse: functionResponse,
				})
			}

			// Handle reasoning messages
			if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeReasoning && msg.ResponsesReasoning != nil {
				// Skip this reasoning message if it was already consumed as a thought signature
				if consumedIndices[i] {
					continue
				}

				// Reasoning content is in the Summary array
				if len(msg.ResponsesReasoning.Summary) > 0 {
					for _, summaryBlock := range msg.ResponsesReasoning.Summary {
						if summaryBlock.Text != "" {
							currentParts = append(currentParts, &Part{
								Text:    summaryBlock.Text,
								Thought: true,
							})
						}
					}
				}
				if msg.ResponsesReasoning.EncryptedContent != nil {
					decodedSig, err := base64.StdEncoding.DecodeString(*msg.ResponsesReasoning.EncryptedContent)
					if err == nil {
						currentParts = append(currentParts, &Part{
							ThoughtSignature: decodedSig,
						})
					}
				}
			}
		}

		// Add the last candidate if we have parts
		if len(currentParts) > 0 {
			candidate := &Candidate{
				Index: int32(len(candidates)),
				Content: &Content{
					Parts: currentParts,
					Role:  currentRole,
				},
			}

			// Determine finish reason based on incomplete details
			if bifrostResp.IncompleteDetails != nil {
				switch bifrostResp.IncompleteDetails.Reason {
				case "max_tokens":
					candidate.FinishReason = FinishReasonMaxTokens
				case "content_filter":
					candidate.FinishReason = FinishReasonSafety
				default:
					candidate.FinishReason = FinishReasonOther
				}
			} else {
				candidate.FinishReason = FinishReasonStop
			}

			// Attach grounding metadata if web search was used
			if lastWebSearchCall != nil {
				candidate.GroundingMetadata = buildGroundingMetadataFromWebSearch(lastWebSearchCall, webSearchAnnotations, lastRenderedContent)
			}

			candidates = append(candidates, candidate)
		}

		geminiResp.Candidates = candidates
	}

	// Convert usage metadata
	if bifrostResp.Usage != nil {
		geminiResp.UsageMetadata = &GenerateContentResponseUsageMetadata{
			PromptTokenCount:     int32(bifrostResp.Usage.InputTokens),
			CandidatesTokenCount: int32(bifrostResp.Usage.OutputTokens),
			TotalTokenCount:      int32(bifrostResp.Usage.TotalTokens),
		}
		if bifrostResp.Usage.OutputTokensDetails != nil {
			geminiResp.UsageMetadata.ThoughtsTokenCount = int32(bifrostResp.Usage.OutputTokensDetails.ReasoningTokens)
		}
	}

	return geminiResp
}

// BifrostToGeminiStreamState tracks state when converting Bifrost streams to Gemini format
type BifrostToGeminiStreamState struct {
	// Web search buffering
	WebSearchCall   *schemas.ResponsesMessage                             // Buffered web_search_call
	Annotations     []schemas.ResponsesOutputMessageContentTextAnnotation // Buffered annotations
	RenderedContent *string                                               // Buffered rendered content from search entry point
	HasWebSearch    bool                                                  // Whether we've seen web search
}

// NewBifrostToGeminiStreamState creates a new state for Bifrost→Gemini streaming
func NewBifrostToGeminiStreamState() *BifrostToGeminiStreamState {
	return &BifrostToGeminiStreamState{
		Annotations: make([]schemas.ResponsesOutputMessageContentTextAnnotation, 0),
	}
}

func ToGeminiResponsesStreamResponse(bifrostResp *schemas.BifrostResponsesStreamResponse, state *BifrostToGeminiStreamState) *GenerateContentResponse {
	if bifrostResp == nil {
		return nil
	}

	// Initialize state if not provided (backward compatibility)
	if state == nil {
		state = NewBifrostToGeminiStreamState()
	}

	// Buffer web search call
	if bifrostResp.Type == schemas.ResponsesStreamResponseTypeOutputItemDone &&
		bifrostResp.Item != nil &&
		bifrostResp.Item.Type != nil &&
		*bifrostResp.Item.Type == schemas.ResponsesMessageTypeWebSearchCall {
		state.WebSearchCall = bifrostResp.Item
		state.HasWebSearch = true
		return nil // Don't emit yet, wait for completion
	}

	// Buffer annotations
	if bifrostResp.Type == schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded &&
		bifrostResp.Annotation != nil {
		state.Annotations = append(state.Annotations, *bifrostResp.Annotation)
		return nil // Don't emit yet, wait for completion
	}

	// Buffer rendered_content messages
	if bifrostResp.Type == schemas.ResponsesStreamResponseTypeOutputItemDone &&
		bifrostResp.Item != nil &&
		bifrostResp.Item.Content != nil &&
		bifrostResp.Item.Content.ContentBlocks != nil {
		for _, block := range bifrostResp.Item.Content.ContentBlocks {
			if block.Type == schemas.ResponsesOutputMessageContentTypeRenderedContent &&
				block.ResponsesOutputMessageContentRenderedContent != nil &&
				block.ResponsesOutputMessageContentRenderedContent.RenderedContent != "" {
				state.RenderedContent = &block.ResponsesOutputMessageContentRenderedContent.RenderedContent
				return nil // Don't emit yet, wait for completion
			}
		}
	}

	// Skip lifecycle events that don't have corresponding Gemini equivalents
	switch bifrostResp.Type {
	case schemas.ResponsesStreamResponseTypePing,
		schemas.ResponsesStreamResponseTypeCreated,
		schemas.ResponsesStreamResponseTypeInProgress,
		schemas.ResponsesStreamResponseTypeReasoningSummaryPartAdded,
		schemas.ResponsesStreamResponseTypeQueued,
		// Skip web search lifecycle events - buffered above
		schemas.ResponsesStreamResponseTypeWebSearchCallInProgress,
		schemas.ResponsesStreamResponseTypeWebSearchCallSearching,
		schemas.ResponsesStreamResponseTypeWebSearchCallCompleted,
		schemas.ResponsesStreamResponseTypeWebSearchCallResultsAdded,
		schemas.ResponsesStreamResponseTypeWebSearchCallResultsCompleted:
		// These are lifecycle events with no Gemini equivalent or are buffered
		return nil
	}

	streamResp := &GenerateContentResponse{
		Candidates: []*Candidate{
			{
				Content: &Content{
					Parts: []*Part{},
					Role:  "model",
				},
			},
		},
	}

	candidate := streamResp.Candidates[0]

	switch bifrostResp.Type {
	case schemas.ResponsesStreamResponseTypeOutputTextDelta:
		if bifrostResp.Delta != nil && *bifrostResp.Delta != "" {
			candidate.Content.Parts = append(candidate.Content.Parts, &Part{
				Text: *bifrostResp.Delta,
			})
		}

	case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta:
		if bifrostResp.Delta != nil && *bifrostResp.Delta != "" {
			candidate.Content.Parts = append(candidate.Content.Parts, &Part{
				Text:    *bifrostResp.Delta,
				Thought: true,
			})
		}

	case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
		// For streaming, we'll accumulate these, but Gemini typically sends complete calls
		// We'll return nil here and let the done event handle it
		return nil

	// Function call completed
	case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone:
		if bifrostResp.Item != nil && bifrostResp.Item.ResponsesToolMessage != nil {
			argsMap := make(map[string]any)
			if bifrostResp.Item.ResponsesToolMessage.Arguments != nil {
				if err := sonic.Unmarshal([]byte(*bifrostResp.Item.ResponsesToolMessage.Arguments), &argsMap); err == nil {
					functionCall := &FunctionCall{
						Name: "",
						Args: argsMap,
					}
					if bifrostResp.Item.ResponsesToolMessage.Name != nil {
						functionCall.Name = *bifrostResp.Item.ResponsesToolMessage.Name
					}

					var thoughtSig string
					if bifrostResp.Item.ResponsesToolMessage.CallID != nil {
						// Extract thought signature from CallID if present
						if strings.Contains(*bifrostResp.Item.ResponsesToolMessage.CallID, thoughtSignatureSeparator) {
							parts := strings.SplitN(*bifrostResp.Item.ResponsesToolMessage.CallID, thoughtSignatureSeparator, 2)
							if len(parts) == 2 {
								thoughtSig = parts[1]
							}
						}
						functionCall.ID = *bifrostResp.Item.ResponsesToolMessage.CallID
					}
					functionCallPart := &Part{
						FunctionCall: functionCall,
					}
					if thoughtSig != "" {
						if decodedSig, err := base64.RawURLEncoding.DecodeString(thoughtSig); err == nil {
							functionCallPart.ThoughtSignature = decodedSig
						}
					}
					candidate.Content.Parts = append(candidate.Content.Parts, functionCallPart)
				}
			}
		}

	case schemas.ResponsesStreamResponseTypeOutputTextDone:
		if bifrostResp.Text != nil && *bifrostResp.Text != "" {
			candidate.Content.Parts = append(candidate.Content.Parts, &Part{
				Text: *bifrostResp.Text,
			})
		}

	case schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone,
		schemas.ResponsesStreamResponseTypeReasoningSummaryPartDone:
		// Already handled via deltas, skip
		return nil
	case schemas.ResponsesStreamResponseTypeOutputItemAdded:
		if bifrostResp.Item != nil && bifrostResp.Item.ResponsesReasoning != nil && bifrostResp.Item.EncryptedContent != nil {
			candidate.Content.Parts = append(candidate.Content.Parts, &Part{
				ThoughtSignature: []byte(*bifrostResp.Item.ResponsesReasoning.EncryptedContent),
			})
		}

	case schemas.ResponsesStreamResponseTypeOutputItemDone:
		return nil

	case schemas.ResponsesStreamResponseTypeContentPartAdded:
		// Handle content parts that contain images, audio, or files
		if bifrostResp.Part != nil {
			part, err := convertContentBlockToGeminiPart(*bifrostResp.Part)
			if err == nil && part != nil {
				candidate.Content.Parts = append(candidate.Content.Parts, part)
			}
		}

	case schemas.ResponsesStreamResponseTypeContentPartDone:
		// Already handled via ContentPartAdded
		return nil

	case schemas.ResponsesStreamResponseTypeCompleted:
		if bifrostResp.Response != nil {
			// Set model version if available
			if bifrostResp.Response.Model != "" {
				streamResp.ModelVersion = bifrostResp.Response.Model
			}

			// Convert usage metadata if available
			if bifrostResp.Response.Usage != nil {
				streamResp.UsageMetadata = &GenerateContentResponseUsageMetadata{
					PromptTokenCount:     int32(bifrostResp.Response.Usage.InputTokens),
					CandidatesTokenCount: int32(bifrostResp.Response.Usage.OutputTokens),
					TotalTokenCount:      int32(bifrostResp.Response.Usage.TotalTokens),
				}
				if bifrostResp.Response.Usage.InputTokensDetails != nil {
					streamResp.UsageMetadata.CachedContentTokenCount = int32(bifrostResp.Response.Usage.InputTokensDetails.CachedTokens)
				}
				if bifrostResp.Response.Usage.OutputTokensDetails != nil {
					streamResp.UsageMetadata.ThoughtsTokenCount = int32(bifrostResp.Response.Usage.OutputTokensDetails.ReasoningTokens)
				}
				if bifrostResp.Response.Usage.OutputTokensDetails != nil && bifrostResp.Response.Usage.OutputTokensDetails.AudioTokens > 0 {
					// Store audio tokens separately or add proper field
					streamResp.UsageMetadata.CandidatesTokensDetails = append(streamResp.UsageMetadata.CandidatesTokensDetails, &ModalityTokenCount{
						Modality:   "AUDIO",
						TokenCount: int32(bifrostResp.Response.Usage.OutputTokensDetails.AudioTokens),
					})
				}
			}

			// Set finish reason
			candidate.FinishReason = FinishReasonStop

			// Attach grounding metadata if we buffered web search data
			if state.HasWebSearch && state.WebSearchCall != nil {
				candidate.GroundingMetadata = buildGroundingMetadataFromWebSearch(state.WebSearchCall, state.Annotations, state.RenderedContent)
			}
		}

	// Response failed
	case schemas.ResponsesStreamResponseTypeFailed:
		candidate.FinishReason = FinishReasonOther
		if bifrostResp.Response != nil && bifrostResp.Response.Error != nil {
			streamResp.PromptFeedback = &GenerateContentResponsePromptFeedback{
				BlockReason:        "ERROR",
				BlockReasonMessage: bifrostResp.Response.Error.Message,
			}
		}

	// Refusal
	case schemas.ResponsesStreamResponseTypeRefusalDelta:
		if bifrostResp.Delta != nil && *bifrostResp.Delta != "" {
			candidate.Content.Parts = append(candidate.Content.Parts, &Part{
				Text: *bifrostResp.Delta,
			})
		}

	case schemas.ResponsesStreamResponseTypeRefusalDone:
		if bifrostResp.Refusal != nil && *bifrostResp.Refusal != "" {
			candidate.FinishReason = FinishReasonSafety
		}

	default:
		// For any other event types we don't explicitly handle, return nil
		return nil
	}

	// If we didn't add any parts and there's no metadata, return nil
	if len(candidate.Content.Parts) == 0 && streamResp.UsageMetadata == nil &&
		streamResp.PromptFeedback == nil && candidate.FinishReason == "" {
		return nil
	}

	return streamResp
}

// GeminiResponsesStreamState tracks state during streaming conversion for responses API
type GeminiResponsesStreamState struct {
	// Lifecycle flags
	HasEmittedCreated    bool // Whether response.created has been sent
	HasEmittedInProgress bool // Whether response.in_progress has been sent
	HasEmittedCompleted  bool // Whether response.completed has been sent

	// Item tracking
	CurrentOutputIndex int            // Current output index counter
	TextOutputIndex    int            // Output index of the current text item (cached for reuse)
	ItemIDs            map[int]string // Maps output_index to item ID
	TextItemClosed     bool           // Whether text item has been closed

	// Tool call tracking
	ToolCallIDs         map[int]string // Maps output_index to tool call ID
	ToolCallNames       map[int]string // Maps output_index to tool name
	ToolArgumentBuffers map[int]string // Accumulates tool arguments as JSON

	// Response metadata
	MessageID  *string // Generated message ID
	Model      *string // Model version
	CreatedAt  int     // Timestamp for consistency
	ResponseID *string // Gemini's responseId

	// Content tracking
	HasStartedText     bool            // Whether we've started text content
	HasStartedToolCall bool            // Whether we've started a tool call
	TextBuffer         strings.Builder // Accumulates text deltas for output_text.done

	// Web search tracking
	HasEmittedWebSearch bool // Whether web_search_call events have been emitted
}

// geminiResponsesStreamStatePool provides a pool for Gemini responses stream state objects.
var geminiResponsesStreamStatePool = sync.Pool{
	New: func() interface{} {
		return &GeminiResponsesStreamState{
			ItemIDs:              make(map[int]string),
			ToolCallIDs:          make(map[int]string),
			ToolCallNames:        make(map[int]string),
			ToolArgumentBuffers:  make(map[int]string),
			CurrentOutputIndex:   0,
			TextOutputIndex:      -1,
			CreatedAt:            int(time.Now().Unix()),
			HasEmittedCreated:    false,
			HasEmittedInProgress: false,
			HasEmittedCompleted:  false,
			TextItemClosed:       false,
			HasStartedText:       false,
			HasStartedToolCall:   false,
			HasEmittedWebSearch:  false,
		}
	},
}

// acquireGeminiResponsesStreamState gets a Gemini responses stream state from the pool.
func acquireGeminiResponsesStreamState() *GeminiResponsesStreamState {
	state := geminiResponsesStreamStatePool.Get().(*GeminiResponsesStreamState)
	state.flush()
	return state
}

// releaseGeminiResponsesStreamState returns a Gemini responses stream state to the pool.
func releaseGeminiResponsesStreamState(state *GeminiResponsesStreamState) {
	if state != nil {
		state.flush()
		geminiResponsesStreamStatePool.Put(state)
	}
}

func (state *GeminiResponsesStreamState) flush() {
	// Clear maps
	if state.ItemIDs == nil {
		state.ItemIDs = make(map[int]string)
	} else {
		clear(state.ItemIDs)
	}
	if state.ToolCallIDs == nil {
		state.ToolCallIDs = make(map[int]string)
	} else {
		clear(state.ToolCallIDs)
	}
	if state.ToolCallNames == nil {
		state.ToolCallNames = make(map[int]string)
	} else {
		clear(state.ToolCallNames)
	}
	if state.ToolArgumentBuffers == nil {
		state.ToolArgumentBuffers = make(map[int]string)
	} else {
		clear(state.ToolArgumentBuffers)
	}
	state.CurrentOutputIndex = 0
	state.TextOutputIndex = -1
	state.MessageID = nil
	state.Model = nil
	state.ResponseID = nil
	state.CreatedAt = int(time.Now().Unix())
	state.HasEmittedCreated = false
	state.HasEmittedCompleted = false
	state.HasEmittedInProgress = false
	state.TextItemClosed = false
	state.HasStartedText = false
	state.HasStartedToolCall = false
	state.TextBuffer.Reset()
}

// closeTextItemIfOpen closes the text item if it's open and returns the responses.
// Returns nil if no text item was open.
func (state *GeminiResponsesStreamState) closeTextItemIfOpen(sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	if state.HasStartedText && !state.TextItemClosed {
		return closeGeminiTextItem(state, sequenceNumber)
	}
	return nil
}

// nextOutputIndex returns the current output index and increments it for the next use.
func (state *GeminiResponsesStreamState) nextOutputIndex() int {
	index := state.CurrentOutputIndex
	state.CurrentOutputIndex++
	return index
}

// generateItemID creates a unique item ID with the given suffix.
// Falls back to index-based ID if MessageID is nil.
func (state *GeminiResponsesStreamState) generateItemID(suffix string, outputIndex int) string {
	if state.MessageID != nil {
		return fmt.Sprintf("msg_%s_%s_%d", *state.MessageID, suffix, outputIndex)
	}
	return fmt.Sprintf("%s_%d", suffix, outputIndex)
}

// ToBifrostResponsesStream converts a Gemini stream event to Bifrost Responses Stream responses
func (response *GenerateContentResponse) ToBifrostResponsesStream(sequenceNumber int, state *GeminiResponsesStreamState) ([]*schemas.BifrostResponsesStreamResponse, *schemas.BifrostError) {
	var responses []*schemas.BifrostResponsesStreamResponse

	// First event: Emit response.created and response.in_progress
	if !state.HasEmittedCreated {
		// Generate message ID
		if state.MessageID == nil {
			messageID := fmt.Sprintf("msg_%d", state.CreatedAt)
			state.MessageID = &messageID
		}

		// Set model and response ID from Gemini
		if response.ModelVersion != "" && state.Model == nil {
			state.Model = &response.ModelVersion
		}
		if response.ResponseID != "" && state.ResponseID == nil {
			state.ResponseID = &response.ResponseID
		}

		// Emit response.created
		createdResp := &schemas.BifrostResponsesResponse{
			ID:        state.MessageID,
			CreatedAt: state.CreatedAt,
		}
		if state.Model != nil {
			createdResp.Model = *state.Model
		}
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeCreated,
			SequenceNumber: sequenceNumber + len(responses),
			Response:       createdResp,
		})
		state.HasEmittedCreated = true

		// Emit response.in_progress
		inProgressResp := &schemas.BifrostResponsesResponse{
			ID:        state.MessageID,
			CreatedAt: state.CreatedAt,
		}
		if state.Model != nil {
			inProgressResp.Model = *state.Model
		}
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeInProgress,
			SequenceNumber: sequenceNumber + len(responses),
			Response:       inProgressResp,
		})
		state.HasEmittedInProgress = true
	}

	// Process candidates
	if len(response.Candidates) > 0 {
		candidate := response.Candidates[0]
		if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
			for _, part := range candidate.Content.Parts {
				partResponses := processGeminiPart(part, state, sequenceNumber+len(responses))
				responses = append(responses, partResponses...)
			}
		}

		// Check for finish reason (indicates end of generation)
		// Only close if we've actually started emitting content (text, tool calls, etc.)
		// This prevents emitting response.completed for empty chunks with just finishReason
		if candidate.FinishReason != "" && len(state.ItemIDs) > 0 {
			// Check for grounding metadata (web search results)
			if candidate.GroundingMetadata != nil && !state.HasEmittedWebSearch {
				// Emit web search events before closing
				webSearchResponses := emitWebSearchFromGroundingMetadata(
					candidate.GroundingMetadata,
					state,
					sequenceNumber+len(responses),
				)
				responses = append(responses, webSearchResponses...)
			}

			// Close any open items
			closeResponses := closeGeminiOpenItems(state, candidate.GroundingMetadata, response.UsageMetadata, sequenceNumber+len(responses))
			responses = append(responses, closeResponses...)
		}
	}

	return responses, nil
}

// processGeminiPart processes a single Gemini part and returns appropriate lifecycle events
func processGeminiPart(part *Part, state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	switch {
	case part.Thought && part.Text != "":
		// Reasoning/thinking content
		responses = append(responses, processGeminiThoughtPart(part, state, sequenceNumber)...)
	case part.Text != "" && !part.Thought:
		// Regular text content
		responses = append(responses, processGeminiTextPart(part, state, sequenceNumber)...)

	case part.FunctionCall != nil:
		// Function call
		responses = append(responses, processGeminiFunctionCallPart(part, state, sequenceNumber)...)

	case part.ThoughtSignature != nil:
		// Encrypted reasoning content (thoughtSignature)
		responses = append(responses, processGeminiThoughtSignaturePart(part, state, sequenceNumber)...)

	case part.FunctionResponse != nil:
		// Function response (tool result)
		responses = append(responses, processGeminiFunctionResponsePart(part, state, sequenceNumber)...)
	case part.InlineData != nil:
		// Inline data
		responses = append(responses, processGeminiInlineDataPart(part, state, sequenceNumber)...)
	case part.FileData != nil:
		// File data
		responses = append(responses, processGeminiFileDataPart(part, state, sequenceNumber)...)
	}

	return responses
}

// processGeminiTextPart handles regular text parts
func processGeminiTextPart(part *Part, state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	var outputIndex int
	// If this is the first text, emit output_item.added and content_part.added
	if !state.HasStartedText {
		outputIndex = state.nextOutputIndex()
		state.TextOutputIndex = outputIndex // Cache the text item's output index
		itemID := state.generateItemID("item", outputIndex)
		state.ItemIDs[outputIndex] = itemID

		// Emit output_item.added
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ItemID:         &itemID,
			Item: &schemas.ResponsesMessage{
				ID:     &itemID,
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Status: schemas.Ptr("in_progress"),
				Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{},
				},
			},
		})

		// Emit content_part.added
		contentIndex := 0
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ContentIndex:   &contentIndex,
			ItemID:         &itemID,
			Part: &schemas.ResponsesMessageContentBlock{
				Type: schemas.ResponsesOutputMessageContentTypeText,
				Text: schemas.Ptr(""),
				ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
					LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
					Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
				},
			},
		})

		state.HasStartedText = true
	} else {
		// Text already started, reuse the cached text item's output index
		outputIndex = state.TextOutputIndex
	}

	// Emit output_text.delta for the text content
	if part.Text != "" {
		itemID := state.ItemIDs[outputIndex]
		contentIndex := 0
		text := part.Text

		// Accumulate text for output_text.done
		state.TextBuffer.WriteString(text)

		streamResponse := &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ContentIndex:   &contentIndex,
			ItemID:         &itemID,
			Delta:          &text,
			LogProbs:       []schemas.ResponsesOutputMessageContentTextLogProb{},
		}
		if len(part.ThoughtSignature) > 0 {
			thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
			streamResponse.Signature = &thoughtSig
		}

		responses = append(responses, streamResponse)
	}

	return responses
}

// processGeminiThoughtPart handles reasoning/thought parts
func processGeminiThoughtPart(part *Part, state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Close text item if open
	if closeResponses := state.closeTextItemIfOpen(sequenceNumber); closeResponses != nil {
		responses = append(responses, closeResponses...)
	}

	// For Gemini thoughts/reasoning, we emit them as reasoning summary text deltas
	outputIndex := state.nextOutputIndex()
	itemID := state.generateItemID("reasoning", outputIndex)
	state.ItemIDs[outputIndex] = itemID

	// Emit output_item.added for reasoning
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:   &itemID,
			Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		},
	})

	// Emit reasoning summary part added
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryPartAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
	})

	// Emit reasoning summary text delta with the thought content
	if part.Text != "" {
		text := part.Text
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ItemID:         &itemID,
			Delta:          &text,
		})
	}

	// Emit reasoning summary text done
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
	})

	// Emit reasoning summary part done
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryPartDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
	})

	// Emit output_item.done for reasoning
	statusCompleted := "completed"
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:     &itemID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Status: &statusCompleted,
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary: []schemas.ResponsesReasoningSummary{},
			},
		},
	})

	return responses
}

// processGeminiThoughtSignaturePart handles encrypted reasoning content (thoughtSignature)
func processGeminiThoughtSignaturePart(part *Part, state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Close text item if open
	if closeResponses := state.closeTextItemIfOpen(sequenceNumber); closeResponses != nil {
		responses = append(responses, closeResponses...)
	}

	// Create a new reasoning item for the thought signature
	outputIndex := state.nextOutputIndex()
	itemID := state.generateItemID("reasoning", outputIndex)
	state.ItemIDs[outputIndex] = itemID

	// Convert thoughtSignature to string
	thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)

	// Emit output_item.added for reasoning with encrypted content
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:   &itemID,
			Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary:          []schemas.ResponsesReasoningSummary{},
				EncryptedContent: &thoughtSig,
			},
		},
	})

	// Emit output_item.done for reasoning (thought signature is complete)
	statusCompleted := "completed"
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:     &itemID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Status: &statusCompleted,
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary:          []schemas.ResponsesReasoningSummary{},
				EncryptedContent: &thoughtSig,
			},
		},
	})

	return responses
}

// processGeminiFunctionCallPart handles function call parts
func processGeminiFunctionCallPart(part *Part, state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Close text item if open
	if closeResponses := state.closeTextItemIfOpen(sequenceNumber); closeResponses != nil {
		responses = append(responses, closeResponses...)
	}

	// Start new function call item
	outputIndex := state.nextOutputIndex()

	toolUseID := part.FunctionCall.ID
	if toolUseID == "" {
		toolUseID = part.FunctionCall.Name // Fallback to name as ID
	}

	state.ItemIDs[outputIndex] = toolUseID
	state.ToolCallIDs[outputIndex] = toolUseID
	state.ToolCallNames[outputIndex] = part.FunctionCall.Name

	// Convert args to JSON string
	argsJSON := ""
	if part.FunctionCall.Args != nil {
		if argsBytes, err := sonic.Marshal(part.FunctionCall.Args); err == nil {
			argsJSON = string(argsBytes)
		}
	}
	state.ToolArgumentBuffers[outputIndex] = argsJSON

	// Attach thought signature to ID if present
	if len(part.ThoughtSignature) > 0 && !strings.Contains(toolUseID, thoughtSignatureSeparator) {
		encoded := base64.RawURLEncoding.EncodeToString(part.ThoughtSignature)
		toolUseID = fmt.Sprintf("%s%s%s", toolUseID, thoughtSignatureSeparator, encoded)
	}

	// Emit output_item.added for function call
	status := "in_progress"
	addedEvent := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &toolUseID,
		Item: &schemas.ResponsesMessage{
			ID:     &toolUseID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			Status: &status,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    &toolUseID,
				Name:      &part.FunctionCall.Name,
				Arguments: schemas.Ptr(""),
			},
		},
	}

	responses = append(responses, addedEvent)

	// Generate synthetic argument deltas to simulate streaming behavior
	if argsJSON != "" {
		deltaEvents := generateSyntheticFunctionCallArgumentDeltas(
			argsJSON,
			&outputIndex,
			&toolUseID,
			sequenceNumber+len(responses),
		)
		responses = append(responses, deltaEvents...)
	}

	// Gemini sends complete function calls, so emit done event after synthetic deltas
	doneEvent := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &toolUseID,
		Arguments:      &argsJSON,
		Item: &schemas.ResponsesMessage{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: &toolUseID,
				Name:   &part.FunctionCall.Name,
			},
		},
	}

	responses = append(responses, doneEvent)

	outputItemDone := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &toolUseID,
		Item: &schemas.ResponsesMessage{
			ID:     &toolUseID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			Status: schemas.Ptr("completed"),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    &toolUseID,
				Name:      &part.FunctionCall.Name,
				Arguments: &argsJSON,
			},
		},
	}

	responses = append(responses, outputItemDone)

	delete(state.ToolArgumentBuffers, outputIndex)

	state.HasStartedToolCall = true

	return responses
}

// processGeminiFunctionResponsePart handles function response (tool result) parts
func processGeminiFunctionResponsePart(part *Part, state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Close text item if open
	if closeResponses := state.closeTextItemIfOpen(sequenceNumber); closeResponses != nil {
		responses = append(responses, closeResponses...)
	}

	// Extract output from function response
	output := extractFunctionResponseOutput(part.FunctionResponse)

	// Create new output item for the function response
	outputIndex := state.nextOutputIndex()

	responseID := part.FunctionResponse.ID
	if responseID == "" {
		responseID = part.FunctionResponse.Name // Fallback to name
	}

	itemID := fmt.Sprintf("func_resp_%s", responseID)
	state.ItemIDs[outputIndex] = itemID

	// Emit output_item.added for function call output
	status := "completed"
	item := &schemas.ResponsesMessage{
		ID:     &itemID,
		Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
		Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Status: &status,
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: &responseID,
			Output: &schemas.ResponsesToolMessageOutputStruct{
				ResponsesToolCallOutputStr: &output,
			},
		},
	}

	// Set tool name if present
	if name := strings.TrimSpace(part.FunctionResponse.Name); name != "" {
		item.ResponsesToolMessage.Name = schemas.Ptr(name)
	}

	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item:           item,
	})

	// Immediately emit output_item.done since function responses are complete
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:     &itemID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
			Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Status: &status,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: &responseID,
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: &output,
				},
			},
		},
	})
	// Add tool name if present
	if name := strings.TrimSpace(part.FunctionResponse.Name); name != "" {
		last := responses[len(responses)-1]
		if last.Item != nil && last.Item.ResponsesToolMessage != nil {
			last.Item.ResponsesToolMessage.Name = schemas.Ptr(name)
		}
	}

	return responses
}

// processGeminiInlineDataPart handles inline data parts
func processGeminiInlineDataPart(part *Part, state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Close text item if open
	if closeResponses := state.closeTextItemIfOpen(sequenceNumber); closeResponses != nil {
		responses = append(responses, closeResponses...)
	}

	// Convert inline data to content block
	block := convertGeminiInlineDataToContentBlock(part.InlineData)
	if block == nil {
		return responses
	}

	// Create new output item for the inline data
	outputIndex := state.nextOutputIndex()
	itemID := state.generateItemID("item", outputIndex)
	state.ItemIDs[outputIndex] = itemID

	// Emit output_item.added with the inline data content block
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:   &itemID,
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{*block},
			},
		},
	})

	// Emit content_part.added
	contentIndex := 0
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ContentIndex:   &contentIndex,
		ItemID:         &itemID,
		Part:           block,
	})

	// Emit content_part.done
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ContentIndex:   &contentIndex,
		ItemID:         &itemID,
		Part:           block,
	})

	// Emit output_item.done
	statusCompleted := "completed"
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:     &itemID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Status: &statusCompleted,
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{},
			},
		},
	})

	return responses
}

// processGeminiFileDataPart handles file data parts
func processGeminiFileDataPart(part *Part, state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	// Close text item if open
	if closeResponses := state.closeTextItemIfOpen(sequenceNumber); closeResponses != nil {
		responses = append(responses, closeResponses...)
	}

	// Convert file data to content block
	block := convertGeminiFileDataToContentBlock(part.FileData)
	if block == nil {
		return responses
	}

	// Create new output item for the file data
	outputIndex := state.nextOutputIndex()
	itemID := state.generateItemID("item", outputIndex)
	state.ItemIDs[outputIndex] = itemID

	// Emit output_item.added with the file data content block
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:   &itemID,
			Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{*block},
			},
		},
	})

	// Emit content_part.added
	contentIndex := 0
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ContentIndex:   &contentIndex,
		ItemID:         &itemID,
		Part:           block,
	})

	// Emit content_part.done
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ContentIndex:   &contentIndex,
		ItemID:         &itemID,
		Part:           block,
	})

	// Emit output_item.done
	statusCompleted := "completed"
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:     &itemID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Status: &statusCompleted,
			Content: &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{},
			},
		},
	})

	return responses
}

// closeGeminiTextItem closes the text item and emits appropriate done events
func closeGeminiTextItem(state *GeminiResponsesStreamState, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	outputIndex := state.TextOutputIndex
	itemID := state.ItemIDs[outputIndex]
	contentIndex := 0

	// Emit output_text.done
	fullText := state.TextBuffer.String()
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputTextDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ContentIndex:   &contentIndex,
		ItemID:         &itemID,
		Text:           &fullText,
		LogProbs:       []schemas.ResponsesOutputMessageContentTextLogProb{},
	})

	// Emit content_part.done
	part := &schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesOutputMessageContentTypeText,
		Text: schemas.Ptr(""),
		ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
			LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
			Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
		},
	}
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ContentIndex:   &contentIndex,
		ItemID:         &itemID,
		Part:           part,
	})

	// Emit output_item.done
	doneItem := &schemas.ResponsesMessage{
		Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Status: schemas.Ptr("completed"),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{},
		},
	}
	if itemID != "" {
		doneItem.ID = &itemID
	}
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item:           doneItem,
	})

	state.TextItemClosed = true

	return responses
}

// closeGeminiOpenItems closes any open items and emits the final completed event
func closeGeminiOpenItems(state *GeminiResponsesStreamState, groundingMetadata *GroundingMetadata, usage *GenerateContentResponseUsageMetadata, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	if state.HasEmittedCompleted {
		return nil
	}

	var responses []*schemas.BifrostResponsesStreamResponse

	// Close text item if still open
	if closeResponses := state.closeTextItemIfOpen(sequenceNumber); closeResponses != nil {
		responses = append(responses, closeResponses...)
	}

	// Emit annotations from grounding supports if present
	if groundingMetadata != nil && len(groundingMetadata.GroundingSupports) > 0 && state.TextOutputIndex >= 0 {
		annotationResponses := emitAnnotationsFromGroundingSupports(
			groundingMetadata,
			state,
			sequenceNumber+len(responses),
		)
		responses = append(responses, annotationResponses...)
	}

	// Close any open tool calls
	for outputIndex := range state.ToolArgumentBuffers {
		itemID := state.ItemIDs[outputIndex]
		toolCallID := state.ToolCallIDs[outputIndex]
		toolName := state.ToolCallNames[outputIndex]
		toolArgs := state.ToolArgumentBuffers[outputIndex]
		if strings.TrimSpace(toolName) == "" {
			toolName = toolCallID
		}

		// Emit output_item.done for tool call
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ItemID:         &itemID,
			Item: &schemas.ResponsesMessage{
				ID:     &itemID,
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    &toolCallID,
					Name:      &toolName,
					Arguments: &toolArgs,
				},
			},
		})
	}

	// Emit response.completed with usage
	bifrostUsage := convertGeminiUsageMetadataToResponsesUsage(usage)

	completedResp := &schemas.BifrostResponsesResponse{
		ID:        state.MessageID,
		CreatedAt: state.CreatedAt,
		Usage:     bifrostUsage,
	}
	if state.Model != nil {
		completedResp.Model = *state.Model
	}

	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeCompleted,
		SequenceNumber: sequenceNumber + len(responses),
		Response:       completedResp,
	})

	state.HasEmittedCompleted = true

	return responses
}

// FinalizeGeminiResponsesStream finalizes the stream by closing any open items and emitting completed event
func FinalizeGeminiResponsesStream(state *GeminiResponsesStreamState, usage *GenerateContentResponseUsageMetadata, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	return closeGeminiOpenItems(state, nil, usage, sequenceNumber)
}

// convertGeminiSystemInstructionToResponsesMessage converts Gemini SystemInstruction to a system role message
func convertGeminiSystemInstructionToResponsesMessage(systemInstruction *Content) *schemas.ResponsesMessage {
	if systemInstruction == nil || len(systemInstruction.Parts) == 0 {
		return nil
	}

	var contentBlocks []schemas.ResponsesMessageContentBlock
	var hasTextContent bool

	for _, part := range systemInstruction.Parts {
		if part.Text != "" {
			contentBlocks = append(contentBlocks, schemas.ResponsesMessageContentBlock{
				Type: schemas.ResponsesInputMessageContentBlockTypeText,
				Text: &part.Text,
			})
			hasTextContent = true
		}
	}

	if !hasTextContent {
		return nil
	}

	// If single text block, use ContentStr
	if len(contentBlocks) == 1 {
		return &schemas.ResponsesMessage{
			Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
			Content: &schemas.ResponsesMessageContent{
				ContentStr: contentBlocks[0].Text,
			},
		}
	}

	// Multiple blocks, use ContentBlocks
	return &schemas.ResponsesMessage{
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleSystem),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: contentBlocks,
		},
	}
}

func convertGeminiContentsToResponsesMessages(contents []Content) []schemas.ResponsesMessage {
	var messages []schemas.ResponsesMessage
	// Track function call IDs by name to match with responses
	functionCallIDs := make(map[string]string)

	for _, content := range contents {
		// Determine the role for all messages from this Content
		var role *schemas.ResponsesMessageRoleType
		switch content.Role {
		case "model":
			role = schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant)
		case "user":
			role = schemas.Ptr(schemas.ResponsesInputMessageRoleUser)
		default:
			// Default to user for unknown roles
			role = schemas.Ptr(schemas.ResponsesInputMessageRoleUser)
		}

		// Process each part - each part can become a separate message
		for _, part := range content.Parts {
			switch {
			case part.FunctionCall != nil:
				// Function call message
				argsJSON := "{}"
				if part.FunctionCall.Args != nil {
					if argsBytes, err := sonic.Marshal(part.FunctionCall.Args); err == nil {
						argsJSON = string(argsBytes)
					}
				}

				callID := part.FunctionCall.ID
				if callID == "" {
					callID = part.FunctionCall.Name
				}

				// Track this function call ID by name for later matching with responses
				functionCallIDs[part.FunctionCall.Name] = callID

				msg := schemas.ResponsesMessage{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID:    &callID,
						Name:      &part.FunctionCall.Name,
						Arguments: &argsJSON,
					},
				}
				messages = append(messages, msg)

				// If this part also has a thought signature, create a separate reasoning message
				if len(part.ThoughtSignature) > 0 {
					thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
					reasoningMsg := schemas.ResponsesMessage{
						Role: role,
						Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
						ResponsesReasoning: &schemas.ResponsesReasoning{
							Summary:          []schemas.ResponsesReasoningSummary{},
							EncryptedContent: &thoughtSig,
						},
					}
					messages = append(messages, reasoningMsg)
				}

			case part.FunctionResponse != nil:
				// Function response message
				responseID := part.FunctionResponse.ID
				if responseID == "" {
					// Try to find the matching function call ID by name
					if callID, ok := functionCallIDs[part.FunctionResponse.Name]; ok {
						responseID = callID
					} else {
						// Fallback to function name if no matching call found
						responseID = part.FunctionResponse.Name
					}
				}

				// Convert response map to string
				responseStr := ""
				if part.FunctionResponse.Response != nil {
					if output, ok := part.FunctionResponse.Response["output"].(string); ok {
						responseStr = output
					} else if responseBytes, err := sonic.Marshal(part.FunctionResponse.Response); err == nil {
						responseStr = string(responseBytes)
					}
				}

				msg := schemas.ResponsesMessage{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: &responseID,
						Output: &schemas.ResponsesToolMessageOutputStruct{
							ResponsesToolCallOutputStr: &responseStr,
						},
					},
				}

				messages = append(messages, msg)

			case part.Thought && part.Text != "":
				// Thought/reasoning text content
				msg := schemas.ResponsesMessage{
					Role: role,
					Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesOutputMessageContentTypeReasoning,
								Text: &part.Text,
							},
						},
					},
				}
				messages = append(messages, msg)

			case part.Text != "":
				// Regular text message
				msg := schemas.ResponsesMessage{
					Role: role,
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: func() schemas.ResponsesMessageContentBlockType {
									if content.Role == "model" {
										return schemas.ResponsesOutputMessageContentTypeText
									}
									return schemas.ResponsesInputMessageContentBlockTypeText
								}(),
								Text: &part.Text,
							},
						},
					},
				}

				// add signature to above text content block if present
				if len(part.ThoughtSignature) > 0 {
					thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
					msg.Content.ContentBlocks[len(msg.Content.ContentBlocks)-1].Signature = &thoughtSig
				}

				messages = append(messages, msg)

			case part.InlineData != nil:
				// Handle inline data (images, audio, files)
				block := convertGeminiInlineDataToContentBlock(part.InlineData)
				if block != nil {
					msg := schemas.ResponsesMessage{
						Role: role,
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{*block},
						},
					}
					messages = append(messages, msg)
				}

			case part.FileData != nil:
				// Handle file data (URI-based)
				block := convertGeminiFileDataToContentBlock(part.FileData)
				if block != nil {
					msg := schemas.ResponsesMessage{
						Role: role,
						Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{*block},
						},
					}
					messages = append(messages, msg)
				}
			}
		}
	}

	return messages
}

// convertGeminiInlineDataToContentBlock converts Gemini inline data (blob) to content block
func convertGeminiInlineDataToContentBlock(blob *Blob) *schemas.ResponsesMessageContentBlock {
	if blob == nil {
		return nil
	}

	// Determine content type based on MIME type
	mimeType := blob.MIMEType
	if mimeType == "" {
		return nil
	}

	// Handle images
	if isImageMimeType(mimeType) {
		// Convert to base64 data URL
		imageURL := fmt.Sprintf("data:%s;base64,%s", mimeType, blob.Data)
		return &schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesInputMessageContentBlockTypeImage,
			ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
				ImageURL: &imageURL,
			},
		}
	}

	// Handle audio
	if strings.HasPrefix(mimeType, "audio/") {
		encodedData := blob.Data
		format := mimeType
		if strings.HasPrefix(mimeType, "audio/") {
			format = mimeType[6:] // Remove "audio/" prefix
		}

		return &schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesInputMessageContentBlockTypeAudio,
			Audio: &schemas.ResponsesInputMessageContentBlockAudio{
				Format: format,
				Data:   encodedData,
			},
		}
	}

	// Handle other files - format as data URL
	mimeTypeForFile := mimeType
	if mimeTypeForFile == "" {
		mimeTypeForFile = "application/pdf"
	}

	filename := blob.DisplayName
	if filename == "" {
		filename = "unnamed_file"
	}

	fileDataURL := blob.Data
	if !strings.HasPrefix(fileDataURL, "data:") {
		fileDataURL = fmt.Sprintf("data:%s;base64,%s", mimeTypeForFile, fileDataURL)
	}
	return &schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesInputMessageContentBlockTypeFile,
		ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
			FileData: &fileDataURL,
			FileType: &mimeTypeForFile,
			Filename: &filename,
		},
	}
}

// convertGeminiFileDataToContentBlock converts Gemini file data (URI) to content block
func convertGeminiFileDataToContentBlock(fileData *FileData) *schemas.ResponsesMessageContentBlock {
	if fileData == nil || fileData.FileURI == "" {
		return nil
	}

	mimeType := fileData.MIMEType
	if mimeType == "" {
		mimeType = "application/pdf"
	}

	// Handle images
	if isImageMimeType(mimeType) {
		return &schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesInputMessageContentBlockTypeImage,
			ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
				ImageURL: &fileData.FileURI,
			},
		}
	}

	// Handle other files
	block := &schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesInputMessageContentBlockTypeFile,
		ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
			FileURL: &fileData.FileURI,
		},
	}

	// Set FileType if available
	block.ResponsesInputMessageContentBlockFile.FileType = &mimeType

	return block
}

func convertGeminiToolsToResponsesTools(tools []Tool) []schemas.ResponsesTool {
	var responsesTools []schemas.ResponsesTool

	for _, tool := range tools {
		// you cant use function declarations and google search together
		if tool.GoogleSearch != nil {
			responsesTool := schemas.ResponsesTool{
				Type: schemas.ResponsesToolTypeWebSearch,
			}
			responsesTool.ResponsesToolWebSearch = &schemas.ResponsesToolWebSearch{}
			if tool.GoogleSearch.TimeRangeFilter != nil || len(tool.GoogleSearch.ExcludeDomains) > 0 {
				filters := &schemas.ResponsesToolWebSearchFilters{
					BlockedDomains: tool.GoogleSearch.ExcludeDomains,
				}
				if tool.GoogleSearch.TimeRangeFilter != nil {
					filters.TimeRangeFilter = &schemas.Interval{
						StartTime: tool.GoogleSearch.TimeRangeFilter.StartTime,
						EndTime:   tool.GoogleSearch.TimeRangeFilter.EndTime,
					}
				}
				responsesTool.ResponsesToolWebSearch.Filters = filters
			}
			responsesTools = append(responsesTools, responsesTool)
		} else if len(tool.FunctionDeclarations) > 0 {
			for _, fn := range tool.FunctionDeclarations {
				responsesTool := schemas.ResponsesTool{
					Type:                  schemas.ResponsesToolTypeFunction,
					Name:                  schemas.Ptr(fn.Name),
					Description:           schemas.Ptr(fn.Description),
					ResponsesToolFunction: &schemas.ResponsesToolFunction{},
				}
				// Convert parameters schema if present
				if fn.Parameters != nil {
					params := convertSchemaToFunctionParameters(fn.Parameters)
					responsesTool.ResponsesToolFunction.Parameters = &params
				}
				responsesTools = append(responsesTools, responsesTool)
			}
		}
	}

	return responsesTools
}

func convertGeminiToolConfigToToolChoice(toolConfig ToolConfig) *schemas.ResponsesToolChoice {
	if toolConfig.FunctionCallingConfig == nil {
		return nil
	}

	toolChoice := &schemas.ResponsesToolChoiceStruct{
		Type: schemas.ResponsesToolChoiceTypeFunction,
	}

	switch toolConfig.FunctionCallingConfig.Mode {
	case FunctionCallingConfigModeAuto:
		toolChoice.Mode = schemas.Ptr("auto")
	case FunctionCallingConfigModeNone:
		toolChoice.Mode = schemas.Ptr("none")
	default:
		toolChoice.Mode = schemas.Ptr("auto")
	}

	if toolConfig.FunctionCallingConfig.AllowedFunctionNames != nil {
		for _, functionName := range toolConfig.FunctionCallingConfig.AllowedFunctionNames {
			toolChoice.Tools = append(toolChoice.Tools, schemas.ResponsesToolChoiceAllowedToolDef{
				Type: string(schemas.ResponsesToolTypeFunction),
				Name: schemas.Ptr(functionName),
			})
		}
	}

	return &schemas.ResponsesToolChoice{
		ResponsesToolChoiceStruct: toolChoice,
	}
}

// Helper functions for Responses conversion
// convertGeminiCandidatesToResponsesOutput converts Gemini candidates to Responses output messages
func convertGeminiCandidatesToResponsesOutput(candidates []*Candidate) []schemas.ResponsesMessage {
	var messages []schemas.ResponsesMessage

	for _, candidate := range candidates {
		if candidate.Content == nil || len(candidate.Content.Parts) == 0 {
			continue
		}

		for _, part := range candidate.Content.Parts {
			// Handle different types of parts
			switch {
			case part.Thought:
				// Thinking/reasoning message
				if part.Text != "" {
					msg := schemas.ResponsesMessage{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
						Content: &schemas.ResponsesMessageContent{
							ContentBlocks: []schemas.ResponsesMessageContentBlock{
								{
									Type: schemas.ResponsesOutputMessageContentTypeReasoning,
									Text: &part.Text,
								},
							},
						},
						Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					}
					messages = append(messages, msg)
				}

			case part.Text != "":
				// Regular text message
				msg := schemas.ResponsesMessage{
					ID:     schemas.Ptr("msg_" + providerUtils.GetRandomString(50)),
					Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Status: schemas.Ptr("completed"),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesOutputMessageContentTypeText,
								Text: &part.Text,
								ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
									LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
									Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
								},
							},
						},
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				}
				// add signature to above text content block if present
				if len(part.ThoughtSignature) > 0 {
					thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
					msg.Content.ContentBlocks[len(msg.Content.ContentBlocks)-1].Signature = &thoughtSig
				}
				messages = append(messages, msg)

			case part.FunctionCall != nil:
				// Function call message
				// Convert Args to JSON string if it's not already a string
				argumentsStr := ""
				if part.FunctionCall.Args != nil {
					if argsBytes, err := sonic.Marshal(part.FunctionCall.Args); err == nil {
						argumentsStr = string(argsBytes)
					}
				}

				callID := part.FunctionCall.ID
				if strings.TrimSpace(callID) == "" {
					callID = part.FunctionCall.Name
				}

				// Attach thought signature to callID (same as streaming path)
				if len(part.ThoughtSignature) > 0 && !strings.Contains(callID, thoughtSignatureSeparator) {
					thoughtSig := base64.RawURLEncoding.EncodeToString(part.ThoughtSignature)
					callID = fmt.Sprintf("%s%s%s", callID, thoughtSignatureSeparator, thoughtSig)
				}

				name := part.FunctionCall.Name
				toolMsg := &schemas.ResponsesToolMessage{
					CallID:    &callID,
					Name:      &name,
					Arguments: &argumentsStr,
				}
				msg := schemas.ResponsesMessage{
					ID:                   schemas.Ptr("fc_" + providerUtils.GetRandomString(50)),
					Role:                 schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					Status:               schemas.Ptr("completed"),
					ResponsesToolMessage: toolMsg,
				}
				messages = append(messages, msg)

			case part.FunctionResponse != nil:
				// Function response message
				output := extractFunctionResponseOutput(part.FunctionResponse)

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
					ResponsesToolMessage: &schemas.ResponsesToolMessage{
						CallID: schemas.Ptr(part.FunctionResponse.ID),
						Output: &schemas.ResponsesToolMessageOutputStruct{
							ResponsesToolCallOutputStr: &output,
						},
					},
				}

				// Also set the tool name if present (Gemini associates on name)
				if name := strings.TrimSpace(part.FunctionResponse.Name); name != "" {
					msg.ResponsesToolMessage.Name = schemas.Ptr(name)
				} else {
					// set name from call id
					// if it contains a thought signature, remove it
					if strings.Contains(part.FunctionResponse.ID, thoughtSignatureSeparator) {
						parts := strings.SplitN(part.FunctionResponse.ID, thoughtSignatureSeparator, 2)
						if len(parts) == 2 {
							name := parts[0]
							msg.ResponsesToolMessage.Name = schemas.Ptr(name)
						}
					} else {
						msg.ResponsesToolMessage.Name = schemas.Ptr(part.FunctionResponse.ID)
					}
				}
				messages = append(messages, msg)

			case part.InlineData != nil:
				// Handle inline data (images, audio, etc.)
				contentBlocks := []schemas.ResponsesMessageContentBlock{
					{
						Type: func() schemas.ResponsesMessageContentBlockType {
							if strings.HasPrefix(part.InlineData.MIMEType, "image/") {
								return schemas.ResponsesInputMessageContentBlockTypeImage
							} else if strings.HasPrefix(part.InlineData.MIMEType, "audio/") {
								return schemas.ResponsesInputMessageContentBlockTypeAudio
							}
							return schemas.ResponsesInputMessageContentBlockTypeText
						}(),
						ResponsesInputMessageContentBlockImage: func() *schemas.ResponsesInputMessageContentBlockImage {
							if strings.HasPrefix(part.InlineData.MIMEType, "image/") {
								return &schemas.ResponsesInputMessageContentBlockImage{
									ImageURL: schemas.Ptr("data:" + part.InlineData.MIMEType + ";base64," + part.InlineData.Data),
								}
							}
							return nil
						}(),
						Audio: func() *schemas.ResponsesInputMessageContentBlockAudio {
							if strings.HasPrefix(part.InlineData.MIMEType, "audio/") {
								// Extract format from MIME type (e.g., "audio/wav" -> "wav")
								format := strings.TrimPrefix(part.InlineData.MIMEType, "audio/")
								return &schemas.ResponsesInputMessageContentBlockAudio{
									Format: format,
									Data:   part.InlineData.Data,
								}
							}
							return nil
						}(),
					},
				}

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: contentBlocks,
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				}
				messages = append(messages, msg)

			case part.FileData != nil:
				// Handle file data
				block := schemas.ResponsesMessageContentBlock{
					Type: schemas.ResponsesInputMessageContentBlockTypeFile,
					ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
						FileURL: schemas.Ptr(part.FileData.FileURI),
					},
				}
				if strings.HasPrefix(part.FileData.MIMEType, "image/") {
					block.Type = schemas.ResponsesInputMessageContentBlockTypeImage
					block.ResponsesInputMessageContentBlockImage = &schemas.ResponsesInputMessageContentBlockImage{
						ImageURL: schemas.Ptr(part.FileData.FileURI),
					}
				}
				contentBlocks := []schemas.ResponsesMessageContentBlock{block}

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: contentBlocks,
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				}
				messages = append(messages, msg)

			case part.CodeExecutionResult != nil:
				// Handle code execution results
				output := part.CodeExecutionResult.Output
				if part.CodeExecutionResult.Outcome != OutcomeOK {
					output = "Error: " + output
				}

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesOutputMessageContentTypeText,
								Text: &output,
							},
						},
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeCodeInterpreterCall),
				}
				messages = append(messages, msg)

			case part.ExecutableCode != nil:
				// Handle executable code
				codeContent := "```" + part.ExecutableCode.Language + "\n" + part.ExecutableCode.Code + "\n```"

				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesOutputMessageContentTypeText,
								Text: &codeContent,
							},
						},
					},
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				}
				messages = append(messages, msg)
			case part.ThoughtSignature != nil:
				// Handle thought signature
				thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
				msg := schemas.ResponsesMessage{
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
					ResponsesReasoning: &schemas.ResponsesReasoning{
						Summary:          []schemas.ResponsesReasoningSummary{},
						EncryptedContent: &thoughtSig,
					},
				}
				messages = append(messages, msg)
			}
		}

		// check if gemini used google search tool
		if candidate.GroundingMetadata != nil {
			webSearchmessage := schemas.ResponsesMessage{
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Action: &schemas.ResponsesToolMessageActionStruct{
						ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{
							Type:    "search",
							Queries: candidate.GroundingMetadata.WebSearchQueries,
						},
					},
				},
			}
			if len(candidate.GroundingMetadata.WebSearchQueries) > 0 {
				webSearchmessage.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction.Query =
					schemas.Ptr(candidate.GroundingMetadata.WebSearchQueries[0])
			}

			sources := []schemas.ResponsesWebSearchToolCallActionSearchSource{}
			for _, source := range candidate.GroundingMetadata.GroundingChunks {
				if source.Web != nil {
					sources = append(sources, schemas.ResponsesWebSearchToolCallActionSearchSource{
						Type:  "url",
						Title: schemas.Ptr(source.Web.Title),
						URL:   source.Web.URI,
					})
				}
			}

			if len(sources) > 0 {
				webSearchmessage.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction.Sources = sources
			}

			messages = append(messages, webSearchmessage)

			// create a annotations message for grounding supports
			if len(candidate.GroundingMetadata.GroundingSupports) > 0 {
				annotations := []schemas.ResponsesOutputMessageContentTextAnnotation{}
				for _, support := range candidate.GroundingMetadata.GroundingSupports {
					if support.Segment != nil {
						annotation := schemas.ResponsesOutputMessageContentTextAnnotation{
							Type:       "url_citation",
							Text:       schemas.Ptr(support.Segment.Text),
							StartIndex: schemas.Ptr(int(support.Segment.StartIndex)),
							EndIndex:   schemas.Ptr(int(support.Segment.EndIndex)),
						}

						// Look up URL from grounding chunks
						if len(support.GroundingChunkIndices) > 0 {
							chunkIdx := support.GroundingChunkIndices[0]
							if chunkIdx >= 0 && int(chunkIdx) < len(candidate.GroundingMetadata.GroundingChunks) {
								chunk := candidate.GroundingMetadata.GroundingChunks[chunkIdx]
								if chunk.Web != nil {
									annotation.URL = schemas.Ptr(chunk.Web.URI)
									if chunk.Web.Title != "" {
										annotation.Title = schemas.Ptr(chunk.Web.Title)
									}
								}
							}
						}

						if annotation.URL != nil {
							annotations = append(annotations, annotation)
						}
					}
				}
				annotationsMessage := schemas.ResponsesMessage{
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Status: schemas.Ptr("completed"),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesOutputMessageContentTypeText,
								Text: schemas.Ptr(""),
								ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
									Annotations: annotations,
								},
							},
						},
					},
				}
				messages = append(messages, annotationsMessage)
			}

			// Emit rendered content if present
			if candidate.GroundingMetadata.SearchEntryPoint != nil &&
				candidate.GroundingMetadata.SearchEntryPoint.RenderedContent != "" {
				renderedContentMessage := schemas.ResponsesMessage{
					Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Status: schemas.Ptr("completed"),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesOutputMessageContentTypeRenderedContent,
								ResponsesOutputMessageContentRenderedContent: &schemas.ResponsesOutputMessageContentRenderedContent{
									RenderedContent: candidate.GroundingMetadata.SearchEntryPoint.RenderedContent,
								},
							},
						},
					},
				}
				messages = append(messages, renderedContentMessage)
			}
		}
	}

	return messages
}

// convertTextConfigToGenerationConfig converts ResponsesTextConfig to Gemini's GenerationConfig fields
func convertTextConfigToGenerationConfig(textConfig *schemas.ResponsesTextConfig, config *GenerationConfig) {
	if textConfig == nil || config == nil {
		return
	}

	if textConfig.Format == nil {
		return
	}

	switch textConfig.Format.Type {
	case "json_schema":
		config.ResponseMIMEType = "application/json"
		if textConfig.Format.JSONSchema != nil {
			if schema := reconstructSchemaFromJSONSchema(textConfig.Format.JSONSchema); schema != nil {
				config.ResponseJSONSchema = schema
			}
			// no schema, mime type remains as is
		}

	case "json_object":
		config.ResponseMIMEType = "application/json"

	case "text":
		config.ResponseMIMEType = "text/plain"
	}
}

// reconstructSchemaFromJSONSchema rebuilds a schema map from ResponsesTextConfigFormatJSONSchema
func reconstructSchemaFromJSONSchema(jsonSchema *schemas.ResponsesTextConfigFormatJSONSchema) interface{} {
	var schema map[string]interface{}

	if jsonSchema.Schema != nil {
		// If Schema field is set, use it directly
		schemaMap, ok := (*jsonSchema.Schema).(map[string]interface{})
		if !ok {
			return *jsonSchema.Schema
		}
		schema = schemaMap
	} else {
		// New format: Schema is spread across individual fields
		schema = make(map[string]interface{})

		if jsonSchema.Type != nil {
			schema["type"] = *jsonSchema.Type
		}

		if jsonSchema.Properties != nil {
			schema["properties"] = *jsonSchema.Properties
		}

		if len(jsonSchema.Required) > 0 {
			schema["required"] = jsonSchema.Required
		}

		if jsonSchema.Description != nil {
			schema["description"] = *jsonSchema.Description
		}

		if jsonSchema.AdditionalProperties != nil {
			schema["additionalProperties"] = *jsonSchema.AdditionalProperties
		}

		if jsonSchema.Name != nil {
			schema["title"] = *jsonSchema.Name
		}

		// Return nil if no fields were populated
		if len(schema) == 0 {
			return nil
		}
	}

	// Normalize the schema for Gemini compatibility (handle union types, etc.)
	return normalizeSchemaForGemini(schema)
}

// convertParamsToGenerationConfigResponses converts ChatParameters to GenerationConfig for Responses
func (r *GeminiGenerationRequest) convertParamsToGenerationConfigResponses(params *schemas.ResponsesParameters) GenerationConfig {
	config := GenerationConfig{}

	if params.Temperature != nil {
		config.Temperature = schemas.Ptr(float64(*params.Temperature))
	}
	if params.TopP != nil {
		config.TopP = schemas.Ptr(float64(*params.TopP))
	}
	if params.MaxOutputTokens != nil {
		config.MaxOutputTokens = int32(*params.MaxOutputTokens)
	}
	if params.Reasoning != nil {
		config.ThinkingConfig = &GenerationConfigThinkingConfig{
			IncludeThoughts: true,
		}

		// Get max tokens for conversions
		maxTokens := DefaultCompletionMaxTokens
		if config.MaxOutputTokens > 0 {
			maxTokens = int(config.MaxOutputTokens)
		}
		minBudget := DefaultReasoningMinBudget

		hasMaxTokens := params.Reasoning.MaxTokens != nil
		hasEffort := params.Reasoning.Effort != nil
		supportsLevel := isGemini3Plus(r.Model) // Check if model is 3.0+

		// PRIORITY RULE: If both max_tokens and effort are present, use ONLY max_tokens (budget)
		// This ensures we send only thinkingBudget to Gemini, not thinkingLevel

		// Handle "none" effort explicitly (only if max_tokens not present)
		if !hasMaxTokens && hasEffort && *params.Reasoning.Effort == "none" {
			config.ThinkingConfig.IncludeThoughts = false
			config.ThinkingConfig.ThinkingBudget = schemas.Ptr(int32(0))
		} else if hasMaxTokens {
			// User provided max_tokens - use thinkingBudget (all Gemini models support this)
			// If both max_tokens and effort are present, we ignore effort and use ONLY max_tokens
			budget := *params.Reasoning.MaxTokens
			switch budget {
			case 0:
				config.ThinkingConfig.IncludeThoughts = false
				config.ThinkingConfig.ThinkingBudget = schemas.Ptr(int32(0))
			case DynamicReasoningBudget: // Special case: -1 means dynamic budget
				config.ThinkingConfig.ThinkingBudget = schemas.Ptr(int32(DynamicReasoningBudget))
			default:
				config.ThinkingConfig.ThinkingBudget = schemas.Ptr(int32(budget))
			}
		} else if hasEffort {
			// User provided effort only (no max_tokens)
			if supportsLevel {
				// Gemini 3.0+ - use thinkingLevel (more native)
				config.ThinkingConfig.ThinkingLevel = schemas.Ptr(effortToThinkingLevel(*params.Reasoning.Effort, r.Model))
			} else {
				// Gemini < 3.0 - must convert effort to budget
				budgetTokens, err := providerUtils.GetBudgetTokensFromReasoningEffort(
					*params.Reasoning.Effort,
					minBudget,
					maxTokens,
				)
				if err == nil {
					config.ThinkingConfig.ThinkingBudget = schemas.Ptr(int32(budgetTokens))
				}
			}
		}
	}
	if params.Text != nil {
		convertTextConfigToGenerationConfig(params.Text, &config)
	}

	if params.ExtraParams != nil {
		if topK, ok := params.ExtraParams["top_k"]; ok {
			if val, success := schemas.SafeExtractInt(topK); success {
				config.TopK = schemas.Ptr(val)
			}
		}
		if frequencyPenalty, ok := params.ExtraParams["frequency_penalty"]; ok {
			if val, success := schemas.SafeExtractFloat64(frequencyPenalty); success {
				config.FrequencyPenalty = schemas.Ptr(val)
			}
		}
		if presencePenalty, ok := params.ExtraParams["presence_penalty"]; ok {
			if val, success := schemas.SafeExtractFloat64(presencePenalty); success {
				config.PresencePenalty = schemas.Ptr(val)
			}
		}
		if stopSequences, ok := params.ExtraParams["stop_sequences"]; ok {
			if val, success := schemas.SafeExtractStringSlice(stopSequences); success {
				config.StopSequences = val
			}
		}

	}

	return config
}

// convertResponsesToolsToGemini converts Responses tools to Gemini tools
func convertResponsesToolsToGemini(tools []schemas.ResponsesTool) []Tool {
	geminiTool := Tool{}

	hasWebSearchTool := false

	for _, tool := range tools {
		if tool.Type == schemas.ResponsesToolTypeWebSearch {
			hasWebSearchTool = true
			break
		}
	}

	for _, tool := range tools {
		// you cant use function declarations and google search together
		if tool.Type == schemas.ResponsesToolTypeFunction && !hasWebSearchTool {
			// Extract function information from ResponsesExtendedTool
			if tool.ResponsesToolFunction != nil {
				if tool.Name != nil && tool.ResponsesToolFunction != nil {
					funcDecl := &FunctionDeclaration{
						Name: *tool.Name,
						Description: func() string {
							if tool.Description != nil {
								return *tool.Description
							}
							return ""
						}(),
						Parameters: func() *Schema {
							if tool.ResponsesToolFunction.Parameters != nil {
								return convertFunctionParametersToSchema(*tool.ResponsesToolFunction.Parameters)
							}
							return nil
						}(),
					}
					geminiTool.FunctionDeclarations = append(geminiTool.FunctionDeclarations, funcDecl)
				}
			}
		}
		if tool.Type == schemas.ResponsesToolTypeWebSearch {
			geminiTool.GoogleSearch = &GoogleSearch{}
			if tool.ResponsesToolWebSearch != nil && tool.ResponsesToolWebSearch.Filters != nil {
				if tool.ResponsesToolWebSearch.Filters.TimeRangeFilter != nil {
					geminiTool.GoogleSearch.TimeRangeFilter = &Interval{
						StartTime: tool.ResponsesToolWebSearch.Filters.TimeRangeFilter.StartTime,
						EndTime:   tool.ResponsesToolWebSearch.Filters.TimeRangeFilter.EndTime,
					}
				}
				if len(tool.ResponsesToolWebSearch.Filters.BlockedDomains) > 0 {
					geminiTool.GoogleSearch.ExcludeDomains = tool.ResponsesToolWebSearch.Filters.BlockedDomains
				}
			}
		}
	}

	if len(geminiTool.FunctionDeclarations) > 0 || geminiTool.GoogleSearch != nil {
		return []Tool{geminiTool}
	}
	return []Tool{}
}

// convertResponsesToolChoiceToGemini converts Responses tool choice to Gemini tool config
func convertResponsesToolChoiceToGemini(toolChoice *schemas.ResponsesToolChoice) ToolConfig {
	config := ToolConfig{}

	if toolChoice.ResponsesToolChoiceStruct != nil {
		funcConfig := &FunctionCallingConfig{}
		ext := toolChoice.ResponsesToolChoiceStruct

		if ext.Mode != nil {
			switch *ext.Mode {
			case "auto":
				funcConfig.Mode = FunctionCallingConfigModeAuto
			case "required":
				funcConfig.Mode = FunctionCallingConfigModeAny
			case "none":
				funcConfig.Mode = FunctionCallingConfigModeNone
			}
		}

		if ext.Name != nil {
			funcConfig.Mode = FunctionCallingConfigModeAny
			funcConfig.AllowedFunctionNames = []string{*ext.Name}
		}

		config.FunctionCallingConfig = funcConfig
		return config
	}

	// Handle string-based tool choice modes
	if toolChoice.ResponsesToolChoiceStr != nil {
		funcConfig := &FunctionCallingConfig{}
		switch *toolChoice.ResponsesToolChoiceStr {
		case "none":
			funcConfig.Mode = FunctionCallingConfigModeNone
		case "required", "any":
			funcConfig.Mode = FunctionCallingConfigModeAny
		default: // "auto" or any other value
			funcConfig.Mode = FunctionCallingConfigModeAuto
		}
		config.FunctionCallingConfig = funcConfig
	}

	return config
}

// convertResponsesMessagesToGeminiContents converts Responses messages to Gemini contents
func convertResponsesMessagesToGeminiContents(messages []schemas.ResponsesMessage) ([]Content, *Content, error) {
	var contents []Content
	var systemInstruction *Content

	// Track consecutive function call output messages to group them for parallel function calling
	// According to Gemini docs, all function responses must be in a single message
	var pendingFunctionResponseParts []*Part

	for i, msg := range messages {
		// Skip standalone reasoning messages (they're handled as part of function calls)
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeReasoning && msg.ResponsesReasoning != nil {
			continue
		}

		// Handle system messages separately
		if msg.Role != nil && *msg.Role == schemas.ResponsesInputMessageRoleSystem {
			if systemInstruction == nil {
				systemInstruction = &Content{}
			}

			// Convert system message content
			if msg.Content != nil {
				if msg.Content.ContentStr != nil {
					systemInstruction.Parts = append(systemInstruction.Parts, &Part{
						Text: *msg.Content.ContentStr,
					})
				}
				if msg.Content.ContentBlocks != nil {
					for _, block := range msg.Content.ContentBlocks {
						part, err := convertContentBlockToGeminiPart(block)
						if err != nil {
							return nil, nil, fmt.Errorf("failed to convert system message content block: %w", err)
						}
						if part != nil {
							systemInstruction.Parts = append(systemInstruction.Parts, part)
						}
					}
				}
			}

			continue
		}

		// Check if this is a function call output message
		isFunctionOutput := msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCallOutput && msg.ResponsesToolMessage != nil

		// If we have pending function responses and current message is NOT a function output,
		// flush the pending responses as a single Content (for parallel function calling)
		if len(pendingFunctionResponseParts) > 0 && !isFunctionOutput {
			contents = append(contents, Content{
				Parts: pendingFunctionResponseParts,
				Role:  "model", // Function responses use "model" role in Gemini
			})
			pendingFunctionResponseParts = nil
		}

		// Handle regular messages
		content := Content{}

		if msg.Role != nil {
			// Map Responses roles to Gemini roles (Gemini only supports "user" and "model")
			switch *msg.Role {
			case schemas.ResponsesInputMessageRoleAssistant:
				content.Role = "model"
			case schemas.ResponsesInputMessageRoleUser, schemas.ResponsesInputMessageRoleDeveloper:
				content.Role = "user"
			default:
				// Default to "user" for input messages (any instructions/context)
				content.Role = "user"
			}
		}

		// Handle tool calls/responses
		if msg.ResponsesToolMessage != nil && msg.Type != nil {
			switch *msg.Type {
			case schemas.ResponsesMessageTypeFunctionCall:
				// Convert function call to Gemini FunctionCall
				if msg.ResponsesToolMessage.Name != nil {
					argsMap := map[string]any{}
					if msg.ResponsesToolMessage.Arguments != nil {
						if err := sonic.Unmarshal([]byte(*msg.ResponsesToolMessage.Arguments), &argsMap); err != nil {
							return nil, nil, fmt.Errorf("failed to decode function call arguments: %w", err)
						}
					}

					var thoughtSig string
					part := &Part{
						FunctionCall: &FunctionCall{
							Name: *msg.ResponsesToolMessage.Name,
							Args: argsMap,
						},
					}
					if msg.ResponsesToolMessage.CallID != nil {
						if strings.Contains(*msg.ResponsesToolMessage.CallID, thoughtSignatureSeparator) {
							parts := strings.SplitN(*msg.ResponsesToolMessage.CallID, thoughtSignatureSeparator, 2)
							if len(parts) == 2 {
								thoughtSig = parts[1] // Extract signature (after separator)
							}
						}
						// Keep the full CallID as-is (don't strip thought signature)
						part.FunctionCall.ID = *msg.ResponsesToolMessage.CallID
					}
					if thoughtSig != "" {
						var err error
						part.ThoughtSignature, err = base64.RawURLEncoding.DecodeString(thoughtSig)
						if err != nil {
							// Silently ignore decode errors - ID will be used without signature
							thoughtSig = ""
						}
					}

					// Preserve thought signature from ResponsesReasoning message (required for Gemini 3 Pro)
					// Look ahead to see if the next message is a reasoning message with encrypted content
					if i+1 < len(messages) {
						nextMsg := messages[i+1]
						if nextMsg.Type != nil && *nextMsg.Type == schemas.ResponsesMessageTypeReasoning &&
							nextMsg.ResponsesReasoning != nil && nextMsg.ResponsesReasoning.EncryptedContent != nil {
							decodedSig, err := base64.StdEncoding.DecodeString(*nextMsg.ResponsesReasoning.EncryptedContent)
							if err == nil {
								part.ThoughtSignature = decodedSig
							}
						}
					}

					content.Parts = append(content.Parts, part)
				}

			case schemas.ResponsesMessageTypeFunctionCallOutput:
				// Convert function response - collect for grouping
				// According to Gemini parallel function calling docs, multiple function responses
				// must be sent in a single message with only functionResponse parts (no text/content parts)
				if msg.ResponsesToolMessage.CallID != nil {
					responseMap := make(map[string]any)

					// Extract output from ResponsesToolMessage.Output
					if msg.ResponsesToolMessage.Output != nil && msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
						responseMap["output"] = *msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr
					} else if msg.Content != nil && msg.Content.ContentStr != nil {
						// Fallback to Content.ContentStr for backward compatibility
						responseMap["output"] = *msg.Content.ContentStr
					}

					// Prefer the declared tool name; fallback to CallID if the name is absent
					funcName := ""
					if msg.ResponsesToolMessage.Name != nil && strings.TrimSpace(*msg.ResponsesToolMessage.Name) != "" {
						funcName = *msg.ResponsesToolMessage.Name
					} else {
						funcName = *msg.ResponsesToolMessage.CallID
					}

					part := &Part{
						FunctionResponse: &FunctionResponse{
							Name:     funcName,
							Response: responseMap,
							ID:       *msg.ResponsesToolMessage.CallID,
						},
					}
					pendingFunctionResponseParts = append(pendingFunctionResponseParts, part)

					// If this is the last message, flush pending responses
					if i == len(messages)-1 && len(pendingFunctionResponseParts) > 0 {
						contents = append(contents, Content{
							Parts: pendingFunctionResponseParts,
							Role:  "model",
						})
						pendingFunctionResponseParts = nil
					}

					continue // Skip normal content handling
				}
			}
		}

		// For non-function-output messages, convert message content normally
		if !isFunctionOutput {
			// Convert message content
			if msg.Content != nil {
				if msg.Content.ContentStr != nil {
					content.Parts = append(content.Parts, &Part{
						Text: *msg.Content.ContentStr,
					})
				}

				if msg.Content.ContentBlocks != nil {
					for _, block := range msg.Content.ContentBlocks {
						part, err := convertContentBlockToGeminiPart(block)
						if err != nil {
							return nil, nil, fmt.Errorf("failed to convert message content block: %w", err)
						}
						if part != nil {
							content.Parts = append(content.Parts, part)
						}
					}
				}
			}
		}

		if len(content.Parts) > 0 {
			contents = append(contents, content)
		}
	}

	return contents, systemInstruction, nil
}

// convertContentBlockToGeminiPart converts a content block to Gemini part
func convertContentBlockToGeminiPart(block schemas.ResponsesMessageContentBlock) (*Part, error) {
	switch block.Type {
	case schemas.ResponsesInputMessageContentBlockTypeText,
		schemas.ResponsesOutputMessageContentTypeText:
		if block.Text != nil && *block.Text != "" {
			part := &Part{
				Text: *block.Text,
			}
			if block.Signature != nil {
				decodedSig, err := base64.StdEncoding.DecodeString(*block.Signature)
				if err == nil {
					part.ThoughtSignature = decodedSig
				}
			}
			return part, nil
		}

	case schemas.ResponsesOutputMessageContentTypeReasoning:
		if block.Text != nil && *block.Text != "" {
			return &Part{
				Text:    *block.Text,
				Thought: true,
			}, nil
		}

	case schemas.ResponsesOutputMessageContentTypeRefusal:
		// Refusals are treated as regular text in Gemini
		if block.ResponsesOutputMessageContentRefusal != nil {
			return &Part{
				Text: block.ResponsesOutputMessageContentRefusal.Refusal,
			}, nil
		}

	case schemas.ResponsesInputMessageContentBlockTypeImage:
		if block.ResponsesInputMessageContentBlockImage != nil && block.ResponsesInputMessageContentBlockImage.ImageURL != nil {
			imageURL := *block.ResponsesInputMessageContentBlockImage.ImageURL

			// Use existing utility functions to handle URL parsing
			sanitizedURL, err := schemas.SanitizeImageURL(imageURL)
			if err != nil {
				return nil, fmt.Errorf("failed to sanitize image URL: %w", err)
			}

			urlInfo := schemas.ExtractURLTypeInfo(sanitizedURL)
			mimeType := "image/jpeg" // default
			if urlInfo.MediaType != nil {
				mimeType = *urlInfo.MediaType
			}

			if urlInfo.Type == schemas.ImageContentTypeBase64 {
				data := ""
				if urlInfo.DataURLWithoutPrefix != nil {
					data = *urlInfo.DataURLWithoutPrefix
				}

				// Decode base64 data (handles both standard and URL-safe base64)
				decodedData, err := decodeBase64StringToBytes(data)
				if err != nil {
					return nil, fmt.Errorf("failed to decode base64 image data: %w", err)
				}

				return &Part{
					InlineData: &Blob{
						MIMEType: mimeType,
						Data:     encodeBytesToBase64String(decodedData),
					},
				}, nil
			} else {
				return &Part{
					FileData: &FileData{
						MIMEType: mimeType,
						FileURI:  sanitizedURL,
					},
				}, nil
			}
		}

	case schemas.ResponsesInputMessageContentBlockTypeAudio:
		if block.Audio != nil {
			// Decode base64 audio data (handles both standard and URL-safe base64)
			decodedData, err := decodeBase64StringToBytes(block.Audio.Data)
			if err != nil {
				return nil, fmt.Errorf("failed to decode base64 audio data: %w", err)
			}

			return &Part{
				InlineData: &Blob{
					MIMEType: func() string {
						f := strings.ToLower(strings.TrimSpace(block.Audio.Format))
						if f == "" {
							return "audio/mpeg"
						}
						if strings.HasPrefix(f, "audio/") {
							return f
						}
						return "audio/" + f
					}(),
					Data: encodeBytesToBase64String(decodedData),
				},
			}, nil
		}

	case schemas.ResponsesInputMessageContentBlockTypeFile:
		if block.ResponsesInputMessageContentBlockFile != nil {
			fileBlock := block.ResponsesInputMessageContentBlockFile

			// Handle FileURL (URI-based file)
			if fileBlock.FileURL != nil {
				mimeType := "application/pdf"
				if fileBlock.FileType != nil {
					mimeType = *fileBlock.FileType
				}

				part := &Part{
					FileData: &FileData{
						MIMEType: mimeType,
						FileURI:  *fileBlock.FileURL,
					},
				}

				return part, nil
			}

			// Handle FileData (inline file data)
			if fileBlock.FileData != nil {
				mimeType := "application/pdf"
				if fileBlock.FileType != nil {
					mimeType = *fileBlock.FileType
				}

				// Convert file data to bytes using the helper function
				dataBytes, extractedMimeType := convertFileDataToBytes(*fileBlock.FileData)
				if extractedMimeType != "" {
					mimeType = extractedMimeType
				}

				if len(dataBytes) > 0 {
					part := &Part{
						InlineData: &Blob{
							MIMEType: mimeType,
							Data:     encodeBytesToBase64String(dataBytes),
						},
					}

					return part, nil
				}
			}
		}
	}

	return nil, nil
}

// buildGroundingMetadataFromWebSearch converts a Bifrost web_search_call message to Gemini GroundingMetadata
func buildGroundingMetadataFromWebSearch(webSearchCall *schemas.ResponsesMessage, annotations []schemas.ResponsesOutputMessageContentTextAnnotation, renderedContent *string) *GroundingMetadata {
	if webSearchCall == nil || webSearchCall.ResponsesToolMessage == nil || webSearchCall.ResponsesToolMessage.Action == nil {
		return nil
	}

	action := webSearchCall.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction
	if action == nil {
		return nil
	}

	groundingMetadata := &GroundingMetadata{}

	// Add SearchEntryPoint with rendered content if provided
	if renderedContent != nil && *renderedContent != "" {
		groundingMetadata.SearchEntryPoint = &SearchEntryPoint{
			RenderedContent: *renderedContent,
		}
	}

	// Extract web search queries
	if len(action.Queries) > 0 {
		groundingMetadata.WebSearchQueries = action.Queries
	} else if action.Query != nil {
		groundingMetadata.WebSearchQueries = []string{*action.Query}
	}

	// Extract grounding chunks from sources
	var groundingChunks []*GroundingChunk
	urlToIndexMap := make(map[string]int32) // Map URL to chunk index for annotation processing

	for _, source := range action.Sources {
		if source.URL == "" {
			continue
		}

		title := source.URL // Use URL as fallback
		if source.Title != nil && *source.Title != "" {
			title = *source.Title
		}

		chunk := &GroundingChunk{
			Web: &GroundingChunkWeb{
				URI:   source.URL,
				Title: title,
			},
		}
		groundingChunks = append(groundingChunks, chunk)
		urlToIndexMap[source.URL] = int32(len(groundingChunks) - 1)
	}

	if len(groundingChunks) > 0 {
		groundingMetadata.GroundingChunks = groundingChunks
	}

	// Convert annotations to grounding supports
	var groundingSupports []*GroundingSupport
	for _, annotation := range annotations {
		if annotation.Type != "url_citation" {
			continue
		}

		support := &GroundingSupport{
			Segment: &Segment{},
		}

		// Set segment text
		if annotation.Text != nil {
			support.Segment.Text = *annotation.Text
		}

		// Set segment indices
		if annotation.StartIndex != nil {
			support.Segment.StartIndex = int32(*annotation.StartIndex)
		}
		if annotation.EndIndex != nil {
			support.Segment.EndIndex = int32(*annotation.EndIndex)
		}

		// Map annotation URL to chunk indices
		if annotation.URL != nil {
			if chunkIdx, exists := urlToIndexMap[*annotation.URL]; exists {
				support.GroundingChunkIndices = []int32{chunkIdx}
			}
		}

		// Only add support if we have valid segment or chunk indices
		if support.Segment.Text != "" || len(support.GroundingChunkIndices) > 0 {
			groundingSupports = append(groundingSupports, support)
		}
	}

	if len(groundingSupports) > 0 {
		groundingMetadata.GroundingSupports = groundingSupports
	}

	// Return nil if no meaningful data was extracted
	if len(groundingMetadata.WebSearchQueries) == 0 && len(groundingMetadata.GroundingChunks) == 0 {
		return nil
	}

	return groundingMetadata
}

// emitWebSearchFromGroundingMetadata converts grounding metadata to web search event stream
func emitWebSearchFromGroundingMetadata(
	metadata *GroundingMetadata,
	state *GeminiResponsesStreamState,
	sequenceNumber int,
) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	if metadata == nil || len(metadata.WebSearchQueries) == 0 {
		return responses
	}

	outputIndex := state.nextOutputIndex()
	itemID := state.generateItemID("ws", outputIndex)
	state.ItemIDs[outputIndex] = itemID

	// Build web search action
	action := &schemas.ResponsesWebSearchToolCallAction{
		Type:    "search",
		Queries: metadata.WebSearchQueries,
	}
	if len(metadata.WebSearchQueries) > 0 {
		action.Query = &metadata.WebSearchQueries[0]
	}

	// Convert groundingChunks to sources
	var sources []schemas.ResponsesWebSearchToolCallActionSearchSource
	for _, chunk := range metadata.GroundingChunks {
		if chunk.Web != nil && chunk.Web.URI != "" {
			source := schemas.ResponsesWebSearchToolCallActionSearchSource{
				Type: "url",
				URL:  chunk.Web.URI,
			}
			if chunk.Web.Title != "" {
				source.Title = &chunk.Web.Title
			} else {
				source.Title = &chunk.Web.URI // Fallback to URI
			}
			sources = append(sources, source)
		}
	}
	action.Sources = sources

	// 1. output_item.added (web_search_call, in_progress)
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		Item: &schemas.ResponsesMessage{
			ID:     &itemID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
			Status: schemas.Ptr("in_progress"),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Action: &schemas.ResponsesToolMessageActionStruct{
					ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{
						Type: "search",
					},
				},
			},
		},
	})

	// 2. web_search_call.in_progress
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeWebSearchCallInProgress,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
	})

	// 3. web_search_call.searching
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeWebSearchCallSearching,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
	})

	// 4. web_search_call.completed
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeWebSearchCallCompleted,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
	})

	// 5. output_item.done (with full action including sources)
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ItemID:         &itemID,
		Item: &schemas.ResponsesMessage{
			ID:     &itemID,
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeWebSearchCall),
			Status: schemas.Ptr("completed"),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Action: &schemas.ResponsesToolMessageActionStruct{
					ResponsesWebSearchToolCallAction: action,
				},
			},
		},
	})

	state.HasEmittedWebSearch = true

	// Emit rendered content if present
	if metadata.SearchEntryPoint != nil && metadata.SearchEntryPoint.RenderedContent != "" {
		renderedIndex := state.nextOutputIndex()
		renderedItemID := state.generateItemID("rc", renderedIndex)
		state.ItemIDs[renderedIndex] = renderedItemID

		// output_item.added with rendered_content
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &renderedIndex,
			Item: &schemas.ResponsesMessage{
				ID:     &renderedItemID,
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Status: schemas.Ptr("completed"),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeRenderedContent,
							ResponsesOutputMessageContentRenderedContent: &schemas.ResponsesOutputMessageContentRenderedContent{
								RenderedContent: metadata.SearchEntryPoint.RenderedContent,
							},
						},
					},
				},
			},
		})

		// output_item.done for rendered content
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &renderedIndex,
			ItemID:         &renderedItemID,
		})
	}

	return responses
}

// emitAnnotationsFromGroundingSupports converts grounding supports to annotation events
func emitAnnotationsFromGroundingSupports(
	metadata *GroundingMetadata,
	state *GeminiResponsesStreamState,
	sequenceNumber int,
) []*schemas.BifrostResponsesStreamResponse {
	var responses []*schemas.BifrostResponsesStreamResponse

	if metadata == nil || len(metadata.GroundingSupports) == 0 || state.TextOutputIndex < 0 {
		return responses
	}

	itemID := state.ItemIDs[state.TextOutputIndex]

	emmitedIndex := 0
	// Convert each grounding support to an annotation event
	for _, support := range metadata.GroundingSupports {
		if support.Segment == nil {
			continue
		}

		annotation := schemas.ResponsesOutputMessageContentTextAnnotation{
			Type: "url_citation",
		}

		// Set text and indices
		if support.Segment.Text != "" {
			annotation.Text = &support.Segment.Text
		}
		annotation.StartIndex = schemas.Ptr(int(support.Segment.StartIndex))
		annotation.EndIndex = schemas.Ptr(int(support.Segment.EndIndex))

		// Find URL and title from chunk indices
		if len(support.GroundingChunkIndices) > 0 {
			chunkIdx := support.GroundingChunkIndices[0]
			if int(chunkIdx) < len(metadata.GroundingChunks) {
				chunk := metadata.GroundingChunks[chunkIdx]
				if chunk.Web != nil {
					annotation.URL = &chunk.Web.URI
					if chunk.Web.Title != "" {
						annotation.Title = &chunk.Web.Title
					}
				}
			}
		}

		if annotation.URL == nil {
			continue
		}

		// Emit annotation.added event
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:            schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded,
			SequenceNumber:  sequenceNumber + len(responses),
			OutputIndex:     &state.TextOutputIndex,
			ItemID:          &itemID,
			ContentIndex:    schemas.Ptr(0),
			Annotation:      &annotation,
			AnnotationIndex: &emmitedIndex,
		})
		emmitedIndex++
	}

	return responses
}

// generateSyntheticFunctionCallArgumentDeltas creates synthetic FunctionCallArgumentsDelta events
// from complete JSON arguments to simulate streaming behavior for providers that don't natively stream
func generateSyntheticFunctionCallArgumentDeltas(argumentsJSON string, outputIndex *int, itemID *string, baseSequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	var events []*schemas.BifrostResponsesStreamResponse

	// Chunk size for synthetic streaming (matching realistic streaming patterns)
	chunkSize := 8 // Small chunks to simulate realistic streaming

	// Break the JSON into chunks
	runes := []rune(argumentsJSON)
	for i := 0; i < len(runes); i += chunkSize {
		end := min(i+chunkSize, len(runes))

		chunk := string(runes[i:end])
		deltaEvent := &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			SequenceNumber: baseSequenceNumber + len(events),
			OutputIndex:    outputIndex,
			ItemID:         itemID,
			Delta:          &chunk,
		}
		events = append(events, deltaEvent)
	}

	return events
}
