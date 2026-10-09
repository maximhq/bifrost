package anthropic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
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

// TestAnthropicContainerRoundTrip covers issue #5707: the "container" request
// param (string id for container reuse, or object with skills[]) must survive
// the /v1/messages ingress-to-egress round trip. Both forms were silently
// dropped: ToBifrostResponsesRequest never read req.Container, so a client
// requesting container reuse got HTTP 200 with a fresh container and all
// previously staged files missing.
func TestAnthropicContainerRoundTrip(t *testing.T) {
	t.Run("StringForm", func(t *testing.T) {
		req := &AnthropicMessageRequest{
			Model:     "claude-sonnet-4-5",
			MaxTokens: 100,
			Container: &AnthropicContainer{ContainerStr: schemas.Ptr("container_011CPQ2vNi9wkjJdrCFJNkCq")},
		}

		bifrostReq := req.ToBifrostResponsesRequest(nil)
		out, err := ToAnthropicResponsesRequest(nil, bifrostReq)
		if err != nil {
			t.Fatalf("egress error: %v", err)
		}

		if out.Container == nil || out.Container.ContainerStr == nil {
			t.Fatalf("string-form container dropped in round trip: %+v", out.Container)
		}
		if *out.Container.ContainerStr != "container_011CPQ2vNi9wkjJdrCFJNkCq" {
			t.Errorf("container id = %q, want %q", *out.Container.ContainerStr, "container_011CPQ2vNi9wkjJdrCFJNkCq")
		}
		// Consumed onto the typed field, so it must not also linger in
		// ExtraParams and serialize twice.
		if _, ok := out.ExtraParams["container"]; ok {
			t.Errorf("container left in ExtraParams after promotion: %#v", out.ExtraParams["container"])
		}
	})

	t.Run("ObjectForm", func(t *testing.T) {
		req := &AnthropicMessageRequest{
			Model:     "claude-sonnet-4-5",
			MaxTokens: 100,
			Container: &AnthropicContainer{ContainerObject: &AnthropicContainerObject{
				ID:     schemas.Ptr("container_011CPQ2vNi9wkjJdrCFJNkCq"),
				Skills: []AnthropicContainerSkill{{SkillID: "pdf", Type: "anthropic"}},
			}},
		}

		bifrostReq := req.ToBifrostResponsesRequest(nil)
		out, err := ToAnthropicResponsesRequest(nil, bifrostReq)
		if err != nil {
			t.Fatalf("egress error: %v", err)
		}

		if out.Container == nil || out.Container.ContainerObject == nil {
			t.Fatalf("object-form container dropped in round trip: %+v", out.Container)
		}
		obj := out.Container.ContainerObject
		if obj.ID == nil || *obj.ID != "container_011CPQ2vNi9wkjJdrCFJNkCq" {
			t.Errorf("container object id = %v, want container_011CPQ2vNi9wkjJdrCFJNkCq", obj.ID)
		}
		if len(obj.Skills) != 1 || obj.Skills[0].SkillID != "pdf" || obj.Skills[0].Type != "anthropic" {
			t.Errorf("container skills not preserved: %+v", obj.Skills)
		}
		if _, ok := out.ExtraParams["container"]; ok {
			t.Errorf("container left in ExtraParams after promotion: %#v", out.ExtraParams["container"])
		}
	})
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

// TestToAnthropicResponsesRequest_StructuredOutput_Fable51_NoForcedToolChoice is the
// Fable 5.1 counterpart: the synthetic tool is still added, but the pin is not,
// because Fable 5.1 / Mythos 5.1 reject tool_choice "tool" and "any" with a 400.
// The model reaches the tool under the default "auto" — with only the bf_so_*
// tool bound there is nothing else it can call.
func TestToAnthropicResponsesRequest_StructuredOutput_Fable51_NoForcedToolChoice(t *testing.T) {
	for _, provider := range toolConversionProviders {
		for _, model := range []string{"claude-fable-5-1", "claude-mythos-5-1"} {
			t.Run(string(provider)+"/"+model, func(t *testing.T) {
				req := &schemas.BifrostResponsesRequest{
					Provider: provider,
					Model:    model,
					Input: []schemas.ResponsesMessage{
						{
							Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
							Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Hello")},
						},
					},
					Params: &schemas.ResponsesParameters{Text: makeResponsesTextFormat("my_schema")},
				}

				ctx := schemas.NewBifrostContext(nil, time.Time{})
				result, err := ToAnthropicResponsesRequest(ctx, req)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				found := false
				for _, tool := range result.Tools {
					if tool.Name == "bf_so_my_schema" {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected the synthetic tool to still be added for %s/%s", provider, model)
				}
				if result.ToolChoice != nil {
					t.Errorf("expected no forced ToolChoice for %s/%s, got %+v", provider, model, result.ToolChoice)
				}
			})
		}
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

// A non-streaming Responses turn cut short by the output-token cap arrives from
// OpenAI-shaped providers (Azure, OpenAI, chat-completions fallbacks) with
// status "incomplete" and incomplete_details.reason set, but no stop_reason:
// that field is Anthropic/Bedrock-only. The Anthropic egress must derive
// stop_reason from incomplete_details, never report end_turn for a truncated
// turn (#6782). Mirrors the streaming precedence StopReason > IncompleteDetails
// > tool_use inference > end_turn.
func TestToAnthropicResponsesResponse_IncompleteReportsTruncationStopReason(t *testing.T) {
	cases := []struct {
		name       string
		status     string
		stopReason *string
		incomplete *schemas.ResponsesResponseIncompleteDetails
		want       AnthropicStopReason
	}{
		{
			name:       "StopReasonLength",
			status:     schemas.ResponsesResponseStatusIncomplete,
			stopReason: schemas.Ptr("length"),
			incomplete: &schemas.ResponsesResponseIncompleteDetails{Reason: schemas.ResponsesResponseIncompleteReasonMaxOutputTokens},
			want:       AnthropicStopReasonMaxTokens,
		},
		{
			// The reported shape: Azure /openai/v1/responses sets no stop_reason.
			name:       "MaxTokensFromIncompleteDetailsOnly",
			status:     schemas.ResponsesResponseStatusIncomplete,
			incomplete: &schemas.ResponsesResponseIncompleteDetails{Reason: schemas.ResponsesResponseIncompleteReasonMaxOutputTokens},
			want:       AnthropicStopReasonMaxTokens,
		},
		{
			name:       "ContentFilterFromIncompleteDetailsOnly",
			status:     schemas.ResponsesResponseStatusIncomplete,
			incomplete: &schemas.ResponsesResponseIncompleteDetails{Reason: schemas.ResponsesResponseIncompleteReasonContentFilter},
			want:       AnthropicStopReasonRefusal,
		},
		{
			// Control: a completed text turn with neither field keeps end_turn.
			name:   "CompletedTextIsEndTurn",
			status: schemas.ResponsesResponseStatusCompleted,
			want:   AnthropicStopReasonEndTurn,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
			resp := ToAnthropicResponsesResponse(ctx, &schemas.BifrostResponsesResponse{
				ID:                schemas.Ptr("resp_1"),
				Model:             "azure-glm-5.2",
				Status:            schemas.Ptr(tc.status),
				StopReason:        tc.stopReason,
				IncompleteDetails: tc.incomplete,
				Output: []schemas.ResponsesMessage{{
					Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
					Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
					Content: &schemas.ResponsesMessageContent{
						ContentBlocks: []schemas.ResponsesMessageContentBlock{{
							Type: schemas.ResponsesOutputMessageContentTypeText,
							Text: schemas.Ptr("1\n2\n3"),
						}},
					},
				}},
				Usage: &schemas.ResponsesResponseUsage{InputTokens: 55974, OutputTokens: 4096, TotalTokens: 60070},
			})
			if resp == nil {
				t.Fatal("ToAnthropicResponsesResponse returned nil")
			}
			if resp.StopReason != tc.want {
				t.Errorf("stop_reason = %q, want %q", resp.StopReason, tc.want)
			}
			if resp.Usage == nil || resp.Usage.OutputTokens != 4096 {
				t.Errorf("usage.output_tokens not carried: %+v", resp.Usage)
			}
		})
	}
}

// Claude Code auto-mode classifier: safeguards must survive the typed
// (non-passthrough) ingress→egress request conversion for Anthropic direct.
func TestAnthropicSafeguardsRequestRoundTrip(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	var req AnthropicMessageRequest
	if err := sonic.Unmarshal([]byte(`{"model":"claude-opus-4-8","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"safeguards":{"check":"auto_mode"}}`), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	bifrostReq := req.ToBifrostResponsesRequest(ctx)
	if bifrostReq == nil || bifrostReq.Params == nil {
		t.Fatal("nil bifrost request from ingress")
	}

	out, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("egress conversion: %v", err)
	}
	body, err := sonic.Marshal(out)
	if err != nil {
		t.Fatalf("marshal egress request: %v", err)
	}
	if want := `"safeguards":{"check":"auto_mode"}`; !strings.Contains(string(body), want) {
		t.Fatalf("safeguards dropped on typed request round trip: %s", string(body))
	}
}

// Claude Code auto-mode classifier: safeguard_results must survive the typed
// unary response conversion (provider decode → Bifrost → Anthropic client shape).
func TestAnthropicSafeguardResultsUnaryRoundTrip(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	var resp AnthropicMessageResponse
	if err := sonic.Unmarshal([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-8","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1},"safeguard_results":[{"id":"sg_1","verdict":"allow"}]}`), &resp); err != nil {
		t.Fatalf("unmarshal provider response: %v", err)
	}

	bifrostResp := resp.ToBifrostResponsesResponse(ctx)
	if bifrostResp == nil {
		t.Fatal("nil bifrost response")
	}

	out := ToAnthropicResponsesResponse(ctx, bifrostResp)
	body, err := sonic.Marshal(out)
	if err != nil {
		t.Fatalf("marshal client response: %v", err)
	}
	if want := `"safeguard_results":[{"id":"sg_1","verdict":"allow"}]`; !strings.Contains(string(body), want) {
		t.Fatalf("safeguard_results dropped on typed unary round trip: %s", string(body))
	}
}

// Issue #7601: /v1/responses on anthropic/* returned no status, and a turn cut
// short by max_tokens was indistinguishable from a complete one. OpenAI's
// Responses contract (and the Bedrock fix in #4679) sets status "completed" on a
// finished turn and status "incomplete" + incomplete_details on a truncated or
// refused one.
func TestAnthropicResponsesStatusFromStopReason(t *testing.T) {
	for _, tc := range []struct {
		stopReason     AnthropicStopReason
		wantStatus     string
		wantIncomplete string
	}{
		{AnthropicStopReasonEndTurn, schemas.ResponsesResponseStatusCompleted, ""},
		{AnthropicStopReasonStopSequence, schemas.ResponsesResponseStatusCompleted, ""},
		{AnthropicStopReasonToolUse, schemas.ResponsesResponseStatusCompleted, ""},
		{AnthropicStopReasonMaxTokens, schemas.ResponsesResponseStatusIncomplete, schemas.ResponsesResponseIncompleteReasonMaxOutputTokens},
		{AnthropicStopReasonModelContextWindowExceeded, schemas.ResponsesResponseStatusIncomplete, schemas.ResponsesResponseIncompleteReasonMaxOutputTokens},
		{AnthropicStopReasonRefusal, schemas.ResponsesResponseStatusIncomplete, schemas.ResponsesResponseIncompleteReasonContentFilter},
	} {
		t.Run(string(tc.stopReason), func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			resp := (&AnthropicMessageResponse{
				ID:         "msg_01",
				Model:      "claude-sonnet-4-6",
				Content:    []AnthropicContentBlock{{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr("Rome was")}},
				StopReason: tc.stopReason,
				Usage:      &AnthropicUsage{InputTokens: 20, OutputTokens: 40},
			}).ToBifrostResponsesResponse(ctx)

			if resp.Status == nil || *resp.Status != tc.wantStatus {
				t.Fatalf("status = %v, want %q", resp.Status, tc.wantStatus)
			}
			assertIncompleteDetails(t, resp.IncompleteDetails, tc.wantIncomplete)
		})
	}
}

// The streaming half of #7601: the terminal event was always response.completed
// with no status, even when message_delta carried stop_reason max_tokens.
func TestAnthropicResponsesStreamTerminalFromStopReason(t *testing.T) {
	for _, tc := range []struct {
		stopReason     AnthropicStopReason
		wantType       schemas.ResponsesStreamResponseType
		wantStatus     string
		wantIncomplete string
	}{
		{AnthropicStopReasonEndTurn, schemas.ResponsesStreamResponseTypeCompleted, schemas.ResponsesResponseStatusCompleted, ""},
		{AnthropicStopReasonMaxTokens, schemas.ResponsesStreamResponseTypeIncomplete, schemas.ResponsesResponseStatusIncomplete, schemas.ResponsesResponseIncompleteReasonMaxOutputTokens},
	} {
		t.Run(string(tc.stopReason), func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			state := AcquireAnthropicResponsesStreamState()
			defer ReleaseAnthropicResponsesStreamState(state)

			frames := []string{
				`{"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"stop_reason":null,"usage":{"input_tokens":20,"output_tokens":1}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Rome was"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"` + string(tc.stopReason) + `","stop_sequence":null},"usage":{"output_tokens":40}}`,
				`{"type":"message_stop"}`,
			}
			var terminal *schemas.BifrostResponsesStreamResponse
			seq := 0
			for _, frame := range frames {
				var chunk AnthropicStreamEvent
				if err := sonic.Unmarshal([]byte(frame), &chunk); err != nil {
					t.Fatalf("unmarshal %s: %v", frame, err)
				}
				responses, bErr, isLast := chunk.ToBifrostResponsesStream(ctx, seq, state)
				if bErr != nil {
					t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
				}
				seq += len(responses)
				if isLast && len(responses) > 0 {
					terminal = responses[len(responses)-1]
				}
			}
			if terminal == nil || terminal.Response == nil {
				t.Fatal("stream produced no terminal event")
			}
			if terminal.Type != tc.wantType {
				t.Errorf("terminal event type = %q, want %q", terminal.Type, tc.wantType)
			}
			if terminal.Response.Status == nil || *terminal.Response.Status != tc.wantStatus {
				t.Errorf("terminal status = %v, want %q", terminal.Response.Status, tc.wantStatus)
			}
			assertIncompleteDetails(t, terminal.Response.IncompleteDetails, tc.wantIncomplete)
		})
	}
}

func assertIncompleteDetails(t *testing.T, got *schemas.ResponsesResponseIncompleteDetails, wantReason string) {
	t.Helper()
	if wantReason == "" {
		if got != nil {
			t.Errorf("incomplete_details = %+v, want nil", got)
		}
		return
	}
	if got == nil || got.Reason != wantReason {
		t.Errorf("incomplete_details = %+v, want reason %q", got, wantReason)
	}
}

// Follow-up to #7601: OpenAI marks the output item that was being written when the
// cap hit as status "incomplete". The response-level status was fixed, but the
// truncated message item still reported "completed".
func TestAnthropicResponsesTruncatedOutputItemIncomplete(t *testing.T) {
	for _, tc := range []struct {
		stopReason AnthropicStopReason
		want       string
	}{
		{AnthropicStopReasonMaxTokens, schemas.ResponsesResponseStatusIncomplete},
		{AnthropicStopReasonEndTurn, "completed"},
	} {
		t.Run(string(tc.stopReason), func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			resp := (&AnthropicMessageResponse{
				ID: "msg_01", Model: "claude-sonnet-4-6",
				Content:    []AnthropicContentBlock{{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr("Rome was")}},
				StopReason: tc.stopReason,
				Usage:      &AnthropicUsage{InputTokens: 20, OutputTokens: 40},
			}).ToBifrostResponsesResponse(ctx)
			assertAnthropicLastOutputItemStatus(t, "non-stream", resp.Output, tc.want)

			state := AcquireAnthropicResponsesStreamState()
			defer ReleaseAnthropicResponsesStreamState(state)
			var terminal *schemas.BifrostResponsesStreamResponse
			seq := 0
			for _, frame := range []string{
				`{"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"stop_reason":null,"usage":{"input_tokens":20,"output_tokens":1}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Rome was"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"` + string(tc.stopReason) + `","stop_sequence":null},"usage":{"output_tokens":40}}`,
				`{"type":"message_stop"}`,
			} {
				var chunk AnthropicStreamEvent
				if err := sonic.Unmarshal([]byte(frame), &chunk); err != nil {
					t.Fatalf("unmarshal %s: %v", frame, err)
				}
				responses, bErr, isLast := chunk.ToBifrostResponsesStream(ctx, seq, state)
				if bErr != nil {
					t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
				}
				seq += len(responses)
				if isLast && len(responses) > 0 {
					terminal = responses[len(responses)-1]
				}
			}
			if terminal == nil || terminal.Response == nil {
				t.Fatal("stream produced no terminal event")
			}
			assertAnthropicLastOutputItemStatus(t, "stream terminal", terminal.Response.Output, tc.want)
		})
	}
}

// TestConvertBifrostMessages_ShellCallKeepsCommands verifies that a shell_call
// replayed to Anthropic keeps its commands instead of collapsing to a bare type name.
func TestConvertBifrostMessages_ShellCallKeepsCommands(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	caps := schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-4-5-20250929")

	callID := "shell_call_1"
	timeout := 5000
	shellCall := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeShellCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: &callID,
			Action: &schemas.ResponsesToolMessageActionStruct{
				ResponsesShellToolCallAction: &schemas.ResponsesShellToolCallAction{
					Commands:  []string{"ls -la", "cat go.mod"},
					TimeoutMS: &timeout,
				},
			},
			ResponsesShellCall: &schemas.ResponsesShellCall{
				Environment: &schemas.ResponsesShellCallEnvironment{Type: "local"},
			},
		},
	}

	msgs, _ := ConvertBifrostMessagesToAnthropicMessages(ctx, []schemas.ResponsesMessage{shellCall}, true, caps)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
	}
	if len(msgs[0].Content.ContentBlocks) != 1 || msgs[0].Content.ContentBlocks[0].Text == nil {
		t.Fatalf("expected a single text block, got %+v", msgs[0].Content.ContentBlocks)
	}

	text := *msgs[0].Content.ContentBlocks[0].Text
	for _, want := range []string{"ls -la", "cat go.mod", `"timeout_ms":5000`} {
		if !strings.Contains(text, want) {
			t.Errorf("shell call text missing %q, got:\n%s", want, text)
		}
	}
}

// TestConvertBifrostMessages_ShellCallOutputKeepsOutcome pins the replayed output:
// without the outcome a failed command reads like a successful one, and a silent
// command used to produce no message at all.
func TestConvertBifrostMessages_ShellCallOutputKeepsOutcome(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	caps := schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-4-5-20250929")

	callID := "shell_call_1"
	exit127 := 127
	exit0 := 0

	tests := []struct {
		name    string
		output  []schemas.ResponsesShellCallOutputContent
		want    []string
		notWant []string
	}{
		{
			name:   "failure keeps stderr and the exit code",
			output: []schemas.ResponsesShellCallOutputContent{{Stderr: "bash: nope: command not found", Outcome: schemas.ResponsesShellCallOutcome{Type: "exit", ExitCode: &exit127}}},
			want:   []string{"command not found", "[exit code 127]"},
		},
		{
			name:   "silent success still reaches the model",
			output: []schemas.ResponsesShellCallOutputContent{{Outcome: schemas.ResponsesShellCallOutcome{Type: "exit", ExitCode: &exit0}}},
			want:   []string{"[exit code 0]"},
		},
		{
			name:   "timeout is named",
			output: []schemas.ResponsesShellCallOutputContent{{Stdout: "partial", Outcome: schemas.ResponsesShellCallOutcome{Type: "timeout"}}},
			want:   []string{"partial", "[command timed out]"},
		},
		{
			name:   "no text and no outcome still emits a message",
			output: []schemas.ResponsesShellCallOutputContent{{}},
			want:   []string{"[no output]"},
		},
		{
			name:    "success with output does not gain noise",
			output:  []schemas.ResponsesShellCallOutputContent{{Stdout: "go.mod", Outcome: schemas.ResponsesShellCallOutcome{Type: "exit", ExitCode: &exit0}}},
			want:    []string{"go.mod", "[exit code 0]"},
			notWant: []string{"[no output]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeShellCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: &callID,
					Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesShellCallOutput: tt.output},
				},
			}

			msgs, _ := ConvertBifrostMessagesToAnthropicMessages(ctx, []schemas.ResponsesMessage{msg}, true, caps)
			if len(msgs) != 1 {
				t.Fatalf("expected 1 message, got %d: %+v", len(msgs), msgs)
			}
			if len(msgs[0].Content.ContentBlocks) != 1 || msgs[0].Content.ContentBlocks[0].Text == nil {
				t.Fatalf("expected a single text block, got %+v", msgs[0].Content.ContentBlocks)
			}

			text := *msgs[0].Content.ContentBlocks[0].Text
			for _, want := range tt.want {
				if !strings.Contains(text, want) {
					t.Errorf("replayed output missing %q, got:\n%s", want, text)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(text, notWant) {
					t.Errorf("replayed output must not contain %q, got:\n%s", notWant, text)
				}
			}
		})
	}
}

func assertAnthropicLastOutputItemStatus(t *testing.T, label string, output []schemas.ResponsesMessage, want string) {
	t.Helper()
	if len(output) == 0 {
		t.Fatalf("%s: no output items", label)
	}
	last := output[len(output)-1]
	if last.Status == nil || *last.Status != want {
		t.Errorf("%s: last output item status = %v, want %q", label, derefStatus(last.Status), want)
	}
}

func derefStatus(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// perMessageEffortPiBody is the exact body Pi 0.87.1 sends when supportsMidConvoEffort is
// on: a top-level output_config.effort for the conversation plus an effort-only
// role:"system" message (empty content, output_config.effort) that overrides it for the
// current turn under beta mid-conversation-output-config-2026-07-01. The gateway must
// forward the override verbatim; AnthropicMessage previously kept only role and content,
// so the per-message effort was dropped and the upstream request ran at "high".
const perMessageEffortPiBody = `{
  "model": "claude-opus-5-5",
  "max_tokens": 128,
  "stream": true,
  "thinking": {"type": "adaptive"},
  "output_config": {"effort": "high"},
  "messages": [
    {"role": "user", "content": "Say hello."},
    {"role": "system", "content": [], "output_config": {"effort": "low"}}
  ]
}`

func decodeAnthropicMessagesBody(t *testing.T, body string) *AnthropicMessageRequest {
	t.Helper()
	var req AnthropicMessageRequest
	if err := schemas.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decoding Messages API body: %v", err)
	}
	return &req
}

// jsonArrayOfObjects marshals v (a slice) and decodes it back generically, so a test can
// assert on the wire shape without depending on struct fields that may not exist yet.
func jsonArrayOfObjects(t *testing.T, v any) ([]map[string]any, string) {
	t.Helper()
	raw, err := sonic.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out []map[string]any
	if err := sonic.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return out, string(raw)
}

// assertEffortOnlySystemMessage checks one wire message is the per-message effort form:
// role system, an empty content array, and output_config.effort == want.
func assertEffortOnlySystemMessage(t *testing.T, msg map[string]any, want string, wire string) {
	t.Helper()
	if msg["role"] != "system" {
		t.Errorf("role = %v, want system: %s", msg["role"], wire)
	}
	if content, ok := msg["content"].([]any); !ok || len(content) != 0 {
		t.Errorf("content = %v, want an empty array (effort-only message carries no text): %s", msg["content"], wire)
	}
	oc, _ := msg["output_config"].(map[string]any)
	if oc == nil || oc["effort"] != want {
		t.Errorf("output_config.effort = %v, want %q: %s", oc, want, wire)
	}
}

// TestAnthropicIngress_PerMessageEffortReachesNeutralRequest: the effort-only system
// message must survive the Anthropic -> Bifrost conversion as its own input item carrying
// the override, instead of being discarded for having no content blocks.
func TestAnthropicIngress_PerMessageEffortReachesNeutralRequest(t *testing.T) {
	t.Parallel()

	bifrostReq := decodeAnthropicMessagesBody(t, perMessageEffortPiBody).ToBifrostResponsesRequest(effortTestCtx(t))
	if bifrostReq == nil {
		t.Fatal("nil neutral request")
	}
	input, wire := jsonArrayOfObjects(t, bifrostReq.Input)
	if len(input) != 2 {
		t.Fatalf("neutral input has %d items, want 2 (the effort-only system message was dropped on ingress): %s", len(input), wire)
	}
	assertEffortOnlySystemMessage(t, input[1], "low", wire)
}

// TestAnthropicRoundTrip_PerMessageEffortOverrideForwarded is the direct regression for
// the report: the request Pi sends must leave the gateway with both the top-level effort
// and the per-message override intact.
func TestAnthropicRoundTrip_PerMessageEffortOverrideForwarded(t *testing.T) {
	t.Parallel()

	ctx := effortTestCtx(t)
	bifrostReq := decodeAnthropicMessagesBody(t, perMessageEffortPiBody).ToBifrostResponsesRequest(ctx)
	// Pi sends an unprefixed model, so ingress leaves Provider empty and core resolves it
	// from the model before egress (bifrost.go, req.Provider == ""). The converters never
	// see an empty provider in production, so resolve it here as roundTrip does.
	bifrostReq.Provider = schemas.Anthropic
	out, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}
	if out.OutputConfig == nil || out.OutputConfig.Effort == nil || *out.OutputConfig.Effort != "high" {
		t.Errorf("top-level output_config.effort = %v, want high", out.OutputConfig)
	}
	msgs, wire := jsonArrayOfObjects(t, out.Messages)
	if len(msgs) != 2 {
		t.Fatalf("forwarded messages = %d, want 2 (per-message effort override was dropped, upstream runs at the top-level effort): %s", len(msgs), wire)
	}
	if msgs[0]["role"] != "user" {
		t.Errorf("messages[0].role = %v, want user: %s", msgs[0]["role"], wire)
	}
	assertEffortOnlySystemMessage(t, msgs[1], "low", wire)
}

// TestToAnthropicResponsesRequest_PerMessageEffortFromNeutralInput covers the neutral wire:
// a /v1/responses caller expressing the same override on an input item reaches Anthropic
// in the documented shape.
func TestToAnthropicResponsesRequest_PerMessageEffortFromNeutralInput(t *testing.T) {
	t.Parallel()

	var input []schemas.ResponsesMessage
	if err := schemas.Unmarshal([]byte(`[
		{"type":"message","role":"user","content":"Say hello."},
		{"type":"message","role":"system","content":[],"output_config":{"effort":"low"}}
	]`), &input); err != nil {
		t.Fatalf("decode neutral input: %v", err)
	}
	ctx := effortTestCtx(t)
	out, err := ToAnthropicResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-opus-5-5",
		Input:    input,
		Params:   &schemas.ResponsesParameters{MaxOutputTokens: schemas.Ptr(128)},
	})
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}
	msgs, wire := jsonArrayOfObjects(t, out.Messages)
	if len(msgs) != 2 {
		t.Fatalf("forwarded messages = %d, want 2 (neutral per-message effort was dropped): %s", len(msgs), wire)
	}
	assertEffortOnlySystemMessage(t, msgs[1], "low", wire)
}

// TestToAnthropicResponsesRequest_PerMessageEffortSurvivesTextFallbacks: a system item that
// carries text AND a per-message effort can only go out natively when its placement is
// valid. On the hoist (leading run) and inline (rejected placement) fallbacks the text cannot
// carry the override, so it is re-emitted as an effort-only system message, which Anthropic
// exempts from placement rules ("It can appear anywhere in messages, including as the first
// entry", https://platform.claude.com/docs/en/build-with-claude/effort). Without it the
// caller's effort change is silently lost (PR #8018 review).
func TestToAnthropicResponsesRequest_PerMessageEffortSurvivesTextFallbacks(t *testing.T) {
	t.Parallel()

	convert := func(t *testing.T, provider schemas.ModelProvider, inputJSON string) ([]map[string]any, string, *AnthropicMessageRequest) {
		t.Helper()
		var input []schemas.ResponsesMessage
		if err := schemas.Unmarshal([]byte(inputJSON), &input); err != nil {
			t.Fatalf("decode neutral input: %v", err)
		}
		out, err := ToAnthropicResponsesRequest(effortTestCtx(t), &schemas.BifrostResponsesRequest{
			Provider: provider,
			Model:    "claude-opus-5-5",
			Input:    input,
			Params:   &schemas.ResponsesParameters{MaxOutputTokens: schemas.Ptr(128)},
		})
		if err != nil {
			t.Fatalf("ToAnthropicResponsesRequest: %v", err)
		}
		msgs, wire := jsonArrayOfObjects(t, out.Messages)
		return msgs, wire, out
	}

	t.Run("hoisted leading system item keeps its effort", func(t *testing.T) {
		t.Parallel()
		msgs, wire, out := convert(t, schemas.Anthropic, `[
			{"type":"message","role":"system","content":"Be terse.","output_config":{"effort":"low"}},
			{"type":"message","role":"user","content":"Say hello."}
		]`)
		system, _ := sonic.Marshal(out.System)
		if !strings.Contains(string(system), "Be terse.") {
			t.Fatalf("leading system text was not hoisted into system: %s", system)
		}
		if len(msgs) != 2 {
			t.Fatalf("messages = %d, want 2 (effort-only override + user): %s", len(msgs), wire)
		}
		assertEffortOnlySystemMessage(t, msgs[0], "low", wire)
		if msgs[1]["role"] != "user" {
			t.Fatalf("messages[1].role = %v, want user: %s", msgs[1]["role"], wire)
		}
	})

	t.Run("inlined mid-conversation system item keeps its effort", func(t *testing.T) {
		t.Parallel()
		// Followed by a user turn, so the native placement is rejected and the text is inlined.
		msgs, wire, _ := convert(t, schemas.Anthropic, `[
			{"type":"message","role":"user","content":"First question."},
			{"type":"message","role":"system","content":"Be terse.","output_config":{"effort":"low"}},
			{"type":"message","role":"user","content":"Second question."}
		]`)
		effortAt := -1
		for i, m := range msgs {
			if m["role"] == "system" {
				if effortAt != -1 {
					t.Fatalf("more than one system message: %s", wire)
				}
				effortAt = i
				assertEffortOnlySystemMessage(t, m, "low", wire)
			}
		}
		if effortAt == -1 {
			t.Fatalf("per-message effort lost on the inline fallback: %s", wire)
		}
		if effortAt+1 >= len(msgs) {
			t.Fatalf("effort-only message must precede the inlined reminder: %s", wire)
		}
		next, _ := sonic.Marshal(msgs[effortAt+1])
		if !strings.Contains(string(next), "system-reminder") || !strings.Contains(string(next), "Be terse.") {
			t.Fatalf("message after the effort-only override is not the inlined reminder: %s", wire)
		}
	})

	t.Run("unsupported surface emits no override on the hoist", func(t *testing.T) {
		t.Parallel()
		msgs, wire, _ := convert(t, schemas.Vertex, `[
			{"type":"message","role":"system","content":"Be terse.","output_config":{"effort":"low"}},
			{"type":"message","role":"user","content":"Say hello."}
		]`)
		if len(msgs) != 1 || msgs[0]["role"] != "user" {
			t.Fatalf("Vertex must receive only the user turn: %s", wire)
		}
	})
}

// TestAnthropicRoundTrip_PerMessageEffortDroppedWhenModelLacksSupport pins the fail-soft
// side: Opus 4.8 accepts mid-conversation system messages but not per-turn effort
// (Anthropic 400s "output_config.effort requires a model that supports per-turn effort"),
// so the override is dropped rather than forwarded into a guaranteed rejection.
func TestAnthropicRoundTrip_PerMessageEffortDroppedWhenModelLacksSupport(t *testing.T) {
	t.Parallel()

	ctx := effortTestCtx(t)
	body := strings.Replace(perMessageEffortPiBody, "claude-opus-5-5", "claude-opus-4-8", 1)
	bifrostReq := decodeAnthropicMessagesBody(t, body).ToBifrostResponsesRequest(ctx)
	bifrostReq.Provider = schemas.Anthropic // see TestAnthropicRoundTrip_PerMessageEffortOverrideForwarded
	out, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}
	msgs, wire := jsonArrayOfObjects(t, out.Messages)
	if len(msgs) != 1 || msgs[0]["role"] != "user" {
		t.Fatalf("forwarded messages = %s, want only the user turn on a model without per-turn effort", wire)
	}
	if out.OutputConfig == nil || out.OutputConfig.Effort == nil || *out.OutputConfig.Effort != "high" {
		t.Errorf("top-level output_config.effort = %v, want high (unchanged)", out.OutputConfig)
	}
}

// TestToAnthropicResponsesRequest_PerMessageEffortEmptyStringContent: content:"" with an
// override is the effort-only form as much as content:[] is, even as the first item. A
// non-nil empty string must not be hoisted into the system block with the effort dropped.
func TestToAnthropicResponsesRequest_PerMessageEffortEmptyStringContent(t *testing.T) {
	t.Parallel()

	var input []schemas.ResponsesMessage
	if err := schemas.Unmarshal([]byte(`[
		{"type":"message","role":"system","content":"","output_config":{"effort":"low"}},
		{"type":"message","role":"user","content":"Say hello."}
	]`), &input); err != nil {
		t.Fatalf("decode neutral input: %v", err)
	}
	ctx := effortTestCtx(t)
	out, err := ToAnthropicResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic, Model: "claude-opus-5-5", Input: input,
		Params: &schemas.ResponsesParameters{MaxOutputTokens: schemas.Ptr(128)},
	})
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}
	if out.System != nil {
		t.Errorf("empty-string effort-only item was hoisted into the system block: %+v", out.System)
	}
	msgs, wire := jsonArrayOfObjects(t, out.Messages)
	if len(msgs) != 2 || msgs[1]["role"] != "user" {
		t.Fatalf("forwarded messages = %s, want [effort-only system, user]", wire)
	}
	assertEffortOnlySystemMessage(t, msgs[0], "low", wire)
}

// TestToAnthropicResponsesRequest_PerMessageEffortThenTextSystemStaysNative: "Consecutive
// system messages are accepted and treated as a single system section" (Anthropic docs), so
// a text-bearing system item right after an emitted effort-only one must still be judged
// against the user turn before the group, not against its sibling, and stay native.
func TestToAnthropicResponsesRequest_PerMessageEffortThenTextSystemStaysNative(t *testing.T) {
	t.Parallel()

	var input []schemas.ResponsesMessage
	if err := schemas.Unmarshal([]byte(`[
		{"type":"message","role":"user","content":"Hello"},
		{"type":"message","role":"system","content":[],"output_config":{"effort":"low"}},
		{"type":"message","role":"system","content":"Respond in one word.","output_config":{"effort":"high"}}
	]`), &input); err != nil {
		t.Fatalf("decode neutral input: %v", err)
	}
	ctx := effortTestCtx(t)
	out, err := ToAnthropicResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic, Model: "claude-opus-5-5", Input: input,
		Params: &schemas.ResponsesParameters{MaxOutputTokens: schemas.Ptr(128)},
	})
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}
	msgs, wire := jsonArrayOfObjects(t, out.Messages)
	if len(msgs) != 3 || msgs[0]["role"] != "user" || msgs[1]["role"] != "system" || msgs[2]["role"] != "system" {
		t.Fatalf("forwarded roles wrong, want user,system,system (text item was inlined as a user reminder): %s", wire)
	}
	assertEffortOnlySystemMessage(t, msgs[1], "low", wire)
	if msgs[2]["content"] != "Respond in one word." {
		t.Errorf("text system item content = %v, want the text kept native: %s", msgs[2]["content"], wire)
	}
	oc, _ := msgs[2]["output_config"].(map[string]any)
	if oc == nil || oc["effort"] != "high" {
		t.Errorf("text system item lost its override, output_config = %v: %s", oc, wire)
	}
}

// TestToAnthropicResponsesRequest_PerMessageEffortTextSystemOnSonnet55: Sonnet 5.5 supports
// both mid-conversation system messages and per-message effort (Anthropic docs), so a
// text-bearing system item with an override must take the native path there, not be
// inlined as a user reminder with the override dropped.
func TestToAnthropicResponsesRequest_PerMessageEffortTextSystemOnSonnet55(t *testing.T) {
	t.Parallel()

	var input []schemas.ResponsesMessage
	if err := schemas.Unmarshal([]byte(`[
		{"type":"message","role":"user","content":"Hello"},
		{"type":"message","role":"system","content":"Respond in one word.","output_config":{"effort":"low"}}
	]`), &input); err != nil {
		t.Fatalf("decode neutral input: %v", err)
	}
	ctx := effortTestCtx(t)
	out, err := ToAnthropicResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic, Model: "claude-sonnet-5-5", Input: input,
		Params: &schemas.ResponsesParameters{MaxOutputTokens: schemas.Ptr(128)},
	})
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}
	msgs, wire := jsonArrayOfObjects(t, out.Messages)
	if len(msgs) != 2 || msgs[1]["role"] != "system" {
		t.Fatalf("forwarded roles wrong, want user,system on Sonnet 5.5: %s", wire)
	}
	oc, _ := msgs[1]["output_config"].(map[string]any)
	if oc == nil || oc["effort"] != "low" {
		t.Errorf("Sonnet 5.5 text system item lost its override, output_config = %v: %s", oc, wire)
	}
}

// TestToAnthropicResponsesRequest_PerMessageEffortSystemGroupTrailingBoundary: the placement
// rule applies to a consecutive system group "as a whole", so the FIRST member of
// [user, system(text+override), system(text)] must be judged by what follows the group (end
// of messages), not by its sibling. Judging it by the sibling inlines it and drops its override.
func TestToAnthropicResponsesRequest_PerMessageEffortSystemGroupTrailingBoundary(t *testing.T) {
	t.Parallel()

	var input []schemas.ResponsesMessage
	if err := schemas.Unmarshal([]byte(`[
		{"type":"message","role":"user","content":"Hello"},
		{"type":"message","role":"system","content":"Answer in lowercase.","output_config":{"effort":"low"}},
		{"type":"message","role":"system","content":"Be brief."}
	]`), &input); err != nil {
		t.Fatalf("decode neutral input: %v", err)
	}
	ctx := effortTestCtx(t)
	out, err := ToAnthropicResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic, Model: "claude-opus-5-5", Input: input,
		Params: &schemas.ResponsesParameters{MaxOutputTokens: schemas.Ptr(128)},
	})
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}
	msgs, wire := jsonArrayOfObjects(t, out.Messages)
	if len(msgs) != 3 || msgs[0]["role"] != "user" || msgs[1]["role"] != "system" || msgs[2]["role"] != "system" {
		t.Fatalf("forwarded roles wrong, want user,system,system (first system item was inlined as a user reminder): %s", wire)
	}
	oc, _ := msgs[1]["output_config"].(map[string]any)
	if oc == nil || oc["effort"] != "low" {
		t.Errorf("first system item lost its override, output_config = %v: %s", oc, wire)
	}
}

// TestToAnthropicResponsesRequest_LeadingEffortOnlyKeepsSystemPromptHoisted: an effort-only
// item emitted first is not a conversation turn. The system prompt that follows it is still
// the leading system run and must be hoisted into the top-level system block, not demoted to
// a mid-conversation reminder (which here would be inlined as a user turn, leaving system null).
func TestToAnthropicResponsesRequest_LeadingEffortOnlyKeepsSystemPromptHoisted(t *testing.T) {
	t.Parallel()

	var input []schemas.ResponsesMessage
	if err := schemas.Unmarshal([]byte(`[
		{"type":"message","role":"system","content":[],"output_config":{"effort":"low"}},
		{"type":"message","role":"system","content":"Always answer in JSON."},
		{"type":"message","role":"user","content":"Hello"}
	]`), &input); err != nil {
		t.Fatalf("decode neutral input: %v", err)
	}
	ctx := effortTestCtx(t)
	out, err := ToAnthropicResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic, Model: "claude-opus-5-5", Input: input,
		Params: &schemas.ResponsesParameters{MaxOutputTokens: schemas.Ptr(128)},
	})
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}
	if out.System == nil || out.System.ContentStr == nil || *out.System.ContentStr != "Always answer in JSON." {
		t.Errorf("leading system prompt was not hoisted into the system block: %+v", out.System)
	}
	msgs, wire := jsonArrayOfObjects(t, out.Messages)
	if len(msgs) != 2 || msgs[0]["role"] != "system" || msgs[1]["role"] != "user" {
		t.Fatalf("forwarded roles wrong, want system(effort-only),user (system prompt demoted to a user reminder): %s", wire)
	}
	assertEffortOnlySystemMessage(t, msgs[0], "low", wire)
}

// A turn that ended on a requested stop sequence must reach Anthropic-compatible
// clients as stop_reason "stop_sequence" with the matched string, not end_turn/null,
// after the Anthropic -> Bifrost Responses -> Anthropic round trip.

// assertStopFields checks that a converted stop_reason and stop_sequence match the
// expected pair, treating a nil wantSeq as requiring a null stop_sequence.
func assertStopFields(t *testing.T, gotReason AnthropicStopReason, gotSeq *string, wantReason AnthropicStopReason, wantSeq *string) {
	t.Helper()
	if gotReason != wantReason {
		t.Errorf("stop_reason = %q, want %q", gotReason, wantReason)
	}
	switch {
	case wantSeq == nil && gotSeq != nil:
		t.Errorf("stop_sequence = %q, want null", *gotSeq)
	case wantSeq != nil && (gotSeq == nil || *gotSeq != *wantSeq):
		t.Errorf("stop_sequence = %v, want %q", gotSeq, *wantSeq)
	}
}

// TestStopSequence_NonStreamingRoundTrip verifies that a non-streaming Anthropic message
// keeps its stop_reason and matched stop_sequence through the Responses round trip, and
// that a stray sequence on a non-stop_sequence reason is dropped.
func TestStopSequence_NonStreamingRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		reason     AnthropicStopReason
		sequence   *string
		wantReason AnthropicStopReason
		wantSeq    *string
	}{
		{"stop_sequence keeps reason and match", AnthropicStopReasonStopSequence, schemas.Ptr("###"), AnthropicStopReasonStopSequence, schemas.Ptr("###")},
		{"end_turn stays end_turn", AnthropicStopReasonEndTurn, nil, AnthropicStopReasonEndTurn, nil},
		{"stray sequence on end_turn is dropped", AnthropicStopReasonEndTurn, schemas.Ptr("###"), AnthropicStopReasonEndTurn, nil},
		{"max_tokens unaffected", AnthropicStopReasonMaxTokens, nil, AnthropicStopReasonMaxTokens, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()

			anthropicResp := &AnthropicMessageResponse{
				ID:           "msg_stopseq",
				Type:         "message",
				Role:         "assistant",
				Model:        "claude-sonnet-4-5",
				StopReason:   tt.reason,
				StopSequence: tt.sequence,
				Content:      []AnthropicContentBlock{{Type: AnthropicContentBlockTypeText, Text: schemas.Ptr("1 2 3")}},
			}
			result := ToAnthropicResponsesResponse(ctx, anthropicResp.ToBifrostResponsesResponse(ctx))
			assertStopFields(t, result.StopReason, result.StopSequence, tt.wantReason, tt.wantSeq)
		})
	}
}

// TestStopSequence_BifrostStopWithoutSequenceIsEndTurn verifies that the ambiguous "stop"
// OpenAI-style providers report with no matched sequence keeps mapping to end_turn
// rather than guessing stop_sequence.
func TestStopSequence_BifrostStopWithoutSequenceIsEndTurn(t *testing.T) {
	t.Parallel()
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	result := ToAnthropicResponsesResponse(ctx, &schemas.BifrostResponsesResponse{
		Model:      "gpt-5.1",
		StopReason: schemas.Ptr(string(schemas.BifrostFinishReasonStop)),
	})
	assertStopFields(t, result.StopReason, result.StopSequence, AnthropicStopReasonEndTurn, nil)
}

// TestStopSequence_StreamingRoundTrip verifies that stop_reason and stop_sequence survive
// streaming conversion, both on the relayed message_delta and on the message_delta
// synthesized from response.completed.
func TestStopSequence_StreamingRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		reason     AnthropicStopReason
		sequence   *string
		wantReason AnthropicStopReason
		wantSeq    *string
	}{
		{"stop_sequence", AnthropicStopReasonStopSequence, schemas.Ptr("END"), AnthropicStopReasonStopSequence, schemas.Ptr("END")},
		{"end_turn", AnthropicStopReasonEndTurn, nil, AnthropicStopReasonEndTurn, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ingressCtx := context.WithValue(context.Background(), schemas.BifrostContextKeyIntegrationType, "anthropic")
			state := newFallbackStreamState()

			delta := &AnthropicStreamEvent{
				Type:  AnthropicStreamEventTypeMessageDelta,
				Delta: &AnthropicStreamDelta{StopReason: schemas.Ptr(tt.reason), StopSequence: tt.sequence},
				Usage: &AnthropicUsage{OutputTokens: 3},
			}
			deltaResps, bErr, _ := delta.ToBifrostResponsesStream(ingressCtx, 1, state)
			if bErr != nil || len(deltaResps) != 1 || deltaResps[0].Response == nil {
				t.Fatalf("unexpected message_delta conversion: %v %+v", bErr, deltaResps)
			}
			stop := &AnthropicStreamEvent{Type: AnthropicStreamEventTypeMessageStop}
			stopResps, bErr, _ := stop.ToBifrostResponsesStream(ingressCtx, 2, state)
			if bErr != nil || len(stopResps) != 1 || stopResps[0].Response == nil {
				t.Fatalf("unexpected message_stop conversion: %v %+v", bErr, stopResps)
			}

			// Egress of the relayed message_delta event.
			egressCtx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()
			egressCtx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
			events := ToAnthropicResponsesStreamResponse(egressCtx, deltaResps[0])
			if len(events) == 0 || events[0].Delta == nil || events[0].Delta.StopReason == nil {
				t.Fatalf("message_delta egress missing stop_reason: %+v", events)
			}
			assertStopFields(t, *events[0].Delta.StopReason, events[0].Delta.StopSequence, tt.wantReason, tt.wantSeq)

			// Egress when message_delta is synthesized from response.completed.
			completedCtx, cancel2 := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel2()
			events = ToAnthropicResponsesStreamResponse(completedCtx, stopResps[0])
			if len(events) != 2 || events[0].Delta == nil || events[0].Delta.StopReason == nil {
				t.Fatalf("completed egress missing message_delta: %+v", events)
			}
			assertStopFields(t, *events[0].Delta.StopReason, events[0].Delta.StopSequence, tt.wantReason, tt.wantSeq)
		})
	}
}

// TestConvertBifrostMessages_UnsupportedToolCallWithoutShellAction verifies the
// generic fallback still applies to non-shell unsupported tool calls.
func TestConvertBifrostMessages_UnsupportedToolCallWithoutShellAction(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	caps := schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-4-5-20250929")

	callID := "fs_1"
	fileSearch := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFileSearchCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: &callID,
			Name:   schemas.Ptr("file_search"),
		},
	}

	msgs, _ := ConvertBifrostMessagesToAnthropicMessages(ctx, []schemas.ResponsesMessage{fileSearch}, true, caps)
	if len(msgs) != 1 || len(msgs[0].Content.ContentBlocks) != 1 || msgs[0].Content.ContentBlocks[0].Text == nil {
		t.Fatalf("expected a single text block, got %+v", msgs)
	}
	if text := *msgs[0].Content.ContentBlocks[0].Text; !strings.Contains(text, "Tool call: file_search") {
		t.Fatalf("unexpected fallback text: %s", text)
	}
}

// TestConvertBifrostToolsToAnthropicDropsMCPAllowedCallers pins the drop. Anthropic
// answers "tools.0.mcp_toolset.allowed_callers: Extra inputs are not permitted", so the
// restriction is not expressible on a toolset and must not fail the request either.
func TestConvertBifrostToolsToAnthropicDropsMCPAllowedCallers(t *testing.T) {
	caps := schemas.ModelCaps{}
	mcpTool := func(callers []string) schemas.ResponsesTool {
		return schemas.ResponsesTool{
			Type:           schemas.ResponsesToolTypeMCP,
			AllowedCallers: callers,
			ResponsesToolMCP: &schemas.ResponsesToolMCP{
				ServerLabel: "docs",
				ServerURL:   schemas.Ptr("https://mcp.example.com"),
			},
		}
	}

	for _, tc := range []struct {
		name    string
		callers []string
	}{
		{"no callers", nil},
		{"direct", []string{"direct"}},
		{"programmatic", []string{"programmatic"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools, servers, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{mcpTool(tc.callers)}, schemas.Anthropic)
			if err != nil {
				t.Fatalf("convert failed: %v", err)
			}
			if len(servers) != 1 || len(tools) != 1 || tools[0].MCPToolset == nil {
				t.Fatalf("expected one mcp server and one toolset, got %d servers and %+v", len(servers), tools)
			}
			data, err := sonic.Marshal(tools[0])
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}
			if strings.Contains(string(data), "allowed_callers") {
				t.Fatalf("mcp_toolset must not carry allowed_callers: %s", data)
			}
		})
	}

	// The callers are dropped, so they must not reach the shared code-execution
	// resolution either: no version raise, and no failure on a legacy version.
	t.Run("does not raise the code execution version", func(t *testing.T) {
		tools, _, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeCodeInterpreter, ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{}},
			mcpTool([]string{"programmatic"}),
		}, schemas.Anthropic)
		if err != nil {
			t.Fatalf("convert failed: %v", err)
		}
		assertCodeExecutionVersion(t, tools, "code_execution_20250825")
	})

	t.Run("does not trip the legacy version guard", func(t *testing.T) {
		_, _, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{
			{
				Type:                         schemas.ResponsesToolTypeCodeInterpreter,
				ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{Version: schemas.Ptr("code_execution_20250522")},
			},
			mcpTool([]string{"programmatic"}),
		}, schemas.Anthropic)
		if err != nil {
			t.Fatalf("an mcp caller must not fail the request: %v", err)
		}
	})

	// A real programmatic caller alongside an mcp tool is still translated.
	t.Run("a function tool beside it still gets its caller", func(t *testing.T) {
		tools, _, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{
			mcpTool([]string{"programmatic"}),
			{
				Type:                  schemas.ResponsesToolTypeFunction,
				Name:                  schemas.Ptr("query_database"),
				AllowedCallers:        []string{"programmatic"},
				ResponsesToolFunction: &schemas.ResponsesToolFunction{},
			},
		}, schemas.Anthropic)
		if err != nil {
			t.Fatalf("convert failed: %v", err)
		}
		assertToolCallers(t, tools, "query_database", []string{"code_execution_20260120"})
	})
}

// TestConvertBifrostToolsToAnthropicTranslatesProgrammaticCaller runs the rewrite
// through the request converter, including the code_interpreter version it has to
// raise so programmatic tool calling exists at all.
func TestConvertBifrostToolsToAnthropicTranslatesProgrammaticCaller(t *testing.T) {
	caps := schemas.ModelCaps{}
	queryTool := schemas.ResponsesTool{
		Type:                  schemas.ResponsesToolTypeFunction,
		Name:                  schemas.Ptr("query_database"),
		AllowedCallers:        []string{"programmatic"},
		ResponsesToolFunction: &schemas.ResponsesToolFunction{},
	}

	t.Run("version-less code_interpreter is raised to 20260120", func(t *testing.T) {
		tools, _, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeCodeInterpreter, ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{}},
			queryTool,
		}, schemas.Anthropic)
		if err != nil {
			t.Fatalf("convert failed: %v", err)
		}
		assertToolCallers(t, tools, "query_database", []string{"code_execution_20260120"})
		assertCodeExecutionVersion(t, tools, "code_execution_20260120")
	})

	t.Run("explicit version is matched, not raised", func(t *testing.T) {
		tools, _, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{
			{
				Type:                         schemas.ResponsesToolTypeCodeInterpreter,
				ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{Version: schemas.Ptr("code_execution_20250825")},
			},
			queryTool,
		}, schemas.Anthropic)
		if err != nil {
			t.Fatalf("convert failed: %v", err)
		}
		assertToolCallers(t, tools, "query_database", []string{"code_execution_20250825"})
		assertCodeExecutionVersion(t, tools, "code_execution_20250825")
	})

	t.Run("no code execution tool falls back to the auto-injected version", func(t *testing.T) {
		tools, _, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{queryTool}, schemas.Anthropic)
		if err != nil {
			t.Fatalf("convert failed: %v", err)
		}
		assertToolCallers(t, tools, "query_database", []string{"code_execution_20260120"})
	})

	// Legacy 20250522 has no allowed_callers value at all. Dropping the caller would
	// hand the model a tool the request said was sandbox-only, so this fails instead.
	t.Run("legacy 20250522 is rejected rather than silently widened", func(t *testing.T) {
		_, _, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{
			{
				Type:                         schemas.ResponsesToolTypeCodeInterpreter,
				ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{Version: schemas.Ptr("code_execution_20250522")},
			},
			queryTool,
		}, schemas.Anthropic)
		if err == nil {
			t.Fatal("expected a conversion error for a programmatic caller on code_execution_20250522")
		}
		if !strings.Contains(err.Error(), "code_execution_20250522") {
			t.Fatalf("error must name the offending version: %v", err)
		}
	})

	// Beside web search the interpreter is never sent (Anthropic injects its own code
	// execution), so its legacy version must not block the request.
	legacyInterpreter := func(callers ...string) schemas.ResponsesTool {
		return schemas.ResponsesTool{
			Type:                         schemas.ResponsesToolTypeCodeInterpreter,
			AllowedCallers:               callers,
			ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{Version: schemas.Ptr("code_execution_20250522")},
		}
	}
	webSearch := schemas.ResponsesTool{Type: schemas.ResponsesToolTypeWebSearch}

	t.Run("a dropped legacy interpreter with a programmatic caller does not block the request", func(t *testing.T) {
		tools, _, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{webSearch, legacyInterpreter("programmatic")}, schemas.Anthropic)
		if err != nil {
			t.Fatalf("convert failed: %v", err)
		}
		if len(tools) != 1 || tools[0].Type == nil || !strings.HasPrefix(string(*tools[0].Type), "web_search") {
			t.Fatalf("want only the web_search tool, got %+v", tools)
		}
	})

	t.Run("a dropped legacy interpreter does not decide another tool's caller", func(t *testing.T) {
		tools, _, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{webSearch, legacyInterpreter(), queryTool}, schemas.Anthropic)
		if err != nil {
			t.Fatalf("convert failed: %v", err)
		}
		assertToolCallers(t, tools, "query_database", []string{"code_execution_20260120"})
	})

	t.Run("a request without programmatic callers is untouched", func(t *testing.T) {
		tools, _, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeCodeInterpreter, ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{}},
			{
				Type:                  schemas.ResponsesToolTypeFunction,
				Name:                  schemas.Ptr("query_database"),
				AllowedCallers:        []string{"direct"},
				ResponsesToolFunction: &schemas.ResponsesToolFunction{},
			},
		}, schemas.Anthropic)
		if err != nil {
			t.Fatalf("convert failed: %v", err)
		}
		assertToolCallers(t, tools, "query_database", []string{"direct"})
		// The default version stands when nothing asks for programmatic tool calling.
		assertCodeExecutionVersion(t, tools, "code_execution_20250825")
	})
}

func assertToolCallers(t *testing.T, tools []AnthropicTool, name string, want []string) {
	t.Helper()
	for _, tool := range tools {
		if tool.Name != name {
			continue
		}
		if len(tool.AllowedCallers) != len(want) {
			t.Fatalf("%s allowed_callers = %v, want %v", name, tool.AllowedCallers, want)
		}
		for i := range want {
			if tool.AllowedCallers[i] != want[i] {
				t.Fatalf("%s allowed_callers = %v, want %v", name, tool.AllowedCallers, want)
			}
		}
		return
	}
	t.Fatalf("tool %s missing from %+v", name, tools)
}

func assertCodeExecutionVersion(t *testing.T, tools []AnthropicTool, want string) {
	t.Helper()
	for _, tool := range tools {
		if tool.Name != string(AnthropicToolNameCodeExecution) {
			continue
		}
		if tool.Type == nil || string(*tool.Type) != want {
			t.Fatalf("code_execution type = %v, want %s", tool.Type, want)
		}
		return
	}
	t.Fatalf("code_execution tool missing from %+v", tools)
}

// TestConvertBifrostMessages_ApplyPatchCallKeepsOperation verifies that an
// apply_patch_call replayed to Anthropic keeps its file operation.
func TestConvertBifrostMessages_ApplyPatchCallKeepsOperation(t *testing.T) {
	tests := []struct {
		name      string
		operation schemas.ResponsesApplyPatchOperation
		want      []string
		notWant   []string
	}{
		{
			name:      "create_file",
			operation: schemas.ResponsesApplyPatchOperation{Type: "create_file", Path: "hello.txt", Diff: schemas.Ptr("+hello\n")},
			want:      []string{`"type":"create_file"`, `"path":"hello.txt"`, "+hello"},
		},
		{
			name:      "update_file",
			operation: schemas.ResponsesApplyPatchOperation{Type: "update_file", Path: "main.go", Diff: schemas.Ptr("-old\n+new\n")},
			want:      []string{`"type":"update_file"`, `"path":"main.go"`, "-old", "+new"},
		},
		{
			name:      "delete_file carries no diff",
			operation: schemas.ResponsesApplyPatchOperation{Type: "delete_file", Path: "gone.txt"},
			want:      []string{`"type":"delete_file"`, `"path":"gone.txt"`},
			notWant:   []string{"diff"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
			defer cancel()
			caps := schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-4-5-20250929")

			operation := tt.operation
			applyPatchCall := schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeApplyPatchCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:                  schemas.Ptr("apply_patch_call_1"),
					ResponsesApplyPatchCall: &schemas.ResponsesApplyPatchCall{Operation: &operation},
				},
			}

			msgs, _ := ConvertBifrostMessagesToAnthropicMessages(ctx, []schemas.ResponsesMessage{applyPatchCall}, true, caps)
			if len(msgs) != 1 || len(msgs[0].Content.ContentBlocks) != 1 || msgs[0].Content.ContentBlocks[0].Text == nil {
				t.Fatalf("expected a single text block, got %+v", msgs)
			}

			text := *msgs[0].Content.ContentBlocks[0].Text
			if !strings.Contains(text, "apply_patch_call") {
				t.Errorf("apply_patch call text missing the item type, got:\n%s", text)
			}
			for _, want := range tt.want {
				if !strings.Contains(text, want) {
					t.Errorf("apply_patch call text missing %q, got:\n%s", want, text)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(text, notWant) {
					t.Errorf("apply_patch call text unexpectedly contains %q, got:\n%s", notWant, text)
				}
			}
		})
	}
}

// TestConvertBifrostMessages_UnsupportedToolCallTurnOrder verifies that an unsupported
// tool call replayed as text keeps the reasoning before it and follows earlier tool calls.
func TestConvertBifrostMessages_UnsupportedToolCallTurnOrder(t *testing.T) {
	applyPatchCall := schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeApplyPatchCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: schemas.Ptr("call_patch"),
			ResponsesApplyPatchCall: &schemas.ResponsesApplyPatchCall{
				Operation: &schemas.ResponsesApplyPatchOperation{Type: "update_file", Path: "main.go", Diff: schemas.Ptr("-a\n+b\n")},
			},
		},
	}
	user := schemas.ResponsesMessage{
		Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
		Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("fix the bug")},
	}
	convert := func(t *testing.T, input []schemas.ResponsesMessage) []AnthropicMessage {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()
		msgs, _ := ConvertBifrostMessagesToAnthropicMessages(ctx, input, true, schemas.ResolveModelCaps(schemas.Anthropic, "claude-sonnet-4-5-20250929"))
		return msgs
	}

	t.Run("reasoning leads the call turn", func(t *testing.T) {
		reasoning := schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary: []schemas.ResponsesReasoningSummary{{Type: schemas.ResponsesReasoningContentBlockTypeSummaryText, Text: "patch main.go"}},
			},
		}
		msgs := convert(t, []schemas.ResponsesMessage{user, reasoning, applyPatchCall})
		if len(msgs) != 2 {
			t.Fatalf("expected user + assistant, got %+v", msgs)
		}
		blocks := msgs[1].Content.ContentBlocks
		if len(blocks) != 2 || blocks[0].Type != AnthropicContentBlockTypeThinking || blocks[1].Type != AnthropicContentBlockTypeText {
			t.Fatalf("expected [thinking, text] on the call turn, got %+v", blocks)
		}
	})

	t.Run("call follows a pending tool_use", func(t *testing.T) {
		functionCall := schemas.ResponsesMessage{
			Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:    schemas.Ptr("call_read"),
				Name:      schemas.Ptr("read"),
				Arguments: schemas.Ptr("{}"),
			},
		}
		msgs := convert(t, []schemas.ResponsesMessage{user, functionCall, applyPatchCall})
		if len(msgs) != 3 {
			t.Fatalf("expected user + tool_use + call text, got %+v", msgs)
		}
		if b := msgs[1].Content.ContentBlocks; len(b) != 1 || b[0].Type != AnthropicContentBlockTypeToolUse {
			t.Fatalf("expected the tool_use turn first, got %+v", b)
		}
		if b := msgs[2].Content.ContentBlocks; len(b) != 1 || b[0].Text == nil || !strings.Contains(*b[0].Text, "apply_patch_call") {
			t.Fatalf("expected the apply_patch text after the tool_use, got %+v", b)
		}
	})

	// Anthropic rejects a user turn whose tool_result is not first, so a held
	// function result must be written before a text-rendered tool output.
	for _, outputType := range []schemas.ResponsesMessageType{
		schemas.ResponsesMessageTypeShellCallOutput,
		schemas.ResponsesMessageTypeApplyPatchCallOutput,
		schemas.ResponsesMessageTypeLocalShellCallOutput,
		schemas.ResponsesMessageTypeCustomToolCallOutput,
	} {
		t.Run("pending tool_result precedes "+string(outputType), func(t *testing.T) {
			functionCall := schemas.ResponsesMessage{
				Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("call_fn"), Name: schemas.Ptr("get_time"), Arguments: schemas.Ptr("{}")},
			}
			functionOutput := schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("call_fn"),
					Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("12:00")}},
			}
			toolOutput := schemas.ResponsesMessage{
				Type: schemas.Ptr(outputType),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("call_patch"),
					Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: schemas.Ptr("done")}},
			}
			// Either output order must replay the same way.
			for name, outputs := range map[string][]schemas.ResponsesMessage{
				"function output first": {functionOutput, toolOutput},
				"tool output first":     {toolOutput, functionOutput},
			} {
				t.Run(name, func(t *testing.T) {
					msgs := convert(t, append([]schemas.ResponsesMessage{user, functionCall, applyPatchCall}, outputs...))
					if len(msgs) != 5 {
						t.Fatalf("expected user, tool_use, call text, tool_result, output text; got %+v", msgs)
					}
					if b := msgs[3].Content.ContentBlocks; len(b) != 1 || b[0].Type != AnthropicContentBlockTypeToolResult {
						t.Fatalf("expected the tool_result before the tool output text, got %+v", msgs[3])
					}
					if b := msgs[4].Content.ContentBlocks; msgs[4].Role != AnthropicMessageRoleUser || len(b) != 1 || b[0].Type != AnthropicContentBlockTypeText {
						t.Fatalf("expected the tool output text as the last user turn, got %+v", msgs[4])
					}
				})
			}
		})
	}
}

// A failed apply_patch whose output text is absent must still replay as a message.
// Dropping it leaves the turn looking like the patch was never attempted, so the model
// retries or assumes success.
func TestConvertBifrostToolOutputFailureWithoutOutputText(t *testing.T) {
	tests := []struct {
		name string
		msg  schemas.ResponsesMessage
		want string
	}{
		{
			name: "failed status with error string",
			msg: schemas.ResponsesMessage{
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeApplyPatchCallOutput),
				Status: schemas.Ptr("failed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("ap_1"),
					Error: &schemas.ResponsesToolMessageError{
						ResponsesToolMessageErrorStr: schemas.Ptr("context lines did not match"),
					},
				},
			},
			want: "[tool call failed: context lines did not match]",
		},
		{
			name: "failed status with structured error message",
			msg: schemas.ResponsesMessage{
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeApplyPatchCallOutput),
				Status: schemas.Ptr("failed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("ap_2"),
					Error: &schemas.ResponsesToolMessageError{
						ResponsesToolMessageErrorStruct: &schemas.ResponsesToolMessageErrorStruct{
							Type:    "invalid_patch",
							Message: schemas.Ptr("file not found"),
						},
					},
				},
			},
			want: "[tool call failed: file not found]",
		},
		{
			name: "structured error falls back to its type",
			msg: schemas.ResponsesMessage{
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeApplyPatchCallOutput),
				Status: schemas.Ptr("failed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("ap_3"),
					Error: &schemas.ResponsesToolMessageError{
						ResponsesToolMessageErrorStruct: &schemas.ResponsesToolMessageErrorStruct{Type: "invalid_patch"},
					},
				},
			},
			want: "[tool call failed: invalid_patch]",
		},
		{
			name: "failed status with no error detail",
			msg: schemas.ResponsesMessage{
				Type:                 schemas.Ptr(schemas.ResponsesMessageTypeApplyPatchCallOutput),
				Status:               schemas.Ptr("failed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("ap_4")},
			},
			want: "[tool call failed]",
		},
		{
			name: "error without a failed status",
			msg: schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeApplyPatchCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("ap_5"),
					Error: &schemas.ResponsesToolMessageError{
						ResponsesToolMessageErrorStr: schemas.Ptr("patch rejected"),
					},
				},
			},
			want: "[tool call error: patch rejected]",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := convertBifrostToolOutputToAnthropicMessage(&tc.msg)
			if got == nil {
				t.Fatalf("message dropped; want replay text %q", tc.want)
			}
			if len(got.Content.ContentBlocks) != 1 || got.Content.ContentBlocks[0].Text == nil {
				t.Fatalf("content = %+v, want one text block", got.Content)
			}
			if text := *got.Content.ContentBlocks[0].Text; text != tc.want {
				t.Fatalf("text = %q, want %q", text, tc.want)
			}
			if got.Role != AnthropicMessageRoleUser {
				t.Fatalf("role = %q, want user", got.Role)
			}
		})
	}
}

// computerCallHistoryRequest builds an OpenAI-shaped history with one computer_call and its screenshot output.
func computerCallHistoryRequest(t *testing.T, computerCall string) *AnthropicMessageRequest {
	t.Helper()
	body := `{"provider":"anthropic","model":"claude-sonnet-4-6","input":[
		{"type":"message","role":"user","content":"click then type"},
		` + computerCall + `,
		{"type":"computer_call_output","call_id":"call_1","output":{"type":"computer_screenshot","image_url":"data:image/png;base64,iVBORw0KGgo="}}],
		"params":{"tools":[{"type":"computer_use_preview","display_width":1280,"display_height":800,"environment":"browser"}]}}`
	var req schemas.BifrostResponsesRequest
	if err := sonic.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	out, err := ToAnthropicResponsesRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), &req)
	if err != nil {
		t.Fatalf("convert failed: %v", err)
	}
	if len(out.Messages) != 3 {
		t.Fatalf("expected user, assistant, user messages, got %d", len(out.Messages))
	}
	return out
}

// A batched computer_call becomes one tool_use per action, and the single
// screenshot output gains a filler result for each earlier action.
func TestToAnthropicResponsesRequest_ComputerCallActionsSplit(t *testing.T) {
	out := computerCallHistoryRequest(t, `{"type":"computer_call","id":"cu_1","call_id":"call_1","status":"completed",
		"actions":[{"type":"click","button":"left","x":100,"y":200},{"type":"type","text":"hi"}]}`)

	calls := out.Messages[1].Content.ContentBlocks
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool_use blocks, got %d", len(calls))
	}
	wantCalls := []struct{ id, input string }{
		{"call_1_0", `{"action":"left_click","coordinate":[100,200]}`},
		{"call_1", `{"action":"type","text":"hi"}`},
	}
	for i, want := range wantCalls {
		if calls[i].ID == nil || *calls[i].ID != want.id {
			t.Fatalf("tool_use %d: expected id %q, got %v", i, want.id, calls[i].ID)
		}
		if string(calls[i].Input) != want.input {
			t.Fatalf("tool_use %d: expected input %s, got %s", i, want.input, calls[i].Input)
		}
	}

	results := out.Messages[2].Content.ContentBlocks
	if len(results) != 2 {
		t.Fatalf("expected 2 tool_result blocks, got %d", len(results))
	}
	if *results[0].ToolUseID != "call_1_0" || results[0].Content == nil || results[0].Content.ContentStr == nil {
		t.Fatalf("expected a text filler result for call_1_0, got %+v", results[0])
	}
	if *results[1].ToolUseID != "call_1" || results[1].Content == nil || len(results[1].Content.ContentBlocks) != 1 ||
		results[1].Content.ContentBlocks[0].Type != AnthropicContentBlockTypeImage {
		t.Fatalf("expected the screenshot on call_1, got %+v", results[1])
	}
}

// A computer_call with the single action field keeps one tool_use under the call id.
func TestToAnthropicResponsesRequest_ComputerCallSingleAction(t *testing.T) {
	out := computerCallHistoryRequest(t, `{"type":"computer_call","id":"cu_1","call_id":"call_1","status":"completed",
		"action":{"type":"click","button":"left","x":100,"y":200}}`)

	calls := out.Messages[1].Content.ContentBlocks
	if len(calls) != 1 || calls[0].ID == nil || *calls[0].ID != "call_1" {
		t.Fatalf("expected one tool_use with id call_1, got %+v", calls)
	}
	if string(calls[0].Input) != `{"action":"left_click","coordinate":[100,200]}` {
		t.Fatalf("unexpected input %s", calls[0].Input)
	}
	results := out.Messages[2].Content.ContentBlocks
	if len(results) != 1 || *results[0].ToolUseID != "call_1" {
		t.Fatalf("expected one tool_result for call_1, got %+v", results)
	}
}

// assertComputerCallActions checks a computer_call carries its action in both action and actions.
func assertComputerCallActions(t *testing.T, item schemas.ResponsesMessage) {
	t.Helper()
	if item.Type == nil || *item.Type != schemas.ResponsesMessageTypeComputerCall {
		t.Fatalf("expected computer_call, got %v", item.Type)
	}
	data, err := sonic.Marshal(item)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	for _, want := range []string{
		`"action":{"type":"click","x":100,"y":200,"button":"left"}`,
		`"actions":[{"type":"click","x":100,"y":200,"button":"left"}]`,
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("expected %s in %s", want, data)
		}
	}
}

// A Claude computer tool_use reaches the client with both action and actions.
func TestToBifrostResponsesResponse_ComputerCallActions(t *testing.T) {
	var resp AnthropicMessageResponse
	if err := sonic.Unmarshal([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","stop_reason":"tool_use",
		"content":[{"type":"tool_use","id":"toolu_1","name":"computer","input":{"action":"left_click","coordinate":[100,200]}}],
		"usage":{"input_tokens":1,"output_tokens":1}}`), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	out := resp.ToBifrostResponsesResponse(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))
	if len(out.Output) != 1 {
		t.Fatalf("expected one output item, got %d", len(out.Output))
	}
	assertComputerCallActions(t, out.Output[0])
}

