package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sagemakerruntime"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	pluginName      = "sagemaker"
	defaultProvider = "sagemaker"
	defaultTimeout  = 60 * time.Second
	maxTimeout      = 15 * time.Minute
	wildcardModel   = "*"
)

type runtimeClient interface {
	InvokeEndpoint(context.Context, *sagemakerruntime.InvokeEndpointInput, ...func(*sagemakerruntime.Options)) (*sagemakerruntime.InvokeEndpointOutput, error)
}

type pluginConfig struct {
	Provider       string                    `json:"provider"`
	Region         string                    `json:"region"`
	TimeoutSeconds int                       `json:"timeout_seconds"`
	Endpoints      map[string]endpointConfig `json:"endpoints"`
}

type endpointConfig struct {
	EndpointName           string `json:"endpoint_name"`
	TargetModel            string `json:"target_model"`
	TargetVariant          string `json:"target_variant"`
	InferenceComponentName string `json:"inference_component_name"`
	CustomAttributes       string `json:"custom_attributes"`
}

type pluginState struct {
	provider  string
	timeout   time.Duration
	endpoints map[string]endpointConfig
	client    runtimeClient
}

var activeState atomic.Pointer[pluginState]

// Init validates the plugin configuration and creates a SageMaker Runtime client
// from the AWS SDK default credential chain.
func Init(rawConfig any) error {
	parsed, err := parseConfig(rawConfig)
	if err != nil {
		return err
	}

	loadOptions := make([]func(*awsconfig.LoadOptions) error, 0, 1)
	if parsed.Region != "" {
		loadOptions = append(loadOptions, awsconfig.WithRegion(parsed.Region))
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(context.Background(), loadOptions...)
	if err != nil {
		return fmt.Errorf("load AWS configuration: %w", err)
	}
	if strings.TrimSpace(awsConfig.Region) == "" {
		return errors.New("AWS region is required in plugin config or the AWS SDK environment")
	}

	activeState.Store(&pluginState{
		provider:  parsed.Provider,
		timeout:   time.Duration(parsed.TimeoutSeconds) * time.Second,
		endpoints: parsed.Endpoints,
		client:    sagemakerruntime.NewFromConfig(awsConfig),
	})
	return nil
}

// GetName returns the plugin's system identifier.
func GetName() string {
	return pluginName
}

// PreRequestHook does not alter routing. The custom provider name in the
// Bifrost request determines whether PreLLMHook handles the request.
func PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

// PreLLMHook converts a supported Bifrost request to its OpenAI wire shape,
// invokes SageMaker with IAM/SigV4 authentication, and returns a typed response.
func PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if req == nil {
		return req, nil, nil
	}

	provider, model, _ := req.GetRequestFields()
	state := activeState.Load()
	if state == nil {
		if string(provider) == defaultProvider {
			return req, failure(500, "plugin_not_initialized", "the SageMaker plugin is not initialized"), nil
		}
		return req, nil, nil
	}
	if string(provider) != state.provider {
		return req, nil, nil
	}

	endpoint, ok := resolveEndpoint(state.endpoints, model)
	if !ok {
		return req, failure(400, "unsupported_model", fmt.Sprintf("model %q is not mapped to a SageMaker endpoint", model)), nil
	}

	bifrostCtx := ctx
	if bifrostCtx == nil {
		bifrostCtx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}
	body, bodyErr := buildOpenAIRequestBody(bifrostCtx, req)
	if bodyErr != nil {
		allowFallbacks := false
		bodyErr.AllowFallbacks = &allowFallbacks
		if bodyErr.Error != nil && bodyErr.Error.Message != "" {
			return req, &schemas.LLMPluginShortCircuit{Error: bodyErr}, nil
		}
		return req, failure(400, "invalid_request", "could not serialize the request"), nil
	}

	invokeContext, cancel := context.WithTimeout(bifrostCtx, state.timeout)
	defer cancel()

	invokeInput := &sagemakerruntime.InvokeEndpointInput{
		EndpointName: aws.String(endpoint.EndpointName),
		Body:         body,
		ContentType:  aws.String("application/json"),
		Accept:       aws.String("application/json"),
	}
	setOptionalInvokeFields(invokeInput, endpoint)

	startedAt := time.Now()
	output, err := state.client.InvokeEndpoint(invokeContext, invokeInput)
	latency := time.Since(startedAt)
	if err != nil {
		bifrostCtx.Log(schemas.LogLevelError, fmt.Sprintf("SageMaker InvokeEndpoint failed for model %q: %v", model, err))
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(invokeContext.Err(), context.DeadlineExceeded) {
			return req, failure(504, "sagemaker_timeout", "the SageMaker endpoint invocation timed out"), nil
		}
		return req, failure(502, "sagemaker_invoke_failed", "the SageMaker endpoint invocation failed"), nil
	}
	if output == nil {
		return req, failure(502, "invalid_sagemaker_response", "SageMaker returned an empty response"), nil
	}

	response, err := parseOpenAIResponse(req, output.Body)
	if err != nil {
		return req, failure(502, "invalid_sagemaker_response", err.Error()), nil
	}
	if extraFields := response.GetExtraFields(); extraFields != nil {
		extraFields.Latency = latency.Milliseconds()
	}

	return req, &schemas.LLMPluginShortCircuit{Response: response}, nil
}

