package bedrock

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// uploadToS3 uploads content to an S3 bucket using the credentials configured
// on the supplied BedrockKeyConfig. Resolution (static keys, default chain,
// named profile, STS AssumeRole) goes through resolveAWSConfig so this path
// honors the same auth modes as the request-signing path — in particular
// role_arn is applied when set, so a key configured for cross-account
// AssumeRole uploads under the assumed role rather than the source identity.
func uploadToS3(
	ctx context.Context,
	cfg *schemas.BedrockKeyConfig,
	region string,
	bucket, key string,
	content []byte,
) *schemas.BifrostError {
	if cfg == nil {
		// Default credential chain only (no profile / role).
		empty := schemas.EnvVar{}
		awsCfg, bifrostErr := resolveAWSConfig(ctx, empty, empty, nil, nil, nil, nil, nil, region)
		if bifrostErr != nil {
			return bifrostErr
		}
		return s3PutObject(ctx, awsCfg, bucket, key, content)
	}
	awsCfg, bifrostErr := resolveAWSConfig(ctx,
		cfg.AccessKey, cfg.SecretKey,
		cfg.SessionToken, cfg.Profile,
		cfg.RoleARN, cfg.ExternalID, cfg.RoleSessionName,
		region)
	if bifrostErr != nil {
		return bifrostErr
	}
	return s3PutObject(ctx, awsCfg, bucket, key, content)
}

// s3PutObject performs the actual S3 PutObject call. Split out so callers can
// share the same client construction without duplicating the resolveAWSConfig
// boilerplate.
func s3PutObject(ctx context.Context, awsCfg aws.Config, bucket, key string, content []byte) *schemas.BifrostError {
	client := s3.NewFromConfig(awsCfg)
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(content),
		ContentType: aws.String("application/jsonl"),
	}); err != nil {
		return providerUtils.NewBifrostOperationError(fmt.Sprintf("failed to upload to s3: %s/%s", bucket, key), err)
	}
	return nil
}

// generateBatchInputS3Key generates a unique S3 key for batch input files.
func generateBatchInputS3Key(jobName string) string {
	timestamp := time.Now().UnixNano()
	return fmt.Sprintf("bifrost-batch-input/%s-%d.jsonl", jobName, timestamp)
}

// deriveInputS3URIFromOutput derives an input S3 URI from the output S3 URI.
// It uses the same bucket but with a different path for input files.
func deriveInputS3URIFromOutput(outputS3URI, inputKey string) string {
	bucket, _ := parseS3URI(outputS3URI)
	return fmt.Sprintf("s3://%s/%s", bucket, inputKey)
}

// ConvertBedrockRequestsToJSONL converts batch request items to JSONL format for Bedrock.
// Bedrock uses a specific format for batch inference requests.
func ConvertBedrockRequestsToJSONL(requests []schemas.BatchRequestItem, modelID *string) ([]byte, error) {
	// Model ID is required for Bedrock batch JSONL conversion
	if modelID == nil || *modelID == "" {
		return nil, fmt.Errorf("modelID is required for Bedrock batch JSONL conversion")
	}
	// Initialize the buffer
	var buf bytes.Buffer

	// Iterate over the requests
	for _, req := range requests {
		// Build the Bedrock batch request format
		bedrockReq := map[string]interface{}{
			"recordId": req.CustomID,
			"modelInput": map[string]interface{}{
				"modelId": *modelID,
			},
		}

		// If the request has a body, use it as the model input parameters
		if req.Body != nil {
			modelInput := bedrockReq["modelInput"].(map[string]interface{})
			for k, v := range req.Body {
				if k != "model" { // Don't override modelId
					modelInput[k] = v
				}
			}
		} else if req.Params != nil {
			modelInput := bedrockReq["modelInput"].(map[string]interface{})
			for k, v := range req.Params {
				if k != "model" {
					modelInput[k] = v
				}
			}
		}

		// Marshal the request as a JSON line
		line, err := providerUtils.MarshalSorted(bedrockReq)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal batch request item %s: %w", req.CustomID, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	return buf.Bytes(), nil
}
