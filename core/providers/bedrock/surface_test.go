package bedrock

import (
	"context"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func surfaceTestCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

// withAlias returns a context carrying a resolved alias, optionally with a
// per-deployment inference profile ARN.
func withAlias(aliasKey, modelID, profileARN string) *schemas.BifrostContext {
	ctx := surfaceTestCtx()
	cfg := &schemas.AliasConfig{ModelID: modelID}
	if profileARN != "" {
		cfg.BedrockAliasCfg = &schemas.BedrockAliasCfg{InferenceProfileARN: schemas.NewSecretVar(profileARN)}
	}
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: aliasKey, Config: cfg})
	return ctx
}

func keyWithARN(arn string) schemas.Key {
	if arn == "" {
		return schemas.Key{}
	}
	return schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{ARN: schemas.NewSecretVar(arn)}}
}

// installCaps registers a datasheet stub for the duration of the test.
func installCaps(t *testing.T, rows map[schemas.ModelProvider]map[string][]schemas.BedrockAPI) {
	t.Helper()
	schemas.SetCapabilityResolver(func(provider schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		apis, ok := rows[provider][model]
		if !ok {
			return nil
		}
		return &schemas.ModelCapabilities{BedrockAPIs: apis}
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
}

const (
	appProfileARN    = "arn:aws:bedrock:us-east-1:791152688819:application-inference-profile"
	appProfileFullID = "arn:aws:bedrock:us-east-1:791152688819:application-inference-profile/3dnkdwuaalc7"
	sysProfileARN    = "arn:aws:bedrock:us-east-1:791152688819:inference-profile"
)

func TestBedrockARNResourceType(t *testing.T) {
	cases := []struct {
		arn  string
		want string
	}{
		{appProfileARN, "application-inference-profile"},
		{appProfileFullID, "application-inference-profile"},
		{sysProfileARN, "inference-profile"},
		{"arn:aws:bedrock:us-east-1:123:inference-profile/global.openai.gpt-5.6-luna", "inference-profile"},
		{"arn:aws:bedrock:us-east-1::foundation-model/anthropic.claude-v2", "foundation-model"},
		// Not ARNs.
		{"openai.gpt-5.6-luna", ""},
		{"3dnkdwuaalc7", ""},
		{"", ""},
		// Malformed: too few segments to carry a resource type.
		{"arn:aws:bedrock:us-east-1", ""},
	}
	for _, tc := range cases {
		if got := bedrockARNResourceType(tc.arn); got != tc.want {
			t.Errorf("bedrockARNResourceType(%q) = %q, want %q", tc.arn, got, tc.want)
		}
	}
}

// A system-defined profile ARN must not be mistaken for an application profile:
// "inference-profile" is a suffix of "application-inference-profile", so a
// substring test would divert cross-region configs to the wrong endpoint.
func TestIsApplicationInferenceProfileARN(t *testing.T) {
	if !isApplicationInferenceProfileARN(appProfileARN) {
		t.Error("application profile ARN should be recognised")
	}
	if isApplicationInferenceProfileARN(sysProfileARN) {
		t.Error("system-defined profile ARN must NOT be treated as an application profile")
	}
	if isApplicationInferenceProfileARN("arn:aws:bedrock:us-east-1:123:inference-profile/us.anthropic.claude-opus-4-8") {
		t.Error("system-defined profile ARN with resource id must NOT match")
	}
}

func TestHasBedrockCrossRegionPrefix(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"us.anthropic.claude-opus-4-8", true},
		{"eu.amazon.nova-pro-v1:0", true},
		{"apac.anthropic.claude-3-5-sonnet-20241022-v2:0", true},
		{"au.anthropic.claude-opus-4-6-v1", true},
		{"jp.anthropic.claude-haiku-4-5-20251001-v1:0", true},
		{"global.openai.gpt-5.6-luna", true},
		{"us-gov.anthropic.claude-opus-4-8", true},
		{"in.openai.gpt-5.6-luna", true},
		// Region prefix is Bifrost syntax, stripped before the check.
		{"us-west-2/global.xai.grok-4.6", true},
		{"us-gov-west-1/openai.gpt-oss-120b", false},
		// Vendor segments must not be mistaken for geo prefixes.
		{"openai.gpt-5.6-luna", false},
		{"xai.grok-4.6", false},
		{"anthropic.claude-opus-5", false},
		{"amazon.titan-text-express-v1", false},
		{"3dnkdwuaalc7", false},
	}
	for _, tc := range cases {
		if got := hasBedrockCrossRegionPrefix(tc.model); got != tc.want {
			t.Errorf("hasBedrockCrossRegionPrefix(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestResolveBedrockSurface(t *testing.T) {
	cases := []struct {
		name       string
		ctx        *schemas.BifrostContext
		key        schemas.Key
		model      string
		wantMantle bool
		wantReason bedrockSurfaceReason
	}{
		{
			// The reported bug: an alias-level application profile on a gpt model.
			// AWS rejects application profiles on Responses/ChatCompletions on both
			// endpoints, so Converse on bedrock-runtime is the only surface.
			name:       "alias application profile pins runtime",
			ctx:        withAlias("gpt-model", "3dnkdwuaalc7", appProfileARN),
			model:      "3dnkdwuaalc7",
			wantMantle: false,
			wantReason: reasonApplicationProfile,
		},
		{
			// A system-defined profile ARN is not an application profile and must
			// not trigger the divert.
			name:       "alias system profile does not pin runtime",
			ctx:        withAlias("gpt-model", "openai.gpt-5.6-luna", sysProfileARN),
			model:      "openai.gpt-5.6-luna",
			wantMantle: true,
			wantReason: reasonModelFamilyFallback,
		},
		{
			name:       "key application profile pins runtime for an opaque id",
			ctx:        withAlias("claude-alias", "abc12xyz", ""),
			key:        keyWithARN(appProfileARN),
			model:      "abc12xyz",
			wantMantle: false,
			wantReason: reasonApplicationProfile,
		},
		{
			// A key-level ARN diverts exactly like an alias-level one when the
			// deployment names a profile resource id. The alias key naming a gpt
			// model must not pull it back to mantle.
			name:       "key application profile diverts a gpt-named alias",
			ctx:        withAlias("gpt-5.6-luna", "3dnkdwuaalc7", ""),
			key:        keyWithARN(appProfileARN),
			model:      "3dnkdwuaalc7",
			wantMantle: false,
			wantReason: reasonApplicationProfile,
		},
		{
			// Grok on Converse: mantle has no cross-region identifiers, so this
			// 404s there today.
			name:       "cross-region grok id pins runtime",
			ctx:        surfaceTestCtx(),
			model:      "us.xai.grok-4.6",
			wantMantle: false,
			wantReason: reasonCrossRegionIdentifier,
		},
		{
			name:       "cross-region gpt id pins runtime",
			ctx:        surfaceTestCtx(),
			model:      "global.openai.gpt-5.6-luna",
			wantMantle: false,
			wantReason: reasonCrossRegionIdentifier,
		},
		{
			// The existing-user case: a bare mantle id stays on mantle.
			name:       "bare mantle id stays on mantle",
			ctx:        withAlias("gpt-model", "openai.gpt-5.6-luna", ""),
			model:      "openai.gpt-5.6-luna",
			wantMantle: true,
			wantReason: reasonModelFamilyFallback,
		},
		{
			name:       "claude stays on runtime",
			ctx:        surfaceTestCtx(),
			model:      "anthropic.claude-opus-5",
			wantMantle: false,
			wantReason: reasonModelFamilyFallback,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveBedrockSurface(tc.ctx, tc.key, tc.model)
			if got.isMantle() != tc.wantMantle {
				t.Errorf("isMantle = %v, want %v (reason %q)", got.isMantle(), tc.wantMantle, got.reason)
			}
			if got.reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", got.reason, tc.wantReason)
			}
		})
	}
}

func TestResolveBedrockSurfaceDatasheet(t *testing.T) {
	installCaps(t, map[schemas.ModelProvider]map[string][]schemas.BedrockAPI{
		schemas.Bedrock: {
			// Runtime-only: a Converse model the family match would not divert.
			"amazon.nova-pro-v1:0": {schemas.BedrockAPIConverse},
			// Ambiguous — rows on both providers, as Claude and gpt-oss really have.
			"anthropic.claude-opus-5": {schemas.BedrockAPIConverse, schemas.BedrockAPIMessages},
			"openai.gpt-oss-120b":     {schemas.BedrockAPIConverse},
		},
		schemas.BedrockMantle: {
			"google.gemma-4-31b":      {schemas.BedrockAPIChatCompletions, schemas.BedrockAPIResponses},
			"anthropic.claude-opus-5": {schemas.BedrockAPIResponses},
			"openai.gpt-oss-120b":     {schemas.BedrockAPIChatCompletions},
		},
	})

	cases := []struct {
		name       string
		model      string
		wantMantle bool
		wantReason bedrockSurfaceReason
	}{
		{"runtime-only row decides", "amazon.nova-pro-v1:0", false, reasonDatasheetRuntimeOnly},
		{"mantle-only row decides", "google.gemma-4-31b", true, reasonDatasheetMantleOnly},
		// Rows on both providers are not a signal: falling through to the family
		// match is what keeps Claude on Converse and gpt-oss on mantle.
		{"both rows fall through to family (claude)", "anthropic.claude-opus-5", false, reasonModelFamilyFallback},
		{"both rows fall through to family (gpt-oss)", "openai.gpt-oss-120b", true, reasonModelFamilyFallback},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveBedrockSurface(surfaceTestCtx(), schemas.Key{}, tc.model)
			if got.isMantle() != tc.wantMantle || got.reason != tc.wantReason {
				t.Errorf("got (mantle=%v, reason=%q), want (mantle=%v, reason=%q)",
					got.isMantle(), got.reason, tc.wantMantle, tc.wantReason)
			}
		})
	}
}

