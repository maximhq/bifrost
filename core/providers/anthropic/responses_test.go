package anthropic

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// makeResponsesTextFormat returns a minimal json_schema text config for the
// Responses API structured-output request path.
func makeResponsesTextFormat(schemaName string) *schemas.ResponsesTextConfig {
	properties := map[string]any{
		"color":  map[string]interface{}{"type": "string"},
		"animal": map[string]interface{}{"type": "string"},
	}
	return &schemas.ResponsesTextConfig{
		Format: &schemas.ResponsesTextConfigFormat{
			Type: "json_schema",
			Name: schemas.Ptr(schemaName),
			JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
				Type:       schemas.Ptr("object"),
				Properties: schemas.OrderedMapFromMap(properties),
				Required:   []string{"color", "animal"},
			},
		},
	}
}

// TestToAnthropicResponsesRequest_StructuredOutput_ToolConversion verifies that,
// mirroring the Chat Completions path, providers whose native Anthropic endpoint
// rejects output_config.format get structured output converted into a synthetic
// bf_so_*/json_response tool instead. Any provider added to toolConversionProviders
// in the future must also be added to the branch under test in responses.go.
func TestToAnthropicResponsesRequest_StructuredOutput_ToolConversion(t *testing.T) {
	for _, provider := range toolConversionProviders {
		t.Run(string(provider), func(t *testing.T) {
			req := &schemas.BifrostResponsesRequest{
				Provider: provider,
				Model:    "claude-opus-4-6",
				Input: []schemas.ResponsesMessage{
					{
						Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
						Content: &schemas.ResponsesMessageContent{
							ContentStr: schemas.Ptr("Hello"),
						},
					},
				},
				Params: &schemas.ResponsesParameters{
					Text: makeResponsesTextFormat("my_schema"),
				},
			}

			ctx := schemas.NewBifrostContext(nil, time.Time{})
			result, err := ToAnthropicResponsesRequest(ctx, req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.OutputConfig != nil {
				t.Errorf("expected OutputConfig to stay unset for %s (native field unsupported), got %+v", provider, result.OutputConfig)
			}

			found := false
			for _, tool := range result.Tools {
				if tool.Name == "bf_so_my_schema" {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected a synthetic tool named %q to be added for %s structured output", "bf_so_my_schema", provider)
			}

			if result.ToolChoice == nil || result.ToolChoice.Name != "bf_so_my_schema" {
				t.Errorf("expected ToolChoice to be forced to the synthetic tool for %s, got %+v", provider, result.ToolChoice)
			}
		})
	}
}

// TestToAnthropicResponsesRequest_StructuredOutput_NativeOutputConfig_Anthropic is the
// negative-case control: Anthropic itself supports output_config.format natively, so no
// synthetic tool should be added. This is the branch that Azure incorrectly took before
// being added to toolConversionProviders.
func TestToAnthropicResponsesRequest_StructuredOutput_NativeOutputConfig_Anthropic(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-6",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("Hello"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Text: makeResponsesTextFormat("my_schema"),
		},
	}

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	result, err := ToAnthropicResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.OutputConfig == nil || result.OutputConfig.Format == nil {
		t.Fatal("expected OutputConfig.Format to be set natively for Anthropic")
	}

	for _, tool := range result.Tools {
		if tool.Name == "bf_so_my_schema" {
			t.Errorf("did not expect a synthetic tool for Anthropic, got %q", tool.Name)
		}
	}
}

// makeContextManagementReq builds a minimal BifrostResponsesRequest for the
// ContextManagement conversion tests below.
func makeContextManagementReq(params *schemas.ResponsesParameters) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-4-6",
		Input: []schemas.ResponsesMessage{
			{
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Hello")},
			},
		},
		Params: params,
	}
}

