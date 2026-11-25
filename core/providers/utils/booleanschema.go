package utils

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// RewriteBooleanSchemaNodes replaces every boolean subschema in a tool parameter schema
// with its object form, for upstreams whose validator iterates every subschema as an
// object and fails on a boolean ("argument of type 'bool' is not iterable"). The
// rewrite is lossless: `true` and `{}` both accept every instance, and `false` and
// `{"not":{}}` both reject every instance.
//
// Only schema positions are rewritten: the values of properties, patternProperties,
// $defs, definitions and dependentSchemas, and the schema values of draft-07
// dependencies; the single-schema keywords items, contains, propertyNames, not, if,
// then, else, additionalItems and contentSchema; and the entries of anyOf, oneOf,
// allOf, prefixItems and tuple-form items. A boolean
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

// RewriteToolBooleanSchemaNodesJSON applies the same rewrite to the tool parameter
// schemas inside a raw Chat Completions or Responses request body, for bodies that
// are forwarded without being decoded (large payload passthrough). A function tool
// carries its schema at parameters (Responses) or function.parameters (Chat
// Completions, also accepted on Responses), and a namespace tool is walked through
// its member tools. Each schema is walked as a whole schema object, so its root
// keywords are covered too.
//
// Only the boolean subschemas are replaced, each spliced in at its own offset; every
// other byte of the body, number tokens and whitespace included, is kept as it was.
// A body with no boolean subschema is returned as-is.
func RewriteToolBooleanSchemaNodesJSON(body []byte) ([]byte, bool, error) {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body, false, nil
	}
	if tools.Index <= 0 || tools.Index+len(tools.Raw) > len(body) || string(body[tools.Index:tools.Index+len(tools.Raw)]) != tools.Raw {
		return nil, false, fmt.Errorf("cannot locate tools in the request body")
	}
	var edits []rawSchemaEdit
	if err := collectToolSchemaEdits(tools.Raw, gjson.Parse(tools.Raw), "", &edits); err != nil {
		return nil, false, err
	}
	if len(edits) == 0 {
		return body, false, nil
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	size := len(body)
	for _, edit := range edits {
		size += len(edit.raw) - (edit.end - edit.start)
	}
	out := make([]byte, 0, size)
	prev := 0
	for _, edit := range edits {
		out = append(out, body[prev:tools.Index+edit.start]...)
		out = append(out, edit.raw...)
		prev = tools.Index + edit.end
	}
	out = append(out, body[prev:]...)
	return out, true, nil
}

// rawSchemaEdit replaces toolsRaw[start:end] with raw.
type rawSchemaEdit struct {
	start, end int
	raw        []byte
}

// collectToolSchemaEdits records one edit per tool schema in list, the tools array at
// prefix (a gjson path into toolsRaw, empty for toolsRaw itself, used in errors), that
// holds a boolean subschema. The array is walked once: ForEach and Result.Get carry
// offsets relative to toolsRaw, so no lookup rescans the array from its start.
func collectToolSchemaEdits(toolsRaw string, list gjson.Result, prefix string, edits *[]rawSchemaEdit) error {
	var err error
	i := -1
	list.ForEach(func(_, tool gjson.Result) bool {
		i++
		switch tool.Get("type").String() {
		case "", string(schemas.ResponsesToolTypeFunction):
			if err = collectSchemaEdit(toolsRaw, tool.Get("parameters"), prefix, i, "parameters", edits); err != nil {
				return false
			}
			if err = collectSchemaEdit(toolsRaw, tool.Get("function.parameters"), prefix, i, "function.parameters", edits); err != nil {
				return false
			}
		case string(schemas.ResponsesToolTypeNamespace):
			if members := tool.Get("tools"); members.IsArray() {
				if err = collectToolSchemaEdits(toolsRaw, members, toolSchemaPath(prefix, i, "tools"), edits); err != nil {
					return false
				}
			}
		}
		return true
	})
	return err
}

