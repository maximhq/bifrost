package routing

import (
	"context"
	"fmt"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/rules"
)

// resolveGovernanceScope reports who the request is governed as, for matching rules and reading rule
// variables. Returns ok=false when the request carries a credential that may not be used (unknown,
// inactive or expired), which skips rule evaluation the same way the downstream governance checks refuse
// such a request.
//
// The scope is read off the context, where resolving the request's access stamped it. Nothing here loads
// a credential: a rule asks which key, team or customer a request belongs to, and a request granted
// access by something other than a key has those answers too.
func (p *RoutingPlugin) resolveGovernanceScope(ctx *schemas.BifrostContext) (rules.GovernanceScope, bool) {
	access, err := p.governance.ResolveAccess(ctx)
	if err != nil {
		// A request nothing settled who it is: governance refuses it downstream, and there is no
		// scope to match rules against here.
		return rules.GovernanceScope{}, false
	}
	if access == nil {
		return rules.GovernanceScope{}, true
	}
	return rules.GovernanceScope{
		VirtualKeyID:   bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID),
		VirtualKeyName: bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyName),
		UserID:         bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyUserID),
		TeamID:         bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceTeamID),
		TeamName:       bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceTeamName),
		CustomerID:     bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceCustomerID),
		CustomerName:   bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceCustomerName),
	}, true
}

// warnFallbacksOutsideAccess notes in the routing trail each rule fallback the request's access does
// not allow. Governance refuses such a fallback before it reaches its provider, and once the chain
// runs out the caller is answered with the primary's error, so without this a misconfigured rule
// reads as a fallback that never ran (#3261). The chain is left as the rule wrote it: the refusal is
// governance's to make, per attempt. Only a matched rule that names fallbacks pays for the check,
// and the access it reads was already resolved for this request.
func (p *RoutingPlugin) warnFallbacksOutsideAccess(ctx *schemas.BifrostContext, ruleName string, fallbacks []schemas.Fallback) {
	access, err := p.governance.ResolveAccess(ctx)
	if err != nil || access == nil {
		return
	}
	for _, fb := range fallbacks {
		// A model check also fails for a provider the access does not allow, so an allowed fallback
		// costs one check and the provider is asked only to word a refusal.
		if access.IsModelAllowed(string(fb.Provider), fb.Model) {
			continue
		}
		reason := fmt.Sprintf("model %s is not allowed on %s for this request", fb.Model, fb.Provider)
		if !access.IsProviderAllowed(string(fb.Provider)) {
			reason = fmt.Sprintf("provider %s is not allowed for this request", fb.Provider)
		}
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelWarn, fmt.Sprintf("Rule '%s': fallback %s/%s will be refused by governance: %s", ruleName, fb.Provider, fb.Model, reason))
	}
}

// resolveAnalyzerConfigFromStoreOrArg prefers an explicitly configured analyzer config over
// the persisted one. Returns nil when neither is usable, which leaves the analyzer on its
// built-in defaults.
func resolveAnalyzerConfigFromStoreOrArg(
	ctx context.Context,
	logger schemas.Logger,
	configStore configstore.ConfigStore,
	override *complexity.AnalyzerConfig,
) *complexity.AnalyzerConfig {
	if override != nil {
		cfg, err := complexity.ValidateAndNormalize(override)
		if err != nil {
			if logger != nil {
				logger.Warn("invalid complexity analyzer config from routing config: %v", err)
			}
		} else if cfg != nil {
			return cfg
		}
	}
	if configStore != nil {
		cfg, err := configStore.GetComplexityAnalyzerConfig(ctx)
		if err != nil {
			if logger != nil {
				logger.Warn("failed to load complexity analyzer config from store: %v", err)
			}
		} else if cfg != nil {
			return cfg
		}
	}
	return nil
}

// maxLoggedExemplarChars bounds one operator-supplied tier phrase echoed into
// a routing log line.
const maxLoggedExemplarChars = 120

// truncateExemplarForLog bounds one operator-supplied tier phrase echoed into a
// routing log. It cuts on runes so a multi-byte phrase cannot be split
// mid-character, and returns "" for a phrase that carries nothing to show.
func truncateExemplarForLog(phrase string) string {
	trimmed := strings.TrimSpace(phrase)
	runes := []rune(trimmed)
	if len(runes) <= maxLoggedExemplarChars {
		return trimmed
	}
	return string(runes[:maxLoggedExemplarChars]) + "..."
}

// withMatchedExemplar appends the exemplar a semantic classification landed on
// to a routing log line. A generation that cannot name its match omits the
// suffix rather than printing an empty one, which would read as a real match
// against the empty string.
func withMatchedExemplar(message, exemplar string) string {
	matched := truncateExemplarForLog(exemplar)
	if matched == "" {
		return message
	}
	return fmt.Sprintf("%s matched=%q", message, matched)
}
