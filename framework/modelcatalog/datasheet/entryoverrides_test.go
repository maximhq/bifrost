package datasheet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newEntryOverrideStore wires a sqlite-backed Store to an httptest datasheet
// whose body the test can swap between syncs.
func newEntryOverrideStore(t *testing.T, body string) (*Store, configstore.ConfigStore, *atomic.Value) {
	t.Helper()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	ctx := context.Background()
	cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close(ctx) })

	current := &atomic.Value{}
	current.Store(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(current.Load().(string)))
	}))
	t.Cleanup(srv.Close)
	s := New(cs, logger, Config{URL: srv.URL, ModelParametersURL: srv.URL})
	require.NoError(t, s.SyncFromURL(ctx))
	return s, cs, current
}

// saveEntryOverride mirrors ModelCatalog.SaveDatasheetOverride: baseline,
// persist, apply.
func saveEntryOverride(t *testing.T, s *Store, cs configstore.ConfigStore, row configstoreTables.TableDatasheetOverride) configstoreTables.TableDatasheetOverride {
	t.Helper()
	ctx := context.Background()
	parsed, err := ParseEntryOverride(&row)
	require.NoError(t, err)
	baseline, err := s.BuildEntryBaseline(ctx, &parsed)
	require.NoError(t, err)
	row.Baseline, err = json.Marshal(baseline)
	require.NoError(t, err)
	if row.ID == "" {
		row.ID = row.Provider + "/" + row.Model + "/" + row.Mode
	}
	require.NoError(t, cs.UpsertDatasheetOverride(ctx, &row))
	require.NoError(t, s.UpsertEntryOverride(ctx, row))
	return row
}

func chatCost(s *Store, provider schemas.ModelProvider, model string, scopes *LookupScopes) float64 {
	return s.CalculateCost(makeChatResponse(provider, model, &schemas.BifrostLLMUsage{
		PromptTokens:     1000,
		CompletionTokens: 1000,
		TotalTokens:      2000,
	}), scopes)
}

const gpt4oDatasheet = `{
  "gpt-4o": {"provider": "openai", "mode": "chat", "context_length": 128000, "max_output_tokens": 16384,
    "input_cost_per_token": 2.5e-06, "output_cost_per_token": 1e-05},
  "gpt-4o-embed": {"provider": "openai", "mode": "embedding", "input_cost_per_token": 1e-07}
}`

// An edited field keeps the user's value across syncs, even when upstream
// changes it, while the stored row keeps the upstream value and the change is
// reported as drift.
func TestEntryOverride_SurvivesPricingSyncAndReportsDrift(t *testing.T) {
	ctx := context.Background()
	s, cs, datasheetBody := newEntryOverrideStore(t, gpt4oDatasheet)

	saveEntryOverride(t, s, cs, configstoreTables.TableDatasheetOverride{
		Provider:     "openai",
		Model:        "gpt-4o",
		Mode:         "chat",
		PricingPatch: json.RawMessage(`{"context_length": 64000, "input_cost_per_token": 1e-06}`),
	})
	row := s.Get("gpt-4o", schemas.OpenAI, schemas.ChatCompletionRequest)
	require.NotNil(t, row)
	assert.Equal(t, 64000, *row.ContextLength)
	assert.Equal(t, 1e-06, *row.InputCostPerToken)
	assert.Equal(t, 1e-05, *row.OutputCostPerToken, "unpatched fields keep the upstream value")

	// Upstream raises the input price; the user's value must still win.
	datasheetBody.Store(`{
  "gpt-4o": {"provider": "openai", "mode": "chat", "context_length": 128000, "max_output_tokens": 16384,
    "input_cost_per_token": 3e-06, "output_cost_per_token": 1.2e-05}
}`)
	require.NoError(t, s.SyncFromURL(ctx))

	row = s.Get("gpt-4o", schemas.OpenAI, schemas.ChatCompletionRequest)
	require.NotNil(t, row)
	assert.Equal(t, 64000, *row.ContextLength)
	assert.Equal(t, 1e-06, *row.InputCostPerToken)
	assert.Equal(t, 1.2e-05, *row.OutputCostPerToken, "upstream changes to unpatched fields flow through")
	assert.InDelta(t, 1e-06*1000+1.2e-05*1000, chatCost(s, schemas.OpenAI, "gpt-4o", nil), 1e-12)

	stored, err := cs.GetModelPrices(ctx)
	require.NoError(t, err)
	idx := slices.IndexFunc(stored, func(r configstoreTables.TableModelPricing) bool { return r.Model == "gpt-4o" && r.Mode == "chat" })
	require.GreaterOrEqual(t, idx, 0)
	assert.Equal(t, 3e-06, *stored[idx].InputCostPerToken, "the sync keeps writing the upstream value")

	entries, total := s.ListDatasheetEntries(DatasheetEntryQuery{Search: "gpt-4o", OverriddenOnly: true})
	require.Equal(t, 1, total)
	assert.Equal(t, 3e-06, *entries[0].Upstream.InputCostPerToken)
	assert.Equal(t, 1e-06, *entries[0].Effective.InputCostPerToken)
	assert.Equal(t, []string{DriftPricingPrefix + "input_cost_per_token"}, entries[0].DriftedFields)
}

