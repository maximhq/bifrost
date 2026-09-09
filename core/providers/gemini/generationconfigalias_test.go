package gemini

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for GenerationConfig dropping snake_case fields.
//
// Same bug class as the system_instruction drop fixed in systeminstructionalias_test.go: the
// Gemini REST surface is protobuf-derived, and protobuf JSON accepts BOTH the lowerCamelCase
// name and the original snake_case field name. Google's own SDKs emit snake_case. GenerationConfig
// tagged only camelCase and had no UnmarshalJSON, so every snake_case field was silently dropped -
// the request still returned 200, it just ignored the config entirely.
//
// The harness caught it as "content was not JSON" on 47.4.F /genai :generateContent across SEVEN
// providers (openai, azure, anthropic, bedrock, xai, deepseek, vertex). Every one returned 200 with
// markdown-fenced JSON, because response_mime_type/response_schema never bound and the model was
// merely asked in prose. Vertex went further and answered {"Greeting": {"text": "hi"}} - the wrong
// shape entirely, proving no schema was in force.

const greetingSchemaJSON = `{
	"type": "object",
	"properties": {"text": {"type": "string"}},
	"required": ["text"]
}`

// TestGenerationConfig_AcceptsSnakeCaseStructuredOutput is the cell 47.4.F sends verbatim.
func TestGenerationConfig_AcceptsSnakeCaseStructuredOutput(t *testing.T) {
	body := `{
		"response_mime_type": "application/json",
		"response_schema": ` + greetingSchemaJSON + `
	}`

	var cfg GenerationConfig
	require.NoError(t, json.Unmarshal([]byte(body), &cfg))

	assert.Equal(t, "application/json", cfg.ResponseMIMEType,
		"response_mime_type must not be dropped; without it the model is only asked in prose and replies with fenced JSON")
	require.NotNil(t, cfg.ResponseSchema, "response_schema must not be dropped")
	assert.Contains(t, cfg.ResponseSchema.Properties, "text")
}

// TestGenerationConfig_AcceptsCamelCaseStructuredOutput is the control: the spelling that
// already worked must keep working.
func TestGenerationConfig_AcceptsCamelCaseStructuredOutput(t *testing.T) {
	body := `{
		"responseMimeType": "application/json",
		"responseSchema": ` + greetingSchemaJSON + `
	}`

	var cfg GenerationConfig
	require.NoError(t, json.Unmarshal([]byte(body), &cfg))

	assert.Equal(t, "application/json", cfg.ResponseMIMEType)
	require.NotNil(t, cfg.ResponseSchema)
}

// TestGenerationConfig_CamelCaseWinsOverSnakeCase pins precedence, matching how every other
// alias in this package resolves a conflict: snake_case fills in only when camelCase is absent.
func TestGenerationConfig_CamelCaseWinsOverSnakeCase(t *testing.T) {
	body := `{
		"responseMimeType": "application/json",
		"response_mime_type": "text/plain"
	}`

	var cfg GenerationConfig
	require.NoError(t, json.Unmarshal([]byte(body), &cfg))

	assert.Equal(t, "application/json", cfg.ResponseMIMEType,
		"camelCase must win when both spellings are present")
}

// TestGenerationConfig_AcceptsSnakeCaseCommonFields covers the rest of the config surface. These
// were not what the harness caught, but they are dropped by the exact same mechanism, and Google's
// SDKs emit all of them in snake_case - so fixing only the two observed fields would leave the
// same trap armed for the next caller.
func TestGenerationConfig_AcceptsSnakeCaseCommonFields(t *testing.T) {
	body := `{
		"max_output_tokens": 2048,
		"stop_sequences": ["END"],
		"top_p": 0.5,
		"top_k": 7,
		"candidate_count": 1,
		"presence_penalty": 0.25,
		"frequency_penalty": 0.75,
		"response_logprobs": true,
		"media_resolution": "MEDIA_RESOLUTION_LOW",
		"thinking_config": {"thinking_budget": 1024, "include_thoughts": true}
	}`

	var cfg GenerationConfig
	require.NoError(t, json.Unmarshal([]byte(body), &cfg))

	assert.Equal(t, int32(2048), cfg.MaxOutputTokens, "max_output_tokens dropped")
	assert.Equal(t, []string{"END"}, cfg.StopSequences, "stop_sequences dropped")
	require.NotNil(t, cfg.TopP, "top_p dropped")
	assert.InDelta(t, 0.5, *cfg.TopP, 1e-9)
	require.NotNil(t, cfg.TopK, "top_k dropped")
	assert.Equal(t, 7, *cfg.TopK)
	assert.Equal(t, int32(1), cfg.CandidateCount, "candidate_count dropped")
	require.NotNil(t, cfg.PresencePenalty, "presence_penalty dropped")
	require.NotNil(t, cfg.FrequencyPenalty, "frequency_penalty dropped")
	assert.True(t, cfg.ResponseLogprobs, "response_logprobs dropped")
	assert.Equal(t, "MEDIA_RESOLUTION_LOW", cfg.MediaResolution, "media_resolution dropped")
	require.NotNil(t, cfg.ThinkingConfig, "thinking_config dropped")
	require.NotNil(t, cfg.ThinkingConfig.ThinkingBudget)
	assert.Equal(t, int32(1024), *cfg.ThinkingConfig.ThinkingBudget)
	assert.True(t, cfg.ThinkingConfig.IncludeThoughts)
}
