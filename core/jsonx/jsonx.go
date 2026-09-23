// Package jsonx wraps github.com/bytedance/sonic's Get/GetFromString helpers
// with a runtime CPU-feature guard and a pure-Go fallback.
//
// Why this exists: sonic's native SSE implementation of ast.GetByPath
// (internal/native/sse/get_by_path) is bound unconditionally on amd64 CPUs
// without AVX2 (see internal/native/dispatch_amd64.go, useSSE). That compiled
// C code executes PCLMULQDQ, which older CPUs (pre-Nehalem era and AMD Bobcat,
// e.g. AMD C-60) do not implement, causing a SIGILL crash on any call — see
// bifrost issue #5645. Unlike sonic's Unmarshal/Marshal (which fall back to
// encoding/json at runtime), ast.GetByPath has no such guard.
//
// All callers only ever search single top-level keys, so the fallback path
// walks the JSON with encoding/json and wraps the result with ast.NewRaw.
// The fast native path is kept for CPUs that support the instructions the
// SSE implementation requires (SSE4.2 + PCLMULQDQ, i.e. Nehalem 2008+).
package jsonx

import (
	"encoding/json"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
	"github.com/klauspost/cpuid/v2"
)

// nativeAstSafe reports whether the CPU implements the instructions sonic's
// non-AVX2 ("sse") native code path requires. The SSE build of get_by_path
// uses PCLMULQDQ; executing it on CPUs lacking it raises SIGILL (#5645).
var nativeAstSafe = cpuid.CPU.Has(cpuid.CLMUL) && cpuid.CPU.Has(cpuid.SSE42)

// forceFallback is overridden in tests to exercise the pure-Go path everywhere.
var forceFallback = false

// Get searches src for the given path, like sonic.Get, but falls back to a
// pure-Go implementation on CPUs whose feature set the sonic native code
// assumes but does not verify (SSE4.2 / PCLMULQDQ on amd64).
func Get(src []byte, path ...interface{}) (ast.Node, error) {
	if nativeAstSafe && !forceFallback {
		return sonic.Get(src, path...)
	}
	return goGet(string(src), path...)
}

// GetFromString searches src for the given path, like sonic.GetFromString,
// but falls back to a pure-Go implementation on CPUs whose feature set the
// sonic native SSE code does not support (no PCLMULQDQ/SSE4.2). Avoids the
// SIGILL crash reported in #5645.
func GetFromString(src string, path ...interface{}) (ast.Node, error) {
	if nativeAstSafe && !forceFallback {
		return sonic.GetFromString(src, path...)
	}
	return goGet(src, path...)
}

// goGet walks path over encoding/json's RawMessage tree. Supports the same
// path grammar as sonic (string keys and int indexes) for the subset used by
// bifrost: single- or multi-level lookups returning a raw node.
func goGet(src string, path ...interface{}) (ast.Node, error) {
	cur := json.RawMessage(src)
	for _, p := range path {
		switch k := p.(type) {
		case string:
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(cur, &obj); err != nil {
				return ast.Node{}, err
			}
			raw, ok := obj[k]
			if !ok {
				return ast.Node{}, ast.ErrNotExist
			}
			cur = raw
		case int:
			var arr []json.RawMessage
			if err := json.Unmarshal(cur, &arr); err != nil {
				return ast.Node{}, err
			}
			if k < 0 || k >= len(arr) {
				return ast.Node{}, ast.ErrNotExist
			}
			cur = arr[k]
		default:
			return ast.Node{}, fmt.Errorf("%w: path must be either int(>=0) or string", ast.ErrNotExist)
		}
		if cur == nil {
			return ast.Node{}, ast.ErrNotExist
		}
	}
	return ast.NewRaw(string(cur)), nil
}
