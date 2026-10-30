package utils

import "github.com/maximhq/bifrost/core/schemas"

// RewriteBooleanSchemaNodes replaces every boolean subschema in a tool parameter schema
// with its object form, for upstreams whose validator iterates every subschema as an
// object and fails on a boolean ("argument of type 'bool' is not iterable"). The
// rewrite is lossless: `true` and `{}` both accept every instance, and `false` and
// `{"not":{}}` both reject every instance.
//
// Only schema positions are rewritten: the values of properties, patternProperties,
// $defs, definitions and dependentSchemas; the single-schema keywords items,
// contains, propertyNames, not, if, then, else, additionalItems and contentSchema;
// and the entries of anyOf, oneOf, allOf, prefixItems and tuple-form items. A boolean
// additionalProperties, unevaluatedProperties or unevaluatedItems is that keyword's
// standard form and stays a boolean; only an object there is walked. Instance data
// (enum, const, default, examples), required, property names and unknown keywords
// are never touched.
//
// Copy-on-write like ApplyToolSchemaPolicy: key order is preserved, the caller's
// schema is never written through, and a schema with no boolean subschema is
// returned as-is without allocating.
func RewriteBooleanSchemaNodes(params *schemas.ToolFunctionParameters) (*schemas.ToolFunctionParameters, bool) {
	if params == nil {
		return params, false
	}
	var updated *schemas.ToolFunctionParameters
	edit := func() *schemas.ToolFunctionParameters {
		if updated == nil {
			cp := *params
			updated = &cp
		}
		return updated
	}

	if rewritten, changed := rewriteBooleanSchemaMembers(params.Properties); changed {
		edit().Properties = rewritten
	}
	if rewritten, changed := rewriteBooleanSchemaObject(params.Items); changed {
		edit().Items = rewritten
	}
	if rewritten, changed := rewriteBooleanSchemaMembers(params.Defs); changed {
		edit().Defs = rewritten
	}
	if rewritten, changed := rewriteBooleanSchemaMembers(params.Definitions); changed {
		edit().Definitions = rewritten
	}
	// The boolean variant of additionalProperties is carried through untouched.
	if additional := params.AdditionalProperties; additional != nil && additional.AdditionalPropertiesMap != nil {
		if rewritten, changed := rewriteBooleanSchemaObject(additional.AdditionalPropertiesMap); changed {
			additionalCopy := *additional
			additionalCopy.AdditionalPropertiesMap = rewritten
			edit().AdditionalProperties = &additionalCopy
		}
	}
	if rewritten, changed := rewriteBooleanSchemaObjects(params.AnyOf); changed {
		edit().AnyOf = rewritten
	}
	if rewritten, changed := rewriteBooleanSchemaObjects(params.OneOf); changed {
		edit().OneOf = rewritten
	}
	if rewritten, changed := rewriteBooleanSchemaObjects(params.AllOf); changed {
		edit().AllOf = rewritten
	}

	if updated == nil {
		return params, false
	}
	return updated, true
}

// RewriteChatToolBooleanSchemaNodes applies RewriteBooleanSchemaNodes to every Chat
// tool's parameter schema, with the copy-on-write guarantee of ApplyChatToolSchemaPolicy.
func RewriteChatToolBooleanSchemaNodes(tools []schemas.ChatTool) ([]schemas.ChatTool, bool) {
	return rewriteChatToolParameters(tools, RewriteBooleanSchemaNodes)
}

// RewriteResponsesToolBooleanSchemaNodes applies RewriteBooleanSchemaNodes to every
// Responses tool's parameter schema, namespace members included, with the
// copy-on-write guarantee of ApplyResponsesToolSchemaPolicy.
func RewriteResponsesToolBooleanSchemaNodes(tools []schemas.ResponsesTool) ([]schemas.ResponsesTool, bool) {
	return rewriteResponsesToolParameters(tools, RewriteBooleanSchemaNodes)
}

// booleanSchemaObject returns the object form of a boolean schema. Each call returns a
// fresh map, so no two rewritten positions share one the caller could later edit.
func booleanSchemaObject(accept bool) *schemas.OrderedMap {
	if accept {
		return schemas.NewOrderedMap()
	}
	return schemas.NewOrderedMapFromPairs(schemas.KV("not", schemas.NewOrderedMap()))
}

// rewriteBooleanSchema rewrites the value at a schema position: a boolean becomes its
// object form and an object is walked as a schema. Anything else is not a schema and
// is returned untouched. No-change paths hand back value itself, never a re-boxed copy.
func rewriteBooleanSchema(value any) (any, bool) {
	switch typed := value.(type) {
	case bool:
		return booleanSchemaObject(typed), true
	case *schemas.OrderedMap:
		if rewritten, changed := rewriteBooleanSchemaObject(typed); changed {
			return rewritten, true
		}
	case map[string]any:
		// Schemas built in code arrive as plain maps (see normalizeSchemaValue).
		var updated map[string]any
		for key, nested := range typed {
			rewritten, changed := rewriteBooleanSchemaKeyword(key, nested)
			if !changed {
				continue
			}
			if updated == nil {
				updated = make(map[string]any, len(typed))
				for k, v := range typed {
					updated[k] = v
				}
			}
			updated[key] = rewritten
		}
		if updated != nil {
			return updated, true
		}
	}
	return value, false
}

