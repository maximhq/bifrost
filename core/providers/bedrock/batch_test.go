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

// TestBatchJobMetadata_CarriesBedrockStatus verifies the raw Bedrock job
// status is kept in Metadata, since ToBifrostBatchStatus folds Submitted,
// Validating and Scheduled into one Bifrost status.
func TestBatchJobMetadata_CarriesBedrockStatus(t *testing.T) {
	metadata := batchJobMetadata("job-1", "anthropic.claude-3", "Scheduled")
	assert.Equal(t, "job-1", metadata["job_name"])
	assert.Equal(t, "anthropic.claude-3", metadata["model_id"])
	assert.Equal(t, "Scheduled", metadata[BatchMetadataBedrockStatus])
	assert.Equal(t, schemas.BatchStatusValidating, ToBifrostBatchStatus("Scheduled"))

	assert.Empty(t, batchJobMetadata("", "", ""))
}
