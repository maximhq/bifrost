package handlers

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/pricingoverrides"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type pricingOverrideTestGovernanceManager struct{}

func (pricingOverrideTestGovernanceManager) GetGovernanceData() *governance.GovernanceData {
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
func (pricingOverrideTestGovernanceManager) ReloadProvider(context.Context, schemas.ModelProvider) (*configstoreTables.TableProvider, error) {
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

func TestPatchPricingOverride_MergesPatch(t *testing.T) {
	SetLogger(&mockLogger{})
	store := setupPricingOverrideHandlerStore(t)
	handler := &GovernanceHandler{
		configStore:       store,
		governanceManager: pricingOverrideTestGovernanceManager{},
		modelCatalog:      &modelcatalog.ModelCatalog{},
	}

	inputCost := 1.0
	outputCost := 2.0
	override := configstoreTables.TablePricingOverride{
		ID:        "override-1",
		Name:      "Config Managed",
		ScopeKind: pricingoverrides.ScopeKindGlobal,
		MatchType: pricingoverrides.MatchTypeExact,
		Pattern:   "gpt-4.1",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		RequestTypes: []schemas.RequestType{
			schemas.ChatCompletionRequest,
		},
		Patch: pricingoverrides.Patch{
			InputCostPerToken:  &inputCost,
			OutputCostPerToken: &outputCost,
		},
	}
	require.NoError(t, store.CreatePricingOverride(context.Background(), &override))

	ctx := newTestRequestCtx(`{"patch":{"output_cost_per_token":3.5}}`)
	ctx.SetUserValue("id", override.ID)

	handler.patchPricingOverride(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	stored, err := store.GetPricingOverrideByID(context.Background(), override.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.Patch.InputCostPerToken)
	assert.Equal(t, inputCost, *stored.Patch.InputCostPerToken)
	require.NotNil(t, stored.Patch.OutputCostPerToken)
	assert.Equal(t, 3.5, *stored.Patch.OutputCostPerToken)
	assert.Empty(t, stored.ConfigHash)
}

func TestProviderHandlers_RejectProviderLevelPricingOverrides(t *testing.T) {
	SetLogger(&mockLogger{})

	tests := []struct {
		name    string
		handler func(*ProviderHandler, *fasthttp.RequestCtx)
		prepare func(*fasthttp.RequestCtx)
	}{
		{
			name: "add",
			handler: func(h *ProviderHandler, ctx *fasthttp.RequestCtx) {
				h.addProvider(ctx)
			},
			prepare: func(ctx *fasthttp.RequestCtx) {},
		},
		{
			name: "update",
			handler: func(h *ProviderHandler, ctx *fasthttp.RequestCtx) {
				h.updateProvider(ctx)
			},
			prepare: func(ctx *fasthttp.RequestCtx) {
				ctx.SetUserValue("provider", "openai")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newTestRequestCtx(`{"provider":"openai","pricing_overrides":[]}`)
			tc.prepare(ctx)

			tc.handler(&ProviderHandler{}, ctx)

			assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
			assert.Contains(t, strings.ToLower(string(ctx.Response.Body())), "pricing_overrides is not a supported provider field")
		})
	}
}
