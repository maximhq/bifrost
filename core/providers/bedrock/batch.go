package bedrock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// BedrockBatchJobRequest represents a request to create a batch inference job.
type BedrockBatchJobRequest struct {
	JobName                string                  `json:"jobName"`
	ModelID                string                  `json:"modelId"`
	RoleArn                string                  `json:"roleArn"`
	InputDataConfig        BedrockInputDataConfig  `json:"inputDataConfig"`
	OutputDataConfig       BedrockOutputDataConfig `json:"outputDataConfig"`
	TimeoutDurationInHours int                     `json:"timeoutDurationInHours,omitempty"`
	Tags                   []BedrockTag            `json:"tags,omitempty"`
	Provider               schemas.ModelProvider   `json:"-"` // For cross-provider routing (not serialized)
}

// BedrockInputDataConfig represents the input configuration for a batch job.
type BedrockInputDataConfig struct {
	S3InputDataConfig BedrockS3InputDataConfig `json:"s3InputDataConfig"`
}

// BedrockS3InputDataConfig represents S3 input configuration.
type BedrockS3InputDataConfig struct {
	S3Uri         string `json:"s3Uri"`
	S3InputFormat string `json:"s3InputFormat,omitempty"` // "JSONL"
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
	JobArn             string                    `json:"jobArn"`
	Status             string                    `json:"status"`
	JobName            string                    `json:"jobName,omitempty"`
	ModelId            string                    `json:"modelId,omitempty"`
	RoleArn            string                    `json:"roleArn,omitempty"`
	InputDataConfig    *BedrockInputDataConfig   `json:"inputDataConfig,omitempty"`
	OutputDataConfig   *BedrockOutputDataConfig  `json:"outputDataConfig,omitempty"`
	SubmitTime         *time.Time                `json:"submitTime,omitempty"`
	LastModifiedTime   *time.Time                `json:"lastModifiedTime,omitempty"`
	EndTime            *time.Time                `json:"endTime,omitempty"`
	Message            string                    `json:"message,omitempty"`
	ClientRequestToken string                    `json:"clientRequestToken,omitempty"`
	JobExpirationTime  *time.Time                `json:"jobExpirationTime,omitempty"`
	TimeoutDurationInHours int                   `json:"timeoutDurationInHours,omitempty"`
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
	ModelId          string     `json:"modelId"`
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

// BatchCreate creates a new batch inference job on AWS Bedrock.
func (provider *BedrockProvider) BatchCreate(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.BatchCreateRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	// Require RoleArn in extra params
	roleArn := ""
	if request.ExtraParams != nil {
		if r, ok := request.ExtraParams["role_arn"].(string); ok {
			roleArn = r
		}
	}
	if roleArn == "" {
		return nil, providerUtils.NewBifrostOperationError("role_arn is required for Bedrock batch API (provide in extra_params)", nil, providerName)
	}

	// Get output S3 URI from extra params
	outputS3Uri := ""
	if request.ExtraParams != nil {
		if o, ok := request.ExtraParams["output_s3_uri"].(string); ok {
			outputS3Uri = o
		}
	}
	if outputS3Uri == "" {
		return nil, providerUtils.NewBifrostOperationError("output_s3_uri is required for Bedrock batch API (provide in extra_params)", nil, providerName)
	}

	// Get model ID
	modelID := request.Model
	if key.BedrockKeyConfig.Deployments != nil {
		if deployment, ok := key.BedrockKeyConfig.Deployments[request.Model]; ok {
			modelID = deployment
		}
	}

	// Generate job name
	jobName := fmt.Sprintf("bifrost-batch-%d", time.Now().Unix())
	if request.Metadata != nil {
		if name, ok := request.Metadata["job_name"]; ok {
			jobName = name
		}
	}

	// Determine input file ID (S3 URI)
	inputFileID := request.InputFileID

	// If no S3 URI provided but inline requests are available, upload them to S3 first
	if inputFileID == "" && len(request.Requests) > 0 {
		// Get region for S3 upload
		region := DefaultBedrockRegion
		if key.BedrockKeyConfig.Region != nil {
			region = *key.BedrockKeyConfig.Region
		}

		// Convert inline requests to Bedrock JSONL format
		jsonlData, err := ConvertBedrockRequestsToJSONL(request.Requests, modelID)
		if err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to convert requests to JSONL", err, providerName)
		}

		// Generate S3 key for the input file
		inputKey := generateBatchInputS3Key(jobName)

		// Derive bucket from output S3 URI
		inputS3URI := deriveInputS3URIFromOutput(outputS3Uri, inputKey)
		bucket, s3Key := parseS3URI(inputS3URI)

		// Upload to S3 using Bedrock credentials
		if bifrostErr := uploadToS3(
			ctx,
			key.BedrockKeyConfig.AccessKey,
			key.BedrockKeyConfig.SecretKey,
			key.BedrockKeyConfig.SessionToken,
			region,
			bucket,
			s3Key,
			jsonlData,
			providerName,
		); bifrostErr != nil {
			return nil, bifrostErr
		}

		inputFileID = inputS3URI
	}

	// Validate that we have an input file ID (either provided or uploaded)
	if inputFileID == "" {
		return nil, providerUtils.NewBifrostOperationError("either input_file_id (S3 URI) or requests array is required for Bedrock batch API", nil, providerName)
	}

	// Build request
	bedrockReq := &BedrockBatchJobRequest{
		JobName: jobName,
		ModelID: modelID,
		RoleArn: roleArn,
		InputDataConfig: BedrockInputDataConfig{
			S3InputDataConfig: BedrockS3InputDataConfig{
				S3Uri:         inputFileID,
				S3InputFormat: "JSONL",
			},
		},
		OutputDataConfig: BedrockOutputDataConfig{
			S3OutputDataConfig: BedrockS3OutputDataConfig{
				S3Uri: outputS3Uri,
			},
		},
	}

	// Set timeout if provided
	if request.CompletionWindow != "" {
		// Parse completion window (e.g., "24h" -> 24)
		if d, err := time.ParseDuration(request.CompletionWindow); err == nil {
			bedrockReq.TimeoutDurationInHours = int(d.Hours())
		}
	}

	jsonData, err := sonic.Marshal(bedrockReq)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestMarshal, err, providerName)
	}

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil {
		region = *key.BedrockKeyConfig.Region
	}

	// Create HTTP request
	reqURL := fmt.Sprintf("https://bedrock.%s.amazonaws.com/model-invocation-job", region)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", err, providerName)
	}

	// Sign request
	if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "bedrock", providerName); err != nil {
		return nil, err
	}

	// Execute request
	startTime := time.Now()
	resp, err := provider.client.Do(httpReq)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error reading response", err, providerName)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		var errorResp BedrockError
		if err := sonic.Unmarshal(body, &errorResp); err == nil && errorResp.Message != "" {
			return nil, providerUtils.NewProviderAPIError(errorResp.Message, nil, resp.StatusCode, providerName, nil, nil)
		}
		return nil, providerUtils.NewProviderAPIError(string(body), nil, resp.StatusCode, providerName, nil, nil)
	}

	var bedrockResp BedrockBatchJobResponse
	if err := sonic.Unmarshal(body, &bedrockResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	result := &schemas.BifrostBatchCreateResponse{
		ID:               bedrockResp.JobArn,
		Object:           "batch",
		InputFileID:      inputFileID,
		Status:           ToBifrostBatchStatus(bedrockResp.Status),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchCreateRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if bedrockResp.SubmitTime != nil {
		result.CreatedAt = bedrockResp.SubmitTime.Unix()
	}
	if bedrockResp.JobExpirationTime != nil {
		result.ExpiresAt = bedrockResp.JobExpirationTime.Unix()
	}

	return result, nil
}

// BatchList lists batch inference jobs from AWS Bedrock.
func (provider *BedrockProvider) BatchList(ctx context.Context, keys []schemas.Key, request *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.BatchListRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if len(keys) == 0 {
		return nil, providerUtils.NewConfigurationError("no keys provided", providerName)
	}

	key := keys[0]
	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil {
		region = *key.BedrockKeyConfig.Region
	}

	// Build URL with query params
	params := url.Values{}
	if request.Limit > 0 {
		params.Set("maxResults", fmt.Sprintf("%d", request.Limit))
	}
	if request.PageToken != nil {
		params.Set("nextToken", *request.PageToken)
	}

	reqURL := fmt.Sprintf("https://bedrock.%s.amazonaws.com/model-invocation-jobs", region)
	if len(params) > 0 {
		reqURL += "?" + params.Encode()
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", err, providerName)
	}

	// Sign request
	if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "bedrock", providerName); err != nil {
		return nil, err
	}

	// Execute request
	startTime := time.Now()
	resp, err := provider.client.Do(httpReq)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error reading response", err, providerName)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp BedrockError
		if err := sonic.Unmarshal(body, &errorResp); err == nil && errorResp.Message != "" {
			return nil, providerUtils.NewProviderAPIError(errorResp.Message, nil, resp.StatusCode, providerName, nil, nil)
		}
		return nil, providerUtils.NewProviderAPIError(string(body), nil, resp.StatusCode, providerName, nil, nil)
	}

	var bedrockResp BedrockBatchJobListResponse
	if err := sonic.Unmarshal(body, &bedrockResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostBatchListResponse{
		Object:  "list",
		Data:    make([]schemas.BifrostBatchRetrieveResponse, len(bedrockResp.InvocationJobSummaries)),
		HasMore: bedrockResp.NextToken != nil,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchListRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	for i, job := range bedrockResp.InvocationJobSummaries {
		var createdAt int64
		if job.SubmitTime != nil {
			createdAt = job.SubmitTime.Unix()
		}
		bifrostResp.Data[i] = schemas.BifrostBatchRetrieveResponse{
			ID:        job.JobArn,
			Object:    "batch",
			Status:    ToBifrostBatchStatus(job.Status),
			CreatedAt: createdAt,
		}
	}

	return bifrostResp, nil
}

// BatchRetrieve retrieves a specific batch inference job from AWS Bedrock.
func (provider *BedrockProvider) BatchRetrieve(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.BatchRetrieveRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id (job ARN) is required", nil, providerName)
	}

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil {
		region = *key.BedrockKeyConfig.Region
	}

	// URL encode the job ARN
	encodedJobArn := url.PathEscape(request.BatchID)
	reqURL := fmt.Sprintf("https://bedrock.%s.amazonaws.com/model-invocation-job/%s", region, encodedJobArn)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", err, providerName)
	}

	// Sign request
	if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "bedrock", providerName); err != nil {
		return nil, err
	}

	// Execute request
	startTime := time.Now()
	resp, err := provider.client.Do(httpReq)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error reading response", err, providerName)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp BedrockError
		if err := sonic.Unmarshal(body, &errorResp); err == nil && errorResp.Message != "" {
			return nil, providerUtils.NewProviderAPIError(errorResp.Message, nil, resp.StatusCode, providerName, nil, nil)
		}
		return nil, providerUtils.NewProviderAPIError(string(body), nil, resp.StatusCode, providerName, nil, nil)
	}

	var bedrockResp BedrockBatchJobResponse
	if err := sonic.Unmarshal(body, &bedrockResp); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err, providerName)
	}

	result := &schemas.BifrostBatchRetrieveResponse{
		ID:     bedrockResp.JobArn,
		Object: "batch",
		Status: ToBifrostBatchStatus(bedrockResp.Status),
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchRetrieveRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}

	if bedrockResp.InputDataConfig != nil {
		result.InputFileID = bedrockResp.InputDataConfig.S3InputDataConfig.S3Uri
	}
	if bedrockResp.OutputDataConfig != nil {
		outputUri := bedrockResp.OutputDataConfig.S3OutputDataConfig.S3Uri
		result.OutputFileID = &outputUri
	}
	if bedrockResp.SubmitTime != nil {
		result.CreatedAt = bedrockResp.SubmitTime.Unix()
	}
	if bedrockResp.EndTime != nil {
		completedAt := bedrockResp.EndTime.Unix()
		result.CompletedAt = &completedAt
	}
	if bedrockResp.JobExpirationTime != nil {
		expiresAt := bedrockResp.JobExpirationTime.Unix()
		result.ExpiresAt = &expiresAt
	}

	return result, nil
}