// A custom entry adds a model upstream does not list; it is priced from the
// patch and survives a sync.
func TestEntryOverride_CustomEntryIsPricedAndSurvivesSync(t *testing.T) {
	ctx := context.Background()
	s, cs, _ := newEntryOverrideStore(t, gpt4oDatasheet)

	saveEntryOverride(t, s, cs, configstoreTables.TableDatasheetOverride{
		Provider:     "openai",
		Model:        "ft:gpt-4o:acme",
		Mode:         "chat",
		IsCustom:     true,
		PricingPatch: json.RawMessage(`{"input_cost_per_token": 4e-06, "output_cost_per_token": 8e-06, "context_length": 32000}`),
	})
	require.NoError(t, s.SyncFromURL(ctx))

	assert.InDelta(t, 4e-06*1000+8e-06*1000, chatCost(s, schemas.OpenAI, "ft:gpt-4o:acme", nil), 1e-12)
	assert.Contains(t, s.DatasheetModelsForProvider(schemas.OpenAI), "ft:gpt-4o:acme")

	entries, _ := s.ListDatasheetEntries(DatasheetEntryQuery{Search: "ft:gpt-4o"})
	require.Len(t, entries, 1)
	assert.True(t, entries[0].IsCustom)
	assert.Nil(t, entries[0].Upstream)
	assert.Empty(t, entries[0].DriftedFields)
}

// Deleting an override restores the upstream values and drops custom rows.
func TestEntryOverride_DeleteRestoresUpstream(t *testing.T) {
	ctx := context.Background()
	s, cs, _ := newEntryOverrideStore(t, gpt4oDatasheet)

	edit := saveEntryOverride(t, s, cs, configstoreTables.TableDatasheetOverride{
		Provider: "openai", Model: "gpt-4o", Mode: "chat",
		PricingPatch: json.RawMessage(`{"is_deprecated": true}`),
	})
	custom := saveEntryOverride(t, s, cs, configstoreTables.TableDatasheetOverride{
		Provider: "openai", Model: "my-model", Mode: "chat", IsCustom: true,
		PricingPatch: json.RawMessage(`{"input_cost_per_token": 1e-06}`),
	})
	assert.Contains(t, s.DeprecatedDatasheetModelsForProvider(schemas.OpenAI), "gpt-4o")

	found := s.FindEntryOverride("openai", "gpt-4o", "chat")
	require.NotNil(t, found)
	assert.Equal(t, edit.ID, found.ID)
	assert.Nil(t, s.FindEntryOverride("openai", "gpt-4o", "responses"))

	s.DeleteEntryOverride(ctx, edit.ID)
	s.DeleteEntryOverride(ctx, custom.ID)
	assert.NotContains(t, s.DeprecatedDatasheetModelsForProvider(schemas.OpenAI), "gpt-4o")
	assert.Nil(t, s.Get("my-model", schemas.OpenAI, schemas.ChatCompletionRequest))
	assert.Equal(t, 2.5e-06, *s.Get("gpt-4o", schemas.OpenAI, schemas.ChatCompletionRequest).InputCostPerToken)
}

