package requestguard

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestFingerprintRequiresTenantIdentityAndAlwaysScopesIt(t *testing.T) {
	cfg := DefaultConfig().Coalescer
	cfg.Enabled = true
	cfg.IncludeVirtualKeyScope = false
	cfg.IncludeCustomerScope = false
	req := requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", false)
	features := ExtractRequestFeatures(req)

	if _, eligible, reason := ComputeFingerprint(requestGuardUnscopedTestContext(), req, features, cfg); eligible || !strings.Contains(reason, "tenant identity") {
		t.Fatalf("unscoped fingerprint = (eligible=%v, reason=%q), want safe bypass", eligible, reason)
	}
	first, eligible, reason := ComputeFingerprint(requestGuardTestContextForIdentity("user-a", "key-a", "customer-a"), req, features, cfg)
	if !eligible {
		t.Fatalf("first fingerprint ineligible: %s", reason)
	}
	second, eligible, reason := ComputeFingerprint(requestGuardTestContextForIdentity("user-b", "key-b", "customer-b"), req, features, cfg)
	if !eligible {
		t.Fatalf("second fingerprint ineligible: %s", reason)
	}
	if first == second {
		t.Fatal("mandatory tenant discriminator did not separate request identities")
	}
}

func TestPluginDoesNotCoalesceAcrossTenantsOrWithoutIdentity(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CapabilityGuard.Mode = PolicyOff
	cfg.BudgetGuard.Mode = PolicyOff
	cfg.Coalescer.Enabled = true
	plugin, err := Init(cfg, nil, requestGuardTestCatalog())
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer plugin.Cleanup()

	firstCtx := requestGuardTestContextForIdentity("tenant-a", "key-a", "customer-a")
	secondCtx := requestGuardTestContextForIdentity("tenant-b", "key-b", "customer-b")
	request := requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", false)
	if _, short, err := plugin.PreLLMHook(firstCtx, request); err != nil || short != nil {
		t.Fatalf("first tenant PreLLMHook() = (%+v, %v), want leader", short, err)
	}
	if _, short, err := plugin.PreLLMHook(secondCtx, requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", false)); err != nil || short != nil {
		t.Fatalf("second tenant PreLLMHook() = (%+v, %v), want separate leader", short, err)
	}
	firstHandle, firstOK := flightHandleFromContext(firstCtx)
	secondHandle, secondOK := flightHandleFromContext(secondCtx)
	if !firstOK || !secondOK || firstHandle.flight == secondHandle.flight {
		t.Fatal("different tenants joined the same coalesced flight")
	}

	if _, short, err := plugin.PreLLMHook(requestGuardUnscopedTestContext(), requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", false)); err != nil || short != nil {
		t.Fatalf("unscoped PreLLMHook() = (%+v, %v), want fail-open provider execution", short, err)
	}
	plugin.PostLLMHook(firstCtx, requestGuardTestResponse("first"), nil)
	plugin.PostLLMHook(secondCtx, requestGuardTestResponse("second"), nil)
}

func TestFingerprintBypassesLargePayloadAndCapturesHTTPBypassHeader(t *testing.T) {
	cfg := DefaultConfig().Coalescer
	cfg.Enabled = true
	cfg.BypassHeader = "X-RequestGuard-Bypass"
	features := ExtractRequestFeatures(requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", false))

	largePayloadCtx := requestGuardTestContext()
	largePayloadCtx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
	if _, eligible, reason := ComputeFingerprint(largePayloadCtx, requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", false), features, cfg); eligible || !strings.Contains(reason, "large payload") {
		t.Fatalf("large-payload fingerprint = (eligible=%v, reason=%q), want bypass", eligible, reason)
	}

	ctx := requestGuardTestContext()
	plugin, err := Init(func() Config {
		config := DefaultConfig()
		config.Coalescer.BypassHeader = cfg.BypassHeader
		return config
	}(), nil, requestGuardTestCatalog())
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer plugin.Cleanup()
	if _, err := plugin.HTTPTransportPreHook(ctx, &schemas.HTTPRequest{Headers: map[string]string{"x-requestguard-bypass": ""}}); err != nil {
		t.Fatalf("HTTPTransportPreHook() error = %v", err)
	}
	if _, eligible, reason := ComputeFingerprint(ctx, requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", false), features, cfg); eligible || !strings.Contains(reason, "bypass header") {
		t.Fatalf("header-bypassed fingerprint = (eligible=%v, reason=%q), want bypass", eligible, reason)
	}
}

func TestFingerprintIncludesResponsesInstructions(t *testing.T) {
	cfg := DefaultConfig().Coalescer
	cfg.Enabled = true
	newRequest := func(instructions string) *schemas.BifrostRequest {
		return &schemas.BifrostRequest{
			RequestType: schemas.ResponsesRequest,
			ResponsesRequest: &schemas.BifrostResponsesRequest{
				Provider: schemas.OpenAI,
				Model:    "model",
				Input:    []schemas.ResponsesMessage{{}},
				Params:   &schemas.ResponsesParameters{Instructions: stringPtr(instructions)},
			},
		}
	}
	firstReq, secondReq := newRequest("answer briefly"), newRequest("answer with citations")
	first, firstOK, firstReason := ComputeFingerprint(requestGuardTestContext(), firstReq, ExtractRequestFeatures(firstReq), cfg)
	second, secondOK, secondReason := ComputeFingerprint(requestGuardTestContext(), secondReq, ExtractRequestFeatures(secondReq), cfg)
	if !firstOK || !secondOK {
		t.Fatalf("fingerprints not eligible: first=%q second=%q", firstReason, secondReason)
	}
	if first == second {
		t.Fatal("different Responses instructions produced the same coalescing fingerprint")
	}
	identicalReq := newRequest("answer briefly")
	identical, identicalOK, identicalReason := ComputeFingerprint(requestGuardTestContext(), identicalReq, ExtractRequestFeatures(identicalReq), cfg)
	if !identicalOK || identical != first {
		t.Fatalf("identical Responses instructions did not produce a stable fingerprint: %q %v", identicalReason, identicalOK)
	}
}