// rewriteBooleanSchemaKeyword rewrites one keyword of a schema object.
func rewriteBooleanSchemaKeyword(key string, value any) (any, bool) {
	switch key {
	case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas":
		return rewriteBooleanSchemaMemberValues(value)
	case "anyOf", "oneOf", "allOf", "prefixItems":
		if list, ok := value.([]any); ok {
			return rewriteBooleanSchemaList(value, list)
		}
	case "items":
		// Draft-07 tuple form: every entry is a schema.
		if list, ok := value.([]any); ok {
			return rewriteBooleanSchemaList(value, list)
		}
		return rewriteBooleanSchema(value)
	case "contains", "propertyNames", "not", "if", "then", "else", "additionalItems", "contentSchema":
		return rewriteBooleanSchema(value)
	case "additionalProperties", "unevaluatedProperties", "unevaluatedItems":
		// A boolean is this keyword's standard form and upstreams accept it.
		if _, ok := value.(bool); !ok {
			return rewriteBooleanSchema(value)
		}
	}
	return value, false
}

// rewriteBooleanSchemaMemberValues rewrites a name-to-schema map such as properties.
// The names are data and stay as they are; every value is a schema position.
func rewriteBooleanSchemaMemberValues(value any) (any, bool) {
	switch typed := value.(type) {
	case *schemas.OrderedMap:
		if rewritten, changed := rewriteBooleanSchemaMembers(typed); changed {
			return rewritten, true
		}
	case map[string]any:
		var updated map[string]any
		for name, nested := range typed {
			rewritten, changed := rewriteBooleanSchema(nested)
			if !changed {
				continue
			}
			if updated == nil {
				updated = make(map[string]any, len(typed))
				for k, v := range typed {
					updated[k] = v
				}
			}
			updated[name] = rewritten
		}
		if updated != nil {
			return updated, true
		}
	}
	return value, false
}

// rewriteBooleanSchemaList rewrites a list whose every entry is a schema position.
// value is list boxed by the caller and is what an unchanged list returns.
func rewriteBooleanSchemaList(value any, list []any) (any, bool) {
	var updated []any
	for i := range list {
		rewritten, changed := rewriteBooleanSchema(list[i])
		if !changed {
			continue
		}
		if updated == nil {
			updated = make([]any, len(list))
			copy(updated, list)
		}
		updated[i] = rewritten
	}
	if updated == nil {
		return value, false
	}
	return updated, true
}

// rewriteBooleanSchemaObject walks one schema object. The map is cloned only when one
// of its own keywords changes; Set on the clone keeps every key in place.
func rewriteBooleanSchemaObject(schema *schemas.OrderedMap) (*schemas.OrderedMap, bool) {
	if schema == nil || schema.Len() == 0 {
		return schema, false
	}
	var updated *schemas.OrderedMap
	schema.Range(func(key string, value any) bool {
		if rewritten, changed := rewriteBooleanSchemaKeyword(key, value); changed {
			if updated == nil {
				updated = schema.Clone()
			}
			updated.Set(key, rewritten)
		}
		return true
	})
	if updated == nil {
		return schema, false
	}
	return updated, true
}

// rewriteBooleanSchemaMembers is rewriteBooleanSchemaObject for a name-to-schema map.
func rewriteBooleanSchemaMembers(members *schemas.OrderedMap) (*schemas.OrderedMap, bool) {
	if members == nil || members.Len() == 0 {
		return members, false
	}
	var updated *schemas.OrderedMap
	members.Range(func(name string, value any) bool {
		if rewritten, changed := rewriteBooleanSchema(value); changed {
			if updated == nil {
				updated = members.Clone()
			}
			updated.Set(name, rewritten)
		}
		return true
	})
	if updated == nil {
		return members, false
	}
	return updated, true
}

// rewriteBooleanSchemaObjects walks a composition list typed as schema objects, such as
// the top-level anyOf. The slice is copied only when an element changes.
func rewriteBooleanSchemaObjects(list []schemas.OrderedMap) ([]schemas.OrderedMap, bool) {
	var updated []schemas.OrderedMap
	for i := range list {
		rewritten, changed := rewriteBooleanSchemaObject(&list[i])
		if !changed {
			continue
		}
		if updated == nil {
			updated = make([]schemas.OrderedMap, len(list))
			copy(updated, list)
		}
		updated[i] = *rewritten
	}
	if updated == nil {
		return list, false
	}
	return updated, true
}
