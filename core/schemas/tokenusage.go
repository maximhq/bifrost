package schemas

import "github.com/tidwall/gjson"

// Numeric convenience fields cannot distinguish an absent measurement from
// measured zero. Preserve that distinction after decoding, conversion and
// accumulation without retaining provider payloads. Programmatically constructed
// usage declares its numeric fields; decoded usage declares only observed fields.
type tokenUsagePresence uint8

const (
	tokenUsageTracked tokenUsagePresence = 1 << iota
	tokenUsageInput
	tokenUsageOutput
	tokenUsageTotal
)

func (p tokenUsagePresence) observed() tokenUsagePresence {
	if p&tokenUsageTracked == 0 {
		return tokenUsageTracked | tokenUsageInput | tokenUsageOutput | tokenUsageTotal
	}
	return p
}

func decodedTokenPresence(data []byte, input, output string) tokenUsagePresence {
	p := tokenUsageTracked
	value := gjson.ParseBytes(data)
	if value.Get(input).Type == gjson.Number {
		p |= tokenUsageInput
	}
	if value.Get(output).Type == gjson.Number {
		p |= tokenUsageOutput
	}
	if value.Get("total_tokens").Type == gjson.Number {
		p |= tokenUsageTotal
	}
	return p
}

func observedTokenValue(value *int, present bool) *int {
	if present {
		return value
	}
	return nil
}

// TokenMeasurements exposes only observed counters, including an explicit zero.
func (u *BifrostLLMUsage) TokenMeasurements() (input, output, total *int) {
	if u == nil {
		return nil, nil, nil
	}
	p := u.tokenPresence.observed()
	return observedTokenValue(&u.PromptTokens, p&tokenUsageInput != 0),
		observedTokenValue(&u.CompletionTokens, p&tokenUsageOutput != 0),
		observedTokenValue(&u.TotalTokens, p&tokenUsageTotal != 0)
}

// TokenMeasurements exposes only observed counters, including an explicit zero.
func (u *ResponsesResponseUsage) TokenMeasurements() (input, output, total *int) {
	if u == nil {
		return nil, nil, nil
	}
	p := u.tokenPresence.observed()
	return observedTokenValue(&u.InputTokens, p&tokenUsageInput != 0),
		observedTokenValue(&u.OutputTokens, p&tokenUsageOutput != 0),
		observedTokenValue(&u.TotalTokens, p&tokenUsageTotal != 0)
}

func (u *BifrostLLMUsage) UnmarshalJSON(data []byte) error {
	type wire BifrostLLMUsage
	*u = BifrostLLMUsage{}
	if err := Unmarshal(data, (*wire)(u)); err != nil {
		return err
	}
	u.tokenPresence = decodedTokenPresence(data, "prompt_tokens", "completion_tokens")
	return nil
}

func (u BifrostLLMUsage) MarshalJSON() ([]byte, error) {
	type wire BifrostLLMUsage
	p := u.tokenPresence.observed()
	if p == tokenUsageTracked|tokenUsageInput|tokenUsageOutput|tokenUsageTotal && u.PromptTokens != 0 && u.CompletionTokens != 0 {
		return Marshal((*wire)(&u))
	}
	return Marshal(&struct {
		*wire
		Input  *int `json:"prompt_tokens,omitempty"`
		Output *int `json:"completion_tokens,omitempty"`
		Total  *int `json:"total_tokens,omitempty"`
	}{(*wire)(&u), observedTokenValue(&u.PromptTokens, p&tokenUsageInput != 0),
		observedTokenValue(&u.CompletionTokens, p&tokenUsageOutput != 0),
		observedTokenValue(&u.TotalTokens, p&tokenUsageTotal != 0)})
}

func (u *ResponsesResponseUsage) UnmarshalJSON(data []byte) error {
	type wire ResponsesResponseUsage
	*u = ResponsesResponseUsage{}
	if err := Unmarshal(data, (*wire)(u)); err != nil {
		return err
	}
	u.tokenPresence = decodedTokenPresence(data, "input_tokens", "output_tokens")
	return nil
}

func (u ResponsesResponseUsage) MarshalJSON() ([]byte, error) {
	type wire ResponsesResponseUsage
	p := u.tokenPresence.observed()
	if p == tokenUsageTracked|tokenUsageInput|tokenUsageOutput|tokenUsageTotal {
		return Marshal((*wire)(&u))
	}
	return Marshal(&struct {
		*wire
		Input  *int `json:"input_tokens,omitempty"`
		Output *int `json:"output_tokens,omitempty"`
		Total  *int `json:"total_tokens,omitempty"`
	}{(*wire)(&u), observedTokenValue(&u.InputTokens, p&tokenUsageInput != 0),
		observedTokenValue(&u.OutputTokens, p&tokenUsageOutput != 0),
		observedTokenValue(&u.TotalTokens, p&tokenUsageTotal != 0)})
}

// NewTokenUsageAccumulator starts without observed counters. It can merge
// cumulative provider receipts without fabricating a missing component or total.
func NewTokenUsageAccumulator() *BifrostLLMUsage {
	return &BifrostLLMUsage{tokenPresence: tokenUsageTracked}
}

// AccumulateTokenUsage merges cumulative numeric observations. Cache and other
// usage details keep their existing provider-specific accumulation semantics.
func (u *BifrostLLMUsage) AccumulateTokenUsage(other *BifrostLLMUsage) {
	if other == nil {
		return
	}
	u.tokenPresence = u.tokenPresence.observed() | other.tokenPresence.observed()
	u.PromptTokens = max(u.PromptTokens, other.PromptTokens)
	u.CompletionTokens = max(u.CompletionTokens, other.CompletionTokens)
	u.TotalTokens = max(u.TotalTokens, other.TotalTokens)
	if u.tokenPresence&(tokenUsageInput|tokenUsageOutput) == tokenUsageInput|tokenUsageOutput {
		u.TotalTokens = max(u.TotalTokens, u.PromptTokens+u.CompletionTokens)
		u.tokenPresence |= tokenUsageTotal
	}
}

// NewTokenUsage declares only supplied token counters, copying the values so the
// returned usage cannot change when the provider response or caller is reused.
func NewTokenUsage(input, output, total *int) *BifrostLLMUsage {
	u := NewTokenUsageAccumulator()
	if input != nil {
		u.PromptTokens = *input
		u.tokenPresence |= tokenUsageInput
	}
	if output != nil {
		u.CompletionTokens = *output
		u.tokenPresence |= tokenUsageOutput
	}
	if total != nil {
		u.TotalTokens = *total
		u.tokenPresence |= tokenUsageTotal
	}
	return u
}
