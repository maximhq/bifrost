package bedrock

import (
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// BedrockBatchJobRequest represents a request to create a batch inference job.
type BedrockBatchJobRequest struct {
	JobName                string                  `json:"jobName"`
	ModelID                *string                 `json:"modelId"`
	RoleArn                string                  `json:"roleArn"`
	InputDataConfig        BedrockInputDataConfig  `json:"inputDataConfig"`
	OutputDataConfig       BedrockOutputDataConfig `json:"outputDataConfig"`
	TimeoutDurationInHours int                     `json:"timeoutDurationInHours,omitempty"`
	Tags                   []BedrockTag            `json:"tags,omitempty"`
}

// BedrockInputDataConfig represents the input configuration for a batch job.
type BedrockInputDataConfig struct {
	S3InputDataConfig BedrockS3InputDataConfig `json:"s3InputDataConfig"`
}

// BedrockS3InputDataConfig represents S3 input configuration.
type BedrockS3InputDataConfig struct {
	S3Uri         string  `json:"s3Uri"`
	S3InputFormat string  `json:"s3InputFormat,omitempty"` // "JSONL"
	Endpoint      *string `json:"endpoint,omitempty"`
	FileID        *string `json:"file_id,omitempty"`
}

// BedrockOutputDataConfig represents the output configuration for a batch job.
type BedrockOutputDataConfig struct {
	S3OutputDataConfig BedrockS3OutputDataConfig `json:"s3OutputDataConfig"`
}

// BedrockS3OutputDataConfig represents S3 output configuration.
type BedrockS3OutputDataConfig struct {
	S3Uri string `json:"s3Uri"`
}

// BedrockTag represents a tag for a batch job.
type BedrockTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// BedrockBatchJobResponse represents a batch job response.
type BedrockBatchJobResponse struct {
	JobArn                 string                   `json:"jobArn"`
	Status                 string                   `json:"status"`
	JobName                string                   `json:"jobName,omitempty"`
	ModelID                string                   `json:"modelId,omitempty"`
	RoleArn                string                   `json:"roleArn,omitempty"`
	InputDataConfig        *BedrockInputDataConfig  `json:"inputDataConfig,omitempty"`
	OutputDataConfig       *BedrockOutputDataConfig `json:"outputDataConfig,omitempty"`
	VpcConfig              *BedrockVpcConfig        `json:"vpcConfig,omitempty"`
	SubmitTime             *time.Time               `json:"submitTime,omitempty"`
	LastModifiedTime       *time.Time               `json:"lastModifiedTime,omitempty"`
	EndTime                *time.Time               `json:"endTime,omitempty"`
	Message                string                   `json:"message,omitempty"`
	ClientRequestToken     string                   `json:"clientRequestToken,omitempty"`
	JobExpirationTime      *time.Time               `json:"jobExpirationTime,omitempty"`
	TimeoutDurationInHours int                      `json:"timeoutDurationInHours,omitempty"`
}

// BedrockBatchJobListResponse represents a list of batch jobs.
type BedrockBatchJobListResponse struct {
	InvocationJobSummaries []BedrockBatchJobSummary `json:"invocationJobSummaries"`
	NextToken              *string                  `json:"nextToken,omitempty"`
}

// BedrockBatchJobSummary represents a summary of a batch job.
type BedrockBatchJobSummary struct {
	JobArn           string     `json:"jobArn"`
	JobName          string     `json:"jobName"`
	ModelID          string     `json:"modelId"`
	Status           string     `json:"status"`
	SubmitTime       *time.Time `json:"submitTime,omitempty"`
	LastModifiedTime *time.Time `json:"lastModifiedTime,omitempty"`
	EndTime          *time.Time `json:"endTime,omitempty"`
	Message          string     `json:"message,omitempty"`
}

// BedrockBatchResultRecord represents a single result record in Bedrock batch output JSONL.
type BedrockBatchResultRecord struct {
	RecordID    string                 `json:"recordId"`
	ModelOutput map[string]interface{} `json:"modelOutput,omitempty"`
	Error       *BedrockBatchError     `json:"error,omitempty"`
}

// BedrockBatchError represents an error in batch processing.
type BedrockBatchError struct {
	ErrorCode    int    `json:"errorCode,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

// BedrockBatchListRequest represents a request to list batch jobs.
type BedrockBatchListRequest struct {
	MaxResults   int     `json:"maxResults,omitempty"`
	NextToken    *string `json:"nextToken,omitempty"`
	StatusEquals string  `json:"statusEquals,omitempty"`
	NameContains string  `json:"nameContains,omitempty"`
}

// BedrockBatchRetrieveRequest represents a request to retrieve a batch job.
type BedrockBatchRetrieveRequest struct {
	JobIdentifier string `json:"jobIdentifier"`
}

// BedrockBatchCancelRequest represents a request to cancel/stop a batch job.
type BedrockBatchCancelRequest struct {
	JobIdentifier string `json:"jobIdentifier"`
}

// BedrockBatchCancelResponse represents the response from stopping a batch job.
type BedrockBatchCancelResponse struct {
	JobArn string `json:"jobArn"`
	Status string `json:"status"`
}

// ToBifrostBatchStatus converts Bedrock status to Bifrost status.
func ToBifrostBatchStatus(status string) schemas.BatchStatus {
	switch status {
	case "Submitted", "Validating":
		return schemas.BatchStatusValidating
	case "InProgress":
		return schemas.BatchStatusInProgress
	case "Completed":
		return schemas.BatchStatusCompleted
	case "Failed", "PartiallyCompleted":
		return schemas.BatchStatusFailed
	case "Stopping":
		return schemas.BatchStatusCancelling
	case "Stopped":
		return schemas.BatchStatusCancelled
	case "Expired":
		return schemas.BatchStatusExpired
	case "Scheduled":
		return schemas.BatchStatusValidating
	default:
		return schemas.BatchStatus(status)
	}
}

// parseBatchResultsJSONL parses JSONL content from Bedrock batch output into Bifrost format.
// Returns the parsed results and any parse errors encountered.
func parseBatchResultsJSONL(content []byte, provider *BedrockProvider) ([]schemas.BatchResultItem, []schemas.BatchError) {
	var results []schemas.BatchResultItem

	parseResult := providerUtils.ParseJSONL(content, func(line []byte) error {
		var bedrockResult BedrockBatchResultRecord
		if err := sonic.Unmarshal(line, &bedrockResult); err != nil {
			provider.logger.Warn(fmt.Sprintf("failed to parse batch result line: %v", err))
			return err
		}

		// Convert Bedrock format to Bifrost format
		resultItem := schemas.BatchResultItem{
			CustomID: bedrockResult.RecordID,
		}

		if bedrockResult.ModelOutput != nil {
			resultItem.Response = &schemas.BatchResultResponse{
				StatusCode: 200,
				Body:       bedrockResult.ModelOutput,
			}
		}

		if bedrockResult.Error != nil {
			resultItem.Error = &schemas.BatchResultError{
				Code:    fmt.Sprintf("%d", bedrockResult.Error.ErrorCode),
				Message: bedrockResult.Error.ErrorMessage,
			}
			// Set status code to indicate error if there's an error
			if resultItem.Response == nil {
				resultItem.Response = &schemas.BatchResultResponse{
					StatusCode: bedrockResult.Error.ErrorCode,
				}
			}
		}

		results = append(results, resultItem)
		return nil
	})

	return results, parseResult.Errors
}

// ToBedrockBatchJobResponse converts a Bifrost batch create response to Bedrock format.
func ToBedrockBatchJobResponse(resp *schemas.BifrostBatchCreateResponse) *BedrockBatchJobResponse {
	// Here if the provider is not Bedrock - then we create a dummy arn and string using the batch ID
	if resp.ExtraFields.Provider != schemas.Bedrock {
		return &BedrockBatchJobResponse{
			JobArn: fmt.Sprintf("arn:aws:bedrock:us-east-1:444444444444:batch:%s", resp.ID),
			Status: toBedrockBatchStatus(resp.Status),
		}
	}
	// For bedrock, we go as is
	result := &BedrockBatchJobResponse{
		JobArn: resp.ID,
		Status: toBedrockBatchStatus(resp.Status),
	}

	if resp.Metadata != nil {
		if jobName, ok := resp.Metadata["job_name"]; ok {
			result.JobName = jobName
		}
	}

	if resp.CreatedAt > 0 {
		t := time.Unix(resp.CreatedAt, 0)
		result.SubmitTime = &t
	}

	return result
}

// ToBedrockBatchJobListResponse converts a Bifrost batch list response to Bedrock format.
func ToBedrockBatchJobListResponse(resp *schemas.BifrostBatchListResponse) *BedrockBatchJobListResponse {
	result := &BedrockBatchJobListResponse{
		InvocationJobSummaries: make([]BedrockBatchJobSummary, len(resp.Data)),
	}

	for i, batch := range resp.Data {
		summary := BedrockBatchJobSummary{
			JobArn: batch.ID,
			Status: toBedrockBatchStatus(batch.Status),
		}

		if batch.Metadata != nil {
			if jobName, ok := batch.Metadata["job_name"]; ok {
				summary.JobName = jobName
			}
			if modelId, ok := batch.Metadata["model_id"]; ok {
				summary.ModelID = modelId
			}
		}

		if batch.CreatedAt > 0 {
			t := time.Unix(batch.CreatedAt, 0)
			summary.SubmitTime = &t
		}

		if batch.CompletedAt != nil && *batch.CompletedAt > 0 {
			t := time.Unix(*batch.CompletedAt, 0)
			summary.EndTime = &t
		}

		result.InvocationJobSummaries[i] = summary
	}

	if resp.LastID != nil {
		result.NextToken = resp.LastID
	}

	return result
}

// ToBedrockBatchJobRetrieveResponse converts a Bifrost batch retrieve response to Bedrock format.
func ToBedrockBatchJobRetrieveResponse(resp *schemas.BifrostBatchRetrieveResponse) *BedrockBatchJobResponse {
	result := &BedrockBatchJobResponse{
		JobArn: resp.ID,
		Status: toBedrockBatchStatus(resp.Status),
	}

	if resp.Metadata != nil {
		if jobName, ok := resp.Metadata["job_name"]; ok {
			result.JobName = jobName
		}
	}

	if resp.CreatedAt > 0 {
		t := time.Unix(resp.CreatedAt, 0)
		result.SubmitTime = &t
	}

	if resp.CompletedAt != nil && *resp.CompletedAt > 0 {
		t := time.Unix(*resp.CompletedAt, 0)
		result.EndTime = &t
	}

	if resp.InputFileID != "" {
		result.InputDataConfig = &BedrockInputDataConfig{
			S3InputDataConfig: BedrockS3InputDataConfig{
				S3Uri:         resp.InputFileID,
				S3InputFormat: "JSONL",
			},
		}
	}

	if resp.OutputFileID != nil && *resp.OutputFileID != "" {
		result.OutputDataConfig = &BedrockOutputDataConfig{
			S3OutputDataConfig: BedrockS3OutputDataConfig{
				S3Uri: *resp.OutputFileID,
			},
		}
	}

	return result
}

// toBedrockBatchStatus converts Bifrost batch status to Bedrock status.
func toBedrockBatchStatus(status schemas.BatchStatus) string {
	switch status {
	case schemas.BatchStatusValidating:
		return "Validating"
	case schemas.BatchStatusInProgress:
		return "InProgress"
	case schemas.BatchStatusCompleted:
		fallthrough
	case schemas.BatchStatusEnded:
		return "Completed"
	case schemas.BatchStatusFailed:
		return "Failed"
	case schemas.BatchStatusCancelling:
		return "Stopping"
	case schemas.BatchStatusCancelled:
		return "Stopped"
	case schemas.BatchStatusExpired:
		return "Expired"
	default:
		return string(status)
	}
}

// ToBifrostBatchListRequest converts a Bedrock batch list request to Bifrost format.
func ToBifrostBatchListRequest(req *BedrockBatchListRequest, provider schemas.ModelProvider) *schemas.BifrostBatchListRequest {
	result := &schemas.BifrostBatchListRequest{
		Provider: provider,
		Limit:    req.MaxResults,
	}

	if req.NextToken != nil {
		result.PageToken = req.NextToken
	}

	if req.StatusEquals != "" || req.NameContains != "" {
		result.ExtraParams = make(map[string]interface{})
		if req.StatusEquals != "" {
			result.ExtraParams["statusEquals"] = req.StatusEquals
		}
		if req.NameContains != "" {
			result.ExtraParams["nameContains"] = req.NameContains
		}
	}

	return result
}

// ToBifrostBatchRetrieveRequest converts a Bedrock batch retrieve request to Bifrost format.
func ToBifrostBatchRetrieveRequest(req *BedrockBatchRetrieveRequest, provider schemas.ModelProvider) *schemas.BifrostBatchRetrieveRequest {
	return &schemas.BifrostBatchRetrieveRequest{
		Provider: provider,
		BatchID:  req.JobIdentifier,
	}
}

// ToBifrostBatchCancelRequest converts a Bedrock batch cancel request to Bifrost format.
func ToBifrostBatchCancelRequest(req *BedrockBatchCancelRequest, provider schemas.ModelProvider) *schemas.BifrostBatchCancelRequest {
	return &schemas.BifrostBatchCancelRequest{
		Provider: provider,
		BatchID:  req.JobIdentifier,
	}
}

// ToBedrockBatchCancelResponse converts a Bifrost batch cancel response to Bedrock format.
func ToBedrockBatchCancelResponse(resp *schemas.BifrostBatchCancelResponse) *BedrockBatchCancelResponse {
	return &BedrockBatchCancelResponse{
		JobArn: resp.ID,
		Status: toBedrockBatchStatus(resp.Status),
	}
}

// splitJSONL splits JSONL content into individual lines.
func splitJSONL(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				lines = append(lines, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// BedrockVpcConfig represents VPC configuration for a batch job.
type BedrockVpcConfig struct {
	SecurityGroupIds []string `json:"securityGroupIds,omitempty"`
	SubnetIds        []string `json:"subnetIds,omitempty"`
}

// BedrockBatchManifest represents the manifest.json.out file structure from S3.
type BedrockBatchManifest struct {
	TotalRecordCount     int `json:"totalRecordCount"`
	ProcessedRecordCount int `json:"processedRecordCount"`
	ErrorRecordCount     int `json:"errorRecordCount"`
}