// toolSchemaPath joins prefix, the tool index and suffix into a gjson path for errors.
func toolSchemaPath(prefix string, index int, suffix string) string {
	path := strconv.Itoa(index) + "." + suffix
	if prefix != "" {
		path = prefix + "." + path
	}
	return path
}

// collectSchemaEdit records one edit per boolean subschema of value, the schema object
// at suffix of the tool at index in the tools array at prefix. A missing or non-object
// value is not a schema and is skipped. The schema is walked in place and never
// re-encoded, so number tokens beyond float64 precision keep their exact bytes.
func collectSchemaEdit(toolsRaw string, value gjson.Result, prefix string, index int, suffix string, edits *[]rawSchemaEdit) error {
	if !value.IsObject() {
		return nil
	}
	if value.Index <= 0 || value.Index+len(value.Raw) > len(toolsRaw) || toolsRaw[value.Index:value.Index+len(value.Raw)] != value.Raw {
		return fmt.Errorf("cannot locate tool schema %s in the request body", toolSchemaPath(prefix, index, suffix))
	}
	if !gjson.Valid(value.Raw) {
		return fmt.Errorf("failed to decode tool schema %s: invalid JSON", toolSchemaPath(prefix, index, suffix))
	}
	collectRawBooleanSchemaObject(value, edits)
	return nil
}

// rawTrueSchema and rawFalseSchema are the object forms spliced over a raw boolean
// subschema. Edits only read them.
var (
	rawTrueSchema  = []byte(`{}`)
	rawFalseSchema = []byte(`{"not":{}}`)
)

// collectRawBooleanSchemaObject walks one raw schema object with the keyword rules of
// rewriteBooleanSchemaKeyword. Offsets from ForEach are relative to toolsRaw because
// every walked value descends from a Result located in it.
func collectRawBooleanSchemaObject(schema gjson.Result, edits *[]rawSchemaEdit) {
	schema.ForEach(func(key, value gjson.Result) bool {
		switch booleanSchemaKeywordKind(key.Str) {
		case keywordSchemaMembers:
			if value.IsObject() {
				collectRawBooleanSchemaEntries(value, edits)
			}
		case keywordSchemaList:
			if value.IsArray() {
				collectRawBooleanSchemaEntries(value, edits)
			}
		case keywordItems:
			if value.IsArray() {
				collectRawBooleanSchemaEntries(value, edits)
			} else {
				collectRawBooleanSchema(value, edits)
			}
		case keywordSchema:
			collectRawBooleanSchema(value, edits)
		case keywordBooleanOrSchema:
			if value.IsObject() {
				collectRawBooleanSchemaObject(value, edits)
			}
		}
		return true
	})
}

// collectRawBooleanSchemaEntries walks every value of a raw object or array whose
// entries are all schema positions.
func collectRawBooleanSchemaEntries(container gjson.Result, edits *[]rawSchemaEdit) {
	container.ForEach(func(_, entry gjson.Result) bool {
		collectRawBooleanSchema(entry, edits)
		return true
	})
}

