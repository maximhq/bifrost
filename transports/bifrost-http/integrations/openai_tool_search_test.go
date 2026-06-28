package integrations

import (
	"strings"
	"testing"
)

func TestNormalizeOpenAIResponsesRawResponseToolSearchArguments(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"object":"response","created_at":0,"output":[{"type":"tool_search_call","status":"completed","call_id":"call_sdWGbJEy2EfdVPce4Es1J62h","arguments":"{\"query\":\"codexself tools\",\"limit\":20}"}]}`)
	normalized := normalizeOpenAIResponsesRawResponse(raw)
	normalizedBytes, ok := normalized.([]byte)
	if !ok {
		t.Fatalf("expected []byte normalized response, got %T", normalized)
	}
	encoded := string(normalizedBytes)

	if !strings.Contains(encoded, `"type":"tool_search_call"`) {
		t.Fatalf("expected tool_search_call item, got %s", encoded)
	}
	if !strings.Contains(encoded, `"arguments":{"query":"codexself tools","limit":20}`) {
		t.Fatalf("expected object-typed arguments, got %s", encoded)
	}
	if strings.Contains(encoded, `"arguments":"{\"query\":\"codexself tools\",\"limit\":20}"`) {
		t.Fatalf("expected arguments not to remain string-encoded, got %s", encoded)
	}
}

func TestNormalizeOpenAIResponsesRawStreamResponseToolSearchArguments(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"type":"response.output_item.done","sequence_number":5,"output_index":1,"item":{"type":"tool_search_call","status":"completed","call_id":"call_sdWGbJEy2EfdVPce4Es1J62h","arguments":"{\"query\":\"codexself tools\",\"limit\":20}"}}`)
	normalized := normalizeOpenAIResponsesRawStreamResponse(raw)
	normalizedBytes, ok := normalized.([]byte)
	if !ok {
		t.Fatalf("expected []byte normalized stream response, got %T", normalized)
	}
	encoded := string(normalizedBytes)

	if !strings.Contains(encoded, `"type":"tool_search_call"`) {
		t.Fatalf("expected tool_search_call item, got %s", encoded)
	}
	if !strings.Contains(encoded, `"arguments":{"query":"codexself tools","limit":20}`) {
		t.Fatalf("expected object-typed arguments, got %s", encoded)
	}
	if strings.Contains(encoded, `"arguments":"{\"query\":\"codexself tools\",\"limit\":20}"`) {
		t.Fatalf("expected arguments not to remain string-encoded, got %s", encoded)
	}
}