// PostLLMHook leaves the response unchanged.
func PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// Cleanup removes the active client and endpoint configuration.
func Cleanup() error {
	activeState.Store(nil)
	return nil
}

func parseConfig(rawConfig any) (pluginConfig, error) {
	var parsed pluginConfig
	if rawConfig == nil {
		return parsed, errors.New("plugin config must be an object")
	}

	payload, err := json.Marshal(rawConfig)
	if err != nil {
		return parsed, fmt.Errorf("encode plugin config: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parsed); err != nil {
		return parsed, fmt.Errorf("decode plugin config: %w", err)
	}

	parsed.Provider = strings.TrimSpace(parsed.Provider)
	if parsed.Provider == "" {
		parsed.Provider = defaultProvider
	}
	parsed.Region = strings.TrimSpace(parsed.Region)
	if parsed.TimeoutSeconds == 0 {
		parsed.TimeoutSeconds = int(defaultTimeout / time.Second)
	}
	timeout := time.Duration(parsed.TimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > maxTimeout {
		return pluginConfig{}, fmt.Errorf("timeout_seconds must be between 1 and %d", int(maxTimeout/time.Second))
	}
	if len(parsed.Endpoints) == 0 {
		return pluginConfig{}, errors.New("at least one endpoint mapping is required")
	}

	normalizedEndpoints := make(map[string]endpointConfig, len(parsed.Endpoints))
	for rawModel, endpoint := range parsed.Endpoints {
		model := strings.TrimSpace(rawModel)
		if model == "" {
			return pluginConfig{}, errors.New("endpoint model names must not be empty")
		}
		if _, exists := normalizedEndpoints[model]; exists {
			return pluginConfig{}, fmt.Errorf("endpoint model %q is configured more than once", model)
		}

		endpoint.EndpointName = strings.TrimSpace(endpoint.EndpointName)
		endpoint.TargetModel = strings.TrimSpace(endpoint.TargetModel)
		endpoint.TargetVariant = strings.TrimSpace(endpoint.TargetVariant)
		endpoint.InferenceComponentName = strings.TrimSpace(endpoint.InferenceComponentName)
		endpoint.CustomAttributes = strings.TrimSpace(endpoint.CustomAttributes)
		if endpoint.EndpointName == "" {
			return pluginConfig{}, fmt.Errorf("endpoint_name is required for model %q", model)
		}
		if err := validateCustomAttributes(endpoint.CustomAttributes); err != nil {
			return pluginConfig{}, fmt.Errorf("custom_attributes for model %q: %w", model, err)
		}
		normalizedEndpoints[model] = endpoint
	}
	parsed.Endpoints = normalizedEndpoints

	return parsed, nil
}

func resolveEndpoint(endpoints map[string]endpointConfig, model string) (endpointConfig, bool) {
	if endpoint, ok := endpoints[model]; ok {
		return endpoint, true
	}
	endpoint, ok := endpoints[wildcardModel]
	return endpoint, ok
}

func buildOpenAIRequestBody(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) ([]byte, *schemas.BifrostError) {
	switch req.RequestType {
	case schemas.TextCompletionRequest:
		if req.TextCompletionRequest == nil {
			return nil, invalidRequestError("text completion request is missing")
		}
		return providerUtils.CheckContextAndGetRequestBody(ctx, req.TextCompletionRequest, func() (providerUtils.RequestBodyWithExtraParams, error) {
			return openai.ToOpenAITextCompletionRequest(req.TextCompletionRequest), nil
		})
	case schemas.ChatCompletionRequest:
		if req.ChatRequest == nil {
			return nil, invalidRequestError("chat completion request is missing")
		}
		return providerUtils.CheckContextAndGetRequestBody(ctx, req.ChatRequest, func() (providerUtils.RequestBodyWithExtraParams, error) {
			return openai.ToOpenAIChatRequest(ctx, req.ChatRequest), nil
		})
	case schemas.ResponsesRequest:
		if req.ResponsesRequest == nil {
			return nil, invalidRequestError("responses request is missing")
		}
		return providerUtils.CheckContextAndGetRequestBody(ctx, req.ResponsesRequest, func() (providerUtils.RequestBodyWithExtraParams, error) {
			return openai.ToOpenAIResponsesRequest(ctx, req.ResponsesRequest), nil
		})
	case schemas.EmbeddingRequest:
		if req.EmbeddingRequest == nil {
			return nil, invalidRequestError("embedding request is missing")
		}
		return providerUtils.CheckContextAndGetRequestBody(ctx, req.EmbeddingRequest, func() (providerUtils.RequestBodyWithExtraParams, error) {
			return openai.ToOpenAIEmbeddingRequest(req.EmbeddingRequest), nil
		})
	case schemas.RerankRequest:
		if req.RerankRequest == nil {
			return nil, invalidRequestError("rerank request is missing")
		}
		return providerUtils.CheckContextAndGetRequestBody(ctx, req.RerankRequest, func() (providerUtils.RequestBodyWithExtraParams, error) {
			return openai.ToOpenAIRerankRequest(req.RerankRequest), nil
		})
	default:
		return nil, invalidRequestError("the SageMaker plugin supports non-streaming text completions, chat completions, responses, embeddings, and rerank requests")
	}
}

func parseOpenAIResponse(req *schemas.BifrostRequest, body []byte) (*schemas.BifrostResponse, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, errors.New("SageMaker returned an empty response body")
	}

	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, errors.New("SageMaker returned invalid JSON")
	}
	if rawError := bytes.TrimSpace(envelope.Error); len(rawError) > 0 && !bytes.Equal(rawError, []byte("null")) {
		return nil, errors.New("SageMaker returned an OpenAI error response")
	}

	switch req.RequestType {
	case schemas.TextCompletionRequest:
		var response schemas.BifrostTextCompletionResponse
		if err := schemas.Unmarshal(body, &response); err != nil {
			return nil, errors.New("SageMaker returned an invalid text completion response")
		}
		if len(response.Choices) == 0 {
			return nil, errors.New("SageMaker text completion response must include choices")
		}
		if response.Model == "" {
			response.Model = req.TextCompletionRequest.Model
		}
		if response.Object == "" {
			response.Object = "text_completion"
		}
		return &schemas.BifrostResponse{TextCompletionResponse: &response}, nil
	case schemas.ChatCompletionRequest:
		var response schemas.BifrostChatResponse
		if err := schemas.Unmarshal(body, &response); err != nil {
			return nil, errors.New("SageMaker returned an invalid chat completion response")
		}
		if len(response.Choices) == 0 {
			return nil, errors.New("SageMaker chat completion response must include choices")
		}
		response.BackfillParams(req.ChatRequest)
		return &schemas.BifrostResponse{ChatResponse: &response}, nil
	case schemas.ResponsesRequest:
		var response schemas.BifrostResponsesResponse
		if err := schemas.Unmarshal(body, &response); err != nil {
			return nil, errors.New("SageMaker returned an invalid responses response")
		}
		if response.Model == "" {
			response.Model = req.ResponsesRequest.Model
		}
		if response.Object == "" {
			response.Object = "response"
		}
		return &schemas.BifrostResponse{ResponsesResponse: &response}, nil
	case schemas.EmbeddingRequest:
		var response schemas.BifrostEmbeddingResponse
		if err := schemas.Unmarshal(body, &response); err != nil {
			return nil, errors.New("SageMaker returned an invalid embedding response")
		}
		if err := validateEmbeddingResponse(req.EmbeddingRequest, &response); err != nil {
			return nil, err
		}
		response.BackfillParams(req.EmbeddingRequest)
		if response.Object == "" {
			response.Object = "list"
		}
		return &schemas.BifrostResponse{EmbeddingResponse: &response}, nil
	case schemas.RerankRequest:
		var wire openai.OpenAIRerankResponse
		if err := schemas.Unmarshal(body, &wire); err != nil {
			return nil, errors.New("SageMaker returned an invalid rerank response")
		}
		if len(wire.Results) == 0 {
			return nil, errors.New("SageMaker rerank response must include results")
		}
		returnDocuments := req.RerankRequest.Params != nil &&
			req.RerankRequest.Params.ReturnDocuments != nil &&
			*req.RerankRequest.Params.ReturnDocuments
		response := wire.ToBifrostRerankResponse(req.RerankRequest.Documents, returnDocuments)
		response.Model = req.RerankRequest.Model
		seenIndexes := make([]bool, len(req.RerankRequest.Documents))
		for _, result := range response.Results {
			if result.Index < 0 || result.Index >= len(req.RerankRequest.Documents) {
				return nil, errors.New("SageMaker rerank response contains an out-of-range index")
			}
			if seenIndexes[result.Index] {
				return nil, errors.New("SageMaker rerank response contains a duplicate index")
			}
			seenIndexes[result.Index] = true
		}
		return &schemas.BifrostResponse{RerankResponse: response}, nil
	default:
		return nil, errors.New("unsupported SageMaker response type")
	}
}

