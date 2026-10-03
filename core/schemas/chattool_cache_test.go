package schemas

import (
	"bytes"
	"encoding/json"
	"testing"
)

const cacheTestToolJSON = `{
  "type": "function",
  "function": {
    "name": "get_weather",
    "description": "Get the weather for a location",
    "parameters": {
      "type": "object",
      "properties": {
        "location": {"type": "string", "description": "City and state, e.g. <SF>, CA & more"},
        "unit": {"type": "string", "enum": ["celsius", "fahrenheit"], "description": "Temperature unit"},
        "days": {"type": "array", "items": {"type": "integer"}, "description": "Forecast days"}
      },
      "required": ["location"],
      "additionalProperties": false
    }
  }
}`

func mustTool(t *testing.T) ChatTool {
	t.Helper()
	var tool ChatTool
	if err := Unmarshal([]byte(cacheTestToolJSON), &tool); err != nil {
		t.Fatalf("unmarshal tool: %v", err)
	}
	return tool
}

// TestChatTool_SerializedCache_ByteIdentical pins that a precomputed (cached)
// tool marshals byte-for-byte the same as the uncached tool — the safety net for
// the MCP source-serialization optimization.
func TestChatTool_SerializedCache_ByteIdentical(t *testing.T) {
	fresh, err := mustTool(t).MarshalJSON() // uncached path (serialized == nil)
	if err != nil {
		t.Fatalf("fresh marshal: %v", err)
	}

	cached := mustTool(t)
	if err := cached.EnsureSerialized(); err != nil {
		t.Fatalf("EnsureSerialized: %v", err)
	}
	if len(cached.serialized) == 0 {
		t.Fatal("EnsureSerialized did not populate the cache")
	}
	got, err := cached.MarshalJSON() // cache-hit path
	if err != nil {
		t.Fatalf("cached marshal: %v", err)
	}
	if !bytes.Equal(fresh, got) {
		t.Fatalf("cached != fresh\n fresh:  %s\n cached: %s", fresh, got)
	}

	// The log path marshals []ChatTool via sonic -> each element's MarshalJSON.
	freshArr, err := MarshalSorted([]ChatTool{mustTool(t)})
	if err != nil {
		t.Fatalf("fresh []ChatTool: %v", err)
	}
	cachedArr, err := MarshalSorted([]ChatTool{cached})
	if err != nil {
		t.Fatalf("cached []ChatTool: %v", err)
	}
	if !bytes.Equal(freshArr, cachedArr) {
		t.Fatalf("[]ChatTool cached != fresh\n fresh:  %s\n cached: %s", freshArr, cachedArr)
	}
}

// TestChatTool_EnsureSerialized_Idempotent pins that a second call is a no-op and
// keeps the same bytes.
func TestChatTool_EnsureSerialized_Idempotent(t *testing.T) {
	tool := mustTool(t)
	if err := tool.EnsureSerialized(); err != nil {
		t.Fatal(err)
	}
	first := tool.serialized
	if err := tool.EnsureSerialized(); err != nil {
		t.Fatal(err)
	}
	if &first[0] != &tool.serialized[0] {
		t.Fatal("second EnsureSerialized re-allocated; expected no-op")
	}
}

// TestChatTool_UncachedStillMarshals pins that client tools (never precomputed)
// marshal normally.
func TestChatTool_UncachedStillMarshals(t *testing.T) {
	tool := mustTool(t)
	if len(tool.serialized) != 0 {
		t.Fatal("fresh tool should have no cache")
	}
	b, err := tool.MarshalJSON()
	if err != nil || len(b) == 0 {
		t.Fatalf("uncached marshal failed: %v", err)
	}
}

