package vllm

import (
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// vLLMTranscriptionStreamChunk represents a single transcription streaming chunk from vLLM.
type vLLMTranscriptionStreamChunk struct {
	Object  string `json:"object"`
	Choices []struct {
		Delta struct {
			Content          *string `json:"content"`
			ReasoningContent *string `json:"reasoning_content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason,omitempty"`
		StopReason   *string `json:"stop_reason,omitempty"`
	} `json:"choices"`
	Usage *schemas.TranscriptionUsage `json:"usage,omitempty"`
}
