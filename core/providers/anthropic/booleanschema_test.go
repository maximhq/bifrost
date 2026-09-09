package anthropic

import (
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func booleanSchemaTextConfig(schema *schemas.JSONSchemaOrBool) *schemas.ResponsesTextConfig {
	return &schemas.ResponsesTextConfig{
		Format: &schemas.ResponsesTextConfigFormat{
			Type: "json_schema",
			Name: schemas.Ptr("test"),
			JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
				Schema: schema,
			},
		},
	}
}

func TestConvertResponsesTextFormatToTool_CompositeObjectSchema(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	composite := schemas.NewOrderedMapFromPairs(
		schemas.KV("type", "object"),
		schemas.KV("properties", schemas.NewOrderedMapFromPairs(
			schemas.KV("zip", map[string]any{"type": "string"}),
		)),
	)

	tool, err := convertResponsesTextFormatToTool(ctx, booleanSchemaTextConfig(&schemas.JSONSchemaOrBool{SchemaMap: composite}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tool == nil || tool.InputSchema == nil {
		t.Fatal("expected tool with input schema")
	}
	if tool.InputSchema.Type != "object" {
		t.Fatalf("expected type object, got %q", tool.InputSchema.Type)
	}
	if tool.InputSchema.Properties == nil {
		t.Fatal("composite schema properties dropped: tool built from default schema instead of composite Schema field")
	}
	if _, ok := tool.InputSchema.Properties.Get("zip"); !ok {
		t.Fatal("expected composite property 'zip' in tool input schema")
	}
}

func TestConvertResponsesTextFormatToTool_BooleanSchemaTrue(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)

	tool, err := convertResponsesTextFormatToTool(ctx, booleanSchemaTextConfig(&schemas.JSONSchemaOrBool{SchemaBool: schemas.Ptr(true)}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tool == nil || tool.InputSchema == nil {
		t.Fatal("expected tool with input schema")
	}
	if tool.InputSchema.Type != "object" {
		t.Fatalf("expected unconstrained object schema for boolean true, got type %q", tool.InputSchema.Type)
	}
	if tool.InputSchema.Properties != nil {
		t.Fatal("expected no properties for boolean true schema")
	}
}

func TestConvertResponsesTextFormatToTool_BooleanSchemaFalse(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)

	tool, err := convertResponsesTextFormatToTool(ctx, booleanSchemaTextConfig(&schemas.JSONSchemaOrBool{SchemaBool: schemas.Ptr(false)}))
	if !errors.Is(err, schemas.ErrUnsatisfiableSchema) {
		t.Fatalf("expected ErrUnsatisfiableSchema, got %v", err)
	}
	if tool != nil {
		t.Fatal("expected no tool for boolean false schema")
	}
}
