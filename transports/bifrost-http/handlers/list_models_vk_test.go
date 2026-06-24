package handlers

import (
	"context"
	"errors"
	"strings"
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

func TestApplyListModelsVirtualKeyProviderFilterSetsActiveVKProviders(t *testing.T) {
	h := &CompletionHandler{
		config: &lib.Config{
			ConfigStore: &mockListModelsVKConfigStore{vk: &configstoreTables.TableVirtualKey{
				Value:    "sk-bf-active",
				IsActive: new(true),
				ProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{
					{Provider: "openai"},
					{Provider: " anthropic "},
					{Provider: ""},
				},
			}},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer sk-bf-active")
	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	if ok := h.applyListModelsVirtualKeyProviderFilter(ctx, bifrostCtx); !ok {
		t.Fatalf("expected active VK to apply provider filter")
	}
	got, ok := bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders).([]schemas.ModelProvider)
	if !ok {
		t.Fatalf("expected available providers to be set")
	}
	want := []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic}
	if len(got) != len(want) {
		t.Fatalf("expected providers %#v, got %#v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected providers %#v, got %#v", want, got)
		}
	}
}

func TestApplyListModelsVirtualKeyProviderFilterReturnsErrorOnLookupFailure(t *testing.T) {
	h := &CompletionHandler{
		config: &lib.Config{
			ConfigStore: &mockListModelsVKConfigStore{err: errors.New("database unavailable")},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer sk-bf-active")
	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	if ok := h.applyListModelsVirtualKeyProviderFilter(ctx, bifrostCtx); ok {
		t.Fatalf("expected lookup error to fail request")
	}
	if got := ctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusInternalServerError, got)
	}
	if body := string(ctx.Response.Body()); !strings.Contains(body, "Failed to resolve virtual key") {
		t.Fatalf("expected virtual key lookup error response, got %q", body)
	}
}

func TestApplyListModelsVirtualKeyProviderFilterReturnsUnavailableWithoutConfigStore(t *testing.T) {
	h := &CompletionHandler{config: &lib.Config{}}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer sk-bf-active")
	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	if ok := h.applyListModelsVirtualKeyProviderFilter(ctx, bifrostCtx); ok {
		t.Fatalf("expected missing config store to fail request")
	}
	if got := ctx.Response.StatusCode(); got != fasthttp.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", fasthttp.StatusServiceUnavailable, got)
	}
	if body := string(ctx.Response.Body()); !strings.Contains(body, "database store unavailable") {
		t.Fatalf("expected unavailable response, got %q", body)
	}
}

func TestApplyListModelsVirtualKeyProviderFilterSkipsWhenVKNotFound(t *testing.T) {
	h := &CompletionHandler{
		config: &lib.Config{
			ConfigStore: &mockListModelsVKConfigStore{},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer sk-bf-missing")
	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	if ok := h.applyListModelsVirtualKeyProviderFilter(ctx, bifrostCtx); !ok {
		t.Fatalf("expected missing VK to be ignored without failing request")
	}
	if got := bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders); got != nil {
		t.Fatalf("expected missing VK not to set available providers, got %#v", got)
	}
}

func TestApplyListModelsVirtualKeyProviderFilterSkipsInactiveVK(t *testing.T) {
	h := &CompletionHandler{
		config: &lib.Config{
			ConfigStore: &mockListModelsVKConfigStore{vk: &configstoreTables.TableVirtualKey{
				Value:    "sk-bf-inactive",
				IsActive: new(false),
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
