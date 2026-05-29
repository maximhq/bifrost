package azure

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestBuildPassthroughURL(t *testing.T) {
	t.Parallel()

	provider := &AzureProvider{}
	endpoint := "https://my-resource.openai.azure.com"

	makeKey := func(endpoint string) schemas.Key {
		return schemas.Key{
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint: *schemas.NewEnvVar(endpoint),
			},
		}
	}

	tests := []struct {
		name     string
		path     string
		rawQuery string
		want     string
	}{
		// --- Path normalisation ---
		{
			name: "normalise /openai/responses to /openai/v1/responses",
			path: "/openai/responses",
			want: endpoint + "/openai/v1/responses?api-version=" + AzureAPIVersionPreview,
		},
		{
			name: "normalise /openai/videos to /openai/v1/videos",
			path: "/openai/videos",
			want: endpoint + "/openai/v1/videos",
		},

		// --- /openai/deployments/ — inject default api-version when absent ---
		{
			name: "deployments route: inject default api-version when missing",
			path: "/openai/deployments/gpt-4o/chat/completions",
			want: endpoint + "/openai/deployments/gpt-4o/chat/completions?api-version=" + DefaultAzureAPIVersion,
		},
		{
			name:     "deployments route: preserve caller-supplied api-version",
			path:     "/openai/deployments/gpt-4o/chat/completions",
			rawQuery: "api-version=2024-10-21",
			want:     endpoint + "/openai/deployments/gpt-4o/chat/completions?api-version=2024-10-21",
		},
		{
			name:     "deployments route: preserve caller api-version alongside other params",
			path:     "/openai/deployments/gpt-4o/chat/completions",
			rawQuery: "api-version=2024-06-01&foo=bar",
			want:     endpoint + "/openai/deployments/gpt-4o/chat/completions?api-version=2024-06-01&foo=bar",
		},

		// --- /openai/v1/responses — inject api-version=preview when absent ---
		{
			name: "responses route: inject preview api-version when missing",
			path: "/openai/v1/responses",
			want: endpoint + "/openai/v1/responses?api-version=" + AzureAPIVersionPreview,
		},
		{
			name:     "responses route: preserve caller-supplied api-version",
			path:     "/openai/v1/responses",
			rawQuery: "api-version=2025-01-01-preview",
			want:     endpoint + "/openai/v1/responses?api-version=2025-01-01-preview",
		},

		// --- Anthropic routes — strip api-version ---
		{
			name:     "anthropic route: strip api-version",
			path:     "/anthropic/v1/messages",
			rawQuery: "api-version=2024-10-21",
			want:     endpoint + "/anthropic/v1/messages",
		},
		{
			name:     "anthropic route: preserve other query params after strip",
			path:     "/anthropic/v1/messages",
			rawQuery: "api-version=preview&foo=bar",
			want:     endpoint + "/anthropic/v1/messages?foo=bar",
		},

		// --- /openai/v1/videos — strip api-version ---
		{
			name:     "videos route: strip api-version",
			path:     "/openai/v1/videos",
			rawQuery: "api-version=2024-10-21",
			want:     endpoint + "/openai/v1/videos",
		},

		// --- Other v1 routes — pass through unchanged ---
		{
			name: "v1 chat completions: no api-version injected",
			path: "/openai/v1/chat/completions",
			want: endpoint + "/openai/v1/chat/completions",
		},
		{
			name:     "v1 chat completions: caller params passed through unchanged",
			path:     "/openai/v1/chat/completions",
			rawQuery: "foo=bar",
			want:     endpoint + "/openai/v1/chat/completions?foo=bar",
		},
		{
			name: "v1 embeddings: no api-version injected",
			path: "/openai/v1/embeddings",
			want: endpoint + "/openai/v1/embeddings",
		},
		{
			name: "v1 audio transcriptions: no api-version injected",
			path: "/openai/v1/audio/transcriptions",
			want: endpoint + "/openai/v1/audio/transcriptions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := provider.buildPassthroughURL(makeKey(endpoint), tt.path, tt.rawQuery)
			if got != tt.want {
				t.Errorf("\ngot:  %s\nwant: %s", got, tt.want)
			}
		})
	}
}
