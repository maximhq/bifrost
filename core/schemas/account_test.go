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

// TestKeyAliasesProjectIDFlatRoundTrip verifies the per-alias project_id override is a single
// top-level field: it deserializes from the flat {"model_id":..., "project_id":{...}} shape,
// round-trips byte-stably, and (critically) is NOT dropped by an embedded-field name collision.
// project_id lives on AliasConfig itself (like region) rather than inside each provider sub-config
// precisely so multiple same-depth promoted fields don't silently cancel each other out.
func TestKeyAliasesProjectIDFlatRoundTrip(t *testing.T) {
	in := []byte(`{"chirp-alias":{"model_id":"chirp","project_id":{"value":"proj_elvsngya7ixv4dkb26xe","type":"plain_text"}}}`)
	var ka KeyAliases
	if err := json.Unmarshal(in, &ka); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := ka["chirp-alias"]
	if got.ModelID != "chirp" {
		t.Fatalf("ModelID mismatch: %q", got.ModelID)
	}
	if got.ProjectID == nil {
		t.Fatalf("ProjectID was dropped on unmarshal (embedded field collision regression?)")
	}
	if v := got.ProjectID.GetValue(); v != "proj_elvsngya7ixv4dkb26xe" {
		t.Fatalf("ProjectID value mismatch: %q", v)
	}
	out, err := json.Marshal(ka)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Re-unmarshal and compare structurally (map key order in the SecretVar object is not stable).
	var back KeyAliases
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if bp := back["chirp-alias"].ProjectID; bp == nil || bp.GetValue() != "proj_elvsngya7ixv4dkb26xe" {
		t.Fatalf("ProjectID did not survive marshal round-trip: %s", out)
	}
	// project_id alone (besides model_id) must force the rich object shape, not the legacy string.
	if len(out) == 0 || !strings.Contains(string(out), `"project_id"`) {
		t.Fatalf("expected project_id in serialized output, got %s", out)
	}
}

