package vertex_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/internal/llmtests"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestVertexGemini3StructuredOutput reproduces issue #6589: on the Vertex provider,
// structured-output request shapes (response_format json_object / json_schema) against
// Gemini 3.x models return 400 INVALID_ARGUMENT, while plain-text requests to the same
// models succeed. Gemini 3.x publisher models are only served from the global endpoint,
// so the test account carries a dedicated Vertex key with Region "global" for them.
func TestVertexGemini3StructuredOutput(t *testing.T) {
	if strings.TrimSpace(os.Getenv("VERTEX_PROJECT_ID")) == "" || strings.TrimSpace(os.Getenv("VERTEX_CREDENTIALS")) == "" {
		t.Skip("Skipping Vertex tests because VERTEX_PROJECT_ID or VERTEX_CREDENTIALS is not set")
	}
	client, ctx, cancel, err := llmtests.SetupTest()
	require.NoError(t, err, "test setup must succeed")
	defer cancel()
	defer client.Shutdown()

	sentimentSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"sentiment":     map[string]interface{}{"type": "string"},
			"action_worthy": map[string]interface{}{"type": "boolean"},
		},
		"required":             []string{"sentiment", "action_worthy"},
		"additionalProperties": false,
	}

	cases := []struct {
		name           string
		model          string
		responseFormat interface{}
	}{
		{
			name:           "JSONObjectGemini35Flash",
			model:          "gemini-3.5-flash",
			responseFormat: map[string]interface{}{"type": "json_object"},
		},
		{
			name:  "JSONSchemaGemini35Flash",
			model: "gemini-3.5-flash",
			responseFormat: map[string]interface{}{
				"type": "json_schema",
				"json_schema": map[string]interface{}{
					"name":   "sentiment",
					"strict": true,
					"schema": sentimentSchema,
				},
			},
		},
		{
			name:           "JSONObjectGemini31FlashLite",
			model:          "gemini-3.1-flash-lite",
			responseFormat: map[string]interface{}{"type": "json_object"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rf := tc.responseFormat
			reqCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
			response, bifrostErr := client.ChatCompletionRequest(reqCtx, &schemas.BifrostChatRequest{
				Provider: schemas.Vertex,
				Model:    tc.model,
				Input: []schemas.ChatMessage{
					llmtests.CreateBasicChatMessage("Classify sentiment of: the app is great. Respond in JSON with keys sentiment (string) and action_worthy (boolean)."),
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: schemas.Ptr(2000),
					ResponseFormat:      &rf,
				},
			})
			if bifrostErr != nil {
				raw, _ := json.Marshal(bifrostErr)
				t.Fatalf("structured output request must not error, got: %s", string(raw))
			}
			require.NotNil(t, response)
			content := llmtests.GetChatContent(response)
			require.NotEmpty(t, content, "structured output response content must not be empty")
			var parsed map[string]interface{}
			require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(content)), &parsed),
				"structured output content must be valid JSON, got: %s", content)
		})
	}
}
