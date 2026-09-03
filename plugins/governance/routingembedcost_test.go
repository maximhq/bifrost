package governance

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newOfflinePricingCatalog(t *testing.T) *modelcatalog.ModelCatalog {
	t.Helper()
	abs, err := filepath.Abs("../../framework/modelcatalog/datasheet/testdata/pricing.json")
	require.NoError(t, err)
	ds := datasheet.New(nil, NewMockLogger(), datasheet.Config{URL: "file://" + abs})
	require.NoError(t, ds.LoadFromURLIntoMemory(context.Background()))
	return modelcatalog.NewTestCatalogWithDatasheet(ds)
}

// newWarmupBudgetFixture wires a plugin over a store whose "openai" provider
// carries a provider-level budget — the admin-owned ledger warmup embeds are
// attributed to when count_toward_budgets is on.
func newWarmupBudgetFixture(t *testing.T) (*GovernancePlugin, GovernanceStore) {
	t.Helper()
	logger := NewMockLogger()
	budget := buildBudgetWithUsage("provider-budget", 1000.0, 0.0, "1d")
	budgetID := budget.ID
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		Budgets:   []configstoreTables.TableBudget{*budget},
		Providers: []configstoreTables.TableProvider{{Name: "openai", BudgetID: &budgetID}},
	}, nil)
	require.NoError(t, err)
	plugin := &GovernancePlugin{
		ctx:          context.Background(),
		store:        store,
		modelCatalog: newOfflinePricingCatalog(t),
		logger:       logger,
	}
	return plugin, store
}

func TestAttributeRoutingEmbeddingCostBillsProviderBudget(t *testing.T) {
	plugin, store := newWarmupBudgetFixture(t)

	plugin.AttributeRoutingEmbeddingCost("openai", "text-embedding-3-small", 1000)

	// 1000 tokens × $0.00000002/token (text-embedding-3-small in testdata).
	usage := store.GetGovernanceData(context.Background()).Budgets["provider-budget"].CurrentUsage
	assert.InDelta(t, 0.00002, usage, 1e-12)
}

func TestAttributeRoutingEmbeddingCostWithoutStoreOrCatalog(t *testing.T) {
	// A plugin whose catalog or store never got wired must not panic; the
	// usage is simply unattributable.
	(&GovernancePlugin{}).AttributeRoutingEmbeddingCost("openai", "text-embedding-3-small", 1000)
}