// TestKeyAliasesLegacyVertexProjectIDPromotedOnMarshal guards the back-compat
// shim: a Go-constructed alias that sets only the deprecated VertexAliasCfg.ProjectID
// (json:"-") must not lose its project on a marshal/unmarshal round-trip. MarshalJSON
// promotes it to the top-level project_id so it survives persistence and the config hash.
func TestKeyAliasesLegacyVertexProjectIDPromotedOnMarshal(t *testing.T) {
	ka := KeyAliases{"vtx-alias": {
		ModelID:        "gemini-2.0-flash-001",
		VertexAliasCfg: &VertexAliasCfg{ProjectID: NewSecretVar("legacy-gcp-project")},
	}}
	out, err := json.Marshal(ka)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"project_id"`) {
		t.Fatalf("expected legacy VertexAliasCfg.ProjectID promoted to top-level project_id, got %s", out)
	}
	var back KeyAliases
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	bp := back["vtx-alias"].ProjectID
	if bp == nil || bp.GetValue() != "legacy-gcp-project" {
		t.Fatalf("legacy Vertex project did not survive round-trip: %s", out)
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
	cases := map[string]struct {
		ac        AliasConfig
		wantKey   string
		wantValue any
	}{
		"with_model_name":   {AliasConfig{ModelID: "x", ModelName: Ptr("canonical")}, "model_name", "canonical"},
		"with_model_family": {AliasConfig{ModelID: "x", ModelFamily: Ptr(ModelFamilyAnthropic)}, "model_family", "anthropic"},
		"with_description":  {AliasConfig{ModelID: "x", Description: "prod"}, "description", "prod"},
		"with_azure_subcfg": {AliasConfig{ModelID: "x", AzureAliasCfg: &AzureAliasCfg{APIVersion: Ptr("2024-08-01-preview")}}, "api_version", "2024-08-01-preview"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(c.ac)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			// Rich shape should be a JSON object, not a string.
			if len(data) == 0 || data[0] != '{' {
				t.Fatalf("want object shape, got %s", data)
			}
			var out map[string]any
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatalf("re-unmarshal: %v", err)
			}
			got, ok := out[c.wantKey]
			if !ok {
				t.Fatalf("expected key %q in serialized output, got %s", c.wantKey, data)
			}
			if got != c.wantValue {
				t.Fatalf("field %q: want %v, got %v (raw: %s)", c.wantKey, c.wantValue, got, data)
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
	azureCfg := &AzureAliasCfg{APIVersion: Ptr("2024-08-01-preview")}
	bedrockCfg := &BedrockAliasCfg{InferenceProfileARN: NewSecretVar("arn:aws:bedrock:...")}
	vertexCfg := &VertexAliasCfg{ProjectID: NewSecretVar("my-gcp-project")}
	replicateCfg := &ReplicateAliasCfg{UseDeploymentsEndpoint: Ptr(true)}
	cases := []struct {
		name     string
		provider ModelProvider
		ka       KeyAliases
		wantErr  string
	}{
		{
			name:     "ok",
			provider: OpenAI,
			ka:       KeyAliases{"k": {ModelID: "v"}},
		},
		{
			name:     "empty source",
			provider: OpenAI,
			ka:       KeyAliases{"": {ModelID: "v"}},
			wantErr:  "alias source cannot be empty",
		},
		{
			name:     "empty model id",
			provider: OpenAI,
			ka:       KeyAliases{"k": {ModelID: ""}},
			wantErr:  "model_id cannot be empty",
		},
		{
			name:     "whitespace source",
			provider: OpenAI,
			ka:       KeyAliases{" k ": {ModelID: "v"}},
			wantErr:  "leading or trailing whitespace",
		},
		{
			name:    "whitespace model_id",
			ka:      KeyAliases{"k": {ModelID: "v "}},
			wantErr: "model_id cannot have leading or trailing whitespace",
		},
		{
			name:    "whitespace model_name",
			ka:      KeyAliases{"k": {ModelID: "v", ModelName: Ptr(" canonical ")}},
			wantErr: "model_name cannot have leading or trailing whitespace",
		},
		{
			name:    "duplicate source case-insensitive",
			ka:      KeyAliases{"Key": {ModelID: "v"}, "key": {ModelID: "v"}},
			wantErr: "duplicate alias source",
		},
		{
			name:     "invalid family",
			provider: OpenAI,
			ka:       KeyAliases{"k": {ModelID: "v", ModelFamily: &madeUp}},
			wantErr:  "invalid model_family",
		},
		{
			name:     "azure sub-config on azure key — ok",
			provider: Azure,
			ka:       KeyAliases{"k": {ModelID: "v", AzureAliasCfg: azureCfg}},
		},
		{
			name:     "azure sub-config on non-azure key — error",
			provider: Bedrock,
			ka:       KeyAliases{"k": {ModelID: "v", AzureAliasCfg: azureCfg}},
			wantErr:  "azure sub-config is only valid on Azure keys",
		},
		{
			name:     "bedrock sub-config on bedrock key — ok",
			provider: Bedrock,
			ka:       KeyAliases{"k": {ModelID: "v", BedrockAliasCfg: bedrockCfg}},
		},
		{
			name:     "bedrock sub-config on azure key — error",
			provider: Azure,
			ka:       KeyAliases{"k": {ModelID: "v", BedrockAliasCfg: bedrockCfg}},
			wantErr:  "bedrock sub-config is only valid on Bedrock keys",
		},
		{
			name:     "vertex sub-config on vertex key — ok",
			provider: Vertex,
			ka:       KeyAliases{"k": {ModelID: "v", VertexAliasCfg: vertexCfg}},
		},
		{
			name:     "vertex sub-config on openai key — error",
			provider: OpenAI,
			ka:       KeyAliases{"k": {ModelID: "v", VertexAliasCfg: vertexCfg}},
			wantErr:  "vertex sub-config is only valid on Vertex keys",
		},
		{
			name:     "replicate sub-config on replicate key — ok",
			provider: Replicate,
			ka:       KeyAliases{"k": {ModelID: "v", ReplicateAliasCfg: replicateCfg}},
		},
		{
			name:     "replicate sub-config on bedrock key — error",
			provider: Bedrock,
			ka:       KeyAliases{"k": {ModelID: "v", ReplicateAliasCfg: replicateCfg}},
			wantErr:  "replicate sub-config is only valid on Replicate keys",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.ka.Validate(c.provider)
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

func TestResolveFamilyPrecedence(t *testing.T) {
	familyOpenAI := ModelFamilyOpenAI

	// Helper to build a BifrostContext carrying a ResolvedAlias.
	withAlias := func(ra *ResolvedAlias) *BifrostContext {
		bc := NewBifrostContext(nil, NoDeadline)
		if ra != nil {
			bc.SetValue(BifrostContextKeyResolvedAlias, ra)
		}
		return bc
	}

	cases := []struct {
		name     string
		ra       *ResolvedAlias
		fallback string
		want     ModelFamily
	}{
		{
			name: "tier 1: explicit ModelFamily wins over everything",
			ra: &ResolvedAlias{
				Key: "some-claude-name",
				Config: &AliasConfig{
					ModelID:     "opaque-id",
					ModelFamily: &familyOpenAI, // wins despite name/key smelling like Claude
				},
			},
			fallback: "claude-3-5-sonnet",
			want:     ModelFamilyOpenAI,
		},
		{
			name: "tier 2: ModelName substring when no explicit family",
			ra: &ResolvedAlias{
				Key: "best-model",
				Config: &AliasConfig{
					ModelID:   "opaque-id",
					ModelName: Ptr("claude-3-5-sonnet"),
				},
			},
			fallback: "opaque-id",
			want:     ModelFamilyAnthropic,
		},
		{
			name: "tier 3: ModelID substring when name absent",
			ra: &ResolvedAlias{
				Key: "best-model",
				Config: &AliasConfig{
					ModelID: "us.anthropic.claude-3-5-sonnet-20241022-v2:0",
				},
			},
			fallback: "best-model",
			want:     ModelFamilyAnthropic,
		},
		{
			name: "tier 4: alias key substring when nothing else hits — the legacy 'best-claude→opaque-deployment-id' case the refactor is specifically meant to fix",
			ra: &ResolvedAlias{
				Key: "best-claude",
				Config: &AliasConfig{
					ModelID: "12345-azure-deployment",
				},
			},
			fallback: "12345-azure-deployment",
			want:     ModelFamilyAnthropic,
		},
		{
			name:     "no alias matched: fall back to substring on fallbackModel — preserves pre-refactor behavior",
			ra:       nil,
			fallback: "claude-3-5-sonnet",
			want:     ModelFamilyAnthropic,
		},
		{
			name:     "no alias and no substring hit anywhere",
			ra:       nil,
			fallback: "totally-unknown-model",
			want:     "",
		},
		{
			name: "explicit empty ModelFamily pointer is treated as absent (falls through to name)",
			ra: &ResolvedAlias{
				Key: "x",
				Config: &AliasConfig{
					ModelID:     "opaque-id",
					ModelName:   Ptr("claude-3-5-sonnet"),
					ModelFamily: Ptr(ModelFamily("")),
				},
			},
			fallback: "x",
			want:     ModelFamilyAnthropic,
		},
		{
			name: "first matching candidate wins (ModelName matches Anthropic before ModelID could match anything else)",
			ra: &ResolvedAlias{
				Key: "x",
				Config: &AliasConfig{
					ModelID:   "mistral-large-2407", // would match Mistral but ModelName is checked first
					ModelName: Ptr("claude-3-5-sonnet"),
				},
			},
			fallback: "x",
			want:     ModelFamilyAnthropic,
		},
		{
			name: "uses fallback when ResolvedAlias.Config is nil (defensive)",
			ra:   &ResolvedAlias{Key: "x", Config: nil},
			// With Config==nil the candidates list is empty for the alias branch,
			// so we drop to fallback substring matching.
			fallback: "claude-3-haiku",
			want:     ModelFamilyAnthropic,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveFamily(withAlias(c.ra), c.fallback)
			if got != c.want {
				t.Fatalf("ResolveFamily: got %q, want %q", got, c.want)
			}
		})
	}
}

func TestIsOpenAIModel(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		// gpt / embeddings (pre-existing behavior)
		{"gpt-4o", true},
		{"gpt-5", true},
		{"text-embedding-3-large", true},
		// o-series reasoning families
		{"o1", true},
		{"o1-preview", true},
		{"o1-mini", true},
		{"o3", true},
		{"o3-mini", true},
		{"o4-mini", true},
		{"openai/o3", true},
		{"openai:o4-mini", true},
		// non-OpenAI / false-positive guards
		{"claude-3-5-sonnet", false},
		{"mistral-large-2407", false},
		{"co1", false},      // must not match "o1" mid-word
		{"o3x", false},      // version digit must be terminal or followed by "-"
		{"model-o3", false}, // "o3" not at the start of the (post-prefix) name
		{"", false},
		{"o", false},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			if got := IsOpenAIModel(c.model); got != c.want {
				t.Fatalf("IsOpenAIModel(%q): got %v, want %v", c.model, got, c.want)
			}
		})
	}
}

func TestResolveCanonicalModelPrecedence(t *testing.T) {
	// Helper to build a BifrostContext carrying a ResolvedAlias.
	withAlias := func(ra *ResolvedAlias) *BifrostContext {
		bc := NewBifrostContext(nil, NoDeadline)
		if ra != nil {
			bc.SetValue(BifrostContextKeyResolvedAlias, ra)
		}
		return bc
	}

	cases := []struct {
		name     string
		ra       *ResolvedAlias
		fallback string
		want     string
	}{
		{
			name: "tier 1: explicit ModelName wins over ModelID",
			ra: &ResolvedAlias{
				Key: "best-claude",
				Config: &AliasConfig{
					ModelID:   "12345-azure-deployment",
					ModelName: Ptr("claude-opus-4-8"),
				},
			},
			fallback: "12345-azure-deployment",
			want:     "claude-opus-4-8",
		},
		{
			name: "tier 2: ModelID used when ModelName absent",
			ra: &ResolvedAlias{
				Key: "best-claude",
				Config: &AliasConfig{
					ModelID: "claude-opus-4-8-20251101",
				},
			},
			fallback: "best-claude",
			want:     "claude-opus-4-8-20251101",
		},
		{
			name: "opaque ModelID is returned as-is when ModelName absent (legacy/passthrough alias)",
			ra: &ResolvedAlias{
				Key: "best-claude",
				Config: &AliasConfig{
					ModelID: "12345-azure-deployment",
				},
			},
			fallback: "best-claude",
			want:     "12345-azure-deployment",
		},
		{
			name: "alias Key is NOT consulted — a Key that smells like a version must not leak into gating",
			ra: &ResolvedAlias{
				Key: "opus-4-8-fast", // intentionally version-looking; must be ignored
				Config: &AliasConfig{
					ModelID: "12345-azure-deployment",
				},
			},
			fallback: "12345-azure-deployment",
			want:     "12345-azure-deployment", // ModelID, never the Key
		},
		{
			name: "ModelFamily is NOT consulted — present family must not change the resolved string",
			ra: &ResolvedAlias{
				Key: "x",
				Config: &AliasConfig{
					ModelID:     "claude-opus-4-8",
					ModelFamily: Ptr(ModelFamilyAnthropic),
				},
			},
			fallback: "x",
			want:     "claude-opus-4-8",
		},
		{
			name: "empty ModelName pointer is treated as absent (falls through to ModelID)",
			ra: &ResolvedAlias{
				Key: "x",
				Config: &AliasConfig{
					ModelID:   "claude-opus-4-8",
					ModelName: Ptr(""),
				},
			},
			fallback: "x",
			want:     "claude-opus-4-8",
		},
		{
			name:     "nil Config falls back to fallbackModel (defensive)",
			ra:       &ResolvedAlias{Key: "x", Config: nil},
			fallback: "claude-opus-4-8",
			want:     "claude-opus-4-8",
		},
		{
			name:     "no alias matched: fallbackModel returned verbatim — byte-identical to pre-refactor gating input",
			ra:       nil,
			fallback: "claude-opus-4-8",
			want:     "claude-opus-4-8",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveCanonicalModel(withAlias(c.ra), c.fallback)
			if got != c.want {
				t.Fatalf("ResolveCanonicalModel: got %q, want %q", got, c.want)
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
