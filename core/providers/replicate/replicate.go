// Package providers implements various LLM providers and their utility functions.
// This file contains the replicate provider implementation.
package replicate

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ReplicateProvider implements the Provider interface for Replicate's API.
type ReplicateProvider struct {
	logger               schemas.Logger        // Logger for provider operations
	client               *fasthttp.Client      // HTTP client for API requests
	networkConfig        schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest   bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse  bool                  // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig
}

// NewReplicateProvider creates a new Replicate provider instance.
// It initializes the HTTP client with the provided configuration and sets up response pools.
// The client is configured with timeouts, concurrency limits, and optional proxy settings.
func NewReplicateProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*ReplicateProvider, error) {
	config.CheckAndSetDefaults()

	client := &fasthttp.Client{
		ReadTimeout:         time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		WriteTimeout:        time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds),
		MaxConnsPerHost:     5000,
		MaxIdleConnDuration: 30 * time.Second,
		MaxConnWaitTimeout:  10 * time.Second,
	}

	// Configure proxy and retry policy
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client)
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = replicateAPIBaseURL
	}

	return &ReplicateProvider{
		logger:               logger,
		client:               client,
		networkConfig:        config.NetworkConfig,
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
		customProviderConfig: config.CustomProviderConfig,
	}, nil
}

// GetProviderKey returns the provider identifier for Replicate.
func (provider *ReplicateProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Replicate
}

// buildRequestURL builds the request URL with custom provider config support
func (provider *ReplicateProvider) buildRequestURL(ctx *schemas.BifrostContext, defaultPath string, requestType schemas.RequestType) string {
	path, isCompleteURL := providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)
	if isCompleteURL {
		return path
	}
	return provider.networkConfig.BaseURL + path
}

const (
	replicateAPIBaseURL = "https://api.replicate.com"
	pollingInterval     = 2 * time.Second
)

// createPrediction creates a new prediction on Replicate API
// Supports both sync (with Prefer: wait header) and async modes
// stripPrefer should be true for streaming requests to exclude the Prefer header
func createPrediction(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	jsonBody []byte,
	key schemas.Key,
	url string,
	extraHeaders map[string]string,
	stripPrefer bool,
	logger schemas.Logger,
	sendBackRawRequest bool,
	sendBackRawResponse bool,
) (*ReplicatePredictionResponse, interface{}, time.Duration, map[string]string, *schemas.BifrostError) {
	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set URL
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")

	// Set authorization header
	if value := key.Value.GetValue(); value != "" {
		req.Header.Set("Authorization", "Bearer "+value)
	}

	// Set any extra headers from network config
	// Strip Prefer header for streaming requests to ensure async mode
	headersToUse := extraHeaders
	if stripPrefer {
		headersToUse = stripPreferHeader(extraHeaders)
	}
	providerUtils.SetExtraHeaders(ctx, req, headersToUse, nil)

	req.SetBody(jsonBody)

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, nil, latency, nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusCreated {
		logger.Debug(fmt.Sprintf("error from replicate provider: %s", string(resp.Body())))
		return nil, nil, latency, nil, parseReplicateError(resp.Body(), resp.StatusCode())
	}

	// Extract provider response headers before releasing the response
	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)

	// Parse response
	body, decodeErr := providerUtils.CheckAndDecodeBody(resp)
	if decodeErr != nil {
		return nil, nil, latency, nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decodeErr, schemas.Replicate)
	}

	var prediction ReplicatePredictionResponse
	_, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &prediction, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, sendBackRawResponse))
	if bifrostErr != nil {
		return nil, nil, latency, nil, bifrostErr
	}

	return &prediction, rawResponse, latency, providerResponseHeaders, nil
}

// getPrediction retrieves the current state of a prediction
func getPrediction(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	predictionURL string,
	key schemas.Key,
	logger schemas.Logger,
	sendBackRawResponse bool,
) (*ReplicatePredictionResponse, interface{}, map[string]string, *schemas.BifrostError) {
	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set URL
	req.SetRequestURI(predictionURL)
	req.Header.SetMethod(http.MethodGet)

	// Set authorization header
	if value := key.Value.GetValue(); value != "" {
		req.Header.Set("Authorization", "Bearer "+value)
	}

	// Make request
	_, bifrostErr := providerUtils.MakeRequestWithContext(ctx, client, req, resp)
	if bifrostErr != nil {
		return nil, nil, nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		logger.Debug(fmt.Sprintf("error from replicate provider: %s", string(resp.Body())))
		return nil, nil, nil, parseReplicateError(resp.Body(), resp.StatusCode())
	}

	// Extract provider response headers before releasing the response
	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)

	// Parse response
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, nil, nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, schemas.Replicate)
	}

	prediction := &ReplicatePredictionResponse{}
	_, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, prediction, nil, false, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, nil, nil, bifrostErr
	}

	return prediction, rawResponse, providerResponseHeaders, nil
}

// pollPrediction polls a prediction URL until it reaches a terminal state or timeout
func pollPrediction(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	predictionURL string,
	key schemas.Key,
	timeoutSeconds int,
	logger schemas.Logger,
	sendBackRawResponse bool,
) (*ReplicatePredictionResponse, interface{}, map[string]string, *schemas.BifrostError) {
	// Create context with timeout
	pollCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	ticker := time.NewTicker(pollingInterval)
	defer ticker.Stop()

	// Poll immediately first time
	prediction, rawResponse, providerResponseHeaders, err := getPrediction(ctx, client, predictionURL, key, logger, sendBackRawResponse)
	if err != nil {
		return nil, nil, nil, err
	}

	// If already in terminal state, return immediately
	if isTerminalStatus(prediction.Status) {
		return prediction, rawResponse, providerResponseHeaders, checkForErrorStatus(prediction)
	}

	logger.Debug(fmt.Sprintf("polling replicate prediction %s, status: %s", prediction.ID, prediction.Status))

	// Continue polling until terminal state or timeout
	for {
		select {
		case <-pollCtx.Done():
			return nil, nil, nil, providerUtils.NewBifrostOperationError(
				schemas.ErrProviderRequestTimedOut,
				fmt.Errorf("prediction polling timed out after %d seconds", timeoutSeconds),
				schemas.Replicate,
			)
		case <-ticker.C:
			prediction, rawResponse, providerResponseHeaders, err = getPrediction(ctx, client, predictionURL, key, logger, sendBackRawResponse)
			if err != nil {
				return nil, nil, nil, err
			}

			logger.Debug(fmt.Sprintf("prediction %s status: %s", prediction.ID, prediction.Status))

			if isTerminalStatus(prediction.Status) {
				return prediction, rawResponse, providerResponseHeaders, checkForErrorStatus(prediction)
			}
		}
	}
}

// listDeploymentsByKey performs a list deployments request for a single key.
// Deployments are account-specific, so this needs to be called per key.
func (provider *ReplicateProvider) listDeploymentsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()
	client := provider.client
	extraHeaders := provider.networkConfig.ExtraHeaders

	// Build deployments URL
	deploymentsURL := provider.buildRequestURL(ctx, "/v1/deployments", schemas.ListModelsRequest)

	// Initialize pagination variables
	currentURL := deploymentsURL
	allDeployments := []ReplicateDeployment{}

	// Follow pagination until there are no more pages
	for currentURL != "" {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Set up request
		req.SetRequestURI(currentURL)
		req.Header.SetMethod(http.MethodGet)
		req.Header.SetContentType("application/json")

		// Set authorization header if key is provided
		if key.Value.GetValue() != "" {
			req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
		}

		// Set extra headers from network config
		providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)

		// Make request
		_, bifrostErr := providerUtils.MakeRequestWithContext(ctx, client, req, resp)

		// Release resources
		fasthttp.ReleaseRequest(req)

		if bifrostErr != nil {
			fasthttp.ReleaseResponse(resp)
			return nil, bifrostErr
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			errorResponse := parseReplicateError(resp.Body(), resp.StatusCode())
			fasthttp.ReleaseResponse(resp)
			return nil, errorResponse
		}

		// Make a copy of the response body before releasing
		bodyCopy := make([]byte, len(resp.Body()))
		copy(bodyCopy, resp.Body())

		fasthttp.ReleaseResponse(resp)

		// Parse response from the copy
		var pageResponse ReplicateDeploymentListResponse
		if err := sonic.Unmarshal(bodyCopy, &pageResponse); err != nil {
			return nil, providerUtils.NewBifrostOperationError(
				"failed to parse deployments response",
				err,
				schemas.Replicate,
			)
		}

		// Append results from this page
		allDeployments = append(allDeployments, pageResponse.Results...)

		// Check if there's a next page
		if pageResponse.Next != nil && *pageResponse.Next != "" {
			currentURL = *pageResponse.Next
		} else {
			currentURL = ""
		}
	}

	// Wrap deployments in response structure
	deploymentsResponse := &ReplicateDeploymentListResponse{
		Results: allDeployments,
	}

	// Convert deployments to Bifrost response (no public models here)
	response := ToBifrostListModelsResponse(
		deploymentsResponse,
		providerName,
	)

	return response, nil
}

// ListModels performs a list models request to Replicate's API.
func (provider *ReplicateProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Replicate, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}

	if provider.networkConfig.BaseURL == "" {
		return nil, providerUtils.NewConfigurationError("base_url is not set", provider.GetProviderKey())
	}

	startTime := time.Now()
	providerName := provider.GetProviderKey()

	response, err := providerUtils.HandleMultipleListModelsRequests(
		ctx,
		keys,
		request,
		provider.listDeploymentsByKey,
	)
	if err != nil {
		return nil, err
	}

	// Update metadata with total latency
	latency := time.Since(startTime)
	response.ExtraFields.Provider = providerName
	response.ExtraFields.RequestType = schemas.ListModelsRequest
	response.ExtraFields.Latency = latency.Milliseconds()

	return response, nil
}

