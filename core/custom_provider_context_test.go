package bifrost

import (
	"context"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestSetProviderContextMetadata_MarksCustomProvider(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	config := &schemas.ProviderConfig{
		CustomProviderConfig: &schemas.CustomProviderConfig{
			CustomProviderKey:    "lmstudio",
			BaseProviderType:     schemas.OpenAI,
			SupportsResponsesAPI: schemas.Ptr(false),
		},
	}

	setProviderContextMetadata(ctx, config)

	isCustomProvider, ok := ctx.Value(schemas.BifrostContextKeyIsCustomProvider).(bool)
	if !ok || !isCustomProvider {
		t.Fatalf("expected custom provider flag to be true, got %v", ctx.Value(schemas.BifrostContextKeyIsCustomProvider))
	}
	metadata, ok := schemas.GetCustomProviderContextMetadata(ctx)
	if !ok || metadata == nil {
		t.Fatal("expected custom provider metadata to be stored in context")
	}
	if metadata.ProviderKey != "lmstudio" {
		t.Fatalf("expected custom provider key lmstudio, got %s", metadata.ProviderKey)
	}
	if metadata.BaseProviderType != schemas.OpenAI {
		t.Fatalf("expected base provider type openai, got %s", metadata.BaseProviderType)
	}
	if metadata.SupportsResponsesAPI == nil || *metadata.SupportsResponsesAPI {
		t.Fatalf("expected supports_responses_api=false metadata, got %+v", metadata.SupportsResponsesAPI)
	}
	if customProviderConfig := ctx.Value(schemas.BifrostContextKeyCustomProviderConfig); customProviderConfig != nil {
		t.Fatalf("expected full custom provider config to stay off context, got %+v", customProviderConfig)
	}
}

func TestSetProviderContextMetadata_ClearsCustomProviderValues(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyIsCustomProvider, true)
	ctx.SetValue(schemas.BifrostContextKeyCustomProviderMetadata, &schemas.CustomProviderContextMetadata{ProviderKey: "lmstudio"})
	ctx.SetValue(schemas.BifrostContextKeyCustomProviderConfig, &schemas.CustomProviderConfig{CustomProviderKey: "lmstudio"})

	setProviderContextMetadata(ctx, &schemas.ProviderConfig{})

	isCustomProvider, ok := ctx.Value(schemas.BifrostContextKeyIsCustomProvider).(bool)
	if !ok || isCustomProvider {
		t.Fatalf("expected custom provider flag to be false, got %v", ctx.Value(schemas.BifrostContextKeyIsCustomProvider))
	}
	if metadata := ctx.Value(schemas.BifrostContextKeyCustomProviderMetadata); metadata != nil {
		t.Fatalf("expected custom provider metadata to be cleared, got %+v", metadata)
	}
	if customProviderConfig := ctx.Value(schemas.BifrostContextKeyCustomProviderConfig); customProviderConfig != nil {
		t.Fatalf("expected custom provider config to be cleared, got %+v", customProviderConfig)
	}
}