// A mode-wide override (mode "") patches every mode of the model; a
// mode-specific override for the same field wins on its mode.
func TestEntryOverride_ModeWideAndModeSpecificPrecedence(t *testing.T) {
	s := newTestStore()
	for _, mode := range []string{"chat", "responses"} {
		s.pricingData[makeKey("m", "openai", mode)] = configstoreTables.TableModelPricing{
			Model: "m", Provider: "openai", Mode: mode, InputCostPerToken: bifrost.Ptr(1.0),
		}
	}
	s.SetEntryOverrides(context.Background(), []configstoreTables.TableDatasheetOverride{
		{ID: "chat", Provider: "openai", Model: "m", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 3}`)},
		{ID: "all", Provider: "openai", Model: "m", PricingPatch: json.RawMessage(`{"input_cost_per_token": 2, "max_output_tokens": 10}`)},
	})
	chat := s.Get("m", schemas.OpenAI, schemas.ChatCompletionRequest)
	responses := s.Get("m", schemas.OpenAI, schemas.ResponsesRequest)
	assert.Equal(t, 3.0, *chat.InputCostPerToken)
	assert.Equal(t, 10, *chat.MaxOutputTokens)
	assert.Equal(t, 2.0, *responses.InputCostPerToken)
	assert.Equal(t, 10, *responses.MaxOutputTokens)
}

// Scoped pricing overrides keep stacking on top of the edited datasheet price.
func TestEntryOverride_ScopedPricingOverrideStacksOnEditedBase(t *testing.T) {
	s := newTestStore()
	s.pricingData[makeKey("m", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model: "m", Provider: "openai", Mode: "chat", InputCostPerToken: bifrost.Ptr(1e-06), OutputCostPerToken: bifrost.Ptr(2e-06),
	}
	s.SetEntryOverrides(context.Background(), []configstoreTables.TableDatasheetOverride{
		{ID: "edit", Provider: "openai", Model: "m", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 5e-06}`)},
	})
	require.NoError(t, s.SetOverrides([]configstoreTables.TablePricingOverride{{
		ID:               "scoped",
		ScopeKind:        string(ScopeKindGlobal),
		MatchType:        string(MatchTypeExact),
		Pattern:          "m",
		RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
		PricingPatchJSON: `{"output_cost_per_token": 7e-06}`,
	}}))
	assert.InDelta(t, 5e-06*1000+7e-06*1000, chatCost(s, schemas.OpenAI, "m", &LookupScopes{}), 1e-12)
}