// TextCompletion performs a text completion request to the replicate API.
func (provider *ReplicateProvider) TextCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (*schemas.BifrostTextCompletionResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Replicate, provider.customProviderConfig, schemas.TextCompletionRequest); err != nil {
		return nil, err
	}

	deployment, isDeployment := resolveDeploymentModel(request.Model, key)
	if isDeployment {
		request.Model = deployment
	}

	// build replicate request
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) { return ToReplicateTextRequest(request) },
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Check for Prefer: wait header from context for sync mode
	isSync := parsePreferHeader(provider.networkConfig.ExtraHeaders)

	// Build prediction URL based on model type (version ID or model name)
	predictionURL := buildPredictionURL(
		ctx,
		provider.networkConfig.BaseURL,
		request.Model,
		provider.customProviderConfig,
		schemas.TextCompletionRequest,
		isDeployment,
	)

	// create prediction
	prediction, rawResponse, latency, providerResponseHeaders, err := createPrediction(
		ctx,
		provider.client,
		jsonData,
		key,
		providerUtils.GetPathFromContext(ctx, predictionURL),
		provider.networkConfig.ExtraHeaders,
		false,
		provider.logger,
		provider.sendBackRawRequest,
		provider.sendBackRawResponse,
	)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// if not sync, poll until done
	if !isSync && !isTerminalStatus(prediction.Status) {
		prediction, rawResponse, providerResponseHeaders, err = pollPrediction(
			ctx,
			provider.client,
			prediction.URLs.Get,
			key,
			provider.networkConfig.DefaultRequestTimeoutInSeconds,
			provider.logger,
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		)
		if err != nil {
			return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
	}

	// Check for terminal error status (failed/canceled) after sync mode or polling
	if err := checkForErrorStatus(prediction); err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Convert to Bifrost response
	bifrostResponse := prediction.ToBifrostTextCompletionResponse()

	// Set extra fields
	bifrostResponse.ExtraFields.Provider = schemas.Replicate
	bifrostResponse.ExtraFields.RequestType = schemas.TextCompletionRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// TextCompletionStream performs a streaming text completion request to replicate's API.
// It formats the request, sends it to replicate, and processes the response.
// Returns a channel of BifrostStream objects or an error if the request fails.
func (provider *ReplicateProvider) TextCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTextCompletionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Replicate, provider.customProviderConfig, schemas.TextCompletionStreamRequest); err != nil {
		return nil, err
	}

	deployment, isDeployment := resolveDeploymentModel(request.Model, key)
	if isDeployment {
		request.Model = deployment
	}

	// Convert Bifrost request to Replicate format with streaming enabled
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			replicateReq, err := ToReplicateTextRequest(request)
			if err != nil {
				return nil, err
			}
			replicateReq.Stream = schemas.Ptr(true)
			return replicateReq, nil
		},
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Build prediction URL based on model type (version ID or model name)
	predictionURL := buildPredictionURL(
		ctx,
		provider.networkConfig.BaseURL,
		request.Model,
		provider.customProviderConfig,
		schemas.TextCompletionStreamRequest,
		isDeployment,
	)

	// Create prediction
	prediction, _, _, _, err := createPrediction(
		ctx,
		provider.client,
		jsonData,
		key,
		providerUtils.GetPathFromContext(ctx, predictionURL),
		provider.networkConfig.ExtraHeaders,
		true, // Streaming request, strip Prefer header for async mode
		provider.logger,
		provider.sendBackRawRequest,
		provider.sendBackRawResponse,
	)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Verify stream URL is available
	if prediction.URLs == nil || prediction.URLs.Stream == nil || *prediction.URLs.Stream == "" {
		bifrostErr := providerUtils.NewBifrostOperationError(
			"stream URL not available in prediction response",
			fmt.Errorf("prediction response missing stream URL"),
			provider.GetProviderKey(),
		)
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	streamURL := *prediction.URLs.Stream

	// Connect to stream URL
	bodyStream, resp, bifrostErr := listenToReplicateStreamURL(provider.client, streamURL, key)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Store provider response headers in context for transport layer
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.GetProviderKey(), request.Model, schemas.TextCompletionStreamRequest, provider.logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.GetProviderKey(), request.Model, schemas.TextCompletionStreamRequest, provider.logger)
			}
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)

		// Setup cancellation handler
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, bodyStream, provider.logger)
		defer stopCancellation()

		startTime := time.Now()
		lastChunkTime := startTime
		chunkIndex := 0

		// Setup scanner to read SSE stream
		scanner := bufio.NewScanner(bodyStream)
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		var currentEvent ReplicateSSEEvent
		messageID := prediction.ID

		for scanner.Scan() {
			// Check for context cancellation
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := scanner.Text()

			// Skip comment lines
			if strings.HasPrefix(line, ":") {
				continue
			}

			// Empty line indicates end of event
			if line == "" {
				if currentEvent.Event == "" && currentEvent.Data == "" {
					continue
				}

				// Process the complete event
				switch currentEvent.Event {
				case "output":
					// Accumulate content from data field
					if currentEvent.Data != "" {
						// Create a streaming chunk with text completion response
						text := currentEvent.Data
						response := &schemas.BifrostTextCompletionResponse{
							ID:     messageID,
							Model:  request.Model,
							Object: "text_completion",
							Choices: []schemas.BifrostResponseChoice{
								{
									Index: 0,
									TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{
										Text: &text,
									},
								},
							},
							ExtraFields: schemas.BifrostResponseExtraFields{
								RequestType:    schemas.TextCompletionStreamRequest,
								Provider:       provider.GetProviderKey(),
								ModelRequested: request.Model,
								ChunkIndex:     chunkIndex,
								Latency:        time.Since(lastChunkTime).Milliseconds(),
							},
						}

						// Set raw response if enabled (per-chunk event as JSON string)
						if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
							if eventJSON, err := sonic.Marshal(currentEvent); err == nil {
								response.ExtraFields.RawResponse = string(eventJSON)
							}
						}

						lastChunkTime = time.Now()
						chunkIndex++

						providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
							providerUtils.GetBifrostResponseForStreamResponse(response, nil, nil, nil, nil, nil),
							responseChan)
					}

				case "done":
					// Parse done event data
					var doneData ReplicateDoneEvent
					if currentEvent.Data != "" && currentEvent.Data != "{}" {
						if err := sonic.Unmarshal([]byte(currentEvent.Data), &doneData); err != nil {
							provider.logger.Warn(fmt.Sprintf("Failed to parse done event data: %v", err))
						}
					}

					// Check for cancellation or error
					switch doneData.Reason {
					case "canceled":
						bifrostErr := providerUtils.NewBifrostOperationError(
							"prediction was canceled",
							fmt.Errorf("stream ended: prediction canceled"),
							provider.GetProviderKey(),
						)
						bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
							Provider:       provider.GetProviderKey(),
							ModelRequested: request.Model,
							RequestType:    schemas.TextCompletionStreamRequest,
						}
						ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
						enrichedErr := providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
						providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, enrichedErr, responseChan, provider.logger)
						// Explicitly close the body stream to terminate connection to Replicate
						resp.CloseBodyStream()
						return

					case "error":
						errorMsg := "prediction failed"
						if doneData.Output != nil {
							errorMsg = fmt.Sprintf("prediction failed: %v", doneData.Output)
						}
						bifrostErr := providerUtils.NewBifrostOperationError(
							errorMsg,
							fmt.Errorf("stream ended with error"),
							provider.GetProviderKey(),
						)
						bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
							Provider:       provider.GetProviderKey(),
							ModelRequested: request.Model,
							RequestType:    schemas.TextCompletionStreamRequest,
						}
						ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
						enrichedErr := providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
						providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, enrichedErr, responseChan, provider.logger)
						// Explicitly close the body stream to terminate connection to Replicate
						resp.CloseBodyStream()
						return
					}

					// Send final chunk with finish reason
					finishReason := schemas.Ptr("stop")
					finalResponse := providerUtils.CreateBifrostTextCompletionChunkResponse(
						messageID,
						nil, // usage - not available in done event
						finishReason,
						chunkIndex,
						schemas.TextCompletionStreamRequest,
						provider.GetProviderKey(),
						request.Model,
					)

					// Set raw request if enabled
					if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
						providerUtils.ParseAndSetRawRequest(&finalResponse.ExtraFields, jsonData)
					}

					finalResponse.ExtraFields.Latency = time.Since(startTime).Milliseconds()

					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
						providerUtils.GetBifrostResponseForStreamResponse(finalResponse, nil, nil, nil, nil, nil),
						responseChan)
					resp.CloseBodyStream()
					return
				}

				// Reset event for next one
				currentEvent = ReplicateSSEEvent{}
				continue
			}

			// Parse SSE fields
			if after, ok := strings.CutPrefix(line, "event: "); ok {
				currentEvent.Event = strings.TrimSpace(after)
			} else if after, ok := strings.CutPrefix(line, "data: "); ok {
				// For multiline data, append with newline
				if currentEvent.Data != "" {
					currentEvent.Data += "\n"
				}
				currentEvent.Data += after
			} else if after, ok := strings.CutPrefix(line, "id: "); ok {
				currentEvent.ID = strings.TrimSpace(after)
			}
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			provider.logger.Warn("Error reading stream: %v", err)
			enrichedErr := providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, provider.GetProviderKey()), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, enrichedErr, responseChan, provider.logger)
			return
		}
	}()

	return responseChan, nil
}

