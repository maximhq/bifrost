package handlers

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type pricingOverrideTestGovernanceManager struct{}

func (pricingOverrideTestGovernanceManager) GetGovernanceData(ctx context.Context) *governance.GovernanceData {
	return nil
}
func (pricingOverrideTestGovernanceManager) ResetBudgetUsageInMemory(context.Context, BudgetUsageResetOwner, []string) error {
	return nil
}
func (pricingOverrideTestGovernanceManager) AdoptCalendarAlignmentInMemory(context.Context, BudgetUsageResetOwner, []string, []string) error {
	return nil
}
func (pricingOverrideTestGovernanceManager) ReloadVirtualKey(context.Context, string) (*configstoreTables.TableVirtualKey, error) {
	return nil, nil
}
func (pricingOverrideTestGovernanceManager) RemoveVirtualKey(context.Context, string) error {
	return nil
}
func (pricingOverrideTestGovernanceManager) ReloadTeam(context.Context, string) (*configstoreTables.TableTeam, error) {
	return nil, nil
}
func (pricingOverrideTestGovernanceManager) RemoveTeam(context.Context, string) error {
	return nil
}
func (pricingOverrideTestGovernanceManager) ReloadCustomer(context.Context, string) (*configstoreTables.TableCustomer, error) {
	return nil, nil
}
func (pricingOverrideTestGovernanceManager) RemoveCustomer(context.Context, string) error {
	return nil
}
func (pricingOverrideTestGovernanceManager) ReloadModelConfig(context.Context, string) (*configstoreTables.TableModelConfig, error) {
	return nil, nil
}
func (pricingOverrideTestGovernanceManager) RemoveModelConfig(context.Context, string) error {
	return nil
}
func (pricingOverrideTestGovernanceManager) ReloadProvider(context.Context, schemas.ModelProvider, bool) (*configstoreTables.TableProvider, error) {
	return nil, nil
}
func (pricingOverrideTestGovernanceManager) RemoveProvider(context.Context, schemas.ModelProvider) error {
	return nil
}
func (pricingOverrideTestGovernanceManager) ReloadRoutingRule(context.Context, string) error {
	return nil
}
func (pricingOverrideTestGovernanceManager) RemoveRoutingRule(context.Context, string) error {
	return nil
}
func (pricingOverrideTestGovernanceManager) UpsertPricingOverride(context.Context, *configstoreTables.TablePricingOverride) error {
	return nil
}
func (pricingOverrideTestGovernanceManager) DeletePricingOverride(context.Context, string) error {
	return nil
}

func setupPricingOverrideHandlerStore(t *testing.T) configstore.ConfigStore {
	t.Helper()

	dbPath := t.TempDir() + "/config.db"
	store, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config: &configstore.SQLiteConfig{
			Path: dbPath,
		},
	}, &mockLogger{})
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = os.Remove(dbPath)
	})
	return store
}

func newTestRequestCtx(body string) *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.SetBodyString(body)

	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)
	return ctx
}

