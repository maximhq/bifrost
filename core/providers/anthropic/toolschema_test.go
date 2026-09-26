package anthropic

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// codexUnionToolSchema returns a reduced version of Codex's automation_update
// schema: its root alternatives are references, while nullable fields retain a
// nested anyOf that Anthropic accepts.
func codexUnionToolSchema(t *testing.T) *schemas.ToolFunctionParameters {
	t.Helper()
	var schema schemas.ToolFunctionParameters
	raw := []byte(`{
		"type":"object",
		"properties":{},
		"$defs":{
			"view":{"type":"object","properties":{"mode":{"type":"string","enum":["view"]},"id":{"type":"string"}},"required":["mode","id"],"additionalProperties":false},
			"create":{"type":"object","properties":{"mode":{"type":"string","enum":["create"]},"name":{"type":"string"},"notificationPolicy":{"anyOf":[{"type":"string"},{"type":"null"}]}},"required":["mode","name"],"additionalProperties":false},
			"createGroup":{"oneOf":[{"$ref":"#/$defs/create"}]}
		},
		"oneOf":[{"$ref":"#/$defs/view"},{"$ref":"#/$defs/createGroup"}]
	}`)
	if err := schemas.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal test schema: %v", err)
	}
	return &schema
}

// TestNormalizeAnthropicToolInputSchemaOtherRootCompositions verifies the two
// other composition keywords named by Anthropic's validation error.
func TestNormalizeAnthropicToolInputSchemaOtherRootCompositions(t *testing.T) {
	for _, test := range []struct {
		name         string
		raw          string
		wantRequired []string
	}{
		{
			name:         "anyOf intersects required fields",
			raw:          `{"type":"object","anyOf":[{"type":"object","properties":{"mode":{"type":"string"},"left":{"type":"string"}},"required":["mode","left"]},{"type":"object","properties":{"mode":{"type":"string"},"right":{"type":"string"}},"required":["mode","right"]}]}`,
			wantRequired: []string{"mode"},
		},
		{
			name:         "allOf unions required fields",
			raw:          `{"type":"object","allOf":[{"type":"object","properties":{"left":{"type":"string"}},"required":["left"]},{"type":"object","properties":{"right":{"type":"string"}},"required":["right"]}]}`,
			wantRequired: []string{"left", "right"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var schema schemas.ToolFunctionParameters
			if err := schemas.Unmarshal([]byte(test.raw), &schema); err != nil {
				t.Fatalf("unmarshal schema: %v", err)
			}
			normalized, err := normalizeAnthropicToolInputSchema(&schema)
			if err != nil {
				t.Fatalf("normalize schema: %v", err)
			}
			if len(normalized.OneOf) != 0 || len(normalized.AnyOf) != 0 || len(normalized.AllOf) != 0 {
				t.Fatal("expected root composition to be removed")
			}
			if !equalStringSlices(normalized.Required, test.wantRequired) {
				t.Fatalf("required = %v, want %v", normalized.Required, test.wantRequired)
			}
		})
	}
}

// TestNormalizeAnthropicToolInputSchemaRootOneOf verifies that root
// alternatives are merged without removing accepted nested composition.
func TestNormalizeAnthropicToolInputSchemaRootOneOf(t *testing.T) {
	original := codexUnionToolSchema(t)
	normalized, err := normalizeAnthropicToolInputSchema(original)
	if err != nil {
		t.Fatalf("normalize schema: %v", err)
	}

	if len(normalized.OneOf) != 0 || len(normalized.AnyOf) != 0 || len(normalized.AllOf) != 0 {
		t.Fatal("expected all root composition keywords to be removed")
	}
	if normalized.Type != "object" {
		t.Fatalf("expected object root, got %q", normalized.Type)
	}
	if got := normalized.Properties.Keys(); !equalStringSlices(got, []string{"mode", "id", "name", "notificationPolicy"}) {
		t.Fatalf("unexpected merged property order: %v", got)
	}
	if got := normalized.Required; !equalStringSlices(got, []string{"mode"}) {
		t.Fatalf("expected common required fields, got %v", got)
	}
	if normalized.AdditionalProperties == nil || normalized.AdditionalProperties.AdditionalPropertiesBool == nil || *normalized.AdditionalProperties.AdditionalPropertiesBool {
		t.Fatal("expected merged schema to retain additionalProperties:false")
	}
	mode, _ := normalized.Properties.Get("mode")
	modeSchema := anthropicOrderedMap(mode)
	if modeSchema == nil {
		t.Fatal("expected merged mode schema")
	}
	if _, ok := modeSchema.Get("anyOf"); !ok {
		t.Fatal("expected conflicting mode definitions to become a nested anyOf")
	}
	notification, _ := normalized.Properties.Get("notificationPolicy")
	notificationSchema := anthropicOrderedMap(notification)
	if notificationSchema == nil {
		t.Fatal("expected notificationPolicy schema")
	}
	if _, ok := notificationSchema.Get("anyOf"); !ok {
		t.Fatal("expected the existing nested anyOf to survive")
	}

	if len(original.OneOf) != 2 || original.Properties.Len() != 0 {
		t.Fatal("normalization mutated the caller-owned schema")
	}
}

