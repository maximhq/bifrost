package routing

import (
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing/rules"
)

// MockGovernance stands in for the governance plugin. It serves the canned virtual key and
// usage state its embedded store provides, and records the provider materialization calls the
// routing hook makes so tests can assert what model they were handed.
type MockGovernance struct {
	*rules.MockGovernanceStore

	// AllowlistModels records the model passed to each PublishRoutingAllowlist call, in order.
	AllowlistModels []string
	// LoadBalancedModels records the model on the request at each LoadBalanceProvider call.
	LoadBalancedModels []string
	// LoadBalanceErr, when set, is returned by LoadBalanceProvider.
	LoadBalanceErr error
	// OnLoadBalance, when set, runs inside LoadBalanceProvider. Tests use it to stand in for
	// the real load balancer's provider pick and model refinement.
	OnLoadBalance func(req *schemas.BifrostRequest)
}

func NewMockGovernance() *MockGovernance {
	return &MockGovernance{MockGovernanceStore: rules.NewMockGovernanceStore()}
}

func (m *MockGovernance) PublishRoutingAllowlist(_ *schemas.BifrostContext, _ *configstoreTables.TableVirtualKey, modelStr string) {
	m.AllowlistModels = append(m.AllowlistModels, modelStr)
}

func (m *MockGovernance) LoadBalanceProvider(_ *schemas.BifrostContext, req *schemas.BifrostRequest, _ *configstoreTables.TableVirtualKey) error {
	_, model, _ := req.GetRequestFields()
	m.LoadBalancedModels = append(m.LoadBalancedModels, model)
	if m.OnLoadBalance != nil {
		m.OnLoadBalance(req)
	}
	return m.LoadBalanceErr
}

// AttributeRoutingEmbeddingCost is a no-op; tests assert usage through the
// warmup observer instead of the budget ledger.
func (m *MockGovernance) AttributeRoutingEmbeddingCost(provider schemas.ModelProvider, model string, inputTokens int) {
}