func validateEmbeddingResponse(request *schemas.BifrostEmbeddingRequest, response *schemas.BifrostEmbeddingResponse) error {
	expectedItems := embeddingInputCount(request.Input)
	if expectedItems == 0 {
		return errors.New("embedding request input is empty")
	}
	if len(response.Data) != expectedItems {
		return fmt.Errorf("SageMaker returned %d embeddings for %d inputs", len(response.Data), expectedItems)
	}

	seenIndexes := make([]bool, expectedItems)
	for _, item := range response.Data {
		if item.Index < 0 || item.Index >= expectedItems {
			return errors.New("SageMaker embedding response contains an out-of-range index")
		}
		if seenIndexes[item.Index] {
			return errors.New("SageMaker embedding response contains a duplicate index")
		}
		seenIndexes[item.Index] = true

		vector := item.Embedding
		if vector.EmbeddingStr == nil &&
			len(vector.EmbeddingArray) == 0 &&
			len(vector.Embedding2DArray) == 0 &&
			len(vector.EmbeddingInt8Array) == 0 &&
			len(vector.EmbeddingInt32Array) == 0 {
			return errors.New("SageMaker returned an empty embedding")
		}
		if request.Params != nil &&
			request.Params.Dimensions != nil &&
			len(vector.EmbeddingArray) > 0 &&
			len(vector.EmbeddingArray) != *request.Params.Dimensions {
			return fmt.Errorf("SageMaker returned a %d-dimensional vector; expected %d", len(vector.EmbeddingArray), *request.Params.Dimensions)
		}
	}
	return nil
}

