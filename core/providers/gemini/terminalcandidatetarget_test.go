package gemini_test

import (
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Bifrost groups output items into candidates by role, so a response whose roles alternate
// flushes more than one populated candidate. Gemini itself only ever emits one, and every
// Gemini-shaped client reads candidates[0] -- so terminal metadata merged onto the LAST
// candidate lands where nothing reads it.
func TestTerminalMetadataMergesIntoFirstCandidate(t *testing.T) {
	bifrostResp := &schemas.BifrostResponsesResponse{
		ID:    schemas.Ptr("role-flip"),
		Model: "gemini-3.6-flash",
		Output: []schemas.ResponsesMessage{
			{
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("first group")},
			},
			{
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("second group")},
			},
			// A trailing group that contributes no parts: this is what sends the terminal
			// candidate down the merge branch instead of being appended as its own.
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			},
		},
		StopReason: schemas.Ptr("max_tokens"),
	}

	back := gemini.ToGeminiResponsesResponse(bifrostResp)
	require.NotNil(t, back)
	require.GreaterOrEqual(t, len(back.Candidates), 2,
		"the role flip must flush more than one candidate for this case to mean anything")

	assert.NotEmpty(t, back.Candidates[0].FinishReason,
		"a Gemini client reads candidates[0]; merging the finish reason onto the last candidate hides it")
}
