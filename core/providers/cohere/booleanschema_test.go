package cohere

import (
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestConvertResponsesTextFormatToCohere_CompositeObjectSchema(t *testing.T) {
	composite := schemas.NewOrderedMapFromPairs(
		schemas.KV("type", "object"),
		schemas.KV("properties", schemas.NewOrderedMapFromPairs(
			schemas.KV("zip", map[string]any{"type": "string"}),
		)),
	)
	format := &schemas.ResponsesTextConfigFormat{
		Type: "json_schema",
		JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
			Schema: &schemas.JSONSchemaOrBool{SchemaMap: composite},
		},
	}

	cohereFormat, err := convertResponsesTextFormatToCohere(format)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cohereFormat == nil || cohereFormat.JSONSchema == nil {
		t.Fatal("composite schema dropped: expected json_schema to be set")
	}
	om, ok := (*cohereFormat.JSONSchema).(*schemas.OrderedMap)
	if !ok {
		t.Fatalf("expected composite OrderedMap schema, got %T", *cohereFormat.JSONSchema)
	}
	if _, ok := om.Get("properties"); !ok {
		t.Fatal("expected composite schema properties to survive")
	}
}

func TestConvertResponsesTextFormatToCohere_BooleanSchemaTrue(t *testing.T) {
	format := &schemas.ResponsesTextConfigFormat{
		Type: "json_schema",
		JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
			Schema: &schemas.JSONSchemaOrBool{SchemaBool: schemas.Ptr(true)},
		},
	}

	cohereFormat, err := convertResponsesTextFormatToCohere(format)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cohereFormat == nil {
		t.Fatal("expected response format")
	}
	if cohereFormat.Type != ResponseFormatTypeJSONObject {
		t.Fatalf("expected json_object mode for boolean true, got %v", cohereFormat.Type)
	}
	if cohereFormat.JSONSchema != nil {
		t.Fatal("expected no schema for boolean true (json_object mode is the widest representable form)")
	}
}

func TestConvertResponsesTextFormatToCohere_BooleanSchemaFalse(t *testing.T) {
	format := &schemas.ResponsesTextConfigFormat{
		Type: "json_schema",
		JSONSchema: &schemas.ResponsesTextConfigFormatJSONSchema{
			Schema: &schemas.JSONSchemaOrBool{SchemaBool: schemas.Ptr(false)},
		},
	}

	cohereFormat, err := convertResponsesTextFormatToCohere(format)
	if !errors.Is(err, schemas.ErrUnsatisfiableSchema) {
		t.Fatalf("expected ErrUnsatisfiableSchema, got %v", err)
	}
	if cohereFormat != nil {
		t.Fatal("expected no response format for boolean false schema")
	}
}
