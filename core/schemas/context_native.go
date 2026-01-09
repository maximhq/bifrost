//go:build !tinygo && !wasm

package schemas

import "github.com/valyala/fasthttp"

// isNonCancellingContext returns true if the context is known to have
// a Done() channel that never closes (e.g., fasthttp.RequestCtx).
func isNonCancellingContext(parent any) bool {
	_, ok := parent.(*fasthttp.RequestCtx)
	return ok
}