// The streaming computer_call output_item.done carries both action and actions.
func TestToBifrostResponsesStream_ComputerCallActions(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	state := AcquireAnthropicResponsesStreamState()
	defer ReleaseAnthropicResponsesStreamState(state)

	var done *schemas.ResponsesMessage
	for seq, raw := range []string{
		`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"computer","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"action\":\"left_click\","}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"coordinate\":[100,200]}"}}`,
		`{"type":"content_block_stop","index":0}`,
	} {
		var chunk AnthropicStreamEvent
		if err := sonic.Unmarshal([]byte(raw), &chunk); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		responses, bErr, _ := chunk.ToBifrostResponsesStream(ctx, seq, state)
		if bErr != nil {
			t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
		}
		for _, r := range responses {
			if r.Type == schemas.ResponsesStreamResponseTypeOutputItemDone && r.Item != nil {
				done = r.Item
			}
		}
	}
	if done == nil {
		t.Fatal("expected an output_item.done for the computer_call")
	}
	assertComputerCallActions(t, *done)
}

// OpenAI rejects a replayed computer_call whose id does not begin with "cu", so the
// item minted for an assistant computer tool_use must not reuse the function call prefix.
func TestAnthropicComputerToolUseReplaysWithComputerCallID(t *testing.T) {
	msgs := []AnthropicMessage{
		{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentStr: schemas.Ptr("Take a screenshot.")}},
		{Role: AnthropicMessageRoleAssistant, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{{
			Type: AnthropicContentBlockTypeToolUse, ID: schemas.Ptr("call_turn2"), Name: schemas.Ptr("computer"), Input: json.RawMessage(`{"action":"screenshot"}`),
		}}}},
	}
	for _, grouped := range []bool{true, false} {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		var call *schemas.ResponsesMessage
		out := convertAnthropicMessagesToBifrostMessages(ctx, msgs, nil, false, grouped, false)
		cancel()
		for i := range out {
			if out[i].Type != nil && *out[i].Type == schemas.ResponsesMessageTypeComputerCall {
				call = &out[i]
			}
		}
		if call == nil || call.ID == nil || !strings.HasPrefix(*call.ID, "cu_") {
			t.Fatalf("grouped=%v: want a computer_call with a cu_ id, got %+v", grouped, call)
		}
	}
}

