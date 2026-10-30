package openai

import (
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// rewritesBooleanSchemaNodes reports whether this custom provider declared
// custom_provider_config.boolean_schema_nodes_unsupported. The native OpenAI provider
// has no custom config and never rewrites.
func (provider *OpenAIProvider) rewritesBooleanSchemaNodes() bool {
	return provider.customProviderConfig != nil && provider.customProviderConfig.BaseProviderType == schemas.OpenAI && provider.customProviderConfig.BooleanSchemaNodesUnsupported
}

// normalizeChatToolSchemas returns request unchanged unless this provider rewrites
// boolean schema nodes and a tool schema actually contains one. Then it returns a
// shallow copy whose Params carries the rewritten tools; the caller's request, which
// a fallback attempt may reuse, is never mutated, and every other field (model
// included) is shared as-is.
func (provider *OpenAIProvider) normalizeChatToolSchemas(request *schemas.BifrostChatRequest) *schemas.BifrostChatRequest {
	if request == nil || request.Params == nil || !provider.rewritesBooleanSchemaNodes() {
		return request
	}
	tools, changed := providerUtils.RewriteChatToolBooleanSchemaNodes(request.Params.Tools)
	if !changed {
		return request
	}
	params := *request.Params
	params.Tools = tools
	normalized := *request
	normalized.Params = &params
	return &normalized
}

// normalizeResponsesToolSchemas is the Responses parallel of normalizeChatToolSchemas.
func (provider *OpenAIProvider) normalizeResponsesToolSchemas(request *schemas.BifrostResponsesRequest) *schemas.BifrostResponsesRequest {
	if request == nil || request.Params == nil || !provider.rewritesBooleanSchemaNodes() {
		return request
	}
	tools, changed := providerUtils.RewriteResponsesToolBooleanSchemaNodes(request.Params.Tools)
	if !changed {
		return request
	}
	params := *request.Params
	params.Tools = tools
	normalized := *request
	normalized.Params = &params
	return &normalized
}
