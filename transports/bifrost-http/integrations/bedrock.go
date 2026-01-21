package integrations

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// BedrockRouter handles AWS Bedrock-compatible API endpoints
type BedrockRouter struct {
	*GenericRouter
}

// S3 context keys for storing request parameters

const (
	s3ContextKeyBucket  = schemas.BifrostContextKey("s3_bucket")
	s3ContextKeyPrefix  = schemas.BifrostContextKey("s3_prefix")
	s3ContextKeyMaxKeys = schemas.BifrostContextKey("s3_max_keys")
)

// createBedrockConverseRouteConfig creates a route configuration for the Bedrock Converse API endpoint
// Handles POST /bedrock/model/{modelId}/converse
func createBedrockConverseRouteConfig(pathPrefix string, handlerStore lib.HandlerStore) RouteConfig {
	return RouteConfig{
		Type:   RouteConfigTypeBedrock,
		Path:   pathPrefix + "/model/{modelId}/converse",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &bedrock.BedrockConverseRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if bedrockReq, ok := req.(*bedrock.BedrockConverseRequest); ok {
				bifrostReq, err := bedrockReq.ToBifrostResponsesRequest(ctx)
				if err != nil {
					return nil, fmt.Errorf("failed to convert bedrock request: %w", err)
				}
				return &schemas.BifrostRequest{
					ResponsesRequest: bifrostReq,
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		ResponsesResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesResponse) (interface{}, error) {
			return bedrock.ToBedrockConverseResponse(resp)
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return bedrock.ToBedrockError(err)
		},
		PreCallback: bedrockPreCallback(handlerStore),
	}
}

// createBedrockConverseStreamRouteConfig creates a route configuration for the Bedrock Converse Streaming API endpoint
// Handles POST /bedrock/model/{modelId}/converse-stream
func createBedrockConverseStreamRouteConfig(pathPrefix string, handlerStore lib.HandlerStore) RouteConfig {
	return RouteConfig{
		Type:   RouteConfigTypeBedrock,
		Path:   pathPrefix + "/model/{modelId}/converse-stream",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &bedrock.BedrockConverseRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if bedrockReq, ok := req.(*bedrock.BedrockConverseRequest); ok {
				// Mark as streaming request
				bedrockReq.Stream = true
				bifrostReq, err := bedrockReq.ToBifrostResponsesRequest(ctx)
				if err != nil {
					return nil, fmt.Errorf("failed to convert bedrock request: %w", err)
				}
				return &schemas.BifrostRequest{
					ResponsesRequest: bifrostReq,
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return bedrock.ToBedrockError(err)
		},
		StreamConfig: &StreamConfig{
			ResponsesStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesStreamResponse) (string, interface{}, error) {
				bedrockEvent, err := bedrock.ToBedrockConverseStreamResponse(resp)
				if err != nil {
					return "", nil, err
				}
				// Return empty event type (will use default SSE format) and the event
				// If bedrockEvent is nil, it means we should skip this chunk
				if bedrockEvent == nil {
					return "", nil, nil
				}
				return "", bedrockEvent, nil
			},
		},
		PreCallback: bedrockPreCallback(handlerStore),
	}
}

// createBedrockInvokeWithResponseStreamRouteConfig creates a route configuration for the Bedrock Invoke With Response Stream API endpoint
// Handles POST /bedrock/model/{modelId}/invoke-with-response-stream
func createBedrockInvokeWithResponseStreamRouteConfig(pathPrefix string, handlerStore lib.HandlerStore) RouteConfig {
	return RouteConfig{
		Type:   RouteConfigTypeBedrock,
		Path:   pathPrefix + "/model/{modelId}/invoke-with-response-stream",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &bedrock.BedrockTextCompletionRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if bedrockReq, ok := req.(*bedrock.BedrockTextCompletionRequest); ok {
				// Mark as streaming request
				bedrockReq.Stream = true
				return &schemas.BifrostRequest{
					TextCompletionRequest: bedrockReq.ToBifrostTextCompletionRequest(ctx),
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return bedrock.ToBedrockError(err)
		},
		StreamConfig: &StreamConfig{
			TextStreamResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostTextCompletionResponse) (string, interface{}, error) {
				if resp == nil {
					return "", nil, nil
				}

				// Check if we have raw response (which holds the chunk payload)
				if rawResp, ok := resp.ExtraFields.RawResponse.(string); ok {
					// Create BedrockStreamEvent with InvokeModelRawChunk
					// The payload bytes are the raw JSON string
					bedrockEvent := &bedrock.BedrockStreamEvent{
						InvokeModelRawChunk: []byte(rawResp),
					}
					return "", bedrockEvent, nil
				}
				return "", nil, nil
			},
		},
		PreCallback: bedrockPreCallback(handlerStore),
	}
}

// createBedrockInvokeRouteConfig creates a route configuration for the Bedrock Invoke API endpoint
// Handles POST /bedrock/model/{modelId}/invoke
func createBedrockInvokeRouteConfig(pathPrefix string, handlerStore lib.HandlerStore) RouteConfig {
	return RouteConfig{
		Type:   RouteConfigTypeBedrock,
		Path:   pathPrefix + "/model/{modelId}/invoke",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &bedrock.BedrockTextCompletionRequest{}
		},
		RequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*schemas.BifrostRequest, error) {
			if bedrockReq, ok := req.(*bedrock.BedrockTextCompletionRequest); ok {
				return &schemas.BifrostRequest{
					TextCompletionRequest: bedrockReq.ToBifrostTextCompletionRequest(ctx),
				}, nil
			}
			return nil, errors.New("invalid request type")
		},
		TextResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostTextCompletionResponse) (interface{}, error) {
			return bedrock.ToBedrockTextCompletionResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return bedrock.ToBedrockError(err)
		},
		PreCallback: bedrockPreCallback(handlerStore),
	}
}

