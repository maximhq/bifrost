package openrouter

import "github.com/maximhq/bifrost/core/schemas"

// OpenRouter response structures

// OpenRouterTextResponse represents the response from OpenRouter text completion API
type OpenRouterTextResponse struct {
	ID                string                 `json:"id"`
	Model             string                 `json:"model"`
	Created           int                    `json:"created"`
	SystemFingerprint *string                `json:"system_fingerprint"`
	Choices           []OpenRouterTextChoice `json:"choices"`
	Usage             *schemas.LLMUsage      `json:"usage"`
}

// OpenRouterTextChoice represents a choice in the OpenRouter text completion response
type OpenRouterTextChoice struct {
	Text         string `json:"text"`
	Index        int    `json:"index"`
	FinishReason string `json:"finish_reason"`
}
