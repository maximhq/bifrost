package tables

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestRoutingFallback_UnpinnedRoundTripsByteIdentically guards GenerateRoutingRuleHash: a changed byte shape rewrites every rule on the next boot.
func TestRoutingFallback_UnpinnedRoundTripsByteIdentically(t *testing.T) {
	cases := []string{
		`["openai/gpt-4o"]`,
		`["azure/"]`,
		`["anthropic"]`,
		`["openai/ft:gpt-4o:org::abc/v2"]`,
		`["meta-llama/Llama-3.1-8B"]`,
		`[]`,
		`["openai/gpt-4o","azure/","vertex/gemini-2.5-pro"]`,
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			var decoded []RoutingFallback
			if err := sonic.Unmarshal([]byte(input), &decoded); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			out, err := sonic.Marshal(decoded)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(out) != input {
				t.Fatalf("round-trip changed bytes: got %s, want %s", out, input)
			}
		})
	}
}

// TestRoutingFallback_ParsesLegacyString preserves the existing provider/model parser semantics.
func TestRoutingFallback_ParsesLegacyString(t *testing.T) {
	cases := []struct {
		input    string
		provider string
		model    string
	}{
		{`"openai/gpt-4o"`, "openai", "gpt-4o"},
		{`"azure/"`, "azure", ""},
		{`"anthropic"`, "", "anthropic"},
		{`"meta-llama/Llama-3.1-8B"`, "", "meta-llama/Llama-3.1-8B"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			var fb RoutingFallback
			if err := sonic.Unmarshal([]byte(tc.input), &fb); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if string(fb.Provider) != tc.provider || fb.Model != tc.model {
				t.Fatalf("got provider=%q model=%q, want provider=%q model=%q", fb.Provider, fb.Model, tc.provider, tc.model)
			}
			if fb.IsKeyPinned() {
				t.Fatal("legacy string must not be pinned")
			}
		})
	}
}

// TestRoutingFallback_ObjectFormKeepsKeyID preserves mixed pinned and legacy fallback chains.
func TestRoutingFallback_ObjectFormKeepsKeyID(t *testing.T) {
	input := `["openai/gpt-4o",{"provider":"azure","model":"gpt-4o","key_id":"k1"}]`
	var decoded []RoutingFallback
	if err := sonic.Unmarshal([]byte(input), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded) != 2 {
		t.Fatalf("got %d entries, want 2", len(decoded))
	}
	if decoded[0].IsKeyPinned() {
		t.Fatal("first entry must not be pinned")
	}
	if decoded[1].KeyID != "k1" || !decoded[1].IsKeyPinned() {
		t.Fatalf("second entry lost its key: %+v", decoded[1])
	}
	out, err := sonic.Marshal(decoded)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != input {
		t.Fatalf("mixed round-trip: got %s, want %s", out, input)
	}
}

// TestRoutingFallback_ProgrammaticMarshalsAsString covers entries built in code, which have no raw literal to replay.
func TestRoutingFallback_ProgrammaticMarshalsAsString(t *testing.T) {
	out, err := sonic.Marshal([]RoutingFallback{
		{Fallback: schemas.Fallback{Provider: "openai", Model: "gpt-4o"}},
		{Fallback: schemas.Fallback{Provider: "azure"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != `["openai/gpt-4o","azure/"]` {
		t.Fatalf("got %s", out)
	}
}

// TestRoutingFallback_ProviderOnlyObjectSurvivesPersistence keeps incoming-model fallbacks valid after serialization.
func TestRoutingFallback_ProviderOnlyObjectSurvivesPersistence(t *testing.T) {
	var fallback RoutingFallback
	if err := sonic.Unmarshal([]byte(`{"provider":"azure"}`), &fallback); err != nil {
		t.Fatal(err)
	}
	encoded, err := sonic.Marshal(fallback)
	if err != nil {
		t.Fatal(err)
	}
	var restored RoutingFallback
	if err := sonic.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Provider != "azure" || restored.Model != "" || restored.IsKeyPinned() {
		t.Fatalf("provider-only fallback changed after persistence: %+v (wire %s)", restored, encoded)
	}
}
