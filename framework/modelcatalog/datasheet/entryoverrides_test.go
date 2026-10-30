package datasheet

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
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

// Pricing rows are keyed case-insensitively, so an override saved with the
// datasheet's own mixed-case spelling must still find and patch its row, both
// for one mode and mode-wide, and the patched fields must reach the listings.
func TestEntryOverride_MixedCaseModelIsPatched(t *testing.T) {
	ctx := context.Background()
	s, cs, _ := newEntryOverrideStore(t, `{
  "GPT-4o-Mini": {"provider": "openai", "mode": "chat", "context_length": 128000, "input_cost_per_token": 1.5e-07},
  "Mixed-Model": {"provider": "openai", "mode": "chat", "input_cost_per_token": 1e-06},
  "Mixed-Model-resp": {"provider": "openai", "mode": "responses", "input_cost_per_token": 1e-06}
}`)

	saveEntryOverride(t, s, cs, configstoreTables.TableDatasheetOverride{
		Provider: "openai", Model: "GPT-4o-Mini", Mode: "chat",
		PricingPatch: json.RawMessage(`{"context_length": 64000, "is_deprecated": true, "base_model": "gpt-4o"}`),
	})
	saveEntryOverride(t, s, cs, configstoreTables.TableDatasheetOverride{
		Provider: "openai", Model: "Mixed-Model",
		PricingPatch: json.RawMessage(`{"input_cost_per_token": 2e-06}`),
	})
	require.NoError(t, s.SyncFromURL(ctx))

	row := s.Get("GPT-4o-Mini", schemas.OpenAI, schemas.ChatCompletionRequest)
	require.NotNil(t, row)
	assert.Equal(t, 64000, *row.ContextLength, "a per-mode override on a mixed-case model applies")
	assert.Equal(t, 2e-06, *s.Get("Mixed-Model", schemas.OpenAI, schemas.ChatCompletionRequest).InputCostPerToken, "a mode-wide override on a mixed-case model applies")

	assert.Contains(t, s.DatasheetModelsForProvider(schemas.OpenAI), "GPT-4o-Mini", "the listing keeps the datasheet spelling")
	assert.Contains(t, s.DeprecatedDatasheetModelsForProvider(schemas.OpenAI), "GPT-4o-Mini", "a patched is_deprecated reaches the deprecated listing")
	assert.Equal(t, "gpt-4o", s.BaseModelName("GPT-4o-Mini"), "a patched base_model reaches the base-model index")

	entries, total := s.ListDatasheetEntries(DatasheetEntryQuery{Search: "gpt-4o-mini", OverriddenOnly: true})
	require.Equal(t, 1, total, "the editor lists the mixed-case entry as overridden")
	assert.Equal(t, 128000, *entries[0].Upstream.ContextLength)
}

