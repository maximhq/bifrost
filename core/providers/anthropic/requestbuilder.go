package anthropic

import (
	"fmt"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// AnthropicRequestBuildConfig holds the dynamic, per-call inputs to
// BuildAnthropic{Chat,Responses}RequestBody. The static, per-provider
// request-shaping flags (DeleteModelField, AddAnthropicVersion, etc.) live in
// AnthropicProviderRequestDefaultsMap and are looked up by Provider inside the
// builder — callers do not pass them.
type AnthropicRequestBuildConfig struct {
	// Provider is used for feature-gating (field stripping, header injection,
	// tool validation) and to look up static request-shaping defaults from
	// AnthropicProviderRequestDefaultsMap. Required.
	Provider schemas.ModelProvider

	// Model overrides the model field. When empty the model is read from
	// the request. Azure, Vertex, and Bedrock set this to the deployment /
	// model name.
	Model string

	IsStreaming bool

	// IsCountTokens enables token-counting mode: strips max_tokens and
	// temperature from the body and keeps (or sets) the model field.
	IsCountTokens bool

	// ExcludeFields lists JSON top-level keys to remove from the final body
	// in both raw and typed paths. Used by Anthropic native count-tokens to
	// strip max_tokens and temperature after typed conversion.
	ExcludeFields []string

	// ValidateTools runs ValidateToolsForProvider before typed conversion,
	// returning an error for any tool unsupported by the provider. Set true
	// on Responses-API paths (the chat API doesn't carry ResponsesTool types).
	ValidateTools bool

	// BetaHeaderOverrides / ProviderExtraHeaders feed into the body-side
	// anthropic_beta injection when the provider's defaults set
	// InjectBetaHeadersIntoBody = true (Vertex only). Both come from the
	// caller's NetworkConfig at request time.
	BetaHeaderOverrides  map[string]bool
	ProviderExtraHeaders map[string]string

	// ShouldSendBackRawRequest / ShouldSendBackRawResponse control whether raw
	// request/response bytes are attached to BifrostError.ExtraFields via
	// providerUtils.EnrichError.
	ShouldSendBackRawRequest  bool
	ShouldSendBackRawResponse bool
}

// AnthropicProviderRequestDefaults captures the static, per-provider request-
// shaping flags applied by BuildAnthropic{Chat,Responses}RequestBody.
//
// Keep this in lockstep with ProviderFeatures (utils.go) — together they
// describe everything an Anthropic-family provider needs for request shaping.
type AnthropicProviderRequestDefaults struct {
	// DeleteModelField removes "model" from the output JSON body. Used by
	// providers that put model in the URL (Vertex, Bedrock). Ignored when
	// IsCountTokens is true — those calls retain model for routing.
	DeleteModelField bool

	// DeleteRegionField removes the "region" field from the body (Vertex only).
	DeleteRegionField bool

	// DeleteStreamField removes "stream" from the body unconditionally.
	// Bedrock determines streaming via URL endpoint (invoke vs
	// invoke-with-response-stream); the body must never carry a stream field.
	DeleteStreamField bool

	// AddAnthropicVersion injects "anthropic_version" into the body when the
	// field is absent (Vertex, Bedrock).
	AddAnthropicVersion bool
	AnthropicVersion    string

	// StripCacheControlScope calls SetStripCacheControlScope(true) on the
	// typed request struct before marshalling (Vertex only).
	StripCacheControlScope bool

	// RemapToolVersions runs RemapRawToolVersionsForProvider on the body to
	// downgrade unsupported tool type versions (Vertex, Bedrock).
	RemapToolVersions bool

	// InjectBetaHeadersIntoBody serialises filtered beta headers into the JSON
	// body as "anthropic_beta" (Vertex only — embeds in body, others use HTTP).
	InjectBetaHeadersIntoBody bool
}

// AnthropicProviderRequestDefaultsMap maps each Anthropic-family provider to
// the static request-shaping defaults it needs. The builder reads from this
// map directly using cfg.Provider — callers do not set these fields.
var AnthropicProviderRequestDefaultsMap = map[schemas.ModelProvider]AnthropicProviderRequestDefaults{
	schemas.Anthropic: {},
	schemas.Azure:     {},
	// Bedrock Mantle native-Anthropic endpoint (/anthropic/v1/messages): the
	// request is the native Anthropic Messages body, so model stays in the body
	// (set to the bare Bedrock model id), the version is sent as an
	// "anthropic-version" HTTP header rather than a body field, and stream is a
	// body field. Tool type versions are still remapped to the canonical pair
	// the hosted Claude generation expects.
	schemas.Bedrock: {
		RemapToolVersions: true,
	},
	// Bedrock Mantle shares the Bedrock native-Anthropic request shape (model in
	// body, anthropic-version HTTP header, tool versions remapped). It has its own
	// entry so its feature surface in ProviderFeatures can diverge from Bedrock's
	// Converse path without coupling the two.
	schemas.BedrockMantle: {
		RemapToolVersions: true,
	},
	schemas.DeepSeek: {},
	// Vertex publisher endpoint: model + region in URL, anthropic_version
	// required, beta headers in body (not HTTP), cache_control.scope stripped
	// at marshal time, tool versions remapped.
	schemas.Vertex: {
		DeleteModelField:          true,
		DeleteRegionField:         true,
		AddAnthropicVersion:       true,
		AnthropicVersion:          "vertex-2023-10-16",
		StripCacheControlScope:    true,
		RemapToolVersions:         true,
		InjectBetaHeadersIntoBody: true,
	},
	schemas.SGL: {},
}

// BuildAnthropicResponsesRequestBody is the single implementation of the
// Anthropic-family request-body assembly pipeline, shared by the Anthropic,
// Azure, and Vertex providers. Provider-specific behaviour is encoded in the
// supplied AnthropicRequestBuildConfig; the shared steps (large-payload guard,
// raw-vs-typed branching, field stripping, beta-header injection, fallbacks
// deletion) are handled here.
func BuildAnthropicResponsesRequestBody(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest, cfg AnthropicRequestBuildConfig) ([]byte, *schemas.BifrostError) {
	if providerUtils.IsLargePayloadPassthroughEnabled(ctx) {
		return nil, nil
	}

	// capModel is the canonical model used for capability gating in the raw-body
	capModel := schemas.ResolveCanonicalModel(ctx, request.Model)

	defaults := AnthropicProviderRequestDefaultsMap[cfg.Provider]

	newErr := func(msg string, err error, reqBody []byte) *schemas.BifrostError {
		return providerUtils.EnrichError(
			ctx,
			providerUtils.NewBifrostOperationError(msg, err),
			reqBody,
			nil,
			cfg.ShouldSendBackRawRequest,
			cfg.ShouldSendBackRawResponse,
		)
	}

	var jsonBody []byte
	var err error

	if useRawBody, ok := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && useRawBody {
		jsonBody = request.GetRawRequestBody()

		if cfg.IsCountTokens {
			// Token-counting mode: strip max_tokens / temperature and set model.
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "max_tokens")
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "temperature")
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "model", cfg.Model)
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		} else {
			// Normal path: handle model field per provider.
			if cfg.Model != "" {
				if defaults.DeleteModelField {
					// Vertex/Bedrock: model lives in the URL.
					jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "model")
					if err != nil {
						return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
					}
				} else {
					// Azure: replace model with deployment name.
					jsonBody, err = providerUtils.SetJSONField(jsonBody, "model", cfg.Model)
					if err != nil {
						return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
					}
				}
			} else {
				// Anthropic native: use the alias-resolved model from the request
				jsonBody, err = providerUtils.SetJSONField(jsonBody, "model", request.Model)
				if err != nil {
					return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
				}
			}

			// Ensure max_tokens is present.
			if !providerUtils.JSONFieldExists(jsonBody, "max_tokens") {
				modelForTokens := cfg.Model
				if modelForTokens == "" {
					if r := providerUtils.GetJSONField(jsonBody, "model"); r.Exists() {
						modelForTokens = r.String()
					}
				}
				jsonBody, err = providerUtils.SetJSONField(jsonBody, "max_tokens", providerUtils.GetMaxOutputTokensOrDefault(modelForTokens, AnthropicDefaultMaxTokens))
				if err != nil {
					return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
				}
			}

		}

		if cfg.IsStreaming {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "stream", true)
		} else {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "stream")
		}
		if err != nil {
			return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
		}

		jsonBody, err = StripAutoInjectableTools(jsonBody)
		if err != nil {
			return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
		}

		if defaults.RemapToolVersions {
			// request.Model is the alias-resolved model id; pass it so
			// computer-use / text-editor / bash tools get normalized to the
			// canonical {type, name} pair Anthropic expects for the model's generation.
			jsonBody, err = RemapRawToolVersionsForProvider(jsonBody, cfg.Provider, capModel)
			if err != nil {
				return nil, newErr(err.Error(), nil, jsonBody)
			}
		}

		if defaults.DeleteRegionField {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "region")
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}

		jsonBody, err = StripUnsupportedFieldsFromRawBody(jsonBody, cfg.Provider, capModel)
		if err != nil {
			return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
		}

		if defaults.AddAnthropicVersion && !providerUtils.JSONFieldExists(jsonBody, "anthropic_version") {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "anthropic_version", defaults.AnthropicVersion)
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}

		// Probe-unmarshal to auto-inject beta headers required by fields that
		// survived stripping, so raw-body callers don't need to supply headers
		// manually.
		var probe AnthropicMessageRequest
		if unmarshalErr := schemas.Unmarshal(jsonBody, &probe); unmarshalErr == nil {
			AddMissingBetaHeadersToContext(ctx, &probe, cfg.Provider)
		}

		for _, field := range cfg.ExcludeFields {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, field)
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}
	} else {
		if cfg.ValidateTools && request.Params != nil && request.Params.Tools != nil {
			// Silently drop provider-unsupported tools (e.g. an `mcp` server tool
			// on Vertex, whose Converse API has no remote-MCP connector) rather
			// than failing the whole request. Mirrors ValidateChatToolsForProvider
			// and the Bedrock Responses path. Use a shallow copy so the shared
			// (possibly pooled) request and its Params are never mutated.
			if keep, dropped := ValidateResponsesToolsForProvider(request.Params.Tools, cfg.Provider); len(dropped) > 0 {
				reqCopy := *request
				paramsCopy := *request.Params
				paramsCopy.Tools = keep
				reqCopy.Params = &paramsCopy
				request = &reqCopy
			}
		}

		reqBody, convErr := ToAnthropicResponsesRequest(ctx, request)
		if convErr != nil {
			return nil, newErr(schemas.ErrRequestBodyConversion, convErr, jsonBody)
		}
		if reqBody == nil {
			return nil, newErr("request body is not provided", nil, jsonBody)
		}

		if cfg.Model != "" {
			reqBody.Model = cfg.Model
		}

		if defaults.StripCacheControlScope {
			reqBody.SetStripCacheControlScope(true)
		}

		if cfg.IsStreaming {
			reqBody.Stream = schemas.Ptr(true)
		}

		// Strip request- and tool-level fields the target provider doesn't
		// support. ToAnthropicResponsesRequest doesn't do this internally
		// (unlike ToAnthropicChatRequest), so the builder must — keeping
		// behaviour symmetric across raw and typed paths and across both
		// chat/responses APIs.
		stripUnsupportedAnthropicFields(reqBody, cfg.Provider, request.Model)

		AddMissingBetaHeadersToContext(ctx, reqBody, cfg.Provider)

		jsonBody, err = providerUtils.MarshalSorted(reqBody)
		if err != nil {
			return nil, newErr(schemas.ErrProviderRequestMarshal, fmt.Errorf("failed to marshal request body: %w", err), jsonBody)
		}

		if providerUtils.ShouldPassthroughExtraParams(ctx) {
			extraParams := reqBody.GetExtraParams()
			if len(extraParams) > 0 {
				jsonBody, err = providerUtils.MergeExtraParamsIntoJSON(jsonBody, extraParams)
				if err != nil {
					return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
				}
			}
		}

		if defaults.AddAnthropicVersion && !providerUtils.JSONFieldExists(jsonBody, "anthropic_version") {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "anthropic_version", defaults.AnthropicVersion)
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}

		if cfg.IsCountTokens {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "max_tokens")
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "temperature")
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		} else if defaults.DeleteModelField {
			// Vertex/Bedrock: model is in the URL, remove it from the body.
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "model")
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}

		if defaults.DeleteRegionField {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "region")
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}

		for _, field := range cfg.ExcludeFields {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, field)
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}
	}

	jsonBody, err = StripEmptyThinkingBlocks(jsonBody)
	if err != nil {
		return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
	}

	// Strip Bifrost cross-provider fallback strings, but preserve Anthropic
	// native server-side fallback objects (server-side-fallback-2026-06-01).
	jsonBody, err = stripBifrostFallbacksFromBody(jsonBody, cfg.Provider)
	if err != nil {
		return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
	}

	if cfg.IsCountTokens {
		// The count_tokens endpoint rejects fallback_credit_token outright.
		jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "fallback_credit_token")
		if err != nil {
			return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
		}
	}

	if defaults.DeleteStreamField {
		jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "stream")
		if err != nil {
			return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
		}
	}

	if defaults.InjectBetaHeadersIntoBody {
		if betaHeaders := FilterBetaHeadersForProvider(MergeBetaHeaders(ctx, cfg.ProviderExtraHeaders), cfg.Provider, cfg.BetaHeaderOverrides); len(betaHeaders) > 0 {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "anthropic_beta", betaHeaders)
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}
	}

	return jsonBody, nil
}

