package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSchemaTypeValue(t *testing.T, value interface{}) interface{} {
	t.Helper()

	switch v := value.(type) {
	case map[string]interface{}:
		return v["type"]
	case *OrderedMap:
		got, _ := v.Get("type")
		return got
	case OrderedMap:
		got, _ := v.Get("type")
		return got
	default:
		t.Fatalf("unexpected schema value type %T", value)
		return nil
	}
}

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
	const wantNamespace = "mcp__codexself"
	const wantExecution = "client"
	const wantFunction = "codex_reply"
	callID := "search-1"
	namespace := wantNamespace
	execution := wantExecution
	functionName := wantFunction
	paramDesc := "reply payload"
	params := &ToolFunctionParameters{
		Type:        "object",
		Description: &paramDesc,
		Properties: NewOrderedMapFromPairs(
			Pair{Key: "message", Value: map[string]interface{}{"type": "string"}},
		),
	}

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
								ResponsesToolFunction: &ResponsesToolFunction{
									Parameters: params,
								},
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
	require.Len(t, copied.ResponsesToolMessage.Tools[0].ResponsesToolNamespace.Tools, 1)
	assert.Equal(t, wantFunction, *copied.ResponsesToolMessage.Tools[0].ResponsesToolNamespace.Tools[0].Name)
	assert.Equal(t, "string", testSchemaTypeValue(t, copied.ResponsesToolMessage.Tools[0].ResponsesToolNamespace.Tools[0].ResponsesToolFunction.Parameters.Properties.ToMap()["message"]))

	// Mutate the original after copying; the copy must not observe any of it.
	*original.ResponsesToolMessage.Namespace = "mutated-namespace"
	*original.ResponsesToolMessage.Execution = "server"
	original.ResponsesToolMessage.Tools[0].Type = ResponsesToolType("mutated")
	*original.ResponsesToolMessage.Tools[0].ResponsesToolNamespace.Tools[0].Name = "mutated-function"
	original.ResponsesToolMessage.Tools[0].ResponsesToolNamespace.Tools[0].ResponsesToolFunction.Parameters.Properties.Set("message", map[string]interface{}{"type": "number"})

	assert.Equal(t, wantNamespace, *copied.ResponsesToolMessage.Namespace)
	assert.Equal(t, wantExecution, *copied.ResponsesToolMessage.Execution)
	assert.Equal(t, ResponsesToolType("namespace"), copied.ResponsesToolMessage.Tools[0].Type)
	assert.Equal(t, wantFunction, *copied.ResponsesToolMessage.Tools[0].ResponsesToolNamespace.Tools[0].Name)
	assert.Equal(t, "string", testSchemaTypeValue(t, copied.ResponsesToolMessage.Tools[0].ResponsesToolNamespace.Tools[0].ResponsesToolFunction.Parameters.Properties.ToMap()["message"]))
}
