package modelcatalogresolver

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

func TestResolveProviderFromCatalogPrefersOpenAIForHumeIntegration(t *testing.T) {
	catalog := modelcatalog.NewTestCatalog(nil)
	catalog.UpsertLive(schemas.Azure, "azure-key", false, []string{"shared-model"})
	catalog.UpsertLive(schemas.OpenAI, "openai-key", false, []string{"shared-model"})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "hume")

	provider, candidates := ResolveProviderFromCatalog(ctx, catalog, "shared-model")
	if provider != schemas.OpenAI {
		t.Fatalf("ResolveProviderFromCatalog() provider = %q, want %q; candidates = %v", provider, schemas.OpenAI, candidates)
	}
}