// ChatCompletion performs a chat completion request to the replicate API.
func (provider *ReplicateProvider) ChatCompletion(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Replicate, provider.customProviderConfig, schemas.ChatCompletionRequest); err != nil {
		return nil, err
	}

	deployment, isDeployment := resolveDeploymentModel(request.Model, key)
	if isDeployment {
		request.Model = deployment
	}

	// build replicate request
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) { return ToReplicateChatRequest(request) },
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Check for Prefer: wait header from context for sync mode
	isSync := parsePreferHeader(provider.networkConfig.ExtraHeaders)

	// Build prediction URL based on model type (version ID or model name)
	predictionURL := buildPredictionURL(
		ctx,
		provider.networkConfig.BaseURL,
		request.Model,
		provider.customProviderConfig,
		schemas.ChatCompletionRequest,
		isDeployment,
	)

	// create prediction
	prediction, rawResponse, latency, providerResponseHeaders, err := createPrediction(
		ctx,
		provider.client,
		jsonData,
		key,
		providerUtils.GetPathFromContext(ctx, predictionURL),
		provider.networkConfig.ExtraHeaders,
		false,
		provider.logger,
		provider.sendBackRawRequest,
		provider.sendBackRawResponse,
	)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// if not sync, poll until done
	if !isSync && !isTerminalStatus(prediction.Status) {
		prediction, rawResponse, providerResponseHeaders, err = pollPrediction(
			ctx,
			provider.client,
			prediction.URLs.Get,
			key,
			provider.networkConfig.DefaultRequestTimeoutInSeconds,
			provider.logger,
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		)
		if err != nil {
			return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
	}

	// Check for terminal error status (failed/canceled) after sync mode or polling
	if err := checkForErrorStatus(prediction); err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Convert to Bifrost response
	bifrostResponse := prediction.ToBifrostChatResponse()

	// Set extra fields
	bifrostResponse.ExtraFields.Provider = schemas.Replicate
	bifrostResponse.ExtraFields.RequestType = schemas.ChatCompletionRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// ChatCompletionStream performs a streaming chat completion request to the replicate API.
// It supports real-time streaming of responses using Server-Sent Events (SSE).
// Returns a channel containing BifrostResponse objects representing the stream or an error if the request fails.
func (provider *ReplicateProvider) ChatCompletionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostChatRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Replicate, provider.customProviderConfig, schemas.ChatCompletionStreamRequest); err != nil {
		return nil, err
	}

	deployment, isDeployment := resolveDeploymentModel(request.Model, key)
	if isDeployment {
		request.Model = deployment
	}

	// Convert Bifrost request to Replicate format with streaming enabled
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			replicateReq, err := ToReplicateChatRequest(request)
			if err != nil {
				return nil, err
			}
			replicateReq.Stream = schemas.Ptr(true)
			return replicateReq, nil
		},
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Build prediction URL based on model type (version ID or model name)
	predictionURL := buildPredictionURL(
		ctx,
		provider.networkConfig.BaseURL,
		request.Model,
		provider.customProviderConfig,
		schemas.ChatCompletionStreamRequest,
		isDeployment,
	)

	// Create prediction
	prediction, _, _, _, err := createPrediction(
		ctx,
		provider.client,
		jsonData,
		key,
		providerUtils.GetPathFromContext(ctx, predictionURL),
		provider.networkConfig.ExtraHeaders,
		true,
		provider.logger,
		provider.sendBackRawRequest,
		provider.sendBackRawResponse,
	)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Verify stream URL is available
	if prediction.URLs == nil || prediction.URLs.Stream == nil || *prediction.URLs.Stream == "" {
		bifrostErr := providerUtils.NewBifrostOperationError(
			"stream URL not available in prediction response",
			fmt.Errorf("prediction response missing stream URL"),
			provider.GetProviderKey(),
		)
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	streamURL := *prediction.URLs.Stream

	// Connect to stream URL
	bodyStream, resp, bifrostErr := listenToReplicateStreamURL(provider.client, streamURL, key)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Store provider response headers in context for transport layer
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.GetProviderKey(), request.Model, schemas.ChatCompletionStreamRequest, provider.logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.GetProviderKey(), request.Model, schemas.ChatCompletionStreamRequest, provider.logger)
			}
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)

		// Setup cancellation handler
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, bodyStream, provider.logger)
		defer stopCancellation()

		startTime := time.Now()
		lastChunkTime := startTime
		chunkIndex := 0

		// Setup scanner to read SSE stream
		scanner := bufio.NewScanner(bodyStream)
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		var currentEvent ReplicateSSEEvent
		messageID := prediction.ID

		for scanner.Scan() {
			// Check for context cancellation
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := scanner.Text()

			// Skip comment lines
			if strings.HasPrefix(line, ":") {
				continue
			}

			// Empty line indicates end of event
			if line == "" {
				if currentEvent.Event == "" && currentEvent.Data == "" {
					continue
				}

				// Process the complete event
				switch currentEvent.Event {
				case "output":
					// Accumulate content from data field
					if currentEvent.Data != "" {
						// Create a streaming chunk
						content := currentEvent.Data
						role := string(schemas.ChatMessageRoleAssistant)
						delta := &schemas.ChatStreamResponseChoiceDelta{
							Content: &content,
							Role:    &role,
						}

						response := &schemas.BifrostChatResponse{
							ID:      messageID,
							Model:   request.Model,
							Object:  "chat.completion.chunk",
							Created: int(time.Now().Unix()),
							Choices: []schemas.BifrostResponseChoice{
								{
									Index: 0,
									ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
										Delta: delta,
									},
								},
							},
							ExtraFields: schemas.BifrostResponseExtraFields{
								RequestType:    schemas.ChatCompletionStreamRequest,
								Provider:       provider.GetProviderKey(),
								ModelRequested: request.Model,
								ChunkIndex:     chunkIndex,
								Latency:        time.Since(lastChunkTime).Milliseconds(),
							},
						}

						// Set raw response if enabled (per-chunk event as JSON string)
						if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
							if eventJSON, err := sonic.Marshal(currentEvent); err == nil {
								response.ExtraFields.RawResponse = string(eventJSON)
							}
						}

						lastChunkTime = time.Now()
						chunkIndex++

						providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
							providerUtils.GetBifrostResponseForStreamResponse(nil, response, nil, nil, nil, nil),
							responseChan)
					}

				case "done":
					// Parse done event data
					var doneData ReplicateDoneEvent
					if currentEvent.Data != "" && currentEvent.Data != "{}" {
						if err := sonic.Unmarshal([]byte(currentEvent.Data), &doneData); err != nil {
							provider.logger.Warn(fmt.Sprintf("Failed to parse done event data: %v", err))
						}
					}

					// Check for cancellation or error
					switch doneData.Reason {
					case "canceled":
						bifrostErr := providerUtils.NewBifrostOperationError(
							"prediction was canceled",
							fmt.Errorf("stream ended: prediction canceled"),
							provider.GetProviderKey(),
						)
						bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
							Provider:       provider.GetProviderKey(),
							ModelRequested: request.Model,
							RequestType:    schemas.ChatCompletionStreamRequest,
						}
						ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
						enrichedErr := providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
						providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, enrichedErr, responseChan, provider.logger)
						// Explicitly close the body stream to terminate connection to Replicate
						resp.CloseBodyStream()
						return

					case "error":
						errorMsg := "prediction failed"
						if doneData.Output != nil {
							errorMsg = fmt.Sprintf("prediction failed: %v", doneData.Output)
						}
						bifrostErr := providerUtils.NewBifrostOperationError(
							errorMsg,
							fmt.Errorf("stream ended with error"),
							provider.GetProviderKey(),
						)
						bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
							Provider:       provider.GetProviderKey(),
							ModelRequested: request.Model,
							RequestType:    schemas.ChatCompletionStreamRequest,
						}
						ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
						enrichedErr := providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
						providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, enrichedErr, responseChan, provider.logger)
						// Explicitly close the body stream to terminate connection to Replicate
						resp.CloseBodyStream()
						return
					}

					// Send final chunk with finish reason
					finishReason := "stop"
					finalResponse := &schemas.BifrostChatResponse{
						ID:      messageID,
						Model:   request.Model,
						Object:  "chat.completion.chunk",
						Created: int(time.Now().Unix()),
						Choices: []schemas.BifrostResponseChoice{
							{
								Index:        0,
								FinishReason: &finishReason,
								ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
									Delta: &schemas.ChatStreamResponseChoiceDelta{},
								},
							},
						},
						ExtraFields: schemas.BifrostResponseExtraFields{
							RequestType:    schemas.ChatCompletionStreamRequest,
							Provider:       provider.GetProviderKey(),
							ModelRequested: request.Model,
							ChunkIndex:     chunkIndex,
							Latency:        time.Since(startTime).Milliseconds(),
						},
					}

					// Set raw request if enabled
					if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
						providerUtils.ParseAndSetRawRequest(&finalResponse.ExtraFields, jsonData)
					}

					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
						providerUtils.GetBifrostResponseForStreamResponse(nil, finalResponse, nil, nil, nil, nil),
						responseChan)
					resp.CloseBodyStream()
					return
				}

				// Reset event for next one
				currentEvent = ReplicateSSEEvent{}
				continue
			}

			// Parse SSE fields
			if after, ok := strings.CutPrefix(line, "event: "); ok {
				currentEvent.Event = strings.TrimSpace(after)
			} else if after, ok := strings.CutPrefix(line, "data: "); ok {
				// For multiline data, append with newline
				if currentEvent.Data != "" {
					currentEvent.Data += "\n"
				}
				currentEvent.Data += after
			} else if after, ok := strings.CutPrefix(line, "id: "); ok {
				currentEvent.ID = strings.TrimSpace(after)
			}
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			// If context was cancelled/timed out, let defer handle it
			if ctx.Err() != nil {
				return
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			provider.logger.Warn("Error reading stream: %v", err)
			enrichedErr := providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, provider.GetProviderKey()), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, enrichedErr, responseChan, provider.logger)
			return
		}
	}()

	return responseChan, nil
}

// Responses performs a responses request to the replicate API.
func (provider *ReplicateProvider) Responses(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Replicate, provider.customProviderConfig, schemas.ResponsesRequest); err != nil {
		return nil, err
	}

	deployment, isDeployment := resolveDeploymentModel(request.Model, key)
	if isDeployment {
		request.Model = deployment
	}

	// build replicate request
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) { return ToReplicateResponsesRequest(request) },
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Check for Prefer: wait header from context for sync mode
	isSync := parsePreferHeader(provider.networkConfig.ExtraHeaders)

	// Build prediction URL based on model type (version ID or model name)
	predictionURL := buildPredictionURL(
		ctx,
		provider.networkConfig.BaseURL,
		request.Model,
		provider.customProviderConfig,
		schemas.ResponsesRequest,
		isDeployment,
	)

	// create prediction
	prediction, rawResponse, latency, providerResponseHeaders, err := createPrediction(
		ctx,
		provider.client,
		jsonData,
		key,
		providerUtils.GetPathFromContext(ctx, predictionURL),
		provider.networkConfig.ExtraHeaders,
		false,
		provider.logger,
		provider.sendBackRawRequest,
		provider.sendBackRawResponse,
	)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// if not sync, poll until done
	if !isSync && !isTerminalStatus(prediction.Status) {
		prediction, rawResponse, providerResponseHeaders, err = pollPrediction(
			ctx,
			provider.client,
			prediction.URLs.Get,
			key,
			provider.networkConfig.DefaultRequestTimeoutInSeconds,
			provider.logger,
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		)
		if err != nil {
			return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
	}

	// Check for terminal error status (failed/canceled) after sync mode or polling
	if err := checkForErrorStatus(prediction); err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Convert to Bifrost response
	response := prediction.ToBifrostResponsesResponse()
	response.ExtraFields.RequestType = schemas.ResponsesRequest
	response.ExtraFields.Provider = provider.GetProviderKey()
	response.ExtraFields.ModelRequested = request.Model
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&response.ExtraFields, jsonData)
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		response.ExtraFields.RawResponse = rawResponse
	}
	return response, nil
}

