package governance

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
)

// AttributeRoutingEmbeddingCost bills one warmup/boot embedding made by
// semantic complexity routing to the admin-owned provider-level and global
// model-level budgets — the same ledger the usage tracker bills request
// traffic to. No VK/team/customer budget is ever touched: there is no tenant
// to bill, only the platform-level budgets on the embedding provider/model.
// Called by the routing plugin only when count_toward_budgets is enabled;
// hot-path classification embeds are attributed through the triggering
// request's RoutingDebug stamp instead.
func (p *GovernancePlugin) AttributeRoutingEmbeddingCost(provider schemas.ModelProvider, model string, inputTokens int) {
	if p.modelCatalog == nil || p.store == nil {
		return
	}
	providerStr := string(provider)
	tokens := inputTokens
	cost := p.modelCatalog.CalculateRoutingCallCost(schemas.BifrostRoutingCall{
		ProviderUsed: &providerStr,
		ModelUsed:    &model,
		InputTokens:  &tokens,
	}, nil)
	if cost <= 0 {
		return
	}
	ctx := p.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := p.store.UpdateProviderAndModelBudgetUsageInMemory(ctx, model, provider, cost); err != nil && p.logger != nil {
		p.logger.Error("failed to attribute warmup embedding cost to provider/model budgets: %v", err)
	}
}
