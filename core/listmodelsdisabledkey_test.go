package bifrost

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// catalogWithEverything answers for any provider, as a real catalog would for
// one whose entries outlived the keys that produced them.
type catalogWithEverything struct {
	models []string
}

func (c *catalogWithEverything) CachedModels(schemas.ModelProvider, []string, bool) ([]schemas.Model, bool) {
	return modelsFixture(c.models...), true
}

func (c *catalogWithEverything) RoutableModels(schemas.ModelProvider, []string, bool) []string {
	return c.models
}

// TestListAllModels_DisabledKeysProviderIsAbsent reproduces the reported bug end
// to end: every key on a provider is disabled, the catalog still holds its
// models, and GET /v1/models must not report them.
//
// Uses the real ListAllModels rather than the seam in isolation, because the
// first fix was placed in a branch this scenario never reaches.
func TestListAllModels_DisabledKeysProviderIsAbsent(t *testing.T) {
	account := NewMockAccount()
	disabled := false
	account.AddProvider(schemas.Anthropic, 1, 1)
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{{
		ID:      "anthropic-key-1",
		Value:   *schemas.NewSecretVar("sk-ant-test"),
		Models:  schemas.WhiteList{"*"},
		Enabled: &disabled,
	}})

	client, initErr := Init(context.Background(), schemas.BifrostConfig{
		Account: account,
		Logger:  NewNoOpLogger(),
		ListModelsCatalog: &catalogWithEverything{
			models: []string{"claude-sonnet-4-5", "claude-opus-4-6"},
		},
		ServeListModelsFromCatalog: true,
	})
	if initErr != nil {
		t.Fatalf("Init failed: %v", initErr)
	}
	defer client.Shutdown()

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(10*time.Second))
	defer ctx.Cancel()

	resp, bfErr := client.ListAllModels(ctx, &schemas.BifrostListModelsRequest{})

	// Either outcome is acceptable as long as no Anthropic model is advertised:
	// an error (nothing listable at all) or an empty/other-provider response.
	if bfErr == nil && resp != nil {
		for _, m := range resp.Data {
			t.Errorf("disabled-key provider must not appear in /v1/models, got model %q", m.ID)
		}
	}
}

// The same provider with its key enabled must still be reported, so the guard
// above is not simply suppressing everything.
func TestListAllModels_EnabledKeyProviderIsServedFromCatalog(t *testing.T) {
	account := NewMockAccount()
	account.AddProvider(schemas.Anthropic, 1, 1)
	account.SetKeysForProvider(schemas.Anthropic, []schemas.Key{{
		ID:     "anthropic-key-1",
		Value:  *schemas.NewSecretVar("sk-ant-test"),
		Models: schemas.WhiteList{"*"},
	}})

	client, initErr := Init(context.Background(), schemas.BifrostConfig{
		Account: account,
		Logger:  NewNoOpLogger(),
		ListModelsCatalog: &catalogWithEverything{
			models: []string{"claude-sonnet-4-5"},
		},
		ServeListModelsFromCatalog: true,
	})
	if initErr != nil {
		t.Fatalf("Init failed: %v", initErr)
	}
	defer client.Shutdown()

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(10*time.Second))
	defer ctx.Cancel()

	resp, bfErr := client.ListAllModels(ctx, &schemas.BifrostListModelsRequest{})
	if bfErr != nil {
		t.Fatalf("expected the enabled-key provider to list models, got error: %v", GetErrorMessage(bfErr))
	}
	if resp == nil || len(resp.Data) == 0 {
		t.Fatal("expected the enabled-key provider's models to be listed")
	}
}
