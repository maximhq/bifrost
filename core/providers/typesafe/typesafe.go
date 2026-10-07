// Package typesafe implements the Typesafe provider for Bifrost.
// Typesafe serves judgment models (the jev System One family) through a single
// synchronous evaluation endpoint, POST /v1/systemone. Bifrost exposes it via
// the shared decision operation; every other operation returns unsupported.
package typesafe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TypesafeProvider implements the Provider interface for Typesafe's API.
type TypesafeProvider struct {
	logger               schemas.Logger                // Logger for provider operations
	client               *fasthttp.Client              // HTTP client for unary API requests (ReadTimeout bounds overall response)
	streamingClient      *fasthttp.Client              // HTTP client for streaming API requests (no ReadTimeout; unused today, kept per provider pattern)
	networkConfig        schemas.NetworkConfig         // Network configuration including extra headers
	sendBackRawRequest   bool                          // Whether to include raw request in BifrostResponse
	sendBackRawResponse  bool                          // Whether to include raw response in BifrostResponse
	defaultBaseURL       bool                          // No base_url override: the built-in jev catalog applies alongside the live listing
	customProviderConfig *schemas.CustomProviderConfig // Custom provider config
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

	defaultBaseURL := config.NetworkConfig.BaseURL == ""
	if defaultBaseURL {
		config.NetworkConfig.BaseURL = typesafeDefaultBaseURL
	}
	config.NetworkConfig.BaseURL = strings.TrimRight(config.NetworkConfig.BaseURL, "/")

	return &TypesafeProvider{
		logger:               logger,
		client:               client,
		streamingClient:      streamingClient,
		networkConfig:        config.NetworkConfig,
		sendBackRawRequest:   config.SendBackRawRequest,
		sendBackRawResponse:  config.SendBackRawResponse,
		defaultBaseURL:       defaultBaseURL,
		customProviderConfig: config.CustomProviderConfig,
	}, nil
}

// GetProviderKey returns the provider identifier for Typesafe, or the custom
// provider name when Typesafe backs a custom provider.
func (provider *TypesafeProvider) GetProviderKey() schemas.ModelProvider {
	return providerUtils.GetProviderName(schemas.Typesafe, provider.customProviderConfig)
}

// buildRequestURL resolves the request URL, honouring a context path and the
// custom provider's request_path_overrides (a path or an absolute URL).
func (provider *TypesafeProvider) buildRequestURL(ctx *schemas.BifrostContext, defaultPath string, requestType schemas.RequestType) string {
	path, isCompleteURL := providerUtils.GetRequestPath(ctx, defaultPath, provider.customProviderConfig, requestType)
	if isCompleteURL {
		return path
	}
	return provider.networkConfig.BaseURL + path
}

// ListModels serves the endpoint's native GET /v1/models catalog. Against
// api.typesafe.ai the live listing carries the aliases only, so the pinned
// versioned entries are merged in (versioned IDs are accepted whether or not
// listed) and the pinned catalog stands in when the call fails. A custom
// base_url is a different endpoint: its catalog is served as-is and a failure
// is reported rather than answered with jev models it may not serve.
func (provider *TypesafeProvider) ListModels(ctx *schemas.BifrostContext, keys []schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Typesafe, provider.customProviderConfig, schemas.ListModelsRequest); err != nil {
		return nil, err
	}
	startTime := time.Now()

	var response *schemas.BifrostListModelsResponse
	var err *schemas.BifrostError
	if provider.customProviderConfig != nil && provider.customProviderConfig.IsKeyLess {
		response, err = providerUtils.HandleKeylessListModelsRequest(provider.GetProviderKey(), func() (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
			return provider.listModelsByKey(ctx, schemas.Key{Models: schemas.WhiteList{"*"}}, request)
		})
	} else {
		response, err = providerUtils.HandleMultipleListModelsRequests(ctx, keys, request, provider.listModelsByKey)
	}
	if err != nil {
		return nil, err
	}

	response.ExtraFields.Latency = time.Since(startTime).Milliseconds()
	return response, nil
}

// listModelsByKey fetches the native catalog for one key and filters it
// through the standard list-models pipeline so key whitelists, blacklists,
// and aliases apply.
func (provider *TypesafeProvider) listModelsByKey(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
	response := &schemas.BifrostListModelsResponse{Data: []schemas.Model{}}

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

	catalog, bifrostErr := provider.fetchNativeCatalog(ctx, key)
	if bifrostErr != nil {
		if !provider.defaultBaseURL {
			return nil, bifrostErr
		}
		provider.logger.Warn(fmt.Sprintf("typesafe: live model listing failed, serving the pinned jev catalog: %s", bifrostErr.Error.Message))
		catalog = nil
	}
	if provider.defaultBaseURL {
		catalog = mergePinnedCatalog(catalog)
	}

	included := make(map[string]bool)
	for _, model := range catalog {
		for _, result := range pipeline.FilterModel(model.Name) {
			name := model.Name
			description := model.Description
			entry := schemas.Model{
				ID:          string(provider.GetProviderKey()) + "/" + result.ResolvedID,
				Name:        &name,
				Description: &description,
				OwnedBy:     new("typesafe"),
			}
			if extra, err := providerUtils.MarshalSorted(model); err == nil {
				entry.ProviderExtra = extra
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

// fetchNativeCatalog performs GET /v1/models against the configured endpoint.
func (provider *TypesafeProvider) fetchNativeCatalog(ctx *schemas.BifrostContext, key schemas.Key) ([]TypesafeNativeModel, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(provider.buildRequestURL(ctx, typesafeModelsPath, schemas.ListModelsRequest))
	req.Header.SetMethod(http.MethodGet)
	req.Header.SetContentType("application/json")
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.SetErrorLatency(parseTypesafeError(resp), latency)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err)
	}
	var listing TypesafeNativeListModelsResponse
	if err := sonic.Unmarshal(body, &listing); err != nil {
		return nil, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseUnmarshal, err)
	}
	return listing.Models, nil
}

// mergePinnedCatalog appends the pinned versioned jev entries the live
// listing omits, keyed by name so live metadata wins.
func mergePinnedCatalog(live []TypesafeNativeModel) []TypesafeNativeModel {
	seen := make(map[string]bool, len(live))
	merged := append([]TypesafeNativeModel(nil), live...)
	for _, model := range live {
		seen[model.Name] = true
	}
	for _, model := range typesafeModels {
		if seen[model.ID] {
			continue
		}
		merged = append(merged, TypesafeNativeModel{Name: model.ID, Description: model.Description, ReleaseDate: model.ReleaseDate})
	}
	return merged
}

// Decision performs a synchronous evaluation against POST /v1/systemone.
func (provider *TypesafeProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Typesafe, provider.customProviderConfig, schemas.DecisionRequest); err != nil {
		return nil, err
	}
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
	req.SetRequestURI(provider.buildRequestURL(ctx, typesafeSystemOnePath, schemas.DecisionRequest))
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

	// Captured before the status check so error responses forward them too
	// (the SDKs read x-typesafe-request-id and Retry-After off every reply).
	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

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
	respBody, envelopeFailed := unwrapResultEnvelope(respBody)
	if envelopeFailed {
		return nil, providerUtils.EnrichError(ctx, parseTypesafeEnvelopeFailure(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
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
	bifrostResp.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}
	// The native /typesafe route relays this verbatim so answer and usage
	// metadata outside the shared shape survive.
	var compact bytes.Buffer
	if err := json.Compact(&compact, respBody); err == nil {
		bifrostResp.NativeResponse = json.RawMessage(compact.Bytes())
	}

	return bifrostResp, nil
}
