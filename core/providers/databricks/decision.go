package databricks

import (
	"net/http"

	"github.com/maximhq/bifrost/core/providers/typesafe"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// databricksDecisionPath is the AI Functions endpoint that serves ai_decide.
const databricksDecisionPath = "/api/2.0/ai-functions/ai-decide"

// databricksDecisionVersion pins the ai_decide function API version, so a change to
// Databricks' default cannot change the response shape parsed here.
const databricksDecisionVersion = "1.0"

// ToDatabricksDecisionRequest converts a normalized decision request into the ai_decide
// body. Questions go through Typesafe's converter, so they validate exactly as they do on
// Typesafe. An empty input is rejected locally: ai_decide answers a null state with a 200
// and no answers.
func ToDatabricksDecisionRequest(request *schemas.BifrostDecisionRequest) (*DatabricksDecisionRequest, error) {
	if request.Input.IsEmpty() {
		return nil, providerUtils.InvalidRequestErrorf("decision input is required")
	}
	native, err := typesafe.ToTypesafeDecisionRequest(request)
	if err != nil {
		return nil, err
	}
	return &DatabricksDecisionRequest{
		State:       native.State,
		Questions:   native.Questions,
		Options:     &DatabricksDecisionOptions{Version: databricksDecisionVersion},
		ExtraParams: native.ExtraParams,
	}, nil
}

// ToBifrostDecisionResponse unwraps the ai_decide envelope, reads each noul probability as
// Typesafe's noul, and converts the answers through Typesafe's converter. The body names
// no model, so the requested one is reported.
func ToBifrostDecisionResponse(resp *DatabricksDecisionResponse, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	if resp.Response == nil {
		return nil, providerUtils.NewBifrostOperationError("databricks returned no answers", nil)
	}
	answers := make(map[string]typesafe.TypesafeAnswer, len(resp.Response.Answers))
	for name, answer := range resp.Response.Answers {
		if answer.Noul == nil {
			answer.Noul = answer.Probability
		}
		answers[name] = answer.TypesafeAnswer
	}
	return typesafe.ToBifrostDecisionResponse(&typesafe.TypesafeDecisionResponse{Model: request.Model, Answers: answers}, request)
}

// Decision runs a decision on Databricks' ai_decide AI Function. ai_decide is a workspace
// function, not a serving endpoint, so it is the only model served natively: a datasheet
// supports_decisions row decides, and without one the ai_decide name does. Any other model
// is reported as unsupported; the compat plugin emulates it on the model's Responses API.
func (provider *DatabricksProvider) Decision(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	capModel := schemas.ResolveCanonicalModel(ctx, request.Model)
	if !schemas.ResolveModelCaps(request.Provider, capModel).SupportsDecisions(schemas.IsDatabricksDecisionModel(capModel)) {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.DecisionRequest, provider.GetProviderKey())
	}
	url, bifrostErr := provider.workspaceAPIURL(key, providerUtils.GetPathFromContext(ctx, databricksDecisionPath))
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	jsonBody, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return ToDatabricksDecisionRequest(request)
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}
	authHeaders, bifrostErr := provider.authHeader(ctx, key)
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
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	for k, v := range authHeaders {
		req.Header.Set(k, v)
	}
	// A nil body means large-payload passthrough staged the request as a
	// stream; apply it instead of sending an empty body.
	if !providerUtils.ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, provider.GetProviderKey()) {
		req.SetBody(jsonBody)
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	// Captured before the status check so error responses forward them too.
	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseDatabricksError(resp), jsonBody, nil, sendBackRawRequest, sendBackRawResponse, latency)
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
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonBody, rawErrBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	var decisionResp DatabricksDecisionResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, respBody, &decisionResp, jsonBody, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, respBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

	bifrostResp, bifrostErr := ToBifrostDecisionResponse(&decisionResp, request)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonBody, respBody, sendBackRawRequest, sendBackRawResponse, latency)
	}

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
