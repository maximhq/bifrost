package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

func TestListModelsByKey_FallsBackToConfiguredModelsOnUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := ListModelsByKey(
		ctx,
		&fasthttp.Client{ReadTimeout: time.Second, WriteTimeout: time.Second},
		server.URL,
		schemas.Key{
			ID:     "local-key",
			Models: schemas.WhiteList{"hermes-qwen"},
			Aliases: schemas.KeyAliases{
				"hermes-qwen": "Qwen/Qwen3-32B",
			},
		},
		false,
		nil,
		schemas.ModelProvider("bao-qwen"),
		false,
		false,
	)

	if err != nil {
		t.Fatalf("ListModelsByKey returned error: %v", err)
	}
	if resp == nil || len(resp.Data) != 1 {
		t.Fatalf("expected one fallback model, got %#v", resp)
	}
	if resp.Data[0].ID != "bao-qwen/hermes-qwen" {
		t.Fatalf("expected configured model id, got %q", resp.Data[0].ID)
	}
	if resp.Data[0].Alias == nil || *resp.Data[0].Alias != "Qwen/Qwen3-32B" {
		t.Fatalf("expected alias to preserve upstream model id, got %#v", resp.Data[0].Alias)
	}
}

func TestListModelsByKey_FallsBackToConfiguredAliasesWithWildcardModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := ListModelsByKey(
		ctx,
		&fasthttp.Client{ReadTimeout: time.Second, WriteTimeout: time.Second},
		server.URL,
		schemas.Key{
			ID:     "local-key",
			Models: schemas.WhiteList{"*"},
			Aliases: schemas.KeyAliases{
				"desktop-picker-name": "actual-local-model",
			},
		},
		false,
		nil,
		schemas.ModelProvider("local-openai"),
		false,
		false,
	)

	if err != nil {
		t.Fatalf("ListModelsByKey returned error: %v", err)
	}
	if resp == nil || len(resp.Data) != 1 {
		t.Fatalf("expected one alias fallback model, got %#v", resp)
	}
	if resp.Data[0].ID != "local-openai/desktop-picker-name" {
		t.Fatalf("expected alias model id, got %q", resp.Data[0].ID)
	}
	if resp.Data[0].Alias == nil || *resp.Data[0].Alias != "actual-local-model" {
		t.Fatalf("expected alias target, got %#v", resp.Data[0].Alias)
	}
}

func TestListModelsByKey_UnfilteredDoesNotUseConfiguredFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, err := ListModelsByKey(
		ctx,
		&fasthttp.Client{ReadTimeout: time.Second, WriteTimeout: time.Second},
		server.URL,
		schemas.Key{
			ID:     "local-key",
			Models: schemas.WhiteList{"hermes-qwen"},
		},
		true,
		nil,
		schemas.ModelProvider("bao-qwen"),
		false,
		false,
	)

	if err == nil {
		t.Fatalf("expected upstream error for unfiltered request, got response %#v", resp)
	}
}
