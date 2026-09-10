package anthropic

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// anthropicUnsupportedSchemaKeywords lists JSON Schema validation keywords
// that Anthropic's tool input_schema (and structured-output schema) rejects
// outright, wherever they appear in the schema tree.
//
// See: https://platform.claude.com/docs/en/build-with-claude/structured-outputs#json-schema-limitations
var anthropicUnsupportedSchemaKeywords = []string{
	"minItems", "maxItems",
	"minLength", "maxLength",
	"minimum", "maximum",
	"exclusiveMinimum", "exclusiveMaximum",
}

// anthropicConstraintDescriptionTemplates renders a removed constraint's value
// into a human-readable note appended to the affected schema node's
// description, mirroring the transformation Anthropic's own SDKs perform.
var anthropicConstraintDescriptionTemplates = map[string]string{
	"minItems":         "minimum number of items: %v",
	"maxItems":         "maximum number of items: %v",
	"minLength":        "minimum length: %v",
	"maxLength":        "maximum length: %v",
	"minimum":          "minimum value: %v",
	"maximum":          "maximum value: %v",
	"exclusiveMinimum": "exclusive minimum value: %v",
	"exclusiveMaximum": "exclusive maximum value: %v",
}

// constraintEntry captures a single removed JSON Schema keyword and its value,
// in removal order, so the generated description note is deterministic.
type constraintEntry struct {
	keyword string
	value   interface{}
}

// buildConstraintNote renders removed constraints into a single description
// suffix, e.g. "Note: maximum number of items: 5, minimum length: 1."
func buildConstraintNote(entries []constraintEntry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		tmpl := anthropicConstraintDescriptionTemplates[e.keyword]
		parts = append(parts, fmt.Sprintf(tmpl, e.value))
	}
	return "Note: " + strings.Join(parts, ", ") + "."
}

// appendDescriptionNote appends note to an existing description, separating
// with a space if a description was already present.
func appendDescriptionNote(existing string, note string) string {
	if existing == "" {
		return note
	}
	return existing + " " + note
}

// asSchemaNode coerces a raw JSON-Schema-node value (decoded either as
// *schemas.OrderedMap via the ordered-key JSON path, or as a plain
// map[string]interface{} when constructed programmatically) into an
// *schemas.OrderedMap that can be mutated in place. Returns nil for any other
// (non-object) schema shape, e.g. `true`/`false` schemas.
func asSchemaNode(v interface{}) *schemas.OrderedMap {
	switch t := v.(type) {
	case *schemas.OrderedMap:
		return t
	case map[string]interface{}:
		return schemas.OrderedMapFromMap(t)
	default:
		return nil
	}
}