// CreateBedrockRouteConfigs creates route configurations for Bedrock endpoints
func CreateBedrockRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	return []RouteConfig{
		createBedrockConverseRouteConfig(pathPrefix, handlerStore),
		createBedrockConverseStreamRouteConfig(pathPrefix, handlerStore),
		createBedrockInvokeWithResponseStreamRouteConfig(pathPrefix, handlerStore),
		createBedrockInvokeRouteConfig(pathPrefix, handlerStore),
	}
}

// createBedrockBatchRouteConfigs creates route configurations for Bedrock Batch inference API endpoints.
func createBedrockBatchRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	var routes []RouteConfig

	// Create batch job endpoint - POST /model-invocation-job
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeBedrock,
		Path:   pathPrefix + "/model-invocation-job",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &bedrock.BedrockBatchJobRequest{}
		},
		BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
			if bedrockReq, ok := req.(*bedrock.BedrockBatchJobRequest); ok {
				provider := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)

				// Convert Bedrock batch request to Bifrost format
				// For Bedrock: use S3 URIs directly
				// For other providers: S3 URIs are not applicable, use file_id from tags
				createReq := &schemas.BifrostBatchCreateRequest{
					Provider: provider,
					Model:    bedrockReq.ModelID,
					Metadata: make(map[string]string),
				}

				// Only set InputFileID from S3 URI for native Bedrock
				// Other providers need file_id passed via tags
				if provider == schemas.Bedrock {
					createReq.InputFileID = bedrockReq.InputDataConfig.S3InputDataConfig.S3Uri
				}

				// Store Bedrock-specific config in metadata
				if bedrockReq.JobName != "" {
					createReq.Metadata["job_name"] = bedrockReq.JobName
				}

				// Use snake_case keys to match what the Bedrock provider expects
				createReq.ExtraParams = map[string]interface{}{
					"role_arn":      bedrockReq.RoleArn,
					"output_s3_uri": bedrockReq.OutputDataConfig.S3OutputDataConfig.S3Uri,
				}

				if bedrockReq.TimeoutDurationInHours > 0 {
					createReq.ExtraParams["timeout_duration_in_hours"] = bedrockReq.TimeoutDurationInHours
				}

				// Extract file_id and endpoint from tags (required for non-Bedrock providers)
				if bedrockReq.Tags != nil {
					for _, tag := range bedrockReq.Tags {
						if tag.Key == "endpoint" {
							createReq.Endpoint = schemas.BatchEndpoint(tag.Value)
							continue
						}
						if tag.Key == "file_id" {
							createReq.InputFileID = tag.Value
							continue
						}
					}
				}

				// Validate requirements for non-Bedrock providers
				if provider == schemas.OpenAI {
					if createReq.InputFileID == "" || createReq.Endpoint == "" {
						return nil, errors.New("file_id and endpoint are required for OpenAI batch API. Specify them in tags as \"endpoint\" and \"file_id\"")
					}
				}

				if provider == schemas.Gemini {
					if createReq.InputFileID == "" {
						return nil, errors.New("file_id is required for Gemini batch API. Specify it in tags as \"file_id\" (use the file ID returned from file upload)")
					}
				}

				return &BatchRequest{
					Type:          schemas.BatchCreateRequest,
					CreateRequest: createReq,
				}, nil
			}
			return nil, errors.New("invalid batch create request type")
		},
		BatchCreateResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchCreateResponse) (interface{}, error) {
			// Only return raw response for native Bedrock calls
			// For cross-provider routing, always convert to Bedrock format
			if resp.ExtraFields.RawResponse != nil && resp.ExtraFields.Provider == schemas.Bedrock {
				return resp.ExtraFields.RawResponse, nil
			}
			return bedrock.ToBedrockBatchJobResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return bedrock.ToBedrockError(err)
		},
		PreCallback: func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
			// Extract provider from header for cross-provider routing
			provider := string(ctx.Request.Header.Peek("x-model-provider"))
			if provider != "" {
				bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.ModelProvider(provider))
			} else {
				bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.Bedrock)
			}
			return bedrockBatchPreCallback(handlerStore)(ctx, bifrostCtx, req)
		},
	})

	// List batch jobs endpoint - GET /model-invocation-jobs
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeBedrock,
		Path:   pathPrefix + "/model-invocation-jobs",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
			return &bedrock.BedrockBatchListRequest{}
		},
		BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
			if bedrockReq, ok := req.(*bedrock.BedrockBatchListRequest); ok {
				provider := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				bifrostReq := bedrock.ToBifrostBatchListRequest(bedrockReq, provider)
				return &BatchRequest{
					Type:        schemas.BatchListRequest,
					ListRequest: bifrostReq,
				}, nil
			}
			return nil, errors.New("invalid batch list request type")
		},
		BatchListResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchListResponse) (interface{}, error) {
			// Only return raw response for native Bedrock calls
			// For cross-provider routing, always convert to Bedrock format
			if resp.ExtraFields.RawResponse != nil && resp.ExtraFields.Provider == schemas.Bedrock {
				return resp.ExtraFields.RawResponse, nil
			}
			return bedrock.ToBedrockBatchJobListResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return bedrock.ToBedrockError(err)
		},
		PreCallback: func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
			// Extract provider from header for cross-provider routing
			provider := string(ctx.Request.Header.Peek("x-model-provider"))
			if provider != "" {
				bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.ModelProvider(provider))
			} else {
				bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.Bedrock)
			}
			return extractBedrockBatchListQueryParams(handlerStore)(ctx, bifrostCtx, req)
		},
	})

	// Get batch job endpoint - GET /model-invocation-job/{job_arn}
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeBedrock,
		Path:   pathPrefix + "/model-invocation-job/{job_arn}",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
			return &bedrock.BedrockBatchRetrieveRequest{}
		},
		BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
			if bedrockReq, ok := req.(*bedrock.BedrockBatchRetrieveRequest); ok {
				provider := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				bifrostReq := bedrock.ToBifrostBatchRetrieveRequest(bedrockReq, provider)
				return &BatchRequest{
					Type:            schemas.BatchRetrieveRequest,
					RetrieveRequest: bifrostReq,
				}, nil
			}
			return nil, errors.New("invalid batch retrieve request type")
		},
		BatchRetrieveResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchRetrieveResponse) (interface{}, error) {
			// Only return raw response for native Bedrock calls
			// For cross-provider routing, always convert to Bedrock format
			if resp.ExtraFields.RawResponse != nil && resp.ExtraFields.Provider == schemas.Bedrock {
				return resp.ExtraFields.RawResponse, nil
			}
			return bedrock.ToBedrockBatchJobRetrieveResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return bedrock.ToBedrockError(err)
		},
		PreCallback: func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
			// Extract provider from header for cross-provider routing
			provider := string(ctx.Request.Header.Peek("x-model-provider"))
			if provider != "" {
				bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.ModelProvider(provider))
			} else {
				bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.Bedrock)
			}
			return extractBedrockJobArnFromPath(handlerStore)(ctx, bifrostCtx, req)
		},
	})

	// Stop batch job endpoint - POST /model-invocation-job/{job_arn}/stop
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeBedrock,
		Path:   pathPrefix + "/model-invocation-job/{job_arn}/stop",
		Method: "POST",
		GetRequestTypeInstance: func() interface{} {
			return &bedrock.BedrockBatchCancelRequest{}
		},
		BatchRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*BatchRequest, error) {
			if bedrockReq, ok := req.(*bedrock.BedrockBatchCancelRequest); ok {
				provider := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				bifrostReq := bedrock.ToBifrostBatchCancelRequest(bedrockReq, provider)
				return &BatchRequest{
					Type:          schemas.BatchCancelRequest,
					CancelRequest: bifrostReq,
				}, nil
			}
			return nil, errors.New("invalid batch cancel request type")
		},
		BatchCancelResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostBatchCancelResponse) (interface{}, error) {
			// Only return raw response for native Bedrock calls
			// For cross-provider routing, always convert to Bedrock format
			if resp.ExtraFields.RawResponse != nil && resp.ExtraFields.Provider == schemas.Bedrock {
				return resp.ExtraFields.RawResponse, nil
			}
			return bedrock.ToBedrockBatchCancelResponse(resp), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return bedrock.ToBedrockError(err)
		},
		PreCallback: func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
			// Extract provider from header for cross-provider routing
			provider := string(ctx.Request.Header.Peek("x-model-provider"))
			if provider != "" {
				bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.ModelProvider(provider))
			} else {
				bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.Bedrock)
			}
			return extractBedrockJobArnFromPath(handlerStore)(ctx, bifrostCtx, req)
		},
	})
	return routes
}