// Params patches reach both the bulk index path and the per-model capability
// path, and describe models the params sheet does not list.
func TestEntryOverride_ParamsPatchAppliesToCapabilities(t *testing.T) {
	ctx := context.Background()
	s, cs, _ := newEntryOverrideStore(t, gpt4oDatasheet)
	require.NoError(t, cs.UpsertModelParametersBatch(ctx, []configstoreTables.TableModelParameters{
		{Model: "gpt-4o", Data: `{"provider":"openai","mode":"chat","max_output_tokens":16384,"supports_mcp":false}`},
	}))
	_, err := s.LoadModelParamsFromDB(ctx)
	require.NoError(t, err)

	saveEntryOverride(t, s, cs, configstoreTables.TableDatasheetOverride{
		Provider: "openai", Model: "gpt-4o", Mode: "chat",
		ParamsPatch: json.RawMessage(`{"max_output_tokens": 4096, "supports_mcp": true, "supported_endpoints": ["/v1/chat/completions"]}`),
	})
	saveEntryOverride(t, s, cs, configstoreTables.TableDatasheetOverride{
		Provider: "openai", Model: "my-model", Mode: "chat", IsCustom: true,
		ParamsPatch: json.RawMessage(`{"mode": "chat", "supports_mcp": true}`),
	})

	caps, err := s.LoadModelCapabilities(ctx, schemas.OpenAI, "gpt-4o")
	require.NoError(t, err)
	require.NotNil(t, caps)
	assert.Equal(t, 4096, *caps.MaxOutputTokens)
	assert.True(t, *caps.SupportsMCP)
	assert.True(t, s.IsRequestTypeSupported("gpt-4o", schemas.ChatCompletionRequest), "the derived index sees the patched endpoints")

	custom, err := s.LoadModelCapabilities(ctx, schemas.OpenAI, "my-model")
	require.NoError(t, err)
	require.NotNil(t, custom)
	assert.True(t, *custom.SupportsMCP)

	other, err := s.LoadModelCapabilities(ctx, schemas.Anthropic, "my-model")
	require.NoError(t, err)
	assert.Nil(t, other, "a patch only answers for its own provider")

	resolved, err := s.ResolveModelParameters(ctx, "gpt-4o")
	require.NoError(t, err)
	assert.JSONEq(t, `{"provider":"openai","mode":"chat","max_output_tokens":4096,"supports_mcp":true,"supported_endpoints":["/v1/chat/completions"]}`, resolved.Data)
	stored, err := cs.GetModelParametersByModel(ctx, "gpt-4o")
	require.NoError(t, err)
	assert.Contains(t, stored.Data, `"max_output_tokens":16384`, "the stored row keeps the upstream blob")

	detail, err := s.GetDatasheetEntry(ctx, "openai", "gpt-4o", "chat")
	require.NoError(t, err)
	require.NotNil(t, detail)
	assert.Contains(t, string(detail.UpstreamParams), `"max_output_tokens":16384`)
	assert.Contains(t, string(detail.EffectiveParams), `"max_output_tokens":4096`)

	// Upstream changes a patched key: drift on the params field.
	require.NoError(t, cs.UpsertModelParametersBatch(ctx, []configstoreTables.TableModelParameters{
		{Model: "gpt-4o", Data: `{"provider":"openai","mode":"chat","max_output_tokens":32768,"supports_mcp":false}`},
	}))
	_, err = s.LoadModelParamsFromDB(ctx)
	require.NoError(t, err)
	detail, err = s.GetDatasheetEntry(ctx, "openai", "gpt-4o", "chat")
	require.NoError(t, err)
	assert.Equal(t, []string{DriftParamsPrefix + "max_output_tokens"}, detail.DriftedFields)
	entries, _ := s.ListDatasheetEntries(DatasheetEntryQuery{Search: "gpt-4o", OverriddenOnly: true})
	require.Len(t, entries, 1)
	assert.Equal(t, []string{DriftParamsPrefix + "max_output_tokens"}, entries[0].DriftedFields)
}

