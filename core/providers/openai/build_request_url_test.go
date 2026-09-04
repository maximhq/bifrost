package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestBuildRequestURL_DisableDefaultVersionPath verifies that the /v1 prefix is
// only stripped from the true default path when DisableDefaultVersionPath is set,
// and never from an explicit context path that happens to equal the default.
func TestBuildRequestURL_DisableDefaultVersionPath(t *testing.T) {
	defaultPath := "/v1/chat/completions"

	cases := []struct {
		name      string
		baseURL   string
		disable   bool
		ctxPath   *string
		wantPath  string
	}{
		{
			name:     "default path stripped when flag set and base URL has path",
			baseURL:  "https://api.telnyx.com/v2/ai/openai",
			disable:  true,
			wantPath: "/chat/completions",
		},
		{
			name:     "default path preserved when flag not set",
			baseURL:  "https://api.telnyx.com/v2/ai/openai",
			disable:  false,
			wantPath: "/v1/chat/completions",
		},
		{
			name:     "default path preserved when base URL has no path",
			baseURL:  "https://api.openai.com",
			disable:  true,
			wantPath: "/v1/chat/completions",
		},
		{
			name:     "explicit context path equal to default is not stripped",
			baseURL:  "https://api.telnyx.com/v2/ai/openai",
			disable:  true,
			ctxPath:  schemas.Ptr("/v1/chat/completions"),
			wantPath: "/v1/chat/completions",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &schemas.BifrostContext{}
			if tc.ctxPath != nil {
				ctx = ctx.WithValue(schemas.BifrostContextKeyURLPath, *tc.ctxPath)
			}

			provider := &OpenAIProvider{
				logger:      &testLogger{},
				networkConfig: schemas.NetworkConfig{BaseURL: tc.baseURL},
				customProviderConfig: &schemas.CustomProviderConfig{
					DisableDefaultVersionPath: tc.disable,
				},
			}

			got := provider.buildRequestURL(ctx, defaultPath, schemas.ChatCompletionRequest)
			want := tc.baseURL + tc.wantPath
			if got != want {
				t.Errorf("buildRequestURL() = %q, want %q", got, want)
			}
		})
	}
}