// ResponsesStream performs a streaming responses request to the replicate API.
func (provider *ReplicateProvider) ResponsesStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostResponsesRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Replicate, provider.customProviderConfig, schemas.ResponsesStreamRequest); err != nil {
		return nil, err
	}

	deployment, isDeployment := resolveDeploymentModel(request.Model, key)
	if isDeployment {
		request.Model = deployment
	}

	// Build replicate request
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) { return ToReplicateResponsesRequest(request) },
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Enable streaming
	var replicateReq ReplicatePredictionRequest
	if err := sonic.Unmarshal(jsonData, &replicateReq); err == nil {
		replicateReq.Stream = schemas.Ptr(true)
		var streamErr error
		jsonData, streamErr = sonic.Marshal(replicateReq)
		if streamErr != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to marshal request", streamErr, provider.GetProviderKey())
		}
	}

	// Build prediction URL
	predictionURL := buildPredictionURL(
		ctx,
		provider.networkConfig.BaseURL,
		request.Model,
		provider.customProviderConfig,
		schemas.ResponsesStreamRequest,
		isDeployment,
	)

	// Create prediction
	prediction, _, _, _, err := createPrediction(
		ctx,
		provider.client,
		jsonData,
		key,
		providerUtils.GetPathFromContext(ctx, predictionURL),
		provider.networkConfig.ExtraHeaders,
		true, // Streaming request, strip Prefer header for async mode
		provider.logger,
		provider.sendBackRawRequest,
		provider.sendBackRawResponse,
	)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Verify stream URL is available
	if prediction.URLs == nil || prediction.URLs.Stream == nil || *prediction.URLs.Stream == "" {
		bifrostErr := providerUtils.NewBifrostOperationError(
			"stream URL not available in prediction response",
			fmt.Errorf("prediction response missing stream URL"),
			provider.GetProviderKey(),
		)
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	streamURL := *prediction.URLs.Stream

	// Setup request for streaming
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	resp.StreamBody = true
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(http.MethodGet)
	req.SetRequestURI(streamURL)
	req.Header.Set("Accept", "text/event-stream")

	// Set authorization
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	// Set extra headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// Make the streaming request
	streamErr := provider.client.Do(req, resp)
	if streamErr != nil {
		defer providerUtils.ReleaseStreamingResponse(resp)
		if errors.Is(streamErr, context.Canceled) {
			return nil, providerUtils.EnrichError(ctx, &schemas.BifrostError{
				IsBifrostError: false,
				Error: &schemas.ErrorField{
					Type:    schemas.Ptr(schemas.RequestCancelled),
					Message: schemas.ErrRequestCancelled,
					Error:   streamErr,
				},
			}, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
		if errors.Is(streamErr, fasthttp.ErrTimeout) || errors.Is(streamErr, context.DeadlineExceeded) {
			return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderRequestTimedOut, streamErr, provider.GetProviderKey()), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, streamErr, provider.GetProviderKey()), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Check for HTTP errors
	if resp.StatusCode() != fasthttp.StatusOK {
		defer providerUtils.ReleaseStreamingResponse(resp)
		body := resp.Body()
		return nil, providerUtils.EnrichError(ctx, parseReplicateError(body, resp.StatusCode()), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Store provider response headers in context for transport layer
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, provider.GetProviderKey(), request.Model, schemas.ResponsesStreamRequest, provider.logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, provider.GetProviderKey(), request.Model, schemas.ResponsesStreamRequest, provider.logger)
			}
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)

		// Setup cancellation handler
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, resp.BodyStream(), provider.logger)
		defer stopCancellation()

		if resp.BodyStream() == nil {
			bifrostErr := providerUtils.NewBifrostOperationError(
				"Provider returned an empty response",
				fmt.Errorf("provider returned an empty response"),
				provider.GetProviderKey(),
			)
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse), responseChan, provider.logger)
			return
		}

		scanner := bufio.NewScanner(resp.BodyStream())
		startTime := time.Now()
		sequenceNumber := 0
		messageID := prediction.ID
		// Generate a unique item ID for the message (needed for accumulator to track deltas)
		itemID := "msg_" + messageID

		// Track lifecycle state
		var hasEmittedCreated, hasEmittedInProgress bool
		var hasEmittedOutputItemAdded, hasEmittedContentPartAdded bool
		var hasReceivedContent bool
		outputIndex := 0
		contentIndex := 0

		// Accumulate raw responses for debugging
		var rawResponseChunks []interface{}
		sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
		sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

		// Parse SSE events
		currentEvent := ReplicateSSEEvent{}

		for scanner.Scan() {
			if ctx.Err() != nil {
				return
			}

			line := scanner.Text()

			// Empty line indicates end of event
			if line == "" {
				if currentEvent.Event != "" {
					// Process the event
					switch currentEvent.Event {
					case "output":
						// Text chunk received
						if currentEvent.Data != "" {
							// Accumulate raw response if enabled
							if sendBackRawResponse {
								rawResponseChunks = append(rawResponseChunks, currentEvent)
							}

							// Emit lifecycle events on first content
							if !hasEmittedCreated {
								// response.created
								createdResp := &schemas.BifrostResponsesStreamResponse{
									Type:           schemas.ResponsesStreamResponseTypeCreated,
									SequenceNumber: sequenceNumber,
									Response: &schemas.BifrostResponsesResponse{
										ID:        schemas.Ptr(messageID),
										Model:     request.Model,
										CreatedAt: int(startTime.Unix()),
									},
									ExtraFields: schemas.BifrostResponseExtraFields{
										RequestType:    schemas.ResponsesStreamRequest,
										Provider:       provider.GetProviderKey(),
										ModelRequested: request.Model,
										Latency:        time.Since(startTime).Milliseconds(),
										ChunkIndex:     sequenceNumber,
									},
								}
								if sendBackRawRequest {
									providerUtils.ParseAndSetRawRequest(&createdResp.ExtraFields, jsonData)
								}
								providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
									providerUtils.GetBifrostResponseForStreamResponse(nil, nil, createdResp, nil, nil, nil),
									responseChan)
								sequenceNumber++
								hasEmittedCreated = true
							}

							if !hasEmittedInProgress {
								// response.in_progress
								inProgressResp := &schemas.BifrostResponsesStreamResponse{
									Type:           schemas.ResponsesStreamResponseTypeInProgress,
									SequenceNumber: sequenceNumber,
									Response: &schemas.BifrostResponsesResponse{
										ID:        schemas.Ptr(messageID),
										CreatedAt: int(startTime.Unix()),
									},
									ExtraFields: schemas.BifrostResponseExtraFields{
										RequestType:    schemas.ResponsesStreamRequest,
										Provider:       provider.GetProviderKey(),
										ModelRequested: request.Model,
										ChunkIndex:     sequenceNumber,
									},
								}
								providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
									providerUtils.GetBifrostResponseForStreamResponse(nil, nil, inProgressResp, nil, nil, nil),
									responseChan)
								sequenceNumber++
								hasEmittedInProgress = true
							}

							if !hasEmittedOutputItemAdded {
								// response.output_item.added
								messageType := schemas.ResponsesMessageTypeMessage
								role := schemas.ResponsesInputMessageRoleAssistant
								status := "in_progress"
								itemAddedResp := &schemas.BifrostResponsesStreamResponse{
									Type:           schemas.ResponsesStreamResponseTypeOutputItemAdded,
									SequenceNumber: sequenceNumber,
									OutputIndex:    schemas.Ptr(outputIndex),
									Item: &schemas.ResponsesMessage{
										ID:     schemas.Ptr(itemID),
										Type:   &messageType,
										Role:   &role,
										Status: &status,
										Content: &schemas.ResponsesMessageContent{
											ContentBlocks: []schemas.ResponsesMessageContentBlock{},
										},
									},
									ExtraFields: schemas.BifrostResponseExtraFields{
										RequestType:    schemas.ResponsesStreamRequest,
										Provider:       provider.GetProviderKey(),
										ModelRequested: request.Model,
										ChunkIndex:     sequenceNumber,
									},
								}
								providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
									providerUtils.GetBifrostResponseForStreamResponse(nil, nil, itemAddedResp, nil, nil, nil),
									responseChan)
								sequenceNumber++
								hasEmittedOutputItemAdded = true
							}

							if !hasEmittedContentPartAdded {
								// response.content_part.added
								emptyText := ""
								partAddedResp := &schemas.BifrostResponsesStreamResponse{
									Type:           schemas.ResponsesStreamResponseTypeContentPartAdded,
									SequenceNumber: sequenceNumber,
									OutputIndex:    schemas.Ptr(outputIndex),
									ContentIndex:   schemas.Ptr(contentIndex),
									ItemID:         schemas.Ptr(itemID),
									Part: &schemas.ResponsesMessageContentBlock{
										Type: schemas.ResponsesOutputMessageContentTypeText,
										Text: &emptyText,
										ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
											Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
											LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
										},
									},
									ExtraFields: schemas.BifrostResponseExtraFields{
										RequestType:    schemas.ResponsesStreamRequest,
										Provider:       provider.GetProviderKey(),
										ModelRequested: request.Model,
										ChunkIndex:     sequenceNumber,
									},
								}
								providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
									providerUtils.GetBifrostResponseForStreamResponse(nil, nil, partAddedResp, nil, nil, nil),
									responseChan)
								sequenceNumber++
								hasEmittedContentPartAdded = true
							}

							// response.output_text.delta
							deltaResp := &schemas.BifrostResponsesStreamResponse{
								Type:           schemas.ResponsesStreamResponseTypeOutputTextDelta,
								SequenceNumber: sequenceNumber,
								OutputIndex:    schemas.Ptr(outputIndex),
								ContentIndex:   schemas.Ptr(contentIndex),
								ItemID:         schemas.Ptr(itemID),
								Delta:          schemas.Ptr(currentEvent.Data),
								LogProbs:       []schemas.ResponsesOutputMessageContentTextLogProb{},
								ExtraFields: schemas.BifrostResponseExtraFields{
									RequestType:    schemas.ResponsesStreamRequest,
									Provider:       provider.GetProviderKey(),
									ModelRequested: request.Model,
									ChunkIndex:     sequenceNumber,
								},
							}
							providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
								providerUtils.GetBifrostResponseForStreamResponse(nil, nil, deltaResp, nil, nil, nil),
								responseChan)
							sequenceNumber++
							hasReceivedContent = true
						}
					case "done":
						// Accumulate done event in raw responses if enabled
						if sendBackRawResponse {
							rawResponseChunks = append(rawResponseChunks, currentEvent)
						}

						// Stream completed
						if hasReceivedContent {
							// response.output_text.done
							textDoneResp := &schemas.BifrostResponsesStreamResponse{
								Type:           schemas.ResponsesStreamResponseTypeOutputTextDone,
								SequenceNumber: sequenceNumber,
								OutputIndex:    schemas.Ptr(outputIndex),
								ContentIndex:   schemas.Ptr(contentIndex),
								ItemID:         schemas.Ptr(itemID),
								LogProbs:       []schemas.ResponsesOutputMessageContentTextLogProb{},
								ExtraFields: schemas.BifrostResponseExtraFields{
									RequestType:    schemas.ResponsesStreamRequest,
									Provider:       provider.GetProviderKey(),
									ModelRequested: request.Model,
									ChunkIndex:     sequenceNumber,
								},
							}
							providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
								providerUtils.GetBifrostResponseForStreamResponse(nil, nil, textDoneResp, nil, nil, nil),
								responseChan)
							sequenceNumber++

							// response.content_part.done
							partDoneResp := &schemas.BifrostResponsesStreamResponse{
								Type:           schemas.ResponsesStreamResponseTypeContentPartDone,
								SequenceNumber: sequenceNumber,
								OutputIndex:    schemas.Ptr(outputIndex),
								ContentIndex:   schemas.Ptr(contentIndex),
								ItemID:         schemas.Ptr(itemID),
								Part: &schemas.ResponsesMessageContentBlock{
									Type: schemas.ResponsesOutputMessageContentTypeText,
									ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
										Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
										LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
									},
								},
								ExtraFields: schemas.BifrostResponseExtraFields{
									RequestType:    schemas.ResponsesStreamRequest,
									Provider:       provider.GetProviderKey(),
									ModelRequested: request.Model,
									ChunkIndex:     sequenceNumber,
								},
							}
							providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
								providerUtils.GetBifrostResponseForStreamResponse(nil, nil, partDoneResp, nil, nil, nil),
								responseChan)
							sequenceNumber++

							// response.output_item.done
							messageType := schemas.ResponsesMessageTypeMessage
							role := schemas.ResponsesInputMessageRoleAssistant
							status := "completed"
							itemDoneResp := &schemas.BifrostResponsesStreamResponse{
								Type:           schemas.ResponsesStreamResponseTypeOutputItemDone,
								SequenceNumber: sequenceNumber,
								OutputIndex:    schemas.Ptr(outputIndex),
								Item: &schemas.ResponsesMessage{
									ID:     schemas.Ptr(itemID),
									Type:   &messageType,
									Role:   &role,
									Status: &status,
									Content: &schemas.ResponsesMessageContent{
										ContentBlocks: []schemas.ResponsesMessageContentBlock{
											{
												Type: schemas.ResponsesOutputMessageContentTypeText,
												ResponsesOutputMessageContentText: &schemas.ResponsesOutputMessageContentText{
													Annotations: []schemas.ResponsesOutputMessageContentTextAnnotation{},
													LogProbs:    []schemas.ResponsesOutputMessageContentTextLogProb{},
												},
											},
										},
									},
								},
								ExtraFields: schemas.BifrostResponseExtraFields{
									RequestType:    schemas.ResponsesStreamRequest,
									Provider:       provider.GetProviderKey(),
									ModelRequested: request.Model,
									ChunkIndex:     sequenceNumber,
								},
							}
							providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
								providerUtils.GetBifrostResponseForStreamResponse(nil, nil, itemDoneResp, nil, nil, nil),
								responseChan)
							sequenceNumber++
						}

						// response.completed
						completedResp := &schemas.BifrostResponsesStreamResponse{
							Type:           schemas.ResponsesStreamResponseTypeCompleted,
							SequenceNumber: sequenceNumber,
							Response: &schemas.BifrostResponsesResponse{
								ID:          schemas.Ptr(messageID),
								Model:       request.Model,
								CreatedAt:   int(startTime.Unix()),
								CompletedAt: schemas.Ptr(int(time.Now().Unix())),
							},
							ExtraFields: schemas.BifrostResponseExtraFields{
								RequestType:    schemas.ResponsesStreamRequest,
								Provider:       provider.GetProviderKey(),
								ModelRequested: request.Model,
								Latency:        time.Since(startTime).Milliseconds(),
								ChunkIndex:     sequenceNumber,
							},
						}

						// Set raw request if enabled (on final chunk only)
						if sendBackRawRequest {
							providerUtils.ParseAndSetRawRequest(&completedResp.ExtraFields, jsonData)
						}

						// Set raw response if enabled
						if sendBackRawResponse && len(rawResponseChunks) > 0 {
							completedResp.ExtraFields.RawResponse = rawResponseChunks
						}

						ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
						providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
							providerUtils.GetBifrostResponseForStreamResponse(nil, nil, completedResp, nil, nil, nil),
							responseChan)
						resp.CloseBodyStream()
						return
					case "error":
						// Accumulate error event in raw responses if enabled
						if sendBackRawResponse {
							rawResponseChunks = append(rawResponseChunks, currentEvent)
						}

						// Handle error
						errorMsg := "stream error"
						if currentEvent.Data != "" {
							errorMsg = currentEvent.Data
						}
						bifrostErr := providerUtils.NewBifrostOperationError(
							errorMsg,
							fmt.Errorf("stream error: %s", errorMsg),
							provider.GetProviderKey(),
						)
						bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
							Provider:       provider.GetProviderKey(),
							ModelRequested: request.Model,
							RequestType:    schemas.ResponsesStreamRequest,
						}

						// Include accumulated raw responses in error
						if sendBackRawResponse && len(rawResponseChunks) > 0 {
							bifrostErr.ExtraFields.RawResponse = rawResponseChunks
						}

						ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
						enrichedErr := providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
						providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, enrichedErr, responseChan, provider.logger)
						resp.CloseBodyStream()
						return
					}
				}

				// Reset event for next one
				currentEvent = ReplicateSSEEvent{}
				continue
			}

			// Parse SSE fields
			if after, ok := strings.CutPrefix(line, "event: "); ok {
				currentEvent.Event = strings.TrimSpace(after)
			} else if after, ok := strings.CutPrefix(line, "data: "); ok {
				// For multiline data, append with newline
				if currentEvent.Data != "" {
					currentEvent.Data += "\n"
				}
				currentEvent.Data += after
			}
		}

		// Handle scanner errors
		if err := scanner.Err(); err != nil {
			if ctx.Err() != nil {
				return
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			provider.logger.Warn("Error reading stream: %v", err)
			bifrostErr := providerUtils.NewBifrostOperationError(schemas.ErrProviderDoRequest, err, provider.GetProviderKey())

			// Include accumulated raw responses in error
			if sendBackRawResponse && len(rawResponseChunks) > 0 {
				bifrostErr.ExtraFields.RawResponse = rawResponseChunks
			}

			enrichedErr := providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, enrichedErr, responseChan, provider.logger)
			return
		}
	}()

	return responseChan, nil
}