// bedrockBatchPreCallback returns a pre-callback for Bedrock batch create requests
func bedrockBatchPreCallback(handlerStore lib.HandlerStore) func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		// Handle direct key authentication if allowed
		if !handlerStore.ShouldAllowDirectKeys() {
			return nil
		}

		// Check for Bedrock API Key (alternative to AWS Credentials)
		apiKey := string(ctx.Request.Header.Peek("x-bf-bedrock-api-key"))

		// Check for AWS Credentials
		accessKey := string(ctx.Request.Header.Peek("x-bf-bedrock-access-key"))
		secretKey := string(ctx.Request.Header.Peek("x-bf-bedrock-secret-key"))
		region := string(ctx.Request.Header.Peek("x-bf-bedrock-region"))
		sessionToken := string(ctx.Request.Header.Peek("x-bf-bedrock-session-token"))

		if apiKey != "" {
			key := schemas.Key{
				ID:               uuid.New().String(),
				Value:            *schemas.NewEnvVar(apiKey),
				BedrockKeyConfig: &schemas.BedrockKeyConfig{},
			}
			if region != "" {
				key.BedrockKeyConfig.Region = schemas.NewEnvVar(region)
			}
			bifrostCtx.SetValue(schemas.BifrostContextKeyDirectKey, key)
			return nil
		}

		if accessKey != "" && secretKey != "" {
			if region == "" {
				return errors.New("x-bf-bedrock-region header is required when using direct keys")
			}

			key := schemas.Key{
				ID: uuid.New().String(),
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					AccessKey: *schemas.NewEnvVar(accessKey),
					SecretKey: *schemas.NewEnvVar(secretKey),
				},
			}

			key.BedrockKeyConfig.Region = schemas.NewEnvVar(region)

			if sessionToken != "" {
				key.BedrockKeyConfig.SessionToken = schemas.NewEnvVar(sessionToken)
			}

			bifrostCtx.SetValue(schemas.BifrostContextKeyDirectKey, key)
		}

		return nil
	}
}

