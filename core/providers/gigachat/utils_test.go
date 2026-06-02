package gigachat

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestBuildGigaChatURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		baseURL    string
		apiVersion string
		path       string
		want       string
	}{
		{
			name:       "default base appends v1 path",
			apiVersion: gigaChatAPIVersionV1,
			path:       "/chat/completions",
			want:       "https://gigachat.devices.sberbank.ru/api/v1/chat/completions",
		},
		{
			name:       "primary api root appends v1 path",
			baseURL:    "https://gigachat.devices.sberbank.ru/api/",
			apiVersion: gigaChatAPIVersionV1,
			path:       "/chat/completions",
			want:       "https://gigachat.devices.sberbank.ru/api/v1/chat/completions",
		},
		{
			name:       "primary v1 root keeps v1 path",
			baseURL:    "https://gigachat.devices.sberbank.ru/api/v1",
			apiVersion: gigaChatAPIVersionV1,
			path:       "/chat/completions",
			want:       "https://gigachat.devices.sberbank.ru/api/v1/chat/completions",
		},
		{
			name:       "primary v1 root switches to v2",
			baseURL:    "https://gigachat.devices.sberbank.ru/api/v1",
			apiVersion: gigaChatAPIVersionV2,
			path:       "/chat/completions",
			want:       "https://gigachat.devices.sberbank.ru/api/v2/chat/completions",
		},
		{
			name:       "primary v2 root switches to v1",
			baseURL:    "https://gigachat.devices.sberbank.ru/api/v2/",
			apiVersion: gigaChatAPIVersionV1,
			path:       "/chat/completions",
			want:       "https://gigachat.devices.sberbank.ru/api/v1/chat/completions",
		},
		{
			name:       "business api root appends v1 path",
			baseURL:    "https://api.giga.chat",
			apiVersion: gigaChatAPIVersionV1,
			path:       "chat/completions",
			want:       "https://api.giga.chat/v1/chat/completions",
		},
		{
			name:       "business v2 root keeps v2 path",
			baseURL:    "https://api.giga.chat/v2/",
			apiVersion: gigaChatAPIVersionV2,
			path:       "/chat/completions",
			want:       "https://api.giga.chat/v2/chat/completions",
		},
		{
			name:       "versioned path is not duplicated",
			baseURL:    "https://api.giga.chat",
			apiVersion: gigaChatAPIVersionV1,
			path:       "/v1/chat/completions",
			want:       "https://api.giga.chat/v1/chat/completions",
		},
		{
			name:       "path version is replaced by selected version",
			baseURL:    "https://api.giga.chat",
			apiVersion: gigaChatAPIVersionV2,
			path:       "/v1/chat/completions",
			want:       "https://api.giga.chat/v2/chat/completions",
		},
		{
			name:       "query string is preserved",
			baseURL:    "https://gigachat.devices.sberbank.ru/api",
			apiVersion: gigaChatAPIVersionV1,
			path:       "/models?limit=100",
			want:       "https://gigachat.devices.sberbank.ru/api/v1/models?limit=100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildGigaChatURL(tt.baseURL, tt.apiVersion, tt.path)
			if got != tt.want {
				t.Fatalf("buildGigaChatURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveGigaChatURLs(t *testing.T) {
	t.Parallel()

	t.Run("auth URL defaults", func(t *testing.T) {
		t.Parallel()

		if got := resolveAuthURL(schemas.Key{}); got != gigaChatDefaultAuthURL {
			t.Fatalf("resolveAuthURL() = %q, want %q", got, gigaChatDefaultAuthURL)
		}
	})

	t.Run("auth URL uses key override", func(t *testing.T) {
		t.Parallel()

		key := schemas.Key{
			GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
				AuthURL: "https://auth.example.com/oauth/",
			},
		}
		if got := resolveAuthURL(key); got != "https://auth.example.com/oauth" {
			t.Fatalf("resolveAuthURL() = %q", got)
		}
	})

	t.Run("base URL defaults", func(t *testing.T) {
		t.Parallel()

		if got := resolveBaseURL(schemas.Key{}, schemas.NetworkConfig{}); got != gigaChatDefaultBaseURL {
			t.Fatalf("resolveBaseURL() = %q, want %q", got, gigaChatDefaultBaseURL)
		}
	})

	t.Run("base URL uses provider network config", func(t *testing.T) {
		t.Parallel()

		networkConfig := schemas.NetworkConfig{
			BaseURL: "https://api.giga.chat/v1/",
		}
		if got := resolveBaseURL(schemas.Key{}, networkConfig); got != "https://api.giga.chat/v1" {
			t.Fatalf("resolveBaseURL() = %q", got)
		}
	})

	t.Run("base URL uses key override before provider network config", func(t *testing.T) {
		t.Parallel()

		key := schemas.Key{
			GigaChatKeyConfig: &schemas.GigaChatKeyConfig{
				BaseURL: "https://api.giga.chat/v2/",
			},
		}
		networkConfig := schemas.NetworkConfig{
			BaseURL: "https://gigachat.devices.sberbank.ru/api/v1",
		}
		if got := resolveBaseURL(key, networkConfig); got != "https://api.giga.chat/v2" {
			t.Fatalf("resolveBaseURL() = %q", got)
		}
	})
}

func TestBuildGigaChatRequestURL(t *testing.T) {
	t.Parallel()

	t.Run("context path override is version-normalized", func(t *testing.T) {
		t.Parallel()

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyURLPath, "/v1/custom/chat")

		got := buildGigaChatRequestURL(ctx, "https://api.giga.chat/v1", gigaChatAPIVersionV2, "/chat/completions", nil, schemas.ChatCompletionRequest)
		want := "https://api.giga.chat/v2/custom/chat"
		if got != want {
			t.Fatalf("buildGigaChatRequestURL() = %q, want %q", got, want)
		}
	})

	t.Run("absolute context URL override is preserved", func(t *testing.T) {
		t.Parallel()

		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyURLPath, "https://proxy.example.com/gigachat")

		got := buildGigaChatRequestURL(ctx, "https://api.giga.chat", gigaChatAPIVersionV1, "/chat/completions", nil, schemas.ChatCompletionRequest)
		want := "https://proxy.example.com/gigachat"
		if got != want {
			t.Fatalf("buildGigaChatRequestURL() = %q, want %q", got, want)
		}
	})
}
