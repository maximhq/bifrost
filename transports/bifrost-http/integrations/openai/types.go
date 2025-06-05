package openai

import schemas "github.com/maximhq/bifrost/core/schemas"

// ChatCompletionRequest mirrors the basic OpenAI Chat API request.
// Only a subset of fields are implemented as the goal of this
// middleware is simply to forward the request to Bifrost.
type ChatCompletionRequest struct {
	Model             string                   `json:"model"`
	Messages          []schemas.BifrostMessage `json:"messages"`
	Temperature       *float64                 `json:"temperature,omitempty"`
	TopP              *float64                 `json:"top_p,omitempty"`
	TopK              *int                     `json:"top_k,omitempty"`
	MaxTokens         *int                     `json:"max_tokens,omitempty"`
	StopSequences     *[]string                `json:"stop_sequences,omitempty"`
	PresencePenalty   *float64                 `json:"presence_penalty,omitempty"`
	FrequencyPenalty  *float64                 `json:"frequency_penalty,omitempty"`
	ParallelToolCalls *bool                    `json:"parallel_tool_calls,omitempty"`
	Tools             *[]schemas.Tool          `json:"tools,omitempty"`
	ToolChoice        *schemas.ToolChoice      `json:"tool_choice,omitempty"`
}

// ConvertToBifrostRequest converts the request to a BifrostRequest.
func (r *ChatCompletionRequest) ConvertToBifrostRequest(model string) *schemas.BifrostRequest {
	if model == "" {
		model = r.Model
	}
	params := &schemas.ModelParameters{
		Temperature:       r.Temperature,
		TopP:              r.TopP,
		TopK:              r.TopK,
		MaxTokens:         r.MaxTokens,
		StopSequences:     r.StopSequences,
		PresencePenalty:   r.PresencePenalty,
		FrequencyPenalty:  r.FrequencyPenalty,
		ParallelToolCalls: r.ParallelToolCalls,
		Tools:             r.Tools,
		ToolChoice:        r.ToolChoice,
	}

	return &schemas.BifrostRequest{
		Provider: schemas.OpenAI,
		Model:    model,
		Input: schemas.RequestInput{
			ChatCompletionInput: &r.Messages,
		},
		Params: params,
	}
}