// extractBedrockBatchListQueryParams extracts query parameters for Bedrock batch list requests
func extractBedrockBatchListQueryParams(handlerStore lib.HandlerStore) PreRequestCallback {
	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		// Handle authentication
		if err := bedrockBatchPreCallback(handlerStore)(ctx, bifrostCtx, req); err != nil {
			return err
		}

		if listReq, ok := req.(*bedrock.BedrockBatchListRequest); ok {
			// Extract maxResults
			if maxResults := string(ctx.QueryArgs().Peek("maxResults")); maxResults != "" {
				if limit, err := strconv.Atoi(maxResults); err == nil {
					listReq.MaxResults = limit
				}
			}

			// Extract nextToken for pagination
			if nextToken := string(ctx.QueryArgs().Peek("nextToken")); nextToken != "" {
				listReq.NextToken = &nextToken
			}

			// Extract status filter
			if status := string(ctx.QueryArgs().Peek("statusEquals")); status != "" {
				listReq.StatusEquals = status
			}

			// Extract name filter
			if name := string(ctx.QueryArgs().Peek("nameContains")); name != "" {
				listReq.NameContains = name
			}
		}

		return nil
	}
}

// parseS3URI parses an S3 URI (s3://bucket/key or bucket-name) and returns bucket name and key.
func parseS3URI(uri string) (bucket, key string) {
	if strings.HasPrefix(uri, "s3://") {
		uri = strings.TrimPrefix(uri, "s3://")
		parts := strings.SplitN(uri, "/", 2)
		bucket = parts[0]
		if len(parts) > 1 {
			key = parts[1]
		}
	} else {
		// Assume it's just a bucket name
		bucket = uri
	}
	return
}

// extractBedrockJobArnFromPath extracts job_arn from path parameters for Bedrock
func extractBedrockJobArnFromPath(handlerStore lib.HandlerStore) PreRequestCallback {
	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		// Handle authentication
		if err := bedrockBatchPreCallback(handlerStore)(ctx, bifrostCtx, req); err != nil {
			return err
		}

		jobArn := ctx.UserValue("job_arn")
		if jobArn == nil {
			return errors.New("job_arn is required")
		}

		jobArnStr, ok := jobArn.(string)
		if !ok || jobArnStr == "" {
			return errors.New("job_arn must be a non-empty string")
		}

		// URL-decode the job ARN (ARNs may be URL encoded)
		decodedJobArn, err := url.PathUnescape(jobArnStr)
		if err != nil {
			decodedJobArn = jobArnStr
		}

		// Now if the provider is not Bedrock, we need to convert the job ARN to the format expected by the provider
		if (*bifrostCtx).Value(bifrostContextKeyProvider).(schemas.ModelProvider) != schemas.Bedrock {
			decodedJobArn = strings.Replace(decodedJobArn, "arn:aws:bedrock:us-east-1:444444444444:batch:", "", 1)
		}

		switch r := req.(type) {
		case *bedrock.BedrockBatchRetrieveRequest:
			r.JobIdentifier = decodedJobArn
		case *bedrock.BedrockBatchCancelRequest:
			r.JobIdentifier = decodedJobArn
		}

		return nil
	}
}

