package utils

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// booleanSchemaParams decodes raw the way a client's tool schema arrives.
func booleanSchemaParams(t *testing.T, raw string) *schemas.ToolFunctionParameters {
	t.Helper()
	var params schemas.ToolFunctionParameters
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		t.Fatalf("failed to unmarshal schema: %v", err)
	}
	return &params
}

func marshalBooleanSchemaParams(t *testing.T, params *schemas.ToolFunctionParameters) string {
	t.Helper()
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal schema: %v", err)
	}
	return string(data)
}

// The upstream that motivated the rewrite fails with "argument of type 'bool' is not
// iterable" on any boolean in a schema position. Every one becomes its object form, in
// place, while boolean keyword values and instance data keep their booleans.
func TestRewriteBooleanSchemaNodes_RewritesSchemaPositions(t *testing.T) {
	params := booleanSchemaParams(t, `{"type":"object","properties":{`+
		`"state":true,`+
		`"deny":false,`+
		`"tags":{"type":"array","items":true},`+
		`"choice":{"anyOf":[{"type":"array","items":true},{"type":"null"}]},`+
		`"tuple":{"type":"array","prefixItems":[true,{"type":"string"}],"items":false},`+
		`"legacy":{"type":"array","items":[true,{"type":"string"}],"additionalItems":false},`+
		`"guarded":{"not":true,"if":true,"then":false,"else":true,"contains":true,"propertyNames":true,"contentSchema":true},`+
		`"keyed":{"type":"object","patternProperties":{"^x":true},"dependentSchemas":{"a":false},"$defs":{"d":true},"definitions":{"e":true},"additionalProperties":false},`+
		`"open":{"type":"object","additionalProperties":{"type":"object","properties":{"x":true}},"unevaluatedProperties":false,"unevaluatedItems":true},`+
		`"data":{"type":"boolean","enum":[true,false],"const":true,"default":true,"examples":[true]}`+
		`},"required":["state"],"additionalProperties":false,"$defs":{"anything":true},"anyOf":[{"properties":{"z":true}}]}`)

	rewritten, changed := RewriteBooleanSchemaNodes(params)
	if !changed {
		t.Fatal("boolean subschemas were not rewritten")
	}
	want := `{"type":"object","properties":{` +
		`"state":{},` +
		`"deny":{"not":{}},` +
		`"tags":{"type":"array","items":{}},` +
		`"choice":{"anyOf":[{"type":"array","items":{}},{"type":"null"}]},` +
		`"tuple":{"type":"array","prefixItems":[{},{"type":"string"}],"items":{"not":{}}},` +
		`"legacy":{"type":"array","items":[{},{"type":"string"}],"additionalItems":{"not":{}}},` +
		`"guarded":{"not":{},"if":{},"then":{"not":{}},"else":{},"contains":{},"propertyNames":{},"contentSchema":{}},` +
		`"keyed":{"type":"object","patternProperties":{"^x":{}},"dependentSchemas":{"a":{"not":{}}},"$defs":{"d":{}},"definitions":{"e":{}},"additionalProperties":false},` +
		`"open":{"type":"object","additionalProperties":{"type":"object","properties":{"x":{}}},"unevaluatedProperties":false,"unevaluatedItems":true},` +
		`"data":{"type":"boolean","enum":[true,false],"const":true,"default":true,"examples":[true]}` +
		`},"required":["state"],"additionalProperties":false,"$defs":{"anything":{}},"anyOf":[{"properties":{"z":{}}}]}`
	if got := marshalBooleanSchemaParams(t, rewritten); got != want {
		t.Fatalf("rewritten schema mismatch\n got: %s\nwant: %s", got, want)
	}
}

// Booleans that are keyword values or instance data are not schemas, and neither is
// anything under an unknown keyword, so a schema made only of those is not touched.
func TestRewriteBooleanSchemaNodes_LeavesNonSchemaBooleans(t *testing.T) {
	raw := `{"type":"object","properties":{` +
		`"strict":{"type":"object","additionalProperties":false},` +
		`"loose":{"type":"object","additionalProperties":true,"unevaluatedProperties":true,"unevaluatedItems":false},` +
		`"flag":{"type":"boolean","enum":[true,false],"const":true,"default":true,"examples":[true,false]},` +
		`"not":{"type":"string"},` +
		`"meta":{"type":"string","x-extension":{"not":true,"items":true}}` +
		`},"required":["strict"],"additionalProperties":true,"default":{"items":true}}`
	params := booleanSchemaParams(t, raw)
	before := marshalBooleanSchemaParams(t, params)

	rewritten, changed := RewriteBooleanSchemaNodes(params)
	if changed {
		t.Fatalf("a schema without boolean subschemas was rewritten: %s", marshalBooleanSchemaParams(t, rewritten))
	}
	if rewritten != params {
		t.Fatal("an unchanged schema must be returned as the caller's own pointer")
	}
	if after := marshalBooleanSchemaParams(t, params); after != before {
		t.Fatalf("schema changed\nbefore: %s\n after: %s", before, after)
	}
}

