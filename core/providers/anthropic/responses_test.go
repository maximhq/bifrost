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