// Without a config store, config.json overrides arrive with no ID. Each must
// still be its own override: its own baseline for drift, and deleting one
// leaves the other. An upsert, which always follows an ID-assigning save,
// rejects an empty ID.
func TestEntryOverride_IDlessOverridesStayDistinct(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()
	s.SetPricingRowsForTest([]configstoreTables.TableModelPricing{
		{Model: "a", Provider: "openai", Mode: "chat", InputCostPerToken: bifrost.Ptr(1.0)},
		{Model: "b", Provider: "openai", Mode: "chat", InputCostPerToken: bifrost.Ptr(2.0)},
	})
	s.SetEntryOverrides(ctx, []configstoreTables.TableDatasheetOverride{
		{Provider: "openai", Model: "a", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 5}`), Baseline: json.RawMessage(`{"pricing":{"input_cost_per_token":1}}`)},
		{Provider: "openai", Model: "b", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 6}`), Baseline: json.RawMessage(`{"pricing":{"input_cost_per_token":2}}`)},
	})
	assert.Equal(t, 5.0, *s.Get("a", schemas.OpenAI, schemas.ChatCompletionRequest).InputCostPerToken)
	assert.Equal(t, 6.0, *s.Get("b", schemas.OpenAI, schemas.ChatCompletionRequest).InputCostPerToken)

	entries, total := s.ListDatasheetEntries(DatasheetEntryQuery{OverriddenOnly: true})
	require.Equal(t, 2, total)
	ids := map[string]string{}
	for _, entry := range entries {
		require.Len(t, entry.Overrides, 1, "model %s", entry.Model)
		assert.NotEmpty(t, entry.Overrides[0].ID, "model %s gets an ID", entry.Model)
		assert.Empty(t, entry.DriftedFields, "model %s is checked against its own baseline", entry.Model)
		ids[entry.Model] = entry.Overrides[0].ID
	}
	require.NotEqual(t, ids["a"], ids["b"], "ID-less overrides get distinct IDs")

	s.DeleteEntryOverride(ctx, ids["a"])
	assert.Equal(t, 1.0, *s.Get("a", schemas.OpenAI, schemas.ChatCompletionRequest).InputCostPerToken, "the deleted override is gone")
	assert.Equal(t, 6.0, *s.Get("b", schemas.OpenAI, schemas.ChatCompletionRequest).InputCostPerToken, "deleting one override leaves the other")

	err := s.UpsertEntryOverride(ctx, configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "a", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 7}`)})
	require.Error(t, err, "an upsert without an ID is rejected")
	assert.Equal(t, 1.0, *s.Get("a", schemas.OpenAI, schemas.ChatCompletionRequest).InputCostPerToken)
}

// Target-derived IDs must not collide when a field contains '/': these two
// targets differ only in where the slash sits.
func TestEntryOverride_IDlessTargetsWithSlashesStayDistinct(t *testing.T) {
	s := newTestStore()
	s.SetPricingRowsForTest([]configstoreTables.TableModelPricing{
		{Model: "team/model", Provider: "acme", Mode: "chat", InputCostPerToken: bifrost.Ptr(1.0)},
		{Model: "model", Provider: "acme/team", Mode: "chat", InputCostPerToken: bifrost.Ptr(2.0)},
	})
	s.SetEntryOverrides(context.Background(), []configstoreTables.TableDatasheetOverride{
		{Provider: "acme", Model: "team/model", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 5}`)},
		{Provider: "acme/team", Model: "model", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 6}`)},
	})
	assert.Equal(t, 5.0, *s.Get("team/model", "acme", schemas.ChatCompletionRequest).InputCostPerToken)
	assert.Equal(t, 6.0, *s.Get("model", "acme/team", schemas.ChatCompletionRequest).InputCostPerToken, "the second target is not dropped as a duplicate")
	a := s.FindEntryOverride("acme", "team/model", "chat")
	b := s.FindEntryOverride("acme/team", "model", "chat")
	require.NotNil(t, a)
	require.NotNil(t, b)
	assert.NotEqual(t, a.ID, b.ID)
}

// Two overrides for one pricing target, whether a second ID or a spelling
// that differs only by case, would both patch the same row. The first one
// loaded (oldest) is kept; lookups by target ignore case like the pricing key.
func TestEntryOverride_OneOverridePerTarget(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()
	s.SetPricingRowsForTest([]configstoreTables.TableModelPricing{{Model: "gpt-4o", Provider: "openai", Mode: "chat", InputCostPerToken: bifrost.Ptr(1.0)}})
	s.SetEntryOverrides(ctx, []configstoreTables.TableDatasheetOverride{
		{ID: "first", Provider: "openai", Model: "gpt-4o", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 5}`)},
		{ID: "second", Provider: "openai", Model: "gpt-4o", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 6}`)},
		{ID: "variant", Provider: "OpenAI", Model: "GPT-4o", Mode: "CHAT", PricingPatch: json.RawMessage(`{"input_cost_per_token": 7}`)},
	})
	assert.Equal(t, 5.0, *s.Get("gpt-4o", schemas.OpenAI, schemas.ChatCompletionRequest).InputCostPerToken, "the first override for the target wins")
	entries, _ := s.ListDatasheetEntries(DatasheetEntryQuery{OverriddenOnly: true})
	require.Len(t, entries, 1)
	require.Len(t, entries[0].Overrides, 1, "duplicates of a target are dropped")
	assert.Equal(t, "first", entries[0].Overrides[0].ID)

	found := s.FindEntryOverride("openai", "GPT-4o", "chat")
	require.NotNil(t, found, "a target lookup ignores case")
	assert.Equal(t, "first", found.ID)

	require.NoError(t, s.UpsertEntryOverride(ctx, configstoreTables.TableDatasheetOverride{ID: "new", Provider: "openai", Model: "GPT-4O", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 8}`)}))
	entries, _ = s.ListDatasheetEntries(DatasheetEntryQuery{OverriddenOnly: true})
	require.Len(t, entries, 1)
	require.Len(t, entries[0].Overrides, 1, "an upsert for a case variant replaces the target's override")
	assert.Equal(t, 8.0, *s.Get("gpt-4o", schemas.OpenAI, schemas.ChatCompletionRequest).InputCostPerToken)
}

