package azure

import (
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// AzureTextResponse represents the response structure from Azure's text completion API.
// It includes completion choices, model information, and usage statistics.
type AzureTextResponse struct {
	ID      string `json:"id"`     // Unique identifier for the completion
	Object  string `json:"object"` // Type of completion (always "text.completion")
	Choices []struct {
		FinishReason *string                       `json:"finish_reason,omitempty"` // Reason for completion termination
		Index        int                           `json:"index"`                   // Index of the choice
		Text         string                        `json:"text"`                    // Generated text
		LogProbs     schemas.TextCompletionLogProb `json:"logprobs"`                // Log probabilities
	} `json:"choices"`
	Model             string           `json:"model"`              // Model used for the completion
	Created           int              `json:"created"`            // Unix timestamp of completion creation
	SystemFingerprint *string          `json:"system_fingerprint"` // System fingerprint for the request
	Usage             schemas.LLMUsage `json:"usage"`              // Token usage statistics
}

// AzureError represents the error response structure from Azure's API.
// It includes error code and message information.
type AzureError struct {
	Error struct {
		Code    string `json:"code"`    // Error code
		Message string `json:"message"` // Error message
	} `json:"error"`
}
