package schemas

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestKeyAliasesUnmarshalLegacyStringShape(t *testing.T) {
	in := []byte(`{"best-model": "gpt-4o-deployment"}`)
	var ka KeyAliases
	if err := json.Unmarshal(in, &ka); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := KeyAliases{"best-model": AliasConfig{ModelID: "gpt-4o-deployment"}}
	if !reflect.DeepEqual(ka, want) {
		t.Fatalf("legacy shape mismatch: got %+v, want %+v", ka, want)
	}
}

func TestKeyAliasesUnmarshalRichShape(t *testing.T) {
	// Provider sub-configs are embedded, so their fields appear at the top level of the JSON.
	in := []byte(`{
        "best-model": {
            "model_id": "azure-deployment-xyz",
            "model_name": "claude-3-5-sonnet",
            "model_family": "anthropic",
            "description": "prod",
            "api_version": "2024-08-01-preview"
        }
    }`)
	var ka KeyAliases
	if err := json.Unmarshal(in, &ka); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := ka["best-model"]
	if got.ModelID != "azure-deployment-xyz" {
		t.Fatalf("ModelID mismatch: %q", got.ModelID)
	}
	if got.ModelName == nil || *got.ModelName != "claude-3-5-sonnet" {
		t.Fatalf("ModelName mismatch: %+v", got.ModelName)
	}
	if got.ModelFamily == nil || *got.ModelFamily != ModelFamilyAnthropic {
		t.Fatalf("ModelFamily mismatch: %+v", got.ModelFamily)
	}
	if got.Description != "prod" {
		t.Fatalf("Description mismatch: %q", got.Description)
	}
	if got.AzureAliasCfg == nil || got.APIVersion == nil || *got.APIVersion != "2024-08-01-preview" {
		t.Fatalf("AzureAliasCfg.APIVersion mismatch: %+v", got.AzureAliasCfg)
	}
}

func TestKeyAliasesUnmarshalMixedShape(t *testing.T) {
	in := []byte(`{
        "legacy":  "gpt-4-deployment",
        "rich":    {"model_id": "azure-xyz", "model_family": "openai"}
    }`)
	var ka KeyAliases
	if err := json.Unmarshal(in, &ka); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := ka["legacy"]; got.ModelID != "gpt-4-deployment" || got.ModelFamily != nil {
		t.Fatalf("legacy entry wrong: %+v", got)
	}
	got := ka["rich"]
	if got.ModelID != "azure-xyz" || got.ModelFamily == nil || *got.ModelFamily != ModelFamilyOpenAI {
		t.Fatalf("rich entry wrong: %+v", got)
	}
}

func TestKeyAliasesUnmarshalEmptyAndNull(t *testing.T) {
	cases := map[string]string{
		"empty-obj": `{}`,
		"null":      `null`,
	}
	for name, in := range cases {
		var ka KeyAliases
		if err := json.Unmarshal([]byte(in), &ka); err != nil {
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		if len(ka) != 0 {
			t.Fatalf("%s: want empty/nil, got %+v", name, ka)
		}
	}
}

func TestKeyAliasesUnmarshalRoundTrip(t *testing.T) {
	orig := KeyAliases{
		"best-model": AliasConfig{
			ModelID:     "azure-xyz",
			ModelName:   Ptr("claude-3-5-sonnet"),
			ModelFamily: Ptr(ModelFamilyAnthropic),
			AzureAliasCfg: &AzureAliasCfg{
				APIVersion: Ptr("2024-08-01-preview"),
			},
		},
		"simple": AliasConfig{ModelID: "gpt-4"},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back KeyAliases
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(orig, back) {
		t.Fatalf("round-trip mismatch:\nwant: %+v\ngot:  %+v", orig, back)
	}
}

func TestKeyAliasesMarshalLegacyShapeWhenOnlyModelIDSet(t *testing.T) {
	// Only ModelID populated — should serialize to the legacy string-valued shape.
	ka := KeyAliases{"best-model": AliasConfig{ModelID: "gpt-4o-deployment"}}
	data, err := json.Marshal(ka)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) != `{"best-model":"gpt-4o-deployment"}` {
		t.Fatalf("legacy shape mismatch: got %s", data)
	}
}

func TestKeyAliasesMarshalRichShapeWhenAnyExtraFieldSet(t *testing.T) {
	cases := map[string]AliasConfig{
		"with_model_name":   {ModelID: "x", ModelName: Ptr("canonical")},
		"with_model_family": {ModelID: "x", ModelFamily: Ptr(ModelFamilyAnthropic)},
		"with_description":  {ModelID: "x", Description: "prod"},
		"with_azure_subcfg": {ModelID: "x", AzureAliasCfg: &AzureAliasCfg{APIVersion: Ptr("2024-08-01-preview")}},
	}
	for name, ac := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(ac)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			// Rich shape should be a JSON object, not a string.
			if len(data) == 0 || data[0] != '{' {
				t.Fatalf("want object shape, got %s", data)
			}
		})
	}
}

