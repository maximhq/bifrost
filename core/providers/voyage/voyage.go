// Package voyage implements the Voyage AI provider: text, contextualized and multimodal
// embeddings, and reranking. Voyage serves no chat or other operations; those return the
// unsupported-operation error.
//
// Voyage publishes official Python and TypeScript SDKs only, so this Go provider talks to the
// REST API directly, like every other provider in this repository. Request and response shapes
// follow the API reference at https://docs.voyageai.com/reference.
package voyage

import (
	"net/http"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

const (
	defaultBaseURL = "https://api.voyageai.com"

	embeddingsPath           = "/v1/embeddings"
	contextualizedPath       = "/v1/contextualizedembeddings"
	multimodalEmbeddingsPath = "/v1/multimodalembeddings"
	rerankPath               = "/v1/rerank"
)

// VoyageProvider implements the Provider interface for Voyage AI.
type VoyageProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for API requests
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawRequest   bool                          // Whether to include raw request in BifrostResponse
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
}

// NewVoyageProvider creates a new Voyage provider instance.
func NewVoyageProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*VoyageProvider, error) {
	config.CheckAndSetDefaults()

	requestTimeout := time.Second * time.Duration(config.NetworkConfig.DefaultRequestTimeoutInSeconds)
	client := &fasthttp.Client{
		ReadTimeout:         requestTimeout,
		WriteTimeout:        requestTimeout,
		MaxConnsPerHost:     config.NetworkConfig.MaxConnsPerHost,
		MaxIdleConnDuration: time.Second * time.Duration(config.NetworkConfig.KeepAliveTimeoutInSeconds),
		MaxConnWaitTimeout:  requestTimeout,
		MaxConnDuration:     time.Second * time.Duration(schemas.DefaultMaxConnDurationInSeconds),
		ConnPoolStrategy:    fasthttp.FIFO,
	}

	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client, config.NetworkConfig.AllowPrivateNetwork)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)

	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = defaultBaseURL
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &VoyageProvider{
		logger:               logger,
		client:               client,
		networkConfig:        config.NetworkConfig,
		customProviderConfig: config.CustomProviderConfig,
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Voyage.
func (provider *VoyageProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.Voyage, provider.customProviderConfig)
}

func (provider *VoyageProvider) buildRequestURL(ctx *schemas.BifrostContext, defaultPath string, requestType schemas.RequestType) string {
	path, isCompleteURL := providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)
	if isCompleteURL {
		return path
	}
	return provider.networkConfig.BaseURL + path
}

// completeRequest posts jsonData to url with the key as a bearer token and returns the response
// body. A non-200 reply is parsed as a Voyage error.
func (provider *VoyageProvider) completeRequest(ctx *schemas.BifrostContext, jsonData []byte, url string, key string) ([]byte, time.Duration, map[string]string, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	respOwned := true
	defer func() {
		if respOwned {
			fasthttp.ReleaseResponse(resp)
		}
	}()

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	usedLargePayloadBody := providerUtils.ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, schemas.Voyage)
	if !usedLargePayloadBody {
		req.SetBody(jsonData)
	}

	activeClient := providerUtils.PrepareResponseStreaming(ctx, provider.client, resp)
	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, activeClient, req, resp)
	defer wait()
	if usedLargePayloadBody {
		providerUtils.DrainLargePayloadRemainder(ctx)
	}
	if bifrostErr != nil {
		return nil, latency, nil, bifrostErr
	}

	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)

	if resp.StatusCode() != fasthttp.StatusOK {
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		return nil, latency, providerResponseHeaders, providerUtils.SetErrorLatency(parseVoyageError(resp), latency)
	}

	body, isLargeResp, decodeErr := providerUtils.FinalizeResponseWithLargeDetection(ctx, resp, provider.logger)
	if decodeErr != nil {
		return nil, latency, providerResponseHeaders, decodeErr
	}
	if isLargeResp {
		respOwned = false
		return nil, latency, providerResponseHeaders, nil
	}
	return body, latency, providerResponseHeaders, nil
}

// Embedding generates embeddings with a Voyage text, contextualized or multimodal embedding
// model; the model decides which endpoint serves the request.
func (provider *VoyageProvider) Embedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Voyage, provider.customProviderConfig, schemas.EmbeddingRequest); err != nil {
		return nil, err
	}
	name, model, err := resolveEmbeddingModel(request)
	if err != nil {
		return nil, badRequest(err)
	}

	switch model.endpoint {
	case endpointContextualized:
		return provider.contextualizedEmbedding(ctx, key, request, name, model)
	case endpointMultimodal:
		return provider.multimodalEmbedding(ctx, key, request, name, model)
	default:
		return provider.textEmbedding(ctx, key, request, name, model)
	}
}

func (provider *VoyageProvider) textEmbedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest, name string, model voyageModel) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (*VoyageEmbeddingRequest, error) {
		return ToVoyageEmbeddingRequest(request)
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	opts, err := readEmbeddingOptions(name, model, request.Params)
	if err != nil {
		return nil, badRequest(err)
	}
	return doVoyage(provider, ctx, key, jsonBody, request.Model, embeddingsPath, schemas.EmbeddingRequest,
		func(response *VoyageEmbeddingResponse) (*schemas.BifrostEmbeddingResponse, error) {
			return response.ToBifrostEmbeddingResponse(request, opts)
		},
		func(latency time.Duration, headers map[string]string) *schemas.BifrostEmbeddingResponse {
			return &schemas.BifrostEmbeddingResponse{Model: request.Model, ExtraFields: largeResponseFields(latency, headers)}
		},
		func(r *schemas.BifrostEmbeddingResponse) *schemas.BifrostResponseExtraFields { return &r.ExtraFields })
}

