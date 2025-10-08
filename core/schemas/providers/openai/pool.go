package openai

import (
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
)

// Pool capacity limits to prevent memory leaks from overly large data
const (
	maxByteSliceSize = 10 * 1024 * 1024 // Max 10MB for transcription files before discarding
)

// ==================== REQUEST POOLS ====================

// openaiTextRequestPool provides a pool for OpenAI text completion request objects.
var openaiTextRequestPool = sync.Pool{
	New: func() interface{} {
		return &OpenAITextCompletionRequest{}
	},
}

// openaiChatRequestPool provides a pool for OpenAI chat completion request objects.
var openaiChatRequestPool = sync.Pool{
	New: func() interface{} {
		return &OpenAIChatRequest{}
	},
}

// openaiEmbeddingRequestPool provides a pool for OpenAI embedding request objects.
var openaiEmbeddingRequestPool = sync.Pool{
	New: func() interface{} {
		return &OpenAIEmbeddingRequest{}
	},
}

// openaiResponsesRequestPool provides a pool for OpenAI responses request objects.
var openaiResponsesRequestPool = sync.Pool{
	New: func() interface{} {
		return &OpenAIResponsesRequest{}
	},
}

// openaiSpeechRequestPool provides a pool for OpenAI speech request objects.
var openaiSpeechRequestPool = sync.Pool{
	New: func() interface{} {
		return &OpenAISpeechRequest{}
	},
}

// openaiTranscriptionRequestPool provides a pool for OpenAI transcription request objects.
var openaiTranscriptionRequestPool = sync.Pool{
	New: func() interface{} {
		return &OpenAITranscriptionRequest{}
	},
}

// ==================== PUBLIC ACQUIRE/RELEASE ====================

// AcquireTextRequest gets a text completion request from the pool and resets it.
func AcquireTextRequest() *OpenAITextCompletionRequest {
	req := openaiTextRequestPool.Get().(*OpenAITextCompletionRequest)

	// Reset all fields
	req.Model = ""
	req.Prompt = nil
	req.Stream = nil
	// TextCompletionParameters is embedded - reset to zero value
	req.TextCompletionParameters = schemas.TextCompletionParameters{}

	return req
}

// ReleaseTextRequest returns a text completion request to the pool.
func ReleaseTextRequest(req *OpenAITextCompletionRequest) {
	if req != nil {
		openaiTextRequestPool.Put(req)
	}
}

// AcquireChatRequest gets a chat completion request from the pool and resets it.
func AcquireChatRequest() *OpenAIChatRequest {
	req := openaiChatRequestPool.Get().(*OpenAIChatRequest)

	// Reset all fields
	req.Model = ""
	req.Messages = nil // Don't pool schemas.ChatMessage slices per user instruction
	req.Stream = nil
	// ChatParameters is embedded - reset to zero value
	req.ChatParameters = schemas.ChatParameters{}

	return req
}

// ReleaseChatRequest returns a chat completion request to the pool.
func ReleaseChatRequest(req *OpenAIChatRequest) {
	if req != nil {
		openaiChatRequestPool.Put(req)
	}
}

// AcquireEmbeddingRequest gets an embedding request from the pool and resets it.
func AcquireEmbeddingRequest() *OpenAIEmbeddingRequest {
	req := openaiEmbeddingRequestPool.Get().(*OpenAIEmbeddingRequest)

	// Reset all fields
	req.Model = ""
	req.Input = nil // Don't pool schemas.EmbeddingInput per user instruction
	// EmbeddingParameters is embedded - reset to zero value
	req.EmbeddingParameters = schemas.EmbeddingParameters{}

	return req
}

// ReleaseEmbeddingRequest returns an embedding request to the pool.
func ReleaseEmbeddingRequest(req *OpenAIEmbeddingRequest) {
	if req != nil {
		openaiEmbeddingRequestPool.Put(req)
	}
}

// AcquireResponsesRequest gets a responses request from the pool and resets it.
func AcquireResponsesRequest() *OpenAIResponsesRequest {
	req := openaiResponsesRequestPool.Get().(*OpenAIResponsesRequest)

	// Reset all fields
	req.Model = ""
	req.Input = OpenAIResponsesRequestInput{}
	req.Stream = nil
	// ResponsesParameters is embedded - reset to zero value
	req.ResponsesParameters = schemas.ResponsesParameters{}

	return req
}

// ReleaseResponsesRequest returns a responses request to the pool.
func ReleaseResponsesRequest(req *OpenAIResponsesRequest) {
	if req != nil {
		openaiResponsesRequestPool.Put(req)
	}
}

// AcquireSpeechRequest gets a speech request from the pool and resets it.
func AcquireSpeechRequest() *OpenAISpeechRequest {
	req := openaiSpeechRequestPool.Get().(*OpenAISpeechRequest)

	// Reset all fields
	req.Model = ""
	req.Input = ""
	req.StreamFormat = nil
	// SpeechParameters is embedded - reset to zero value
	req.SpeechParameters = schemas.SpeechParameters{}

	return req
}

// ReleaseSpeechRequest returns a speech request to the pool.
func ReleaseSpeechRequest(req *OpenAISpeechRequest) {
	if req != nil {
		openaiSpeechRequestPool.Put(req)
	}
}

// AcquireTranscriptionRequest gets a transcription request from the pool and resets it.
func AcquireTranscriptionRequest() *OpenAITranscriptionRequest {
	req := openaiTranscriptionRequestPool.Get().(*OpenAITranscriptionRequest)

	// Reset all fields
	req.Model = ""
	req.File = nil // Will be set to new byte slice when used
	req.Stream = nil
	// TranscriptionParameters is embedded - reset to zero value
	req.TranscriptionParameters = schemas.TranscriptionParameters{}

	return req
}

// ReleaseTranscriptionRequest returns a transcription request to the pool.
func ReleaseTranscriptionRequest(req *OpenAITranscriptionRequest) {
	if req == nil {
		return
	}

	// Only pool if file size is reasonable to prevent memory leaks
	if len(req.File) <= maxByteSliceSize {
		openaiTranscriptionRequestPool.Put(req)
	}
	// If file is too large, let it be garbage collected instead of pooling
}