// A params override saved under a mixed-case provider answers for that
// provider's canonical name.
func TestEntryOverride_ParamsPatchProviderIgnoresCase(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()
	s.SetEntryOverrides(ctx, []configstoreTables.TableDatasheetOverride{
		{ID: "azure", Provider: "Azure", Model: "my-deployment", Mode: "chat", IsCustom: true, ParamsPatch: json.RawMessage(`{"mode": "chat", "supports_mcp": true}`)},
	})
	caps, err := s.LoadModelCapabilities(ctx, schemas.Azure, "my-deployment")
	require.NoError(t, err)
	require.NotNil(t, caps, "the Azure override answers for schemas.Azure")
	assert.True(t, *caps.SupportsMCP)
}

// The detail lookup finds an entry whatever the case of model and mode, like
// the request path, and reads params under the row's own spelling.
func TestGetDatasheetEntry_IgnoresCase(t *testing.T) {
	ctx := context.Background()
	s, cs, _ := newEntryOverrideStore(t, gpt4oDatasheet)
	require.NoError(t, cs.UpsertModelParametersBatch(ctx, []configstoreTables.TableModelParameters{
		{Model: "gpt-4o", Data: `{"provider":"openai","mode":"chat","max_output_tokens":16384}`},
	}))
	detail, err := s.GetDatasheetEntry(ctx, "OpenAI", "GPT-4O", "CHAT")
	require.NoError(t, err)
	require.NotNil(t, detail, "a case variant of an existing entry is found")
	assert.Equal(t, "gpt-4o", detail.Model)
	assert.Equal(t, "chat", detail.Mode)
	assert.Contains(t, string(detail.UpstreamParams), `"max_output_tokens":16384`, "params are read under the row's spelling")
}

// bedrock and bedrock_mantle share pricing rows but not model parameters, so
// overrides for the same model under each are two targets with two IDs.
func TestEntryOverride_BedrockAndMantleStayDistinct(t *testing.T) {
	ctx := context.Background()
	for name, ids := range map[string][2]string{"without ids": {"", ""}, "with ids": {"b", "m"}} {
		t.Run(name, func(t *testing.T) {
			s := newTestStore()
			s.SetEntryOverrides(ctx, []configstoreTables.TableDatasheetOverride{
				{ID: ids[0], Provider: "bedrock", Model: "m", Mode: "chat", IsCustom: true, ParamsPatch: json.RawMessage(`{"mode": "chat", "max_output_tokens": 100}`)},
				{ID: ids[1], Provider: "bedrock_mantle", Model: "m", Mode: "chat", IsCustom: true, ParamsPatch: json.RawMessage(`{"mode": "chat", "max_output_tokens": 200}`)},
			})
			bedrock, err := s.LoadModelCapabilities(ctx, schemas.Bedrock, "m")
			require.NoError(t, err)
			require.NotNil(t, bedrock)
			assert.Equal(t, 100, *bedrock.MaxOutputTokens)
			mantle, err := s.LoadModelCapabilities(ctx, schemas.BedrockMantle, "m")
			require.NoError(t, err)
			require.NotNil(t, mantle, "the bedrock_mantle override is not dropped as a duplicate")
			assert.Equal(t, 200, *mantle.MaxOutputTokens)
			b, m := s.FindEntryOverride("bedrock", "m", "chat"), s.FindEntryOverride("bedrock_mantle", "m", "chat")
			require.NotNil(t, b)
			require.NotNil(t, m)
			assert.NotEqual(t, b.ID, m.ID)
		})
	}
}

