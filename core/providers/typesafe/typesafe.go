// Package typesafe implements the Typesafe provider for Bifrost.
// Typesafe serves judgment models (the jev System One family) through a single
// synchronous evaluation endpoint, POST /v1/systemone. Bifrost exposes it via
// the shared decision operation; every other operation returns unsupported.
package typesafe

import (
	"net/http"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TypesafeProvider implements the Provider interface for Typesafe's API.
type TypesafeProvider struct {
	logger              schemas.Logger        // Logger for provider operations
	client              *fasthttp.Client      // HTTP client for unary API requests (ReadTimeout bounds overall response)
	streamingClient     *fasthttp.Client      // HTTP client for streaming API requests (no ReadTimeout; unused today, kept per provider pattern)
	networkConfig       schemas.NetworkConfig // Network configuration including extra headers
	sendBackRawRequest  bool                  // Whether to include raw request in BifrostResponse
	sendBackRawResponse bool                  // Whether to include raw response in BifrostResponse
}

// NewTypesafeProvider creates a new Typesafe provider instance.
func NewTypesafeProvider(config *schemas.ProviderConfig, logger schemas.Logger) (*TypesafeProvider, error) {
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

	// Configure proxy if provided
	client = providerUtils.ConfigureProxy(client, config.ProxyConfig, logger)
	client = providerUtils.ConfigureDialer(client, config.NetworkConfig.AllowPrivateNetwork)
	client = providerUtils.ConfigureTLS(client, config.NetworkConfig, logger)
	streamingClient := providerUtils.BuildStreamingClient(client)

	if config.NetworkConfig.BaseURL == "" {
		config.NetworkConfig.BaseURL = typesafeDefaultBaseURL
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &TypesafeProvider{
		logger:              logger,
		client:              client,
		streamingClient:     streamingClient,
		networkConfig:       config.NetworkConfig,
		sendBackRawRequest:  config.SendBackRawRequest,
		sendBackRawResponse: config.SendBackRawResponse,
	}, nil
}

// GetProviderKey returns the provider identifier for Typesafe.
func (provider *TypesafeProvider) GetProviderKey() schemas.ModelProvider {
	return schemas.Typesafe
}

// ListModels serves the static jev catalog. Typesafe documents no model-listing
// endpoint, so no upstream call is made; the catalog is pinned in utils.go and
// mirrored in the hosted datasheet.
func (provider *TypesafeProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	startTime := time.Now()

	response, err := providerUtils.HandleMultipleListModelsRequests(ctx, keys, request, provider.listModelsByKey)
	if err != nil {
		return nil, err
	}

	response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
	return response, nil
}

// listModelsByKey filters the static catalog through the standard list-models
// pipeline so key whitelists, blacklists, and aliases apply.
func (provider *TypesafeProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	response := &schemas.BifrostListModelsResponse{
		Data: make([]schemas.Model, 0, len(typesafeModels)),
	}

	pipeline := &providerUtils.ListModelsPipeline{
		AllowedModels:     key.Models,
		BlacklistedModels: key.BlacklistedModels,
		Aliases:           key.Aliases,
		Unfiltered:        request.Unfiltered,
		ProviderKey:       provider.GetProviderKey(),
		MatchFns:          providerUtils.DefaultMatchFns(),
	}
	if pipeline.ShouldEarlyExit() {
		return response, nil
	}

	included := make(map[string]bool)
	for _, model := range typesafeModels {
		for _, result := range pipeline.FilterModel(model.ID) {
			name := model.Name
			description := model.Description
			entry := schemas.Model{
				ID:          string(provider.GetProviderKey()) + "/" + result.ResolvedID,
				Name:        &name,
				Description: &description,
				OwnedBy:     new("typesafe"),
			}
			if result.AliasValue != "" {
				alias := result.AliasValue
				entry.Alias = &alias
			}
			response.Data = append(response.Data, entry)
			included[strings.ToLower(result.ResolvedID)] = true
		}
	}
	response.Data = append(response.Data, pipeline.BackfillModels(included)...)

	return response, nil
}

// Decision performs a synchronous evaluation against POST /v1/systemone.
func (provider *TypesafeProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToTypesafeDecisionRequest(request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.networkConfig.BaseURL + providerUtils.GetPathFromContext(ctx, typesafeSystemOnePath))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}
	req.SetBody(jsonData)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseTypesafeError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	ft, fh := providerUtils.StartPhaseSpan(ctx, "response-finalize")
	respBody, err := providerUtils.CheckAndDecodeBody(resp)
	if ft != nil {
		if err != nil {
			ft.EndSpan(fh, schemas.SpanStatusError, err.Error())
		} else {
			ft.EndSpan(fh, schemas.SpanStatusOk, "")
		}
	}
	if err != nil {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, rawErrBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	var typesafeResp TypesafeDecisionResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, respBody, &typesafeResp, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	bifrostResp, bifrostErr := ToBifrostDecisionResponse(&typesafeResp, request)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, respBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}

	return bifrostResp, nil
}