// BatchCancel stops a batch inference job on AWS Bedrock.
func (provider *BedrockProvider) BatchCancel(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.BatchCancelRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	if key.BedrockKeyConfig == nil {
		return nil, providerUtils.NewConfigurationError("bedrock key config is not provided", providerName)
	}

	if request.BatchID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch_id (job ARN) is required", nil, providerName)
	}

	region := DefaultBedrockRegion
	if key.BedrockKeyConfig.Region != nil {
		region = *key.BedrockKeyConfig.Region
	}

	// URL encode the job ARN
	encodedJobArn := url.PathEscape(request.BatchID)
	reqURL := fmt.Sprintf("https://bedrock.%s.amazonaws.com/model-invocation-job/%s/stop", region, encodedJobArn)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, nil)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error creating request", err, providerName)
	}

	// Sign request
	if err := signAWSRequest(ctx, httpReq, key.BedrockKeyConfig.AccessKey, key.BedrockKeyConfig.SecretKey, key.BedrockKeyConfig.SessionToken, region, "bedrock", providerName); err != nil {
		return nil, err
	}

	// Execute request
	startTime := time.Now()
	resp, err := provider.client.Do(httpReq)
	latency := time.Since(startTime)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   err,
				},
			}
		}
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, providerName)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("error reading response", err, providerName)
	}

	if resp.StatusCode != http.StatusOK {
		var errorResp BedrockError
		if err := sonic.Unmarshal(body, &errorResp); err == nil && errorResp.Message != "" {
			return nil, providerUtils.NewProviderAPIError(errorResp.Message, nil, resp.StatusCode, providerName, nil, nil)
		}
		return nil, providerUtils.NewProviderAPIError(string(body), nil, resp.StatusCode, providerName, nil, nil)
	}

	// After stopping, retrieve the job to get updated status
	retrieveResp, bifrostErr := provider.BatchRetrieve(ctx, key, &schemas.BifrostBatchRetrieveRequest{
		Provider: request.Provider,
		BatchID:  request.BatchID,
	})
	if bifrostErr != nil {
		// Return basic response if retrieve fails
		return &schemas.BifrostBatchCancelResponse{
			ID:     request.BatchID,
			Object: "batch",
			Status: schemas.BatchStatusCancelling,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.BatchCancelRequest,
				Provider:    providerName,
				Latency:     latency.Milliseconds(),
			},
		}, nil
	}

	return &schemas.BifrostBatchCancelResponse{
		ID:     retrieveResp.ID,
		Object: "batch",
		Status: retrieveResp.Status,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchCancelRequest,
			Provider:    providerName,
			Latency:     latency.Milliseconds(),
		},
	}, nil
}

