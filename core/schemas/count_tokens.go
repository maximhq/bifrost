package schemas

// BifrostCountTokensResponse captures token counts for a provided input.
type BifrostCountTokensResponse struct {
	Object             string                        `json:"object,omitempty"`
	Model              string                        `json:"model"`
	InputTokens        int                           `json:"input_tokens"`
	InputTokensDetails *ResponsesResponseInputTokens `json:"input_tokens_details,omitempty"`
	Tokens             []int                         `json:"tokens"`
	TokenStrings       []string                      `json:"token_strings,omitempty"`
	OutputTokens       *int                          `json:"output_tokens,omitempty"`
	TotalTokens        *int                          `json:"total_tokens"`
	ExtraFields        BifrostResponseExtraFields    `json:"extra_fields"`
}
