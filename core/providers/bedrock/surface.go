package bedrock

import (
	"regexp"
	"strings"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// AWS serves Bedrock models from two endpoints with mutually exclusive model
// identifiers, so picking the wrong one fails outright rather than degrading:
// a bare id ("openai.gpt-5.6-luna") 400s on bedrock-runtime asking for an
// inference profile, while a cross-region id ("global.openai.gpt-5.6-luna") or
// any ARN 404s on bedrock-mantle. The identifier therefore decides the
// endpoint; the model family never did, even though isMantleModel routes on it.
//
// resolveBedrockSurface layers the rules so the narrowest signal wins and the
// broadest — today's family match — stays the fallback, which is what keeps
// existing configurations byte-identical.

// bedrockApplicationInferenceProfileResource is the ARN resource type of an
// application inference profile. Compared exactly, never as a substring:
// "inference-profile" is a prefix of it, so a Contains check would also match
// the system-defined cross-region profiles and divert them wrongly.
const bedrockApplicationInferenceProfileResource = "application-inference-profile"

// bedrockCrossRegionPrefixes are the geographic and global inference-profile
// prefixes AWS puts in front of a model id. These identifiers exist only on
// bedrock-runtime. None collides with a vendor segment (openai, xai, anthropic,
// …), so matching the first dotted segment is unambiguous.
var bedrockCrossRegionPrefixes = []string{"us-gov", "us", "eu", "apac", "au", "jp", "in", "global"}

// bedrockAIPResourceIDPattern matches an application inference profile resource
// id — the bare alphanumeric token AWS generates, e.g. "3dnkdwuaalc7". Every
// Bedrock model id is vendor-qualified or version-suffixed and so always carries
// a separator ("openai.gpt-5.6-luna", "gemma-4-31b", "qwen.qwen3-32b-v1:0");
// a profile id never does.
var bedrockAIPResourceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9]+$`)

// isAIPResourceID reports whether model is shaped like an application inference
// profile resource id rather than a real model id.
//
// This decides whether a profile ARN actually belongs to a deployment. The ARN
// is a prefix that gets joined as {arn}/{model_id}, which only forms a valid
// profile ARN when model_id is the profile's resource id — so a deployment
// naming a real model proves the ARN is not its own. That matters most for the
// key-level ARN, which applies to every model on the credential: without this,
// a key holding one profile would drag its plain-model deployments onto
// bedrock-runtime, where AWS rejects the concatenated identifier outright.
func isAIPResourceID(model string) bool {
	_, bare := parseBedrockRegionAndModel(model)
	return bare != "" && bedrockAIPResourceIDPattern.MatchString(bare)
}

// bedrockSurfaceReason names the rule that chose a surface. Carried into the
// debug log because a misrouted request is otherwise undiagnosable in the field:
// every wrong answer looks like the same upstream 404.
type bedrockSurfaceReason string

const (
	reasonApplicationProfile    bedrockSurfaceReason = "application_inference_profile"
	reasonCrossRegionIdentifier bedrockSurfaceReason = "cross_region_identifier"
	reasonDatasheetRuntimeOnly  bedrockSurfaceReason = "datasheet_runtime_only"
	reasonDatasheetMantleOnly   bedrockSurfaceReason = "datasheet_mantle_only"
	reasonModelFamilyFallback   bedrockSurfaceReason = "model_family_fallback"
)

// bedrockSurface is the endpoint one attempt targets, with the rule that chose
// it. Only the endpoint is acted on today: which wire API to use on it is
// already settled downstream by the request type (chat vs responses on mantle)
// and by the existing Converse/Invoke/Messages selection on runtime.
type bedrockSurface struct {
	host   bedrockService
	reason bedrockSurfaceReason
}

// isMantle reports whether the attempt targets the Bedrock Mantle endpoint.
func (s bedrockSurface) isMantle() bool { return s.host == bedrockServiceMantle }

// bedrockARNResourceType returns the resource-type segment of an AWS ARN
// ("application-inference-profile" for
// "arn:aws:bedrock:us-east-1:123:application-inference-profile/abc12xyz"), or ""
// when the value is not an ARN. Both the bare prefix form Bifrost documents and
// the full form carrying a resource id parse to the same type.
func bedrockARNResourceType(arn string) string {
	if !strings.HasPrefix(arn, "arn:") {
		return ""
	}
	// arn:partition:service:region:account-id:resource-type[/:]resource-id
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 {
		return ""
	}
	resource := parts[5]
	if idx := strings.IndexAny(resource, "/:"); idx >= 0 {
		resource = resource[:idx]
	}
	return resource
}

// isApplicationInferenceProfileARN reports whether arn names an application
// inference profile, as opposed to a system-defined cross-region profile.
func isApplicationInferenceProfileARN(arn string) bool {
	return bedrockARNResourceType(arn) == bedrockApplicationInferenceProfileResource
}

// hasBedrockCrossRegionPrefix reports whether model carries a geographic or
// global inference-profile prefix. Any region prefix ("us-gov-west-1/…") is
// stripped first, since that is Bifrost's own routing syntax rather than part of
// the AWS identifier.
func hasBedrockCrossRegionPrefix(model string) bool {
	_, bare := parseBedrockRegionAndModel(model)
	for _, prefix := range bedrockCrossRegionPrefixes {
		if strings.HasPrefix(bare, prefix+".") {
			return true
		}
	}
	return false
}

// bedrockDatasheetAPIs returns the APIs the datasheet publishes for the model on
// each endpoint. Rows are provider-scoped, so the mantle row and the runtime row
// are separate lookups and either may be absent.
func bedrockDatasheetAPIs(ctx *schemas.BifrostContext, model string) (runtime, mantle []schemas.BedrockAPI) {
	canonical := schemas.ResolveCanonicalModel(ctx, model)
	return schemas.ResolveModelCaps(schemas.Bedrock, canonical).BedrockAPIs(),
		schemas.ResolveModelCaps(schemas.BedrockMantle, canonical).BedrockAPIs()
}

// resolveBedrockSurface picks the endpoint for one attempt.
//
// Mantle stays the default for the families the transition-phase divert covers;
// a request only leaves it when the identifier says it must. The checks run
// narrowest first, and every one of them fires only on a configuration that is
// broken today, which is what keeps existing setups byte-identical.
func resolveBedrockSurface(ctx *schemas.BifrostContext, key schemas.Key, model string) bedrockSurface {
	// An application inference profile is a bedrock-runtime construct: AWS
	// rejects it on the Responses and Chat Completions APIs on both endpoints,
	// so Converse is the only surface that can serve it. Alias-level ARNs take
	// precedence over the key-level default, which resolveBedrockARN already
	// handles. The id must look like a profile resource id: a deployment naming
	// a real model is not covered by the profile ARN, and diverting it would
	// break a configuration that works today.
	if isApplicationInferenceProfileARN(resolveBedrockARN(ctx, key)) && isAIPResourceID(model) {
		return bedrockSurface{host: bedrockServiceRuntime, reason: reasonApplicationProfile}
	}

	// Cross-region ids exist only on bedrock-runtime; mantle 404s them.
	if hasBedrockCrossRegionPrefix(model) {
		return bedrockSurface{host: bedrockServiceRuntime, reason: reasonCrossRegionIdentifier}
	}

	// The datasheet decides only when it names exactly one endpoint. Seventeen
	// models (Claude among them) carry rows under both providers, so "both" is
	// not a signal and falls through rather than guessing.
	runtimeAPIs, mantleAPIs := bedrockDatasheetAPIs(ctx, model)
	switch {
	case len(runtimeAPIs) > 0 && len(mantleAPIs) == 0:
		return bedrockSurface{host: bedrockServiceRuntime, reason: reasonDatasheetRuntimeOnly}
	case len(mantleAPIs) > 0 && len(runtimeAPIs) == 0:
		return bedrockSurface{host: bedrockServiceMantle, reason: reasonDatasheetMantleOnly}
	}

	// Otherwise today's family match, unchanged.
	if isMantleModel(ctx, model) {
		return bedrockSurface{host: bedrockServiceMantle, reason: reasonModelFamilyFallback}
	}
	return bedrockSurface{host: bedrockServiceRuntime, reason: reasonModelFamilyFallback}
}

// routesToMantle resolves the surface and logs the deciding rule.
func (provider *BedrockProvider) routesToMantle(ctx *schemas.BifrostContext, key schemas.Key, model string) bool {
	surface := resolveBedrockSurface(ctx, key, model)
	if provider.logger != nil {
		provider.logger.Debug("bedrock: model %q routed to %s (%s)", model, surface.host, surface.reason)
	}
	return surface.isMantle()
}
