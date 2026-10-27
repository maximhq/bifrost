package vllm

import (
	"net/http"
	"strconv"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// vllmDecisionPath is where vLLM serves decision models in TypeSafe's System One shape.
const vllmDecisionPath = "/v1/systemone"

func ToVLLMDecisionRequest(request *schemas.BifrostDecisionRequest) (*VLLMDecisionRequest, error) {
	if request.Input.HasAssetParts() {
		return nil, providerUtils.InvalidRequestErrorf("decision input carries an image sent by reference, which this provider cannot read; route the request to a provider that accepts it")
	}
	if request.Input.IsEmpty() {
		return nil, providerUtils.InvalidRequestErrorf("decision input is required")
	}
	input, images, err := providerUtils.SplitDecisionImages(request.Input)
	if err != nil {
		return nil, err
	}
	textOnly := *request
	textOnly.Input = input
	native, err := typesafe.ToTypesafeDecisionRequest(&textOnly)
	if err != nil {
		return nil, err
	}
	distinctScoreLevels(native.Questions, request.Questions)
	return &VLLMDecisionRequest{TypesafeDecisionRequest: native, Images: images}, nil
}

// distinctScoreLevels gives a level sent without a description its label, or its
// index, since vLLM rejects a score question whose level descriptions repeat.
func distinctScoreLevels(native map[string]typesafe.TypesafeQuestion, questions []schemas.DecisionQuestion) {
	names, err := schemas.DecisionQuestionNames(questions)
	if err != nil {
		return
	}
	for i, question := range questions {
		if question.Type != schemas.DecisionTypeScore {
			continue
		}
		levels, ok := native[names[i]].Criteria.([]any)
		if !ok {
			continue
		}
		for j := range levels {
			if levels[j] != nil {
				continue
			}
			if j < len(question.Levels) && question.Levels[j].Label != "" {
				levels[j] = question.Levels[j].Label
			} else {
				levels[j] = strconv.Itoa(j)
			}
		}
	}
}

// parseVLLMDecisionError builds the error from a vLLM error reply. Validation and
// unknown-model errors are OpenAI-shaped; a missing route is {"detail":"Not Found"}
// and a missing key is {"error":"Unauthorized"}, which the OpenAI parser cannot read.
func parseVLLMDecisionError(resp *fasthttp.Response) *schemas.BifrostError {
	bifrostErr := openai.ParseOpenAIError(resp)
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return bifrostErr
	}
	if message := decisionErrorString(body); message != "" {
		if bifrostErr.Error == nil {
			bifrostErr.Error = &schemas.ErrorField{}
		}
		bifrostErr.Error.Message = message
	}
	return bifrostErr
}

// decisionErrorString reads a plain-string error or detail out of an engine's error body.
func decisionErrorString(body []byte) string {
	for _, key := range []string{"error", "detail"} {
		if value := gjson.GetBytes(body, key); value.Type == gjson.String && value.String() != "" {
			return value.String()
		}
	}
	return ""
}

// Decision answers a decision request on vLLM's /v1/systemone. Every model is sent;
// one vLLM does not serve, or cannot read decisions from, comes back as its own error.
func (provider *VLLMProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	baseURL, bifrostErr := provider.baseURLOrError(key)
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToVLLMDecisionRequest(request)
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
	req.SetRequestURI(baseURL + providerUtils.GetPathFromContext(ctx, vllmDecisionPath))
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	for name, value := range openai.BearerAuthHeader(key) {
		req.Header.Set(name, value)
	}
	if !providerUtils.ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, schemas.VLLM) {
		req.SetBody(jsonData)
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseVLLMDecisionError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	respBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		rawErrBody := append([]byte(nil), resp.Body()...)
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, rawErrBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	var decisionResp typesafe.TypesafeDecisionResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, respBody, &decisionResp, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, respBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResp, bifrostErr := typesafe.ToBifrostDecisionResponse(&decisionResp, request)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, respBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResp.ID = gjson.GetBytes(respBody, "id").String()
	bifrostResp.ExtraFields.Latency = latency.Milliseconds()
	bifrostResp.ExtraFields.ProviderResponseHeaders = providerResponseHeaders
	if sendBackRawRequest {
		bifrostResp.ExtraFields.RawRequest = rawRequest
	}
	if sendBackRawResponse {
		bifrostResp.ExtraFields.RawResponse = rawResponse
	}
	return bifrostResp, nil
}