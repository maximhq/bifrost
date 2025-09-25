package streaming

import (
	"sync"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

type StreamType string

const (
	StreamTypeChat  StreamType = "chat"
	StreamTypeAudio StreamType = "audio"
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
	Latency             float64
	StartTimestamp      time.Time
	EndTimestamp        time.Time
	OutputMessage       *schemas.BifrostMessage
	ToolCalls           []schemas.ToolCall
	ErrorDetails        *schemas.BifrostError
	TokenUsage          *schemas.LLMUsage
	CacheDebug          *schemas.BifrostCacheDebug
	Cost                *float64
	Object              string
	TranscriptionOutput *schemas.BifrostTranscribe
	FinishReason        *string
}

// StreamChunk represents a single streaming chunk
type StreamChunk struct {
	Timestamp          time.Time                   // When chunk was received
	Delta              *schemas.BifrostStreamDelta // The actual delta content
	FinishReason       *string                     // If this is the final chunk
	TokenUsage         *schemas.LLMUsage           // Token usage if available
	SemanticCacheDebug *schemas.BifrostCacheDebug  // Semantic cache debug if available
	Cost               *float64                    // Cost in dollars from pricing plugin
	ErrorDetails       *schemas.BifrostError       // Error if any
}

// StreamAccumulator manages accumulation of streaming chunks
type StreamAccumulator struct {
	RequestID      string
	StartTimestamp time.Time
	Chunks         []*StreamChunk
	IsComplete     bool
	FinalTimestamp time.Time
	Object         string // Store object type once for the entire stream
	mu             sync.Mutex
	Timestamp      time.Time
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
