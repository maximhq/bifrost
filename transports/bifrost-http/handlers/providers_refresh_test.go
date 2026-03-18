package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

type refreshModelsManager struct {
	reloadCalled bool
	reloadErr    error
}

func (m *refreshModelsManager) ReloadProvider(ctx context.Context, provider schemas.ModelProvider) (*tables.TableProvider, error) {
	m.reloadCalled = true
	if m.reloadErr != nil {
		return nil, m.reloadErr
	}
	return &tables.TableProvider{}, nil
}

func (m *refreshModelsManager) RemoveProvider(ctx context.Context, provider schemas.ModelProvider) error {
	return nil
}

func (m *refreshModelsManager) GetModelsForProvider(provider schemas.ModelProvider) []string {
	return nil
}

func (m *refreshModelsManager) GetUnfilteredModelsForProvider(provider schemas.ModelProvider) []string {
	return nil
}

func TestRefreshModels_ReturnsNotFoundForUnknownProvider(t *testing.T) {
	h := &ProviderHandler{
		inMemoryStore: &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{}},
		modelsManager: &refreshModelsManager{},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.QueryArgs().Set("provider", string(schemas.Copilot))
	h.refreshModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}

	var manager *refreshModelsManager
	manager = h.modelsManager.(*refreshModelsManager)
	if manager.reloadCalled {
		t.Fatal("expected model discovery not to run for unknown provider")
	}
}

func TestRefreshModels_SkipsDiscoveryForKeylessProvider(t *testing.T) {
	manager := &refreshModelsManager{reloadErr: errors.New("reload should not be called")}
	h := &ProviderHandler{
		inMemoryStore: &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.Copilot: {
				CustomProviderConfig: &schemas.CustomProviderConfig{IsKeyLess: true},
			},
		}},
		modelsManager: manager,
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.QueryArgs().Set("provider", string(schemas.Copilot))
	h.refreshModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if manager.reloadCalled {
		t.Fatal("expected model discovery to be skipped for keyless provider refresh")
	}
}
