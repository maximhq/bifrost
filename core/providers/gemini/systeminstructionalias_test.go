package gemini

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for the dropped system_instruction bug.
//
// The Gemini REST surface is protobuf-derived, and protobuf JSON accepts BOTH the lowerCamelCase
// name and the original snake_case field name. Google's own docs and the google-genai SDKs emit
// `system_instruction`, while GeminiGenerationRequest only tagged `systemInstruction`. Anything
// sending the snake_case spelling had its system prompt silently dropped: the request still
// returned 200, it just answered without ever seeing the instruction.
//
// The harness caught it as "system prompt was dropped" on
//   47.8.F /genai :generateContent -> openai/gpt-4o-mini
//   47.8.F /genai :generateContent -> azure/gpt-4o-mini
// both of which returned 200 with a reply that ignored the instruction entirely.
//
// Other types in this package already accept both spellings (see the UnmarshalJSON on
// GenerationConfig and FileData), so this closes an inconsistency rather than adding a new policy.

const sysText = "You are Bifrost QA. Always begin your reply with the word ACK."

func TestGeminiGenerationRequest_AcceptsSnakeCaseSystemInstruction(t *testing.T) {
	body := `{
		"contents": [{"role": "user", "parts": [{"text": "Who are you?"}]}],
		"system_instruction": {"parts": [{"text": "` + sysText + `"}]}
	}`

	var req GeminiGenerationRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))

	require.NotNil(t, req.SystemInstruction, "system_instruction must not be dropped")
	require.Len(t, req.SystemInstruction.Parts, 1)
	assert.Equal(t, sysText, req.SystemInstruction.Parts[0].Text)
}

func TestGeminiGenerationRequest_AcceptsCamelCaseSystemInstruction(t *testing.T) {
	body := `{
		"contents": [{"role": "user", "parts": [{"text": "Who are you?"}]}],
		"systemInstruction": {"parts": [{"text": "` + sysText + `"}]}
	}`

	var req GeminiGenerationRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))

	require.NotNil(t, req.SystemInstruction, "the canonical spelling must keep working")
	require.Len(t, req.SystemInstruction.Parts, 1)
	assert.Equal(t, sysText, req.SystemInstruction.Parts[0].Text)
}

// camelCase is the canonical spelling, so it wins when a client sends both. Picking the other way
// round would let a stale snake_case field silently override the value the caller actually meant.
func TestGeminiGenerationRequest_CamelCaseWinsWhenBothPresent(t *testing.T) {
	body := `{
		"contents": [{"role": "user", "parts": [{"text": "Who are you?"}]}],
		"systemInstruction": {"parts": [{"text": "canonical"}]},
		"system_instruction": {"parts": [{"text": "legacy"}]}
	}`

	var req GeminiGenerationRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))

	require.NotNil(t, req.SystemInstruction)
	require.Len(t, req.SystemInstruction.Parts, 1)
	assert.Equal(t, "canonical", req.SystemInstruction.Parts[0].Text)
}

// The rest of the request must survive the custom unmarshaler untouched - a hand-written
// UnmarshalJSON is exactly where sibling fields get silently lost.
func TestGeminiGenerationRequest_OtherFieldsSurvive(t *testing.T) {
	body := `{
		"model": "gemini-2.5-flash",
		"contents": [{"role": "user", "parts": [{"text": "hi"}]}],
		"system_instruction": {"parts": [{"text": "` + sysText + `"}]},
		"generationConfig": {"temperature": 0.5, "maxOutputTokens": 128},
		"safetySettings": [{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_NONE"}],
		"labels": {"team": "qa"}
	}`

	var req GeminiGenerationRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))

	assert.Equal(t, "gemini-2.5-flash", req.Model)
	require.Len(t, req.Contents, 1)
	require.Len(t, req.Contents[0].Parts, 1)
	assert.Equal(t, "hi", req.Contents[0].Parts[0].Text)
	require.NotNil(t, req.SystemInstruction)
	require.NotNil(t, req.GenerationConfig.Temperature)
	assert.InDelta(t, 0.5, *req.GenerationConfig.Temperature, 0.0001)
	require.Len(t, req.SafetySettings, 1)
	assert.Equal(t, "qa", req.Labels["team"])
}

// GeminiCountTokensRequest EMBEDS GeminiGenerationRequest, so Go promotes the embedded
// UnmarshalJSON to it. Adding a decoder to the inner type therefore silently hijacks decoding of
// the outer one and drops `generateContentRequest` - which is exactly what happened, and what
// GeminiCountTokensRequest.UnmarshalJSON now guards. These two pin both halves of that override:
// the envelope survives, and the snake_case alias still reaches the embedded fields.
func TestGeminiCountTokensRequest_KeepsEnvelopeAndSnakeCaseAlias(t *testing.T) {
	body := `{
		"generateContentRequest": {
			"model": "gemini-2.5-flash",
			"contents": [{"role": "user", "parts": [{"text": "hi"}]}],
			"system_instruction": {"parts": [{"text": "` + sysText + `"}]}
		}
	}`

	var req GeminiCountTokensRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))

	require.NotNil(t, req.GenerateContentRequest, "the envelope must not be swallowed by the embedded decoder")
	require.NotNil(t, req.GenerateContentRequest.SystemInstruction, "snake_case must survive inside the envelope too")
	assert.Equal(t, sysText, req.GenerateContentRequest.SystemInstruction.Parts[0].Text)
}

func TestGeminiCountTokensRequest_UnEnvelopedFormStillDecodes(t *testing.T) {
	body := `{
		"contents": [{"role": "user", "parts": [{"text": "hi"}]}],
		"system_instruction": {"parts": [{"text": "` + sysText + `"}]}
	}`

	var req GeminiCountTokensRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))

	assert.Nil(t, req.GenerateContentRequest, "no envelope was sent")
	require.NotNil(t, req.SystemInstruction, "the embedded fields must still be populated")
	assert.Equal(t, sysText, req.SystemInstruction.Parts[0].Text)
}

// No system instruction at all must stay nil rather than becoming an empty Content, which would
// send an empty systemInstruction upstream and change the request's meaning.
func TestGeminiGenerationRequest_AbsentSystemInstructionStaysNil(t *testing.T) {
	body := `{"contents": [{"role": "user", "parts": [{"text": "hi"}]}]}`

	var req GeminiGenerationRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))

	assert.Nil(t, req.SystemInstruction)
}
