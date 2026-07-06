package openai

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertRequestsToJSONLRequiresCustomID(t *testing.T) {
	for _, customID := range []string{"", "   "} {
		_, err := ConvertRequestsToJSONL([]schemas.BatchRequestItem{{
			CustomID: customID,
			Method:   "POST",
			URL:      string(schemas.BatchEndpointChatCompletions),
			Body:     map[string]interface{}{"model": "gpt-4o-mini"},
		}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "batch request item 0: custom_id is required")
	}
}

func TestConvertRequestsToJSONLPreservesCustomID(t *testing.T) {
	data, err := ConvertRequestsToJSONL([]schemas.BatchRequestItem{{
		CustomID: "request-1",
		Method:   "POST",
		URL:      string(schemas.BatchEndpointEmbeddings),
		Body:     map[string]interface{}{"model": "text-embedding-3-small", "input": "hello"},
	}})
	require.NoError(t, err)

	line := strings.TrimSpace(string(data))
	var decoded schemas.BatchRequestItem
	require.NoError(t, sonic.UnmarshalString(line, &decoded))
	assert.Equal(t, "request-1", decoded.CustomID)
}
