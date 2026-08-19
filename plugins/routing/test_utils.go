package routing

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grants"
	"github.com/maximhq/bifrost/plugins/routing/rules"
)

// MockGovernance stands in for the governance plugin. It serves the canned access and usage state its
// embedded store provides, and records the provider materialization calls the routing hook makes so tests
// can assert what model they were handed.
type MockGovernance struct {
	*rules.MockGovernanceStore

	// Access is what ResolveEffectiveAccess answers with. Nil stands for a request whose credential
	// resolves to nothing, which routing declines to rewrite.
	Access *grants.EffectiveAccess

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
	// Unrestricted by default: the shape a request that presented nothing carries, which is what most
	// routing tests are exercising.
	return &MockGovernance{
		MockGovernanceStore: rules.NewMockGovernanceStore(),
		Access:              grants.NewEffectiveAccess(grants.UnrestrictedGrant(), nil, "", nil, nil),
	}
}

func (m *MockGovernance) ResolveEffectiveAccess(_ *schemas.BifrostContext) *grants.EffectiveAccess {
	return m.Access
}

func (m *MockGovernance) PublishRoutingAllowlist(_ *schemas.BifrostContext, modelStr string) {
	m.AllowlistModels = append(m.AllowlistModels, modelStr)
}

func (m *MockGovernance) LoadBalanceProvider(_ *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	_, model, _ := req.GetRequestFields()
	m.LoadBalancedModels = append(m.LoadBalancedModels, model)
	if m.OnLoadBalance != nil {
		m.OnLoadBalance(req)
	}
	return m.LoadBalanceErr
}
