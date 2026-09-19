package governance

import (
	"strings"
	"sync"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
)

// A permit holder's own per-model limits are ordinary model configs, stored under a scope keyed by
// the permit's identity. Two things have to be known about a permit type to read them back: the
// scope column its rows carry, and the holder kind a refusal attributes them to.
//
// Both are properties of the holder, not of this package. Virtual keys and projects are OSS's own
// and are registered below; every other kind - the access profile a user holds, and the profiles a
// team, business unit or customer holds - is an enterprise concept whose scope names live there, so
// the enterprise build registers those at startup, the way it already registers the scope values
// themselves with configstoreTables.RegisterModelConfigScope.
//
// Registering rather than switching is what keeps a holder kind from being read as another one: an
// unregistered type resolves to its own type name as a scope, where nothing is stored, and to the
// generic model-config holder. It reads as "this holder declared no per-model limits" instead of
// quietly answering with another holder's rows.
type permitModelConfigScope struct {
	scope string
	kind  grant.LimitHolderKind
}

var (
	permitModelConfigScopesMu sync.RWMutex
	permitModelConfigScopes   = map[string]permitModelConfigScope{
		string(grant.PermitVirtualKey): {scope: configstoreTables.ModelConfigScopeVirtualKey, kind: grant.LimitHolderVirtualKeyModelConfig},
		string(grant.PermitProject):    {scope: configstoreTables.ModelConfigScopeProject, kind: grant.LimitHolderProjectModelConfig},
	}
)

// RegisterPermitModelConfigScope records where one permit type's per-model limits are stored and
// which holder kind they are attributed to. Intended to be called once at process startup, beside
// RegisterModelConfigScope for the same scope; safe to call concurrently. Blank input is ignored.
func RegisterPermitModelConfigScope(permitType grant.PermitType, scope string, kind grant.LimitHolderKind) {
	name := strings.TrimSpace(string(permitType))
	trimmedScope := strings.TrimSpace(scope)
	if name == "" || trimmedScope == "" || strings.TrimSpace(string(kind)) == "" {
		return
	}
	permitModelConfigScopesMu.Lock()
	permitModelConfigScopes[name] = permitModelConfigScope{scope: trimmedScope, kind: kind}
	permitModelConfigScopesMu.Unlock()
}

// lookupPermitModelConfigScope reads the registry.
func lookupPermitModelConfigScope(permitType string) (permitModelConfigScope, bool) {
	permitModelConfigScopesMu.RLock()
	defer permitModelConfigScopesMu.RUnlock()
	entry, ok := permitModelConfigScopes[permitType]
	return entry, ok
}
