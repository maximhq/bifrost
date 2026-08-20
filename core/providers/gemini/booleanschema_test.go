package gemini

import (
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestReconstructSchemaFromJSONSchema_BooleanSchemaTrue(t *testing.T) {
	js := &schemas.ResponsesTextConfigFormatJSONSchema{
		Schema: &schemas.JSONSchemaOrBool{SchemaBool: schemas.Ptr(true)},
	}

	schema, err := reconstructSchemaFromJSONSchema(js)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	schemaMap, ok := schema.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map schema for boolean true, got %T", schema)
	}
	if schemaMap["type"] != "object" {
		t.Fatalf("expected unconstrained object schema for boolean true, got %v", schemaMap)
	}
}

func TestReconstructSchemaFromJSONSchema_BooleanSchemaFalse(t *testing.T) {
	js := &schemas.ResponsesTextConfigFormatJSONSchema{
		Schema: &schemas.JSONSchemaOrBool{SchemaBool: schemas.Ptr(false)},
	}

	schema, err := reconstructSchemaFromJSONSchema(js)
	if !errors.Is(err, schemas.ErrUnsatisfiableSchema) {
		t.Fatalf("expected ErrUnsatisfiableSchema, got %v", err)
	}
	if schema != nil {
		t.Fatalf("expected no schema for boolean false, got %v", schema)
	}
}

func TestReconstructSchemaFromJSONSchema_CompositeObjectSchema(t *testing.T) {
	composite := schemas.NewOrderedMapFromPairs(
		schemas.KV("type", "object"),
		schemas.KV("properties", schemas.NewOrderedMapFromPairs(
			schemas.KV("zip", map[string]any{"type": "string"}),
		)),
	)
	js := &schemas.ResponsesTextConfigFormatJSONSchema{
		Schema: &schemas.JSONSchemaOrBool{SchemaMap: composite},
	}

	schema, err := reconstructSchemaFromJSONSchema(js)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if schema == nil {
		t.Fatal("expected composite schema to be used, got nil")
	}
}