// Unrecognised API names must be skipped, not treated as a claim: the datasheet
// ships ahead of the binaries that read it.
func TestResolveBedrockSurfaceIgnoresUnknownAPIs(t *testing.T) {
	installCaps(t, map[schemas.ModelProvider]map[string][]schemas.BedrockAPI{
		schemas.Bedrock: {"openai.gpt-5.6-luna": {schemas.BedrockAPI("batch")}},
	})
	got := resolveBedrockSurface(surfaceTestCtx(), schemas.Key{}, "openai.gpt-5.6-luna")
	if !got.isMantle() || got.reason != reasonModelFamilyFallback {
		t.Errorf("unknown API should not decide: got (mantle=%v, reason=%q)", got.isMantle(), got.reason)
	}
}

// Back-compat lock. With no datasheet installed, every identifier that is not a
// cross-region id or an ARN must route exactly where isMantleModel sends it
// today. Cross-region ids are the one deliberate change: they 404 on mantle, so
// no working configuration can depend on the old answer.
func TestResolveBedrockSurfaceBackCompat(t *testing.T) {
	models := []string{
		"gpt-oss-120b", "openai.gpt-oss-20b", "gpt-oss-safeguard-120b",
		"gpt-5.5", "openai.gpt-5.4", "openai.gpt-5.6-luna",
		"gemma-4-31b", "google.gemma-4-e2b", "gemma-4-26b-a4b",
		"gemma-3-12b-it", "google.gemma-3-27b-it",
		"xai.grok-4.3", "xai.grok-4.6",
		"claude-opus-4-8", "anthropic.claude-3-5-sonnet-20240620-v1:0",
		"amazon.titan-text-express-v1", "amazon.nova-pro-v1:0",
		"meta.llama3-70b-instruct-v1:0", "cohere.command-r-plus-v1:0",
		"3dnkdwuaalc7",
	}
	ctx := surfaceTestCtx()
	for _, model := range models {
		if hasBedrockCrossRegionPrefix(model) || bedrockARNResourceType(model) != "" {
			t.Fatalf("test list must contain no cross-region or ARN ids, got %q", model)
		}
		want := isMantleModel(ctx, model)
		got := resolveBedrockSurface(ctx, schemas.Key{}, model)
		if got.isMantle() != want {
			t.Errorf("%q: surface routed to mantle=%v, but isMantleModel says %v (reason %q)",
				model, got.isMantle(), want, got.reason)
		}
	}
}