// BuildAnthropicChatRequestBody is the chat-completion analogue of
// BuildAnthropicResponsesRequestBody, shared by Anthropic, Azure, Vertex, and
// Bedrock for ChatCompletion / ChatCompletionStream paths. It mirrors the
// responses pipeline (raw vs typed branching, field stripping, beta-header
// injection, fallbacks deletion) but operates on BifrostChatRequest +
// ToAnthropicChatRequest. IsCountTokens is not honoured here — count-tokens
// is a Responses-API concept.
func BuildAnthropicChatRequestBody(ctx *schemas.BifrostContext, request *schemas.BifrostChatRequest, cfg AnthropicRequestBuildConfig) ([]byte, *schemas.BifrostError) {
	if providerUtils.IsLargePayloadPassthroughEnabled(ctx) {
		return nil, nil
	}

	// capModel is the canonical model used for capability gating in the raw-body
	// path; the wire request.Model may be an opaque alias/deployment id.
	capModel := schemas.ResolveCanonicalModel(ctx, request.Model)

	defaults := AnthropicProviderRequestDefaultsMap[cfg.Provider]

	newErr := func(msg string, err error, reqBody []byte) *schemas.BifrostError {
		return providerUtils.EnrichError(
			ctx,
			providerUtils.NewBifrostOperationError(msg, err),
			reqBody,
			nil,
			cfg.ShouldSendBackRawRequest,
			cfg.ShouldSendBackRawResponse,
		)
	}

	var jsonBody []byte
	var err error

	if useRawBody, ok := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && useRawBody {
		jsonBody = request.GetRawRequestBody()

		if cfg.Model != "" {
			if defaults.DeleteModelField {
				jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "model")
				if err != nil {
					return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
				}
			} else {
				jsonBody, err = providerUtils.SetJSONField(jsonBody, "model", cfg.Model)
				if err != nil {
					return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
				}
			}
		} else {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "model", request.Model)
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}

		if !providerUtils.JSONFieldExists(jsonBody, "max_tokens") {
			modelForTokens := cfg.Model
			if modelForTokens == "" {
				if r := providerUtils.GetJSONField(jsonBody, "model"); r.Exists() {
					modelForTokens = r.String()
				}
			}
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "max_tokens", providerUtils.GetMaxOutputTokensOrDefault(modelForTokens, AnthropicDefaultMaxTokens))
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}

		if cfg.IsStreaming {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "stream", true)
		} else {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "stream")
		}
		if err != nil {
			return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
		}

		jsonBody, err = StripAutoInjectableTools(jsonBody)
		if err != nil {
			return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
		}

		if defaults.RemapToolVersions {
			jsonBody, err = RemapRawToolVersionsForProvider(jsonBody, cfg.Provider, capModel)
			if err != nil {
				return nil, newErr(err.Error(), nil, jsonBody)
			}
		}

		if defaults.DeleteRegionField {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "region")
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}

		jsonBody, err = StripUnsupportedFieldsFromRawBody(jsonBody, cfg.Provider, capModel)
		if err != nil {
			return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
		}

		if defaults.AddAnthropicVersion && !providerUtils.JSONFieldExists(jsonBody, "anthropic_version") {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "anthropic_version", defaults.AnthropicVersion)
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}

		var probe AnthropicMessageRequest
		if unmarshalErr := schemas.Unmarshal(jsonBody, &probe); unmarshalErr == nil {
			AddMissingBetaHeadersToContext(ctx, &probe, cfg.Provider)
		}

		for _, field := range cfg.ExcludeFields {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, field)
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}
	} else {
		reqBody, convErr := ToAnthropicChatRequest(ctx, request)
		if convErr != nil {
			return nil, newErr(schemas.ErrRequestBodyConversion, convErr, jsonBody)
		}
		if reqBody == nil {
			return nil, newErr("request body is not provided", nil, jsonBody)
		}

		if cfg.Model != "" {
			reqBody.Model = cfg.Model
		}

		if defaults.StripCacheControlScope {
			reqBody.SetStripCacheControlScope(true)
		}

		if cfg.IsStreaming {
			reqBody.Stream = schemas.Ptr(true)
		}

		// Re-strip with cfg.Provider (canonical) in case the request was
		// routed through a custom-provider alias whose name doesn't match
		// the ProviderFeatures map entry. Idempotent — ToAnthropicChatRequest
		// already strips using bifrostReq.Provider, so this only changes
		// behaviour when the two diverge.
		stripUnsupportedAnthropicFields(reqBody, cfg.Provider, request.Model)

		AddMissingBetaHeadersToContext(ctx, reqBody, cfg.Provider)

		jsonBody, err = providerUtils.MarshalSorted(reqBody)
		if err != nil {
			return nil, newErr(schemas.ErrProviderRequestMarshal, fmt.Errorf("failed to marshal request body: %w", err), jsonBody)
		}

		if ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams) == true {
			extraParams := reqBody.GetExtraParams()
			if len(extraParams) > 0 {
				jsonBody, err = providerUtils.MergeExtraParamsIntoJSON(jsonBody, extraParams)
				if err != nil {
					return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
				}
			}
		}

		if defaults.AddAnthropicVersion && !providerUtils.JSONFieldExists(jsonBody, "anthropic_version") {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "anthropic_version", defaults.AnthropicVersion)
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}

		if defaults.DeleteModelField {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "model")
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}

		if defaults.DeleteRegionField {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "region")
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}

		for _, field := range cfg.ExcludeFields {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, field)
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}
	}

	jsonBody, err = StripEmptyThinkingBlocks(jsonBody)
	if err != nil {
		return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
	}

	// Strip Bifrost cross-provider fallback strings, but preserve Anthropic
	// native server-side fallback objects (server-side-fallback-2026-06-01).
	jsonBody, err = stripBifrostFallbacksFromBody(jsonBody, cfg.Provider)
	if err != nil {
		return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
	}

	if defaults.DeleteStreamField {
		jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "stream")
		if err != nil {
			return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
		}
	}

	if defaults.InjectBetaHeadersIntoBody {
		if betaHeaders := FilterBetaHeadersForProvider(MergeBetaHeaders(ctx, cfg.ProviderExtraHeaders), cfg.Provider, cfg.BetaHeaderOverrides); len(betaHeaders) > 0 {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "anthropic_beta", betaHeaders)
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}
	}

	return jsonBody, nil
}