// sanitizeSchemaNode recursively strips anthropicUnsupportedSchemaKeywords
// from a raw JSON Schema node (an object schema decoded into an OrderedMap)
// and from every nested node reachable via "properties", "items", "$defs",
// "definitions", "anyOf", "oneOf", and "allOf". Removed constraints are folded
// into the node's own "description" key so the model still sees the intent.
//
// Mutates om in place and returns it for convenience; safe to call with nil.
func sanitizeSchemaNode(om *schemas.OrderedMap) *schemas.OrderedMap {
	if om == nil {
		return nil
	}

	var entries []constraintEntry
	for _, kw := range anthropicUnsupportedSchemaKeywords {
		if val, ok := om.Get(kw); ok {
			entries = append(entries, constraintEntry{keyword: kw, value: val})
			om.Delete(kw)
		}
	}
	if len(entries) > 0 {
		existing := ""
		if v, ok := om.Get("description"); ok {
			if s, ok2 := v.(string); ok2 {
				existing = s
			}
		}
		om.Set("description", appendDescriptionNote(existing, buildConstraintNote(entries)))
	}

	if propsVal, ok := om.Get("properties"); ok {
		if props := asSchemaNode(propsVal); props != nil {
			sanitizeNamedNodeMap(props)
			om.Set("properties", props)
		}
	}
	if itemsVal, ok := om.Get("items"); ok {
		if node := asSchemaNode(itemsVal); node != nil {
			om.Set("items", sanitizeSchemaNode(node))
		}
	}
	for _, key := range []string{"$defs", "definitions"} {
		if val, ok := om.Get(key); ok {
			if defs := asSchemaNode(val); defs != nil {
				sanitizeNamedNodeMap(defs)
				om.Set(key, defs)
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if val, ok := om.Get(key); ok {
			if arr, ok := val.([]interface{}); ok {
				for i, item := range arr {
					if node := asSchemaNode(item); node != nil {
						arr[i] = sanitizeSchemaNode(node)
					}
				}
				om.Set(key, arr)
			}
		}
	}
	// additionalProperties may itself be a schema (not just a bool) per JSON
	// Schema — e.g. {"additionalProperties": {"type": "string", "maxLength": 32}}
	// constrains any extra properties. Sanitize it like any other nested node;
	// a bool value falls through asSchemaNode as nil and is left untouched.
	if addlVal, ok := om.Get("additionalProperties"); ok {
		if node := asSchemaNode(addlVal); node != nil {
			om.Set("additionalProperties", sanitizeSchemaNode(node))
		}
	}

	return om
}

// sanitizeNamedNodeMap sanitizes every value in a name->schema-node map
// (used for "properties" and "$defs"/"definitions" containers) in place.
func sanitizeNamedNodeMap(named *schemas.OrderedMap) {
	if named == nil {
		return
	}
	for _, k := range named.Keys() {
		v, ok := named.Get(k)
		if !ok {
			continue
		}
		if node := asSchemaNode(v); node != nil {
			named.Set(k, sanitizeSchemaNode(node))
		}
	}
}

// SanitizeToolSchemaForAnthropic returns a copy of params with every JSON
// Schema keyword unsupported by Anthropic's tool input_schema removed,
// recursively, from the top-level schema and from every nested
// property/items/defs/anyOf/oneOf/allOf schema. Removed numeric/string/array
// constraints are folded into the affected node's description so the model
// still sees the intent as guidance, mirroring the transformation Anthropic's
// own SDKs perform for structured outputs.
//
// See: https://platform.claude.com/docs/en/build-with-claude/structured-outputs#json-schema-limitations
//
// params is expected to already be an owned copy (e.g. via
// schemas.DeepCopyToolFunctionParameters) — nested OrderedMap trees are
// mutated in place as they are sanitized.
func SanitizeToolSchemaForAnthropic(params *schemas.ToolFunctionParameters) *schemas.ToolFunctionParameters {
	if params == nil {
		return nil
	}
	out := *params

	var entries []constraintEntry
	if out.MinItems != nil {
		entries = append(entries, constraintEntry{"minItems", *out.MinItems})
		out.MinItems = nil
	}
	if out.MaxItems != nil {
		entries = append(entries, constraintEntry{"maxItems", *out.MaxItems})
		out.MaxItems = nil
	}
	if out.MinLength != nil {
		entries = append(entries, constraintEntry{"minLength", *out.MinLength})
		out.MinLength = nil
	}
	if out.MaxLength != nil {
		entries = append(entries, constraintEntry{"maxLength", *out.MaxLength})
		out.MaxLength = nil
	}
	if out.Minimum != nil {
		entries = append(entries, constraintEntry{"minimum", *out.Minimum})
		out.Minimum = nil
	}
	if out.Maximum != nil {
		entries = append(entries, constraintEntry{"maximum", *out.Maximum})
		out.Maximum = nil
	}
	if out.ExclusiveMinimum != nil {
		entries = append(entries, constraintEntry{"exclusiveMinimum", *out.ExclusiveMinimum})
		out.ExclusiveMinimum = nil
	}
	if out.ExclusiveMaximum != nil {
		entries = append(entries, constraintEntry{"exclusiveMaximum", *out.ExclusiveMaximum})
		out.ExclusiveMaximum = nil
	}
	if len(entries) > 0 {
		existing := ""
		if out.Description != nil {
			existing = *out.Description
		}
		combined := appendDescriptionNote(existing, buildConstraintNote(entries))
		out.Description = &combined
	}

	if out.Properties != nil {
		sanitizeNamedNodeMap(out.Properties)
	}
	out.Items = sanitizeSchemaNode(out.Items)
	if out.Defs != nil {
		sanitizeNamedNodeMap(out.Defs)
	}
	if out.Definitions != nil {
		sanitizeNamedNodeMap(out.Definitions)
	}
	for i := range out.AnyOf {
		sanitizeSchemaNode(&out.AnyOf[i])
	}
	for i := range out.OneOf {
		sanitizeSchemaNode(&out.OneOf[i])
	}
	for i := range out.AllOf {
		sanitizeSchemaNode(&out.AllOf[i])
	}
	// additionalProperties may itself carry a schema (AdditionalPropertiesMap)
	// constraining extra properties — sanitize it like any other nested node.
	if out.AdditionalProperties != nil && out.AdditionalProperties.AdditionalPropertiesMap != nil {
		sanitizeSchemaNode(out.AdditionalProperties.AdditionalPropertiesMap)
	}

	return &out
}
