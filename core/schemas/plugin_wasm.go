//go:build tinygo || wasm

package schemas

// PluginShortCircuit represents a plugin's decision to short-circuit the normal flow.
// It can contain either a response (success short-circuit), a stream (streaming short-circuit), or an error (error short-circuit).
type PluginShortCircuit struct {
	Response *BifrostResponse // If set, short-circuit with this response (skips provider call)
	Error    *BifrostError    // If set, short-circuit with this error (can set AllowFallbacks field)
}
