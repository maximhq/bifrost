package openai

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// OpenAIChatRequest represents an OpenAI chat completion request
type OpenAIChatRequest struct {
	Model            string                   `json:"model"`
	Messages         []schemas.BifrostMessage `json:"messages"`
	MaxTokens        *int                     `json:"max_tokens,omitempty"`
	Temperature      *float64                 `json:"temperature,omitempty"`
	TopP             *float64                 `json:"top_p,omitempty"`
	N                *int                     `json:"n,omitempty"`
	Stop             interface{}              `json:"stop,omitempty"`
	PresencePenalty  *float64                 `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64                 `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]float64       `json:"logit_bias,omitempty"`
	User             *string                  `json:"user,omitempty"`
	Tools            *[]schemas.Tool          `json:"tools,omitempty"` // Reuse schema type
	ToolChoice       *schemas.ToolChoice      `json:"tool_choice,omitempty"`
	Stream           *bool                    `json:"stream,omitempty"`
	LogProbs         *bool                    `json:"logprobs,omitempty"`
	TopLogProbs      *int                     `json:"top_logprobs,omitempty"`
	ResponseFormat   interface{}              `json:"response_format,omitempty"`
	Seed             *int                     `json:"seed,omitempty"`
}

// OpenAIChatResponse represents an OpenAI chat completion response
type OpenAIChatResponse struct {
	ID                string                          `json:"id"`
	Object            string                          `json:"object"`
	Created           int                             `json:"created"`
	Model             string                          `json:"model"`
	Choices           []schemas.BifrostResponseChoice `json:"choices"`
	Usage             *schemas.LLMUsage               `json:"usage,omitempty"` // Reuse schema type
	ServiceTier       *string                         `json:"service_tier,omitempty"`
	SystemFingerprint *string                         `json:"system_fingerprint,omitempty"`
}

// ConvertToBifrostRequest converts an OpenAI chat request to Bifrost format
func (r *OpenAIChatRequest) ConvertToBifrostRequest() *schemas.BifrostRequest {
	bifrostReq := &schemas.BifrostRequest{
		Provider: schemas.OpenAI,
		Model:    r.Model,
		Input: schemas.RequestInput{
			ChatCompletionInput: &r.Messages,
		},
	}

	// Map extra parameters and tool settings
	bifrostReq.Params = r.convertParameters()

	return bifrostReq
}

// convertParameters converts OpenAI request parameters to Bifrost ModelParameters
// using direct field access for better performance and type safety.
func (r *OpenAIChatRequest) convertParameters() *schemas.ModelParameters {
	params := &schemas.ModelParameters{
		ExtraParams: make(map[string]interface{}),
	}

	params.Tools = r.Tools
	params.ToolChoice = r.ToolChoice

	// Direct field mapping
	if r.MaxTokens != nil {
		params.MaxTokens = r.MaxTokens
	}
	if r.Temperature != nil {
		params.Temperature = r.Temperature
	}
	if r.TopP != nil {
		params.TopP = r.TopP
	}
	if r.PresencePenalty != nil {
		params.PresencePenalty = r.PresencePenalty
	}
	if r.FrequencyPenalty != nil {
		params.FrequencyPenalty = r.FrequencyPenalty
	}
	if r.N != nil {
		params.ExtraParams["n"] = *r.N
	}
	if r.LogProbs != nil {
		params.ExtraParams["logprobs"] = *r.LogProbs
	}
	if r.TopLogProbs != nil {
		params.ExtraParams["top_logprobs"] = *r.TopLogProbs
	}
	if r.Stop != nil {
		params.ExtraParams["stop"] = r.Stop
	}
	if r.LogitBias != nil {
		params.ExtraParams["logit_bias"] = r.LogitBias
	}
	if r.User != nil {
		params.ExtraParams["user"] = *r.User
	}
	if r.Stream != nil {
		params.ExtraParams["stream"] = *r.Stream
	}
	if r.Seed != nil {
		params.ExtraParams["seed"] = *r.Seed
	}

	return params
}

// DeriveOpenAIFromBifrostResponse converts a Bifrost response to OpenAI format
func DeriveOpenAIFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *OpenAIChatResponse {
	if bifrostResp == nil {
		return nil
	}

	openaiResp := &OpenAIChatResponse{
		ID:                bifrostResp.ID,
		Object:            bifrostResp.Object,
		Created:           bifrostResp.Created,
		Model:             bifrostResp.Model,
		Choices:           bifrostResp.Choices,
		Usage:             &bifrostResp.Usage,
		ServiceTier:       bifrostResp.ServiceTier,
		SystemFingerprint: bifrostResp.SystemFingerprint,
	}

	return openaiResp
}