// NewBedrockRouter creates a new BedrockRouter with the given bifrost client
func NewBedrockRouter(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *BedrockRouter {
	routes := CreateBedrockRouteConfigs("/bedrock", handlerStore)
	routes = append(routes, createBedrockBatchRouteConfigs("/bedrock", handlerStore)...)
	routes = append(routes, createBedrockFilesRouteConfigs("/bedrock/files", handlerStore)...)

	return &BedrockRouter{
		GenericRouter: NewGenericRouter(client, handlerStore, routes, logger),
	}
}

// createBedrockFilesRouteConfigs creates S3-compatible routes for Bedrock file operations.
// This allows boto3's S3 client to work directly against Bifrost using endpoint_url.
// Routes: /bedrock/s3/{bucket}/{key...}
func createBedrockFilesRouteConfigs(pathPrefix string, handlerStore lib.HandlerStore) []RouteConfig {
	var routes []RouteConfig

	// PUT /{bucket}/{key} - S3 PutObject
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeBedrock,
		Path:   pathPrefix + "/{bucket}/{key:*}",
		Method: "PUT",
		GetRequestTypeInstance: func() interface{} {
			return &bedrock.BedrockFileUploadRequest{}
		},
		RequestParser: parseS3PutObjectRequest,
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			if uploadReq, ok := req.(*bedrock.BedrockFileUploadRequest); ok {
				provider := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				prefix := ""
				if uploadReq.Key != "" {
					keyComponents := strings.Split(uploadReq.Key, "/")
					prefix = strings.Join(keyComponents[:len(keyComponents)-1], "/")
				}
				return &FileRequest{
					Type: schemas.FileUploadRequest,
					UploadRequest: &schemas.BifrostFileUploadRequest{
						Provider: provider,
						File:     uploadReq.Body,
						Filename: uploadReq.Filename,
						Purpose:  schemas.FilePurpose(uploadReq.Purpose),
						StorageConfig: &schemas.FileStorageConfig{
							S3: &schemas.S3StorageConfig{
								Bucket: uploadReq.Bucket,
								Prefix: prefix,
							},
						},
					},
				}, nil
			}
			return nil, errors.New("invalid file upload request type")
		},
		FileUploadResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileUploadResponse) (interface{}, error) {
			// S3 PutObject returns empty body with ETag header
			return nil, nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return bedrock.ToS3ErrorXML("InternalError", err.Error.Message, "", "")
		},
		PreCallback: func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
			provider := string(ctx.Request.Header.Peek("x-model-provider"))
			if provider != "" {
				bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.ModelProvider(provider))
			} else {
				bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.Bedrock)
			}
			return nil
		},
		PostCallback: s3PutObjectPostCallback,
	})

	// GET /{bucket}/{key} - S3 GetObject
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeBedrock,
		Path:   pathPrefix + "/{bucket}/{key:*}",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
			return &bedrock.BedrockFileContentRequest{}
		},
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			if contentReq, ok := req.(*bedrock.BedrockFileContentRequest); ok {
				provider := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				return &FileRequest{
					Type: schemas.FileContentRequest,
					ContentRequest: &schemas.BifrostFileContentRequest{
						Provider: provider,
						FileID:   contentReq.S3Uri,
					},
				}, nil
			}
			return nil, errors.New("invalid file content request type")
		},
		FileContentResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileContentResponse) (interface{}, error) {
			// Return raw content
			return resp.Content, nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return bedrock.ToS3ErrorXML("NoSuchKey", err.Error.Message, "", "")
		},
		PreCallback:  extractS3BucketKeyFromPath(handlerStore, "content"),
		PostCallback: s3GetObjectPostCallback,
	})

	// HEAD /{bucket}/{key} - S3 HeadObject
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeBedrock,
		Path:   pathPrefix + "/{bucket}/{key:*}",
		Method: "HEAD",
		GetRequestTypeInstance: func() interface{} {
			return &bedrock.BedrockFileRetrieveRequest{}
		},
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			if retrieveReq, ok := req.(*bedrock.BedrockFileRetrieveRequest); ok {
				provider := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				return &FileRequest{
					Type: schemas.FileRetrieveRequest,
					RetrieveRequest: &schemas.BifrostFileRetrieveRequest{
						Provider: provider,
						FileID:   retrieveReq.ETag,
						StorageConfig: &schemas.FileStorageConfig{
							S3: &schemas.S3StorageConfig{
								Bucket: retrieveReq.Bucket,
								Prefix: retrieveReq.Prefix,
							},
						},
					},
				}, nil
			}
			return nil, errors.New("invalid file retrieve request type")
		},
		FileRetrieveResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileRetrieveResponse) (interface{}, error) {
			// HEAD returns empty body, headers set in PostCallback
			return nil, nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return nil // HEAD returns no body on error
		},
		PreCallback: func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
			provider := string(ctx.Request.Header.Peek("x-model-provider"))
			if provider != "" {
				bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.ModelProvider(provider))
			} else {
				bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.Bedrock)
			}
			return extractS3BucketKeyFromPath(handlerStore, "retrieve")(ctx, bifrostCtx, req)
		},
		PostCallback: s3HeadObjectPostCallback,
	})

	// DELETE /{bucket}/{key} - S3 DeleteObject
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeBedrock,
		Path:   pathPrefix + "/{bucket}/{key:*}",
		Method: "DELETE",
		GetRequestTypeInstance: func() interface{} {
			return &bedrock.BedrockFileDeleteRequest{}
		},
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			if deleteReq, ok := req.(*bedrock.BedrockFileDeleteRequest); ok {
				provider := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				return &FileRequest{
					Type: schemas.FileDeleteRequest,
					DeleteRequest: &schemas.BifrostFileDeleteRequest{
						Provider: provider,
						FileID:   deleteReq.ETag,
						StorageConfig: &schemas.FileStorageConfig{
							S3: &schemas.S3StorageConfig{
								Bucket: deleteReq.Bucket,
								Prefix: deleteReq.Prefix,
							},
						},
					},
				}, nil
			}
			return nil, errors.New("invalid file delete request type")
		},
		FileDeleteResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileDeleteResponse) (interface{}, error) {
			// S3 DeleteObject returns empty body
			return nil, nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return bedrock.ToS3ErrorXML("InternalError", err.Error.Message, "", "")
		},
		PreCallback: func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
			provider := string(ctx.Request.Header.Peek("x-model-provider"))
			if provider != "" {
				bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.ModelProvider(provider))
			} else {
				bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.Bedrock)
			}
			return extractS3BucketKeyFromPath(handlerStore, "delete")(ctx, bifrostCtx, req)
		},
		PostCallback: s3DeleteObjectPostCallback,
	})

	// GET /{bucket} - S3 ListObjectsV2
	routes = append(routes, RouteConfig{
		Type:   RouteConfigTypeBedrock,
		Path:   pathPrefix + "/{bucket}",
		Method: "GET",
		GetRequestTypeInstance: func() interface{} {
			return &bedrock.BedrockFileListRequest{}
		},
		FileRequestConverter: func(ctx *schemas.BifrostContext, req interface{}) (*FileRequest, error) {
			if listReq, ok := req.(*bedrock.BedrockFileListRequest); ok {
				provider := ctx.Value(bifrostContextKeyProvider).(schemas.ModelProvider)
				return &FileRequest{
					Type: schemas.FileListRequest,
					ListRequest: &schemas.BifrostFileListRequest{
						Provider: provider,
						StorageConfig: &schemas.FileStorageConfig{
							S3: &schemas.S3StorageConfig{
								Bucket: listReq.Bucket,
								Prefix: listReq.Prefix,
							},
						},
					},
				}, nil
			}
			return nil, errors.New("invalid file list request type")
		},
		FileListResponseConverter: func(ctx *schemas.BifrostContext, resp *schemas.BifrostFileListResponse) (interface{}, error) {
			// Use raw S3 XML response directly if available (passthrough from core provider)
			if resp.ExtraFields.RawResponse != nil {
				if rawBytes, ok := resp.ExtraFields.RawResponse.([]byte); ok {
					return rawBytes, nil
				}
			}
			// Fallback: reconstruct XML from Bifrost response
			bucket := ""
			prefix := ""
			maxKeys := 1000
			if b := ctx.Value(s3ContextKeyBucket); b != nil {
				bucket = b.(string)
			}
			if p := ctx.Value(s3ContextKeyPrefix); p != nil {
				prefix = p.(string)
			}
			if m := ctx.Value(s3ContextKeyMaxKeys); m != nil {
				maxKeys = m.(int)
			}
			return bedrock.ToS3ListObjectsV2XML(resp, bucket, prefix, maxKeys), nil
		},
		ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
			return bedrock.ToS3ErrorXML("InternalError", err.Error.Message, "", "")
		},
		PreCallback: func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
			provider := string(ctx.Request.Header.Peek("x-model-provider"))
			if provider != "" {
				bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.ModelProvider(provider))
			} else {
				bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.Bedrock)
			}
			return extractS3ListObjectsV2Params(handlerStore)(ctx, bifrostCtx, req)
		},
		PostCallback: s3ListObjectsV2PostCallback,
	})

	return routes
}

