package routing

import (
	"context"
	"fmt"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// resolveVirtualKey loads the virtual key named on the request. Returns ok=false when the
// request carries a virtual key that is unusable (unknown, inactive or expired), which skips
// rule evaluation the same way the downstream governance checks skip such a request.
// A request with no virtual key at all resolves to (nil, true): rules scoped to the user or
// to global still apply.
func (p *RoutingPlugin) resolveVirtualKey(ctx *schemas.BifrostContext) (*configstoreTables.TableVirtualKey, bool) {
	virtualKeyValue := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyVirtualKey)
	if virtualKeyValue == "" {
		return nil, true
	}
	virtualKey, found := p.governance.GetVirtualKey(ctx, virtualKeyValue)
	if !found || virtualKey == nil || !virtualKey.IsActiveValue() || virtualKey.IsExpiredAt(time.Now().UTC()) {
		return nil, false
	}
	return virtualKey, true
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