// TestToAnthropicResponsesRequest_ContextManagement_NeutralField covers the
// /v1/responses ingest path: OpenAIResponsesRequest embeds schemas.ResponsesParameters
// directly, so an incoming context_management body field lands in the neutral
// Params.ContextManagement json.RawMessage — not in ExtraParams. Before this fix,
// ToAnthropicResponsesRequest only read ExtraParams["context_management"], so this
// value was silently dropped and never reached Anthropic.
func TestToAnthropicResponsesRequest_ContextManagement_NeutralField(t *testing.T) {
	req := makeContextManagementReq(&schemas.ResponsesParameters{
		ContextManagement: []byte(`{"edits":[{"type":"` + string(ContextManagementEditTypeCompact) + `"}]}`),
	})

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	result, err := ToAnthropicResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ContextManagement == nil || len(result.ContextManagement.Edits) != 1 {
		t.Fatalf("expected ContextManagement to be decoded from the neutral field, got %+v", result.ContextManagement)
	}
	if result.ContextManagement.Edits[0].Type != ContextManagementEditTypeCompact {
		t.Errorf("expected compact edit type, got %q", result.ContextManagement.Edits[0].Type)
	}
}

// TestToAnthropicResponsesRequest_ContextManagement_ExtraParamsFallback covers the
// /v1/messages ingest path, where AnthropicMessageRequest.ToBifrostResponsesRequest
// stuffs the already-typed ContextManagement into ExtraParams instead of the neutral
// field. This must keep working alongside the neutral-field path above.
func TestToAnthropicResponsesRequest_ContextManagement_ExtraParamsFallback(t *testing.T) {
	params := &schemas.ResponsesParameters{
		ExtraParams: map[string]interface{}{
			"context_management": &ContextManagement{
				Edits: []ContextManagementEdit{{Type: ContextManagementEditTypeCompact}},
			},
		},
	}
	req := makeContextManagementReq(params)

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	result, err := ToAnthropicResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ContextManagement == nil || len(result.ContextManagement.Edits) != 1 {
		t.Fatalf("expected ContextManagement to be decoded from ExtraParams, got %+v", result.ContextManagement)
	}
	if _, exists := result.ExtraParams["context_management"]; exists {
		t.Errorf("expected context_management to be consumed out of the outgoing request's ExtraParams")
	}
}

// TestToAnthropicResponsesRequest_ContextManagement_OpenAIShapeIsDroppedNotErrored
// covers the case this fix is actually guarding: /v1/responses is OpenAI's own
// documented endpoint, and OpenAI's real Responses API defines its own native
// context_management shape — an array of {type, compact_threshold} objects — which
// is completely different from Anthropic's {edits:[...]} object shape. A client
// (or SDK default) may send that OpenAI-shaped value on a request that happens to
// route to an Anthropic model. This must NOT hard-fail the request — Anthropic
// simply doesn't support this shape, so it's dropped like any other inapplicable
// provider-specific param, the same way malformed/incompatible JSON is.
func TestToAnthropicResponsesRequest_ContextManagement_OpenAIShapeIsDroppedNotErrored(t *testing.T) {
	req := makeContextManagementReq(&schemas.ResponsesParameters{
		ContextManagement: []byte(`[{"type":"compaction","compact_threshold":2000}]`),
	})

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	result, err := ToAnthropicResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ContextManagement != nil {
		t.Errorf("expected ContextManagement to be dropped for an OpenAI-shaped payload, got %+v", result.ContextManagement)
	}
}

// TestToAnthropicResponsesRequest_ContextManagement_MalformedJSONIsDroppedNotErrored
// mirrors the OpenAI-shape case for plain invalid JSON: it must not fail the request.
func TestToAnthropicResponsesRequest_ContextManagement_MalformedJSONIsDroppedNotErrored(t *testing.T) {
	req := makeContextManagementReq(&schemas.ResponsesParameters{
		ContextManagement: []byte(`{"edits":`),
	})

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	result, err := ToAnthropicResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ContextManagement != nil {
		t.Errorf("expected ContextManagement to be dropped for malformed JSON, got %+v", result.ContextManagement)
	}
}

