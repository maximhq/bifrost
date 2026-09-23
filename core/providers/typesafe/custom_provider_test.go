package typesafe_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	"github.com/maximhq/bifrost/core/schemas"
)

func newTestProvider(t *testing.T, custom *schemas.CustomProviderConfig) *typesafe.TypesafeProvider {
	t.Helper()
	provider, err := typesafe.NewTypesafeProvider(&schemas.ProviderConfig{
		CustomProviderConfig: custom,
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("NewTypesafeProvider: %v", err)
	}
	return provider
}

// Custom providers are validated against SupportedBaseProviders; without
// Typesafe in that list, base_provider_type: typesafe is rejected outright.
func TestTypesafeIsSupportedBaseProvider(t *testing.T) {
	t.Parallel()
	if !slices.Contains(schemas.SupportedBaseProviders, schemas.Typesafe) {
		t.Fatalf("schemas.Typesafe missing from SupportedBaseProviders")
	}
}

// The account store keys providers by their configured name, so a custom
// provider wrapping typesafe must report that name, not "typesafe".
func TestGetProviderKeyResolvesCustomName(t *testing.T) {
	t.Parallel()

	standard := newTestProvider(t, nil)
	if got := standard.GetProviderKey(); got != schemas.Typesafe {
		t.Fatalf("standard provider key = %q, want %q", got, schemas.Typesafe)
	}

	custom := newTestProvider(t, &schemas.CustomProviderConfig{
		CustomProviderKey: "typesafe_gateway",
		BaseProviderType:  schemas.Typesafe,
	})
	if got := custom.GetProviderKey(); got != "typesafe_gateway" {
		t.Fatalf("custom provider key = %q, want %q", got, "typesafe_gateway")
	}
}

// The static catalog is prefixed with the provider key, so a custom provider
// must list its models under its own name.
func TestListModelsUsesCustomProviderName(t *testing.T) {
	t.Parallel()

	provider := newTestProvider(t, &schemas.CustomProviderConfig{
		CustomProviderKey: "typesafe_gateway",
		BaseProviderType:  schemas.Typesafe,
	})
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	keys := []schemas.Key{{ID: "k1", Models: schemas.WhiteList{"*"}}}

	resp, bifrostErr := provider.ListModels(ctx, keys, &schemas.BifrostListModelsRequest{Provider: "typesafe_gateway"})
	if bifrostErr != nil {
		t.Fatalf("ListModels: %+v", bifrostErr)
	}
	if len(resp.Data) == 0 {
		t.Fatalf("ListModels returned no models")
	}
	for _, model := range resp.Data {
		if !strings.HasPrefix(model.ID, "typesafe_gateway/") {
			t.Errorf("model ID %q not prefixed with the custom provider name", model.ID)
		}
	}
}