// ==================== S3 API HELPER FUNCTIONS ====================

// parseS3PutObjectRequest parses raw body for S3 PutObject request
func parseS3PutObjectRequest(ctx *fasthttp.RequestCtx, req interface{}) error {
	// got s3 put object request
	uploadReq, ok := req.(*bedrock.BedrockFileUploadRequest)
	if !ok {
		return errors.New("invalid request type for S3 PutObject")
	}

	// Extract bucket and key from path
	bucket := ctx.UserValue("bucket")
	key := ctx.UserValue("key")

	if bucket == nil || key == nil {
		return errors.New("bucket and key are required")
	}

	bucketStr, ok := bucket.(string)
	if !ok {
		return errors.New("bucket must be a string")
	}
	keyStr, ok := key.(string)
	if !ok {
		return errors.New("key must be a string")
	}

	// Set storage config
	uploadReq.Bucket = bucketStr
	uploadReq.Key = keyStr
	uploadReq.Body = ctx.Request.Body()
	keyComponents := strings.Split(keyStr, "/")
	uploadReq.Filename = keyComponents[len(keyComponents)-1]
	uploadReq.Purpose = "batch"
	return nil
}

// extractS3BucketKeyFromPath extracts bucket and key from path for S3 operations
func extractS3BucketKeyFromPath(handlerStore lib.HandlerStore, opType string) PreRequestCallback {
	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		// Handle authentication first
		if err := bedrockBatchPreCallback(handlerStore)(ctx, bifrostCtx, req); err != nil {
			return err
		}

		bucket := ctx.UserValue("bucket")
		key := ctx.UserValue("key")

		if bucket == nil || key == nil {
			return errors.New("bucket and key are required")
		}

		provider := string(ctx.Request.Header.Peek("x-model-provider"))
		if provider != "" {
			bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.ModelProvider(provider))
		} else {
			bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.Bedrock)
		}

		bucketStr := bucket.(string)
		keyStr := key.(string)
		s3URI := fmt.Sprintf("s3://%s/%s", bucketStr, keyStr)

		fileID := ctx.Request.Header.Peek("If-Match")

		switch opType {
		case "content":
			if contentReq, ok := req.(*bedrock.BedrockFileContentRequest); ok {
				contentReq.Bucket = bucketStr
				contentReq.Prefix = keyStr
				contentReq.S3Uri = s3URI
				contentReq.ETag = string(fileID)
			}
		case "retrieve":
			if retrieveReq, ok := req.(*bedrock.BedrockFileRetrieveRequest); ok {
				retrieveReq.Bucket = bucketStr
				retrieveReq.Prefix = keyStr
				retrieveReq.S3Uri = s3URI
				retrieveReq.ETag = string(fileID)
			}
		case "delete":
			if deleteReq, ok := req.(*bedrock.BedrockFileDeleteRequest); ok {
				deleteReq.Bucket = bucketStr
				deleteReq.Prefix = keyStr
				deleteReq.S3Uri = s3URI
				deleteReq.ETag = string(fileID)
			}
		}
		return nil
	}
}

