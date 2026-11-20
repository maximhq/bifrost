package openai

import (
	"bytes"
	"io"
	"strings"

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

// rewriteLargePayloadToolSchemas applies the boolean schema rewrite to a request body
// staged for large payload passthrough, which is forwarded as-is and never reaches
// normalizeChatToolSchemas or normalizeResponsesToolSchemas. The staged body is read
// into memory so its tool schemas can be rewritten, and the context then carries the
// rewritten bytes, so the passthrough still sends one body and the upstream answer is
// streamed back as before. It does nothing unless this provider rewrites boolean schema
// nodes and a JSON body is staged.
func (provider *OpenAIProvider) rewriteLargePayloadToolSchemas(ctx *schemas.BifrostContext) *schemas.BifrostError {
	if !provider.rewritesBooleanSchemaNodes() || !providerUtils.IsLargePayloadPassthroughEnabled(ctx) {
		return nil
	}
	if contentType, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadContentType).(string); contentType != "" && !strings.Contains(strings.ToLower(contentType), "json") {
		return nil
	}
	reader, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadReader).(io.Reader)
	body, err := io.ReadAll(reader)
	if err != nil {
		return providerUtils.NewBifrostOperationError("failed to read the large payload request body to rewrite boolean tool schemas", err)
	}
	rewritten, _, err := providerUtils.RewriteToolBooleanSchemaNodesJSON(body)
	if err != nil {
		return providerUtils.NewBifrostBadRequestError("failed to rewrite boolean tool schemas in the request body: " + err.Error())
	}
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, bytes.NewReader(rewritten))
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentLength, len(rewritten))
	return nil
}
