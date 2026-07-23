package gigachat

import (
	"context"
	"strings"
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

func TestRedactGigaChatSensitiveText(t *testing.T) {
	t.Parallel()

	input := `"access_token":"double-secret" "password": "spaced-secret" 'client_secret':'single-secret' credentials=plain-secret authorization=assignment-secret user=auth-secret-user Bearer header-secret -----BEGIN PRIVATE KEY-----
private-secret
-----END PRIVATE KEY-----`

	got := redactGigaChatSensitiveText(input)
	for _, secret := range []string{"double-secret", "spaced-secret", "single-secret", "plain-secret", "assignment-secret", "auth-secret-user", "header-secret", "private-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted text leaked %q in %s", secret, got)
		}
	}
	for _, want := range []string{
		`"access_token":"<redacted>"`,
		`"password": "<redacted>"`,
		`'client_secret':'<redacted>'`,
		`credentials=<redacted>`,
		`authorization=<redacted>`,
		`user=<redacted>`,
		`Bearer <redacted>`,
		`<redacted-private-key>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("redacted text missing %q in %s", want, got)
		}
	}

	benign := redactGigaChatSensitiveText("profile user=visible-user")
	if !strings.Contains(benign, "user=visible-user") {
		t.Fatalf("benign user assignment should not be redacted: %s", benign)
	}
}

func TestRedactGigaChatRawPayloadNarrowsUserField(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"user": "ordinary-user",
		"message": "keep this",
		"gigachat_key_config": {
			"user": {"value": "secret-user"},
			"password": {"value": "secret-password"},
			"credentials": {"value": "secret-credentials"},
			"access_token": {"value": "secret-token"}
		}
	}`)

	redacted := string(redactGigaChatRawPayload(payload))
	for _, secret := range []string{"secret-user", "secret-password", "secret-credentials", "secret-token"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("redacted payload leaked %q in %s", secret, redacted)
		}
	}
	if !strings.Contains(redacted, `"user":"ordinary-user"`) {
		t.Fatalf("benign top-level user field should be preserved: %s", redacted)
	}
	for _, want := range []string{`"user":"<redacted>"`, `"password":"<redacted>"`, `"credentials":"<redacted>"`, `"access_token":"<redacted>"`} {
		if !strings.Contains(redacted, want) {
			t.Fatalf("redacted payload missing %q in %s", want, redacted)
		}
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