// extractS3ListObjectsV2Params extracts query params for S3 ListObjectsV2
func extractS3ListObjectsV2Params(handlerStore lib.HandlerStore) PreRequestCallback {
	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		// Handle authentication first
		if err := bedrockBatchPreCallback(handlerStore)(ctx, bifrostCtx, req); err != nil {
			return err
		}

		bucket := ctx.UserValue("bucket")
		if bucket == nil {
			return errors.New("bucket is required")
		}
		bucketStr := bucket.(string)

		// Extract query parameters
		prefix := string(ctx.QueryArgs().Peek("prefix"))
		maxKeysStr := string(ctx.QueryArgs().Peek("max-keys"))
		continuationToken := string(ctx.QueryArgs().Peek("continuation-token"))

		maxKeys := 1000
		if maxKeysStr != "" {
			if mk, err := strconv.Atoi(maxKeysStr); err == nil {
				maxKeys = mk
			}
		}

		// Store in context for response formatting
		bifrostCtx.SetValue(s3ContextKeyBucket, bucketStr)
		bifrostCtx.SetValue(s3ContextKeyPrefix, prefix)
		bifrostCtx.SetValue(s3ContextKeyMaxKeys, maxKeys)

		if listReq, ok := req.(*bedrock.BedrockFileListRequest); ok {
			listReq.MaxKeys = maxKeys
			if continuationToken != "" {
				listReq.ContinuationToken = continuationToken
			}
			listReq.Bucket = bucketStr
			listReq.Prefix = prefix
		}
		return nil
	}
}

// s3PutObjectPostCallback sets response headers for S3 PutObject
func s3PutObjectPostCallback(ctx *fasthttp.RequestCtx, req interface{}, resp interface{}) error {
	ctx.Response.Header.Set("Content-Type", "application/xml")
	ctx.Response.Header.Set("x-amz-request-id", "bifrost")
	if uploadResp, ok := resp.(*schemas.BifrostFileUploadResponse); ok && uploadResp != nil {
		ctx.Response.Header.Set("ETag", fmt.Sprintf("\"%s\"", uploadResp.ID))
	}
	ctx.SetStatusCode(200)
	return nil
}

// s3GetObjectPostCallback sets response headers for S3 GetObject
func s3GetObjectPostCallback(ctx *fasthttp.RequestCtx, req interface{}, resp interface{}) error {
	if contentResp, ok := resp.(*schemas.BifrostFileContentResponse); ok && contentResp != nil {
		ctx.Response.Header.Set("Content-Type", contentResp.ContentType)
		ctx.Response.Header.Set("Content-Length", strconv.Itoa(len(contentResp.Content)))
		ctx.Response.Header.Set("x-amz-request-id", "bifrost")
		if contentResp.FileID != "" {
			ctx.Response.Header.Set("ETag", fmt.Sprintf("\"%s\"", contentResp.FileID))
		}
	}
	return nil
}

