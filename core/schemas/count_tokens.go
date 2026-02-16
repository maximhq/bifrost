package schemas

import "sync"

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

// bifrostCountTokensResponsePool provides a pool for BifrostCountTokensResponse objects.
var bifrostCountTokensResponsePool = sync.Pool{
	New: func() interface{} {
		return &BifrostCountTokensResponse{}
	},
}

// AcquireBifrostCountTokensResponse gets a BifrostCountTokensResponse from the pool and resets it.
func AcquireBifrostCountTokensResponse() *BifrostCountTokensResponse {
	r := bifrostCountTokensResponsePool.Get().(*BifrostCountTokensResponse)
	*r = BifrostCountTokensResponse{}
	return r
}

// ReleaseBifrostCountTokensResponse returns a BifrostCountTokensResponse to the pool.
// The caller must ensure no other goroutine holds a reference to this response.
func ReleaseBifrostCountTokensResponse(r *BifrostCountTokensResponse) {
	if r == nil {
		return
	}
	r.Object = ""
	r.Model = ""
	r.InputTokens = 0
	r.InputTokensDetails = nil
	r.Tokens = nil
	r.TokenStrings = nil
	r.OutputTokens = nil
	r.TotalTokens = nil
	r.ExtraFields = BifrostResponseExtraFields{}
	bifrostCountTokensResponsePool.Put(r)
}
