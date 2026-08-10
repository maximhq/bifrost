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
