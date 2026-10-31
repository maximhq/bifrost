package vllm

import (
	"github.com/maximhq/bifrost/core/providers/typesafe"
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

type VLLMDecisionRequest struct {
	*typesafe.TypesafeDecisionRequest
	Images []string `json:"images,omitempty"` // data or http(s) URLs, shared by every question
}