// Provider aliases fold whatever their case: a Vertex_AI override edits the
// vertex row and a Vertex_AI custom entry is listed under vertex.
func TestEntryOverride_MixedCaseProviderAliasFolds(t *testing.T) {
	s := newTestStore()
	s.SetPricingRowsForTest([]configstoreTables.TableModelPricing{{Model: "gemini-x", Provider: "vertex", Mode: "chat", InputCostPerToken: bifrost.Ptr(1.0)}})
	s.SetEntryOverrides(context.Background(), []configstoreTables.TableDatasheetOverride{
		{ID: "edit", Provider: "Vertex_AI", Model: "gemini-x", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 5}`)},
		{ID: "custom", Provider: "Vertex_AI", Model: "my-tuned", Mode: "chat", IsCustom: true, PricingPatch: json.RawMessage(`{"input_cost_per_token": 2}`)},
	})
	assert.Equal(t, 5.0, *s.Get("gemini-x", schemas.Vertex, schemas.ChatCompletionRequest).InputCostPerToken, "the Vertex_AI edit patches the vertex row")
	assert.Contains(t, s.DatasheetModelsForProvider(schemas.Vertex), "my-tuned", "the Vertex_AI custom entry is listed under vertex")
}

// bedrock and bedrock_mantle share one pricing row, so only one override may
// patch its pricing; params-only overrides for either provider are fine.
func TestEntryOverride_PricingPatchesMustNotCollideAfterFolding(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()
	s.SetPricingRowsForTest([]configstoreTables.TableModelPricing{{Model: "m", Provider: "bedrock", Mode: "chat", InputCostPerToken: bifrost.Ptr(1.0)}})
	require.NoError(t, s.UpsertEntryOverride(ctx, configstoreTables.TableDatasheetOverride{ID: "a", Provider: "bedrock", Model: "m", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 5}`)}))

	err := s.UpsertEntryOverride(ctx, configstoreTables.TableDatasheetOverride{ID: "b", Provider: "bedrock_mantle", Model: "m", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 6}`)})
	require.Error(t, err, "a second pricing patch on the folded row is rejected")
	assert.Contains(t, err.Error(), `"a"`, "the error names the override that already prices the row")
	require.NoError(t, s.UpsertEntryOverride(ctx, configstoreTables.TableDatasheetOverride{ID: "a", Provider: "bedrock", Model: "m", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 7}`)}), "re-saving the same override is not a collision")
	require.NoError(t, s.UpsertEntryOverride(ctx, configstoreTables.TableDatasheetOverride{ID: "c", Provider: "bedrock_mantle", Model: "m", Mode: "chat", ParamsPatch: json.RawMessage(`{"max_output_tokens": 200}`)}), "a params-only override is fine")
	assert.Equal(t, 7.0, *s.Get("m", schemas.Bedrock, schemas.ChatCompletionRequest).InputCostPerToken)

	s.SetEntryOverrides(ctx, []configstoreTables.TableDatasheetOverride{
		{ID: "a", Provider: "bedrock", Model: "m", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 5}`)},
		{ID: "b", Provider: "bedrock_mantle", Model: "m", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 6}`)},
	})
	assert.Equal(t, 5.0, *s.Get("m", schemas.Bedrock, schemas.ChatCompletionRequest).InputCostPerToken, "a stored collision keeps the first override")
	assert.Nil(t, s.FindEntryOverride("bedrock_mantle", "m", "chat"), "the colliding override is skipped")
}

