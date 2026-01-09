//go:build tinygo || wasm

package schemas

// isNonCancellingContext returns true if the context is known to have
// a Done() channel that never closes. In wasm builds, fasthttp is not
// available, so this always returns false.
func isNonCancellingContext(parent any) bool {
	return false
}