func (provider *VoyageProvider) contextualizedEmbedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest, name string, model voyageModel) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (*VoyageContextualizedRequest, error) {
		return ToVoyageContextualizedRequest(request)
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	opts, err := readEmbeddingOptions(name, model, request.Params)
	if err != nil {
		return nil, badRequest(err)
	}
	inputType := inputTypeDocument
	if opts.inputType != nil {
		inputType = *opts.inputType
	}
	return doVoyage(provider, ctx, key, jsonBody, request.Model, contextualizedPath, schemas.EmbeddingRequest,
		func(response *VoyageContextualizedResponse) (*schemas.BifrostEmbeddingResponse, error) {
			return response.ToBifrostEmbeddingResponse(request, inputType)
		},
		func(latency time.Duration, headers map[string]string) *schemas.BifrostEmbeddingResponse {
			return &schemas.BifrostEmbeddingResponse{Model: request.Model, ExtraFields: largeResponseFields(latency, headers)}
		},
		func(r *schemas.BifrostEmbeddingResponse) *schemas.BifrostResponseExtraFields { return &r.ExtraFields })
}

func (provider *VoyageProvider) multimodalEmbedding(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostEmbeddingRequest, name string, model voyageModel) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (*VoyageMultimodalRequest, error) {
		return ToVoyageMultimodalRequest(request)
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	opts, err := readEmbeddingOptions(name, model, request.Params)
	if err != nil {
		return nil, badRequest(err)
	}
	return doVoyage(provider, ctx, key, jsonBody, request.Model, multimodalEmbeddingsPath, schemas.EmbeddingRequest,
		func(response *VoyageMultimodalResponse) (*schemas.BifrostEmbeddingResponse, error) {
			return response.ToBifrostEmbeddingResponse(request, opts)
		},
		func(latency time.Duration, headers map[string]string) *schemas.BifrostEmbeddingResponse {
			return &schemas.BifrostEmbeddingResponse{Model: request.Model, ExtraFields: largeResponseFields(latency, headers)}
		},
		func(r *schemas.BifrostEmbeddingResponse) *schemas.BifrostResponseExtraFields { return &r.ExtraFields })
}

// Rerank scores documents against a query with a Voyage reranker.
func (provider *VoyageProvider) Rerank(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostRerankRequest) (*schemas.BifrostRerankResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Voyage, provider.customProviderConfig, schemas.RerankRequest); err != nil {
		return nil, err
	}
	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (*VoyageRerankRequest, error) {
		return ToVoyageRerankRequest(request)
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	return doVoyage(provider, ctx, key, jsonBody, request.Model, rerankPath, schemas.RerankRequest,
		func(response *VoyageRerankResponse) (*schemas.BifrostRerankResponse, error) {
			return response.ToBifrostRerankResponse(request)
		},
		func(latency time.Duration, headers map[string]string) *schemas.BifrostRerankResponse {
			return &schemas.BifrostRerankResponse{Model: request.Model, ExtraFields: largeResponseFields(latency, headers)}
		},
		func(r *schemas.BifrostRerankResponse) *schemas.BifrostResponseExtraFields { return &r.ExtraFields })
}

func largeResponseFields(latency time.Duration, headers map[string]string) schemas.BifrostResponseExtraFields {
	return schemas.BifrostResponseExtraFields{Latency: latency.Milliseconds(), ProviderResponseHeaders: headers}
}

// doVoyage sends one request and converts the reply. It is the part of every Voyage operation
// that does not depend on the endpoint: the HTTP exchange, error enrichment, the large-response
// short cut, raw request/response capture and latency.
func doVoyage[W any, B any](
	provider *VoyageProvider,
	ctx *schemas.BifrostContext,
	key schemas.Key,
	jsonBody []byte,
	model string,
	path string,
	requestType schemas.RequestType,
	convert func(*W) (*B, error),
	largeResponse func(time.Duration, map[string]string) *B,
	extraFields func(*B) *schemas.BifrostResponseExtraFields,
) (*B, *schemas.BifrostError) {
	responseBody, latency, providerResponseHeaders, err := provider.completeRequest(ctx, jsonBody, provider.buildRequestURL(ctx, path, requestType), key.Value.GetValue())
	if providerResponseHeaders != nil {
		ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)
	}
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, err, jsonBody, nil, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	if isLargeResp, _ := ctx.Value(schemas.BifrostContextKeyLargeResponseMode).(bool); isLargeResp {
		return largeResponse(latency, providerResponseHeaders), nil
	}

	var response W
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, responseBody, &response, jsonBody, providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest), providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse))
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	convTracer, convHandle := providerUtils.StartResponseConvertorSpan(ctx)
	bifrostResponse, convErr := convert(&response)
	if convTracer != nil {
		status, message := schemas.SpanStatusOk, ""
		if convErr != nil {
			status, message = schemas.SpanStatusError, convErr.Error()
		}
		convTracer.EndSpan(convHandle, status, message)
	}
	if convErr != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(convErr.Error(), nil), jsonBody, responseBody, provider.sendBackRawRequest, provider.sendBackRawResponse, latency)
	}

	fields := extraFields(bifrostResponse)
	fields.Latency = latency.Milliseconds()
	fields.ProviderResponseHeaders = providerResponseHeaders
	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		fields.RawRequest = rawRequest
	}
	if providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse) {
		fields.RawResponse = rawResponse
	}
	return bifrostResponse, nil
}

// badRequest turns a converter error into the 400 the caller should see.
func badRequest(err error) *schemas.BifrostError {
	if bifrostErr, ok := providerUtils.AsBifrostBadRequestError(err); ok {
		return bifrostErr
	}
	return providerUtils.NewBifrostOperationError(schemas.ErrRequestBodyConversion, err)
}