// ---- max_output_tokens floor ----

func capsFor(t *testing.T, model string, min *int) schemas.ModelCaps {
	t.Helper()
	if min != nil {
		schemas.SetCapabilityResolver(func(_ schemas.ModelProvider, m string) *schemas.ModelCapabilities {
			if m != model {
				return nil
			}
			return &schemas.ModelCapabilities{MinOutputTokens: min}
		})
		t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })
	}
	return schemas.ResolveModelCaps(schemas.Bedrock, model)
}

// Bedrock serves the OpenAI family and Grok from an OpenAI-compatible backend
// that rejects max_output_tokens below 16; Claude and Nova return a single token
// happily, so clamping them would silently inflate a legitimate request.
func TestClampMaxTokensFamilyFallback(t *testing.T) {
	cases := []struct {
		model string
		given int
		want  int
	}{
		// Raised to the floor.
		{"openai.gpt-5.6-luna", 1, 16},
		{"global.openai.gpt-5.6-luna", 1, 16},
		{"xai.grok-4.6", 1, 16},
		{"us.xai.grok-4.6", 15, 16},
		// Already at or above the floor — untouched.
		{"openai.gpt-5.6-luna", 16, 16},
		{"openai.gpt-5.6-luna", 4096, 4096},
		// No floor: a 1-token request is valid and must survive.
		{"anthropic.claude-sonnet-4-5-20250929-v1:0", 1, 1},
		{"us.anthropic.claude-opus-4-8", 1, 1},
		{"amazon.nova-pro-v1:0", 1, 1},
		{"meta.llama3-70b-instruct-v1:0", 1, 1},
	}
	for _, tc := range cases {
		caps := capsFor(t, tc.model, nil)
		got := clampMaxTokens(surfaceTestCtx(), &tc.given, caps)
		if got == nil || *got != tc.want {
			t.Errorf("clampMaxTokens(%d, %q) = %v, want %d", tc.given, tc.model, got, tc.want)
		}
	}
}

