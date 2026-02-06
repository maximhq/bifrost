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

func (request *GeminiGenerationRequest) ToBifrostResponsesRequest() *schemas.BifrostResponsesRequest {
	if request == nil {
		return nil
	}

	provider, model := schemas.ParseModelString(request.Model, schemas.Gemini)

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

	if bifrostReq.Params != nil && bifrostReq.Params.ExtraParams != nil {
		if safetySettings, ok := schemas.SafeExtractFromMap(bifrostReq.Params.ExtraParams, "safety_settings"); ok {
			if settings, ok := safetySettings.([]SafetySetting); ok {
				geminiReq.SafetySettings = settings
			}
		}
		if cachedContent, ok := schemas.SafeExtractString(bifrostReq.Params.ExtraParams["cached_content"]); ok {
			geminiReq.CachedContent = cachedContent
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
		Model: response.ModelVersion,
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

		for i, msg := range bifrostResp.Output {
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
						if msg.ResponsesToolMessage.CallID != nil {
							functionCall.ID = *msg.ResponsesToolMessage.CallID
						}

						part := &Part{
							FunctionCall: functionCall,
						}

						// Look ahead to see if the next message is a reasoning message with encrypted content
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

func ToGeminiResponsesStreamResponse(bifrostResp *schemas.BifrostResponsesStreamResponse) *GenerateContentResponse {
	if bifrostResp == nil {
		return nil
	}

	// Skip lifecycle events that don't have corresponding Gemini equivalents
	switch bifrostResp.Type {
	case schemas.ResponsesStreamResponseTypePing,
		schemas.ResponsesStreamResponseTypeCreated,
		schemas.ResponsesStreamResponseTypeInProgress,
		schemas.ResponsesStreamResponseTypeReasoningSummaryPartAdded,
		schemas.ResponsesStreamResponseTypeQueued:
		// These are lifecycle events with no Gemini equivalent
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
					if bifrostResp.Item.ResponsesToolMessage.CallID != nil {
						functionCall.ID = *bifrostResp.Item.ResponsesToolMessage.CallID
					}
					candidate.Content.Parts = append(candidate.Content.Parts, &Part{
						FunctionCall: functionCall,
					})
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
		if candidate.FinishReason != "" && (state.HasStartedText || state.HasStartedToolCall) {
			// Close any open items
			closeResponses := closeGeminiOpenItems(state, response.UsageMetadata, sequenceNumber+len(responses))
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
				ID:   &itemID,
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
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
			Status: &statusCompleted,
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
			Status: &statusCompleted,
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

	// Emit output_item.added for function call
	status := "in_progress"
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
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
				Arguments: &argsJSON,
			},
		},
	})

	// Gemini sends complete function calls, so immediately emit done event
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
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
	})

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
			Status: &status,
		},
	})

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
			Status: &statusCompleted,
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
			Status: &statusCompleted,
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
	})

	// Emit content_part.done
	responses = append(responses, &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
		SequenceNumber: sequenceNumber + len(responses),
		OutputIndex:    &outputIndex,
		ContentIndex:   &contentIndex,
		ItemID:         &itemID,
	})

	// Emit output_item.done
	doneItem := &schemas.ResponsesMessage{
		Status: schemas.Ptr("completed"),
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
func closeGeminiOpenItems(state *GeminiResponsesStreamState, usage *GenerateContentResponseUsageMetadata, sequenceNumber int) []*schemas.BifrostResponsesStreamResponse {
	if state.HasEmittedCompleted {
		return nil
	}

	var responses []*schemas.BifrostResponsesStreamResponse

	// Close text item if still open
	if closeResponses := state.closeTextItemIfOpen(sequenceNumber); closeResponses != nil {
		responses = append(responses, closeResponses...)
	}

	// Close any open tool calls
	for outputIndex := range state.ToolArgumentBuffers {
		itemID := state.ItemIDs[outputIndex]

		// Emit output_item.done for tool call
		responses = append(responses, &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
			SequenceNumber: sequenceNumber + len(responses),
			OutputIndex:    &outputIndex,
			ItemID:         &itemID,
			Item: &schemas.ResponsesMessage{
				ID:     &itemID,
				Status: schemas.Ptr("completed"),
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
	return closeGeminiOpenItems(state, usage, sequenceNumber)
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
		imageURL := fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(blob.Data))
		return &schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesInputMessageContentBlockTypeImage,
			ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
				ImageURL: &imageURL,
			},
		}
	}

	// Handle audio
	if strings.HasPrefix(mimeType, "audio/") {
		encodedData := base64.StdEncoding.EncodeToString(blob.Data)
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

	// Handle other files
	encodedData := base64.StdEncoding.EncodeToString(blob.Data)
	return &schemas.ResponsesMessageContentBlock{
		Type: schemas.ResponsesInputMessageContentBlockTypeFile,
		ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
			FileData: &encodedData,
			FileType: func() *string {
				if blob.MIMEType != "" {
					return &blob.MIMEType
				}
				return nil
			}(),
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
		if len(tool.FunctionDeclarations) > 0 {
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
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{
							{
								Type: schemas.ResponsesOutputMessageContentTypeText,
								Text: &part.Text,
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

				name := part.FunctionCall.Name
				toolMsg := &schemas.ResponsesToolMessage{
					CallID:    &callID,
					Name:      &name,
					Arguments: &argumentsStr,
				}
				msg := schemas.ResponsesMessage{
					Role:                 schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
					ResponsesToolMessage: toolMsg,
				}
				messages = append(messages, msg)

				// Preserve thought signature if present (required for Gemini 3 Pro)
				// Store it in a separate ResponsesReasoning message for better scalability
				if len(part.ThoughtSignature) > 0 {
					thoughtSig := base64.StdEncoding.EncodeToString(part.ThoughtSignature)
					reasoningMsg := schemas.ResponsesMessage{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
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
									ImageURL: schemas.Ptr("data:" + part.InlineData.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(part.InlineData.Data)),
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
									Data:   base64.StdEncoding.EncodeToString(part.InlineData.Data),
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
	if jsonSchema.Schema != nil {
		return *jsonSchema.Schema
	}

	// New format: Schema is spread across individual fields
	schema := make(map[string]interface{})

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

	return schema
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
	var geminiTools []Tool

	for _, tool := range tools {
		if tool.Type == "function" {
			geminiTool := Tool{}

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
					geminiTool.FunctionDeclarations = []*FunctionDeclaration{funcDecl}
				}
			}

			if len(geminiTool.FunctionDeclarations) > 0 {
				geminiTools = append(geminiTools, geminiTool)
			}
		}
	}

	return geminiTools
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

		// Handle tool calls from assistant messages
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

					part := &Part{
						FunctionCall: &FunctionCall{
							Name: *msg.ResponsesToolMessage.Name,
							Args: argsMap,
						},
					}
					if msg.ResponsesToolMessage.CallID != nil {
						part.FunctionCall.ID = *msg.ResponsesToolMessage.CallID
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
				// Convert function response to Gemini FunctionResponse
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
						},
					}
					// Keep ID = CallID
					part.FunctionResponse.ID = *msg.ResponsesToolMessage.CallID
					content.Parts = append(content.Parts, part)
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

				// Decode base64 data
				decodedData, err := base64.StdEncoding.DecodeString(data)
				if err != nil {
					return nil, fmt.Errorf("failed to decode base64 image data: %w", err)
				}

				return &Part{
					InlineData: &Blob{
						MIMEType: mimeType,
						Data:     decodedData,
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
			// Decode base64 audio data
			decodedData, err := base64.StdEncoding.DecodeString(block.Audio.Data)
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
					Data: decodedData,
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

				if fileBlock.Filename != nil {
					part.FileData.DisplayName = *fileBlock.Filename
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
							Data:     dataBytes,
						},
					}

					return part, nil
				}
			}
		}
	}

	return nil, nil
}
