package bedrock_test

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBedrockGuardrailTrace_DecodesAWSConverseShape verifies that the
// BedrockGuardrailTrace struct deserialises a payload that matches the
// AWS Bedrock Runtime GuardrailTraceAssessment schema:
//
//	https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_GuardrailTraceAssessment.html
//
// In particular it pins:
//   - inputAssessment is a map keyed by guardrail ID (singular, not a slice);
//   - outputAssessments is a map whose values are arrays of assessments;
//   - actionReason and modelOutput are surfaced (#2772).
//
// The payload below mirrors the shape AWS returns when a Converse request
// runs with a guardrail attached and `trace = "enabled"`. Before this
// schema fix, sonic.Unmarshal failed on this exact response shape because
// inputAssessment was modelled as a []BedrockGuardrailAssessment slice.
func TestBedrockGuardrailTrace_DecodesAWSConverseShape(t *testing.T) {
	payload := []byte(`{
        "action": "GUARDRAIL_INTERVENED",
        "actionReason": "Guardrail blocked the request",
        "modelOutput": ["redacted"],
        "inputAssessment": {
            "abc123": {
                "topicPolicy": {
                    "topics": [
                        {"name": "Investment Advice", "type": "DENY", "action": "BLOCKED"}
                    ]
                }
            }
        },
        "outputAssessments": {
            "abc123": [
                {
                    "contentPolicy": {
                        "filters": [
                            {"type": "VIOLENCE", "confidence": "HIGH", "action": "BLOCKED"}
                        ]
                    }
                }
            ]
        }
    }`)

	var trace bedrock.BedrockGuardrailTrace
	require.NoError(t, json.Unmarshal(payload, &trace), "payload matching AWS schema must deserialise")

	require.NotNil(t, trace.Action)
	assert.Equal(t, "GUARDRAIL_INTERVENED", *trace.Action)

	require.NotNil(t, trace.ActionReason)
	assert.Equal(t, "Guardrail blocked the request", *trace.ActionReason)

	assert.Equal(t, []string{"redacted"}, trace.ModelOutput)

	require.Contains(t, trace.InputAssessment, "abc123",
		"inputAssessment must be keyed by guardrail ID (singular map, not a slice)")
	assert.NotNil(t, trace.InputAssessment["abc123"].TopicPolicy)

	require.Contains(t, trace.OutputAssessments, "abc123",
		"outputAssessments must be keyed by guardrail ID with array values")
	require.Len(t, trace.OutputAssessments["abc123"], 1)
	assert.NotNil(t, trace.OutputAssessments["abc123"][0].ContentPolicy)
}

// TestBedrockGuardrailTrace_DecodesEmptyTrace covers the non-blocked path
// described in #2772, a request with `trace = "enabled"` that does not
// trip any policy used to fail to decode because the previous schema
// required slice fields. The empty payload here is what the runtime emits
// in that scenario.
func TestBedrockGuardrailTrace_DecodesEmptyTrace(t *testing.T) {
	var trace bedrock.BedrockGuardrailTrace
	require.NoError(t, json.Unmarshal([]byte(`{}`), &trace))
	assert.Nil(t, trace.Action)
	assert.Nil(t, trace.ActionReason)
	assert.Empty(t, trace.InputAssessment)
	assert.Empty(t, trace.OutputAssessments)
}
