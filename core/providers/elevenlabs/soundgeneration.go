package elevenlabs

import (
	"math"
	"net/http"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// ToElevenlabsSoundGenerationRequest maps a Bifrost speech request onto the
// sound-generation body. SFX-specific fields arrive via ExtraParams (they are not
// known fields of the speech schema) and are clamped to the upstream-allowed ranges.
// model_id uses the canonical model resolved from virtual-key aliases so upstream
// receives a real ElevenLabs identifier, not the user-facing alias string.
func ToElevenlabsSoundGenerationRequest(ctx *schemas.BifrostContext, bifrostReq *schemas.BifrostSpeechRequest) *ElevenlabsSoundGenerationRequest {
	if bifrostReq == nil || bifrostReq.Input == nil {
		return nil
	}

	out := &ElevenlabsSoundGenerationRequest{
		Text:    bifrostReq.Input.Input,
		ModelID: schemas.ResolveCanonicalModel(ctx, bifrostReq.Model),
	}

	if bifrostReq.Params != nil && bifrostReq.Params.ExtraParams != nil {
		// Copy the caller's map before consuming keys: the original
		// BifrostSpeechRequest may be reused (e.g. fallback / multi-key retries),
		// so mutating it in place would silently drop these params on retry.
		extra := make(map[string]interface{}, len(bifrostReq.Params.ExtraParams))
		for k, v := range bifrostReq.Params.ExtraParams {
			extra[k] = v
		}
		out.ExtraParams = extra

		if v, ok := schemas.SafeExtractFloat64Pointer(extra["duration_seconds"]); ok {
			delete(extra, "duration_seconds")
			out.DurationSeconds = clampFloat64Ptr(v, 0.5, 30)
		}
		if v, ok := schemas.SafeExtractBoolPointer(extra["loop"]); ok {
			delete(extra, "loop")
			out.Loop = v
		}
		if v, ok := schemas.SafeExtractFloat64Pointer(extra["prompt_influence"]); ok {
			delete(extra, "prompt_influence")
			out.PromptInfluence = clampFloat64Ptr(v, 0, 1)
		}
	}

	return out
}

// clampFloat64Ptr returns a pointer to v clamped to [min, max].
func clampFloat64Ptr(v *float64, min, max float64) *float64 {
	if v == nil {
		return nil
	}
	c := *v
	if c < min {
		c = min
	}
	if c > max {
		c = max
	}
	return &c
}

// soundGeneration performs a text-to-sound-effects request against
// POST /v1/sound-generation. It mirrors Speech() but targets a different endpoint
// and has no voice / timestamps concept. Returns audio bytes in BifrostSpeechResponse.
func (provider *ElevenlabsProvider) soundGeneration(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostSpeechRequest) (*schemas.BifrostSpeechResponse, *schemas.BifrostError) {
	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	providerUtils.SetExtraHeaders(ctx, req, provider.networkConfig.ExtraHeaders, nil)

	// Reuses the speech URL builder for base URL + output_format query handling;
	// only the path differs. enable_logging / optimize_streaming_latency are not
	// set for sound requests, so no TTS-only query params leak in.
	requestURL := provider.buildBaseSpeechRequestURL(ctx, "/v1/sound-generation", schemas.SpeechRequest, request)
	req.SetRequestURI(requestURL)

	req.Header.SetMethod(http.MethodPost)
	req.Header.SetContentType("application/json")
	if key.Value.GetValue() != "" {
		req.Header.Set("xi-api-key", key.Value.GetValue())
	}

	// Build once so the resolved duration can also feed per-second billing below.
	soundReq := ToElevenlabsSoundGenerationRequest(ctx, request)

	jsonData, bifrostErr := providerUtils.CheckContextAndGetRequestBody(
		ctx,
		request,
		func() (providerUtils.RequestBodyWithExtraParams, error) {
			return soundReq, nil
		})
	if bifrostErr != nil {
		return nil, bifrostErr
	}

	if !providerUtils.ApplyLargePayloadRequestBody(ctx, req) {
		req.SetBody(jsonData)
	}

	latency, bifrostErr, wait := providerUtils.MakeRequestWithContext(ctx, provider.client, req, resp)
	defer wait()
	if bifrostErr != nil {
		return nil, providerUtils.EnrichError(ctx, bifrostErr, jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}
	ctx.SetValue(schemas.BifrostContextKeyProviderResponseHeaders, providerUtils.ExtractProviderResponseHeaders(resp))

	if resp.StatusCode() != fasthttp.StatusOK {
		return nil, providerUtils.EnrichError(ctx, parseElevenlabsError(resp), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	body, err := providerUtils.CheckAndDecodeBody(resp)
	if err != nil {
		return nil, providerUtils.EnrichError(ctx, providerUtils.NewBifrostOperationError(schemas.ErrProviderResponseDecode, err), jsonData, nil, provider.sendBackRawRequest, provider.sendBackRawResponse)
	}

	bifrostResponse := &schemas.BifrostSpeechResponse{
		Audio: body,
		ExtraFields: schemas.BifrostResponseExtraFields{
			Latency:                 latency.Milliseconds(),
			ProviderResponseHeaders: providerUtils.ExtractProviderResponseHeaders(resp),
		},
	}

	// ElevenLabs bills sound effects per generated second when a duration is
	// specified. Surface it on usage so the model-catalog per-second rate
	// (OutputCostPerSecond) can bill accurately; auto-duration requests carry no
	// known length and fall back to whatever token/char pricing is configured.
	if soundReq != nil && soundReq.DurationSeconds != nil {
		bifrostResponse.Usage = &schemas.SpeechUsage{
			AudioSeconds: int(math.Round(*soundReq.DurationSeconds)),
		}
	}

	if providerUtils.ShouldSendBackRawRequest(ctx, provider.sendBackRawRequest) {
		providerUtils.ParseAndSetRawRequest(&bifrostResponse.ExtraFields, jsonData)
	}

	return bifrostResponse, nil
}
