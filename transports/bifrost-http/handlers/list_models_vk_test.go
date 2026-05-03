package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

type mockListModelsVKConfigStore struct {
	configstore.ConfigStore
	vk  *configstoreTables.TableVirtualKey
	err error
}

func (m *mockListModelsVKConfigStore) GetVirtualKeyByValue(_ context.Context, _ string) (*configstoreTables.TableVirtualKey, error) {
	return m.vk, m.err
}

func TestApplyListModelsVirtualKeyProviderFilterSkipsInactiveVK(t *testing.T) {
	h := &CompletionHandler{
		config: &lib.Config{
			ConfigStore: &mockListModelsVKConfigStore{vk: &configstoreTables.TableVirtualKey{
				Value:    "sk-bf-inactive",
				IsActive: false,
				ProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{
					{Provider: "openai"},
				},
			}},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer sk-bf-inactive")
	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	if ok := h.applyListModelsVirtualKeyProviderFilter(ctx, bifrostCtx); !ok {
		t.Fatalf("expected inactive VK to be ignored without failing request")
	}
	if got := bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders); got != nil {
		t.Fatalf("expected inactive VK not to set available providers, got %#v", got)
	}
}
