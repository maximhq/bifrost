package requestguard

import (
	"net/http"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestEvaluateCapabilitiesFullCapabilityModelPasses(t *testing.T) {
	cfg := capabilityConfig(PolicyBlock)
	caps := &schemas.ModelCapabilities{
		SupportsFunctionCalling:         boolPtr(true),
		SupportsParallelFunctionCalling: boolPtr(true),
		SupportsToolChoice:              boolPtr(true),
		SupportsForcedToolChoice:        boolPtr(true),
		SupportsResponseSchema:          boolPtr(true),
		SupportsNativeStructuredOutput:  boolPtr(false),
		SupportsResponseSchemaWithTools: boolPtr(true),
		MaxOutputTokens:                 intPtr(4096),
		SupportedEndpoints:              []string{"/v1/chat/completions"},
	}
	catalog := &fakeCatalog{
		capabilities: caps,
		modelInfo: &schemas.Model{
			ContextLength: intPtr(8192),
			Architecture:  &schemas.Architecture{InputModalities: []string{"text", "IMAGE"}},
		},
		requestTypeSupported: false, // The endpoint metadata is the fallback signal.
	}
	features := RequestFeatures{
		RequestType:                       schemas.ChatCompletionStreamRequest,
		Provider:                          schemas.OpenAI,
		Model:                             "model",
		IsStreaming:                       true,
		RequiresTools:                     true,
		ToolCount:                         2,
		RequiresParallelTools:             true,
		RequiresToolChoice:                true,
		RequiresForcedToolChoice:          true,
		RequiresStructuredOutput:          true,
		RequiresStructuredOutputWithTools: true,
		RequiresVision:                    true,
		ImageCount:                        1,
		RequestedMaxOutputTokens:          2048,
		Supported:                         true,
	}

	result := EvaluateCapabilities(catalog, cfg, features)
	if !result.Pass || result.Blocked || result.NeedsDowngrade || len(result.Issues) != 0 || result.Action != CapabilityActionAllow {
		t.Fatalf("EvaluateCapabilities() = %+v, want pass", result)
	}
}

func TestEvaluateCapabilitiesToolSupportModes(t *testing.T) {
	tests := []struct {
		mode       PolicyMode
		wantAction string
		wantSev    string
		wantBlock  bool
	}{
		{mode: PolicyBlock, wantAction: CapabilityActionBlock, wantSev: IssueSeverityBlock, wantBlock: true},
		{mode: PolicyWarn, wantAction: CapabilityActionWarn, wantSev: IssueSeverityWarning},
		{mode: PolicyAudit, wantAction: CapabilityActionAudit, wantSev: IssueSeverityAudit},
	}
	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			cfg := CapabilityGuardConfig{Mode: tt.mode, UnknownPolicy: UnknownCapabilityAllow, CheckTools: true}
			features := RequestFeatures{Supported: true, RequiresTools: true, ToolCount: 1, Model: "model"}
			result := EvaluateCapabilities(&fakeCatalog{capabilities: &schemas.ModelCapabilities{SupportsFunctionCalling: boolPtr(false)}}, cfg, features)
			if len(result.Issues) != 1 {
				t.Fatalf("issues = %+v, want exactly one", result.Issues)
			}
			if result.Action != tt.wantAction || result.Blocked != tt.wantBlock || result.Pass == tt.wantBlock {
				t.Errorf("result = %+v, want action=%s blocked=%v", result, tt.wantAction, tt.wantBlock)
			}
			if result.Issues[0].Capability != "tools" || result.Issues[0].Severity != tt.wantSev || result.Issues[0].Unknown {
				t.Errorf("issue = %+v, want known %s issue", result.Issues[0], tt.wantSev)
			}
		})
	}
}

func TestEvaluateCapabilitiesMissingStructuredOutput(t *testing.T) {
	cfg := CapabilityGuardConfig{Mode: PolicyBlock, UnknownPolicy: UnknownCapabilityAllow, CheckStructuredOutput: true}
	features := RequestFeatures{Supported: true, RequiresStructuredOutput: true, Model: "model"}
	catalog := &fakeCatalog{capabilities: &schemas.ModelCapabilities{
		SupportsResponseSchema:         boolPtr(false),
		SupportsNativeStructuredOutput: boolPtr(false),
	}}
	result := EvaluateCapabilities(catalog, cfg, features)
	if !result.Blocked || len(result.Issues) != 1 || result.Issues[0].Capability != "structured_output" {
		t.Fatalf("EvaluateCapabilities() = %+v, want structured-output block", result)
	}
}

