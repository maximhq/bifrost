package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestTargetsOpenAIHostedAPI(t *testing.T) {
	tests := []struct {
		name    string
		baseURL *string // nil means the key is absent from the context
		want    bool
	}{
		{name: "key absent is hosted", baseURL: nil, want: true},
		{name: "empty base url is hosted", baseURL: schemas.Ptr(""), want: true},
		{name: "api.openai.com is hosted", baseURL: schemas.Ptr("https://api.openai.com"), want: true},
		{name: "openai subdomain is hosted", baseURL: schemas.Ptr("https://eu.api.openai.com/v1"), want: true},
		{name: "mixed case host is hosted", baseURL: schemas.Ptr("https://API.OpenAI.com"), want: true},
		{name: "ollama loopback is not hosted", baseURL: schemas.Ptr("http://127.0.0.1:11434/v1"), want: false},
		{name: "vllm internal host is not hosted", baseURL: schemas.Ptr("http://vllm.internal:8000"), want: false},
		{name: "lookalike domain is not hosted", baseURL: schemas.Ptr("https://api.openai.com.evil.example"), want: false},
		{name: "malformed url falls back to hosted", baseURL: schemas.Ptr("://not a url"), want: true},
		{name: "host-less url falls back to hosted", baseURL: schemas.Ptr("/v1"), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
			if tt.baseURL != nil {
				ctx.SetValue(schemas.BifrostContextKeyProviderBaseURL, *tt.baseURL)
			}
			require.Equal(t, tt.want, targetsOpenAIHostedAPI(ctx))
		})
	}

	require.True(t, targetsOpenAIHostedAPI(nil), "nil context must keep the historical filtering")
}
