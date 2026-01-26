package main

import "github.com/maximhq/bifrost/core/schemas"

// ============================================================================
// Input/Output Structs
// ============================================================================

// HTTPInterceptInput is the input for http_intercept
type HTTPInterceptInput struct {
	Context map[string]interface{} `json:"context"`
	Request *schemas.HTTPRequest   `json:"request,omitempty"`
}

// HTTPInterceptOutput is the output for http_intercept
type HTTPInterceptOutput struct {
	Context     map[string]interface{} `json:"context"`
	Request     *schemas.HTTPRequest   `json:"request,omitempty"`
	Response    *schemas.HTTPResponse  `json:"response,omitempty"`
	HasResponse bool                   `json:"has_response"`
	Error       string                 `json:"error"`
}

// PreHookInput is the input for pre_hook
type PreHookInput struct {
	Context map[string]interface{}  `json:"context"`
	Request *schemas.BifrostRequest `json:"request,omitempty"` // Keep raw for pass-through
}

// PreHookOutput is the output for pre_hook
type PreHookOutput struct {
	Context         map[string]interface{}      `json:"context"`
	Request         *schemas.BifrostRequest     `json:"request,omitempty"`
	ShortCircuit    *schemas.PluginShortCircuit `json:"short_circuit,omitempty"`
	HasShortCircuit bool                        `json:"has_short_circuit"`
	Error           string                      `json:"error"`
}

// PostHookInput is the input for post_hook
type PostHookInput struct {
	Context  map[string]interface{}   `json:"context"`
	Response *schemas.BifrostResponse `json:"response,omitempty"`
	Error    *schemas.BifrostError    `json:"error,omitempty"`
	HasError bool                     `json:"has_error"`
}

// PostHookOutput is the output for post_hook
type PostHookOutput struct {
	Context   map[string]interface{}   `json:"context"`
	Response  *schemas.BifrostResponse `json:"response,omitempty"`
	Error     *schemas.BifrostError    `json:"error,omitempty"`
	HasError  bool                     `json:"has_error"`
	HookError string                   `json:"hook_error"`
}

// HTTPStreamChunkHookInput is the input for http_stream_chunk_hook
type HTTPStreamChunkHookInput struct {
	Context map[string]interface{}     `json:"context"`
	Request *schemas.HTTPRequest       `json:"request,omitempty"`
	Chunk   *schemas.BifrostStreamChunk `json:"chunk,omitempty"`
}

// HTTPStreamChunkHookOutput is the output for http_stream_chunk_hook
type HTTPStreamChunkHookOutput struct {
	Context  map[string]interface{}     `json:"context"`
	Chunk    *schemas.BifrostStreamChunk `json:"chunk,omitempty"`
	HasChunk bool                       `json:"has_chunk"`
	Skip     bool                       `json:"skip"`
	Error    string                     `json:"error"`
}
