package anthropic

import (
	"context"
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