// TestAnthropicOpenAICompatibleToolPathsNormalizeRootOneOf verifies both API
// integrations use the same Anthropic compatibility transform.
func TestAnthropicOpenAICompatibleToolPathsNormalizeRootOneOf(t *testing.T) {
	t.Run("chat completions", func(t *testing.T) {
		schema := codexUnionToolSchema(t)
		req := &schemas.BifrostChatRequest{
			Provider: schemas.Anthropic,
			Model:    "claude-fable-5",
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
			}},
			Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:       "automation_update",
					Parameters: schema,
				},
			}}},
		}
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		defer cancel()
		converted, err := ToAnthropicChatRequest(ctx, req)
		if err != nil {
			t.Fatalf("convert chat request: %v", err)
		}
		assertAnthropicRootCompositionRemoved(t, converted.Tools[0].InputSchema)
	})

	t.Run("responses", func(t *testing.T) {
		schema := codexUnionToolSchema(t)
		name := "automation_update"
		tool := &schemas.ResponsesTool{
			Type: schemas.ResponsesToolTypeFunction,
			Name: &name,
			ResponsesToolFunction: &schemas.ResponsesToolFunction{
				Parameters: schema,
			},
		}
		convertedTools, _, err := convertBifrostToolsToAnthropic(
			schemas.ResolveModelCaps(schemas.Anthropic, "claude-fable-5"),
			[]schemas.ResponsesTool{*tool},
			schemas.Anthropic,
		)
		if err != nil {
			t.Fatalf("convert responses tools: %v", err)
		}
		if len(convertedTools) != 1 {
			t.Fatal("expected converted tool")
		}
		assertAnthropicRootCompositionRemoved(t, convertedTools[0].InputSchema)
	})
}

func TestNormalizeAnthropicToolInputSchemaPreservesAllOfPropertyConjunction(t *testing.T) {
	var schema schemas.ToolFunctionParameters
	if err := schemas.Unmarshal([]byte(`{
		"type":"object",
		"allOf":[
			{"type":"object","properties":{"name":{"type":"string"}}},
			{"type":"object","properties":{"name":{"maxLength":5}}}
		]
	}`), &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}

	normalized, err := normalizeAnthropicToolInputSchema(&schema)
	if err != nil {
		t.Fatalf("normalize schema: %v", err)
	}
	assertAnthropicPropertyComposition(t, normalized.Normalized(), "name", "allOf", 2)

	chatTool, err := convertFunctionToolToAnthropic(schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:       "constrained_name",
			Parameters: &schema,
		},
	})
	if err != nil {
		t.Fatalf("convert chat tool: %v", err)
	}
	assertAnthropicPropertyComposition(t, chatTool.InputSchema, "name", "allOf", 2)

	name := "constrained_name"
	responseTools, _, err := convertBifrostToolsToAnthropic(
		schemas.ResolveModelCaps(schemas.Anthropic, "claude-fable-5"),
		[]schemas.ResponsesTool{{
			Type:                  schemas.ResponsesToolTypeFunction,
			Name:                  &name,
			ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: &schema},
		}},
		schemas.Anthropic,
	)
	if err != nil {
		t.Fatalf("convert responses tool: %v", err)
	}
	assertAnthropicPropertyComposition(t, responseTools[0].InputSchema, "name", "allOf", 2)
}

