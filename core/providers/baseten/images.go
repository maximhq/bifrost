package baseten

import (
	"net/http"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// basetenPredictSuffix marks a deployment_url copied from the dashboard as the
// deployment's /predict endpoint rather than its /sync/v1 base.
const basetenPredictSuffix = "/predict"

// isPredictURL reports whether a trimmed deployment URL is a /predict endpoint.
func isPredictURL(u string) bool {
	return strings.HasSuffix(u, basetenPredictSuffix)
}

// basetenImageResponse holds the two image bodies a deployment returns:
// FLUX.1 and other library Trusses answer {"data": "<base64>"}, and recipes whose
// predict endpoint maps to /v1/images/generations answer OpenAI's
// {"data": [{"b64_json": ...}]}.
type basetenImageResponse struct {
	Created int64 `json:"created"`
	Data    any   `json:"data"`
}

// toBifrostImageResponse reads either body into Bifrost's image response.
func toBifrostImageResponse(resp *basetenImageResponse, model string) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	out := &schemas.BifrostImageGenerationResponse{Created: resp.Created, Model: model}
	if out.Created == 0 {
		out.Created = time.Now().Unix()
	}
	add := func(image schemas.ImageData) {
		if image.B64JSON != "" || image.URL != "" {
			image.Index = len(out.Data)
			out.Data = append(out.Data, image)
		}
	}
	switch v := resp.Data.(type) {
	case string:
		add(schemas.ImageData{B64JSON: v})
	case []any:
		for _, item := range v {
			switch entry := item.(type) {
			case string:
				add(schemas.ImageData{B64JSON: entry})
			case map[string]any:
				b64, _ := entry["b64_json"].(string)
				url, _ := entry["url"].(string)
				revised, _ := entry["revised_prompt"].(string)
				add(schemas.ImageData{B64JSON: b64, URL: url, RevisedPrompt: revised})
			}
		}
	}
	if len(out.Data) == 0 {
		return nil, providerUtils.NewBifrostOperationError("baseten image response carries no image in data; the deployment may not be a text-to-image model", nil)
	}
	return out, nil
}

// ToBasetenImageRequest builds OpenAI's image body for a dedicated deployment. When
// the caller sets no response_format it asks for b64_json: the deployments return
// base64 only and reject OpenAI's url default. The request is not modified.
func ToBasetenImageRequest(request *schemas.BifrostImageGenerationRequest) *openai.OpenAIImageGenerationRequest {
	body := openai.ToOpenAIImageGenerationRequest(request)
	if body != nil && body.ResponseFormat == nil {
		body.ResponseFormat = schemas.Ptr("b64_json")
	}
	return body
}

// parseBasetenImageError reads a deployment's error body. Besides OpenAI's
// error.message and Baseten's string error, the image servers put the message in
// error.detail or a top-level FastAPI detail.
func parseBasetenImageError(resp *fasthttp.Response) *schemas.BifrostError {
	bifrostErr := parseBasetenDecisionError(resp)
	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil || bifrostErr.Error == nil {
		return bifrostErr
	}
	for _, path := range []string{"error.detail", "detail"} {
		if detail := gjson.GetBytes(body, path); detail.Exists() {
			message := detail.String()
			if detail.Type != gjson.String {
				message = detail.Raw
			}
			if strings.TrimSpace(message) != "" {
				bifrostErr.Error.Message = message
				break
			}
		}
	}
	return bifrostErr
}

// imageGeneration sends OpenAI's image body to url, either a deployment's
// /sync/v1/images/generations or its /predict URL as given. Recipes that serve
// OpenAI images map /predict to that route, and FLUX.1-style Trusses read prompt,
// seed and num_inference_steps from the same body and ignore the rest, so one
// request shape serves both. The reply is read in either shape.
func (provider *BasetenProvider) imageGeneration(ctx *schemas.BifrostContext, key schemas.Key, url string, request *schemas.BifrostImageGenerationRequest) (*schemas.BifrostImageGenerationResponse, *schemas.BifrostError) {
	sendBackRawRequest := providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest)
	sendBackRawResponse := providerUtils.ShouldSendBackRawResponse(ctx, provider.sendBackRawResponse)

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (*openai.OpenAIImageGenerationRequest, error) {
		return ToBasetenImageRequest(request), nil
	})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	for k, v := range openai.BearerAuthHeader(key) {
		req.Header.Set(k, v)
	}
	req.SetBody(jsonData)

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}
	providerResponseHeaders := providerUtils.ExtractProviderResponseHeaders(resp)
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerResponseHeaders)

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseBasetenImageError(resp), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}

	respBody, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, nil, sendBackRawRequest, sendBackRawResponse, latency)
	}
	var imageResp basetenImageResponse
	rawRequest, rawResponse, bifrostErr := providerUtils.HandleProviderResponseCtx(ctx, respBody, &imageResp, jsonData, sendBackRawRequest, sendBackRawResponse)
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, respBody, sendBackRawRequest, sendBackRawResponse, latency)
	}
	bifrostResp, bifrostErr := toBifrostImageResponse(&imageResp, request.Model)
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
	return bifrostResp, nil
}
