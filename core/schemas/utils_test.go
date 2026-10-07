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

func TestSupportsGrokReasoningEffort(t *testing.T) {
	// Deny-listed: models confirmed to reject reasoning_effort.
	denied := []string{
		"grok-3",
		"grok-4",
		"grok-4-0709",   // dated alias must normalize to grok-4
		"grok-4-latest", // channel alias must normalize to grok-4
		"xai/grok-4",    // routing prefix must be stripped
		"grok-4-fast-reasoning",
		"grok-4-1-fast-reasoning",
		"grok-code-fast-1",
	}
	for _, m := range denied {
		assert.False(t, SupportsGrokReasoningEffort(m), "expected %q to be denied", m)
	}

	// Allowed: current generation, plus anything unrecognized (fail open).
	allowed := []string{
		"grok-3-mini",
		"grok-4.3",
		"grok-4.5",
		"grok-4.6",
		"grok-4.20-0309-reasoning",
		"grok-4.20-multi-agent-0309",
		"grok-5", // unknown future model must not be silently stripped
	}
	for _, m := range allowed {
		assert.True(t, SupportsGrokReasoningEffort(m), "expected %q to be allowed", m)
	}
}
func TestParseDataURL(t *testing.T) {
	tests := []struct {
		name              string
		dataURL           string
		expectedMediaType string
		expectedBase64    bool
		expectedPayload   string
		expectedOK        bool
	}{
		{"Base64", "data:image/png;base64,iVBORw0KGgo=", "image/png", true, "iVBORw0KGgo=", true},
		// Browsers and OpenAI-compatible clients routinely emit a charset parameter;
		// dropping the whole URL on the floor shipped "data:..." as the payload.
		{"MediaTypeParameter", "data:text/plain;charset=utf-8;base64,QUJD", "text/plain", true, "QUJD", true},
		{"ParameterWithoutBase64", "data:text/plain;charset=utf-8,Hello%20World", "text/plain", false, "Hello%20World", true},
		{"Uppercase", "data:IMAGE/PNG;BASE64,iVBORw0KGgo=", "image/png", true, "iVBORw0KGgo=", true},
		{"OfficeDocument", "data:application/vnd.openxmlformats-officedocument.spreadsheetml.sheet;base64,UEsDBBQ", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", true, "UEsDBBQ", true},
		{"PayloadWithNewlines", "data:image/png;base64,iVBOR\nw0KGgo=", "image/png", true, "iVBOR\nw0KGgo=", true},
		{"MissingMediaType", "data:;base64,iVBORw0KGgo=", "", false, "", false},
		{"MissingPayload", "data:image/png;base64,", "", false, "", false},
		{"NotADataURL", "https://example.com/image.png", "", false, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mediaType, isBase64, payload, ok := ParseDataURL(tt.dataURL)
			assert.Equal(t, tt.expectedOK, ok)
			assert.Equal(t, tt.expectedMediaType, mediaType)
			assert.Equal(t, tt.expectedBase64, isBase64)
			assert.Equal(t, tt.expectedPayload, payload)
		})
	}
}

func TestExtractURLTypeInfoDropsMediaTypeParameters(t *testing.T) {
	info := ExtractURLTypeInfo("data:text/plain;charset=utf-8;base64,QUJD")
	require.NotNil(t, info.MediaType)
	assert.Equal(t, "text/plain", *info.MediaType)
	assert.Equal(t, ImageContentTypeBase64, info.Type)
	require.NotNil(t, info.DataURLWithoutPrefix)
	assert.Equal(t, "QUJD", *info.DataURLWithoutPrefix)
}

func TestSanitizeImageURLAcceptsDataURLWithParameters(t *testing.T) {
	dataURL := "data:image/png;charset=binary;base64,iVBORw0KGgo="
	got, err := SanitizeImageURL(dataURL)
	require.NoError(t, err)
	assert.Equal(t, dataURL, got)

	// A data URL with no media type stays invalid: providers reject "data:;base64,...".
	_, err = SanitizeImageURL("data:;base64,iVBORw0KGgo=")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid data URL format")
}

// TestIsGPT56ModelRequiresRevisionBoundary pins both halves of the family check.
// The substring form is deliberate - catalog ids carry region and vendor
// namespaces, so a prefix test would miss them - but the needle has to end on a
// revision boundary, or a later dot-revision such as gpt-5.60 is mistaken for a
// member of the family and is handed a prompt_cache_breakpoint it never declared.
func TestIsGPT56ModelRequiresRevisionBoundary(t *testing.T) {
	for _, model := range []string{
		"gpt-5.6",
		"GPT-5.6",
		"azure/eu/gpt-5.6",
		"openai.gpt-5.6-terra",
		"gpt-5.6-mini",
	} {
		assert.True(t, IsGPT56Model(model), "expected %q to resolve as gpt-5.6 family", model)
	}

	for _, model := range []string{
		"gpt-5.60",
		"gpt-5.61-preview",
		"azure/eu/gpt-5.65",
		"gpt-5.5",
		"gpt-5",
	} {
		assert.False(t, IsGPT56Model(model), "expected %q NOT to resolve as gpt-5.6 family", model)
	}
}