func embeddingInputCount(input *schemas.EmbeddingInput) int {
	if input == nil {
		return 0
	}
	switch {
	case input.Text != nil:
		return 1
	case input.Texts != nil:
		return len(input.Texts)
	case input.Embedding != nil:
		return 1
	case input.Embeddings != nil:
		return len(input.Embeddings)
	default:
		return 0
	}
}

func validateCustomAttributes(value string) error {
	if len(value) > 1024 {
		return errors.New("must not exceed 1024 bytes")
	}
	for _, character := range []byte(value) {
		if character < 0x20 || character > 0x7e {
			return errors.New("must contain only visible US-ASCII characters")
		}
	}
	return nil
}

func setOptionalInvokeFields(input *sagemakerruntime.InvokeEndpointInput, endpoint endpointConfig) {
	if endpoint.TargetModel != "" {
		input.TargetModel = aws.String(endpoint.TargetModel)
	}
	if endpoint.TargetVariant != "" {
		input.TargetVariant = aws.String(endpoint.TargetVariant)
	}
	if endpoint.InferenceComponentName != "" {
		input.InferenceComponentName = aws.String(endpoint.InferenceComponentName)
	}
	if endpoint.CustomAttributes != "" {
		input.CustomAttributes = aws.String(endpoint.CustomAttributes)
	}
}

func invalidRequestError(message string) *schemas.BifrostError {
	status := 400
	errorType := "invalid_request_error"
	code := "unsupported_request"
	allowFallbacks := false
	return &schemas.BifrostError{
		StatusCode:     &status,
		Type:           &errorType,
		AllowFallbacks: &allowFallbacks,
		Error: &schemas.ErrorField{
			Type:    &errorType,
			Code:    &code,
			Message: message,
		},
	}
}

func failure(status int, code, message string) *schemas.LLMPluginShortCircuit {
	errorType := "invalid_request_error"
	allowFallbacks := false
	if status >= 500 {
		errorType = "upstream_error"
		allowFallbacks = true
	}

	return &schemas.LLMPluginShortCircuit{
		Error: &schemas.BifrostError{
			IsBifrostError: code == "plugin_not_initialized",
			StatusCode:     &status,
			Type:           &errorType,
			AllowFallbacks: &allowFallbacks,
			Error: &schemas.ErrorField{
				Type:    &errorType,
				Code:    &code,
				Message: message,
			},
		},
	}
}
