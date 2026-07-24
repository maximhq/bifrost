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
	StreamTypeImage         StreamType = "image.generation"
	StreamTypeTranscription StreamType = "audio.transcription"
	StreamTypeResponses     StreamType = "responses"
	StreamTypePassthrough   StreamType = "passthrough"
)

// AccumulatedData contains the accumulated data for a stream
type AccumulatedData struct {
	RequestID             string
	Model                 string
	Status                string
	Stream                bool
	Latency               int64 // in milliseconds
	TimeToFirstToken      int64 // Time to first token in milliseconds (streaming only)
	StartTimestamp        time.Time
	EndTimestamp          time.Time
	OutputMessage         *schemas.ChatMessage
	OutputMessages        []schemas.ResponsesMessage // For responses API
	ToolCalls             []schemas.ChatAssistantMessageToolCall
	ErrorDetails          *schemas.BifrostError
	TokenUsage            *schemas.BifrostLLMUsage
	CacheDebug            *schemas.BifrostCacheDebug
	GuardrailDebug        *schemas.BifrostGuardrailDebug
	Cost                  *float64
	AudioOutput           *schemas.BifrostSpeechResponse
	TranscriptionOutput   *schemas.BifrostTranscriptionResponse
	ImageGenerationOutput *schemas.BifrostImageGenerationResponse
	PassthroughOutput     *schemas.BifrostPassthroughResponse // For passthrough streaming
	FinishReason          *string
	LogProbs              *schemas.BifrostLogProbs
	RawResponse           *string
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
	LogProbs           *schemas.BifrostLogProbs               // LogProbs if available
	TokenUsage         *schemas.BifrostLLMUsage               // Token usage if available
	SemanticCacheDebug *schemas.BifrostCacheDebug             // Semantic cache debug if available
	GuardrailDebug     *schemas.BifrostGuardrailDebug         // Guardrail debug if available
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
	GuardrailDebug     *schemas.BifrostGuardrailDebug          // Guardrail debug if available
	Cost               *float64                                // Cost in dollars from pricing plugin
	ErrorDetails       *schemas.BifrostError                   // Error if any
	ChunkIndex         int                                     // Index of the chunk in the stream
	RawResponse        *string
}

// ImageStreamChunk represents a single image streaming chunk
type ImageStreamChunk struct {
	Timestamp          time.Time                                     // When chunk was received
	Delta              *schemas.BifrostImageGenerationStreamResponse // The actual stream response
	FinishReason       *string                                       // If this is the final chunk
	ChunkIndex         int                                           // Index of the chunk in the stream
	ImageIndex         int                                           // Index of the image in the stream
	ErrorDetails       *schemas.BifrostError                         // Error if any
	Cost               *float64                                      // Cost in dollars from pricing plugin
	SemanticCacheDebug *schemas.BifrostCacheDebug                    // Semantic cache debug if available
	TokenUsage         *schemas.ImageUsage                           // Token usage if available
	RawResponse        *string                                       // Raw response if available
}

