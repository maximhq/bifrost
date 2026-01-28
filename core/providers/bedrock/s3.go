package bedrock

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// uploadToS3 uploads content to an S3 bucket using the provided credentials.
func uploadToS3(
	ctx context.Context,
	accessKey, secretKey string,
	sessionToken *string,
	region string,
	bucket, key string,
	content []byte,
	providerName schemas.ModelProvider,
) *schemas.BifrostError {
	// Create AWS config with credentials
	var cfg aws.Config
	var err error

	if accessKey != "" && secretKey != "" {
		// Use provided credentials
		var creds aws.CredentialsProvider
		if sessionToken != nil && *sessionToken != "" {
			creds = credentials.NewStaticCredentialsProvider(accessKey, secretKey, *sessionToken)
		} else {
			creds = credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")
		}

		cfg, err = config.LoadDefaultConfig(ctx,
			config.WithRegion(region),
			config.WithCredentialsProvider(creds),
		)
	} else {
		// Use default credentials chain (IAM role, env vars, etc.)
		cfg, err = config.LoadDefaultConfig(ctx, config.WithRegion(region))
	}

	if err != nil {
		return providerUtils.NewBifrostOperationError("failed to load AWS config for S3", err, providerName)
	}

	// Create S3 client
	client := s3.NewFromConfig(cfg)

	// Upload the content
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(content),
		ContentType: aws.String("application/jsonl"),
	})
	if err != nil {
		return providerUtils.NewBifrostOperationError(fmt.Sprintf("failed to upload to S3: %s/%s", bucket, key), err, providerName)
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
		line, err := sonic.Marshal(bedrockReq)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal batch request item %s: %w", req.CustomID, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	return buf.Bytes(), nil
}
