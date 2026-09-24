package vertex

import (
	"context"
	"slices"
	"strings"
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

// garbageServiceAccountJSON parses as a valid service_account credential (so
// getAuthTokenSource succeeds) but carries an unparseable private key, so the
// token source fails at Token() without any network access.
const garbageServiceAccountJSON = `{"type":"service_account","project_id":"p","private_key_id":"x","private_key":"-----BEGIN PRIVATE KEY-----\ngarbage\n-----END PRIVATE KEY-----\n","client_email":"a@b.iam.gserviceaccount.com","token_uri":"https://oauth2.googleapis.com/token"}`

// TestListModelsByKeyAuthFallback exercises listModelsByKey end to end for the
// paths that never reach the Model Garden API: the restricted-allowlist fast
// path (auth is irrelevant) and the deployments fallback when Model Garden
// credentials are unavailable (token source creation or token acquisition
// fails). Unfiltered requests must keep surfacing the auth error rather than
// silently degrading to config-only output.
func TestListModelsByKeyAuthFallback(t *testing.T) {
	t.Parallel()

	deployments := schemas.KeyAliases{
		"my-pro": {ModelID: "gemini-2.5-pro"},
	}

	tests := []struct {
		name        string
		key         schemas.Key
		request     *schemas.BifrostListModelsRequest
		wantModels  []string
		wantErrPart string
	}{
		{
			name: "restricted allowlist skips auth entirely",
			key: schemas.Key{
				Models:  schemas.WhiteList{"my-pro", "gemini-2.0-flash"},
				Aliases: deployments,
				VertexKeyConfig: &schemas.VertexKeyConfig{
					Region: *schemas.NewSecretVar("us-central1"),
					// Invalid credentials must not matter: the fast path
					// returns before any token source is created.
					AuthCredentials: *schemas.NewSecretVar("restricted-not-json"),
				},
			},
			request:    &schemas.BifrostListModelsRequest{Provider: schemas.Vertex},
			wantModels: []string{"vertex/gemini-2.0-flash", "vertex/my-pro"},
		},
		{
			name: "wildcard deployments fall back when token source creation fails",
			key: schemas.Key{
				Models:  schemas.WhiteList{"*"},
				Aliases: deployments,
				VertexKeyConfig: &schemas.VertexKeyConfig{
					Region:          *schemas.NewSecretVar("us-central1"),
					AuthCredentials: *schemas.NewSecretVar("wildcard-source-not-json"),
				},
			},
			request:    &schemas.BifrostListModelsRequest{Provider: schemas.Vertex},
			wantModels: []string{"vertex/my-pro"},
		},
		{
			name: "wildcard deployments fall back when token acquisition fails",
			key: schemas.Key{
				Models:  schemas.WhiteList{"*"},
				Aliases: deployments,
				VertexKeyConfig: &schemas.VertexKeyConfig{
					Region:          *schemas.NewSecretVar("us-central1"),
					AuthCredentials: *schemas.NewSecretVar(garbageServiceAccountJSON),
				},
			},
			request:    &schemas.BifrostListModelsRequest{Provider: schemas.Vertex},
			wantModels: []string{"vertex/my-pro"},
		},
		{
			name: "unfiltered preserves the auth error instead of falling back",
			key: schemas.Key{
				Models:  schemas.WhiteList{"*"},
				Aliases: deployments,
				VertexKeyConfig: &schemas.VertexKeyConfig{
					Region:          *schemas.NewSecretVar("us-central1"),
					AuthCredentials: *schemas.NewSecretVar("unfiltered-not-json"),
				},
			},
			request:     &schemas.BifrostListModelsRequest{Provider: schemas.Vertex, Unfiltered: true},
			wantErrPart: "error creating auth token source",
		},
		{
			name: "wildcard without deployments preserves the auth error",
			key: schemas.Key{
				Models: schemas.WhiteList{"*"},
				VertexKeyConfig: &schemas.VertexKeyConfig{
					Region:          *schemas.NewSecretVar("us-central1"),
					AuthCredentials: *schemas.NewSecretVar("no-deployments-not-json"),
				},
			},
			request:     &schemas.BifrostListModelsRequest{Provider: schemas.Vertex},
			wantErrPart: "error creating auth token source",
		},
	}

	// A zero-value provider suffices: every case returns (or fails) before the
	// Model Garden fetch, so the HTTP client and logger are never touched.
	provider := &VertexProvider{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()

			response, bifrostErr := provider.listModelsByKey(ctx, tt.key, tt.request)

			if tt.wantErrPart != "" {
				if bifrostErr == nil {
					t.Fatalf("expected error containing %q, got response %v", tt.wantErrPart, modelIDs(response))
				}
				if !strings.Contains(bifrostErr.Error.Message, tt.wantErrPart) {
					t.Errorf("expected error containing %q, got %q", tt.wantErrPart, bifrostErr.Error.Message)
				}
				return
			}

			if bifrostErr != nil {
				t.Fatalf("unexpected error: %v", bifrostErr.Error.Message)
			}
			got := modelIDs(response)
			slices.Sort(got)
			if !slices.Equal(got, tt.wantModels) {
				t.Errorf("expected models %v, got %v", tt.wantModels, got)
			}
		})
	}
}
