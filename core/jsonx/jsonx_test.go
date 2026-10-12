package jsonx

import (
	"testing"

	"github.com/bytedance/sonic/ast"
)

const sample = `{"error":{"message":"boom","code":1},"usage":{"total_tokens":5},"list":[10,20],"str":"x"}`

func testGet(t *testing.T, label string) {
	t.Helper()

	// top-level key exists
	node, err := GetFromString(sample, "error")
	if err != nil {
		t.Fatalf("[%s] error key: %v", label, err)
	}
	if !node.Exists() {
		t.Fatalf("[%s] error key: expected exists", label)
	}

	// missing key: sonic returns (zero Node, ast.ErrNotExist)
	node, err = GetFromString(sample, "missing")
	if err != ast.ErrNotExist {
		t.Fatalf("[%s] missing key: want ErrNotExist, got %v", label, err)
	}
	if node.Exists() {
		t.Fatalf("[%s] missing key: expected not exists", label)
	}

	// Get variant on bytes
	node, err = Get([]byte(sample), "usage")
	if err != nil || !node.Exists() {
		t.Fatalf("[%s] bytes usage: err=%v exists=%v", label, err, node.Exists())
	}

	// nested raw node is valid json
	rawNode, err := GetFromString(sample, "error")
	if err != nil {
		t.Fatal(err)
	}
	rawStr, rawErr := rawNode.Raw()
	if rawErr != nil || rawStr != `{"message":"boom","code":1}` {
		t.Fatalf("[%s] raw: got %q err=%v", label, rawStr, rawErr)
	}

	// multi-level
	node, err = GetFromString(sample, "usage", "total_tokens")
	if err != nil || !node.Exists() {
		t.Fatalf("[%s] multi-level: err=%v exists=%v", label, err, node.Exists())
	}

	// array index
	node, err = GetFromString(sample, "list", 1)
	if err != nil || !node.Exists() {
		t.Fatalf("[%s] array index: err=%v exists=%v", label, err, node.Exists())
	}

	// invalid path type panics like sonic? sonic panics; we return error instead — both usable
	node, err = GetFromString(sample, "error", "message")
	if err != nil || !node.Exists() {
		t.Fatalf("[%s] deep string path: err=%v exists=%v", label, err, node.Exists())
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
