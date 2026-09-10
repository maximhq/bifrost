package bedrock

import (
	"testing"
)

// TestBareModelScope pins the contract core relies on: within the scope the request
// names the bare id mantle knows, and after it the caller's request is byte-for-byte
// what it was, so a retry from core still carries its region prefix. The raw body
// matters because passthrough sends it verbatim.
func TestBareModelScope(t *testing.T) {
	t.Run("prefixed model and raw body are rewritten, then restored", func(t *testing.T) {
		model := "us-west-2/openai.gpt-6-astra"
		raw := []byte(`{"model":"us-west-2/openai.gpt-6-astra","messages":[]}`)
		origRaw := raw

		restore := bareModelScope(&model, &raw)
		if model != "openai.gpt-6-astra" {
			t.Fatalf("model in scope = %q, want bare id", model)
		}
		if got := string(raw); got != `{"model":"openai.gpt-6-astra","messages":[]}` {
			t.Fatalf("raw body in scope = %s", got)
		}

		restore()
		if model != "us-west-2/openai.gpt-6-astra" {
			t.Fatalf("model after restore = %q, want prefix back", model)
		}
		if string(raw) != string(origRaw) {
			t.Fatalf("raw body after restore = %s, want original", raw)
		}
	})

	t.Run("no prefix is a no-op that leaves the raw body untouched", func(t *testing.T) {
		model := "openai.gpt-6-astra"
		raw := []byte(`{"model":"openai.gpt-6-astra"}`)
		before := raw
		restore := bareModelScope(&model, &raw)
		if model != "openai.gpt-6-astra" || string(raw) != string(before) {
			t.Fatalf("no-prefix scope changed the request: model=%q raw=%s", model, raw)
		}
		restore()
	})

	t.Run("prefixed model with no raw body", func(t *testing.T) {
		model := "us-west-2/openai.gpt-6-astra"
		var raw []byte
		restore := bareModelScope(&model, &raw)
		if model != "openai.gpt-6-astra" || raw != nil {
			t.Fatalf("scope: model=%q raw=%v", model, raw)
		}
		restore()
		if model != "us-west-2/openai.gpt-6-astra" || raw != nil {
			t.Fatalf("restore: model=%q raw=%v", model, raw)
		}
	})
}