// The datasheet is authoritative over the family guess, in both directions.
func TestClampMaxTokensDatasheetOverridesFamily(t *testing.T) {
	t.Run("raises a model the family guess would not", func(t *testing.T) {
		floor := 32
		caps := capsFor(t, "amazon.nova-pro-v1:0", &floor)
		given := 1
		if got := clampMaxTokens(surfaceTestCtx(), &given, caps); got == nil || *got != 32 {
			t.Errorf("got %v, want 32", got)
		}
	})
	t.Run("clears the floor the family guess would apply", func(t *testing.T) {
		floor := 0
		caps := capsFor(t, "openai.gpt-5.6-luna", &floor)
		given := 1
		if got := clampMaxTokens(surfaceTestCtx(), &given, caps); got == nil || *got != 1 {
			t.Errorf("got %v, want 1 (datasheet says no floor)", got)
		}
	})
}

func TestClampMaxTokensNil(t *testing.T) {
	if got := clampMaxTokens(surfaceTestCtx(), nil, capsFor(t, "openai.gpt-5.6-luna", nil)); got != nil {
		t.Errorf("nil max tokens should stay nil, got %v", got)
	}
}

// An application inference profile alias carries an opaque resource id as its
// model_id, so the floor has to be recognised from the alias chain rather than
// the wire id alone — this is the reported Claude Code configuration.
func TestClampMaxTokensRecognisesAliasedProfile(t *testing.T) {
	given := 1
	t.Run("alias key names the model", func(t *testing.T) {
		ctx := withAlias("gpt-5.6-luna", "3dnkdwuaalc7", appProfileARN)
		caps := schemas.ResolveModelCaps(schemas.Bedrock, schemas.ResolveCanonicalModel(ctx, "3dnkdwuaalc7"))
		if got := clampMaxTokens(ctx, &given, caps); got == nil || *got != 16 {
			t.Errorf("got %v, want 16", got)
		}
	})
	t.Run("opaque id with no alias has no floor to find", func(t *testing.T) {
		ctx := surfaceTestCtx()
		caps := schemas.ResolveModelCaps(schemas.Bedrock, "3dnkdwuaalc7")
		if got := clampMaxTokens(ctx, &given, caps); got == nil || *got != 1 {
			t.Errorf("got %v, want 1 (nothing identifies the model)", got)
		}
	})
}

// ---- Converse reasoning shape ----

// reasoningFields converts a Responses request and returns the
// additionalModelRequestFields the Converse body would carry.
func reasoningFields(t *testing.T, ctx *schemas.BifrostContext, model string, reasoning *schemas.ResponsesParametersReasoning) map[string]any {
	t.Helper()
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Bedrock,
		Model:    model,
		Input:    []schemas.ResponsesMessage{{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")}}},
		Params:   &schemas.ResponsesParameters{Reasoning: reasoning},
	}
	out, err := ToBedrockResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("convert %q: %v", model, err)
	}
	if out.AdditionalModelRequestFields == nil {
		return map[string]any{}
	}
	return out.AdditionalModelRequestFields.ToMap()
}

