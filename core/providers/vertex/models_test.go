package vertex

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func publisherModelsResponse(names ...string) *VertexListPublisherModelsResponse {
	response := &VertexListPublisherModelsResponse{}
	for _, name := range names {
		response.PublisherModels = append(response.PublisherModels, VertexPublisherModel{
			Name: "publishers/google/models/" + name,
		})
	}
	return response
}

func modelIDs(response *schemas.BifrostListModelsResponse) []string {
	ids := make([]string, 0, len(response.Data))
	for _, model := range response.Data {
		ids = append(ids, model.ID)
	}
	return ids
}

// TestPublisherModelsWildcardWithAliasesMergesCatalog verifies that a wildcard
// allowlist combined with configured deployments (aliases) produces the full
// catalog PLUS the alias entries, instead of the aliases replacing the catalog.
// This is the conversion listModelsByKey relies on when it falls through to the
// Model Garden fetch for wildcard-allowlist keys with deployments configured.
func TestPublisherModelsWildcardWithAliasesMergesCatalog(t *testing.T) {
	t.Parallel()

	response := publisherModelsResponse("gemini-2.5-pro", "gemini-2.5-flash")
	aliases := schemas.KeyAliases{
		"my-pro":       {ModelID: "gemini-2.5-pro"},
		"custom-model": {ModelID: "gemini-9.9-unreleased"},
	}

	result := response.ToBifrostListModelsResponse(schemas.WhiteList{"*"}, nil, aliases, false)

	got := make(map[string]bool)
	for _, id := range modelIDs(result) {
		got[id] = true
	}

	// Catalog models must survive.
	for _, want := range []string{"vertex/gemini-2.5-pro", "vertex/gemini-2.5-flash"} {
		if !got[want] {
			t.Errorf("expected catalog model %q in response, got %v", want, modelIDs(result))
		}
	}
	// Alias matching a catalog model is surfaced alongside it.
	if !got["vertex/my-pro"] {
		t.Errorf("expected alias %q in response, got %v", "vertex/my-pro", modelIDs(result))
	}
	// Alias not present in the catalog is backfilled.
	if !got["vertex/custom-model"] {
		t.Errorf("expected backfilled alias %q in response, got %v", "vertex/custom-model", modelIDs(result))
	}

	// Alias entries must carry the provider-specific model ID.
	for _, model := range result.Data {
		if model.ID == "vertex/my-pro" {
			if model.Alias == nil || *model.Alias != "gemini-2.5-pro" {
				t.Errorf("expected alias value %q for vertex/my-pro, got %v", "gemini-2.5-pro", model.Alias)
			}
		}
	}
}

// TestBuildResponseFromConfigRestrictedAllowlist verifies the config fast path
// used when an explicit allowlist is configured: deployments filtered by the
// allowlist plus remaining allowlist entries, no catalog access.
func TestBuildResponseFromConfigRestrictedAllowlist(t *testing.T) {
	t.Parallel()

	deployments := schemas.KeyAliases{
		"my-pro":   {ModelID: "gemini-2.5-pro"},
		"excluded": {ModelID: "gemini-2.5-flash"},
	}
	allowed := schemas.WhiteList{"my-pro", "gemini-2.0-flash"}

	result := buildResponseFromConfig(deployments, allowed, nil)

	got := make(map[string]bool)
	for _, id := range modelIDs(result) {
		got[id] = true
	}

	if !got["vertex/my-pro"] {
		t.Errorf("expected allowed deployment vertex/my-pro, got %v", modelIDs(result))
	}
	if !got["vertex/gemini-2.0-flash"] {
		t.Errorf("expected allowlist entry vertex/gemini-2.0-flash, got %v", modelIDs(result))
	}
	if got["vertex/excluded"] {
		t.Errorf("deployment not in allowlist must be filtered out, got %v", modelIDs(result))
	}
}

// TestBuildResponseFromConfigWildcardAllowlistDeploymentsOnly documents the
// auth-fallback shape: with a wildcard allowlist, only deployments are listed
// (used when Model Garden credentials are unavailable).
func TestBuildResponseFromConfigWildcardAllowlistDeploymentsOnly(t *testing.T) {
	t.Parallel()

	deployments := schemas.KeyAliases{
		"my-pro": {ModelID: "gemini-2.5-pro"},
	}

	result := buildResponseFromConfig(deployments, schemas.WhiteList{"*"}, nil)

	ids := modelIDs(result)
	if len(ids) != 1 || ids[0] != "vertex/my-pro" {
		t.Errorf("expected only deployment vertex/my-pro, got %v", ids)
	}
}