func TestEvaluateCapabilitiesMissingVisionModality(t *testing.T) {
	cfg := CapabilityGuardConfig{Mode: PolicyBlock, UnknownPolicy: UnknownCapabilityAllow, CheckVision: true}
	features := RequestFeatures{Supported: true, RequiresVision: true, ImageCount: 1, Model: "model"}
	catalog := &fakeCatalog{modelInfo: &schemas.Model{
		Architecture: &schemas.Architecture{InputModalities: []string{"text"}},
	}}
	result := EvaluateCapabilities(catalog, cfg, features)
	if !result.Blocked || len(result.Issues) != 1 || result.Issues[0].Capability != "vision" {
		t.Fatalf("EvaluateCapabilities() = %+v, want vision block", result)
	}
}

func TestEvaluateCapabilitiesStreamingSupportAndUnknownPolicy(t *testing.T) {
	cfg := CapabilityGuardConfig{Mode: PolicyBlock, UnknownPolicy: UnknownCapabilityBlock, CheckStreaming: true}
	features := RequestFeatures{RequestType: schemas.ResponsesStreamRequest, IsStreaming: true, Supported: true, Model: "model"}
	knownUnsupported := EvaluateCapabilities(&fakeCatalog{modelInfo: &schemas.Model{ID: "model"}}, cfg, features)
	if !knownUnsupported.Blocked || len(knownUnsupported.Issues) != 1 || knownUnsupported.Issues[0].Unknown {
		t.Fatalf("known unsupported stream result = %+v", knownUnsupported)
	}

	for _, tt := range []struct {
		policy    UnknownCapabilityPolicy
		wantCount int
		wantBlock bool
		wantSev   string
	}{
		{policy: UnknownCapabilityAllow, wantCount: 0},
		{policy: UnknownCapabilityWarn, wantCount: 1, wantSev: IssueSeverityWarning},
		{policy: UnknownCapabilityBlock, wantCount: 1, wantBlock: true, wantSev: IssueSeverityBlock},
	} {
		t.Run(string(tt.policy), func(t *testing.T) {
			unknownConfig := cfg
			unknownConfig.UnknownPolicy = tt.policy
			result := EvaluateCapabilities(&fakeCatalog{}, unknownConfig, features)
			if len(result.Issues) != tt.wantCount || result.Blocked != tt.wantBlock {
				t.Fatalf("result = %+v, want issue count %d blocked=%v", result, tt.wantCount, tt.wantBlock)
			}
			if tt.wantCount > 0 && (!result.Issues[0].Unknown || result.Issues[0].Capability != "streaming" || result.Issues[0].Severity != tt.wantSev) {
				t.Errorf("unknown issue = %+v", result.Issues[0])
			}
		})
	}
}

func TestEvaluateCapabilitiesMaxOutputTokensExceeded(t *testing.T) {
	cfg := CapabilityGuardConfig{Mode: PolicyBlock, UnknownPolicy: UnknownCapabilityAllow, CheckMaxOutputTokens: true}
	features := RequestFeatures{Supported: true, Model: "model", RequestedMaxOutputTokens: 4097}
	catalog := &fakeCatalog{capabilities: &schemas.ModelCapabilities{MaxOutputTokens: intPtr(4096)}}
	result := EvaluateCapabilities(catalog, cfg, features)
	if !result.Blocked || len(result.Issues) != 1 || result.Issues[0].Capability != "max_output_tokens" {
		t.Fatalf("EvaluateCapabilities() = %+v, want max output block", result)
	}
}

func TestEvaluateCapabilitiesNilCatalogFailsOpen(t *testing.T) {
	cfg := capabilityConfig(PolicyBlock)
	features := RequestFeatures{Supported: true, RequiresTools: true, Model: "model"}
	result := EvaluateCapabilities(nil, cfg, features)
	if !result.Pass || result.Blocked || result.NeedsDowngrade || len(result.Issues) != 0 {
		t.Fatalf("EvaluateCapabilities(nil, ...) = %+v, want fail-open pass", result)
	}
}

func TestUnknownCapabilityBlockSeverityFollowsMode(t *testing.T) {
	for _, test := range []struct {
		mode PolicyMode
		want string
	}{
		{mode: PolicyBlock, want: IssueSeverityBlock},
		{mode: PolicyDowngrade, want: IssueSeverityDowngrade},
		{mode: PolicyWarn, want: IssueSeverityWarning},
		{mode: PolicyAudit, want: IssueSeverityAudit},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			cfg := CapabilityGuardConfig{Mode: test.mode, UnknownPolicy: UnknownCapabilityBlock, CheckTools: true}
			features := RequestFeatures{Supported: true, RequiresTools: true, Model: "unknown"}
			result := EvaluateCapabilities(nil, cfg, features)
			if len(result.Issues) != 1 || result.Issues[0].Severity != test.want {
				t.Fatalf("unknown capability result = %+v, want severity %s", result, test.want)
			}
			cfg.CheckContextLimit = true
			contextIssue := EvaluateContextLimit(nil, cfg, features, CostEstimate{})
			if contextIssue == nil || contextIssue.Severity != test.want {
				t.Fatalf("unknown context-limit issue = %+v, want severity %s", contextIssue, test.want)
			}
		})
	}
}

