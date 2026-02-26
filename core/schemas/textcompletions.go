package schemas

import (
	"fmt"
	"sync"

	"github.com/maximhq/bifrost/core/pool"
)

// BifrostTextCompletionRequest is the request struct for text completion requests
type BifrostTextCompletionRequest struct {
	Provider       ModelProvider             `json:"provider"`
	Model          string                    `json:"model"`
	Input          *TextCompletionInput      `json:"input,omitempty"`
	Params         *TextCompletionParameters `json:"params,omitempty"`
	Fallbacks      []Fallback                `json:"fallbacks,omitempty"`
	RawRequestBody []byte                    `json:"-"` // set bifrost-use-raw-request-body to true in ctx to use the raw request body. Bifrost will directly send this to the downstream provider.
}

// GetRawRequestBody returns the raw request body for the text completion request
func (r *BifrostTextCompletionRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
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

var bifrostTextCompletionRequestPool = sync.Pool{
	New: func() interface{} {
		return &BifrostTextCompletionRequest{}
	},
}

// AcquireBifrostTextCompletionRequest gets a BifrostTextCompletionRequest from the pool and resets it.
func AcquireBifrostTextCompletionRequest() *BifrostTextCompletionRequest {
	return bifrostTextCompletionRequestPool.Get().(*BifrostTextCompletionRequest)
}

// ReleaseBifrostTextCompletionRequest returns a BifrostTextCompletionRequest to the pool.
func ReleaseBifrostTextCompletionRequest(r *BifrostTextCompletionRequest) {
	if r == nil {
		return
	}
	r.Provider = ""
	r.Model = ""
	r.Input = nil
	r.Params = nil
	r.RawRequestBody = nil
	r.Fallbacks = nil
	bifrostTextCompletionRequestPool.Put(r)
}

// BifrostTextCompletionResponse is the response struct for text completion requests
type BifrostTextCompletionResponse struct {
	ID                string                     `json:"id"`
	Choices           []BifrostResponseChoice    `json:"choices"`
	Model             string                     `json:"model"`
	Object            string                     `json:"object"` // "text_completion" (same for text completion stream)
	SystemFingerprint string                     `json:"system_fingerprint"`
	Usage             *BifrostLLMUsage           `json:"usage"`
	ExtraFields       BifrostResponseExtraFields `json:"extra_fields"`
}

// bifrostTextCompletionResponsePool provides a pool for BifrostTextCompletionResponse objects.
var bifrostTextCompletionResponsePool = pool.New[BifrostTextCompletionResponse]("BifrostTextCompletionResponse", func() *BifrostTextCompletionResponse {
	return &BifrostTextCompletionResponse{}
})

// AcquireBifrostTextCompletionResponse gets a BifrostTextCompletionResponse from the pool and resets it.
func AcquireBifrostTextCompletionResponse() *BifrostTextCompletionResponse {
	return bifrostTextCompletionResponsePool.Get()
}

// ReleaseBifrostTextCompletionResponse returns a BifrostTextCompletionResponse to the pool.
func ReleaseBifrostTextCompletionResponse(r *BifrostTextCompletionResponse) {
	if r == nil {
		return
	}
	r.ID = ""
	r.Choices = nil
	r.Model = ""
	r.Object = ""
	r.SystemFingerprint = ""
	r.Usage = nil
	r.ExtraFields = BifrostResponseExtraFields{}
	bifrostTextCompletionResponsePool.Put(r)
}

// TextCompletionInput is the input struct for text completion requests
type TextCompletionInput struct {
	PromptStr   *string
	PromptArray []string
}

// MarshalJSON implements custom JSON marshalling for TextCompletionInput.
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
		return Marshal(*t.PromptStr)
	}
	return Marshal(t.PromptArray)
}

// UnmarshalJSON implements custom JSON unmarshalling for TextCompletionInput.
func (t *TextCompletionInput) UnmarshalJSON(data []byte) error {
	var prompt string
	if err := Unmarshal(data, &prompt); err == nil {
		t.PromptStr = &prompt
		t.PromptArray = nil
		return nil
	}
	var promptArray []string
	if err := Unmarshal(data, &promptArray); err == nil {
		t.PromptStr = nil
		t.PromptArray = promptArray
		return nil
	}
	return fmt.Errorf("invalid text completion input")
}

// textCompletionInputPool provides a pool for TextCompletionInput objects.
var textCompletionInputPool = pool.New[TextCompletionInput]("TextCompletionInput", func() *TextCompletionInput {
	return &TextCompletionInput{}
})

// AcquireTextCompletionInput gets a TextCompletionInput from the pool and resets it.
func AcquireTextCompletionInput() *TextCompletionInput {
	return textCompletionInputPool.Get()
}

// ReleaseTextCompletionInput returns a TextCompletionInput to the pool.
func ReleaseTextCompletionInput(t *TextCompletionInput) {
	if t == nil {
		return
	}
	t.PromptStr = nil
	t.PromptArray = nil
	textCompletionInputPool.Put(t)
}

// TextCompletionParameters is the parameters struct for text completion requests
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

// textCompletionParametersPool provides a pool for TextCompletionParameters objects.
var textCompletionParametersPool = pool.New[TextCompletionParameters]("TextCompletionParameters", func() *TextCompletionParameters {
	return &TextCompletionParameters{}
})

// AcquireTextCompletionParameters gets a TextCompletionParameters from the pool and resets it.
func AcquireTextCompletionParameters() *TextCompletionParameters {
	return textCompletionParametersPool.Get()
}

// ReleaseTextCompletionParameters returns a TextCompletionParameters to the pool.
func ReleaseTextCompletionParameters(p *TextCompletionParameters) {
	if p == nil {
		return
	}
	p.BestOf = nil
	p.Echo = nil
	p.FrequencyPenalty = nil
	p.LogitBias = nil
	p.LogProbs = nil
	p.MaxTokens = nil
	p.N = nil
	p.PresencePenalty = nil
	p.Seed = nil
	p.Stop = nil
	p.Suffix = nil
	p.StreamOptions = nil
	p.Temperature = nil
	p.TopP = nil
	p.User = nil
	p.ExtraParams = nil
	textCompletionParametersPool.Put(p)
}

// TextCompletionLogProb represents log probability information for text completion.
type TextCompletionLogProb struct {
	TextOffset    []int                `json:"text_offset"`
	TokenLogProbs []float64            `json:"token_logprobs"`
	Tokens        []string             `json:"tokens"`
	TopLogProbs   []map[string]float64 `json:"top_logprobs"`
}