// The detail view reads params for the provider asked about, not the provider
// its pricing row is folded under.
func TestGetDatasheetEntry_ParamsUseRequestedProvider(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()
	s.SetPricingRowsForTest([]configstoreTables.TableModelPricing{{Model: "m", Provider: "bedrock", Mode: "chat", InputCostPerToken: bifrost.Ptr(1.0)}})
	s.SetEntryOverrides(ctx, []configstoreTables.TableDatasheetOverride{
		{ID: "b", Provider: "bedrock", Model: "m", Mode: "chat", ParamsPatch: json.RawMessage(`{"max_output_tokens": 100}`)},
		{ID: "m", Provider: "bedrock_mantle", Model: "m", Mode: "chat", ParamsPatch: json.RawMessage(`{"max_output_tokens": 200}`)},
	})
	detail, err := s.GetDatasheetEntry(ctx, "bedrock_mantle", "m", "chat")
	require.NoError(t, err)
	require.NotNil(t, detail)
	assert.Contains(t, string(detail.EffectiveParams), `"max_output_tokens":200`, "the bedrock_mantle params patch, not bedrock's")
}

// A mixed-case provider alias in the listing filter folds like the override's
// provider does, so it finds the canonical row, and the detail lookup through
// that filter resolves the same target as FindEntryOverride.
func TestEntryOverride_ListProviderFilterFoldsMixedCaseAlias(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()
	s.SetPricingRowsForTest([]configstoreTables.TableModelPricing{
		{Model: "gemini-x", Provider: "vertex", Mode: "chat", InputCostPerToken: bifrost.Ptr(1.0)},
		{Model: "m", Provider: "bedrock", Mode: "chat", InputCostPerToken: bifrost.Ptr(1.0)},
	})
	s.SetEntryOverrides(ctx, []configstoreTables.TableDatasheetOverride{
		{ID: "edit", Provider: "vertex_ai", Model: "gemini-x", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 5}`)},
		{ID: "mantle", Provider: "bedrock_mantle", Model: "m", Mode: "chat", ParamsPatch: json.RawMessage(`{"max_output_tokens": 200}`)},
	})
	entries, total := s.ListDatasheetEntries(DatasheetEntryQuery{Provider: "Vertex_AI"})
	require.Equal(t, 1, total, "a Vertex_AI filter finds the vertex row")
	require.Len(t, entries[0].Overrides, 1)
	assert.Equal(t, "edit", entries[0].Overrides[0].ID)

	detail, err := s.GetDatasheetEntry(ctx, "Bedrock_Mantle", "m", "chat")
	require.NoError(t, err)
	require.NotNil(t, detail, "a Bedrock_Mantle detail lookup finds the bedrock row")
	assert.Contains(t, string(detail.EffectiveParams), `"max_output_tokens":200`, "with the bedrock_mantle params")
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

	// Applying mode-wide first must not detach an override from its saved row:
	// the editor resets and deletes by the ID it gets back here.
	require.NotNil(t, s.FindEntryOverride("openai", "m", "chat"))
	assert.Equal(t, "chat", s.FindEntryOverride("openai", "m", "chat").ID)
	assert.Equal(t, "all", s.FindEntryOverride("openai", "m", "").ID)
	s.DeleteEntryOverride(context.Background(), "chat")
	assert.Nil(t, s.FindEntryOverride("openai", "m", "chat"))
	require.NotNil(t, s.FindEntryOverride("openai", "m", ""))
	assert.Equal(t, "all", s.FindEntryOverride("openai", "m", "").ID)
}

// Concurrent saves for different targets all stay applied in memory.
func TestEntryOverride_ConcurrentUpsertsKeepEveryOverride(t *testing.T) {
	ctx := context.Background()
	for round := 0; round < 20; round++ {
		s := newTestStore()
		const n = 16
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				assert.NoError(t, s.UpsertEntryOverride(ctx, configstoreTables.TableDatasheetOverride{
					ID: fmt.Sprintf("o%d", i), Provider: "openai", Model: fmt.Sprintf("m%d", i), Mode: "chat",
					PricingPatch: json.RawMessage(`{"input_cost_per_token": 1}`),
				}))
			}(i)
		}
		wg.Wait()
		for i := 0; i < n; i++ {
			require.NotNil(t, s.FindEntryOverride("openai", fmt.Sprintf("m%d", i), "chat"), "round %d lost override o%d", round, i)
		}
	}
}

// A mode-wide override's baseline comes from one mode; another mode whose
// upstream value already differed is not drift.
func TestEntryOverride_ModeWideBaselineDoesNotFlagOtherModes(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()
	s.pricingData[makeKey("m", "openai", "chat")] = configstoreTables.TableModelPricing{Model: "m", Provider: "openai", Mode: "chat", InputCostPerToken: bifrost.Ptr(1.0)}
	s.pricingData[makeKey("m", "openai", "responses")] = configstoreTables.TableModelPricing{Model: "m", Provider: "openai", Mode: "responses", InputCostPerToken: bifrost.Ptr(2.0)}
	row := configstoreTables.TableDatasheetOverride{ID: "all", Provider: "openai", Model: "m", PricingPatch: json.RawMessage(`{"input_cost_per_token": 5}`)}
	parsed, err := ParseEntryOverride(&row)
	require.NoError(t, err)
	baseline, err := s.BuildEntryBaseline(ctx, &parsed)
	require.NoError(t, err)
	row.Baseline, err = json.Marshal(baseline)
	require.NoError(t, err)
	s.SetEntryOverrides(ctx, []configstoreTables.TableDatasheetOverride{row})

	entries, total := s.ListDatasheetEntries(DatasheetEntryQuery{OverriddenOnly: true})
	require.Equal(t, 2, total)
	for _, entry := range entries {
		assert.Empty(t, entry.DriftedFields, "mode %s has no upstream change", entry.Mode)
	}

	// A real upstream change to the baseline mode is still reported.
	s.mu.Lock()
	s.setPricingRowsUnsafe([]configstoreTables.TableModelPricing{
		{Model: "m", Provider: "openai", Mode: "chat", InputCostPerToken: bifrost.Ptr(1.5)},
		{Model: "m", Provider: "openai", Mode: "responses", InputCostPerToken: bifrost.Ptr(2.0)},
	})
	s.mu.Unlock()
	entries, _ = s.ListDatasheetEntries(DatasheetEntryQuery{OverriddenOnly: true})
	for _, entry := range entries {
		assert.Equal(t, []string{DriftPricingPrefix + "input_cost_per_token"}, entry.DriftedFields, "mode %s", entry.Mode)
	}
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

// A positive provider-reported usage.cost wins over an edited base price
// unless the provider has ignore_provider_cost set.
func TestEntryOverride_ProviderReportedCostBypassesEditedBase(t *testing.T) {
	s := newTestStore()
	s.pricingData[makeKey("m", "openai", "chat")] = configstoreTables.TableModelPricing{
		Model: "m", Provider: "openai", Mode: "chat", InputCostPerToken: bifrost.Ptr(1e-06), OutputCostPerToken: bifrost.Ptr(2e-06),
	}
	s.SetEntryOverrides(context.Background(), []configstoreTables.TableDatasheetOverride{
		{ID: "edit", Provider: "openai", Model: "m", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 5e-06}`)},
	})
	resp := makeChatResponse(schemas.OpenAI, "m", &schemas.BifrostLLMUsage{
		PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000, Cost: &schemas.BifrostCost{TotalCost: 1067},
	})
	assert.InDelta(t, 1067, s.CalculateCost(resp, nil), 1e-12)

	s.SetIgnoreProviderCost(schemas.OpenAI, true)
	assert.InDelta(t, 5e-06*1000+2e-06*1000, s.CalculateCost(resp, nil), 1e-12)
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