func TestNormalizeAnthropicToolInputSchemaRejectsExcessiveComplexity(t *testing.T) {
	t.Run("branch count", func(t *testing.T) {
		_, err := normalizeAnthropicToolInputSchema(explosiveAnthropicToolSchema(t))
		if err == nil || !strings.Contains(err.Error(), "branch count") {
			t.Fatalf("expected branch-count error, got %v", err)
		}
	})

	t.Run("recursion depth", func(t *testing.T) {
		branch := `{"type":"object","properties":{}}`
		for range maxAnthropicSchemaExpansionDepth + 1 {
			branch = `{"anyOf":[` + branch + `]}`
		}
		var schema schemas.ToolFunctionParameters
		if err := schemas.Unmarshal([]byte(`{"type":"object","oneOf":[`+branch+`]}`), &schema); err != nil {
			t.Fatalf("unmarshal deep schema: %v", err)
		}
		_, err := normalizeAnthropicToolInputSchema(&schema)
		if err == nil || !strings.Contains(err.Error(), "depth") {
			t.Fatalf("expected depth error, got %v", err)
		}
	})
}

func TestAnthropicSchemaComplexityErrorsAreBadRequests(t *testing.T) {
	schema := explosiveAnthropicToolSchema(t)
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	chatRequest := &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-fable-5",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
		Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{{
			Type:     schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{Name: "complex", Parameters: schema},
		}}},
	}
	_, chatErr := BuildAnthropicChatRequestBody(ctx, chatRequest, AnthropicRequestBuildConfig{Provider: schemas.Anthropic})
	assertAnthropicBadRequest(t, chatErr)

	name := "complex"
	responsesRequest := &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-fable-5",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
		Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{
			Type:                  schemas.ResponsesToolTypeFunction,
			Name:                  &name,
			ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: schema},
		}}},
	}
	_, responsesErr := BuildAnthropicResponsesRequestBody(ctx, responsesRequest, AnthropicRequestBuildConfig{Provider: schemas.Anthropic})
	assertAnthropicBadRequest(t, responsesErr)
}

func explosiveAnthropicToolSchema(t *testing.T) *schemas.ToolFunctionParameters {
	t.Helper()
	members := make([]string, 7) // 2^7 combinations exceed the 64-branch limit.
	for i := range members {
		members[i] = `{"anyOf":[{"type":"object","properties":{"left":{"type":"string"}}},{"type":"object","properties":{"right":{"type":"string"}}}]}`
	}
	var schema schemas.ToolFunctionParameters
	if err := schemas.Unmarshal([]byte(`{"type":"object","allOf":[`+strings.Join(members, ",")+`]}`), &schema); err != nil {
		t.Fatalf("unmarshal explosive schema: %v", err)
	}
	return &schema
}

func assertAnthropicPropertyComposition(t *testing.T, schema *schemas.ToolFunctionParameters, property, keyword string, wantMembers int) {
	t.Helper()
	value, ok := schema.Properties.Get(property)
	if !ok {
		t.Fatalf("property %q missing", property)
	}
	propertySchema := anthropicOrderedMap(value)
	members, ok := propertySchema.Get(keyword)
	if !ok {
		t.Fatalf("property %q does not contain %s", property, keyword)
	}
	if got := len(anthropicOrderedMapSlice(members)); got != wantMembers {
		t.Fatalf("property %q %s has %d members, want %d", property, keyword, got, wantMembers)
	}
	if _, ok := propertySchema.Get("anyOf"); keyword == "allOf" && ok {
		t.Fatalf("property %q conjunction was broadened to anyOf", property)
	}
}

func assertAnthropicBadRequest(t *testing.T, err *schemas.BifrostError) {
	t.Helper()
	if err == nil || err.StatusCode == nil || *err.StatusCode != 400 {
		t.Fatalf("expected HTTP 400 error, got %+v", err)
	}
}

// assertAnthropicRootCompositionRemoved checks the provider-facing schema.
func assertAnthropicRootCompositionRemoved(t *testing.T, schema *schemas.ToolFunctionParameters) {
	t.Helper()
	if schema == nil {
		t.Fatal("expected input schema")
	}
	if len(schema.OneOf) != 0 || len(schema.AnyOf) != 0 || len(schema.AllOf) != 0 {
		t.Fatalf("root composition survived Anthropic conversion: %+v", schema)
	}
	if schema.Properties == nil || schema.Properties.Len() != 4 {
		t.Fatalf("expected four merged properties, got %v", schema.Properties)
	}
}

// equalStringSlices reports whether two strings slices have identical order.
func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
