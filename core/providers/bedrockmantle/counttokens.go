package bedrockmantle

import (
	"fmt"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	openai "github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// mantleAnthropicCountTokensURL builds the Bedrock Mantle native-Anthropic count-tokens URL.
// AWS exposes Anthropic's count_tokens API at /anthropic/v1/messages/count_tokens on the
// bedrock-mantle host. This is the only token-counting path available for Claude models that
// have no Region-specific bedrock-runtime endpoint (models launching cross-Region-inference
// only), which is why it is not redundant with the bedrock provider's Converse count-tokens.
func mantleAnthropicCountTokensURL(endpoints *schemas.BedrockEndpoints, region string) string {
	return fmt.Sprintf("https://%s/anthropic/v1/messages/count_tokens", mantleHost(endpoints, region))
}

// CountTokens counts the input tokens a request would consume, using the native-Anthropic
// count_tokens path on the Bedrock Mantle endpoint.
//
// Only Anthropic-family (Claude) models are supported: the /anthropic/v1/messages surface is
// Claude-specific and AWS rejects other models on it, while the OpenAI-compatible mantle surface
// exposes no count-tokens path at all. Non-Anthropic models therefore get the standard
// unsupported-operation error rather than a request that is guaranteed to fail upstream.
//
// The request itself goes through the shared Anthropic count-tokens handler, the same way
// ChatCompletion goes through the shared Anthropic messages handler: mantle only supplies the
// region-derived URL, the bare model, its headers, and a signer. Auth mirrors the other mantle
// surfaces: a Bedrock Mantle API key flows through the Authorization header, otherwise the body
// is SigV4-signed for the bedrock-mantle service. The project header is sent because AWS scopes
// the bedrock-mantle:CountTokens IAM action to a Bedrock Project resource.
func (provider *BedrockMantleProvider) CountTokens(ctx *schemas.BifrostContext, key schemas.Key, request *schemas.BifrostResponsesRequest) (*schemas.BifrostCountTokensResponse, *schemas.BifrostError) {
	if !schemas.IsAnthropicModelFamily(ctx, request.Model) {
		return nil, providerUtils.NewUnsupportedOperationError(schemas.CountTokensRequest, provider.GetProviderKey())
	}

	region := provider.resolveRegion(ctx, key, request.Model)
	url := mantleAnthropicCountTokensURL(mantleEndpoints(key.BedrockMantleKeyConfig), region)
	// The region addresses the host, the bare id goes in the body.
	_, bareModel := parseBedrockRegionAndModel(request.Model)

	// IsCountTokens keeps the model field (mantle routes on it) and strips the inference-only
	// fields the count_tokens schema rejects; the handler appends max_tokens and temperature to
	// ExcludeFields so they are removed on the typed path too.
	return anthropic.HandleAnthropicCountTokensRequest(
		ctx,
		provider.mantleClient,
		url,
		request,
		anthropic.AnthropicRequestBuildConfig{
			Provider:                  schemas.BedrockMantle,
			Model:                     bareModel,
			IsCountTokens:             true,
			ValidateTools:             true,
			BetaHeaderOverrides:       provider.networkConfig.BetaHeaderOverrides,
			ShouldSendBackRawRequest:  provider.sendBackRawRequest,
			ShouldSendBackRawResponse: provider.sendBackRawResponse,
		},
		openai.BearerAuthHeader(key),
		addAnthropicHeaders(bedrock.WithMantleProject(provider.networkConfig.ExtraHeaders, bedrock.MantleAnthropicProjectHeader, resolveProjectID(ctx, key))),
		provider.mantleSigner(ctx, key, url, "application/json", region),
		provider.logger,
	)
}
