package schemas

import (
	"fmt"

	"github.com/bytedance/sonic"
)

// BifrostTextCompletionRequest is the request struct for text completion requests
type BifrostTextCompletionRequest struct {
	Provider  ModelProvider             `json:"provider"`
	Model     string                    `json:"model"`
	Input     *TextCompletionInput      `json:"input,omitempty"`
	Params    *TextCompletionParameters `json:"params,omitempty"`
	Fallbacks []Fallback                `json:"fallbacks,omitempty"`
}

// ToBifrostChatRequest converts a Bifrost text completion request to a Bifrost chat completion request
// This method is discouraged to use, but is useful for litellm fallback flows
func (r *BifrostTextCompletionRequest) ToBifrostChatRequest() *BifrostChatRequest {
	if r == nil || r.Input == nil {
		return nil
	}
	message := ChatMessage{Role: ChatMessageRoleUser}
	if r.Input.PromptStr != nil {
		message.Content = &ChatMessageContent{
			ContentStr: r.Input.PromptStr,
		}
	} else if len(r.Input.PromptArray) > 0 {
		blocks := make([]ChatContentBlock, 0, len(r.Input.PromptArray))
		for _, prompt := range r.Input.PromptArray {
			blocks = append(blocks, ChatContentBlock{
				Type: ChatContentBlockTypeText,
				Text: &prompt,
			})
		}
		message.Content = &ChatMessageContent{
			ContentBlocks: blocks,
		}
	}
	params := ChatParameters{}
	if r.Params != nil {
		params.MaxCompletionTokens = r.Params.MaxTokens
		params.Temperature = r.Params.Temperature
		params.TopP = r.Params.TopP
		params.Stop = r.Params.Stop
		params.ExtraParams = r.Params.ExtraParams
		params.StreamOptions = r.Params.StreamOptions
		params.User = r.Params.User
		params.FrequencyPenalty = r.Params.FrequencyPenalty
		params.LogitBias = r.Params.LogitBias
		params.PresencePenalty = r.Params.PresencePenalty
		params.Seed = r.Params.Seed
	}
	return &BifrostChatRequest{
		Provider:  r.Provider,
		Model:     r.Model,
		Fallbacks: r.Fallbacks,
		Input:     []ChatMessage{message},
		Params:    &params,
	}
}

type BifrostTextCompletionResponse struct {
	ID                string                     `json:"id"`
	Choices           []BifrostResponseChoice    `json:"choices"`
	Model             string                     `json:"model"`
	Object            string                     `json:"object"` // "text_completion" (same for text completion stream)
	SystemFingerprint string                     `json:"system_fingerprint"`
	Usage             *BifrostLLMUsage           `json:"usage"`
	ExtraFields       BifrostResponseExtraFields `json:"extra_fields"`
}

type TextCompletionInput struct {
	PromptStr   *string
	PromptArray []string
}

func (t *TextCompletionInput) MarshalJSON() ([]byte, error) {
	set := 0
	if t.PromptStr != nil {
		set++
	}
	if t.PromptArray != nil {
		set++
	}
	if set == 0 {
		return nil, fmt.Errorf("text completion input is empty")
	}
	if set > 1 {
		return nil, fmt.Errorf("text completion input must set exactly one of: prompt_str or prompt_array")
	}
	if t.PromptStr != nil {
		return sonic.Marshal(*t.PromptStr)
	}
	return sonic.Marshal(t.PromptArray)
}

func (t *TextCompletionInput) UnmarshalJSON(data []byte) error {
	var prompt string
	if err := sonic.Unmarshal(data, &prompt); err == nil {
		t.PromptStr = &prompt
		t.PromptArray = nil
		return nil
	}
	var promptArray []string
	if err := sonic.Unmarshal(data, &promptArray); err == nil {
		t.PromptStr = nil
		t.PromptArray = promptArray
		return nil
	}
	return fmt.Errorf("invalid text completion input")
}

type TextCompletionParameters struct {
	BestOf           *int                `json:"best_of,omitempty"`
	Echo             *bool               `json:"echo,omitempty"`
	FrequencyPenalty *float64            `json:"frequency_penalty,omitempty"`
	LogitBias        *map[string]float64 `json:"logit_bias,omitempty"`
	LogProbs         *int                `json:"logprobs,omitempty"`
	MaxTokens        *int                `json:"max_tokens,omitempty"`
	N                *int                `json:"n,omitempty"`
	PresencePenalty  *float64            `json:"presence_penalty,omitempty"`
	Seed             *int                `json:"seed,omitempty"`
	Stop             []string            `json:"stop,omitempty"`
	Suffix           *string             `json:"suffix,omitempty"`
	StreamOptions    *ChatStreamOptions  `json:"stream_options,omitempty"`
	Temperature      *float64            `json:"temperature,omitempty"`
	TopP             *float64            `json:"top_p,omitempty"`
	User             *string             `json:"user,omitempty"`

	// Dynamic parameters that can be provider-specific, they are directly
	// added to the request as is.
	ExtraParams map[string]interface{} `json:"-"`
}

// TextCompletionLogProb represents log probability information for text completion.
type TextCompletionLogProb struct {
	TextOffset    []int                `json:"text_offset"`
	TokenLogProbs []float64            `json:"token_logprobs"`
	Tokens        []string             `json:"tokens"`
	TopLogProbs   []map[string]float64 `json:"top_logprobs"`
}