// TestModelSupportsPromptCacheBreakpointCoversGPT6 pins that the breakpoint fallback,
// and the prompt-caching fallback built on it, include the gpt-6 family.
func TestModelSupportsPromptCacheBreakpointCoversGPT6(t *testing.T) {
	for _, model := range []string{"gpt-5.6-sol", "gpt-6-astra", "azure/gpt-6-astra", "openai.gpt-6-astra"} {
		assert.True(t, ModelSupportsPromptCacheBreakpoint(model), model)
	}
	for _, model := range []string{"gpt-5.5", "gpt-5", "gpt-4o"} {
		assert.False(t, ModelSupportsPromptCacheBreakpoint(model), model)
	}
	assert.True(t, ModelSupportsPromptCaching(OpenAI, "gpt-6-astra"))
	assert.True(t, ModelSupportsPromptCaching(BedrockMantle, "openai.gpt-6-astra"))
	assert.False(t, ModelSupportsPromptCaching(OpenAI, "gpt-5.5"))
}

// TestIsOpenAIDecisionModel pins which models OpenAI serves on its dedicated
// decisions endpoint: the gpt-6-luna family only, not other gpt-6 models or
// chat models that would answer a decision through emulation.
func TestIsOpenAIDecisionModel(t *testing.T) {
	for _, model := range []string{"gpt-6-luna", "GPT-6-Luna", "gpt-6-luna-2026-09-29", "openai/gpt-6-luna"} {
		assert.True(t, IsOpenAIDecisionModel(model), model)
	}
	for _, model := range []string{"gpt-6-astra", "gpt-4o", "gpt-5.6-luna", "my-gpt-6-luna", "typesafe/jev-1.13", ""} {
		assert.False(t, IsOpenAIDecisionModel(model), model)
	}
}

// TestParseModelString pins how a model string is split into a provider and a model: only on the
// first slash, and only when what precedes it is a provider Bifrost knows, so a namespaced model
// keeps its namespace. A custom provider splits while it is registered, and a standard provider
// stays known even when something tries to unregister it.
func TestParseModelString(t *testing.T) {
	for _, tc := range []struct {
		name         string
		model        string
		fallback     ModelProvider
		wantProvider ModelProvider
		wantModel    string
	}{
		{"a provider prefix", "openai/gpt-4o", "", OpenAI, "gpt-4o"},
		{"a bare model", "gpt-4o", "", "", "gpt-4o"},
		{"a bare model takes the default provider", "gpt-4o", Anthropic, Anthropic, "gpt-4o"},
		{"a namespace is not a provider", "meta-llama/Llama-3.1-8B", "", "", "meta-llama/Llama-3.1-8B"},
		{"a namespace keeps the default provider", "meta-llama/Llama-3.1-8B", HuggingFace, HuggingFace, "meta-llama/Llama-3.1-8B"},
		{"only the first slash splits", "openrouter/anthropic/claude-3.5-sonnet", "", OpenRouter, "anthropic/claude-3.5-sonnet"},
		{"a deep model path", "fireworks/accounts/fireworks/models/llama-v3p1-8b-instruct", "", Fireworks, "accounts/fireworks/models/llama-v3p1-8b-instruct"},
		{"a prefixed model beats the default provider", "vertex/gemini-2.5-flash", OpenAI, Vertex, "gemini-2.5-flash"},
		{"a provider with nothing after it", "anthropic/", "", Anthropic, ""},
		{"provider names are case-sensitive", "OpenAI/gpt-4o", "", "", "OpenAI/gpt-4o"},
		{"a leading slash names no provider", "/gpt-4o", "", "", "/gpt-4o"},
		{"an unregistered custom provider is a namespace", "harness-parse-custom/gpt-4o", "", "", "harness-parse-custom/gpt-4o"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider, model := ParseModelString(tc.model, tc.fallback)
			assert.Equal(t, tc.wantProvider, provider)
			assert.Equal(t, tc.wantModel, model)
		})
	}

	t.Run("a custom provider splits only while it is registered", func(t *testing.T) {
		custom := ModelProvider("harness-parse-custom")
		RegisterKnownProvider(custom)
		t.Cleanup(func() { UnregisterKnownProvider(custom) })
		provider, model := ParseModelString("harness-parse-custom/gpt-4o", "")
		assert.Equal(t, custom, provider)
		assert.Equal(t, "gpt-4o", model)

		UnregisterKnownProvider(custom)
		provider, model = ParseModelString("harness-parse-custom/gpt-4o", "")
		assert.Equal(t, ModelProvider(""), provider)
		assert.Equal(t, "harness-parse-custom/gpt-4o", model)
	})

	t.Run("a standard provider cannot be unregistered", func(t *testing.T) {
		UnregisterKnownProvider(OpenAI)
		provider, model := ParseModelString("openai/gpt-4o", "")
		require.Equal(t, OpenAI, provider)
		assert.Equal(t, "gpt-4o", model)
	})
}