// gpt-5.6 accepts only additionalModelRequestFields.reasoning={effort} and
// rejects Nova's reasoningConfig outright; Grok accepts every shape but honors
// only this one. Everything else on Converse either rejects the field (DeepSeek
// R1) or ignores it, so it must be omitted entirely.
func TestConverseReasoningShape(t *testing.T) {
	effort := func(e string) *schemas.ResponsesParametersReasoning {
		return &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr(e)}
	}
	cases := []struct {
		name      string
		model     string
		reasoning *schemas.ResponsesParametersReasoning
		wantKey   string // "" ⇒ nothing emitted
		wantValue map[string]any
	}{
		{"openai effort high", "global.openai.gpt-5.6-luna", effort("high"), "reasoning", map[string]any{"effort": "high"}},
		{"grok effort high", "us.xai.grok-4.6", effort("high"), "reasoning", map[string]any{"effort": "high"}},
		// minimal is rejected by both; the ladder snaps it to the nearest rung.
		{"openai effort minimal snaps to low", "global.openai.gpt-5.6-luna", effort("minimal"), "reasoning", map[string]any{"effort": "low"}},
		{"grok effort minimal snaps to low", "us.xai.grok-4.6", effort("minimal"), "reasoning", map[string]any{"effort": "low"}},
		// max is accepted by gpt but rejected by Grok, which tops out at xhigh.
		{"openai effort max kept", "global.openai.gpt-5.6-luna", effort("max"), "reasoning", map[string]any{"effort": "max"}},
		{"grok effort max snaps to xhigh", "us.xai.grok-4.6", effort("max"), "reasoning", map[string]any{"effort": "xhigh"}},
		// Disable must use the honored shape — reasoningConfig is ignored by Grok,
		// so the caller would silently keep getting (and paying for) reasoning.
		{"openai effort none", "global.openai.gpt-5.6-luna", effort("none"), "reasoning", map[string]any{"effort": "none"}},
		{"grok effort none", "us.xai.grok-4.6", effort("none"), "reasoning", map[string]any{"effort": "none"}},
		// Families whose reasoning Converse cannot express get nothing at all.
		{"deepseek gets nothing", "us.deepseek.r1-v1:0", effort("high"), "", nil},
		{"qwen gets nothing", "qwen.qwen3-32b-v1:0", effort("high"), "", nil},
		{"glm gets nothing", "zai.glm-5", effort("high"), "", nil},
		{"gemma gets nothing", "google.gemma-3-12b-it", effort("high"), "", nil},
		{"deepseek disable gets nothing", "us.deepseek.r1-v1:0", effort("none"), "", nil},
		{"glm disable gets nothing", "zai.glm-5", effort("none"), "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := reasoningFields(t, surfaceTestCtx(), tc.model, tc.reasoning)
			if _, hasNova := fields["reasoningConfig"]; hasNova {
				t.Errorf("must never send Nova's reasoningConfig to %q: %v", tc.model, fields)
			}
			if tc.wantKey == "" {
				if len(fields) != 0 {
					t.Errorf("expected no additionalModelRequestFields, got %v", fields)
				}
				return
			}
			got, ok := fields[tc.wantKey]
			if !ok {
				t.Fatalf("missing %q in %v", tc.wantKey, fields)
			}
			gotMap, _ := got.(map[string]any)
			for k, want := range tc.wantValue {
				if gotMap[k] != want {
					t.Errorf("%s.%s = %v, want %v", tc.wantKey, k, gotMap[k], want)
				}
			}
		})
	}
}

// Anthropic and Nova keep their own shapes.
func TestConverseReasoningLeavesAnthropicAndNovaAlone(t *testing.T) {
	r := &schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("high")}
	if f := reasoningFields(t, surfaceTestCtx(), "us.anthropic.claude-sonnet-4-5-20250929-v1:0", r); f["thinking"] == nil {
		t.Errorf("anthropic should still get thinking, got %v", f)
	}
	if f := reasoningFields(t, surfaceTestCtx(), "us.amazon.nova-pro-v1:0", r); f["reasoningConfig"] == nil {
		t.Errorf("nova should still get reasoningConfig, got %v", f)
	}
}

