package bedrock

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

func TestConvertTextFormatToTool_BooleanSchemaTrue(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)

	tool, _, err := convertTextFormatToTool(ctx, "anthropic.claude-sonnet-4-20250514-v1:0", booleanSchemaTextConfig(&schemas.JSONSchemaOrBool{SchemaBool: schemas.Ptr(true)}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tool == nil || tool.ToolSpec == nil {
		t.Fatal("expected tool with tool spec")
	}
	if got := string(tool.ToolSpec.InputSchema.JSON); got != `{"type":"object"}` {
		t.Fatalf("expected unconstrained object schema for boolean true, got %s", got)
	}
}

func TestConvertTextFormatToTool_BooleanSchemaFalse(t *testing.T) {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)

	tool, _, err := convertTextFormatToTool(ctx, "anthropic.claude-sonnet-4-20250514-v1:0", booleanSchemaTextConfig(&schemas.JSONSchemaOrBool{SchemaBool: schemas.Ptr(false)}))
	if !errors.Is(err, schemas.ErrUnsatisfiableSchema) {
		t.Fatalf("expected ErrUnsatisfiableSchema, got %v", err)
	}
	if tool != nil {
		t.Fatal("expected no tool for boolean false schema")
	}
}
