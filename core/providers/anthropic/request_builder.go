package anthropic

import (
	"fmt"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// AnthropicRequestBuildConfig controls how BuildAnthropicResponsesRequestBody
// assembles the final JSON payload. Each Anthropic-family provider (Anthropic
// native, Azure, Vertex) fills in only the fields relevant to it and leaves
// the rest as zero values.
type AnthropicRequestBuildConfig struct {
	// Provider is used for feature-gating (field stripping, header injection,
	// tool validation). Required.
	Provider schemas.ModelProvider

	// Deployment overrides the model field. When empty the model is read from
	// the request and normalised via ParseModelString (Anthropic native path).
	// Azure and Vertex set this to the deployment/model name.
	Deployment string

	// DeleteModelField removes "model" from the output JSON body.
	// Vertex passes model in the request URL, not the body.
	// Ignored when IsCountTokens is true — count-tokens calls retain the model
	// field so the provider can route to the correct endpoint.
	DeleteModelField bool

	// DeleteRegionField removes the "region" field (Vertex only).
	DeleteRegionField bool

	IsStreaming bool

	// IsCountTokens enables token-counting mode (Vertex only): strips
	// max_tokens and temperature from the body and keeps (or sets) the model
	// field.
	IsCountTokens bool

	// ExcludeFields lists JSON top-level keys to remove from the final body in
	// both the raw and typed paths. Used by Anthropic's count-tokens call to
	// strip max_tokens and temperature after typed conversion.
	ExcludeFields []string

	// AddAnthropicVersion injects "anthropic_version" into the body when the
	// field is absent (Vertex only).
	AddAnthropicVersion bool
	AnthropicVersion    string

	// StripCacheControlScope calls SetStripCacheControlScope(true) on the
	// typed request struct before marshalling (Vertex typed path).
	StripCacheControlScope bool

	// RemapToolVersions runs RemapRawToolVersionsForProvider on the raw body to
	// downgrade unsupported tool type versions (Vertex raw path).
	RemapToolVersions bool

	// InjectBetaHeadersIntoBody serialises filtered beta headers into the JSON
	// body as "anthropic_beta". Vertex embeds beta headers in the body rather
	// than HTTP request headers.
	InjectBetaHeadersIntoBody bool
	BetaHeaderOverrides       map[string]bool
	ProviderExtraHeaders      map[string]string

	// ValidateTools runs ValidateToolsForProvider before typed conversion,
	// returning an error for any tool unsupported by the provider (Azure,
	// Vertex).
	ValidateTools bool

	// ShouldSendBackRawRequest / ShouldSendBackRawResponse control whether raw
	// request/response bytes are attached to BifrostError.ExtraFields via
	// providerUtils.EnrichError. Vertex honours per-provider send-back flags;
	// Anthropic and Azure leave both false.
	ShouldSendBackRawRequest  bool
	ShouldSendBackRawResponse bool
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
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "model", cfg.Deployment)
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		} else {
			// Normal path: handle model field per provider.
			if cfg.Deployment != "" {
				if cfg.DeleteModelField {
					// Vertex: model lives in the URL.
					jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "model")
					if err != nil {
						return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
					}
				} else {
					// Azure: replace model with deployment name.
					jsonBody, err = providerUtils.SetJSONField(jsonBody, "model", cfg.Deployment)
					if err != nil {
						return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
					}
				}
			} else {
				// Anthropic native: normalise the model string via ParseModelString.
				if modelResult := providerUtils.GetJSONField(jsonBody, "model"); modelResult.Exists() {
					if modelStr := modelResult.String(); modelStr != "" {
						_, model := schemas.ParseModelString(modelStr, schemas.Anthropic)
						jsonBody, err = providerUtils.SetJSONField(jsonBody, "model", model)
						if err != nil {
							return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
						}
					}
				}
			}

			// Ensure max_tokens is present.
			if !providerUtils.JSONFieldExists(jsonBody, "max_tokens") {
				modelForTokens := cfg.Deployment
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

		jsonBody, err = StripEmptyThinkingBlocks(jsonBody)
		if err != nil {
			return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
		}

		if cfg.RemapToolVersions {
			jsonBody, err = RemapRawToolVersionsForProvider(jsonBody, cfg.Provider)
			if err != nil {
				return nil, newErr(err.Error(), nil, jsonBody)
			}
		}

		if cfg.DeleteRegionField {
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "region")
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}

		jsonBody, err = StripUnsupportedFieldsFromRawBody(jsonBody, cfg.Provider, request.Model)
		if err != nil {
			return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
		}

		if cfg.AddAnthropicVersion && !providerUtils.JSONFieldExists(jsonBody, "anthropic_version") {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "anthropic_version", cfg.AnthropicVersion)
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
			if toolErr := ValidateToolsForProvider(request.Params.Tools, cfg.Provider); toolErr != nil {
				return nil, newErr(toolErr.Error(), nil, jsonBody)
			}
		}

		reqBody, convErr := ToAnthropicResponsesRequest(ctx, request)
		if convErr != nil {
			return nil, newErr(schemas.ErrRequestBodyConversion, convErr, jsonBody)
		}
		if reqBody == nil {
			return nil, newErr("request body is not provided", nil, jsonBody)
		}

		if cfg.Deployment != "" {
			reqBody.Model = cfg.Deployment
		}

		if cfg.StripCacheControlScope {
			reqBody.SetStripCacheControlScope(true)
		}

		if cfg.IsStreaming {
			reqBody.Stream = schemas.Ptr(true)
		}

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

		if cfg.AddAnthropicVersion && !providerUtils.JSONFieldExists(jsonBody, "anthropic_version") {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "anthropic_version", cfg.AnthropicVersion)
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
		} else if cfg.DeleteModelField {
			// Vertex: model is in the URL, remove it from the body.
			jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "model")
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}

		if cfg.DeleteRegionField {
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

	jsonBody, err = providerUtils.DeleteJSONField(jsonBody, "fallbacks")
	if err != nil {
		return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
	}

	if cfg.InjectBetaHeadersIntoBody {
		if betaHeaders := FilterBetaHeadersForProvider(MergeBetaHeaders(cfg.ProviderExtraHeaders, ctx), cfg.Provider, cfg.BetaHeaderOverrides); len(betaHeaders) > 0 {
			jsonBody, err = providerUtils.SetJSONField(jsonBody, "anthropic_beta", betaHeaders)
			if err != nil {
				return nil, newErr(schemas.ErrProviderRequestMarshal, err, jsonBody)
			}
		}
	}

	return jsonBody, nil
}
