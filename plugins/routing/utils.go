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