// StreamAccumulator manages accumulation of streaming chunks
type StreamAccumulator struct {
	RequestID                 string
	StartTimestamp            time.Time
	FirstChunkTimestamp       time.Time // Timestamp when the first chunk was received (for TTFT calculation)
	ChatStreamChunks          []*ChatStreamChunk
	chatStreamType            StreamType // Chat and text completions share ChatStreamChunks; guarded by mu
	ResponsesStreamChunks     []*ResponsesStreamChunk
	TranscriptionStreamChunks []*TranscriptionStreamChunk
	AudioStreamChunks         []*AudioStreamChunk
	ImageStreamChunks         []*ImageStreamChunk

	// De-dup maps to prevent chunk loss on out-of-order arrival
	ChatChunksSeen          map[int]struct{}
	ResponsesChunksSeen     map[int]struct{}
	TranscriptionChunksSeen map[int]struct{}
	AudioChunksSeen         map[int]struct{}
	ImageChunksSeen         map[string]struct{} // Composite key: "imageIndex:chunkIndex" to scope de-dup per image

	// Track highest ChunkIndex for metadata extraction (TokenUsage, Cost, FinishReason)
	MaxChatChunkIndex          int
	MaxResponsesChunkIndex     int
	MaxTranscriptionChunkIndex int
	MaxAudioChunkIndex         int

	// TerminalErrorChunkIndex holds the reserved chunk index for the terminal error (-1 = unset); reused across plugin calls for correct dedup.
	TerminalErrorChunkIndex int
	// TerminalResponseChunkIndex holds the reserved index for a terminal response event when providers omit or reuse sequence numbers.
	TerminalResponseChunkIndex int

	// Passthrough streaming accumulation
	PassthroughBody       []byte            // Accumulated body bytes from passthrough streaming chunks
	PassthroughStatusCode int               // Status code from passthrough response
	PassthroughHeaders    map[string]string // Headers from passthrough response
	PassthroughPath       string            // Stripped provider path, e.g. "/v1/chat/completions"

	IsComplete     bool
	FinalTimestamp time.Time
	mu             sync.Mutex
	Timestamp      time.Time
	refCount       atomic.Int64

	// Pause/Resume gate state. All guarded by mu.
	// gateState transitions: Active -> Paused -> Active (replay) | Ended.
	// pausedAt is the seqCounter value at the moment PauseStream was called
	// (purely for diagnostics — the buffered chunks themselves drive replay).
	gateState    StreamState
	gatePausedAt int // -1 if never paused
	// gatePendingTerminal is set when an isFinal or isHardErr chunk arrived
	// while Paused. The flusher consumes this flag after Resume drains the
	// buffer and transitions the gate to Ended.
	gatePendingTerminal     bool
	gateSeq                 int                              // monotonic, bumped on every GateSend
	gateReplayBuf           []*schemas.BifrostStreamChunk    // wire-format chunks captured while paused
	gateReplayBufBytes      int64                            // sum of MarshalJSON sizes of chunks in gateReplayBuf; capped by gateReplayBufMaxBytes
	gateReplayEventInterval time.Duration                    // delay between buffered events after paced resume is armed
	gateCond                *sync.Cond                       // wakes flusher on Resume / End / append-while-active
	gateEndError            *schemas.BifrostError            // delivered as terminal chunk if EndStream(err) was called with non-nil
	gateFlusherCh           chan *schemas.BifrostStreamChunk // captured on first GateSend; reused by flusher
	gateFlusherCtx          *schemas.BifrostContext          // captured on first GateSend
	gateFlusherOn           bool                             // flusher goroutine running
	gateFlusherDone         chan struct{}                    // closed when the most recent flusher exits; nil when no flusher has ever started
	// gatePendingCleanup is set by cleanupStreamAccumulator when the caller
	// requested teardown but the gate is still busy (flusher running or
	// Paused). The flusher's exit defer checks this flag and re-runs cleanup
	// once the gate is idle, so teardown can't race ahead of the flusher.
	gatePendingCleanup bool
	parent             *Accumulator // back-reference for deferred cleanup re-entry
}

// StreamState represents the delivery state of a streaming response.
type StreamState int8

const (
	// StreamStateActive: chunks are forwarded to the client as they arrive.
	StreamStateActive StreamState = iota
	// StreamStatePaused: chunks are buffered for later replay; not delivered.
	StreamStatePaused
	// StreamStateEnded: gate is closed; further chunks are dropped.
	StreamStateEnded
)

// getLastChatChunk returns the chunk with the highest ChunkIndex (contains metadata like TokenUsage, Cost)
func (sa *StreamAccumulator) getLastChatChunk() *ChatStreamChunk {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	return sa.getLastChatChunkLocked()
}