// A params row stored under a provider-qualified name is the upstream for a
// patch on the bare name: the patched row keeps its flags.
func TestEntryOverride_ParamsPatchMergesIntoQualifiedUpstreamRow(t *testing.T) {
	s := newTestStore()
	s.SetEntryOverrides(context.Background(), []configstoreTables.TableDatasheetOverride{
		{ID: "limit", Provider: "openai", Model: "gpt-4o", Mode: "chat", ParamsPatch: json.RawMessage(`{"max_output_tokens": 4096}`)},
	})
	s.applyModelParameters(map[string]json.RawMessage{
		"openai/gpt-4o": json.RawMessage(`{"provider":"openai","mode":"chat","supports_function_calling":true,"max_output_tokens":16384}`),
	})
	assert.Contains(t, s.GetSupportedParameters("gpt-4o"), "tools", "the bare row is built from the qualified upstream row, not the patch alone")
	assert.Contains(t, s.GetSupportedParameters("openai/gpt-4o"), "tools")
}

// Without a config store the parameter indexes are rebuilt from the in-memory
// sheet when overrides change.
func TestEntryOverride_ParamsPatchWithoutConfigStoreRebuildsIndexes(t *testing.T) {
	ctx := context.Background()
	s := newTestStore()
	s.applyModelParameters(map[string]json.RawMessage{
		"gpt-4o": json.RawMessage(`{"provider":"openai","mode":"chat","supported_endpoints":["/v1/chat/completions"]}`),
	})
	require.False(t, s.IsRequestTypeSupported("gpt-4o", schemas.ResponsesRequest))

	s.SetEntryOverrides(ctx, []configstoreTables.TableDatasheetOverride{
		{ID: "endpoints", Provider: "openai", Model: "gpt-4o", Mode: "chat", ParamsPatch: json.RawMessage(`{"supported_endpoints":["/v1/chat/completions","/v1/responses"]}`)},
	})
	assert.True(t, s.IsRequestTypeSupported("gpt-4o", schemas.ResponsesRequest))

	s.SetEntryOverrides(ctx, nil)
	assert.False(t, s.IsRequestTypeSupported("gpt-4o", schemas.ResponsesRequest), "removing the override restores the upstream index")
}