// An override whose target is not in the datasheet stays listed so it can be
// found and deleted.
func TestEntryOverride_OrphanOverrideIsListed(t *testing.T) {
	s := newTestStore()
	s.SetEntryOverrides(context.Background(), []configstoreTables.TableDatasheetOverride{
		{ID: "gone", Provider: "openai", Model: "retired", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 1}`)},
	})
	entries, total := s.ListDatasheetEntries(DatasheetEntryQuery{})
	require.Equal(t, 1, total)
	assert.Nil(t, entries[0].Effective)
	assert.Equal(t, "gone", entries[0].Overrides[0].ID)
}

func TestParseEntryOverride_Validation(t *testing.T) {
	tests := []struct {
		name    string
		row     configstoreTables.TableDatasheetOverride
		wantErr string
	}{
		{"missing provider", configstoreTables.TableDatasheetOverride{Model: "m", PricingPatch: json.RawMessage(`{"input_cost_per_token":1}`)}, "provider is required"},
		{"missing model", configstoreTables.TableDatasheetOverride{Provider: "openai", PricingPatch: json.RawMessage(`{"input_cost_per_token":1}`)}, "model is required"},
		{"separator in model", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "a|b", PricingPatch: json.RawMessage(`{"input_cost_per_token":1}`)}, "must not contain"},
		{"custom without mode", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", IsCustom: true}, "mode is required"},
		{"empty patches", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", PricingPatch: json.RawMessage(`{}`)}, "at least one of"},
		{"unknown pricing key", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", PricingPatch: json.RawMessage(`{"price":1}`)}, `unknown field "price"`},
		{"identity key", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", PricingPatch: json.RawMessage(`{"provider":"x"}`)}, `unknown field "provider"`},
		{"negative cost", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", PricingPatch: json.RawMessage(`{"input_cost_per_token":-1}`)}, "must not be negative"},
		{"wrong type", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", PricingPatch: json.RawMessage(`{"context_length":"big"}`)}, "pricing_patch"},
		{"patch not an object", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", PricingPatch: json.RawMessage(`[1]`)}, "must be a JSON object"},
		{"params provider", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", ParamsPatch: json.RawMessage(`{"provider":"x"}`)}, "provider is the override target"},
		{"params negative", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", ParamsPatch: json.RawMessage(`{"max_output_tokens":-5}`)}, "must not be negative"},
		{"params wrong type", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", ParamsPatch: json.RawMessage(`{"supports_mcp":"yes"}`)}, "params_patch"},
		{"valid edit", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", PricingPatch: json.RawMessage(`{"context_length":1000,"is_deprecated":true,"input_cost_per_token":null}`)}, ""},
		{"valid custom without patches", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", Mode: "chat", IsCustom: true}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseEntryOverride(&tt.row)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestMergeAndComposePatch(t *testing.T) {
	target := map[string]any{"a": 1.0, "b": map[string]any{"x": 1.0, "y": 2.0}, "c": 3.0}
	got := mergePatch(target, map[string]any{"a": nil, "b": map[string]any{"y": nil, "z": 4.0}, "d": "new"})
	assert.Equal(t, map[string]any{"b": map[string]any{"x": 1.0, "z": 4.0}, "c": 3.0, "d": "new"}, got)

	// Applying the composition equals applying both patches in order.
	first := map[string]any{"a": 1.0, "b": map[string]any{"x": 1.0}}
	second := map[string]any{"a": nil, "b": map[string]any{"y": 2.0}}
	composed := composePatch(composePatch(nil, first), second)
	sequential := mergePatch(mergePatch(map[string]any{"a": 0.0}, first), second)
	assert.Equal(t, sequential, mergePatch(map[string]any{"a": 0.0}, composed))
	assert.Equal(t, map[string]any{"x": 1.0}, first["b"], "composing never mutates an input patch")
}

// BenchmarkLoadModelCapabilities measures the per-model cache-miss path with
// no datasheet overrides (the common case, which must not regress) and with an
// override for the requested model.
func BenchmarkLoadModelCapabilities(b *testing.B) {
	rows := map[string]string{"gpt-4o": `{"provider":"openai","mode":"chat","max_output_tokens":16384,"supports_mcp":false}`}
	ctx := context.Background()
	b.Run("no_overrides", func(b *testing.B) {
		s := NewTestStore(nil)
		s.configStore = paramsOnlyConfigStore{rows: rows}
		b.ReportAllocs()
		for b.Loop() {
			if _, err := s.LoadModelCapabilities(ctx, schemas.OpenAI, "gpt-4o"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("with_override", func(b *testing.B) {
		s := NewTestStore(nil)
		s.configStore = paramsOnlyConfigStore{rows: rows}
		s.installEntryOverrides(s.parseEntryOverrideRows([]configstoreTables.TableDatasheetOverride{
			{ID: "o", Provider: "openai", Model: "gpt-4o", Mode: "chat", ParamsPatch: json.RawMessage(`{"max_output_tokens":4096}`)},
		}))
		b.ReportAllocs()
		for b.Loop() {
			if _, err := s.LoadModelCapabilities(ctx, schemas.OpenAI, "gpt-4o"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkCalculateCostWithEntryOverrides pins that overrides are merged at
// load time: the request-path cost is identical with or without them.
func BenchmarkCalculateCostWithEntryOverrides(b *testing.B) {
	newStore := func(withOverride bool) *Store {
		s := newTestStore()
		s.pricingData[makeKey("gpt-4o", "openai", "chat")] = configstoreTables.TableModelPricing{
			Model: "gpt-4o", Provider: "openai", Mode: "chat", InputCostPerToken: bifrost.Ptr(2.5e-06), OutputCostPerToken: bifrost.Ptr(1e-05),
		}
		if withOverride {
			s.SetEntryOverrides(context.Background(), []configstoreTables.TableDatasheetOverride{
				{ID: "o", Provider: "openai", Model: "gpt-4o", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 1e-06}`)},
			})
		}
		return s
	}
	for _, tc := range []struct {
		name     string
		override bool
	}{{"no_overrides", false}, {"with_override", true}} {
		b.Run(tc.name, func(b *testing.B) {
			s := newStore(tc.override)
			resp := makeChatResponse(schemas.OpenAI, "gpt-4o", &schemas.BifrostLLMUsage{PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000})
			b.ReportAllocs()
			for b.Loop() {
				_ = s.CalculateCost(resp, nil)
			}
		})
	}
}
