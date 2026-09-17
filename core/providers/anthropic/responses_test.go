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