// Params patches for one model apply in order, so a later patch that adds part
// of an object back does not undo an earlier null on that object.
func TestEntryOverride_ParamsPatchesApplyInOrder(t *testing.T) {
	ctx := context.Background()
	s, cs, _ := newEntryOverrideStore(t, gpt4oDatasheet)
	require.NoError(t, cs.UpsertModelParametersBatch(ctx, []configstoreTables.TableModelParameters{
		{Model: "gpt-4o", Data: `{"provider":"openai","mode":"chat","server_tools":{"a":"x","b":"y"}}`},
	}))
	_, err := s.LoadModelParamsFromDB(ctx)
	require.NoError(t, err)
	saveEntryOverride(t, s, cs, configstoreTables.TableDatasheetOverride{
		ID: "all", Provider: "openai", Model: "gpt-4o", ParamsPatch: json.RawMessage(`{"server_tools": null}`),
	})
	saveEntryOverride(t, s, cs, configstoreTables.TableDatasheetOverride{
		ID: "chat", Provider: "openai", Model: "gpt-4o", Mode: "chat", ParamsPatch: json.RawMessage(`{"server_tools": {"c": "z"}}`),
	})
	caps, err := s.LoadModelCapabilities(ctx, schemas.OpenAI, "gpt-4o")
	require.NoError(t, err)
	require.NotNil(t, caps)
	assert.Equal(t, map[string]string{"c": "z"}, caps.ServerTools)
}

// A custom model that only a params override adds must leave the parameter
// indexes when that override is removed, even when the upstream params feed is
// empty and so never rebuilds them: with a config store (empty table) and
// without one (empty in-memory sheet).
func TestEntryOverride_RemovingLastParamsOverrideDropsCustomModel(t *testing.T) {
	ctx := context.Background()
	custom := configstoreTables.TableDatasheetOverride{
		ID: "custom", Provider: "openai", Model: "my-model", Mode: "chat", IsCustom: true,
		ParamsPatch: json.RawMessage(`{"mode": "chat", "supports_function_calling": true}`),
	}
	stores := map[string]func(t *testing.T) *Store{
		"empty params table": func(t *testing.T) *Store {
			cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
				Enabled: true, Type: configstore.ConfigStoreTypeSQLite,
				Config: &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
			}, bifrost.NewDefaultLogger(schemas.LogLevelError))
			require.NoError(t, err)
			t.Cleanup(func() { _ = cs.Close(ctx) })
			return New(cs, bifrost.NewDefaultLogger(schemas.LogLevelError), Config{})
		},
		"empty sheet without a store": func(t *testing.T) *Store {
			s := newTestStore()
			s.applyModelParameters(map[string]json.RawMessage{})
			return s
		},
	}
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			require.NoError(t, s.UpsertEntryOverride(ctx, custom))
			require.True(t, s.IsRequestTypeSupported("my-model", schemas.ChatCompletionRequest), "the override adds the model")
			require.Contains(t, s.GetSupportedParameters("my-model"), "tools")

			s.DeleteEntryOverride(ctx, custom.ID)
			assert.False(t, s.IsRequestTypeSupported("my-model", schemas.ChatCompletionRequest), "the removed override no longer answers for its model")
			assert.Nil(t, s.GetSupportedParameters("my-model"), "the removed override no longer lists parameters")
		})
	}
}