// BatchResults retrieves batch results from AWS Bedrock.
// For Bedrock, results are stored in S3 at the output S3 URI prefix.
// The output includes JSONL files with results (*.jsonl.out) and a manifest file.
func (provider *BedrockProvider) BatchResults(ctx context.Context, key schemas.Key, request *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Bedrock, provider.customProviderConfig, schemas.BatchResultsRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()

	// First, retrieve the batch to get the output S3 URI prefix
	batchResp, bifrostErr := provider.BatchRetrieve(ctx, key, &schemas.BifrostBatchRetrieveRequest{
		Provider: request.Provider,
		BatchID:  request.BatchID,
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if batchResp.OutputFileID == nil || *batchResp.OutputFileID == "" {
		return nil, providerUtils.NewBifrostOperationError("batch results not available: output S3 URI is empty (batch may not be completed)", nil, providerName)
	}

	outputS3URI := *batchResp.OutputFileID
	var allResults []schemas.BatchResultItem
	var totalLatency int64

	// The output S3 URI is a prefix/folder. List files in that folder to find output JSONL files.
	listResp, bifrostErr := provider.FileList(ctx, []schemas.Key{key}, &schemas.BifrostFileListRequest{
		Provider: request.Provider,
		StorageConfig: &schemas.FileStorageConfig{
			S3: &schemas.S3StorageConfig{
				Bucket: outputS3URI, // Use the output URI as the bucket/prefix
			},
		},
		Limit: 100, // Reasonable limit for output files
	})
	if bifrostErr != nil {
		// If listing fails, try direct download (in case outputS3URI is already a file path)
		fileContentResp, directErr := provider.FileContent(ctx, key, &schemas.BifrostFileContentRequest{
			Provider: request.Provider,
			FileID:   outputS3URI,
		})
		if directErr != nil {
			return nil, providerUtils.NewBifrostOperationError(
				fmt.Sprintf("failed to access batch results at %s: listing failed and direct access failed", outputS3URI),
				nil, providerName)
		}

		// Direct download succeeded, parse the content
		results := parseBatchResultsJSONL(fileContentResp.Content, provider)
		return &schemas.BifrostBatchResultsResponse{
			BatchID: request.BatchID,
			Results: results,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.BatchResultsRequest,
				Provider:    providerName,
				Latency:     fileContentResp.ExtraFields.Latency,
			},
		}, nil
	}

	totalLatency += listResp.ExtraFields.Latency

	// Find and download JSONL output files (files ending with .jsonl.out or containing results)
	for _, file := range listResp.Data {
		// Skip manifest files, only process JSONL output files
		if strings.HasSuffix(file.ID, ".jsonl.out") || strings.HasSuffix(file.ID, ".jsonl") {
			fileContentResp, fileErr := provider.FileContent(ctx, key, &schemas.BifrostFileContentRequest{
				Provider: request.Provider,
				FileID:   file.ID,
			})
			if fileErr != nil {
				provider.logger.Warn(fmt.Sprintf("failed to download batch result file %s: %v", file.ID, fileErr))
				continue
			}

			totalLatency += fileContentResp.ExtraFields.Latency
			results := parseBatchResultsJSONL(fileContentResp.Content, provider)
			allResults = append(allResults, results...)
		}
	}

	return &schemas.BifrostBatchResultsResponse{
		BatchID: request.BatchID,
		Results: allResults,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType: schemas.BatchResultsRequest,
			Provider:    providerName,
			Latency:     totalLatency,
		},
	}, nil
}

// parseBatchResultsJSONL parses JSONL content from Bedrock batch output into Bifrost format.
func parseBatchResultsJSONL(content []byte, provider *BedrockProvider) []schemas.BatchResultItem {
	var results []schemas.BatchResultItem
	lines := splitJSONL(content)

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		var bedrockResult BedrockBatchResultRecord
		if err := sonic.Unmarshal(line, &bedrockResult); err != nil {
			provider.logger.Warn(fmt.Sprintf("failed to parse batch result line: %v", err))
			continue
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
	}

	return results
}

// ToBedrockBatchJobResponse converts a Bifrost batch create response to Bedrock format.
func ToBedrockBatchJobResponse(resp *schemas.BifrostBatchCreateResponse) *BedrockBatchJobResponse {
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