// TestToAnthropicResponsesRequest_ContextManagement_FallsBackAfterFailedNeutralDecode
// covers the case the existing ExtraParamsFallback/OpenAIShape/MalformedJSON tests each
// only prove half of: ExtraParamsFallback only exercises the fallback when the neutral
// field is absent entirely, and OpenAIShapeIsDroppedNotErrored/MalformedJSONIsDroppedNotErrored
// only prove a failed neutral decode is dropped when there's nothing else to fall back to.
// Neither proves that a neutral field which is PRESENT but fails to decode (wrong shape or
// malformed) still falls back to a valid Anthropic value sitting in ExtraParams, rather than
// short-circuiting to nil once the neutral field is non-empty.
func TestToAnthropicResponsesRequest_ContextManagement_FallsBackAfterFailedNeutralDecode(t *testing.T) {
	validExtra := &ContextManagement{
		Edits: []ContextManagementEdit{{Type: ContextManagementEditTypeClearThinking}},
	}

	cases := []struct {
		name    string
		neutral []byte
	}{
		{"OpenAI-shaped neutral field", []byte(`[{"type":"compaction","compact_threshold":2000}]`)},
		{"malformed neutral field", []byte(`{"edits":`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := makeContextManagementReq(&schemas.ResponsesParameters{
				ContextManagement: tc.neutral,
				ExtraParams: map[string]interface{}{
					"context_management": validExtra,
				},
			})

			ctx := schemas.NewBifrostContext(nil, time.Time{})
			result, err := ToAnthropicResponsesRequest(ctx, req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.ContextManagement == nil || len(result.ContextManagement.Edits) != 1 {
				t.Fatalf("expected fallback to the valid ExtraParams value, got %+v", result.ContextManagement)
			}
			if result.ContextManagement.Edits[0].Type != ContextManagementEditTypeClearThinking {
				t.Errorf("expected the ExtraParams edit type to survive, got %q", result.ContextManagement.Edits[0].Type)
			}
			if _, exists := result.ExtraParams["context_management"]; exists {
				t.Errorf("expected context_management to be removed from the outgoing request's ExtraParams")
			}
		})
	}
}

// TestToAnthropicResponsesRequest_ContextManagement_SkippedWhenUnsupportedByProvider
// verifies the parse is skipped entirely (not attempted-then-discarded) for a
// provider whose ProviderFeatures entry has ContextManagementField: false — avoids
// wasted unmarshal work when the post-processing strip pass would throw the result
// away anyway (see stripUnsupportedAnthropicFields, utils.go:382).
func TestToAnthropicResponsesRequest_ContextManagement_SkippedWhenUnsupportedByProvider(t *testing.T) {
	const noContextManagementProvider = schemas.ModelProvider("test-no-context-management")
	ProviderFeatures[noContextManagementProvider] = ProviderFeatureSupport{ContextManagementField: false}
	defer delete(ProviderFeatures, noContextManagementProvider)

	req := makeContextManagementReq(&schemas.ResponsesParameters{
		ContextManagement: []byte(`{"edits":[{"type":"` + string(ContextManagementEditTypeCompact) + `"}]}`),
	})
	req.Provider = noContextManagementProvider

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	result, err := ToAnthropicResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ContextManagement != nil {
		t.Errorf("expected ContextManagement to be skipped for a provider with ContextManagementField=false, got %+v", result.ContextManagement)
	}
}

// TestToAnthropicResponsesRequest_ContextManagement_UnknownProviderFailsOpen verifies
// that a provider absent from ProviderFeatures entirely still gets the parse attempted
// (fail-open), mirroring stripUnsupportedAnthropicFields's own "unknown provider — safe
// default: don't strip anything" behavior (utils.go:247).
func TestToAnthropicResponsesRequest_ContextManagement_UnknownProviderFailsOpen(t *testing.T) {
	const unknownProvider = schemas.ModelProvider("test-unknown-provider")
	if _, ok := ProviderFeatures[unknownProvider]; ok {
		t.Fatalf("test sentinel provider %q unexpectedly already present in ProviderFeatures", unknownProvider)
	}

	req := makeContextManagementReq(&schemas.ResponsesParameters{
		ContextManagement: []byte(`{"edits":[{"type":"` + string(ContextManagementEditTypeCompact) + `"}]}`),
	})
	req.Provider = unknownProvider

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	result, err := ToAnthropicResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ContextManagement == nil || len(result.ContextManagement.Edits) != 1 {
		t.Fatalf("expected ContextManagement to be decoded (fail-open) for an unknown provider, got %+v", result.ContextManagement)
	}
}

// TestToAnthropicResponsesRequest_ContextManagement_BothInputSources covers
// both input sources being present at once: Params.ContextManagement (the
// neutral field) decodes successfully, which used to mean the
// ExtraParams-consuming branch below it was skipped entirely — leaving a raw
// duplicate of context_management sitting in the outgoing ExtraParams. The
// neutral field must win, and the extra must still be removed either way.
func TestToAnthropicResponsesRequest_ContextManagement_BothInputSources(t *testing.T) {
	req := makeContextManagementReq(&schemas.ResponsesParameters{
		ContextManagement: []byte(`{"edits":[{"type":"` + string(ContextManagementEditTypeCompact) + `"}]}`),
		ExtraParams: map[string]interface{}{
			"context_management": &ContextManagement{
				Edits: []ContextManagementEdit{{Type: ContextManagementEditTypeClearThinking}},
			},
		},
	})

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	result, err := ToAnthropicResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ContextManagement == nil || len(result.ContextManagement.Edits) != 1 {
		t.Fatalf("expected ContextManagement to be decoded, got %+v", result.ContextManagement)
	}
	if result.ContextManagement.Edits[0].Type != ContextManagementEditTypeCompact {
		t.Errorf("expected the neutral field to win over ExtraParams, got edit type %q", result.ContextManagement.Edits[0].Type)
	}
	if _, exists := result.ExtraParams["context_management"]; exists {
		t.Errorf("expected context_management to be removed from the outgoing request's ExtraParams even though the neutral field won")
	}
}

// TestToAnthropicResponsesRequest_ContextManagement_UnsupportedProviderExtraParamsDropped
// covers a provider with ContextManagementField: false carrying
// ExtraParams["context_management"] (not the neutral field). The whole
// feature gate is skipped for such a provider, which used to mean the
// ExtraParams-consuming delete never ran either — leaving the unsupported
// value sitting in the outgoing ExtraParams to potentially leak through if
// passthrough is enabled. It must be removed regardless of the gate.
func TestToAnthropicResponsesRequest_ContextManagement_UnsupportedProviderExtraParamsDropped(t *testing.T) {
	const noContextManagementProvider = schemas.ModelProvider("test-no-context-management-extra")
	ProviderFeatures[noContextManagementProvider] = ProviderFeatureSupport{ContextManagementField: false}
	defer delete(ProviderFeatures, noContextManagementProvider)

	req := makeContextManagementReq(&schemas.ResponsesParameters{
		ExtraParams: map[string]interface{}{
			"context_management": &ContextManagement{
				Edits: []ContextManagementEdit{{Type: ContextManagementEditTypeCompact}},
			},
		},
	})
	req.Provider = noContextManagementProvider

	ctx := schemas.NewBifrostContext(nil, time.Time{})
	result, err := ToAnthropicResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ContextManagement != nil {
		t.Errorf("expected ContextManagement to stay unset for a provider with ContextManagementField=false, got %+v", result.ContextManagement)
	}
	if _, exists := result.ExtraParams["context_management"]; exists {
		t.Errorf("expected context_management to be removed from the outgoing request's ExtraParams even though the feature gate is closed")
	}
}

// TestAnthropicIngressLiftsServerSideToolOptIn covers issue #5679: the
// include_server_side_tool_invocations opt-in arrives as an unregistered
// Anthropic field (captured into ExtraParams) but the Gemini declaration-drop
// gate reads the typed Params.IncludeServerSideToolInvocations, so the ingress
// conversion must lift it. Without the lift, combining a server-side tool with
// a function tool on /anthropic/v1/messages routed to Gemini silently drops
// the function declarations.
func TestAnthropicIngressLiftsServerSideToolOptIn(t *testing.T) {
	body := []byte(`{
		"model": "gemini-3-pro",
		"max_tokens": 512,
		"messages": [{"role": "user", "content": "search and compute"}],
		"include_server_side_tool_invocations": true
	}`)

	var req AnthropicMessageRequest
	if err := req.UnmarshalJSON(body); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	bifrostReq := req.ToBifrostResponsesRequest(nil)
	if bifrostReq == nil || bifrostReq.Params == nil {
		t.Fatal("converted request or params is nil")
	}
	if bifrostReq.Params.IncludeServerSideToolInvocations == nil ||
		!*bifrostReq.Params.IncludeServerSideToolInvocations {
		t.Fatalf("include_server_side_tool_invocations not lifted to typed param: %v",
			bifrostReq.Params.IncludeServerSideToolInvocations)
	}
}
