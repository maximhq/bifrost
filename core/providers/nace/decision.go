package nace

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ToNaceDecisionRequest reuses System One question and state conversion.
func ToNaceDecisionRequest(request *schemas.BifrostDecisionRequest) (*NaceDecisionRequest, error) {
	if request == nil {
		return nil, providerUtils.InvalidRequestErrorf("decision request is required")
	}
	input, images, err := splitImages(request.Input)
	if err != nil {
		return nil, err
	}
	if len(images) > 0 && !readsImages(request.Model) {
		return nil, providerUtils.InvalidRequestErrorf("images require a drex-max model; %q is text-only", request.Model)
	}
	textOnly := *request
	textOnly.Input = input
	native, err := typesafe.ToTypesafeDecisionRequest(&textOnly)
	if err != nil {
		return nil, err
	}
	if len(native.Assets) > 0 {
		return nil, providerUtils.InvalidRequestErrorf("nace requires top-level images rather than asset references")
	}
	// Drex reads strings where System One also permits objects and arrays.
	for name, question := range native.Questions {
		question.Instructions, err = naceDecisionText(question.Instructions)
		if err != nil {
			return nil, err
		}
		switch criteria := question.Criteria.(type) {
		case map[string]any:
			for option, description := range criteria {
				criteria[option], err = naceDecisionText(description)
				if err != nil {
					return nil, err
				}
			}
		case []any:
			for i, description := range criteria {
				criteria[i], err = naceDecisionText(description)
				if err != nil {
					return nil, err
				}
				// Score levels must be strings; a level without a description keeps its index.
				if criteria[i] == nil {
					criteria[i] = strconv.Itoa(i)
				}
			}
		}
		native.Questions[name] = question
	}
	for _, question := range request.Questions {
		if question.Type == schemas.DecisionTypeBox {
			return nil, providerUtils.InvalidRequestErrorf("nace does not support box questions")
		}
	}
	return &NaceDecisionRequest{TypesafeDecisionRequest: native, Images: images}, nil
}

// naceDecisionText renders structured descriptions losslessly as readable, sorted JSON.
// Plain strings and null descriptions keep their native representation.
func naceDecisionText(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	if _, ok := value.(string); ok {
		return value, nil
	}
	data, err := providerUtils.MarshalSortedIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	if bytes.Equal(data, []byte("null")) {
		return nil, nil
	}
	return string(data), nil
}

// ToBifrostDecisionResponse adapts only Drex's array legend before using the shared converter.
func ToBifrostDecisionResponse(native *NaceDecisionResponse, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	shared := &typesafe.TypesafeDecisionResponse{Model: native.Model, Answers: make(map[string]typesafe.TypesafeAnswer, len(native.Answers))}
	if native.Usage != nil {
		shared.Usage = &native.Usage.TypesafeUsage
	}
	for name, answer := range native.Answers {
		converted := answer.TypesafeAnswer
		if answer.Legend != nil {
			converted.Legend = make(map[string]any, len(answer.Legend))
			for i, label := range answer.Legend {
				converted.Legend[strconv.Itoa(i)] = label
			}
		}
		shared.Answers[name] = converted
	}
	response, err := typesafe.ToBifrostDecisionResponse(shared, request)
	if err == nil {
		response.ID = native.RequestID
		if native.Usage != nil && native.Usage.ImageTokens > 0 {
			response.Usage.PromptTokensDetails = &schemas.ChatPromptTokensDetails{ImageTokens: native.Usage.ImageTokens}
		}
	}
	return response, err
}

// Decision calls the native System One endpoint. Retry policy belongs to Bifrost.
func (provider *NaceProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	if err := providerUtils.CheckOperationAllowed(schemas.Nace, provider.customProviderConfig, schemas.DecisionRequest); err != nil {
		return nil, err
	}
	rawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	rawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)
	// Predicate and choice replies have no wire differences: use the shared handler.
	needsNative := false
	if request != nil {
		for _, q := range request.Questions {
			if q.Type == schemas.DecisionTypeScore {
				needsNative = true
				break
			}
		}
	}
	var response *schemas.BifrostDecisionResponse
	var err *schemas.BifrostError
	if request != nil && request.Input.NonTextPartType() == schemas.DecisionInputPartTypeImage {
		needsNative = true
	}
	if !needsNative {
		headers := map[string]string{"Authorization": "Bearer " + key.Value.GetValue(), "X-Nace-Client": provider.clientID, "User-Agent": provider.clientID}
		response, err = typesafe.HandleTypesafeDecisionRequest(ctx, provider.client, provider.buildRequestURL(ctx, "/v1/systemone", schemas.DecisionRequest), request, func(r *schemas.BifrostDecisionRequest) (*typesafe.TypesafeDecisionRequest, error) {
			native, err := ToNaceDecisionRequest(r)
			if err != nil {
				return nil, err
			}
			return native.TypesafeDecisionRequest, nil
		}, headers, provider.networkConfig.ExtraHeaders, provider.GetProviderKey(), parseNaceError, rawRequest, rawResponse, false)
	} else {
		response, err = provider.nativeDecision(ctx, key, request, rawRequest, rawResponse)
	}
	if response != nil {
		for name, value := range response.ExtraFields.ProviderResponseHeaders {
			if response.ID == "" && strings.EqualFold(name, "x-request-id") {
				response.ID = value
			}
		}
	}
	return response, err
}

// nativeDecision handles Drex images and its distinct score decoder.
func (provider *NaceProvider) nativeDecision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest, rawRequest, rawResponse bool) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	body, err := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (*NaceDecisionRequest, error) { return ToNaceDecisionRequest(request) })
	if err != nil {
		return nil, err
	}
	req, resp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	provider.setHeaders(ctx, req, key)
	req.SetRequestURI(provider.buildRequestURL(ctx, "/v1/systemone", schemas.DecisionRequest))
	req.Header.SetMethod("POST")
	req.Header.SetContentType("application/json")
	if !providerUtils.ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, provider.GetProviderKey()) {
		req.SetBody(body)
	}
	latency, err, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	enrich := func(err *schemas.BifrostError) *schemas.BifrostError {
		var errorBody []byte
		if rawResponse {
			// Invalid JSON is retained as-is by EnrichError; detach it from the pool.
			errorBody = bytes.Clone(resp.Body())
		}
		return providerUtils.EnrichError(ctx, err, body, errorBody, rawRequest, rawResponse, latency)
	}
	if err != nil {
		return nil, enrich(err)
	}
	headers := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, headers)
	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, enrich(parseNaceError(resp))
	}
	decoded, decodeErr := providerUtils.CheckAndDecodeBody(resp)
	if decodeErr != nil {
		return nil, enrich(providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, decodeErr))
	}
	var native NaceDecisionResponse
	rawReq, rawResp, err := providerUtils.HandleProviderResponseCtx(ctx, decoded, &native, body, rawRequest, rawResponse)
	if err != nil {
		return nil, enrich(err)
	}
	response, err := ToBifrostDecisionResponse(&native, request)
	if err != nil {
		return nil, enrich(err)
	}
	response.ExtraFields.Latency = latency.Milliseconds()
	response.ExtraFields.ProviderResponseHeaders = headers
	response.ExtraFields.RawRequest, response.ExtraFields.RawResponse = rawReq, rawResp
	return response, nil
}