// s3HeadObjectPostCallback sets response headers for S3 HeadObject
func s3HeadObjectPostCallback(ctx *fasthttp.RequestCtx, req interface{}, resp interface{}) error {
	if retrieveResp, ok := resp.(*schemas.BifrostFileRetrieveResponse); ok && retrieveResp != nil {
		ctx.Response.Header.Set("Content-Type", "application/octet-stream")
		ctx.Response.Header.Set("Content-Length", strconv.FormatInt(retrieveResp.Bytes, 10))
		ctx.Response.Header.Set("x-amz-request-id", "bifrost")
		ctx.Response.Header.Set("ETag", fmt.Sprintf("\"%s\"", retrieveResp.ID))
	}
	ctx.SetStatusCode(200)
	return nil
}

// s3DeleteObjectPostCallback sets response headers for S3 DeleteObject
func s3DeleteObjectPostCallback(ctx *fasthttp.RequestCtx, req interface{}, resp interface{}) error {
	ctx.Response.Header.Set("x-amz-request-id", "bifrost")
	ctx.SetStatusCode(204)
	return nil
}

// s3ListObjectsV2PostCallback sets response headers for S3 ListObjectsV2
func s3ListObjectsV2PostCallback(ctx *fasthttp.RequestCtx, req interface{}, resp interface{}) error {
	ctx.Response.Header.Set("Content-Type", "application/xml")
	ctx.Response.Header.Set("x-amz-request-id", "bifrost")
	return nil
}

// bedrockPreCallback returns a pre-callback that extracts model ID and handles direct authentication
func bedrockPreCallback(handlerStore lib.HandlerStore) func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
	return func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, req interface{}) error {
		// Extract modelId from path parameter
		modelIDVal := ctx.UserValue("modelId")
		if modelIDVal == nil {
			return errors.New("modelId not found in path")
		}

		modelIDStr, ok := modelIDVal.(string)
		if !ok {
			return fmt.Errorf("modelId must be a string, got %T", modelIDVal)
		}
		if modelIDStr == "" {
			return errors.New("modelId cannot be empty")
		}

		// URL-decode the model ID (handles cases like cohere%2Fcommand-a-03-2025 -> cohere/command-a-03-2025)
		decodedModelID, err := url.PathUnescape(modelIDStr)
		if err != nil {
			// If decoding fails, use the original string
			decodedModelID = modelIDStr
		}

		// Determine model ID - use ParseModelString to check if provider prefix exists
		provider, _ := schemas.ParseModelString(decodedModelID, "")

		var fullModelID string
		if provider == "" {
			// No provider prefix found, add bedrock/ for native Bedrock models
			fullModelID = "bedrock/" + decodedModelID
		} else {
			// Provider prefix already present (e.g., "anthropic/claude-...")
			fullModelID = decodedModelID
		}

		switch r := req.(type) {
		case *bedrock.BedrockConverseRequest:
			r.ModelID = fullModelID
		case *bedrock.BedrockTextCompletionRequest:
			r.ModelID = fullModelID
		default:
			return errors.New("invalid request type for bedrock model extraction")
		}

		// Handle direct key authentication if allowed
		if !handlerStore.ShouldAllowDirectKeys() {
			return nil
		}

		// Check for Bedrock API Key (alternative to AWS Credentials)
		apiKey := string(ctx.Request.Header.Peek("x-bf-bedrock-api-key"))

		// Check for AWS Credentials
		accessKey := string(ctx.Request.Header.Peek("x-bf-bedrock-access-key"))
		secretKey := string(ctx.Request.Header.Peek("x-bf-bedrock-secret-key"))
		region := string(ctx.Request.Header.Peek("x-bf-bedrock-region"))
		sessionToken := string(ctx.Request.Header.Peek("x-bf-bedrock-session-token"))

		if apiKey != "" {
			// Case 1: API Key Authentication
			key := schemas.Key{
				ID:    uuid.New().String(),
				Value: *schemas.NewEnvVar(apiKey),
				// BedrockKeyConfig is required by the provider even if using API Key
				BedrockKeyConfig: &schemas.BedrockKeyConfig{},
			}

			if region != "" {
				key.BedrockKeyConfig.Region = schemas.NewEnvVar(region)
			}
			bifrostCtx.SetValue(schemas.BifrostContextKeyDirectKey, key)
			return nil
		} else if accessKey != "" && secretKey != "" {
			// Case 2: AWS Credentials Authentication
			if region == "" {
				return errors.New("x-bf-bedrock-region header is required when using direct keys")
			}

			key := schemas.Key{
				ID: uuid.New().String(),
				BedrockKeyConfig: &schemas.BedrockKeyConfig{
					AccessKey: *schemas.NewEnvVar(accessKey),
					SecretKey: *schemas.NewEnvVar(secretKey),
				},
			}

			if region != "" {
				key.BedrockKeyConfig.Region = schemas.NewEnvVar(region)
			}

			if sessionToken != "" {
				key.BedrockKeyConfig.SessionToken = schemas.NewEnvVar(sessionToken)
			}

			bifrostCtx.SetValue(schemas.BifrostContextKeyDirectKey, key)
		}

		return nil
	}
}