// Embedding is not supported by the replicate provider.
func (provider *ReplicateProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.EmbeddingRequest, provider.GetProviderKey())
}

// Speech is not supported by the replicate provider.
func (provider *ReplicateProvider) Speech(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechRequest, provider.GetProviderKey())
}

// SpeechStream is not supported by the replicate provider.
func (provider *ReplicateProvider) SpeechStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostSpeechRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.SpeechStreamRequest, provider.GetProviderKey())
}

// Transcription is not supported by the replicate provider.
func (provider *ReplicateProvider) Transcription(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (*schemas.BifrostTranscriptionResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionRequest, provider.GetProviderKey())
}

// TranscriptionStream is not supported by the replicate provider.
func (provider *ReplicateProvider) TranscriptionStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostTranscriptionRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.TranscriptionStreamRequest, provider.GetProviderKey())
}

// ImageGeneration performs an image generation request to the replicate API using predictions.
func (provider *ReplicateProvider) ImageGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Replicate, provider.customProviderConfig, schemas.ImageGenerationRequest); err != nil {
		return nil, err
	}

	deployment, isDeployment := resolveDeploymentModel(request.Model, key)
	if isDeployment {
		request.Model = deployment
	}

	// Convert Bifrost request to Replicate format
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToReplicateImageGenerationInput(request), nil
		},
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Check for Prefer: wait header from context for sync mode
	isSync := parsePreferHeader(provider.networkConfig.ExtraHeaders)

	// Build prediction URL based on model type (version ID or model name)
	predictionURL := buildPredictionURL(
		ctx,
		provider.networkConfig.BaseURL,
		request.Model,
		provider.customProviderConfig,
		schemas.ImageGenerationRequest,
		isDeployment,
	)

	// Create prediction with appropriate mode
	prediction, rawResponse, latency, providerResponseHeaders, err := createPrediction(
		ctx,
		provider.client,
		jsonData,
		key,
		providerUtils.GetPathFromContext(ctx, predictionURL),
		provider.networkConfig.ExtraHeaders,
		false,
		provider.logger,
		provider.sendBackRawRequest,
		provider.sendBackRawResponse,
	)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// If async mode and not complete, poll until done
	if !isSync && !isTerminalStatus(prediction.Status) {
		prediction, rawResponse, providerResponseHeaders, err = pollPrediction(
			ctx,
			provider.client,
			prediction.URLs.Get,
			key,
			provider.networkConfig.DefaultRequestTimeoutInSeconds,
			provider.logger,
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		)
		if err != nil {
			return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
	}

	// Check for terminal error status (failed/canceled) after sync mode or polling
	if err := checkForErrorStatus(prediction); err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Convert to Bifrost response
	bifrostResponse, err := ToBifrostImageGenerationResponse(prediction)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Set extra fields
	bifrostResponse.ExtraFields.Provider = schemas.Replicate
	bifrostResponse.ExtraFields.RequestType = schemas.ImageGenerationRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// ImageGenerationStream performs a streaming image generation request to the replicate API.
// It creates a prediction with streaming enabled and listens to the stream URL for progressive updates.
func (provider *ReplicateProvider) ImageGenerationStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostImageGenerationRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Replicate, provider.customProviderConfig, schemas.ImageGenerationStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	deployment, isDeployment := resolveDeploymentModel(request.Model, key)
	if isDeployment {
		request.Model = deployment
	}

	// Convert Bifrost request to Replicate format with streaming enabled
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			replicateReq := ToReplicateImageGenerationInput(request)
			replicateReq.Stream = schemas.Ptr(true)
			return replicateReq, nil
		},
		providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Build prediction URL based on model type (version ID or model name)
	predictionURL := buildPredictionURL(
		ctx,
		provider.networkConfig.BaseURL,
		request.Model,
		provider.customProviderConfig,
		schemas.ImageGenerationStreamRequest,
		isDeployment,
	)
	// Create prediction
	prediction, _, _, _, err := createPrediction(
		ctx,
		provider.client,
		jsonData,
		key,
		providerUtils.GetPathFromContext(ctx, predictionURL),
		provider.networkConfig.ExtraHeaders,
		true, // Streaming request, strip Prefer header for async mode
		provider.logger,
		provider.sendBackRawRequest,
		provider.sendBackRawResponse,
	)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Verify stream URL is available
	if prediction.URLs == nil || prediction.URLs.Stream == nil || *prediction.URLs.Stream == "" {
		return nil, providerUtils.NewBifrostOperationError(
			"stream URL not available in prediction response",
			fmt.Errorf("prediction response missing stream URL"),
			providerName,
		)
	}

	streamURL := *prediction.URLs.Stream

	// Connect to stream URL
	bodyStream, resp, bifrostErr := listenToReplicateStreamURL(provider.client, streamURL, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Store provider response headers in context for transport layer
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ImageGenerationStreamRequest, provider.logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ImageGenerationStreamRequest, provider.logger)
			}
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)

		// Setup cancellation handler
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, bodyStream, provider.logger)
		defer stopCancellation()

		startTime := time.Now()
		lastChunkTime := startTime
		chunkIndex := 0

		// Setup scanner to read SSE stream
		scanner := bufio.NewScanner(bodyStream)
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		var currentEvent ReplicateSSEEvent
		// Track last image data for final chunk
		var lastB64Data string
		var lastOutputFormat string
		// Accumulate all raw response chunks for complete stream history
		var rawResponseChunks []interface{}

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := scanner.Text()

			// Skip comment lines
			if strings.HasPrefix(line, ":") {
				continue
			}

			// Empty line indicates end of event
			if line == "" {
				if currentEvent.Event == "" && currentEvent.Data == "" {
					continue
				}

				// Process the complete event
				switch currentEvent.Event {
				case "output":
					// Check if data is a data URI (image) or plain text
					var b64Data, outputFormat string
					if strings.HasPrefix(currentEvent.Data, "data:") {
						// Parse image data from data URI
						var mimeType string
						b64Data, mimeType = parseDataURIImage(currentEvent.Data)

						// Extract output format from MIME type
						if mimeType != "" {
							// Convert "image/webp" to "webp"
							parts := strings.Split(mimeType, "/")
							if len(parts) == 2 {
								outputFormat = parts[1]
							}
						}
					} else {
						// For non-data-URI output (e.g., text), store as-is
						// This shouldn't happen for image generation but handle it gracefully
						provider.logger.Debug(fmt.Sprintf("Received non-data-URI output: %s", currentEvent.Data[:min(100, len(currentEvent.Data))]))
						// Skip non-image output for image generation
						currentEvent = ReplicateSSEEvent{}
						continue
					}

					// Create chunk
					chunk := &schemas.BifrostImageGenerationStreamResponse{
						Type:         schemas.ImageGenerationEventTypePartial,
						Index:        0, // Single image for now
						ChunkIndex:   chunkIndex,
						B64JSON:      b64Data,
						CreatedAt:    time.Now().Unix(),
						OutputFormat: outputFormat,
						ExtraFields: schemas.BifrostResponseExtraFields{
							RequestType:    schemas.ImageGenerationStreamRequest,
							Provider:       providerName,
							ModelRequested: request.Model,
							ChunkIndex:     chunkIndex,
							Latency:        time.Since(lastChunkTime).Milliseconds(),
						},
					}

					// Accumulate raw response chunks if enabled
					if sendBackRawResponse {
						rawResponseChunks = append(rawResponseChunks, currentEvent)
					}

					// Track last image data for final chunk
					lastB64Data = b64Data
					lastOutputFormat = outputFormat

					lastChunkTime = time.Now()
					chunkIndex++

					providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
						providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, nil, chunk),
						responseChan)

				case "done":
					// Parse done event data
					var doneData ReplicateDoneEvent
					if currentEvent.Data != "" && currentEvent.Data != "{}" {
						if err := sonic.Unmarshal([]byte(currentEvent.Data), &doneData); err != nil {
							provider.logger.Warn(fmt.Sprintf("Failed to parse done event data: %v", err))
						}
					}

					// Check for cancellation or error
					switch doneData.Reason {
					case "canceled":
						bifrostErr := providerUtils.NewBifrostOperationError(
							"prediction was canceled",
							fmt.Errorf("stream ended: prediction canceled"),
							providerName,
						)
						bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
							Provider:       providerName,
							ModelRequested: request.Model,
							RequestType:    schemas.ImageGenerationStreamRequest,
						}
						// Include accumulated raw responses in error
						if sendBackRawResponse && len(rawResponseChunks) > 0 {
							bifrostErr.ExtraFields.RawResponse = rawResponseChunks
						}
						ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
						providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
						return
					case "error":
						bifrostErr := providerUtils.NewBifrostOperationError(
							"prediction failed",
							fmt.Errorf("stream ended with error"),
							providerName,
						)
						bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
							Provider:       providerName,
							ModelRequested: request.Model,
							RequestType:    schemas.ImageGenerationStreamRequest,
						}
						// Include accumulated raw responses in error
						if sendBackRawResponse && len(rawResponseChunks) > 0 {
							bifrostErr.ExtraFields.RawResponse = rawResponseChunks
						}
						ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
						providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
						return
					}

					// Send completion chunk (success case when reason is empty or not present)
					finalChunk := &schemas.BifrostImageGenerationStreamResponse{
						Type:         schemas.ImageGenerationEventTypeCompleted,
						Index:        0,
						ChunkIndex:   chunkIndex,
						B64JSON:      lastB64Data,      // Include last image data
						OutputFormat: lastOutputFormat, // Include output format
						CreatedAt:    time.Now().Unix(),
						ExtraFields: schemas.BifrostResponseExtraFields{
							RequestType:    schemas.ImageGenerationStreamRequest,
							Provider:       providerName,
							ModelRequested: request.Model,
							ChunkIndex:     chunkIndex,
							Latency:        time.Since(startTime).Milliseconds(),
						},
					}

					// Set raw request only on final chunk if enabled
					if sendBackRawRequest {
						providerUtils.ParseAndSetRawRequest(&finalChunk.ExtraFields, jsonData)
					}

					// Set accumulated raw responses on final chunk if enabled
					if sendBackRawResponse {
						// Append the final done event to the accumulated chunks
						rawResponseChunks = append(rawResponseChunks, currentEvent)
						finalChunk.ExtraFields.RawResponse = rawResponseChunks
					}

					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
						providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, nil, finalChunk),
						responseChan)
					return

				case "error":
					// Parse error event data
					var errorData ReplicateErrorEvent
					errorMsg := "stream error"

					if currentEvent.Data != "" {
						if err := sonic.Unmarshal([]byte(currentEvent.Data), &errorData); err != nil {
							provider.logger.Warn(fmt.Sprintf("Failed to parse error event data: %v", err))
							// Fallback to raw data
							errorMsg = currentEvent.Data
						} else if errorData.Detail != "" {
							errorMsg = errorData.Detail
						}
					}

					bifrostErr := &schemas.BifrostError{
						IsBifrostError: false,
						Error: &schemas.ErrorField{
							Message: errorMsg,
						},
						ExtraFields: schemas.BifrostErrorExtraFields{
							Provider:       providerName,
							ModelRequested: request.Model,
							RequestType:    schemas.ImageGenerationStreamRequest,
						},
					}
					// Include accumulated raw responses in error
					if sendBackRawResponse {
						rawResponseChunks = append(rawResponseChunks, currentEvent)
						bifrostErr.ExtraFields.RawResponse = rawResponseChunks
					}
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
					return
				}

				// Reset current event after processing
				currentEvent = ReplicateSSEEvent{}
				continue
			}

			// Parse SSE field
			if strings.HasPrefix(line, "event: ") {
				currentEvent.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			} else if strings.HasPrefix(line, "data:") {
				currentEvent.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			} else if strings.HasPrefix(line, "id:") {
				currentEvent.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			}
		}

		// Check for scanner errors
		if err := scanner.Err(); err != nil {
			if ctx.Err() != nil {
				return
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			provider.logger.Warn(fmt.Sprintf("Error reading SSE stream: %v", err))
			providerUtils.ProcessAndSendError(ctx, postHookRunner, err, responseChan, schemas.ImageGenerationStreamRequest, providerName, request.Model, provider.logger)
		}
	}()

	return responseChan, nil
}

