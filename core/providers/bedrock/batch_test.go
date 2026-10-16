package bedrock

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
)

// TestToBedrockBatchJobRetrieveResponse_SurfacesFailureMessage verifies the
// AWS job failure reason carried in the normalized Errors field is mapped back
// to Bedrock's native message field, so callers can see why a job failed
// without dropping to the AWS CLI.
func TestToBedrockBatchJobRetrieveResponse_SurfacesFailureMessage(t *testing.T) {
	const failure = "Batch job arn:... contains less records (1) than the required minimum of: 100"

	resp := &schemas.BifrostBatchRetrieveResponse{
		ID:     "arn:aws:bedrock:us-east-1:123:model-invocation-job/abc",
		Status: schemas.BatchStatusFailed,
		Errors: &schemas.BatchErrors{
			Object: "list",
			Data:   []schemas.BatchError{{Message: failure}},
		},
	}

	out := ToBedrockBatchJobRetrieveResponse(resp)
	assert.Equal(t, "Failed", out.Status)
	assert.Equal(t, failure, out.Message)
}

// TestToBedrockBatchJobRetrieveResponse_NoErrors confirms the message stays
// empty when there is no failure reason.
func TestToBedrockBatchJobRetrieveResponse_NoErrors(t *testing.T) {
	resp := &schemas.BifrostBatchRetrieveResponse{
		ID:     "arn:aws:bedrock:us-east-1:123:model-invocation-job/abc",
		Status: schemas.BatchStatusCompleted,
	}

	out := ToBedrockBatchJobRetrieveResponse(resp)
	assert.Empty(t, out.Message)
}

// TestResolveBedrockBatchModelID verifies the batch modelId carries the full
// inference profile ARN when one is configured, since CreateModelInvocationJob
// rejects a bare application inference profile ID.
func TestResolveBedrockBatchModelID(t *testing.T) {
	keyARN := "arn:aws:bedrock:us-east-1:123456789012:application-inference-profile"
	aliasARN := "arn:aws:bedrock:us-east-1:210987654321:application-inference-profile"
	withARN := schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{ARN: schemas.NewSecretVar(keyARN)}}
	noARN := schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{}}

	aliasCtx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	aliasCtx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "batch-model",
		Config: &schemas.AliasConfig{
			ModelID:         "abc123profile",
			BedrockAliasCfg: &schemas.BedrockAliasCfg{InferenceProfileARN: schemas.NewSecretVar(aliasARN)},
		},
	})

	tests := []struct {
		name  string
		ctx   *schemas.BifrostContext
		key   schemas.Key
		model string
		want  string
	}{
		{"key ARN joined to bare profile ID", nil, withARN, "abc123profile", keyARN + "/abc123profile"},
		{"alias ARN overrides key ARN", aliasCtx, withARN, "abc123profile", aliasARN + "/abc123profile"},
		{"region prefix stripped before joining", nil, withARN, "us-east-1/abc123profile", keyARN + "/abc123profile"},
		{"full ARN passed through unchanged", nil, withARN, keyARN + "/abc123profile", keyARN + "/abc123profile"},
		{"no ARN configured leaves model unchanged", nil, noARN, "anthropic.claude-haiku-4-5-20251001-v1:0", "anthropic.claude-haiku-4-5-20251001-v1:0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, resolveBedrockBatchModelID(tc.ctx, tc.key, tc.model))
		})
	}
}
