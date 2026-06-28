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

func TestDeepCopyResponsesMessagePreservesToolSearchFields(t *testing.T) {
	t.Parallel()

	msgType := ResponsesMessageTypeToolSearchOutput
	callID := "search-1"
	const wantNamespace = "mcp__codexself"
	const wantExecution = "client"
	namespace := wantNamespace
	execution := wantExecution
	functionName := "codex_reply"

	original := ResponsesMessage{
		Type: &msgType,
		ResponsesToolMessage: &ResponsesToolMessage{
			CallID:    &callID,
			Namespace: &namespace,
			Execution: &execution,
			Tools: []ResponsesTool{
				{
					Type: ResponsesToolType("namespace"),
					Name: Ptr(namespace),
					ResponsesToolNamespace: &ResponsesToolNamespace{
						Tools: []ResponsesTool{
							{
								Type: ResponsesToolType("function"),
								Name: Ptr(functionName),
							},
						},
					},
				},
			},
		},
	}

	copied := DeepCopyResponsesMessage(original)
	require.NotNil(t, copied.ResponsesToolMessage)
	require.NotNil(t, copied.ResponsesToolMessage.Namespace)
	require.NotNil(t, copied.ResponsesToolMessage.Execution)
	require.Len(t, copied.ResponsesToolMessage.Tools, 1)

	assert.Equal(t, wantNamespace, *copied.ResponsesToolMessage.Namespace)
	assert.Equal(t, wantExecution, *copied.ResponsesToolMessage.Execution)
	assert.Equal(t, ResponsesToolType("namespace"), copied.ResponsesToolMessage.Tools[0].Type)

	*original.ResponsesToolMessage.Namespace = "mutated-namespace"
	*original.ResponsesToolMessage.Execution = "server"
	original.ResponsesToolMessage.Tools[0].Type = ResponsesToolType("mutated")

	assert.Equal(t, wantNamespace, *copied.ResponsesToolMessage.Namespace)
	assert.Equal(t, wantExecution, *copied.ResponsesToolMessage.Execution)
	assert.Equal(t, ResponsesToolType("namespace"), copied.ResponsesToolMessage.Tools[0].Type)
}