// ImageEdit is not supported by the Replicate provider.
func (provider *ReplicateProvider) ImageEdit(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageEditRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Replicate, provider.customProviderConfig, schemas.ImageEditRequest); err != nil {
		return nil, err
	}

	deployment, isDeployment := resolveDeploymentModel(request.Model, key)
	if isDeployment {
		request.Model = deployment
	}

	// Convert Bifrost request to Replicate format
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToReplicateImageEditInput(request), nil
		},
		provider.GetProviderKey())
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Check for Prefer: wait header from context for sync mode
	isSync := parsePreferHeader(provider.networkConfig.ExtraHeaders)

	// Build prediction URL based on model type (version ID or model name)
	predictionURL := buildPredictionURL(
		ctx,
		provider.networkConfig.BaseURL,
		request.Model,
		provider.customProviderConfig,
		schemas.ImageEditRequest,
		isDeployment,
	)

	// Create prediction with appropriate mode
	prediction, rawResponse, latency, providerResponseHeaders, err := createPrediction(
		ctx,
		provider.client,
		jsonData,
		key,
		providerUtils.GetPathFromContext(ctx, predictionURL),
		provider.networkConfig.ExtraHeaders,
		false,
		provider.logger,
		provider.sendBackRawRequest,
		provider.sendBackRawResponse,
	)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// If async mode and not complete, poll until done
	if !isSync && !isTerminalStatus(prediction.Status) {
		prediction, rawResponse, providerResponseHeaders, err = pollPrediction(
			ctx,
			provider.client,
			prediction.URLs.Get,
			key,
			provider.networkConfig.DefaultRequestTimeoutInSeconds,
			provider.logger,
			providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse),
		)
		if err != nil {
			return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
		}
	}

	// Check for terminal error status (failed/canceled) after sync mode or polling
	if err := checkForErrorStatus(prediction); err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Convert to Bifrost response (reuse image generation response format)
	bifrostResponse, err := ToBifrostImageGenerationResponse(prediction)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Set extra fields
	bifrostResponse.ExtraFields.Provider = schemas.Replicate
	bifrostResponse.ExtraFields.RequestType = schemas.ImageEditRequest
	bifrostResponse.ExtraFields.Latency = latency.Milliseconds()
	bifrostResponse.ExtraFields.ModelRequested = request.Model
	bifrostResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		bifrostResponse.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResponse, nil
}

