package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestSanitizeToolSchemaForAnthropic_Nil(t *testing.T) {
	if got := SanitizeToolSchemaForAnthropic(nil); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

// TestSanitizeToolSchemaForAnthropic_TopLevel covers the description-note
// synthesis behavior for the 8 top-level constraint fields: stripped +
// fresh note, stripped + appended to an existing description, and a no-op
// pass-through when nothing needs stripping.
func TestSanitizeToolSchemaForAnthropic_TopLevel(t *testing.T) {
	t.Run("strips all constraints and synthesizes a description", func(t *testing.T) {
		minItems, maxItems := int64(1), int64(5)
		minLength, maxLength := int64(2), int64(50)
		minimum, maximum := 0.0, 100.0
		exclusiveMinimum, exclusiveMaximum := -1.0, 101.0

		out := SanitizeToolSchemaForAnthropic(&schemas.ToolFunctionParameters{
			Type: "array", MinItems: &minItems, MaxItems: &maxItems,
			MinLength: &minLength, MaxLength: &maxLength,
			Minimum: &minimum, Maximum: &maximum,
			ExclusiveMinimum: &exclusiveMinimum, ExclusiveMaximum: &exclusiveMaximum,
		})

		if out.MinItems != nil || out.MaxItems != nil || out.MinLength != nil || out.MaxLength != nil ||
			out.Minimum != nil || out.Maximum != nil || out.ExclusiveMinimum != nil || out.ExclusiveMaximum != nil {
			t.Fatalf("expected all 8 constraint fields to be stripped, got %+v", out)
		}
		const expected = "Note: minimum number of items: 1, maximum number of items: 5, minimum length: 2, maximum length: 50, minimum value: 0, maximum value: 100, exclusive minimum value: -1, exclusive maximum value: 101."
		if out.Description == nil || *out.Description != expected {
			t.Errorf("description mismatch:\n got:  %v\nwant: %q", out.Description, expected)
		}
	})

	t.Run("appends note to an existing description", func(t *testing.T) {
		maxItems := int64(3)
		desc := "A list of tags."
		out := SanitizeToolSchemaForAnthropic(&schemas.ToolFunctionParameters{
			Type: "array", Description: &desc, MaxItems: &maxItems,
		})
		const expected = "A list of tags. Note: maximum number of items: 3."
		if out.Description == nil || *out.Description != expected {
			t.Errorf("description mismatch: got %v, want %q", out.Description, expected)
		}
	})

	t.Run("leaves a clean schema's description untouched", func(t *testing.T) {
		desc := "A plain string field."
		out := SanitizeToolSchemaForAnthropic(&schemas.ToolFunctionParameters{Type: "string", Description: &desc})
		if out.Description == nil || *out.Description != desc {
			t.Errorf("expected description unchanged, got %v", out.Description)
		}
	})
}

// TestSanitizeToolSchemaForAnthropic_NestedContainers reproduces the exact
// failure in https://github.com/maximhq/bifrost/issues/4691 (an OpenAI-style
// array property carrying `maxItems`, forwarded to Anthropic, 400s with
// "tools.1.custom: For 'array' type, property 'maxItems' is not supported")
// and exercises every kind of nested container the recursive sanitizer must
// walk in one representative schema: properties, items, $defs,
// anyOf/oneOf/allOf, and schema-valued vs. bool-valued additionalProperties.
func TestSanitizeToolSchemaForAnthropic_NestedContainers(t *testing.T) {
	params := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			// issue #4691: array property with min/maxItems + nested items schema.
			schemas.KV("tags", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "array"),
				schemas.KV("minItems", int64(1)),
				schemas.KV("maxItems", int64(5)),
				schemas.KV("items", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
			)),
			// exclusiveMinimum/Maximum here are nested inside a property's raw
			// schema node (an *OrderedMap), not the top-level typed struct
			// fields covered by TestSanitizeToolSchemaForAnthropic_TopLevel —
			// property-level schemas are never typed, only the top-level
			// ToolFunctionParameters struct is, so the recursive node
			// sanitizer must catch these independently of the top-level fields.
			schemas.KV("score", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "number"),
				schemas.KV("exclusiveMinimum", 0.0),
				schemas.KV("exclusiveMaximum", 1.0),
			)),
			// additionalProperties may itself be a schema (not a bool).
			schemas.KV("metadata", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "object"),
				schemas.KV("additionalProperties", schemas.NewOrderedMapFromPairs(
					schemas.KV("type", "number"),
					schemas.KV("maximum", 10.0),
				)),
			)),
		),
		// $defs / Pydantic-Zod-style refs.
		Defs: schemas.NewOrderedMapFromPairs(
			schemas.KV("Tag", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "string"),
				schemas.KV("maxLength", int64(32)),
			)),
		),
		// Union schemas.
		AnyOf: []schemas.OrderedMap{
			*schemas.NewOrderedMapFromPairs(schemas.KV("type", "array"), schemas.KV("minItems", int64(1))),
			*schemas.NewOrderedMapFromPairs(schemas.KV("type", "null")),
		},
		// Top-level additionalProperties, bool-valued (common case): must be left alone.
		AdditionalProperties: &schemas.AdditionalPropertiesStruct{AdditionalPropertiesBool: schemas.Ptr(false)},
	}

	out := SanitizeToolSchemaForAnthropic(params)

	get := func(om *schemas.OrderedMap, key string) *schemas.OrderedMap {
		v, _ := om.Get(key)
		node, _ := v.(*schemas.OrderedMap)
		return node
	}

	tags := get(out.Properties, "tags")
	if _, ok := tags.Get("minItems"); ok {
		t.Error("expected 'minItems' stripped from nested array property")
	}
	if _, ok := tags.Get("maxItems"); ok {
		t.Error("expected 'maxItems' stripped from nested array property")
	}
	if items := get(tags, "items"); items == nil {
		t.Error("expected nested 'items' schema to survive")
	} else if typ, _ := items.Get("type"); typ != "string" {
		t.Errorf("expected items.type to remain 'string', got %v", typ)
	}

	score := get(out.Properties, "score")
	if _, ok := score.Get("exclusiveMinimum"); ok {
		t.Error("expected 'exclusiveMinimum' stripped from nested property")
	}
	if _, ok := score.Get("exclusiveMaximum"); ok {
		t.Error("expected 'exclusiveMaximum' stripped from nested property")
	}

	if nestedAddl := get(get(out.Properties, "metadata"), "additionalProperties"); nestedAddl == nil {
		t.Error("expected nested schema-valued additionalProperties to survive")
	} else if _, ok := nestedAddl.Get("maximum"); ok {
		t.Error("expected 'maximum' stripped from nested additionalProperties schema")
	}

	if tag := get(out.Defs, "Tag"); tag == nil {
		t.Error("expected $defs entry to survive")
	} else if _, ok := tag.Get("maxLength"); ok {
		t.Error("expected 'maxLength' stripped from $defs entry")
	}

	if _, ok := out.AnyOf[0].Get("minItems"); ok {
		t.Error("expected 'minItems' stripped from anyOf[0]")
	}
	if typ, _ := out.AnyOf[1].Get("type"); typ != "null" {
		t.Errorf("expected anyOf[1] left untouched, got %v", typ)
	}

	if out.AdditionalProperties.AdditionalPropertiesBool == nil || *out.AdditionalProperties.AdditionalPropertiesBool != false {
		t.Error("expected bool-valued top-level additionalProperties left untouched")
	}
}