// A computer_call's actions only arrive on output_item.done, often as actions with no
// action (OpenAI), so each action must stream as a complete tool_use block, matching the
// non-streaming conversion.
func TestToAnthropicResponsesStream_ComputerCallActions(t *testing.T) {
	type block struct {
		index int
		id    string
		input string
	}
	collect := func(t *testing.T, ctx *schemas.BifrostContext, events []*schemas.BifrostResponsesStreamResponse) []block {
		var blocks []block
		open := map[int]*block{}
		for _, ev := range events {
			for _, out := range ToAnthropicResponsesStreamResponse(ctx, ev) {
				switch out.Type {
				case AnthropicStreamEventTypeContentBlockStart:
					if out.ContentBlock == nil || out.ContentBlock.Type != AnthropicContentBlockTypeToolUse {
						continue
					}
					b := &block{index: *out.Index}
					if out.ContentBlock.ID != nil {
						b.id = *out.ContentBlock.ID
					}
					open[*out.Index] = b
				case AnthropicStreamEventTypeContentBlockDelta:
					if b := open[*out.Index]; b != nil && out.Delta != nil && out.Delta.PartialJSON != nil {
						b.input += *out.Delta.PartialJSON
					}
				case AnthropicStreamEventTypeContentBlockStop:
					if b := open[*out.Index]; b != nil {
						blocks = append(blocks, *b)
						delete(open, *out.Index)
					}
				}
			}
		}
		if len(open) != 0 {
			t.Fatalf("tool_use blocks left open: %+v", open)
		}
		return blocks
	}
	computerCall := func(status string, actions ...schemas.ResponsesComputerToolCallAction) *schemas.ResponsesMessage {
		return &schemas.ResponsesMessage{
			ID:     schemas.Ptr("cu_1"),
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeComputerCall),
			Status: schemas.Ptr(status),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID:                    schemas.Ptr("call_1"),
				ResponsesComputerToolCall: &schemas.ResponsesComputerToolCall{Actions: actions},
			},
		}
	}
	stream := func(prefix []*schemas.BifrostResponsesStreamResponse, done *schemas.ResponsesMessage) []*schemas.BifrostResponsesStreamResponse {
		oi := len(prefix)
		return append(prefix,
			&schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: schemas.Ptr(oi), Item: computerCall("in_progress")},
			&schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: schemas.Ptr(oi), Item: done},
		)
	}
	actionOf := func(t *testing.T, input string) string {
		var m map[string]any
		if err := sonic.Unmarshal([]byte(input), &m); err != nil {
			t.Fatalf("tool_use input %q is not JSON: %v", input, err)
		}
		a, _ := m["action"].(string)
		return a
	}
	newCtx := func() *schemas.BifrostContext { return schemas.NewBifrostContext(context.Background(), time.Time{}) }

	t.Run("single action sent only as actions", func(t *testing.T) {
		blocks := collect(t, newCtx(), stream(nil, computerCall("completed", schemas.ResponsesComputerToolCallAction{Type: "screenshot"})))
		if len(blocks) != 1 || blocks[0].id != "call_1" || actionOf(t, blocks[0].input) != "screenshot" {
			t.Fatalf("want one screenshot tool_use with the call id, got %+v", blocks)
		}
	})

	t.Run("batched actions become one block each", func(t *testing.T) {
		blocks := collect(t, newCtx(), stream(nil, computerCall("completed",
			schemas.ResponsesComputerToolCallAction{Type: "click", X: schemas.Ptr(100), Y: schemas.Ptr(200), Button: schemas.Ptr("left")},
			schemas.ResponsesComputerToolCallAction{Type: "type", Text: schemas.Ptr("hello")},
		)))
		if len(blocks) != 2 {
			t.Fatalf("want two tool_use blocks, got %+v", blocks)
		}
		if blocks[0].id != "call_1_0" || blocks[1].id != "call_1" {
			t.Fatalf("ids = %q, %q; want call_1_0, call_1 (the last keeps the call id)", blocks[0].id, blocks[1].id)
		}
		if actionOf(t, blocks[0].input) != "left_click" || actionOf(t, blocks[1].input) != "type" {
			t.Fatalf("inputs = %s, %s", blocks[0].input, blocks[1].input)
		}
		if blocks[1].index != blocks[0].index+1 {
			t.Fatalf("indices = %d, %d; want consecutive", blocks[0].index, blocks[1].index)
		}
	})

	t.Run("a preceding text block keeps its index", func(t *testing.T) {
		ctx := newCtx()
		text := []*schemas.BifrostResponsesStreamResponse{
			{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: schemas.Ptr(0), Item: &schemas.ResponsesMessage{
				ID: schemas.Ptr("msg_1"), Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant)}},
			{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: schemas.Ptr(0), Item: &schemas.ResponsesMessage{
				ID: schemas.Ptr("msg_1"), Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant)}},
		}
		blocks := collect(t, ctx, stream(text, computerCall("completed", schemas.ResponsesComputerToolCallAction{Type: "screenshot"})))
		if len(blocks) != 1 || blocks[0].index != 1 {
			t.Fatalf("want the tool_use at index 1 after the text block, got %+v", blocks)
		}
	})

	t.Run("Claude computer stream round-trips", func(t *testing.T) {
		ctx := newCtx()
		state := AcquireAnthropicResponsesStreamState()
		defer ReleaseAnthropicResponsesStreamState(state)
		var neutral []*schemas.BifrostResponsesStreamResponse
		for seq, raw := range []string{
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"computer","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"action\":\"left_click\",\"coordinate\":[100,200]}"}}`,
			`{"type":"content_block_stop","index":0}`,
		} {
			var chunk AnthropicStreamEvent
			if err := sonic.Unmarshal([]byte(raw), &chunk); err != nil {
				t.Fatalf("unmarshal event: %v", err)
			}
			responses, bErr, _ := chunk.ToBifrostResponsesStream(ctx, seq, state)
			if bErr != nil {
				t.Fatalf("ToBifrostResponsesStream error: %v", bErr)
			}
			neutral = append(neutral, responses...)
		}
		blocks := collect(t, ctx, neutral)
		if len(blocks) != 1 || blocks[0].id != "toolu_1" || actionOf(t, blocks[0].input) != "left_click" {
			t.Fatalf("want the left_click tool_use back unchanged, got %+v", blocks)
		}
	})
}

// The fallback must not disturb the existing paths: real output text still wins, and an
// output carrying neither text nor a failure is still dropped rather than replayed as
// an empty user turn.
func TestConvertBifrostToolOutputFallbackPreservesExistingBehavior(t *testing.T) {
	t.Run("output text wins over the failure fallback", func(t *testing.T) {
		msg := schemas.ResponsesMessage{
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeApplyPatchCallOutput),
			Status: schemas.Ptr("failed"),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: schemas.Ptr("ap_6"),
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: schemas.Ptr("failed to apply patch to greet.txt"),
				},
				Error: &schemas.ResponsesToolMessageError{
					ResponsesToolMessageErrorStr: schemas.Ptr("should not be used"),
				},
			},
		}
		got := convertBifrostToolOutputToAnthropicMessage(&msg)
		if got == nil {
			t.Fatal("message dropped")
		}
		if text := *got.Content.ContentBlocks[0].Text; text != "failed to apply patch to greet.txt" {
			t.Fatalf("text = %q, want the original output text", text)
		}
	})

	t.Run("no text and no failure is still dropped", func(t *testing.T) {
		msg := schemas.ResponsesMessage{
			Type:                 schemas.Ptr(schemas.ResponsesMessageTypeApplyPatchCallOutput),
			Status:               schemas.Ptr("completed"),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: schemas.Ptr("ap_7")},
		}
		if got := convertBifrostToolOutputToAnthropicMessage(&msg); got != nil {
			t.Fatalf("message = %+v, want nil", got)
		}
	})
}

// TestConvertBifrostToolsToAnthropic_ServerToolCacheControl pins that a
// cache_control breakpoint on a server tool reaches Anthropic for every branch
// of convertBifrostToolToAnthropic, not only the generic function path.
func TestConvertBifrostToolsToAnthropic_ServerToolCacheControl(t *testing.T) {
	const cc = `"cache_control":{"type":"ephemeral"}`
	for _, tc := range []struct {
		name  string
		model string
		raw   string
	}{
		{"function", "claude-sonnet-4-6", `{"type":"function","name":"get_weather",` + cc + `}`},
		{"computer dated", "claude-sonnet-4-6", `{"type":"computer_use_preview","display_width":1280,"display_height":800,"environment":"browser",` + cc + `}`},
		{"computer toolset", "claude-opus-5-5", `{"type":"computer_use_preview","display_width":1280,"display_height":800,"environment":"browser",` + cc + `}`},
		{"code interpreter", "claude-sonnet-4-6", `{"type":"code_interpreter",` + cc + `}`},
		{"web search", "claude-sonnet-4-6", `{"type":"web_search",` + cc + `}`},
		{"web fetch", "claude-sonnet-4-6", `{"type":"web_fetch",` + cc + `}`},
		{"memory", "claude-sonnet-4-6", `{"type":"memory_20250818","name":"memory",` + cc + `}`},
		{"tool search", "claude-sonnet-4-6", `{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex",` + cc + `}`},
		{"local shell", "claude-sonnet-4-6", `{"type":"local_shell",` + cc + `}`},
		{"text editor", "claude-sonnet-4-6", `{"type":"text_editor_20250728","name":"str_replace_based_edit_tool",` + cc + `}`},
		{"advisor", "claude-sonnet-4-6", `{"type":"advisor_20260301","name":"advisor","model":"claude-opus-4-8",` + cc + `}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caps := schemas.ResolveModelCaps(schemas.Anthropic, tc.model)
			tools, _, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{responsesToolFromJSON(t, tc.raw)}, schemas.Anthropic)
			if err != nil {
				t.Fatalf("convert failed: %v", err)
			}
			if len(tools) != 1 {
				t.Fatalf("expected one tool, got %d", len(tools))
			}
			data, err := sonic.Marshal(tools[0])
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}
			if !strings.Contains(string(data), cc) {
				t.Fatalf("cache_control dropped: %s", data)
			}
		})
	}
}

// TestAnthropicToolsRoundTrip_ServerToolCacheControl pins that a cache_control
// breakpoint on an inbound /v1/messages server tool survives the Anthropic ->
// Responses -> Anthropic round trip, not only on the function tool path.
// The outgoing tool must own its cache_control: stripping the scope for a cloud
// surface must not reach the caller's tool, or an Anthropic fallback built from the
// same request loses the scope it asked for.
func TestServerToolCacheControlStripLeavesSourceUnchanged(t *testing.T) {
	const model = "claude-sonnet-4-6"
	strippedForVertex := func(t *testing.T, tools []AnthropicTool) {
		t.Helper()
		req := &AnthropicMessageRequest{Model: model, Tools: tools}
		stripUnsupportedAnthropicFields(req, schemas.Vertex, model)
		if cc := req.Tools[0].CacheControl; cc != nil && cc.Scope != nil {
			t.Fatalf("precondition: Vertex strip kept the scope %q", *cc.Scope)
		}
	}

	t.Run("responses tool to anthropic", func(t *testing.T) {
		source := schemas.ResponsesTool{Type: schemas.ResponsesToolTypeWebSearch,
			CacheControl: &schemas.CacheControl{Type: schemas.CacheControlTypeEphemeral, Scope: schemas.Ptr("global")}}
		tools, _, err := convertBifrostToolsToAnthropic(schemas.ResolveModelCaps(schemas.Vertex, model), []schemas.ResponsesTool{source}, schemas.Vertex)
		if err != nil || len(tools) != 1 {
			t.Fatalf("convert: %v, %+v", err, tools)
		}
		strippedForVertex(t, tools)
		if source.CacheControl.Scope == nil || *source.CacheControl.Scope != "global" {
			t.Fatalf("the caller's tool lost its scope: %+v", source.CacheControl)
		}
	})

	t.Run("anthropic tool through responses and back", func(t *testing.T) {
		var req AnthropicMessageRequest
		raw := `{"model":"anthropic/` + model + `","max_tokens":64,"messages":[{"role":"user","content":"hi"}],` +
			`"tools":[{"type":"web_search_20250305","name":"web_search","cache_control":{"type":"ephemeral","scope":"global"}}]}`
		if err := sonic.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		bifrostReq := req.ToBifrostResponsesRequest(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))
		tools, _, err := convertBifrostToolsToAnthropic(schemas.ResolveModelCaps(schemas.Vertex, model), bifrostReq.Params.Tools, schemas.Vertex)
		if err != nil || len(tools) != 1 {
			t.Fatalf("convert: %v, %+v", err, tools)
		}
		strippedForVertex(t, tools)
		if cc := bifrostReq.Params.Tools[0].CacheControl; cc == nil || cc.Scope == nil || *cc.Scope != "global" {
			t.Fatalf("the neutral tool lost its scope: %+v", cc)
		}
		if cc := req.Tools[0].CacheControl; cc == nil || cc.Scope == nil || *cc.Scope != "global" {
			t.Fatalf("the client's tool lost its scope: %+v", cc)
		}
	})
}

func TestAnthropicToolsRoundTrip_ServerToolCacheControl(t *testing.T) {
	const cc = `"cache_control":{"type":"ephemeral"}`
	for _, tc := range []struct {
		name  string
		model string
		raw   string
	}{
		{"custom", "claude-sonnet-4-6", `{"name":"get_weather","input_schema":{"type":"object","properties":{}},` + cc + `}`},
		{"web search", "claude-sonnet-4-6", `{"type":"web_search_20250305","name":"web_search",` + cc + `}`},
		{"web fetch", "claude-sonnet-4-6", `{"type":"web_fetch_20250910","name":"web_fetch",` + cc + `}`},
		{"computer dated", "claude-sonnet-4-6", `{"type":"computer_20251124","name":"computer","display_width_px":1280,"display_height_px":800,` + cc + `}`},
		{"computer toolset", "claude-opus-5-5", `{"type":"computer_toolset_20260801",` + cc + `}`},
		{"code execution", "claude-sonnet-4-6", `{"type":"code_execution_20250825","name":"code_execution",` + cc + `}`},
		{"memory", "claude-sonnet-4-6", `{"type":"memory_20250818","name":"memory",` + cc + `}`},
		{"tool search", "claude-sonnet-4-6", `{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex",` + cc + `}`},
		{"bash", "claude-sonnet-4-6", `{"type":"bash_20250124","name":"bash",` + cc + `}`},
		{"text editor", "claude-sonnet-4-6", `{"type":"text_editor_20250728","name":"str_replace_based_edit_tool",` + cc + `}`},
		{"advisor", "claude-sonnet-4-6", `{"type":"advisor_20260301","name":"advisor","model":"claude-opus-4-8",` + cc + `}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"model":"anthropic/` + tc.model + `","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[` + tc.raw + `]}`
			var req AnthropicMessageRequest
			if err := sonic.Unmarshal([]byte(raw), &req); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			bifrostReq := req.ToBifrostResponsesRequest(ctx)
			if bifrostReq.Params == nil || len(bifrostReq.Params.Tools) != 1 {
				t.Fatalf("expected one responses tool, got %+v", bifrostReq.Params)
			}
			if bifrostReq.Params.Tools[0].CacheControl == nil {
				t.Fatalf("cache_control dropped on ingress: %+v", bifrostReq.Params.Tools[0])
			}
			out, err := ToAnthropicResponsesRequest(ctx, bifrostReq)
			if err != nil {
				t.Fatalf("convert failed: %v", err)
			}
			if len(out.Tools) != 1 {
				t.Fatalf("expected one anthropic tool, got %d", len(out.Tools))
			}
			data, err := sonic.Marshal(out.Tools[0])
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}
			if !strings.Contains(string(data), cc) {
				t.Fatalf("cache_control dropped on egress: %s", data)
			}
		})
	}
}
