// Package openai provides the OpenAI provider implementation for the Bifrost framework.
package openai

import (
	"net/http"
	"time"

	"github.com/bytedance/sonic"
	"github.com/valyala/fasthttp"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// largePayloadResult holds the lightweight metadata extracted from a large payload passthrough.
type largePayloadResult struct {
	Usage        *schemas.BifrostLLMUsage
	Latency      int64
	ResponseBody []byte // non-nil for request types that need the raw upstream response (transcription, speech, etc.)
}

// setStreamingRequestBody sets the request body for streaming handlers.
// In normal mode it uses the marshaled jsonBody. In large payload mode it delegates to
// ApplyLargePayloadRequestBodyWithModelNormalization which streams the original request
// body to upstream with model prefix rewriting.
func setStreamingRequestBody(ctx *schemas.BifrostContext, req *fasthttp.Request, jsonBody []byte, providerName schemas.ModelProvider) {
	if !providerUtils.ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, providerName) {
		req.SetBody(jsonBody)
	}
}

// handleOpenAILargePayloadPassthrough handles a complete large payload request-response cycle
// for OpenAI-compatible providers. When large payload mode is active, it streams the request
// body to upstream and optionally streams the response back without full materialization.
//
// Returns (result, nil, true) on success, (nil, err, true) on error, or (nil, nil, false) when
// large payload mode is not active and the caller should use the normal path.
func handleOpenAILargePayloadPassthrough(
	ctx *schemas.BifrostContext,
	client *fasthttp.Client,
	url string,
	key schemas.Key,
	extraHeaders map[string]string,
	providerName schemas.ModelProvider,
	model string,
	requestType schemas.RequestType,
	logger schemas.Logger,
) (*largePayloadResult, *schemas.BifrostError, bool) {
	isLargePayload, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool)
	if !isLargePayload {
		return nil, nil, false
	}

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	// resp lifecycle: managed manually when large response streaming is active

	providerUtils.SetExtraHeaders(ctx, req, extraHeaders, nil)
	req.SetRequestURI(url)
	req.Header.SetMethod(http.MethodPost)
	if key.Value.GetValue() != "" {
		req.Header.Set("Authorization", "Bearer "+key.Value.GetValue())
	}

	// Rewrite model prefix and stream request body to upstream.
	// Sets content-type from context; falls back to JSON if not set.
	if !providerUtils.ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, providerName) {
		fasthttp.ReleaseResponse(resp)
		return nil, nil, false
	}
	if len(req.Header.ContentType()) == 0 {
		req.Header.SetContentType("application/json")
	}

	// Choose client: enable response body streaming when threshold is configured
	activeClient := providerUtils.PrepareResponseStreaming(ctx, client, resp)

	latency, bifrostErr := providerUtils.MakeRequestWithContext(ctx, activeClient, req, resp)
	if bifrostErr != nil {
		fasthttp.ReleaseResponse(resp)
		return nil, bifrostErr, true
	}

	// Error responses are always small — materialize stream body for error parsing
	if resp.StatusCode() != fasthttp.StatusOK {
		providerUtils.MaterializeStreamErrorBody(ctx, resp)
		parsedErr := ParseOpenAIError(resp, requestType, providerName, model)
		fasthttp.ReleaseResponse(resp)
		return nil, parsedErr, true
	}

	// Delegate response body handling (large detection + resp lifecycle) to finalizeOpenAIResponse
	body, result, respErr := finalizeOpenAIResponse(ctx, resp, latency, providerName, logger)
	if respErr != nil {
		return nil, respErr, true
	}
	if result != nil {
		return result, nil, true
	}
	// Normal path — extract usage from raw bytes (passthrough doesn't parse structured response)
	usage := extractOpenAIUsageFromBytes(body)
	return &largePayloadResult{Usage: usage, Latency: latency.Milliseconds(), ResponseBody: body}, nil, true
}

// finalizeOpenAIResponse handles response body processing with optional large response detection.
// Delegates to FinalizeResponseWithLargeDetection for the core branching logic.
// Takes ownership of resp — caller must NOT defer ReleaseResponse and must set respOwned = false
// after this call returns.
//
// Returns:
//   - (body, nil, nil) — normal path; body ready for parsing; resp released.
//   - (nil, result, nil) — large response detected; context flags set for streaming; resp
//     wrapped in reader (released on reader Close).
//   - (nil, nil, err) — error; resp released.
func finalizeOpenAIResponse(
	ctx *schemas.BifrostContext,
	resp *fasthttp.Response,
	latency time.Duration,
	providerName schemas.ModelProvider,
	logger schemas.Logger,
) ([]byte, *largePayloadResult, *schemas.BifrostError) {
	body, isLarge, bifrostErr := providerUtils.FinalizeResponseWithLargeDetection(ctx, resp, providerName, logger)
	if bifrostErr != nil {
		fasthttp.ReleaseResponse(resp)
		return nil, nil, bifrostErr
	}
	if isLarge {
		// Extract usage from the response preview stored in context by FinalizeResponseWithLargeDetection
		preview, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadResponsePreview).(string)
		usage := extractOpenAIUsageFromBytes([]byte(preview))
		// resp owned by LargeResponseReader in context — don't release
		return nil, &largePayloadResult{Usage: usage, Latency: latency.Milliseconds()}, nil
	}
	// Normal path — body already copied by shared utility, safe to release resp
	fasthttp.ReleaseResponse(resp)
	return body, nil, nil
}

// extractOpenAIUsageFromBytes extracts usage metadata from OpenAI response bytes using sonic.Get.
// OpenAI responses have "usage" at the top level with prompt_tokens, completion_tokens, total_tokens.
func extractOpenAIUsageFromBytes(data []byte) *schemas.BifrostLLMUsage {
	node, err := sonic.Get(data, "usage")
	if err != nil {
		return nil
	}
	raw, err := node.Raw()
	if err != nil || raw == "" {
		return nil
	}
	return providerUtils.ParseOpenAIUsageFromBytes([]byte(raw))
}
