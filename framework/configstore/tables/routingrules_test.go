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

// TestRoutingFallback_ResolvedReparsesLegacyString covers #7538: rules load before bifrost.Init
// registers custom providers, so a legacy string must be re-parsed when it is routed on.
func TestRoutingFallback_ResolvedReparsesLegacyString(t *testing.T) {
	const custom = schemas.ModelProvider("custom-resolved-7538")
	schemas.UnregisterKnownProvider(custom)
	t.Cleanup(func() { schemas.UnregisterKnownProvider(custom) })

	var decoded []RoutingFallback
	input := `["` + string(custom) + `/m","` + string(custom) + `/","unknown-prefix/m",{"provider":"vertex","model":"gemini-2.5-pro","key_id":"k-1"}]`
	if err := sonic.Unmarshal([]byte(input), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded[0].Provider != "" {
		t.Fatalf("precondition: an unregistered prefix must not split at decode time, got provider=%q", decoded[0].Provider)
	}

	schemas.RegisterKnownProvider(custom)

	want := []schemas.Fallback{
		{Provider: custom, Model: "m"},
		{Provider: custom, Model: ""},
		{Provider: "", Model: "unknown-prefix/m"},
		{Provider: "vertex", Model: "gemini-2.5-pro", KeyID: "k-1"},
	}
	for i, fb := range decoded {
		if got := fb.Resolved(); got != want[i] {
			t.Fatalf("fallback %d: got %+v, want %+v", i, got, want[i])
		}
	}

	out, err := sonic.Marshal(decoded)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	wantOut := `["` + string(custom) + `/m","` + string(custom) + `/","unknown-prefix/m",{"provider":"vertex","model":"gemini-2.5-pro","key_id":"k-1"}]`
	if string(out) != wantOut {
		t.Fatalf("resolving must not change the stored form: got %s, want %s", out, wantOut)
	}
}

// TestRoutingFallback_ObjectFormTrimsFieldsAcrossRestart: an unpinned object is persisted as the
// legacy string, so padding kept at decode time would become an unknown provider prefix after a
// restart and silently drop the fallback (the same symptom as #7538).
func TestRoutingFallback_ObjectFormTrimsFieldsAcrossRestart(t *testing.T) {
	var decoded []RoutingFallback
	if err := sonic.Unmarshal([]byte(`[{"provider":" openai ","model":" gpt-4o "},{"provider":" azure ","model":" gpt-4o ","key_id":" k-1 "}]`), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	stored, err := sonic.Marshal(decoded)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var reloaded []RoutingFallback
	if err := sonic.Unmarshal(stored, &reloaded); err != nil {
		t.Fatalf("reload: %v", err)
	}
	want := []schemas.Fallback{
		{Provider: "openai", Model: "gpt-4o"},
		{Provider: "azure", Model: "gpt-4o", KeyID: "k-1"},
	}
	for i, fb := range reloaded {
		if got := fb.Resolved(); got != want[i] {
			t.Fatalf("fallback %d after restart: got %+v, want %+v (stored %s)", i, got, want[i], stored)
		}
	}
}

// TestRoutingFallback_MalformedEntries pins how entries that are neither a string nor a valid
// object decode: wrong types fail loudly (the API returns 400 and AfterFind surfaces the error),
// while null and unknown object fields decode to something the routing plugin already handles.
func TestRoutingFallback_MalformedEntries(t *testing.T) {
	for _, input := range []string{`[1]`, `[true]`, `[["openai/gpt-4o"]]`, `[{"provider":1}]`, `[{"key_id":["k"]}]`} {
		t.Run("rejects "+input, func(t *testing.T) {
			var decoded []RoutingFallback
			if err := sonic.Unmarshal([]byte(input), &decoded); err == nil {
				t.Fatalf("expected a decode error, got %+v", decoded)
			}
			rule := TableRoutingRule{Fallbacks: &input}
			if err := rule.AfterFind(nil); err == nil {
				t.Fatal("AfterFind must surface a malformed fallbacks column")
			}
		})
	}

	t.Run("null entry decodes to an empty fallback", func(t *testing.T) {
		var decoded []RoutingFallback
		if err := sonic.Unmarshal([]byte(`[null]`), &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(decoded) != 1 || decoded[0].Resolved() != (schemas.Fallback{}) {
			t.Fatalf("got %+v, want one empty fallback (dropped by the plugin for its empty provider)", decoded)
		}
	})

	t.Run("unknown object fields are ignored", func(t *testing.T) {
		var decoded []RoutingFallback
		if err := sonic.Unmarshal([]byte(`[{"provider":"vertex","model":"gemini-2.5-pro","key_id":"k1","weight":2}]`), &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		want := schemas.Fallback{Provider: "vertex", Model: "gemini-2.5-pro", KeyID: "k1"}
		if len(decoded) != 1 || decoded[0].Resolved() != want {
			t.Fatalf("got %+v, want %+v", decoded, want)
		}
	})

	t.Run("empty and whitespace columns decode to no fallbacks", func(t *testing.T) {
		for _, column := range []string{"", "   "} {
			rule := TableRoutingRule{Fallbacks: &column}
			if err := rule.AfterFind(nil); err != nil || len(rule.ParsedFallbacks) != 0 {
				t.Fatalf("column %q: err=%v fallbacks=%+v", column, err, rule.ParsedFallbacks)
			}
		}
	})
}