// collectRawBooleanSchema handles the raw value at a schema position: a boolean is
// replaced by its object form and an object is walked as a schema.
func collectRawBooleanSchema(value gjson.Result, edits *[]rawSchemaEdit) {
	switch {
	case value.Type == gjson.True:
		*edits = append(*edits, rawSchemaEdit{start: value.Index, end: value.Index + len(value.Raw), raw: rawTrueSchema})
	case value.Type == gjson.False:
		*edits = append(*edits, rawSchemaEdit{start: value.Index, end: value.Index + len(value.Raw), raw: rawFalseSchema})
	case value.IsObject():
		collectRawBooleanSchemaObject(value, edits)
	}
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
	case schemas.OrderedMap:
		// Schemas built in code may store the map by value; it serializes the same way.
		if rewritten, changed := rewriteBooleanSchemaObject(&typed); changed {
			return *rewritten, true
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

// booleanSchemaKeyword classifies how a schema keyword's value holds subschemas. The
// decoded and the raw walks both switch on it, so they rewrite the same positions.
type booleanSchemaKeyword uint8

const (
	// keywordNoSchema: instance data, required, names and unknown keywords.
	keywordNoSchema booleanSchemaKeyword = iota
	// keywordSchemaMembers: a name-to-schema map such as properties.
	keywordSchemaMembers
	// keywordSchemaList: a list whose every entry is a schema, such as anyOf.
	keywordSchemaList
	// keywordItems: one schema, or the draft-07 tuple form, a list of schemas.
	keywordItems
	// keywordSchema: a single schema, such as not.
	keywordSchema
	// keywordBooleanOrSchema: a boolean is the keyword's standard form and upstreams
	// accept it, so only an object there is walked.
	keywordBooleanOrSchema
)

// booleanSchemaKeywordKind returns how key holds subschemas.
func booleanSchemaKeywordKind(key string) booleanSchemaKeyword {
	switch key {
	case "properties", "patternProperties", "$defs", "definitions", "dependentSchemas":
		return keywordSchemaMembers
	case "dependencies":
		// Draft-07: each value is a schema or a list of property names. A list is not
		// a schema, so the schema rewrite leaves it as it is.
		return keywordSchemaMembers
	case "anyOf", "oneOf", "allOf", "prefixItems":
		return keywordSchemaList
	case "items":
		return keywordItems
	case "contains", "propertyNames", "not", "if", "then", "else", "additionalItems", "contentSchema":
		return keywordSchema
	case "additionalProperties", "unevaluatedProperties", "unevaluatedItems":
		return keywordBooleanOrSchema
	}
	return keywordNoSchema
}

// rewriteBooleanSchemaKeyword rewrites one keyword of a schema object.
func rewriteBooleanSchemaKeyword(key string, value any) (any, bool) {
	switch booleanSchemaKeywordKind(key) {
	case keywordSchemaMembers:
		return rewriteBooleanSchemaMemberValues(value)
	case keywordSchemaList:
		rewritten, changed, _ := rewriteBooleanSchemaListValue(value)
		return rewritten, changed
	case keywordItems:
		if rewritten, changed, isList := rewriteBooleanSchemaListValue(value); isList {
			return rewritten, changed
		}
		return rewriteBooleanSchema(value)
	case keywordSchema:
		return rewriteBooleanSchema(value)
	case keywordBooleanOrSchema:
		if _, ok := value.(bool); !ok {
			return rewriteBooleanSchema(value)
		}
	}
	return value, false
}

// rewriteBooleanSchemaListValue rewrites a list of schemas, decoded as []any or built
// in code as []schemas.OrderedMap (the type of the top-level anyOf); isList reports
// whether value is such a list. An unchanged list is returned as value itself.
func rewriteBooleanSchemaListValue(value any) (rewritten any, changed bool, isList bool) {
	switch list := value.(type) {
	case []any:
		rewritten, changed = rewriteBooleanSchemaList(value, list)
		return rewritten, changed, true
	case []schemas.OrderedMap:
		if updated, changed := rewriteBooleanSchemaObjects(list); changed {
			return updated, true, true
		}
		return value, false, true
	}
	return value, false, false
}

// rewriteBooleanSchemaMemberValues rewrites a name-to-schema map such as properties.
// The names are data and stay as they are; every value is a schema position.
func rewriteBooleanSchemaMemberValues(value any) (any, bool) {
	switch typed := value.(type) {
	case *schemas.OrderedMap:
		if rewritten, changed := rewriteBooleanSchemaMembers(typed); changed {
			return rewritten, true
		}
	case schemas.OrderedMap:
		if rewritten, changed := rewriteBooleanSchemaMembers(&typed); changed {
			return *rewritten, true
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
