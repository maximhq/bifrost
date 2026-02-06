//go:build tinygo || wasm

package schemas

// LLMPluginShortCircuit represents a plugin's decision to short-circuit the normal flow.
// It can contain either a response (success short-circuit), a stream (streaming short-circuit), or an error (error short-circuit).
// Streams are not supported in WASM plugins.
type LLMPluginShortCircuit struct {
	Response *BifrostResponse // If set, short-circuit with this response (skips provider call)
	Error    *BifrostError    // If set, short-circuit with this error (can set AllowFallbacks field)
}

// PluginShortCircuit is the legacy name for LLMPluginShortCircuit (v1.3.x compatibility).
// Deprecated: Use LLMPluginShortCircuit instead.
type PluginShortCircuit = LLMPluginShortCircuit
