package streaming

import (
	"sync"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

type StreamType string

const (
	StreamTypeText          StreamType = "text.completion"
	StreamTypeChat          StreamType = "chat.completion"
	StreamTypeAudio         StreamType = "audio.speech"
	StreamTypeTranscription StreamType = "audio.transcription"
	StreamTypeResponses     StreamType = "responses"
)

type StreamResponseType string

const (
	StreamResponseTypeDelta StreamResponseType = "delta"
	StreamResponseTypeFinal StreamResponseType = "final"
)

// AccumulatedData contains the accumulated data for a stream
type AccumulatedData struct {
	RequestID           string
	Model               string
	Status              string
	Stream              bool
	Latency             int64 // in milliseconds
	StartTimestamp      time.Time
	EndTimestamp        time.Time
	OutputMessage       *schemas.ChatMessage
	OutputMessages      []schemas.ResponsesMessage // For responses API
	ToolCalls           []schemas.ChatAssistantMessageToolCall
	ErrorDetails        *schemas.BifrostError
	TokenUsage          *schemas.BifrostLLMUsage
	CacheDebug          *schemas.BifrostCacheDebug
	Cost                *float64
	AudioOutput         *schemas.BifrostSpeechResponse
	TranscriptionOutput *schemas.BifrostTranscriptionResponse
	FinishReason        *string
	RawResponse         *string
}

// AudioStreamChunk represents a single streaming chunk
type AudioStreamChunk struct {
	Timestamp          time.Time                            // When chunk was received
	Delta              *schemas.BifrostSpeechStreamResponse // The actual delta content
	FinishReason       *string                              // If this is the final chunk
	TokenUsage         *schemas.SpeechUsage                 // Token usage if available
	SemanticCacheDebug *schemas.BifrostCacheDebug           // Semantic cache debug if available
	Cost               *float64                             // Cost in dollars from pricing plugin
	ErrorDetails       *schemas.BifrostError                // Error if any
	ChunkIndex         int                                  // Index of the chunk in the stream
	RawResponse        *string
}

// TranscriptionStreamChunk represents a single transcription streaming chunk
type TranscriptionStreamChunk struct {
	Timestamp          time.Time                                   // When chunk was received
	Delta              *schemas.BifrostTranscriptionStreamResponse // The actual delta content
	FinishReason       *string                                     // If this is the final chunk
	TokenUsage         *schemas.TranscriptionUsage                 // Token usage if available
	SemanticCacheDebug *schemas.BifrostCacheDebug                  // Semantic cache debug if available
	Cost               *float64                                    // Cost in dollars from pricing plugin
	ErrorDetails       *schemas.BifrostError                       // Error if any
	ChunkIndex         int                                         // Index of the chunk in the stream
	RawResponse        *string
}

// ChatStreamChunk represents a single streaming chunk
type ChatStreamChunk struct {
	Timestamp          time.Time                              // When chunk was received
	Delta              *schemas.ChatStreamResponseChoiceDelta // The actual delta content
	FinishReason       *string                                // If this is the final chunk
	TokenUsage         *schemas.BifrostLLMUsage               // Token usage if available
	SemanticCacheDebug *schemas.BifrostCacheDebug             // Semantic cache debug if available
	Cost               *float64                               // Cost in dollars from pricing plugin
	ErrorDetails       *schemas.BifrostError                  // Error if any
	ChunkIndex         int                                    // Index of the chunk in the stream
	RawResponse        *string                                // Raw response if available
}

// ResponsesStreamChunk represents a single responses streaming chunk
type ResponsesStreamChunk struct {
	Timestamp          time.Time                               // When chunk was received
	StreamResponse     *schemas.BifrostResponsesStreamResponse // The actual stream response
	FinishReason       *string                                 // If this is the final chunk
	TokenUsage         *schemas.BifrostLLMUsage                // Token usage if available
	SemanticCacheDebug *schemas.BifrostCacheDebug              // Semantic cache debug if available
	Cost               *float64                                // Cost in dollars from pricing plugin
	ErrorDetails       *schemas.BifrostError                   // Error if any
	ChunkIndex         int                                     // Index of the chunk in the stream
	RawResponse        *string
}

// StreamAccumulator manages accumulation of streaming chunks
type StreamAccumulator struct {
	RequestID                 string
	StartTimestamp            time.Time
	ChatStreamChunks          []*ChatStreamChunk
	ResponsesStreamChunks     []*ResponsesStreamChunk
	TranscriptionStreamChunks []*TranscriptionStreamChunk
	AudioStreamChunks         []*AudioStreamChunk
	IsComplete                bool
	FinalTimestamp            time.Time
	mu                        sync.Mutex
	Timestamp                 time.Time
}

// ProcessedStreamResponse represents a processed streaming response
type ProcessedStreamResponse struct {
	Type       StreamResponseType
	RequestID  string
	StreamType StreamType
	Provider   schemas.ModelProvider
	Model      string
	Data       *AccumulatedData
	RawRequest *interface{}
}

// ToBifrostResponse converts a ProcessedStreamResponse to a BifrostResponse
func (p *ProcessedStreamResponse) ToBifrostResponse() *schemas.BifrostResponse {
	resp := &schemas.BifrostResponse{}

	switch p.StreamType {
	case StreamTypeText:
		text := ""
		if p.Data.OutputMessage != nil && p.Data.OutputMessage.Content != nil && p.Data.OutputMessage.Content.ContentStr != nil {
			text = *p.Data.OutputMessage.Content.ContentStr
		}
		textResp := &schemas.BifrostTextCompletionResponse{
			ID:     p.RequestID,
			Object: "text_completion",
			Model:  p.Model,
			Choices: []schemas.BifrostResponseChoice{
				{
					Index:        0,
					FinishReason: p.Data.FinishReason,
					TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{
						Text: &text,
					},
				},
			},
			Usage: p.Data.TokenUsage,
		}

		resp.TextCompletionResponse = textResp
		resp.TextCompletionResponse.ExtraFields = schemas.BifrostResponseExtraFields{
			RequestType:    schemas.TextCompletionRequest,
			Provider:       p.Provider,
			ModelRequested: p.Model,
			Latency:        p.Data.Latency,
		}
		if p.RawRequest != nil {
			resp.TextCompletionResponse.ExtraFields.RawRequest = p.RawRequest
		}
	case StreamTypeChat:
		chatResp := &schemas.BifrostChatResponse{
			ID:      p.RequestID,
			Object:  "chat.completion",
			Model:   p.Model,
			Created: int(p.Data.StartTimestamp.Unix()),
			Choices: []schemas.BifrostResponseChoice{
				{
					Index:        0,
					FinishReason: p.Data.FinishReason,
				},
			},
			Usage: p.Data.TokenUsage,
		}

		// Get reference to the choice in the slice so we can modify it
		choice := &chatResp.Choices[0]

		if p.Data.OutputMessage.Content.ContentStr != nil {
			choice.ChatNonStreamResponseChoice = &schemas.ChatNonStreamResponseChoice{
				Message: &schemas.ChatMessage{
					Role: schemas.ChatMessageRoleAssistant,
					Content: &schemas.ChatMessageContent{
						ContentStr: p.Data.OutputMessage.Content.ContentStr,
					},
				},
			}
		}
		if p.Data.OutputMessage.ChatAssistantMessage != nil {
			if choice.ChatNonStreamResponseChoice == nil {
				choice.ChatNonStreamResponseChoice = &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role:                 schemas.ChatMessageRoleAssistant,
						ChatAssistantMessage: p.Data.OutputMessage.ChatAssistantMessage,
					},
				}
			} else {
				// If we already have a message, we need to add the ChatAssistantMessage to it
				choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage = p.Data.OutputMessage.ChatAssistantMessage
			}
		}

		resp.ChatResponse = chatResp
		resp.ChatResponse.ExtraFields = schemas.BifrostResponseExtraFields{
			RequestType:    schemas.ChatCompletionRequest,
			Provider:       p.Provider,
			ModelRequested: p.Model,
			Latency:        p.Data.Latency,
		}
		if p.RawRequest != nil {
			resp.ChatResponse.ExtraFields.RawRequest = p.RawRequest
		}
	case StreamTypeResponses:
		responsesResp := &schemas.BifrostResponsesResponse{}

		if p.Data.OutputMessages != nil {
			responsesResp.Output = p.Data.OutputMessages
		}
		if p.Data.TokenUsage != nil {
			responsesResp.Usage = p.Data.TokenUsage.ToResponsesResponseUsage()
		}
		responsesResp.ExtraFields = schemas.BifrostResponseExtraFields{
			RequestType:    schemas.ResponsesRequest,
			Provider:       p.Provider,
			ModelRequested: p.Model,
			Latency:        p.Data.Latency,
		}
		if p.RawRequest != nil {
			responsesResp.ExtraFields.RawRequest = p.RawRequest
		}
		resp.ResponsesResponse = responsesResp
	case StreamTypeAudio:
		speechResp := p.Data.AudioOutput
		if speechResp == nil {
			speechResp = &schemas.BifrostSpeechResponse{}
		}
		resp.SpeechResponse = speechResp
		resp.SpeechResponse.ExtraFields = schemas.BifrostResponseExtraFields{
			RequestType:    schemas.SpeechRequest,
			Provider:       p.Provider,
			ModelRequested: p.Model,
			Latency:        p.Data.Latency,
		}
		if p.RawRequest != nil {
			resp.SpeechResponse.ExtraFields.RawRequest = p.RawRequest
		}
	case StreamTypeTranscription:
		transcriptionResp := p.Data.TranscriptionOutput
		if transcriptionResp == nil {
			transcriptionResp = &schemas.BifrostTranscriptionResponse{}
		}
		resp.TranscriptionResponse = transcriptionResp
		resp.TranscriptionResponse.ExtraFields = schemas.BifrostResponseExtraFields{
			RequestType:    schemas.TranscriptionRequest,
			Provider:       p.Provider,
			ModelRequested: p.Model,
			Latency:        p.Data.Latency,
		}
		if p.RawRequest != nil {
			resp.TranscriptionResponse.ExtraFields.RawRequest = p.RawRequest
		}
	}
	return resp
}