// TestSanitizeToolSchemaForAnthropic_ReturnsDistinctTopLevelCopy verifies the
// top-level struct returned is a distinct copy, not an alias — so scalar
// fields on the caller's original (e.g. Description) are never overwritten in
// place. This does NOT cover nested OrderedMap trees (Properties, Items,
// etc.): those are intentionally mutated in place, by design (see
// SanitizeToolSchemaForAnthropic's doc comment) — callers must pass an owned
// copy (e.g. via schemas.DeepCopyToolFunctionParameters) if they need the
// original nested data preserved, which is what both chat.go and responses.go
// do; that specific invariant is covered by
// TestConvertResponsesFunctionToolToAnthropic_DeepCopiesBeforeSanitizing.
func TestSanitizeToolSchemaForAnthropic_ReturnsDistinctTopLevelCopy(t *testing.T) {
	desc := "unchanged"
	params := &schemas.ToolFunctionParameters{Type: "string", Description: &desc}

	out := SanitizeToolSchemaForAnthropic(params)
	if out == params {
		t.Fatal("expected a distinct copy, not the same pointer")
	}
	if params.Description == nil || *params.Description != "unchanged" {
		t.Fatal("expected caller's original Description to be left untouched")
	}
}

// TestConvertFunctionToolToAnthropic is an end-to-end regression test for
// issue #4691, gated on `strict`: Anthropic only rejects these keywords under
// strict (grammar-constrained) tool-input validation — verified live, a
// non-strict call with `maxItems` is accepted by the real API unchanged — so
// the sanitizer must strip for strict tools and leave non-strict ones alone.
func TestConvertFunctionToolToAnthropic(t *testing.T) {
	buildTool := func(strict *bool) schemas.ChatTool {
		return schemas.ChatTool{
			Type: schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{
				Name:   "list_files",
				Strict: strict,
				Parameters: &schemas.ToolFunctionParameters{
					Type: "object",
					Properties: schemas.NewOrderedMapFromPairs(
						schemas.KV("paths", schemas.NewOrderedMapFromPairs(
							schemas.KV("type", "array"),
							schemas.KV("maxItems", int64(5)),
							schemas.KV("items", schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))),
						)),
					),
				},
			},
		}
	}

	t.Run("strict tool: maxItems stripped from struct and wire bytes", func(t *testing.T) {
		anthropicTool := convertFunctionToolToAnthropic(buildTool(schemas.Ptr(true)))

		pathsVal, _ := anthropicTool.InputSchema.Properties.Get("paths")
		paths := pathsVal.(*schemas.OrderedMap)
		if _, ok := paths.Get("maxItems"); ok {
			t.Fatal("expected 'maxItems' stripped from the converted Anthropic tool schema")
		}

		// Also assert at the wire level: Anthropic never sees the Go struct,
		// only the serialized JSON bytes actually sent over HTTP.
		raw, err := json.Marshal(anthropicTool)
		if err != nil {
			t.Fatalf("failed to marshal AnthropicTool: %v", err)
		}
		if strings.Contains(string(raw), `"maxItems"`) {
			t.Errorf("wire payload must not contain \"maxItems\", but it does:\n%s", raw)
		}
	})

	t.Run("non-strict tool: schema left untouched", func(t *testing.T) {
		anthropicTool := convertFunctionToolToAnthropic(buildTool(nil))

		pathsVal, _ := anthropicTool.InputSchema.Properties.Get("paths")
		paths := pathsVal.(*schemas.OrderedMap)
		if _, ok := paths.Get("maxItems"); !ok {
			t.Fatal("expected 'maxItems' preserved for a non-strict tool schema")
		}
	})
}

