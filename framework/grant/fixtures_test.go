package grant

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

// Fixtures shared by this package's tests. The builders cover the three shapes a permit takes in
// almost every case (permitting providers wholesale, permitting named models, permitting one MCP
// client's tools), so a test that needs something else builds it inline where it can be read.

// ptr returns a pointer to v, for the optional fields a fixture sets inline.
func ptr[T any](v T) *T {
	return &v
}

// permitSpec names the arguments of NewPermit, so a fixture built inline reads as what it grants
// rather than as a row of positional values.
type permitSpec struct {
	Type      PermitType
	ID        string
	Name      string
	IsActive  bool
	IsExpired bool

	ProviderPermits []schemas.ProviderPermit
	MCPPermits      []schemas.MCPPermit
}

// newPermit builds a Permit from a named spec.
func newPermit(spec permitSpec) *Permit {
	return NewPermit(spec.Type, spec.ID, spec.Name, spec.IsActive, spec.IsExpired, spec.ProviderPermits, spec.MCPPermits)
}

// held wraps the permits a caller holds for NewAccess, in the order given. A nil entry is passed
// through as a typed nil, which NewAccess is expected to drop.
func held(permits ...*Permit) []schemas.Permit {
	bases := make([]schemas.Permit, 0, len(permits))
	for _, permit := range permits {
		bases = append(bases, permit)
	}
	return bases
}

// fakeProviderConfigSource stands in for the deployment's configured providers.
type fakeProviderConfigSource struct {
	configuredProviders map[schemas.ModelProvider]configstore.ProviderConfig
}

func (f *fakeProviderConfigSource) GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig {
	return f.configuredProviders
}

// catalogMatcher resolves allowed-models entries through the catalog the way a deployment does,
// against the provider configuration the source holds.
func catalogMatcher(catalog *modelcatalog.ModelCatalog, source *fakeProviderConfigSource) ModelMatcher {
	return func(provider, model string, allowed []string) bool {
		cfg, ok := source.GetConfiguredProviders()[schemas.ModelProvider(provider)]
		var cfgPtr *configstore.ProviderConfig
		if ok {
			cfgPtr = &cfg
		}
		return catalog.IsModelAllowedForProvider(schemas.ModelProvider(provider), model, cfgPtr, schemas.WhiteList(allowed))
	}
}

// permitWithProviders builds a permit permitting each provider for all models.
func permitWithProviders(permitType PermitType, id, name string, providers ...string) *Permit {
	providerPermits := make([]schemas.ProviderPermit, 0, len(providers))
	for _, provider := range providers {
		providerPermits = append(providerPermits, schemas.ProviderPermit{
			Provider:      provider,
			AllowedModels: []string{"*"},
			KeyIDs:        []string{"*"},
		})
	}
	return NewPermit(permitType, id, name, true, false, providerPermits, nil)
}

// permitWithModels builds a permit permitting exactly the listed models on provider.
func permitWithModels(permitType PermitType, id, name, provider string, models ...string) *Permit {
	return NewPermit(permitType, id, name, true, false, []schemas.ProviderPermit{{
		Provider:      provider,
		AllowedModels: models,
		KeyIDs:        []string{"*"},
	}}, nil)
}

// permitWithTools builds a permit permitting the listed tools of one MCP client.
func permitWithTools(permitType PermitType, id, name, client string, tools ...string) *Permit {
	return NewPermit(permitType, id, name, true, false, nil, []schemas.MCPPermit{{
		Client:     client + "-id",
		ClientName: client,
		Tools:      tools,
	}})
}

func limitIDs(limits []schemas.Limit) []string {
	ids := make([]string, 0, len(limits))
	for _, limit := range limits {
		ids = append(ids, limit.ID)
	}
	return ids
}
