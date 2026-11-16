package bifrost

import "github.com/maximhq/bifrost/core/schemas"

// restoreResponsesBillingHeader runs after alias resolution so each attempt uses
// the actual destination family. Ingress already removed the metadata before
// sharing Input: non-Anthropic attempts need neither a scan nor a slice copy.
func restoreResponsesBillingHeader(ctx *schemas.BifrostContext, provider schemas.ModelProvider, r *schemas.BifrostResponsesRequest) *schemas.BifrostResponsesRequest {
	if r == nil {
		return r
	}
	if ctx != nil && ctx.Value(schemas.BifrostContextKeyUseRawRequestBody) == true && len(r.RawRequestBody) > 0 {
		return r // Native passthrough already carries the original billing block.
	}
	family := schemas.ResolveFamily(ctx, r.Model)
	if family == schemas.ModelFamilyAnthropic || (family == "" && schemas.ResolveBaseProvider(ctx, provider) == schemas.Anthropic) {
		return r.WithAnthropicBillingHeader()
	}
	return r
}
