package openai

import (
	"encoding/json"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

const MinMaxCompletionTokens = 16

const MinReasoningMaxTokens = 1 // Minimum max tokens for reasoning - used for estimation of effort level

const DefaultCompletionMaxTokens = 4096 // Only used for relative reasoning max token calculation - not passed in body by default

// REQUEST TYPES

// OpenAITextCompletionRequest represents an OpenAI text completion request
type OpenAITextCompletionRequest struct {
	Model  string                       `json:"model"`  // Required: Model to use
	Prompt *schemas.TextCompletionInput `json:"prompt"` // Required: String or array of strings

	schemas.TextCompletionParameters
	Stream *bool `json:"stream,omitempty"`

	// Bifrost specific field (only parsed when converting from Provider -> Bifrost request)
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *OpenAITextCompletionRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

// OpenAIEmbeddingRequest represents an OpenAI embedding request
type OpenAIEmbeddingRequest struct {
	Model string                  `json:"model"`
	Input *schemas.EmbeddingInput `json:"input"` // Can be string or []string

	schemas.EmbeddingParameters

	// Bifrost specific field (only parsed when converting from Provider -> Bifrost request)
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// OpenAIChatRequest represents an OpenAI chat completion request
type OpenAIChatRequest struct {
	Model    string          `json:"model"`
	Messages []OpenAIMessage `json:"messages"`

	schemas.ChatParameters
	Stream *bool `json:"stream,omitempty"`

	//NOTE: MaxCompletionTokens is a new replacement for max_tokens but some providers still use max_tokens.
	// This Field is populated only for such providers and is NOT to be used externally.
	MaxTokens *int `json:"max_tokens,omitempty"`

	// Bifrost specific field (only parsed when converting from Provider -> Bifrost request)
	Fallbacks []string `json:"fallbacks,omitempty"`
}

type OpenAIMessage struct {
	Name    *string                     `json:"name,omitempty"` // for chat completions
	Role    schemas.ChatMessageRole     `json:"role,omitempty"`
	Content *schemas.ChatMessageContent `json:"content,omitempty"`

	// Embedded pointer structs - when non-nil, their exported fields are flattened into the top-level JSON object
	// IMPORTANT: Only one of the following can be non-nil at a time, otherwise the JSON marshalling will override the common fields
	*schemas.ChatToolMessage
	*OpenAIChatAssistantMessage
}

type OpenAIChatAssistantMessage struct {
	Refusal     *string                                  `json:"refusal,omitempty"`
	Reasoning   *string                                  `json:"reasoning,omitempty"`
	Annotations []schemas.ChatAssistantMessageAnnotation `json:"annotations,omitempty"`
	ToolCalls   []schemas.ChatAssistantMessageToolCall   `json:"tool_calls,omitempty"`
}

// MarshalJSON implements custom JSON marshalling for OpenAIChatRequest.
// It excludes the reasoning field and instead marshals reasoning_effort
// with the value of Reasoning.Effort if not nil.
// It also removes cache_control from messages, their content blocks, and tools.
func (r *OpenAIChatRequest) MarshalJSON() ([]byte, error) {
	if r == nil {
		return []byte("null"), nil
	}
	type Alias OpenAIChatRequest

	// First pass: check if we need to modify any messages
	needsCopy := false
	for _, msg := range r.Messages {
		if hasFieldsToStripInChatMessage(msg) {
			needsCopy = true
			break
		}
	}

	// Process messages if needed
	var processedMessages []OpenAIMessage
	if needsCopy {
		processedMessages = make([]OpenAIMessage, len(r.Messages))
		for i, msg := range r.Messages {
			if !hasFieldsToStripInChatMessage(msg) {
				// No modification needed, use original
				processedMessages[i] = msg
				continue
			}

			// Copy message
			processedMessages[i] = msg

			// Strip CacheControl and FileType from content blocks if needed
			if msg.Content != nil && msg.Content.ContentBlocks != nil {
				contentCopy := *msg.Content
				contentCopy.ContentBlocks = make([]schemas.ChatContentBlock, len(msg.Content.ContentBlocks))
				for j, block := range msg.Content.ContentBlocks {
					needsBlockCopy := block.CacheControl != nil || block.Citations != nil || (block.File != nil && block.File.FileType != nil)
					if needsBlockCopy {
						blockCopy := block
						blockCopy.CacheControl = nil
						blockCopy.Citations = nil
						// Strip FileType and FileURL from file block
						if blockCopy.File != nil && (blockCopy.File.FileType != nil || blockCopy.File.FileURL != nil) {
							fileCopy := *blockCopy.File
							fileCopy.FileType = nil
							fileCopy.FileURL = nil
							blockCopy.File = &fileCopy
						}
						contentCopy.ContentBlocks[j] = blockCopy
					} else {
						contentCopy.ContentBlocks[j] = block
					}
				}
				processedMessages[i].Content = &contentCopy
			}
		}
	} else {
		processedMessages = r.Messages
	}

	// Process tools if needed
	var processedTools []schemas.ChatTool
	if len(r.Tools) > 0 {
		needsToolCopy := false
		for _, tool := range r.Tools {
			if tool.CacheControl != nil {
				needsToolCopy = true
				break
			}
		}

		if needsToolCopy {
			processedTools = make([]schemas.ChatTool, len(r.Tools))
			for i, tool := range r.Tools {
				if tool.CacheControl != nil {
					toolCopy := tool
					toolCopy.CacheControl = nil
					processedTools[i] = toolCopy
				} else {
					processedTools[i] = tool
				}
			}
		} else {
			processedTools = r.Tools
		}
	} else {
		processedTools = r.Tools
	}

	// Aux struct:
	// - Alias embeds all original fields
	// - Messages shadows the embedded Messages field to use processed messages
	// - Tools shadows the embedded Tools field to use processed tools
	// - Reasoning shadows the embedded ChatParameters.Reasoning
	//   so that "reasoning" is not emitted
	// - ReasoningEffort is emitted as "reasoning_effort"
	aux := struct {
		*Alias
		// Shadow the embedded "messages" field to use processed messages
		Messages []OpenAIMessage `json:"messages"`
		// Shadow the embedded "tools" field to use processed tools
		Tools []schemas.ChatTool `json:"tools,omitempty"`
		// Shadow the embedded "reasoning" field and omit it
		Reasoning       *schemas.ChatReasoning `json:"reasoning,omitempty"`
		ReasoningEffort *string                `json:"reasoning_effort,omitempty"`
	}{
		Alias:    (*Alias)(r),
		Messages: processedMessages,
		Tools:    processedTools,
	}

	// DO NOT set aux.Reasoning → it stays nil and is omitted via omitempty, and also due to double reference to the same json field.

	if r.Reasoning != nil && r.Reasoning.Effort != nil {
		aux.ReasoningEffort = r.Reasoning.Effort
	}

	return sonic.Marshal(aux)
}

// UnmarshalJSON implements custom JSON unmarshalling for OpenAIChatRequest.
// This is needed because ChatParameters has a custom UnmarshalJSON method,
// which would otherwise "hijack" the unmarshalling and ignore the other fields
// (Model, Messages, Stream, MaxTokens, Fallbacks).
func (r *OpenAIChatRequest) UnmarshalJSON(data []byte) error {
	// Unmarshal the request-specific fields directly
	type baseFields struct {
		Model     string          `json:"model"`
		Messages  []OpenAIMessage `json:"messages"`
		Stream    *bool           `json:"stream,omitempty"`
		MaxTokens *int            `json:"max_tokens,omitempty"`
		Fallbacks []string        `json:"fallbacks,omitempty"`
	}
	var base baseFields
	if err := sonic.Unmarshal(data, &base); err != nil {
		return err
	}
	r.Model = base.Model
	r.Messages = base.Messages
	r.Stream = base.Stream
	r.MaxTokens = base.MaxTokens
	r.Fallbacks = base.Fallbacks

	// Unmarshal ChatParameters (which has its own custom unmarshaller)
	var params schemas.ChatParameters
	if err := sonic.Unmarshal(data, &params); err != nil {
		return err
	}
	r.ChatParameters = params

	return nil
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *OpenAIChatRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

// ResponsesRequestInput is a union of string and array of responses messages
type OpenAIResponsesRequestInput struct {
	OpenAIResponsesRequestInputStr   *string
	OpenAIResponsesRequestInputArray []schemas.ResponsesMessage
}

// UnmarshalJSON unmarshals the responses request input
func (r *OpenAIResponsesRequestInput) UnmarshalJSON(data []byte) error {
	var str string
	if err := sonic.Unmarshal(data, &str); err == nil {
		r.OpenAIResponsesRequestInputStr = &str
		r.OpenAIResponsesRequestInputArray = nil
		return nil
	}
	var array []schemas.ResponsesMessage
	if err := sonic.Unmarshal(data, &array); err == nil {
		r.OpenAIResponsesRequestInputStr = nil
		r.OpenAIResponsesRequestInputArray = array
		return nil
	}
	return fmt.Errorf("openai responses request input is neither a string nor an array of responses messages")
}

func (r *OpenAIResponsesRequestInput) MarshalJSON() ([]byte, error) {
	if r.OpenAIResponsesRequestInputStr != nil {
		return sonic.Marshal(*r.OpenAIResponsesRequestInputStr)
	}
	if r.OpenAIResponsesRequestInputArray != nil {
		// First pass: check if we need to modify anything
		needsCopy := false
		for _, msg := range r.OpenAIResponsesRequestInputArray {
			if hasFieldsToStripInResponsesMessage(msg) {
				needsCopy = true
				break
			}
		}

		// If no CacheControl found anywhere, marshal as-is
		if !needsCopy {
			return sonic.Marshal(r.OpenAIResponsesRequestInputArray)
		}

		// Only copy messages that have CacheControl
		messagesCopy := make([]schemas.ResponsesMessage, len(r.OpenAIResponsesRequestInputArray))
		for i, msg := range r.OpenAIResponsesRequestInputArray {
			if !hasFieldsToStripInResponsesMessage(msg) {
				// No modification needed, use original
				messagesCopy[i] = msg
				continue
			}

			// Copy only this message
			messagesCopy[i] = msg

			// Strip CacheControl, FileType, and filter unsupported citation types from content blocks if needed
			if msg.Content != nil && msg.Content.ContentBlocks != nil {
				contentCopy := *msg.Content
				contentCopy.ContentBlocks = make([]schemas.ResponsesMessageContentBlock, 0, len(msg.Content.ContentBlocks))
				hasContentModification := false
				for _, block := range msg.Content.ContentBlocks {
					// Skip rendered_content blocks entirely - OpenAI doesn't support them
					if block.Type == schemas.ResponsesOutputMessageContentTypeRenderedContent {
						hasContentModification = true
						continue
					}

					needsBlockCopy := block.CacheControl != nil || block.Citations != nil || (block.ResponsesInputMessageContentBlockFile != nil && block.ResponsesInputMessageContentBlockFile.FileType != nil) || (block.ResponsesOutputMessageContentText != nil && len(block.ResponsesOutputMessageContentText.Annotations) > 0)
					if needsBlockCopy {
						hasContentModification = true
						blockCopy := block
						blockCopy.CacheControl = nil
						blockCopy.Citations = nil

						// Filter out unsupported citation types from annotations
						if blockCopy.ResponsesOutputMessageContentText != nil && len(blockCopy.ResponsesOutputMessageContentText.Annotations) > 0 {
							textCopy := *blockCopy.ResponsesOutputMessageContentText
							filteredAnnotations := filterSupportedAnnotations(textCopy.Annotations)
							if len(filteredAnnotations) > 0 {
								textCopy.Annotations = filteredAnnotations
								blockCopy.ResponsesOutputMessageContentText = &textCopy
							} else {
								// If no supported annotations remain, remove the annotations array
								textCopy.Annotations = nil
								blockCopy.ResponsesOutputMessageContentText = &textCopy
							}
						}

						// Strip FileType from file block
						if blockCopy.ResponsesInputMessageContentBlockFile != nil && blockCopy.ResponsesInputMessageContentBlockFile.FileType != nil {
							fileCopy := *blockCopy.ResponsesInputMessageContentBlockFile
							fileCopy.FileType = nil
							blockCopy.ResponsesInputMessageContentBlockFile = &fileCopy
						}
						contentCopy.ContentBlocks = append(contentCopy.ContentBlocks, blockCopy)
					} else {
						contentCopy.ContentBlocks = append(contentCopy.ContentBlocks, block)
					}
				}
				if hasContentModification {
					messagesCopy[i].Content = &contentCopy
				}
			}

			// Strip unsupported fields from tool message
			if msg.ResponsesToolMessage != nil {
				toolMsgCopy := *msg.ResponsesToolMessage
				toolMsgModified := false

				// Strip unsupported fields from web search sources
				if msg.ResponsesToolMessage.Action != nil && msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction != nil {
					sources := msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction.Sources
					if len(sources) > 0 {
						needsSourceCopy := false
						for _, source := range sources {
							if source.Title != nil || source.EncryptedContent != nil || source.PageAge != nil {
								needsSourceCopy = true
								break
							}
						}

						if needsSourceCopy {
							actionCopy := *msg.ResponsesToolMessage.Action
							webSearchActionCopy := *msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction
							strippedSources := make([]schemas.ResponsesWebSearchToolCallActionSearchSource, len(sources))
							for j, source := range sources {
								// Only keep Type and URL for OpenAI
								strippedSources[j] = schemas.ResponsesWebSearchToolCallActionSearchSource{
									Type: source.Type,
									URL:  source.URL,
									// Title, EncryptedContent, and PageAge are omitted
								}
							}
							webSearchActionCopy.Sources = strippedSources
							actionCopy.ResponsesWebSearchToolCallAction = &webSearchActionCopy
							toolMsgCopy.Action = &actionCopy
							toolMsgModified = true
						}
					}
				}

				// Strip CacheControl and FileType from tool message output blocks if needed
				if msg.ResponsesToolMessage.Output != nil && msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks != nil {
					hasToolModification := false
					for _, block := range msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks {
						if block.CacheControl != nil || block.Citations != nil || (block.ResponsesInputMessageContentBlockFile != nil && block.ResponsesInputMessageContentBlockFile.FileType != nil) {
							hasToolModification = true
							break
						}
					}

					if hasToolModification {
						outputCopy := *msg.ResponsesToolMessage.Output
						outputCopy.ResponsesFunctionToolCallOutputBlocks = make([]schemas.ResponsesMessageContentBlock, len(msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks))
						for j, block := range msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks {
							needsBlockCopy := block.CacheControl != nil || (block.ResponsesInputMessageContentBlockFile != nil && block.ResponsesInputMessageContentBlockFile.FileType != nil)
							if needsBlockCopy {
								blockCopy := block
								blockCopy.CacheControl = nil
								blockCopy.Citations = nil
								// Strip FileType from file block
								if blockCopy.ResponsesInputMessageContentBlockFile != nil && blockCopy.ResponsesInputMessageContentBlockFile.FileType != nil {
									fileCopy := *blockCopy.ResponsesInputMessageContentBlockFile
									fileCopy.FileType = nil
									blockCopy.ResponsesInputMessageContentBlockFile = &fileCopy
								}
								outputCopy.ResponsesFunctionToolCallOutputBlocks[j] = blockCopy
							} else {
								outputCopy.ResponsesFunctionToolCallOutputBlocks[j] = block
							}
						}
						toolMsgCopy.Output = &outputCopy
						toolMsgModified = true
					}
				}

				if toolMsgModified {
					messagesCopy[i].ResponsesToolMessage = &toolMsgCopy
				}
			}
		}
		return sonic.Marshal(messagesCopy)
	}
	return sonic.Marshal(nil)
}

// Helper function to check if a chat message has any CacheControl fields or FileType in file blocks
func hasFieldsToStripInChatMessage(msg OpenAIMessage) bool {
	if msg.Content != nil && msg.Content.ContentBlocks != nil {
		for _, block := range msg.Content.ContentBlocks {
			if block.CacheControl != nil {
				return true
			}
			if block.Citations != nil {
				return true
			}
			if block.File != nil && (block.File.FileType != nil || block.File.FileURL != nil) {
				return true
			}
		}
	}
	return false
}

// Helper function to check if a responses message has any CacheControl fields or FileType in file blocks
func hasFieldsToStripInResponsesMessage(msg schemas.ResponsesMessage) bool {
	if msg.Content != nil && msg.Content.ContentBlocks != nil {
		for _, block := range msg.Content.ContentBlocks {
			if block.CacheControl != nil {
				return true
			}
			if block.Citations != nil {
				return true
			}
			if block.ResponsesInputMessageContentBlockFile != nil && block.ResponsesInputMessageContentBlockFile.FileType != nil {
				return true
			}
			if block.ResponsesOutputMessageContentText != nil && len(block.ResponsesOutputMessageContentText.Annotations) > 0 {
				return true
			}
			// OpenAI doesn't support rendered_content blocks
			if block.Type == schemas.ResponsesOutputMessageContentTypeRenderedContent {
				return true
			}
		}
	}
	if msg.ResponsesToolMessage != nil {
		// Check if we need to strip fields from web search sources
		if msg.ResponsesToolMessage.Action != nil && msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction != nil {
			for _, source := range msg.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction.Sources {
				if source.Title != nil || source.EncryptedContent != nil || source.PageAge != nil {
					return true
				}
			}
		}
		// Check output blocks
		if msg.ResponsesToolMessage.Output != nil && msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks != nil {
			for _, block := range msg.ResponsesToolMessage.Output.ResponsesFunctionToolCallOutputBlocks {
				if block.CacheControl != nil {
					return true
				}
				if block.ResponsesInputMessageContentBlockFile != nil && block.ResponsesInputMessageContentBlockFile.FileType != nil {
					return true
				}
			}
		}
	}
	return false
}

// filterSupportedAnnotations filters out unsupported (non-OpenAI native) citation types
// OpenAI supports: file_citation, url_citation, container_file_citation, file_path
func filterSupportedAnnotations(annotations []schemas.ResponsesOutputMessageContentTextAnnotation) []schemas.ResponsesOutputMessageContentTextAnnotation {
	if len(annotations) == 0 {
		return annotations
	}

	supportedAnnotations := make([]schemas.ResponsesOutputMessageContentTextAnnotation, 0, len(annotations))
	for _, annotation := range annotations {
		switch annotation.Type {
		case "url_citation":
			supportedAnnotations = append(supportedAnnotations, schemas.ResponsesOutputMessageContentTextAnnotation{
				Type:       "url_citation",
				URL:        annotation.URL,
				Title:      annotation.Title,
				StartIndex: annotation.StartIndex,
				EndIndex:   annotation.EndIndex,
			})
		case "file_citation", "container_file_citation", "file_path", "text_annotation":
			// OpenAI native types - keep them
			supportedAnnotations = append(supportedAnnotations, annotation)
		default:
			continue
		}
	}

	return supportedAnnotations
}

type OpenAIResponsesRequest struct {
	Model string                      `json:"model"`
	Input OpenAIResponsesRequestInput `json:"input"`

	schemas.ResponsesParameters
	Stream *bool `json:"stream,omitempty"`

	// Bifrost specific field (only parsed when converting from Provider -> Bifrost request)
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// MarshalJSON implements custom JSON marshalling for OpenAIResponsesRequest.
// It sets parameters.reasoning.max_tokens to nil before marshaling.
func (r *OpenAIResponsesRequest) MarshalJSON() ([]byte, error) {
	type Alias OpenAIResponsesRequest

	// Manually marshal Input using its custom MarshalJSON method
	inputBytes, err := r.Input.MarshalJSON()
	if err != nil {
		return nil, err
	}

	// Process tools if needed
	var processedTools []schemas.ResponsesTool
	if len(r.Tools) > 0 {
		needsToolCopy := false
		for _, tool := range r.Tools {
			if tool.CacheControl != nil {
				needsToolCopy = true
				break
			}
		}

		if needsToolCopy {
			processedTools = make([]schemas.ResponsesTool, len(r.Tools))
			for i, tool := range r.Tools {
				if tool.CacheControl != nil {
					toolCopy := tool
					toolCopy.CacheControl = nil
					processedTools[i] = toolCopy
				} else {
					processedTools[i] = tool
				}
			}
		} else {
			processedTools = r.Tools
		}
	} else {
		processedTools = r.Tools
	}

	// Aux struct:
	// - Alias embeds all original fields
	// - Input shadows the embedded Input field and uses json.RawMessage to preserve custom marshaling
	// - Reasoning shadows the embedded ResponsesParameters.Reasoning
	//   so that we can modify max_tokens before marshaling
	aux := struct {
		*Alias
		// Shadow the embedded "input" field to use custom marshaling
		Input json.RawMessage `json:"input"`
		// Shadow the embedded "reasoning" field to modify it
		Reasoning *schemas.ResponsesParametersReasoning `json:"reasoning,omitempty"`
		// Shadow the embedded "tools" field to use processed tools
		Tools []schemas.ResponsesTool `json:"tools,omitempty"`
	}{
		Alias: (*Alias)(r),
		Input: json.RawMessage(inputBytes),
		Tools: processedTools,
	}

	// Copy reasoning but set MaxTokens to nil
	if r.Reasoning != nil {
		aux.Reasoning = &schemas.ResponsesParametersReasoning{
			Effort:          r.Reasoning.Effort,
			GenerateSummary: r.Reasoning.GenerateSummary,
			Summary:         r.Reasoning.Summary,
			MaxTokens:       nil, // Always set to nil
		}
	}

	return sonic.Marshal(aux)
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *OpenAIResponsesRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

// OpenAISpeechRequest represents an OpenAI speech synthesis request
type OpenAISpeechRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`

	schemas.SpeechParameters
	StreamFormat *string `json:"stream_format,omitempty"`

	// Bifrost specific field (only parsed when converting from Provider -> Bifrost request)
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// OpenAITranscriptionRequest represents an OpenAI transcription request
// Note: This is used for JSON body parsing, actual form parsing is handled in the router
type OpenAITranscriptionRequest struct {
	Model string `json:"model"`
	File  []byte `json:"file"` // Binary audio data

	schemas.TranscriptionParameters
	Stream *bool `json:"stream,omitempty"`

	// Bifrost specific field (only parsed when converting from Provider -> Bifrost request)
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// IsStreamingRequested implements the StreamingRequest interface for speech
func (r *OpenAISpeechRequest) IsStreamingRequested() bool {
	return r.StreamFormat != nil && *r.StreamFormat == "sse"
}

// IsStreamingRequested implements the StreamingRequest interface for transcription
func (r *OpenAITranscriptionRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

// OpenAIModel represents an OpenAI model
type OpenAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
	Created *int64 `json:"created,omitempty"`

	// GROQ specific fields
	Active        *bool `json:"active,omitempty"`
	ContextWindow *int  `json:"context_window,omitempty"`
}

// OpenAIListModelsResponse represents an OpenAI list models response
type OpenAIListModelsResponse struct {
	Object string        `json:"object"`
	Data   []OpenAIModel `json:"data"`
}

// OpenAIImageGenerationRequest is the struct for Image Generation requests by OpenAI.
type OpenAIImageGenerationRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`

	schemas.ImageGenerationParameters

	Stream    *bool    `json:"stream,omitempty"`
	Fallbacks []string `json:"fallbacks,omitempty"`
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *OpenAIImageGenerationRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

// OpenAIImageStreamResponse is the struct for Image Generation streaming responses by OpenAI.
type OpenAIImageStreamResponse struct {
	Type              schemas.ImageGenerationEventType `json:"type,omitempty"`
	SequenceNumber    *int                             `json:"sequence_number,omitempty"`
	B64JSON           *string                          `json:"b64_json,omitempty"`
	PartialImageIndex *int                             `json:"partial_image_index,omitempty"`
	CreatedAt         int64                            `json:"created_at,omitempty"`
	Size              string                           `json:"size,omitempty"`
	Quality           string                           `json:"quality,omitempty"`
	Background        string                           `json:"background,omitempty"`
	OutputFormat      string                           `json:"output_format,omitempty"`
	RawSSE            string                           `json:"-"` // For internal use
	Usage             *schemas.ImageUsage              `json:"usage,omitempty"`
	// Error fields for error events
	Error *struct {
		Code    *string `json:"code,omitempty"`
		Message string  `json:"message,omitempty"`
		Param   *string `json:"param,omitempty"`
		Type    *string `json:"type,omitempty"`
	} `json:"error,omitempty"`
}