// TestChatTool_InvalidateSerialized_AfterRename pins the cache-invalidation
// contract that the MCP client-rename path (MCPManager.UpdateClient) relies on:
// mutating Function.Name must be followed by InvalidateSerialized, or the stale
// cache keeps emitting the old name.
func TestChatTool_InvalidateSerialized_AfterRename(t *testing.T) {
	tool := mustTool(t)
	if err := tool.EnsureSerialized(); err != nil {
		t.Fatalf("EnsureSerialized: %v", err)
	}

	// Rename without invalidating: the cache is now stale and MarshalJSON keeps
	// emitting the old name — this is exactly the bug the fix prevents.
	tool.Function.Name = "renamed_weather"
	stale, err := tool.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal after rename: %v", err)
	}
	if !bytes.Contains(stale, []byte(`"get_weather"`)) || bytes.Contains(stale, []byte(`"renamed_weather"`)) {
		t.Fatalf("expected stale cache to still emit the old name; got %s", stale)
	}

	// Invalidate + recompute: MarshalJSON must now use the new name.
	tool.InvalidateSerialized()
	if err := tool.EnsureSerialized(); err != nil {
		t.Fatalf("EnsureSerialized after invalidate: %v", err)
	}
	fresh, err := tool.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal after invalidate: %v", err)
	}
	if !bytes.Contains(fresh, []byte(`"renamed_weather"`)) || bytes.Contains(fresh, []byte(`"get_weather"`)) {
		t.Fatalf("expected re-serialized tool to use the new name; got %s", fresh)
	}
}

func TestChatTool_MCPMetadataExcludedFromProviderCache(t *testing.T) {
	tool := mustTool(t)
	before, err := MarshalSorted(tool)
	if err != nil {
		t.Fatal(err)
	}
	tool.MCPToolSchema = &MCPToolSchema{InputSchema: []byte(`{"x-original":true}`), OutputSchema: []byte(`{}`)}
	tool.Annotations = &MCPToolAnnotations{ReadOnlyHint: Ptr(true)}
	metadata := tool.MCPToolSchema.Clone()
	annotations := tool.Annotations.Clone()
	metadata.InputSchema[0] = '['
	*annotations.ReadOnlyHint = false
	if tool.MCPToolSchema.InputSchema[0] != '{' || !*tool.Annotations.ReadOnlyHint {
		t.Fatal("MCP metadata clone aliases original")
	}
	after, err := MarshalSorted(tool)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("MCP metadata leaked to provider JSON")
	}
	if err := tool.EnsureSerialized(); err != nil {
		t.Fatal(err)
	}
	cached, err := MarshalSorted(tool)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, cached) {
		t.Fatal("MCP metadata leaked to cached provider JSON")
	}
}

func TestChatTool_MCPPersistenceOwnsMetadataAndPreservesProviderCache(t *testing.T) {
	tool := mustTool(t)
	tool.MCPToolSchema = &MCPToolSchema{InputSchema: []byte(`{"maximum":9007199254740993}`), OutputSchema: []byte(`{}`)}
	tool.Annotations = &MCPToolAnnotations{ReadOnlyHint: Ptr(true)}
	if err := tool.EnsureSerialized(); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := MarshalMCPDiscoveredTools(map[string]ChatTool{"weather": tool})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stored, []byte("9007199254740993")) {
		t.Fatal("stored numeric schema bound lost precision")
	}
	got, err := UnmarshalMCPDiscoveredTools(stored)
	if err != nil {
		t.Fatal(err)
	}
	restored := got["weather"]
	after, err := json.Marshal(restored)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("provider projection changed: %s %v", after, err)
	}
	restored.MCPToolSchema.InputSchema[0] = '['
	*restored.Annotations.ReadOnlyHint = false
	if tool.MCPToolSchema.InputSchema[0] != '{' || !*tool.Annotations.ReadOnlyHint {
		t.Fatal("restored metadata aliases original")
	}
	if storedAgain, err := MarshalMCPDiscoveredTools(map[string]ChatTool{"weather": tool}); err != nil || !bytes.Equal(stored, storedAgain) {
		t.Fatal("MCP storage mutated or nondeterministic")
	}
	if after, err := json.Marshal(tool); err != nil || !bytes.Equal(before, after) {
		t.Fatal("storage changed serialized provider cache")
	}
}
