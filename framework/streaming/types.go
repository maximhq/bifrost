package streaming

import (
	"sync"
	"sync/atomic"
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

// AccumulatedData contains the accumulated data for a stream
type AccumulatedData struct {
	RequestID           string
	Model               string
	Status              string
	Stream              bool
	Latency             int64 // in milliseconds
	TimeToFirstToken    int64 // Time to first token in milliseconds (streaming only)
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
	FirstChunkTimestamp       time.Time // Timestamp when the first chunk was received (for TTFT calculation)
	ChatStreamChunks          []*ChatStreamChunk
	ResponsesStreamChunks     []*ResponsesStreamChunk
	TranscriptionStreamChunks []*TranscriptionStreamChunk
	AudioStreamChunks         []*AudioStreamChunk

	// De-dup maps to prevent chunk loss on out-of-order arrival
	ChatChunksSeen          map[int]struct{}
	ResponsesChunksSeen     map[int]struct{}
	TranscriptionChunksSeen map[int]struct{}
	AudioChunksSeen         map[int]struct{}

	// Track highest ChunkIndex for metadata extraction (TokenUsage, Cost, FinishReason)
	MaxChatChunkIndex          int
	MaxResponsesChunkIndex     int
	MaxTranscriptionChunkIndex int
	MaxAudioChunkIndex         int

	IsComplete     bool
	FinalTimestamp time.Time
	mu             sync.Mutex
	Timestamp      time.Time
	refCount       atomic.Int64
}

// getLastChatChunk returns the chunk with the highest ChunkIndex (contains metadata like TokenUsage, Cost)
func (sa *StreamAccumulator) getLastChatChunk() *ChatStreamChunk {
	if sa.MaxChatChunkIndex < 0 {
		return nil
	}
	for _, chunk := range sa.ChatStreamChunks {
		if chunk.ChunkIndex == sa.MaxChatChunkIndex {
			return chunk
		}
	}
	return nil
}

// getLastResponsesChunk returns the chunk with the highest ChunkIndex (contains metadata like TokenUsage, Cost)
func (sa *StreamAccumulator) getLastResponsesChunk() *ResponsesStreamChunk {
	if sa.MaxResponsesChunkIndex < 0 {
		return nil
	}
	for _, chunk := range sa.ResponsesStreamChunks {
		if chunk.ChunkIndex == sa.MaxResponsesChunkIndex {
			return chunk
		}
	}
	return nil
}

// getLastTranscriptionChunk returns the chunk with the highest ChunkIndex (contains metadata like TokenUsage, Cost)
func (sa *StreamAccumulator) getLastTranscriptionChunk() *TranscriptionStreamChunk {
	if sa.MaxTranscriptionChunkIndex < 0 {
		return nil
	}
	for _, chunk := range sa.TranscriptionStreamChunks {
		if chunk.ChunkIndex == sa.MaxTranscriptionChunkIndex {
			return chunk
		}
	}
	return nil
}

// getLastAudioChunk returns the chunk with the highest ChunkIndex (contains metadata like TokenUsage, Cost)
func (sa *StreamAccumulator) getLastAudioChunk() *AudioStreamChunk {
	if sa.MaxAudioChunkIndex < 0 {
		return nil
	}
	for _, chunk := range sa.AudioStreamChunks {
		if chunk.ChunkIndex == sa.MaxAudioChunkIndex {
			return chunk
		}
	}
	return nil
}

// ProcessedStreamResponse represents a processed streaming response
type ProcessedStreamResponse struct {
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
		if p.Data.RawResponse != nil {
			resp.TextCompletionResponse.ExtraFields.RawResponse = *p.Data.RawResponse
		}
		if p.Data.CacheDebug != nil {
			resp.TextCompletionResponse.ExtraFields.CacheDebug = p.Data.CacheDebug
		}
	case StreamTypeChat:
		var message *schemas.ChatMessage
		if p.Data.OutputMessage != nil {
			message = &schemas.ChatMessage{
				Role:                 p.Data.OutputMessage.Role,
				Content:              p.Data.OutputMessage.Content,
				ChatAssistantMessage: p.Data.OutputMessage.ChatAssistantMessage,
				ChatToolMessage:      p.Data.OutputMessage.ChatToolMessage,
				Name:                 p.Data.OutputMessage.Name,
			}
		}
		chatResp := &schemas.BifrostChatResponse{
			ID:      p.RequestID,
			Object:  "chat.completion",
			Model:   p.Model,
			Created: int(p.Data.StartTimestamp.Unix()),
			Choices: []schemas.BifrostResponseChoice{
				{
					Index:        0,
					FinishReason: p.Data.FinishReason,
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: message,
					},
				},
			},
			Usage: p.Data.TokenUsage,
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
		if p.Data.RawResponse != nil {
			resp.ChatResponse.ExtraFields.RawResponse = *p.Data.RawResponse
		}
		if p.Data.CacheDebug != nil {
			resp.ChatResponse.ExtraFields.CacheDebug = p.Data.CacheDebug
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
		if p.Data.RawResponse != nil {
			responsesResp.ExtraFields.RawResponse = *p.Data.RawResponse
		}
		if p.Data.CacheDebug != nil {
			responsesResp.ExtraFields.CacheDebug = p.Data.CacheDebug
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
		if p.Data.RawResponse != nil {
			resp.SpeechResponse.ExtraFields.RawResponse = *p.Data.RawResponse
		}
		if p.Data.CacheDebug != nil {
			resp.SpeechResponse.ExtraFields.CacheDebug = p.Data.CacheDebug
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
		if p.Data.RawResponse != nil {
			resp.TranscriptionResponse.ExtraFields.RawResponse = *p.Data.RawResponse
		}
		if p.Data.CacheDebug != nil {
			resp.TranscriptionResponse.ExtraFields.CacheDebug = p.Data.CacheDebug
		}
	}
	return resp
}