// The request the caller holds is reused for fallbacks, so the rewrite must land on
// copies only, and untouched subtrees must stay shared rather than copied.
func TestRewriteBooleanSchemaNodes_DoesNotMutateInput(t *testing.T) {
	params := booleanSchemaParams(t, `{"type":"object","properties":{"state":true,"tags":{"type":"array","items":true},"name":{"type":"string"}}}`)
	before := marshalBooleanSchemaParams(t, params)
	originalProperties := params.Properties

	rewritten, changed := RewriteBooleanSchemaNodes(params)
	if !changed || rewritten == params {
		t.Fatal("expected a rewritten copy")
	}
	if after := marshalBooleanSchemaParams(t, params); after != before {
		t.Fatalf("caller's schema was mutated\nbefore: %s\n after: %s", before, after)
	}
	if params.Properties != originalProperties {
		t.Fatal("caller's properties map was replaced")
	}
	if state, _ := params.Properties.Get("state"); state != true {
		t.Fatalf("caller's properties.state = %#v, want true", state)
	}
	tags, _ := params.Properties.Get("tags")
	if items, _ := tags.(*schemas.OrderedMap).Get("items"); items != true {
		t.Fatalf("caller's properties.tags.items = %#v, want true", items)
	}
	name, _ := params.Properties.Get("name")
	rewrittenName, _ := rewritten.Properties.Get("name")
	if name.(*schemas.OrderedMap) != rewrittenName.(*schemas.OrderedMap) {
		t.Fatal("an untouched property schema was copied instead of shared")
	}
}

// The common case, a schema with no boolean subschema, must cost nothing.
func TestRewriteBooleanSchemaNodes_UnchangedSchemaDoesNotAllocate(t *testing.T) {
	params := booleanSchemaParams(t, `{"type":"object","properties":{"a":{"type":"array","items":{"type":"string"}},"b":{"anyOf":[{"type":"string"},{"type":"null"}]},"c":{"type":"object","additionalProperties":false,"properties":{"d":{"type":"boolean","enum":[true,false]}}}},"additionalProperties":false}`)
	tools := []schemas.ChatTool{{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "f", Parameters: params}}}

	if rewritten, changed := RewriteBooleanSchemaNodes(params); changed || rewritten != params {
		t.Fatal("unchanged schema was not returned as-is")
	}
	if rewritten, changed := RewriteChatToolBooleanSchemaNodes(tools); changed || &rewritten[0] != &tools[0] {
		t.Fatal("unchanged tools were not returned as-is")
	}
	if allocs := testing.AllocsPerRun(100, func() {
		RewriteBooleanSchemaNodes(params)
		RewriteChatToolBooleanSchemaNodes(tools)
	}); allocs != 0 {
		t.Fatalf("unchanged schema allocated %v times, want 0", allocs)
	}
}

// Key order feeds the prompt cache key, so the object form takes the boolean's place.
func TestRewriteBooleanSchemaNodes_PreservesKeyOrder(t *testing.T) {
	params := booleanSchemaParams(t, `{"properties":{"zulu":true,"alpha":{"type":"string"},"mike":{"items":true,"type":"array","description":"m"}},"type":"object"}`)
	rewritten, changed := RewriteBooleanSchemaNodes(params)
	if !changed {
		t.Fatal("expected a rewrite")
	}
	want := `{"properties":{"zulu":{},"alpha":{"type":"string"},"mike":{"items":{},"type":"array","description":"m"}},"type":"object"}`
	if got := marshalBooleanSchemaParams(t, rewritten); got != want {
		t.Fatalf("key order changed\n got: %s\nwant: %s", got, want)
	}
}

