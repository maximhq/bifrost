package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeImageURLDefaultRejectsNonHTTPSchemes(t *testing.T) {
	// The no-args overload must keep the historical http/https-only policy. Providers
	// that legitimately accept other schemes (gs://, file://, ...) must opt in via
	// SanitizeImageURLWithAllowedSchemes — otherwise a future caller silently inherits
	// a wider attack/regression surface.
	_, err := SanitizeImageURL("gs://my-bucket/path/image.png")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `URL scheme "gs" is not allowed`)

	_, err = SanitizeImageURL("file:///etc/passwd")
	require.Error(t, err)
}

func TestSanitizeImageURLWithAllowedSchemesAcceptsOptIn(t *testing.T) {
	sanitizedURL, err := SanitizeImageURLWithAllowedSchemes(" gs://my-bucket/path/image.png ", "http", "https", "gs")
	require.NoError(t, err)
	assert.Equal(t, "gs://my-bucket/path/image.png", sanitizedURL)
}

func TestSanitizeImageURLWithAllowedSchemesRejectsUnlisted(t *testing.T) {
	_, err := SanitizeImageURLWithAllowedSchemes("gs://my-bucket/path/image.png", "http", "https")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `URL scheme "gs" is not allowed`)
}

func TestSanitizeImageURLWithEmptyAllowlistRejects(t *testing.T) {
	// Empty allowlist means "no non-data URL is acceptable" — an explicit denial,
	// not "fall back to defaults".
	_, err := SanitizeImageURLWithAllowedSchemes("https://example.com/foo.png")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no schemes permitted`)
}

func TestSanitizeImageURLDataURLUnaffectedByAllowlist(t *testing.T) {
	dataURL := "data:image/png;base64,iVBORw0KGgo="
	got, err := SanitizeImageURL(dataURL)
	require.NoError(t, err)
	assert.Equal(t, dataURL, got)

	got, err = SanitizeImageURLWithAllowedSchemes(dataURL)
	require.NoError(t, err)
	assert.Equal(t, dataURL, got)
}

func TestParseModelStringProviderPrefix(t *testing.T) {
	// Custom provider registered in display case, as it would be at config load time.
	RegisterKnownProvider("NVIDIA")
	t.Cleanup(func() { UnregisterKnownProvider("NVIDIA") })

	tests := []struct {
		name          string
		model         string
		wantProvider  ModelProvider
		wantModelName string
	}{
		{"builtin lowercase", "openai/gpt-4", OpenAI, "gpt-4"},
		{"builtin mixed case", "OpenAI/gpt-4", OpenAI, "gpt-4"},
		{"custom exact case", "NVIDIA/meta/llama-3.1-8b-instruct", "NVIDIA", "meta/llama-3.1-8b-instruct"},
		{"custom lowercase resolves to canonical", "nvidia/meta/llama-3.1-8b-instruct", "NVIDIA", "meta/llama-3.1-8b-instruct"},
		{"unknown namespace preserved", "meta-llama/Llama-3.1-8B", "", "meta-llama/Llama-3.1-8B"},
		{"no prefix", "gpt-4", "", "gpt-4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, modelName := ParseModelString(tt.model, "")
			assert.Equal(t, tt.wantProvider, provider)
			assert.Equal(t, tt.wantModelName, modelName)
		})
	}
}

func TestResolveKnownProviderUnregister(t *testing.T) {
	RegisterKnownProvider("NVIDIA")
	provider, ok := ResolveKnownProvider("nvidia")
	require.True(t, ok)
	assert.Equal(t, ModelProvider("NVIDIA"), provider)

	UnregisterKnownProvider("NVIDIA")
	_, ok = ResolveKnownProvider("nvidia")
	assert.False(t, ok)

	// Standard providers are unaffected by both operations.
	provider, ok = ResolveKnownProvider("OpenAI")
	require.True(t, ok)
	assert.Equal(t, OpenAI, provider)
}
