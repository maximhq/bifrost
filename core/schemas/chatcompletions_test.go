package schemas

import (
	"encoding/json"
	"strings"
	"testing"
)

// ChatParameters' reasoning union accepts clients that mirror the same
// directive in both spellings (flat reasoning_* shorthand plus the reasoning
// object) and canonicalizes to the object form, while still rejecting
// contradictory values. Agents built on ai-sdk are known to emit every vendor
// dialect at once — reasoning_effort and reasoning.effort carrying the same
// value — and the previous "both present" rejection 400'd those requests
// before routing.
func TestChatParametersReasoningUnion(t *testing.T) {
	t.Run("duplicate spellings with equal values are accepted", func(t *testing.T) {
		var cp ChatParameters
		err := Unmarshal([]byte(`{"reasoning_effort":"high","reasoning":{"effort":"high"}}`), &cp)
		if err != nil {
			t.Fatalf("equal duplicate effort should decode, got %v", err)
		}
		if cp.Reasoning == nil || cp.Reasoning.Effort == nil || *cp.Reasoning.Effort != "high" {
			t.Fatalf("effort should canonicalize onto the reasoning object, got %+v", cp.Reasoning)
		}

		// Re-encoding must carry exactly one spelling so the union invariant
		// still holds on the wire.
		out, err := json.Marshal(&cp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(out), "reasoning_effort") {
			t.Fatalf("marshalled payload should drop the shorthand, got %s", out)
		}
	})

	t.Run("conflicting effort values are rejected", func(t *testing.T) {
		var cp ChatParameters
		err := Unmarshal([]byte(`{"reasoning_effort":"high","reasoning":{"effort":"max"}}`), &cp)
		if err == nil {
			t.Fatal("conflicting effort values should error")
		}
		if !strings.Contains(err.Error(), "conflicts with reasoning.effort") {
			t.Fatalf("error should name the conflict, got %v", err)
		}
	})

	t.Run("shorthand and object max_tokens agree", func(t *testing.T) {
		var cp ChatParameters
		err := Unmarshal([]byte(`{"reasoning_max_tokens":2048,"reasoning":{"max_tokens":2048}}`), &cp)
		if err != nil {
			t.Fatalf("equal duplicate max_tokens should decode, got %v", err)
		}
		if cp.Reasoning == nil || cp.Reasoning.MaxTokens == nil || *cp.Reasoning.MaxTokens != 2048 {
			t.Fatalf("max_tokens should canonicalize onto the reasoning object, got %+v", cp.Reasoning)
		}
	})

	t.Run("conflicting max_tokens values are rejected", func(t *testing.T) {
		var cp ChatParameters
		err := Unmarshal([]byte(`{"reasoning_max_tokens":2048,"reasoning":{"max_tokens":4096}}`), &cp)
		if err == nil {
			t.Fatal("conflicting max_tokens values should error")
		}
	})

	t.Run("duplicate display agrees, conflict rejected", func(t *testing.T) {
		var cp ChatParameters
		if err := Unmarshal([]byte(`{"reasoning_display":"summarized","reasoning":{"display":"summarized"}}`), &cp); err != nil {
			t.Fatalf("equal duplicate display should decode, got %v", err)
		}
		if err := Unmarshal([]byte(`{"reasoning_display":"summarized","reasoning":{"display":"omitted"}}`), &cp); err == nil {
			t.Fatal("conflicting display values should error")
		}
	})

	t.Run("single-spelling requests keep working", func(t *testing.T) {
		var shorthand, object ChatParameters
		if err := Unmarshal([]byte(`{"reasoning_effort":"low"}`), &shorthand); err != nil {
			t.Fatalf("shorthand-only decode: %v", err)
		}
		if err := Unmarshal([]byte(`{"reasoning":{"effort":"low"}}`), &object); err != nil {
			t.Fatalf("object-only decode: %v", err)
		}
		if shorthand.Reasoning == nil || object.Reasoning == nil ||
			*shorthand.Reasoning.Effort != "low" || *object.Reasoning.Effort != "low" {
			t.Fatal("both single-spelling forms should decode to the same canonical state")
		}
	})
}
