package bedrockmantle

import (
	"maps"
	"regexp"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// awsRegionRegex matches valid AWS region identifiers (e.g. "us-east-1", "eu-north-1", "us-gov-east-1").
// (?:-[a-z]+)+ allows multi-segment directional parts so GovCloud regions (us-gov-east-1) are
// recognised alongside standard single-segment ones (eu-north-1, ap-southeast-2).
var awsRegionRegex = regexp.MustCompile(`^[a-z]{2,3}(?:-[a-z]+)+-\d+$`)

// addAnthropicHeaders returns a copy of the given extra headers with the native-Anthropic
// mantle anthropic-version header added. It clones the input first so it never mutates the
// shared networkConfig.ExtraHeaders map (which would leak anthropic-version onto the
// OpenAI-compatible requests and race across concurrent requests).
func addAnthropicHeaders(headers map[string]string) map[string]string {
	out := maps.Clone(headers)
	if out == nil {
		out = make(map[string]string, 1)
	}
	out["anthropic-version"] = mantleAnthropicVersion
	return out
}

// resolveProjectID returns the Bedrock project configured for this attempt, or "" when none is set
// (AWS then routes to the account's default project). The value is sent as the OpenAI-Project or
// anthropic-workspace-id header depending on the request surface.
// Priority: per-alias AliasConfig.ProjectID > key-level BedrockMantleKeyConfig.ProjectID. The
// per-alias override lets one credential scope different aliased models to different projects.
func resolveProjectID(ctx *schemas.BifrostContext, key schemas.Key) string {
	if ra := schemas.GetResolvedAlias(ctx); ra != nil && ra.Config != nil && ra.Config.ProjectID != nil {
		if v := ra.Config.ProjectID.GetValue(); v != "" {
			return v
		}
	}
	if key.BedrockMantleKeyConfig != nil && key.BedrockMantleKeyConfig.ProjectID != nil {
		return key.BedrockMantleKeyConfig.ProjectID.GetValue()
	}
	return ""
}

// parseBedrockRegionAndModel splits a model string that optionally carries an AWS region prefix
// into its region and bare model ID components.
// If no region prefix is present the returned region is empty and bareModel equals model.
func parseBedrockRegionAndModel(model string) (region, bareModel string) {
	if idx := strings.IndexByte(model, '/'); idx > 0 {
		prefix := model[:idx]
		if awsRegionRegex.MatchString(prefix) {
			return prefix, model[idx+1:]
		}
	}
	return "", model
}

// bareModelScope rewrites request.Model (and the model field of a raw passthrough
// body) to the bare id for the duration of a handler call, and returns the func that
// puts both back. Nothing is copied: the caller's request is what core hands to the
// next retry and what pricing and logging read, so it has to leave with its prefix
// intact. The native-Anthropic handlers take the bare model through their build
// config; the OpenAI-compatible ones read it off the request, and mantle 404s the
// prefixed form ("The model 'us-west-2/openai.gpt-6-astra' does not exist"). Costs
// nothing when there is no prefix, which is every request that names a region on
// the key or the alias rather than the model.
func bareModelScope(model *string, raw *[]byte) func() {
	region, bare := parseBedrockRegionAndModel(*model)
	if region == "" {
		return func() {}
	}
	origModel, origRaw := *model, *raw
	*model = bare
	if len(origRaw) > 0 {
		if edited, err := providerUtils.SetJSONField(origRaw, "model", bare); err == nil {
			*raw = edited
		}
	}
	return func() {
		*model, *raw = origModel, origRaw
	}
}

// resolveRegion returns the AWS region to use for a request.
// Priority: model-string region prefix > alias-level Region > key-level
// BedrockMantleKeyConfig.Region > defaultMantleRegion. The model-string prefix
// stays highest since it's the most explicit signal — when an admin types a
// region into their model ID they expect that to win.
func (provider *BedrockMantleProvider) resolveRegion(ctx *schemas.BifrostContext, key schemas.Key, model string) string {
	if region, _ := parseBedrockRegionAndModel(model); region != "" {
		return region
	}
	if ra := schemas.GetResolvedAlias(ctx); ra != nil && ra.Config != nil && ra.Config.Region != nil {
		if v := ra.Config.Region.GetValue(); v != "" {
			return v
		}
	}
	if key.BedrockMantleKeyConfig != nil && key.BedrockMantleKeyConfig.Region != nil && key.BedrockMantleKeyConfig.Region.GetValue() != "" {
		return key.BedrockMantleKeyConfig.Region.GetValue()
	}
	return defaultMantleRegion
}
