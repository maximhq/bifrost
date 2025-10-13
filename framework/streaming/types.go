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
	StreamTypeResponses     StreamType = "response"
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
	ToolCalls           []schemas.ChatAssistantMessageToolCall
	ErrorDetails        *schemas.BifrostError
	TokenUsage          *schemas.LLMUsage
	CacheDebug          *schemas.BifrostCacheDebug
	Cost                *float64
	Object              string
	AudioOutput         *schemas.BifrostSpeech
	TranscriptionOutput *schemas.BifrostTranscribe
	ResponsesOutput     *schemas.ResponsesResponse
	FinishReason        *string
}

// AudioStreamChunk represents a single streaming chunk
type AudioStreamChunk struct {
	Timestamp          time.Time                  // When chunk was received
	Delta              *schemas.BifrostSpeech     // The actual delta content
	FinishReason       *string                    // If this is the final chunk
	TokenUsage         *schemas.AudioLLMUsage     // Token usage if available
	SemanticCacheDebug *schemas.BifrostCacheDebug // Semantic cache debug if available
	Cost               *float64                   // Cost in dollars from pricing plugin
	ErrorDetails       *schemas.BifrostError      // Error if any
}

// TranscriptionStreamChunk represents a single transcription streaming chunk
type TranscriptionStreamChunk struct {
	Timestamp          time.Time                                // When chunk was received
	Delta              *schemas.BifrostTranscribeStreamResponse // The actual delta content
	FinishReason       *string                                  // If this is the final chunk
	TokenUsage         *schemas.LLMUsage                        // Token usage if available
	TranscriptionUsage *schemas.TranscriptionUsage              // Token usage if available
	SemanticCacheDebug *schemas.BifrostCacheDebug               // Semantic cache debug if available
	Cost               *float64                                 // Cost in dollars from pricing plugin
	ErrorDetails       *schemas.BifrostError                    // Error if any
}

// ChatStreamChunk represents a single streaming chunk
type ChatStreamChunk struct {
	Timestamp          time.Time                   // When chunk was received
	Delta              *schemas.BifrostStreamDelta // The actual delta content
	FinishReason       *string                     // If this is the final chunk
	TokenUsage         *schemas.LLMUsage           // Token usage if available
	SemanticCacheDebug *schemas.BifrostCacheDebug  // Semantic cache debug if available
	Cost               *float64                    // Cost in dollars from pricing plugin
	ErrorDetails       *schemas.BifrostError       // Error if any
}

// ResponsesStreamChunk represents a single responses streaming chunk
type ResponsesStreamChunk struct {
	Timestamp          time.Time                        // When chunk was received
	Event              *schemas.ResponsesStreamResponse // The event payload from the provider
	TokenUsage         *schemas.LLMUsage                // Token usage if available
	SemanticCacheDebug *schemas.BifrostCacheDebug       // Semantic cache debug if available
	Cost               *float64                         // Cost in dollars from pricing plugin
	ErrorDetails       *schemas.BifrostError            // Error if any
}

// StreamAccumulator manages accumulation of streaming chunks
type StreamAccumulator struct {
	RequestID                 string
	StartTimestamp            time.Time
	ChatStreamChunks          []*ChatStreamChunk
	TranscriptionStreamChunks []*TranscriptionStreamChunk
	AudioStreamChunks         []*AudioStreamChunk
	ResponsesStreamChunks     []*ResponsesStreamChunk
	IsComplete                bool
	FinalTimestamp            time.Time
	Object                    string // Store object type once for the entire stream
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
}

// ToBifrostResponse converts a ProcessedStreamResponse to a BifrostResponse
func (p *ProcessedStreamResponse) ToBifrostResponse() *schemas.BifrostResponse {
	resp := &schemas.BifrostResponse{}
	resp.ID = p.RequestID
	resp.Object = string(p.StreamType)
	resp.Data = []schemas.BifrostEmbedding{}
	resp.Speech = p.Data.AudioOutput
	resp.Transcribe = p.Data.TranscriptionOutput
	resp.ResponsesResponse = p.Data.ResponsesOutput
	if p.Data.OutputMessage != nil {
		choice := schemas.BifrostChatResponseChoice{
			Index:        0,
			FinishReason: p.Data.FinishReason,
		}
		if p.Data.OutputMessage.Content.ContentStr != nil {
			choice.BifrostNonStreamResponseChoice = &schemas.BifrostNonStreamResponseChoice{
				Message: &schemas.ChatMessage{
					Role: schemas.ChatMessageRoleAssistant,
					Content: &schemas.ChatMessageContent{
						ContentStr: p.Data.OutputMessage.Content.ContentStr,
					},
				},
			}
		}
		if p.Data.OutputMessage.ChatAssistantMessage != nil {
			if choice.BifrostNonStreamResponseChoice == nil {
				choice.BifrostNonStreamResponseChoice = &schemas.BifrostNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role:                 schemas.ChatMessageRoleAssistant,
						ChatAssistantMessage: p.Data.OutputMessage.ChatAssistantMessage,
					},
				}
			}
		}
		resp.Choices = []schemas.BifrostChatResponseChoice{
			choice,
		}
	}
	resp.Model = p.Model
	resp.Created = int(p.Data.StartTimestamp.Unix())
	if p.Data.TokenUsage != nil {
		resp.Usage = p.Data.TokenUsage
	}
	if p.Data.CacheDebug != nil {
		resp.ExtraFields.CacheDebug = p.Data.CacheDebug
	}
	resp.ExtraFields = schemas.BifrostResponseExtraFields{
		CacheDebug: p.Data.CacheDebug,
		Provider:   p.Provider,
	}
	if p.StreamType == StreamTypeResponses {
		resp.ExtraFields.RequestType = schemas.ResponsesRequest
	}
	if p.Data.Latency != 0 {
		resp.ExtraFields.Latency = p.Data.Latency
	}
	return resp
}