// TestConvertResponsesFunctionToolToAnthropic_DeepCopiesBeforeSanitizing
// guards the Responses-API tool path: since SanitizeToolSchemaForAnthropic
// mutates OrderedMap trees in place, the conversion must deep-copy the
// caller-owned schemas.ResponsesToolFunction.Parameters before sanitizing it,
// otherwise a caller that reuses the same *ToolFunctionParameters across
// providers would see it silently stripped for non-Anthropic use too.
func TestConvertResponsesFunctionToolToAnthropic_DeepCopiesBeforeSanitizing(t *testing.T) {
	maxItems := int64(5)
	originalParams := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("tags", schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "array"),
				schemas.KV("maxItems", maxItems),
			)),
		),
	}

	name := "list_tags"
	tool := schemas.ResponsesTool{
		Type: schemas.ResponsesToolTypeFunction,
		Name: &name,
		ResponsesToolFunction: &schemas.ResponsesToolFunction{
			Parameters: originalParams,
			Strict:     schemas.Ptr(true),
		},
	}

	anthropicTool := convertBifrostToolToAnthropic("claude-sonnet-4-20250514", &tool, schemas.Anthropic, false)
	if anthropicTool == nil {
		t.Fatal("expected a non-nil AnthropicTool")
	}

	convertedTags, _ := anthropicTool.InputSchema.Properties.Get("tags")
	if _, ok := convertedTags.(*schemas.OrderedMap).Get("maxItems"); ok {
		t.Fatal("expected 'maxItems' to be stripped from the converted schema")
	}

	// The caller's original schema must be untouched.
	originalTags, _ := originalParams.Properties.Get("tags")
	if _, ok := originalTags.(*schemas.OrderedMap).Get("maxItems"); !ok {
		t.Fatal("expected caller-owned original Parameters to remain unmodified (no deep copy before sanitize)")
	}
}
