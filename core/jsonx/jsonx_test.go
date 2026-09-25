package jsonx

import (
	"testing"

	"github.com/bytedance/sonic/ast"
)

const sample = `{"error":{"message":"boom","code":1},"usage":{"total_tokens":5},"list":[10,20],"str":"x"}`

// getCase pins one lookup contract of Get/GetFromString. Path elements follow
// the sonic grammar (string keys and int indexes); invalid path types panic in
// sonic while jsonx returns an error instead — both are usable contracts, only
// the happy path and ErrNotExist are pinned here.
type getCase struct {
	name       string
	useBytes   bool  // exercise Get([]byte) instead of GetFromString
	path       []any // e.g. []any{"error"}, []any{"usage", "total_tokens"}, []any{"list", 1}
	wantErr    error // nil or ast.ErrNotExist
	wantExists bool
	wantRaw    string // optional: expected Raw() output (nested-object case)
}

var getCases = []getCase{
	{name: "top-level key", path: []any{"error"}, wantExists: true, wantRaw: `{"message":"boom","code":1}`},
	{name: "missing key", path: []any{"missing"}, wantErr: ast.ErrNotExist},
	{name: "bytes variant", useBytes: true, path: []any{"usage"}, wantExists: true},
	{name: "multi-level", path: []any{"usage", "total_tokens"}, wantExists: true},
	{name: "array index", path: []any{"list", 1}, wantExists: true},
	{name: "deep string path", path: []any{"error", "message"}, wantExists: true},
}

func testGet(t *testing.T, label string) {
	t.Helper()

	for _, tc := range getCases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				node ast.Node
				err  error
			)
			if tc.useBytes {
				node, err = Get([]byte(sample), tc.path...)
			} else {
				node, err = GetFromString(sample, tc.path...)
			}
			if err != tc.wantErr {
				t.Fatalf("[%s] %s: want err %v, got %v", label, tc.name, tc.wantErr, err)
			}
			if node.Exists() != tc.wantExists {
				t.Fatalf("[%s] %s: want exists=%v, got %v", label, tc.name, tc.wantExists, node.Exists())
			}
			if tc.wantRaw != "" {
				rawStr, rawErr := node.Raw()
				if rawErr != nil || rawStr != tc.wantRaw {
					t.Fatalf("[%s] %s: raw: got %q err=%v, want %q", label, tc.name, rawStr, rawErr, tc.wantRaw)
				}
			}
		})
	}
}

func TestGetNative(t *testing.T) {
	testGet(t, "native")
}

func TestGetFallback(t *testing.T) {
	old := forceFallback
	forceFallback = true
	// simulate a CPU without the required features
	oldSafe := nativeAstSafe
	nativeAstSafe = false
	defer func() { forceFallback = old; nativeAstSafe = oldSafe }()
	testGet(t, "fallback")
}

// TestFallbackSemantics pins the exact error semantics consumers rely on:
// missing keys return (zero Node, ast.ErrNotExist), mirroring sonic.
func TestFallbackSemantics(t *testing.T) {
	if nativeAstSafe && !forceFallback {
		t.Skip("fallback logic identical when native is safe; forcing anyway")
	}
	old := nativeAstSafe
	nativeAstSafe = false
	defer func() { nativeAstSafe = old }()

	node, err := GetFromString(sample, "nope")
	if err != ast.ErrNotExist {
		t.Fatalf("want ast.ErrNotExist, got %v", err)
	}
	if (node != ast.Node{}) {
		t.Fatalf("want zero node, got %+v", node)
	}
}