func TestUpdatePricingOverride_ReplacesFullBody(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	handler := &GovernanceHandler{
		configStore:       store,
		governanceManager: pricingOverrideTestGovernanceManager{},
	}

	now := time.Now().UTC()
	override := configstoreTables.TablePricingOverride{
		ID:               "override-1",
		Name:             "Original",
		ScopeKind:        string(modelcatalog.ScopeKindGlobal),
		MatchType:        string(modelcatalog.MatchTypeExact),
		Pattern:          "gpt-4.1",
		CreatedAt:        now,
		UpdatedAt:        now,
		PricingPatchJSON: `{"input_cost_per_token":1,"output_cost_per_token":2}`,
		RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
	}
	require.NoError(t, store.CreatePricingOverride(context.Background(), &override))

	// Patch replaces in full: send only input_cost_per_token.
	// output_cost_per_token must be absent from the stored patch afterwards,
	// confirming full-replace (not merge) semantics.
	body := `{
		"name":"Updated",
		"scope_kind":"global",
		"match_type":"exact",
		"pattern":"gpt-4.1",
		"request_types":["chat_completion"],
		"patch":{"input_cost_per_token":1.5}
	}`
	ctx := newTestRequestCtx(body)
	ctx.SetUserValue("id", override.ID)

	handler.updatePricingOverride(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	stored, err := store.GetPricingOverrideByID(context.Background(), override.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated", stored.Name)

	var patch modelcatalog.PricingOptions
	require.NoError(t, json.Unmarshal([]byte(stored.PricingPatchJSON), &patch))
	// Sent field must reflect the new value.
	require.NotNil(t, patch.InputCostPerToken)
	assert.Equal(t, 1.5, *patch.InputCostPerToken)
	// Omitted field must be cleared — patch is always fully replaced, not merged.
	assert.Nil(t, patch.OutputCostPerToken)
	assert.Empty(t, stored.ConfigHash)
}

func TestUpdatePricingOverride_CacheCreationAbove12hrFields(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	handler := &GovernanceHandler{configStore: store, governanceManager: pricingOverrideTestGovernanceManager{}}

	now := time.Now().UTC()
	override := configstoreTables.TablePricingOverride{
		ID: "override-1hr", Name: "12h", ScopeKind: string(modelcatalog.ScopeKindGlobal),
		MatchType: string(modelcatalog.MatchTypeExact), Pattern: "gpt-5.6-sol", CreatedAt: now, UpdatedAt: now,
		PricingPatchJSON: `{"input_cost_per_token":1}`, RequestTypes: []schemas.RequestType{schemas.ResponsesRequest},
	}
	require.NoError(t, store.CreatePricingOverride(context.Background(), &override))

	cases := []struct {
		key  string
		want float64
		get  func(modelcatalog.PricingOptions) *float64
	}{
		{"cache_creation_input_token_cost_above_12hr", 0.00001, func(p modelcatalog.PricingOptions) *float64 { return p.CacheCreationInputTokenCostAbove12hr }},
		{"cache_creation_input_token_cost_above_12hr_above_272k_tokens", 0.00002, func(p modelcatalog.PricingOptions) *float64 {
			return p.CacheCreationInputTokenCostAbove12hrAbove272kTokens
		}},
		{"cache_creation_input_token_cost_above_12hr_priority", 0.00003, func(p modelcatalog.PricingOptions) *float64 { return p.CacheCreationInputTokenCostAbove12hrPriority }},
		{"cache_creation_input_token_cost_above_12hr_above_272k_tokens_priority", 0.00004, func(p modelcatalog.PricingOptions) *float64 {
			return p.CacheCreationInputTokenCostAbove12hrAbove272kTokensPriority
		}},
		{"cache_creation_input_token_cost_above_12hr_flex", 0.000005, func(p modelcatalog.PricingOptions) *float64 { return p.CacheCreationInputTokenCostAbove12hrFlex }},
		{"cache_creation_input_token_cost_above_12hr_above_272k_tokens_flex", 0.00001, func(p modelcatalog.PricingOptions) *float64 {
			return p.CacheCreationInputTokenCostAbove12hrAbove272kTokensFlex
		}},
		{"cache_creation_input_token_cost_above_12hr_ultrafast", 0.00015, func(p modelcatalog.PricingOptions) *float64 { return p.CacheCreationInputTokenCostAbove12hrUltrafast }},
		{"cache_creation_input_token_cost_above_12hr_above_272k_tokens_ultrafast", 0.0003, func(p modelcatalog.PricingOptions) *float64 {
			return p.CacheCreationInputTokenCostAbove12hrAbove272kTokensUltrafast
		}},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			ctx := newTestRequestCtx(`{"patch":{"` + tc.key + `":` + strconv.FormatFloat(tc.want, 'g', -1, 64) + `}}`)
			ctx.SetUserValue("id", override.ID)
			handler.updatePricingOverride(ctx)
			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

			stored, err := store.GetPricingOverrideByID(context.Background(), override.ID)
			require.NoError(t, err)
			var patch modelcatalog.PricingOptions
			require.NoError(t, json.Unmarshal([]byte(stored.PricingPatchJSON), &patch))
			got := tc.get(patch)
			require.NotNil(t, got, stored.PricingPatchJSON)
			assert.Equal(t, tc.want, *got)
		})
	}
}

func (pricingOverrideTestGovernanceManager) ModelConfigIndexKey(model string, provider *string) string {
	return governance.CanonicalModelConfigName(model, provider)
}