// The listing's provider filter matches regardless of case, like the lookups.
func TestEntryOverride_ListProviderFilterIgnoresCase(t *testing.T) {
	s := newTestStore()
	s.SetPricingRowsForTest([]configstoreTables.TableModelPricing{{Model: "gpt-4o", Provider: "azure", Mode: "chat", InputCostPerToken: bifrost.Ptr(1.0)}})
	s.SetEntryOverrides(context.Background(), []configstoreTables.TableDatasheetOverride{
		{ID: "edit", Provider: "Azure", Model: "gpt-4o", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 2}`)},
		{ID: "gone", Provider: "Azure", Model: "retired", Mode: "chat", PricingPatch: json.RawMessage(`{"input_cost_per_token": 3}`)},
	})
	entries, total := s.ListDatasheetEntries(DatasheetEntryQuery{Provider: "Azure"})
	require.Equal(t, 2, total, "a mixed-case filter finds the row and the orphan override")
	byModel := map[string]DatasheetEntry{}
	for _, e := range entries {
		byModel[e.Model] = e
	}
	require.Contains(t, byModel, "gpt-4o")
	require.NotNil(t, byModel["gpt-4o"].Effective)
	assert.Equal(t, 2.0, *byModel["gpt-4o"].Effective.InputCostPerToken)
	require.Contains(t, byModel, "retired")
	assert.Nil(t, byModel["retired"].Effective, "the override for an unlisted model is the orphan")
}

// Every pricing mode a request is looked up under is a valid custom mode.
func TestPricingLookupModesCoverEveryRequestType(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "..", "..", "core", "schemas", "bifrost.go"), nil, 0)
	require.NoError(t, err)
	var seen int
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			if ident, ok := vs.Type.(*ast.Ident); !ok || ident.Name != "RequestType" || len(vs.Values) != 1 {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok {
				continue
			}
			value, err := strconv.Unquote(lit.Value)
			require.NoError(t, err)
			seen++
			if mode := normalizeRequestType(schemas.RequestType(value)); mode != "unknown" {
				assert.True(t, IsPricingLookupMode(mode), "%s is looked up under mode %q", value, mode)
			}
		}
	}
	require.Greater(t, seen, 20, "parsed the RequestType constants")
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
		{"baseline not an object", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", PricingPatch: json.RawMessage(`{"input_cost_per_token":1}`), Baseline: json.RawMessage(`[1]`)}, "baseline must be a JSON object"},
		{"valid edit", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", PricingPatch: json.RawMessage(`{"context_length":1000,"is_deprecated":true,"input_cost_per_token":null}`)}, ""},
		{"valid custom without patches", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", Mode: "chat", IsCustom: true}, ""},
		{"custom with a mode pricing never looks up", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", Mode: "chat_completion", IsCustom: true}, `mode "chat_completion" is not a pricing mode`},
		{"valid custom realtime model priced as responses", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", Mode: "responses", IsCustom: true}, ""},
		{"edit keeps any mode its row has", configstoreTables.TableDatasheetOverride{Provider: "openai", Model: "m", Mode: "moderation", PricingPatch: json.RawMessage(`{"input_cost_per_token":1}`)}, ""},
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

func TestMergePatch(t *testing.T) {
	target := map[string]any{"a": 1.0, "b": map[string]any{"x": 1.0, "y": 2.0}, "c": 3.0}
	got := mergePatch(target, map[string]any{"a": nil, "b": map[string]any{"y": nil, "z": 4.0}, "d": "new"})
	assert.Equal(t, map[string]any{"b": map[string]any{"x": 1.0, "z": 4.0}, "c": 3.0, "d": "new"}, got)

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