func TestEvaluateCapabilitiesNoOpForUnsupportedAndOff(t *testing.T) {
	catalog := &fakeCatalog{}
	features := RequestFeatures{Supported: false}
	result := EvaluateCapabilities(catalog, capabilityConfig(PolicyBlock), features)
	if !result.Pass || len(result.Issues) != 0 {
		t.Fatalf("unsupported feature result = %+v, want pass", result)
	}
	features.Supported = true
	result = EvaluateCapabilities(catalog, capabilityConfig(PolicyOff), features)
	if !result.Pass || len(result.Issues) != 0 {
		t.Fatalf("off mode result = %+v, want pass", result)
	}
}

func TestBlockingErrorAndGuardrailIntervention(t *testing.T) {
	err := NewBlockingError("  capability is unavailable  ", false)
	if !err.IsBifrostError || err.StatusCode == nil || *err.StatusCode != http.StatusBadRequest || err.Error == nil || err.Error.Message != "capability is unavailable" {
		t.Fatalf("blocking error = %+v", err)
	}
	if err.AllowFallbacks == nil || *err.AllowFallbacks || err.ExtraFields.ErrorType != schemas.ErrorTypePolicyModelBlocked {
		t.Fatalf("blocking error fallback/type fields = %+v", err)
	}

	resp := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{}}
	longReason := strings.Repeat("x", 300)
	if !AppendGuardrailIntervention(resp, schemas.BifrostGuardrailIntervention{Reason: longReason}) {
		t.Fatal("AppendGuardrailIntervention() = false, want true")
	}
	metadata := resp.GetExtraFields().GuardrailDebug
	if metadata == nil || len(metadata.Interventions) != 1 {
		t.Fatalf("intervention metadata = %+v", metadata)
	}
	intervention := metadata.Interventions[0]
	if intervention.Phase != "request" || intervention.GuardrailName != "request_guard" || len(intervention.Reason) > 256 {
		t.Errorf("intervention = %+v", intervention)
	}
	if AppendGuardrailIntervention(nil, schemas.BifrostGuardrailIntervention{Reason: "reason"}) {
		t.Fatal("AppendGuardrailIntervention(nil, ...) = true, want false")
	}
}

func capabilityConfig(mode PolicyMode) CapabilityGuardConfig {
	return CapabilityGuardConfig{
		Mode:                  mode,
		UnknownPolicy:         UnknownCapabilityAllow,
		CheckTools:            true,
		CheckParallelTools:    true,
		CheckToolChoice:       true,
		CheckStructuredOutput: true,
		CheckVision:           true,
		CheckStreaming:        true,
		CheckContextLimit:     true,
		CheckMaxOutputTokens:  true,
	}
}

func BenchmarkEvaluateCapabilities(b *testing.B) {
	maxOutput := 4096
	config := capabilityConfig(PolicyBlock)
	features := RequestFeatures{
		RequestType:                       schemas.ChatCompletionRequest,
		Provider:                          schemas.OpenAI,
		Model:                             "model",
		RequiresTools:                     true,
		ToolCount:                         2,
		RequiresParallelTools:             true,
		RequiresToolChoice:                true,
		RequiresForcedToolChoice:          true,
		RequiresStructuredOutput:          true,
		RequiresStructuredOutputWithTools: true,
		RequiresVision:                    true,
		ImageCount:                        1,
		RequestedMaxOutputTokens:          2048,
		Supported:                         true,
	}
	catalog := &fakeCatalog{
		capabilities: &schemas.ModelCapabilities{
			SupportsFunctionCalling:         boolPtr(true),
			SupportsParallelFunctionCalling: boolPtr(true),
			SupportsToolChoice:              boolPtr(true),
			SupportsForcedToolChoice:        boolPtr(true),
			SupportsResponseSchema:          boolPtr(true),
			SupportsResponseSchemaWithTools: boolPtr(true),
			MaxOutputTokens:                 &maxOutput,
		},
		modelInfo: &schemas.Model{
			ContextLength: intPtr(8192),
			Architecture:  &schemas.Architecture{InputModalities: []string{"text", "image"}},
		},
		requestTypeSupported: true,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EvaluateCapabilities(catalog, config, features)
	}
}
