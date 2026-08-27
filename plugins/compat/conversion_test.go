package compat

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestFlattenNamespaceTools_AzureAliasedAnthropicModel guards against a
// routing rule (or virtual model alias) rewriting the request model to a bare
// name like "haiku" that doesn't literally contain "claude"/"anthropic.":
// namespace tools must still be flattened for an Azure-hosted Anthropic model,
// or the request goes out with namespace-scoped tools Anthropic can't parse.
func TestFlattenNamespaceTools_AzureAliasedAnthropicModel(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Azure,
		Model:    "haiku",
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{
					Type: schemas.ResponsesToolTypeNamespace,
					ResponsesToolNamespace: &schemas.ResponsesToolNamespace{
						Tools: []schemas.ResponsesTool{
							{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("get_weather")},
						},
					},
				},
			},
		},
	}

	ctx := newTestContext()
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Config: &schemas.AliasConfig{ModelID: "anthropic.claude-haiku-4-5-v1:0"},
	})

	flattenNamespaceTools(ctx, req)

	tools := req.Params.Tools
	if len(tools) != 1 || tools[0].Type != schemas.ResponsesToolTypeFunction || tools[0].Name == nil || *tools[0].Name != "get_weather" {
		t.Errorf("tools = %+v, want flattened to a single function tool", tools)
	}
}