// An alias or application inference profile hides the family behind an opaque
// wire id. IsOpenAIModelFamily walks the alias chain by itself, but IsGrokModel
// is a bare substring match, so passing the raw request model dropped reasoning
// for aliased Grok entirely — enabled and disabled alike.
func TestConverseReasoningResolvesAliasedGrok(t *testing.T) {
	aliasedGrok := func(t *testing.T) *schemas.BifrostContext {
		t.Helper()
		ctx := withAlias("my-grok", "3dnkdwuaalc7", appProfileARN)
		ra := schemas.GetResolvedAlias(ctx)
		name := "grok-4.6"
		ra.Config.ModelName = &name
		return ctx
	}
	cases := []struct {
		name       string
		effort     string
		wantEffort string
	}{
		{"enabled", "high", "high"},
		{"clamped to grok's ladder", "max", "xhigh"},
		{"disabled", "none", "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := reasoningFields(t, aliasedGrok(t), "3dnkdwuaalc7",
				&schemas.ResponsesParametersReasoning{Effort: schemas.Ptr(tc.effort)})
			got, ok := fields["reasoning"]
			if !ok {
				t.Fatalf("aliased Grok must still get a reasoning field, got %v", fields)
			}
			if m, _ := got.(map[string]any); m["effort"] != tc.wantEffort {
				t.Errorf("effort = %v, want %q", m["effort"], tc.wantEffort)
			}
		})
	}
}

// The same alias shape must keep the OpenAI arm working (it resolves via the
// alias chain rather than the wire id too).
func TestConverseReasoningResolvesAliasedOpenAI(t *testing.T) {
	ctx := withAlias("my-gpt", "3dnkdwuaalc7", appProfileARN)
	ra := schemas.GetResolvedAlias(ctx)
	name := "gpt-5.6-luna"
	ra.Config.ModelName = &name
	fields := reasoningFields(t, ctx, "3dnkdwuaalc7",
		&schemas.ResponsesParametersReasoning{Effort: schemas.Ptr("max")})
	got, ok := fields["reasoning"]
	if !ok {
		t.Fatalf("aliased gpt must get a reasoning field, got %v", fields)
	}
	// gpt accepts "max"; only Grok clamps it.
	if m, _ := got.(map[string]any); m["effort"] != "max" {
		t.Errorf("effort = %v, want max", m["effort"])
	}
}

// A profile ARN is a prefix joined as {arn}/{model_id}, so it only applies to a
// deployment whose model_id is the profile's resource id. A key-level ARN spans
// every model on the credential, so a deployment naming a real model must keep
// routing exactly as it did before the ARN existed — AWS rejects the
// concatenated identifier ("The provided model identifier is invalid").
func TestApplicationProfileOnlyDivertsProfileResourceIDs(t *testing.T) {
	cases := []struct {
		name       string
		modelID    string
		wantMantle bool
		wantReason bedrockSurfaceReason
	}{
		{"profile resource id diverts", "3dnkdwuaalc7", false, reasonApplicationProfile},
		{"short profile id diverts", "abc12xyz", false, reasonApplicationProfile},
		// Real model ids on the same key are untouched by its profile ARN.
		{"mantle model id stays on mantle", "openai.gpt-5.6-luna", true, reasonModelFamilyFallback},
		{"gemma-4 stays on mantle", "gemma-4-31b", true, reasonModelFamilyFallback},
		{"grok id stays on mantle", "xai.grok-4.6", true, reasonModelFamilyFallback},
		{"claude id stays on runtime", "anthropic.claude-opus-4-8", false, reasonModelFamilyFallback},
		{"versioned id stays put", "qwen.qwen3-32b-v1:0", false, reasonModelFamilyFallback},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, level := range []string{"key", "alias"} {
				ctx, key := surfaceTestCtx(), schemas.Key{}
				if level == "key" {
					key = keyWithARN(appProfileARN)
				} else {
					ctx = withAlias("d", tc.modelID, appProfileARN)
				}
				got := resolveBedrockSurface(ctx, key, tc.modelID)
				if got.isMantle() != tc.wantMantle || got.reason != tc.wantReason {
					t.Errorf("%s-level: got (mantle=%v, %q), want (mantle=%v, %q)",
						level, got.isMantle(), got.reason, tc.wantMantle, tc.wantReason)
				}
			}
		})
	}
}

func TestIsAIPResourceID(t *testing.T) {
	for _, id := range []string{"3dnkdwuaalc7", "53zk8ewxcsfh", "abc12xyz"} {
		if !isAIPResourceID(id) {
			t.Errorf("%q should look like a profile resource id", id)
		}
	}
	for _, id := range []string{
		"openai.gpt-5.6-luna", "anthropic.claude-opus-4-8", "gemma-4-31b",
		"zai.glm-5", "qwen.qwen3-32b-v1:0", "deepseek.v3.2", "us.xai.grok-4.6",
		"moonshotai.kimi-k2-thinking", "amazon.nova-pro-v1:0", "",
	} {
		if isAIPResourceID(id) {
			t.Errorf("%q is a real model id, must not look like a profile resource id", id)
		}
	}
}