// ImageEditStream performs a streaming image edit request to the replicate API.
// It creates a prediction with streaming enabled and listens to the stream URL for progressive updates.
func (provider *ReplicateProvider) ImageEditStream(ctx *schemas.BifrostContext, postHookRunner schemas.PostHookRunner, key schemas.Key, request *schemas.BifrostImageEditRequest) (chan *schemas.BifrostStreamChunk, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Replicate, provider.customProviderConfig, schemas.ImageEditStreamRequest); err != nil {
		return nil, err
	}

	providerName := provider.GetProviderKey()
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	deployment, isDeployment := resolveDeploymentModel(request.Model, key)
	if isDeployment {
		request.Model = deployment
	}

	// Convert Bifrost request to Replicate format with streaming enabled
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			replicateReq := ToReplicateImageEditInput(request)
			replicateReq.Stream = schemas.Ptr(true)
			return replicateReq, nil
		},
		providerName)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Build prediction URL based on model type (version ID or model name)
	predictionURL := buildPredictionURL(
		ctx,
		provider.networkConfig.BaseURL,
		request.Model,
		provider.customProviderConfig,
		schemas.ImageEditStreamRequest,
		isDeployment,
	)

	// Create prediction
	prediction, _, _, _, err := createPrediction(
		ctx,
		provider.client,
		jsonData,
		key,
		providerUtils.GetPathFromContext(ctx, predictionURL),
		provider.networkConfig.ExtraHeaders,
		true, // Streaming request, strip Prefer header for async mode
		provider.logger,
		provider.sendBackRawRequest,
		provider.sendBackRawResponse,
	)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	// Verify stream URL is available
	if prediction.URLs == nil || prediction.URLs.Stream == nil || *prediction.URLs.Stream == "" {
		return nil, providerUtils.NewBifrostOperationError(
			"stream URL not available in prediction response",
			fmt.Errorf("prediction response missing stream URL"),
			providerName,
		)
	}

	streamURL := *prediction.URLs.Stream

	// Connect to stream URL
	bodyStream, resp, bifrostErr := listenToReplicateStreamURL(provider.client, streamURL, key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Store provider response headers in context for transport layer
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	// Create response channel
	responseChan := make(chan *schemas.BifrostStreamChunk, schemas.DefaultStreamBufferSize)

	// Start streaming in a goroutine
	go func() {
		defer func() {
			if ctx.Err() == context.Canceled {
				providerUtils.HandleStreamCancellation(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ImageEditStreamRequest, provider.logger)
			} else if ctx.Err() == context.DeadlineExceeded {
				providerUtils.HandleStreamTimeout(ctx, postHookRunner, responseChan, providerName, request.Model, schemas.ImageEditStreamRequest, provider.logger)
			}
			close(responseChan)
		}()
		defer providerUtils.ReleaseStreamingResponse(resp)

		// Setup cancellation handler
		stopCancellation := providerUtils.SetupStreamCancellation(ctx, bodyStream, provider.logger)
		defer stopCancellation()

		startTime := time.Now()
		lastChunkTime := startTime
		chunkIndex := 0

		// Setup scanner to read SSE stream
		scanner := bufio.NewScanner(bodyStream)
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 10*1024*1024)

		var currentEvent ReplicateSSEEvent
		// Track last image data for final chunk
		var lastB64Data string
		var lastOutputFormat string
		// Accumulate all raw response chunks for complete stream history
		var rawResponseChunks []interface{}

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := scanner.Text()

			// Skip comment lines
			if strings.HasPrefix(line, ":") {
				continue
			}

			// Empty line indicates end of event
			if line == "" {
				if currentEvent.Event == "" && currentEvent.Data == "" {
					continue
				}

				// Process the complete event
				switch currentEvent.Event {
				case "output":
					// Check if data is a data URI (image) or plain text
					var b64Data, outputFormat string
					if strings.HasPrefix(currentEvent.Data, "data:") {
						// Parse image data from data URI
						var mimeType string
						b64Data, mimeType = parseDataURIImage(currentEvent.Data)

						// Extract output format from MIME type
						if mimeType != "" {
							// Convert "image/webp" to "webp"
							parts := strings.Split(mimeType, "/")
							if len(parts) == 2 {
								outputFormat = parts[1]
							}
						}
					} else {
						// For non-data-URI output, skip for image edit
						provider.logger.Debug(fmt.Sprintf("Received non-data-URI output: %s", currentEvent.Data[:min(100, len(currentEvent.Data))]))
						currentEvent = ReplicateSSEEvent{}
						continue
					}

					// Create chunk (use ImageEditEventTypePartial)
					chunk := &schemas.BifrostImageGenerationStreamResponse{
						Type:         schemas.ImageEditEventTypePartial,
						Index:        0,
						ChunkIndex:   chunkIndex,
						B64JSON:      b64Data,
						CreatedAt:    time.Now().Unix(),
						OutputFormat: outputFormat,
						ExtraFields: schemas.BifrostResponseExtraFields{
							RequestType:    schemas.ImageEditStreamRequest,
							Provider:       providerName,
							ModelRequested: request.Model,
							ChunkIndex:     chunkIndex,
							Latency:        time.Since(lastChunkTime).Milliseconds(),
						},
					}

					// Accumulate raw response chunks if enabled
					if sendBackRawResponse {
						rawResponseChunks = append(rawResponseChunks, currentEvent)
					}

					// Track last image data for final chunk
					lastB64Data = b64Data
					lastOutputFormat = outputFormat

					lastChunkTime = time.Now()
					chunkIndex++

					providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
						providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, nil, chunk),
						responseChan)

				case "done":
					// Parse done event data
					var doneData ReplicateDoneEvent
					if currentEvent.Data != "" && currentEvent.Data != "{}" {
						if err := sonic.Unmarshal([]byte(currentEvent.Data), &doneData); err != nil {
							provider.logger.Warn(fmt.Sprintf("Failed to parse done event data: %v", err))
						}
					}

					// Check for cancellation or error
					switch doneData.Reason {
					case "canceled":
						bifrostErr := providerUtils.NewBifrostOperationError(
							"prediction was canceled",
							fmt.Errorf("stream ended: prediction canceled"),
							providerName,
						)
						bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
							Provider:       providerName,
							ModelRequested: request.Model,
							RequestType:    schemas.ImageEditStreamRequest,
						}
						if sendBackRawResponse && len(rawResponseChunks) > 0 {
							bifrostErr.ExtraFields.RawResponse = rawResponseChunks
						}
						ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
						providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
						return
					case "error":
						bifrostErr := providerUtils.NewBifrostOperationError(
							"prediction failed",
							fmt.Errorf("stream ended with error"),
							providerName,
						)
						bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
							Provider:       providerName,
							ModelRequested: request.Model,
							RequestType:    schemas.ImageEditStreamRequest,
						}
						if sendBackRawResponse && len(rawResponseChunks) > 0 {
							bifrostErr.ExtraFields.RawResponse = rawResponseChunks
						}
						ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
						providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
						return
					}

					// Send completion chunk (success case)
					finalChunk := &schemas.BifrostImageGenerationStreamResponse{
						Type:         schemas.ImageEditEventTypeCompleted,
						Index:        0,
						ChunkIndex:   chunkIndex,
						B64JSON:      lastB64Data,
						CreatedAt:    time.Now().Unix(),
						OutputFormat: lastOutputFormat,
						ExtraFields: schemas.BifrostResponseExtraFields{
							RequestType:    schemas.ImageEditStreamRequest,
							Provider:       providerName,
							ModelRequested: request.Model,
							ChunkIndex:     chunkIndex,
							Latency:        time.Since(startTime).Milliseconds(),
						},
					}

					if sendBackRawRequest {
						providerUtils.ParseAndSetRawRequest(&finalChunk.ExtraFields, jsonData)
					}
					if sendBackRawResponse {
						rawResponseChunks = append(rawResponseChunks, currentEvent)
						finalChunk.ExtraFields.RawResponse = rawResponseChunks
					}

					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendResponse(ctx, postHookRunner,
						providerUtils.GetBifrostResponseForStreamResponse(nil, nil, nil, nil, nil, finalChunk),
						responseChan)
					return

				case "error":
					// Parse error event
					var errorData ReplicateErrorEvent
					if err := sonic.Unmarshal([]byte(currentEvent.Data), &errorData); err != nil {
						provider.logger.Warn(fmt.Sprintf("Failed to parse error event: %v", err))
						errorData.Detail = currentEvent.Data
					}

					bifrostErr := providerUtils.NewBifrostOperationError(
						"stream error",
						fmt.Errorf("%s", errorData.Detail),
						providerName,
					)
					bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
						Provider:       providerName,
						ModelRequested: request.Model,
						RequestType:    schemas.ImageEditStreamRequest,
					}
					if sendBackRawResponse && len(rawResponseChunks) > 0 {
						bifrostErr.ExtraFields.RawResponse = rawResponseChunks
					}
					ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
					providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
					return
				}

				// Reset for next event
				currentEvent = ReplicateSSEEvent{}
				continue
			}

			// Parse SSE field
			if strings.HasPrefix(line, "event:") {
				currentEvent.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			} else if strings.HasPrefix(line, "data:") {
				currentEvent.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			} else if strings.HasPrefix(line, "id:") {
				currentEvent.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			}
		}

		// Check for scanner errors
		if err := scanner.Err(); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			bifrostErr := providerUtils.NewBifrostOperationError(
				"stream read error",
				err,
				providerName,
			)
			bifrostErr.ExtraFields = schemas.BifrostErrorExtraFields{
				Provider:       providerName,
				ModelRequested: request.Model,
				RequestType:    schemas.ImageEditStreamRequest,
			}
			ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			providerUtils.ProcessAndSendBifrostError(ctx, postHookRunner, bifrostErr, responseChan, provider.logger)
		}
	}()

	return responseChan, nil
}

// ImageVariation is not supported by the Replicate provider.
func (provider *ReplicateProvider) ImageVariation(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostImageVariationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ImageVariationRequest, provider.GetProviderKey())
}

// BatchCreate is not supported by replicate provider.
func (provider *ReplicateProvider) BatchCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostBatchCreateRequest) (*schemas.BifrostBatchCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCreateRequest, provider.GetProviderKey())
}

// BatchList is not supported by replicate provider.
func (provider *ReplicateProvider) BatchList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchListRequest) (*schemas.BifrostBatchListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchListRequest, provider.GetProviderKey())
}

// BatchRetrieve is not supported by replicate provider.
func (provider *ReplicateProvider) BatchRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchRetrieveRequest) (*schemas.BifrostBatchRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchRetrieveRequest, provider.GetProviderKey())
}

// BatchCancel is not supported by replicate provider.
func (provider *ReplicateProvider) BatchCancel(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchCancelRequest) (*schemas.BifrostBatchCancelResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchCancelRequest, provider.GetProviderKey())
}

// BatchResults is not supported by replicate provider.
func (provider *ReplicateProvider) BatchResults(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostBatchResultsRequest) (*schemas.BifrostBatchResultsResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.BatchResultsRequest, provider.GetProviderKey())
}