func TestKeyAliasesMarshalUnmarshalLegacyRoundTrip(t *testing.T) {
	// Legacy in → legacy out: byte-for-byte stable for the unenriched case.
	in := []byte(`{"best-model":"gpt-4o-deployment"}`)
	var ka KeyAliases
	if err := json.Unmarshal(in, &ka); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(ka)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != string(in) {
		t.Fatalf("round-trip drift:\n in: %s\nout: %s", in, out)
	}
}

func TestKeyAliasesUnmarshalInvalidValueType(t *testing.T) {
	for _, in := range []string{
		`{"k": 123}`,
		`{"k": [1,2]}`,
		`{"k": true}`,
	} {
		var ka KeyAliases
		if err := json.Unmarshal([]byte(in), &ka); err == nil {
			t.Fatalf("expected error for %q, got nil", in)
		}
	}
}

func TestKeyAliasesResolveBackwardCompat(t *testing.T) {
	ka := KeyAliases{
		"best-model": AliasConfig{ModelID: "gpt-4o-deployment"},
	}
	if got := ka.Resolve("best-model"); got != "gpt-4o-deployment" {
		t.Fatalf("Resolve mismatch: %q", got)
	}
	if got := ka.Resolve("BEST-MODEL"); got != "gpt-4o-deployment" {
		t.Fatalf("Resolve case-insensitive fallback failed: %q", got)
	}
	if got := ka.Resolve("unmapped"); got != "unmapped" {
		t.Fatalf("Resolve unmatched mismatch: %q", got)
	}
	var nilKA KeyAliases
	if got := nilKA.Resolve("x"); got != "x" {
		t.Fatalf("nil Resolve mismatch: %q", got)
	}
}

func TestKeyAliasesResolveConfig(t *testing.T) {
	ka := KeyAliases{
		"best-model": AliasConfig{ModelID: "azure-xyz", ModelFamily: Ptr(ModelFamilyAnthropic)},
	}
	got := ka.ResolveConfig("best-model")
	if got == nil || got.ModelID != "azure-xyz" || got.ModelFamily == nil || *got.ModelFamily != ModelFamilyAnthropic {
		t.Fatalf("ResolveConfig mismatch: %+v", got)
	}
	if ka.ResolveConfig("unmapped") != nil {
		t.Fatalf("ResolveConfig should return nil for unmapped")
	}
}

func TestKeyAliasesValidate(t *testing.T) {
	madeUp := ModelFamily("made-up")
	cases := []struct {
		name    string
		ka      KeyAliases
		wantErr string
	}{
		{
			name: "ok",
			ka:   KeyAliases{"k": {ModelID: "v"}},
		},
		{
			name:    "empty source",
			ka:      KeyAliases{"": {ModelID: "v"}},
			wantErr: "alias source cannot be empty",
		},
		{
			name:    "empty model id",
			ka:      KeyAliases{"k": {ModelID: ""}},
			wantErr: "model_id cannot be empty",
		},
		{
			name:    "whitespace source",
			ka:      KeyAliases{" k ": {ModelID: "v"}},
			wantErr: "leading or trailing whitespace",
		},
		{
			name:    "invalid family",
			ka:      KeyAliases{"k": {ModelID: "v", ModelFamily: &madeUp}},
			wantErr: "invalid model_family",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.ka.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestModelFamilyIsValid(t *testing.T) {
	valid := []ModelFamily{
		ModelFamilyAnthropic, ModelFamilyOpenAI, ModelFamilyMistral,
		ModelFamilyCohere, ModelFamilyGemini, ModelFamilyNova, ModelFamilyTitan,
	}
	for _, mf := range valid {
		v := mf
		if !v.IsValid() {
			t.Fatalf("%q should be valid", mf)
		}
	}
	for _, mf := range []ModelFamily{"", "unknown", "claude"} {
		v := mf
		if v.IsValid() {
			t.Fatalf("%q should be invalid", mf)
		}
	}
	// nil receiver is invalid.
	var nilMF *ModelFamily
	if nilMF.IsValid() {
		t.Fatal("nil ModelFamily should be invalid")
	}
}
