package schemas

import (
	"bytes"
	"fmt"
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

// buildToolPropertiesFixture returns a tools-payload "properties" fragment as a
// plain Go map — the shape SafeExtractOrderedMap receives when tool schemas
// arrive through map-typed fields (e.g. the Responses conversion path for
// Anthropic-protocol providers, issue #6591). Each property schema carries
// several keys so that undefined map iteration order is observable in
// serialized output.
func buildToolPropertiesFixture() map[string]interface{} {
	props := map[string]interface{}{}
	for _, name := range []string{"location", "unit", "days", "verbose", "limit"} {
		props[name] = map[string]interface{}{
			"type":        "string",
			"description": "property " + name,
			"title":       name,
			"minLength":   float64(1),
		}
	}
	// One object-typed property with nested properties to exercise recursion.
	props["filters"] = map[string]interface{}{
		"type":        "object",
		"description": "structured filters",
		"properties": map[string]interface{}{
			"start": map[string]interface{}{"type": "string", "format": "date", "description": "start date"},
			"end":   map[string]interface{}{"type": "string", "format": "date", "description": "end date"},
		},
	}
	return props
}

// assertNoPlainMaps walks the extracted value and fails if any nested object is
// still a plain map (whose iteration order is undefined) rather than an
// *OrderedMap carrying a stable key order. Plain maps leak Go's randomized
// iteration order into any consumer that re-marshals a plucked sub-schema with
// the hot-path encoder, which breaks provider prompt caching (issue #6591).
func assertNoPlainMaps(t *testing.T, path string, v interface{}) {
	t.Helper()
	switch val := v.(type) {
	case map[string]interface{}:
		t.Fatalf("%s: nested value is %T; plain maps serialize in random key order — want *OrderedMap", path, v)
	case *OrderedMap:
		for _, k := range val.Keys() {
			child, _ := val.Get(k)
			assertNoPlainMaps(t, path+"."+k, child)
		}
	case OrderedMap:
		for _, k := range val.Keys() {
			child, _ := val.Get(k)
			assertNoPlainMaps(t, path+"."+k, child)
		}
	case []interface{}:
		for i, item := range val {
			assertNoPlainMaps(t, fmt.Sprintf("%s[%d]", path, i), item)
		}
	}
}

// Regression test for #6591: tool schemas extracted from plain Go maps must
// come out as fully ordered trees so their serialization is byte-stable.
func TestSafeExtractOrderedMapToolSchemaDeterminism(t *testing.T) {
	const iterations = 50

	cases := []struct {
		name    string
		extract func() (*OrderedMap, bool)
	}{
		{"map", func() (*OrderedMap, bool) { return SafeExtractOrderedMap(buildToolPropertiesFixture()) }},
		{"pointer-to-map", func() (*OrderedMap, bool) {
			m := buildToolPropertiesFixture()
			return SafeExtractOrderedMap(&m)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var firstFull, firstPlucked []byte
			for i := 0; i < iterations; i++ {
				om, ok := tc.extract()
				require.True(t, ok, "SafeExtractOrderedMap returned ok=false")

				// Every nested object must be order-carrying.
				assertNoPlainMaps(t, "properties", om)

				// The whole extracted fragment must serialize byte-identically.
				full, err := Marshal(om)
				require.NoError(t, err)

				// Downstream converters pluck sub-schemas out and re-marshal
				// them individually with the hot-path encoder (sonic does not
				// sort plain map keys).
				plucked, err := Marshal(mustGet(t, om, "location"))
				require.NoError(t, err)

				if i == 0 {
					firstFull, firstPlucked = full, plucked
					continue
				}
				if !bytes.Equal(firstFull, full) {
					t.Fatalf("iteration %d: full serialization diverged:\n first: %s\n later: %s", i, firstFull, full)
				}
				if !bytes.Equal(firstPlucked, plucked) {
					t.Fatalf("iteration %d: plucked sub-schema serialization diverged:\n first: %s\n later: %s", i, firstPlucked, plucked)
				}
			}
		})
	}
}

func mustGet(t *testing.T, om *OrderedMap, key string) interface{} {
	t.Helper()
	v, ok := om.Get(key)
	require.True(t, ok, "key %q missing from extracted map", key)
	return v
}

// TestToolFunctionParametersFromMapSourceByteStable pins the tools payload
// round-trip from #6591: schemas extracted from plain maps, embedded in
// ToolFunctionParameters (the wire type for both Chat function tools and the
// Anthropic input_schema), must marshal byte-identically across repeated
// conversions of the same source.
func TestToolFunctionParametersFromMapSourceByteStable(t *testing.T) {
	const iterations = 50
	var first []byte
	for i := 0; i < iterations; i++ {
		props, ok := SafeExtractOrderedMap(buildToolPropertiesFixture())
		require.True(t, ok)
		items, ok := SafeExtractOrderedMap(map[string]interface{}{
			"type": "integer", "minimum": float64(0), "description": "forecast day"})
		require.True(t, ok)

		params := &ToolFunctionParameters{
			Type:       "object",
			Properties: props,
			Items:      items,
			Required:   []string{"location"},
		}
		b, err := Marshal(params)
		require.NoError(t, err)

		if i == 0 {
			first = b
			continue
		}
		if !bytes.Equal(first, b) {
			t.Fatalf("iteration %d: tools payload serialization diverged:\n first: %s\n later: %s", i, first, b)
		}
	}
}
