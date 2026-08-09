package bedrock

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestConvertBifrostReasoningToBedrockReasoningEncryptedContent verifies that
// encrypted reasoning replay data survives cross-provider translation into the
// Bedrock reasoning signature field. Responses reasoning items use an empty,
// non-nil Summary alongside EncryptedContent, so the converter must not treat
// the empty slice as visible reasoning and silently drop the replay payload.
func TestConvertBifrostReasoningToBedrockReasoningEncryptedContent(t *testing.T) {
	encryptedContent := "EqQBCgIYAhIM...fixture"
	message := &schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
		ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary:          []schemas.ResponsesReasoningSummary{},
			EncryptedContent: &encryptedContent,
		},
	}

	blocks := convertBifrostReasoningToBedrockReasoning(message)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].ReasoningContent)
	require.NotNil(t, blocks[0].ReasoningContent.ReasoningText)
	require.Nil(t, blocks[0].ReasoningContent.ReasoningText.Text)
	require.Equal(t, encryptedContent, *blocks[0].ReasoningContent.ReasoningText.Signature)
}
