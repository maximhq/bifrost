package grants

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// Fixtures shared by this package's tests. The builders cover the three shapes a grant takes in
// almost every case — permitting providers wholesale, permitting named models, permitting one MCP
// client's tools — so a test that needs something else builds it inline where it can be read.

// fakeProviderConfigSource stands in for the deployment's configured providers.
type fakeProviderConfigSource struct {
	configuredProviders map[schemas.ModelProvider]configstore.ProviderConfig
}

func (f *fakeProviderConfigSource) GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig {
	return f.configuredProviders
}

// grantWithProviders builds a grant permitting each provider for all models.
func grantWithProviders(grantType GrantType, id, name string, providers ...string) *Grant {
	grant := &Grant{Type: grantType, ID: id, Name: name}
	for _, provider := range providers {
		grant.ProviderConfigGrants = append(grant.ProviderConfigGrants, ProviderConfigGrant{
			Provider:      provider,
			AllowedModels: schemas.WhiteList{"*"},
			KeyIDs:        schemas.WhiteList{"*"},
		})
	}
	return grant
}

// grantWithModels builds a grant permitting exactly the listed models on provider.
func grantWithModels(grantType GrantType, id, name, provider string, models ...string) *Grant {
	return &Grant{
		Type: grantType,
		ID:   id,
		Name: name,
		ProviderConfigGrants: []ProviderConfigGrant{{
			Provider:      provider,
			AllowedModels: schemas.WhiteList(models),
			KeyIDs:        schemas.WhiteList{"*"},
		}},
	}
}

// grantWithTools builds a grant permitting the listed tools of one MCP client.
func grantWithTools(grantType GrantType, id, name, client string, tools ...string) *Grant {
	return &Grant{
		Type: grantType,
		ID:   id,
		Name: name,
		MCPConfigGrants: []MCPConfigGrant{{
			Client:     client + "-id",
			ClientName: client,
			Tools:      schemas.WhiteList(tools),
		}},
	}
}

func limitIDs(limits []Limit) []string {
	ids := make([]string, 0, len(limits))
	for _, limit := range limits {
		ids = append(ids, limit.ID)
	}
	return ids
}