// Schemas built in code as plain maps get the same treatment, copy-on-write.
func TestRewriteBooleanSchemaNodes_PlainMapSchemas(t *testing.T) {
	inner := map[string]any{"type": "array", "items": true, "oneOf": []any{true, map[string]any{"type": "string"}}}
	props := schemas.NewOrderedMap()
	props.Set("list", inner)
	props.Set("members", map[string]any{"type": "object", "properties": map[string]any{"x": false}})
	params := &schemas.ToolFunctionParameters{Type: "object", Properties: props}

	rewritten, changed := RewriteBooleanSchemaNodes(params)
	if !changed {
		t.Fatal("plain-map boolean subschemas were not rewritten")
	}
	want := `{"type":"object","properties":{"list":{"items":{},"oneOf":[{},{"type":"string"}],"type":"array"},"members":{"properties":{"x":{"not":{}}},"type":"object"}}}`
	if got := marshalBooleanSchemaParams(t, rewritten); got != want {
		t.Fatalf("plain-map rewrite mismatch\n got: %s\nwant: %s", got, want)
	}
	if inner["items"] != true || inner["oneOf"].([]any)[0] != true {
		t.Fatalf("caller's plain map was mutated: %#v", inner)
	}
}

// Responses tools, namespace members included, take the same rewrite without the
// caller's tool structs being written through.
func TestRewriteResponsesToolBooleanSchemaNodes(t *testing.T) {
	dirty := booleanSchemaParams(t, `{"type":"object","properties":{"state":true}}`)
	clean := booleanSchemaParams(t, `{"type":"object","properties":{"name":{"type":"string"}}}`)
	function := &schemas.ResponsesToolFunction{Parameters: dirty}
	member := &schemas.ResponsesToolFunction{Parameters: dirty}
	tools := []schemas.ResponsesTool{
		{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("clean"), ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: clean}},
		{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("dirty"), ResponsesToolFunction: function},
		{Type: schemas.ResponsesToolTypeNamespace, Name: schemas.Ptr("ns"), ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("member"), ResponsesToolFunction: member},
		}}},
	}

	rewritten, changed := RewriteResponsesToolBooleanSchemaNodes(tools)
	if !changed {
		t.Fatal("responses tools were not rewritten")
	}
	if got := marshalBooleanSchemaParams(t, rewritten[1].ResponsesToolFunction.Parameters); got != `{"type":"object","properties":{"state":{}}}` {
		t.Fatalf("function parameters = %s", got)
	}
	if got := marshalBooleanSchemaParams(t, rewritten[2].ResponsesToolNamespace.Tools[0].ResponsesToolFunction.Parameters); got != `{"type":"object","properties":{"state":{}}}` {
		t.Fatalf("namespace member parameters = %s", got)
	}
	if rewritten[0].ResponsesToolFunction != tools[0].ResponsesToolFunction {
		t.Fatal("an unchanged tool was copied instead of shared")
	}
	if tools[1].ResponsesToolFunction != function || function.Parameters != dirty || member.Parameters != dirty {
		t.Fatal("caller's tools were written through")
	}
	if state, _ := dirty.Properties.Get("state"); state != true {
		t.Fatalf("caller's schema was mutated: state = %#v", state)
	}
}

func TestRewriteChatToolBooleanSchemaNodes_InvalidatesOnlyRewrittenCache(t *testing.T) {
	params := booleanSchemaParams(t, `{"type":"object","properties":{"state":true}}`)
	tools := []schemas.ChatTool{{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "set_state", Parameters: params}}}
	if err := tools[0].EnsureSerialized(); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	rewritten, changed := RewriteChatToolBooleanSchemaNodes(tools)
	if !changed {
		t.Fatal("expected a rewritten copy")
	}
	after, err := json.Marshal(rewritten)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"type":"function","function":{"name":"set_state","parameters":{"type":"object","properties":{"state":{}}}}}]`
	if string(after) != want {
		t.Fatalf("cached tool retained its old schema: %s", after)
	}
	original, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != string(before) {
		t.Fatalf("caller's serialized tool changed: %s", original)
	}
}

func TestRewriteBooleanSchemaNodes_LeavesNestedInstanceData(t *testing.T) {
	raw := `{"type":"object","properties":{"data":{"enum":[{"items":true}],"const":{"not":false},"default":{"properties":{"flag":true}},"examples":[{"anyOf":[true,false]}]},"changed":false}}`
	params := booleanSchemaParams(t, raw)
	rewritten, changed := RewriteBooleanSchemaNodes(params)
	if !changed {
		t.Fatal("expected the false property schema to be rewritten")
	}
	want := `{"type":"object","properties":{"data":{"enum":[{"items":true}],"const":{"not":false},"default":{"properties":{"flag":true}},"examples":[{"anyOf":[true,false]}]},"changed":{"not":{}}}}`
	if got := marshalBooleanSchemaParams(t, rewritten); got != want {
		t.Fatalf("instance data changed: %s", got)
	}
}