// getLastChatChunkLocked returns the chunk with the highest ChunkIndex.
// MUST be called with sa.mu already held.
func (sa *StreamAccumulator) getLastChatChunkLocked() *ChatStreamChunk {
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

// getChatFinishReasonLocked returns the finish_reason of the highest-index
// chunk that actually carries one. The highest-index chunk overall can have a
// nil finish_reason (a usage-only chunk, or the synthetic terminal chunk the
// OpenAI-compatible handler appends after forwarding finish_reason on a content
// chunk), so reading it blindly drops the value for providers that send
// finish_reason together with content.
// MUST be called with sa.mu already held.
func (sa *StreamAccumulator) getChatFinishReasonLocked() *string {
	var finishReason *string
	maxIdx := -1
	for _, chunk := range sa.ChatStreamChunks {
		if chunk.FinishReason != nil && chunk.ChunkIndex > maxIdx {
			finishReason = chunk.FinishReason
			maxIdx = chunk.ChunkIndex
		}
	}
	return finishReason
}

// getLastResponsesChunk returns the chunk with the highest ChunkIndex (contains metadata like TokenUsage, Cost)
func (sa *StreamAccumulator) getLastResponsesChunk() *ResponsesStreamChunk {
	sa.mu.Lock()
	defer sa.mu.Unlock()
	return sa.getLastResponsesChunkLocked()
}

// getLastResponsesChunkLocked returns the chunk with the highest ChunkIndex.
// MUST be called with sa.mu already held.
func (sa *StreamAccumulator) getLastResponsesChunkLocked() *ResponsesStreamChunk {
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
	sa.mu.Lock()
	defer sa.mu.Unlock()
	return sa.getLastTranscriptionChunkLocked()
}

// getLastTranscriptionChunkLocked returns the chunk with the highest ChunkIndex.
// MUST be called with sa.mu already held.
func (sa *StreamAccumulator) getLastTranscriptionChunkLocked() *TranscriptionStreamChunk {
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
	sa.mu.Lock()
	defer sa.mu.Unlock()
	return sa.getLastAudioChunkLocked()
}

// getLastAudioChunkLocked returns the chunk with the highest ChunkIndex.
// MUST be called with sa.mu already held.
func (sa *StreamAccumulator) getLastAudioChunkLocked() *AudioStreamChunk {
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
	RequestID      string
	StreamType     StreamType
	Provider       schemas.ModelProvider
	RequestedModel string // original model requested by the caller
	ResolvedModel  string // actual model used by the provider (equals RequestedModel when no alias mapping exists)
	RoutingInfo    schemas.RoutingInfo
	Data           *AccumulatedData
	RawRequest     *interface{}
}

// ToBifrostResponse converts a ProcessedStreamResponse to a BifrostResponse
func (p *ProcessedStreamResponse) ToBifrostResponse() *schemas.BifrostResponse {
	if p.Data == nil {
		return nil
	}

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
			Model:  p.RequestedModel,
			Choices: []schemas.BifrostResponseChoice{
				{
					Index:        0,
					FinishReason: p.Data.FinishReason,
					LogProbs:     p.Data.LogProbs,
					TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{
						Text: &text,
					},
				},
			},
			Usage: p.Data.TokenUsage,
		}

		resp.TextCompletionResponse = textResp
		resp.TextCompletionResponse.ExtraFields = schemas.BifrostResponseExtraFields{
			RequestType:            schemas.TextCompletionRequest,
			Provider:               p.Provider,
			OriginalModelRequested: p.RequestedModel,
			ResolvedModelUsed:      p.ResolvedModel,
			Latency:                p.Data.Latency,
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
		if p.Data.GuardrailDebug != nil {
			resp.TextCompletionResponse.ExtraFields.GuardrailDebug = p.Data.GuardrailDebug
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
		usage := p.Data.TokenUsage
		if usage == nil && p.Data.Cost != nil && *p.Data.Cost > 0 {
			usage = &schemas.BifrostLLMUsage{
				Cost: &schemas.BifrostCost{TotalCost: *p.Data.Cost},
			}
		}
		chatResp := &schemas.BifrostChatResponse{
			ID:      p.RequestID,
			Object:  "chat.completion",
			Model:   p.RequestedModel,
			Created: int(p.Data.StartTimestamp.Unix()),
			Choices: []schemas.BifrostResponseChoice{
				{
					Index:        0,
					FinishReason: p.Data.FinishReason,
					LogProbs:     p.Data.LogProbs,
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: message,
					},
				},
			},
			Usage: usage,
		}

		resp.ChatResponse = chatResp
		resp.ChatResponse.ExtraFields = schemas.BifrostResponseExtraFields{
			RequestType:            schemas.ChatCompletionRequest,
			Provider:               p.Provider,
			OriginalModelRequested: p.RequestedModel,
			ResolvedModelUsed:      p.ResolvedModel,
			Latency:                p.Data.Latency,
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
		if p.Data.GuardrailDebug != nil {
			resp.ChatResponse.ExtraFields.GuardrailDebug = p.Data.GuardrailDebug
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
			RequestType:            schemas.ResponsesRequest,
			Provider:               p.Provider,
			OriginalModelRequested: p.RequestedModel,
			ResolvedModelUsed:      p.ResolvedModel,
			Latency:                p.Data.Latency,
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
		if p.Data.GuardrailDebug != nil {
			responsesResp.ExtraFields.GuardrailDebug = p.Data.GuardrailDebug
		}
		resp.ResponsesResponse = responsesResp
	case StreamTypeAudio:
		speechResp := p.Data.AudioOutput
		if speechResp == nil {
			speechResp = &schemas.BifrostSpeechResponse{}
		}
		resp.SpeechResponse = speechResp
		resp.SpeechResponse.ExtraFields = schemas.BifrostResponseExtraFields{
			RequestType:            schemas.SpeechRequest,
			Provider:               p.Provider,
			OriginalModelRequested: p.RequestedModel,
			ResolvedModelUsed:      p.ResolvedModel,
			Latency:                p.Data.Latency,
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
		if p.Data.GuardrailDebug != nil {
			resp.SpeechResponse.ExtraFields.GuardrailDebug = p.Data.GuardrailDebug
		}
	case StreamTypeTranscription:
		transcriptionResp := p.Data.TranscriptionOutput
		if transcriptionResp == nil {
			transcriptionResp = &schemas.BifrostTranscriptionResponse{}
		}
		resp.TranscriptionResponse = transcriptionResp
		resp.TranscriptionResponse.ExtraFields = schemas.BifrostResponseExtraFields{
			RequestType:            schemas.TranscriptionRequest,
			Provider:               p.Provider,
			OriginalModelRequested: p.RequestedModel,
			ResolvedModelUsed:      p.ResolvedModel,
			Latency:                p.Data.Latency,
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
		if p.Data.GuardrailDebug != nil {
			resp.TranscriptionResponse.ExtraFields.GuardrailDebug = p.Data.GuardrailDebug
		}
	case StreamTypeImage:
		imageResp := p.Data.ImageGenerationOutput
		if imageResp == nil {
			imageResp = &schemas.BifrostImageGenerationResponse{
				Data: make([]schemas.ImageData, 0),
			}
			if p.RequestID != "" {
				imageResp.ID = p.RequestID
			}
			if p.RequestedModel != "" {
				imageResp.Model = p.RequestedModel
			}
		}
		// Ensure Data is never nil to serialize as [] instead of null
		if imageResp.Data == nil {
			imageResp.Data = make([]schemas.ImageData, 0)
		}
		resp.ImageGenerationResponse = imageResp
		resp.ImageGenerationResponse.ExtraFields = schemas.BifrostResponseExtraFields{
			RequestType:            schemas.ImageGenerationRequest,
			Provider:               p.Provider,
			OriginalModelRequested: p.RequestedModel,
			ResolvedModelUsed:      p.ResolvedModel,
			Latency:                p.Data.Latency,
		}
		if p.RawRequest != nil {
			resp.ImageGenerationResponse.ExtraFields.RawRequest = p.RawRequest
		}
		if p.Data.RawResponse != nil {
			resp.ImageGenerationResponse.ExtraFields.RawResponse = *p.Data.RawResponse
		}
		if p.Data.CacheDebug != nil {
			resp.ImageGenerationResponse.ExtraFields.CacheDebug = p.Data.CacheDebug
		}
		if p.Data.GuardrailDebug != nil {
			resp.ImageGenerationResponse.ExtraFields.GuardrailDebug = p.Data.GuardrailDebug
		}

	}
	return resp
}