// FileUpload uploads a file to Replicate's Files API.
func (provider *ReplicateProvider) FileUpload(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostFileUploadRequest) (*schemas.BifrostFileUploadResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if len(request.File) == 0 {
		return nil, providerUtils.NewBifrostOperationError("file content is required", nil, providerName)
	}

	// Create multipart form data
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add file field (content)
	filename := request.Filename
	if filename == "" {
		filename = "file"
	}

	// Determine content type - use from request or infer from filename
	contentType := "application/octet-stream"
	if request.ContentType != nil && *request.ContentType != "" {
		contentType = *request.ContentType
	} else {
		// Try to infer from filename extension
		if strings.HasSuffix(filename, ".json") {
			contentType = "application/json"
		} else if strings.HasSuffix(filename, ".jsonl") {
			contentType = "application/x-ndjson"
		} else if strings.HasSuffix(filename, ".txt") {
			contentType = "text/plain"
		} else if strings.HasSuffix(filename, ".zip") {
			contentType = "application/zip"
		}
	}

	// Create form file with proper headers
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="content"; filename="%s"`, filename))
	h.Set("Content-Type", contentType)

	part, err := writer.CreatePart(h)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to create form file", err, providerName)
	}
	if _, err := part.Write(request.File); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write file content", err, providerName)
	}

	// Add filename field if provided
	if filename != "" {
		if err := writer.WriteField("filename", filename); err != nil {
			return nil, providerUtils.NewBifrostOperationError("failed to write filename field", err, providerName)
		}
	}

	// Add type field (content type)
	if err := writer.WriteField("type", contentType); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to write type field", err, providerName)
	}

	// Add metadata field if provided
	if request.ExtraParams != nil {
		if metadata, ok := request.ExtraParams["metadata"].(map[string]interface{}); ok {
			if len(metadata) > 0 {
				metadataJSON, err := sonic.Marshal(metadata)
				if err != nil {
					return nil, providerUtils.NewBifrostOperationError("failed to marshal metadata", err, providerName)
				}
				h := make(textproto.MIMEHeader)
				h.Set("Content-Disposition", `form-data; name="metadata"`)
				h.Set("Content-Type", "application/json")
				metadataPart, err := writer.CreatePart(h)
				if err != nil {
					return nil, providerUtils.NewBifrostOperationError("failed to create metadata part", err, providerName)
				}
				if _, err := metadataPart.Write(metadataJSON); err != nil {
					return nil, providerUtils.NewBifrostOperationError("failed to write metadata", err, providerName)
				}
			}
		}
	}

	if err := writer.Close(); err != nil {
		return nil, providerUtils.NewBifrostOperationError("failed to close multipart writer", err, providerName)
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/files")
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType(writer.FormDataContentType())

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	req.SetBody(buf.Bytes())

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK && resp.StatusCode() != fasthttp.StatusCreated {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, parseReplicateError(resp.Body(), resp.StatusCode())
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
	}

	var replicateResp ReplicateFileResponse
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &replicateResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	fileResponse := replicateResp.ToBifrostFileUploadResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse)
	fileResponse.ExtraFields.ProviderResponseHeaders = providerUtils.ExtractProviderResponseHeaders(resp)
	return fileResponse, nil
}

// FileList lists files using serial pagination across keys.
// Exhausts all pages from one key before moving to the next.
func (provider *ReplicateProvider) FileList(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileListRequest) (*schemas.BifrostFileListResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)

	// Initialize serial pagination helper (Replicate uses cursor-based pagination)
	helper, err := providerUtils.NewSerialListHelper(keys, request.After, provider.logger)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError("invalid pagination cursor", err, providerName)
	}

	// Get current key to query
	key, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		// All keys exhausted
		return &schemas.BifrostFileListResponse{
			Object:  "list",
			Data:    []schemas.FileObject{},
			HasMore: false,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.FileListRequest,
				Provider:    providerName,
			},
		}, nil
	}

	// Create request
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	// Build URL with query params
	requestURL := provider.networkConfig.BaseURL + "/v1/files"
	values := url.Values{}
	if request.Limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", request.Limit))
	}
	// Use native cursor from serial helper (Replicate pagination URL)
	if nativeCursor != "" {
		// For Replicate, the cursor is actually the full next URL
		requestURL = nativeCursor
	} else if encodedValues := values.Encode(); encodedValues != "" {
		requestURL += "?" + encodedValues
	}

	// Set headers
	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(requestURL)
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")

	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	// Make request
	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Handle error response
	if resp.StatusCode() != fasthttp.StatusOK {
		provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
		return nil, parseReplicateError(resp.Body(), resp.StatusCode())
	}

	body, decodeErr := providerUtils.CheckAndDecodeBody(resp)
	if decodeErr != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decodeErr, providerName)
	}

	var replicateResp ReplicateFileListResponse
	_, _, bifrostErr = providerUtils.HandleProviderResponse(body, &replicateResp, nil, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	// Convert files to Bifrost format
	files := make([]schemas.FileObject, 0, len(replicateResp.Results))
	for _, file := range replicateResp.Results {
		files = append(files, schemas.FileObject{
			ID:        file.ID,
			Object:    "file",
			Bytes:     file.Size,
			CreatedAt: ParseReplicateTimestamp(file.CreatedAt),
			Filename:  file.Name,
			Purpose:   schemas.FilePurposeBatch,
			Status:    ToBifrostFileStatus(&file),
		})
	}

	// Build cursor for next request
	// Replicate uses full URL for pagination
	var nextCursor string
	hasMore := false
	if replicateResp.Next != nil && *replicateResp.Next != "" {
		nextCursor = *replicateResp.Next
		hasMore = true
	}

	// Use helper to build proper cursor with key index
	finalCursor, finalHasMore := helper.BuildNextCursor(hasMore, nextCursor)

	// Convert to Bifrost response
	bifrostResp := &schemas.BifrostFileListResponse{
		Object:  "list",
		Data:    files,
		HasMore: finalHasMore,
		ExtraFields: schemas.BifrostResponseExtraFields{
			RequestType:             schemas.FileListRequest,
			Provider:                providerName,
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
		},
	}
	if finalCursor != "" {
		bifrostResp.After = &finalCursor
	}

	return bifrostResp, nil
}

// FileRetrieve retrieves file metadata from Replicate's Files API by trying each key until found.
func (provider *ReplicateProvider) FileRetrieve(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileRetrieveRequest) (*schemas.BifrostFileRetrieveResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/files/" + url.PathEscape(request.FileID))
		req.Header.SetMethod(http.MethodGet)
		req.Header.SetContentType("application/json")

		if key.Value.GetValue() != "" {
			req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
		}

		// Make request
		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = parseReplicateError(resp.Body(), resp.StatusCode())
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
			continue
		}

		var replicateResp ReplicateFileResponse
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &replicateResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		fileRetrieveResponse := replicateResp.ToBifrostFileRetrieveResponse(providerName, latency, sendBackRawRequest, sendBackRawResponse, rawRequest, rawResponse)
		fileRetrieveResponse.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
		return fileRetrieveResponse, nil
	}

	return nil, lastErr
}

// FileDelete deletes a file from Replicate's Files API by trying each key until successful.
func (provider *ReplicateProvider) FileDelete(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileDeleteRequest) (*schemas.BifrostFileDeleteResponse, *schemas.BifrostError) {
	providerName := provider.GetProviderKey()

	if request.FileID == "" {
		return nil, providerUtils.NewBifrostOperationError("file_id is required", nil, providerName)
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	var lastErr *schemas.BifrostError
	for _, key := range keys {
		// Create request
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()

		// Set headers
		providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
		req.SetRequestURI(provider.networkConfig.BaseURL + "/v1/files/" + url.PathEscape(request.FileID))
		req.Header.SetMethod(http.MethodDelete)
		req.Header.SetContentType("application/json")

		if key.Value.GetValue() != "" {
			req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
		}

		// Make request
		latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		// Handle success response (204 No Content is expected for DELETE)
		if resp.StatusCode() == fasthttp.StatusNoContent {
			providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			return &schemas.BifrostFileDeleteResponse{
				ID:      request.FileID,
				Object:  "file",
				Deleted: true,
				ExtraFields: schemas.BifrostResponseExtraFields{
					RequestType:             schemas.FileDeleteRequest,
					Provider:                providerName,
					Latency:                 latency.Milliseconds(),
					ProviderResponseHeaders: providerResponseHeaders,
				},
			}, nil
		}

		// Handle error response
		if resp.StatusCode() != fasthttp.StatusOK {
			provider.logger.Debug("error from %s provider: %s", providerName, string(resp.Body()))
			lastErr = parseReplicateError(resp.Body(), resp.StatusCode())
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			continue
		}

		// Some APIs return 200 with body, parse it
		body, err := providerUtils.CheckAndDecodeBody(resp)
		if err != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err, providerName)
			continue
		}

		// Try to parse response body if present
		var deleteResp map[string]interface{}
		rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponse(body, &deleteResp, nil, sendBackRawRequest, sendBackRawResponse)
		if bifrostErr != nil {
			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			lastErr = bifrostErr
			continue
		}

		providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)

		result := &schemas.BifrostFileDeleteResponse{
			ID:      request.FileID,
			Object:  "file",
			Deleted: true,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:             schemas.FileDeleteRequest,
				Provider:                providerName,
				Latency:                 latency.Milliseconds(),
				ProviderResponseHeaders: providerResponseHeaders,
			},
		}

		if sendBackRawRequest {
			result.ExtraFields.RawRequest = rawRequest
		}

		if sendBackRawResponse {
			result.ExtraFields.RawResponse = rawResponse
		}

		return result, nil
	}

	return nil, lastErr
}

// FileContent is not supported by replicate provider.
func (provider *ReplicateProvider) FileContent(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostFileContentRequest) (*schemas.BifrostFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.FileContentRequest, provider.GetProviderKey())
}

func (provider *ReplicateProvider) CountTokens(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
}

// ContainerCreate is not supported by replicate provider.
func (provider *ReplicateProvider) ContainerCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerCreateRequest) (*schemas.BifrostContainerCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerCreateRequest, provider.GetProviderKey())
}

// ContainerList is not supported by replicate provider.
func (provider *ReplicateProvider) ContainerList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerListRequest) (*schemas.BifrostContainerListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerListRequest, provider.GetProviderKey())
}

// ContainerRetrieve is not supported by replicate provider.
func (provider *ReplicateProvider) ContainerRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerRetrieveRequest) (*schemas.BifrostContainerRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerRetrieveRequest, provider.GetProviderKey())
}

// ContainerDelete is not supported by replicate provider.
func (provider *ReplicateProvider) ContainerDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerDeleteRequest) (*schemas.BifrostContainerDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerDeleteRequest, provider.GetProviderKey())
}

// ContainerFileCreate is not supported by replicate provider.
func (provider *ReplicateProvider) ContainerFileCreate(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostContainerFileCreateRequest) (*schemas.BifrostContainerFileCreateResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileCreateRequest, provider.GetProviderKey())
}

// ContainerFileList is not supported by replicate provider.
func (provider *ReplicateProvider) ContainerFileList(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileListRequest) (*schemas.BifrostContainerFileListResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileListRequest, provider.GetProviderKey())
}

// ContainerFileRetrieve is not supported by replicate provider.
func (provider *ReplicateProvider) ContainerFileRetrieve(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileRetrieveRequest) (*schemas.BifrostContainerFileRetrieveResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileRetrieveRequest, provider.GetProviderKey())
}

// ContainerFileContent is not supported by replicate provider.
func (provider *ReplicateProvider) ContainerFileContent(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileContentRequest) (*schemas.BifrostContainerFileContentResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileContentRequest, provider.GetProviderKey())
}

// ContainerFileDelete is not supported by replicate provider.
func (provider *ReplicateProvider) ContainerFileDelete(_ *schemas.BifrostContext, _ []schemas.Key, _ *schemas.BifrostContainerFileDeleteRequest) (*schemas.BifrostContainerFileDeleteResponse, *schemas.BifrostError) {
	return nil, providerUtils.NewUnsupportedOperationError(schemas.ContainerFileDeleteRequest, provider.GetProviderKey())
}
