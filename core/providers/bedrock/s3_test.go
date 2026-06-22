package bedrock

import (
	"encoding/json"
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConvertBedrockRequestsToJSONL_NoModelIDInModelInput guards against
// regressing the Bedrock batch bug where modelId was injected into each
// record's modelInput. Bedrock requires each JSONL line to be strictly
// {recordId, modelInput} with modelId only at the job level, otherwise it
// rejects records with "modelId: Extra inputs are not permitted".
func TestConvertBedrockRequestsToJSONL_NoModelIDInModelInput(t *testing.T) {
	modelID := "us.anthropic.claude-opus-4-6-v1"
	requests := []schemas.BatchRequestItem{
		{
			CustomID: "item-00043",
			Body: map[string]interface{}{
				"anthropic_version": "bedrock-2023-05-31",
				"max_tokens":        16,
				"messages": []map[string]interface{}{
					{"role": "user", "content": "Reply with the number 43."},
				},
				"model": modelID, // should be stripped, not leaked into modelInput
			},
		},
		{
			CustomID: "item-00044",
			Params: map[string]interface{}{
				"max_tokens": 8,
				"model":      modelID,
			},
		},
	}

	data, err := ConvertBedrockRequestsToJSONL(requests, &modelID)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)

	for i, line := range lines {
		var record struct {
			RecordID   string                 `json:"recordId"`
			ModelInput map[string]interface{} `json:"modelInput"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &record), "line %d should be valid JSON", i)

		assert.NotEmpty(t, record.RecordID, "recordId should be set")

		// The core regression assertions: neither modelId nor model may appear
		// inside modelInput.
		_, hasModelID := record.ModelInput["modelId"]
		assert.False(t, hasModelID, "modelInput must not contain modelId (line %d)", i)
		_, hasModel := record.ModelInput["model"]
		assert.False(t, hasModel, "modelInput must not contain model (line %d)", i)
	}

	// First record's body should be carried through verbatim (minus model).
	var first struct {
		ModelInput map[string]interface{} `json:"modelInput"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	assert.Equal(t, "bedrock-2023-05-31", first.ModelInput["anthropic_version"])
	assert.Contains(t, first.ModelInput, "messages")
}

// TestConvertBedrockRequestsToJSONL_RequiresModelID confirms the job-level
// model is still mandatory.
func TestConvertBedrockRequestsToJSONL_RequiresModelID(t *testing.T) {
	requests := []schemas.BatchRequestItem{{CustomID: "item-1", Body: map[string]interface{}{"max_tokens": 16}}}

	_, err := ConvertBedrockRequestsToJSONL(requests, nil)
	assert.Error(t, err)

	empty := ""
	_, err = ConvertBedrockRequestsToJSONL(requests, &empty)
	assert.Error(t, err)
}
