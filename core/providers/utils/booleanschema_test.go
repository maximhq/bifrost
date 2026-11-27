package utils

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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

// marshalBooleanSchemaParams preserves the exact serialization for ordered-schema assertions.
func marshalBooleanSchemaParams(t *testing.T, params *schemas.ToolFunctionParameters) string {
	t.Helper()
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal schema: %v", err)
	}
	return string(data)
}

// booleanSchemaPositionsIn holds a boolean at every schema position, beside boolean
// keyword values and instance data; booleanSchemaPositionsOut is its rewrite.
const booleanSchemaPositionsIn = `{"type":"object","properties":{` +
	`"state":true,` +
	`"deny":false,` +
	`"tags":{"type":"array","items":true},` +
	`"choice":{"anyOf":[{"type":"array","items":true},{"type":"null"}]},` +
	`"tuple":{"type":"array","prefixItems":[true,{"type":"string"}],"items":false},` +
	`"legacy":{"type":"array","items":[true,{"type":"string"}],"additionalItems":false},` +
	`"guarded":{"not":true,"if":true,"then":false,"else":true,"contains":true,"propertyNames":true,"contentSchema":true},` +
	`"keyed":{"type":"object","patternProperties":{"^x":true},"dependentSchemas":{"a":false},"$defs":{"d":true},"definitions":{"e":true},"additionalProperties":false},` +
	`"open":{"type":"object","additionalProperties":{"type":"object","properties":{"x":true}},"unevaluatedProperties":false,"unevaluatedItems":true},` +
	`"data":{"type":"boolean","enum":[true,false],"const":true,"default":true,"examples":[true]}` +
	`},"required":["state"],"additionalProperties":false,"$defs":{"anything":true},"anyOf":[{"properties":{"z":true}}]}`

const booleanSchemaPositionsOut = `{"type":"object","properties":{` +
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

// The upstream that motivated the rewrite fails with "argument of type 'bool' is not
// iterable" on any boolean in a schema position. Every one becomes its object form, in
// place, while boolean keyword values and instance data keep their booleans.
func TestRewriteBooleanSchemaNodes_RewritesSchemaPositions(t *testing.T) {
	params := booleanSchemaParams(t, booleanSchemaPositionsIn)

	rewritten, changed := RewriteBooleanSchemaNodes(params)
	if !changed {
		t.Fatal("boolean subschemas were not rewritten")
	}
	if got := marshalBooleanSchemaParams(t, rewritten); got != booleanSchemaPositionsOut {
		t.Fatalf("rewritten schema mismatch\n got: %s\nwant: %s", got, booleanSchemaPositionsOut)
	}
}

// Schemas built in code can hold a composition list or tuple-form items typed as
// []schemas.OrderedMap below the root, which serializes like the decoded []any. Its
// entries are rewritten on a copied slice and the caller's slice is left as it was.
func TestRewriteBooleanSchemaNodes_TypedSchemaSlices(t *testing.T) {
	alternatives := []schemas.OrderedMap{
		*schemas.NewOrderedMapFromPairs(schemas.KV("type", "array"), schemas.KV("items", true)),
		*schemas.NewOrderedMapFromPairs(schemas.KV("type", "null")),
	}
	tuple := []schemas.OrderedMap{
		*schemas.NewOrderedMapFromPairs(schemas.KV("not", false)),
	}
	clean := []schemas.OrderedMap{*schemas.NewOrderedMapFromPairs(schemas.KV("type", "string"))}
	props := schemas.NewOrderedMap()
	props.Set("choice", schemas.NewOrderedMapFromPairs(schemas.KV("anyOf", alternatives)))
	props.Set("pair", schemas.NewOrderedMapFromPairs(schemas.KV("type", "array"), schemas.KV("items", tuple)))
	props.Set("plain", schemas.NewOrderedMapFromPairs(schemas.KV("oneOf", clean)))
	params := &schemas.ToolFunctionParameters{Type: "object", Properties: props}

	rewritten, changed := RewriteBooleanSchemaNodes(params)
	if !changed {
		t.Fatal("boolean subschemas inside typed schema slices were not rewritten")
	}
	want := `{"type":"object","properties":{"choice":{"anyOf":[{"type":"array","items":{}},{"type":"null"}]},"pair":{"type":"array","items":[{"not":{"not":{}}}]},"plain":{"oneOf":[{"type":"string"}]}}}`
	if got := marshalBooleanSchemaParams(t, rewritten); got != want {
		t.Fatalf("typed slice rewrite mismatch\n got: %s\nwant: %s", got, want)
	}
	if items, _ := alternatives[0].Get("items"); items != true {
		t.Fatalf("caller's anyOf slice was mutated: items = %#v", items)
	}
	if not, _ := tuple[0].Get("not"); not != false {
		t.Fatalf("caller's items slice was mutated: not = %#v", not)
	}
	plain, _ := props.Get("plain")
	rewrittenPlain, _ := rewritten.Properties.Get("plain")
	if plain != rewrittenPlain {
		t.Fatal("a schema with an unchanged typed slice was copied instead of shared")
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

// TestRewriteChatToolBooleanSchemaNodes_InvalidatesOnlyRewrittenCache verifies
// that rewritten tools serialize their new parameters without changing the original cache.
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

// TestRewriteBooleanSchemaNodes_LeavesNestedInstanceData keeps schema-shaped
// instance values unchanged while rewriting a neighboring schema node.
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

// A schema stored by value in an OrderedMap serializes like a pointer, so its boolean
// subschemas are rewritten too, on a copy.
func TestRewriteBooleanSchemaNodes_OrderedMapValues(t *testing.T) {
	child := schemas.NewOrderedMapFromPairs(schemas.KV("type", "array"), schemas.KV("items", true))
	members := schemas.NewOrderedMapFromPairs(schemas.KV("x", false))
	props := schemas.NewOrderedMap()
	props.Set("list", *child)
	props.Set("obj", *schemas.NewOrderedMapFromPairs(schemas.KV("type", "object"), schemas.KV("properties", *members)))
	params := &schemas.ToolFunctionParameters{Type: "object", Properties: props}

	rewritten, changed := RewriteBooleanSchemaNodes(params)
	if !changed {
		t.Fatal("boolean subschemas in OrderedMap values were not rewritten")
	}
	want := `{"type":"object","properties":{"list":{"type":"array","items":{}},"obj":{"type":"object","properties":{"x":{"not":{}}}}}}`
	if got := marshalBooleanSchemaParams(t, rewritten); got != want {
		t.Fatalf("OrderedMap value rewrite mismatch\n got: %s\nwant: %s", got, want)
	}
	if items, _ := child.Get("items"); items != true {
		t.Fatalf("caller's child map was mutated: items = %#v", items)
	}
	if x, _ := members.Get("x"); x != false {
		t.Fatalf("caller's members map was mutated: x = %#v", x)
	}
}

// Draft-07 dependencies map a property name to a schema or to a list of property
// names. Schema values are rewritten and name lists stay as they are.
func TestRewriteBooleanSchemaNodes_LegacyDependencies(t *testing.T) {
	params := booleanSchemaParams(t, `{"type":"object","properties":{"obj":{"type":"object","dependencies":{"trigger":false,"other":true,"names":["a","b"],"nested":{"properties":{"y":true}}}}}}`)
	rewritten, changed := RewriteBooleanSchemaNodes(params)
	if !changed {
		t.Fatal("boolean subschemas under dependencies were not rewritten")
	}
	want := `{"type":"object","properties":{"obj":{"type":"object","dependencies":{"trigger":{"not":{}},"other":{},"names":["a","b"],"nested":{"properties":{"y":{}}}}}}}`
	if got := marshalBooleanSchemaParams(t, rewritten); got != want {
		t.Fatalf("dependencies rewrite mismatch\n got: %s\nwant: %s", got, want)
	}

	namesOnly := booleanSchemaParams(t, `{"type":"object","properties":{"obj":{"type":"object","dependencies":{"names":["a","b"]}}}}`)
	if rewritten, changed := RewriteBooleanSchemaNodes(namesOnly); changed || rewritten != namesOnly {
		t.Fatal("a dependencies map holding only property-name lists was rewritten")
	}
}

// A raw request body staged for passthrough gets the rewrite in place: Chat and
// Responses function tools, chat-shaped tools on Responses, namespace members and
// root-level keywords are all covered, and every other byte is kept.
func TestRewriteToolBooleanSchemaNodesJSON(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"tools: {\"items\":true}"}],` +
		`"tools":[` +
		`{"type":"function","function":{"name":"chat","parameters":{"type":"object","properties":{"a":true}}}},` +
		`{"type":"function","name":"resp","parameters":{"type":"array","items":true,"not":true}},` +
		`{"type":"namespace","name":"ns","tools":[{"type":"function","name":"inner","parameters":{"type":"object","properties":{"b":false},"additionalProperties":false}}]},` +
		`{"type":"function","name":"clean","parameters":{"type":"object",  "properties":{"c":{"type":"string"}}}},` +
		`{"type":"web_search","parameters":{"items":true}}` +
		`],"stream":true}`
	want := `{"model":"m","messages":[{"role":"user","content":"tools: {\"items\":true}"}],` +
		`"tools":[` +
		`{"type":"function","function":{"name":"chat","parameters":{"type":"object","properties":{"a":{}}}}},` +
		`{"type":"function","name":"resp","parameters":{"type":"array","items":{},"not":{}}},` +
		`{"type":"namespace","name":"ns","tools":[{"type":"function","name":"inner","parameters":{"type":"object","properties":{"b":{"not":{}}},"additionalProperties":false}}]},` +
		`{"type":"function","name":"clean","parameters":{"type":"object",  "properties":{"c":{"type":"string"}}}},` +
		`{"type":"web_search","parameters":{"items":true}}` +
		`],"stream":true}`
	got, changed, err := RewriteToolBooleanSchemaNodesJSON([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !changed || string(got) != want {
		t.Fatalf("raw body rewrite mismatch (changed=%t)\n got: %s\nwant: %s", changed, got, want)
	}

	for _, unchanged := range []string{
		`{"model":"m","messages":[]}`,
		`{"model":"m","tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"x":{"type":"boolean","default":true}},"additionalProperties":false}}}]}`,
	} {
		got, changed, err := RewriteToolBooleanSchemaNodesJSON([]byte(unchanged))
		if err != nil || changed || string(got) != unchanged {
			t.Fatalf("body without boolean subschemas changed: changed=%t err=%v got=%s", changed, err, got)
		}
	}
}

// The raw path replaces only the boolean subschemas. Number tokens are never decoded,
// so an integer past float64 precision, a trailing zero and an exponent keep their
// exact bytes, and so does the whitespace around them.
func TestRewriteToolBooleanSchemaNodesJSON_PreservesNumberTokens(t *testing.T) {
	body := `{"model":"m","tools":[{"type":"function","function":{"name":"f","parameters":` +
		`{"type":"object","properties":{"flag":true,"id":{"const":9007199254740993},` +
		`"ratio":{"type":"number","minimum":1.50,"multipleOf":1E-7,"enum":[ 12345678901234567890 ]}}}}}]}`
	want := `{"model":"m","tools":[{"type":"function","function":{"name":"f","parameters":` +
		`{"type":"object","properties":{"flag":{},"id":{"const":9007199254740993},` +
		`"ratio":{"type":"number","minimum":1.50,"multipleOf":1E-7,"enum":[ 12345678901234567890 ]}}}}}]}`
	got, changed, err := RewriteToolBooleanSchemaNodesJSON([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !changed || string(got) != want {
		t.Fatalf("raw body rewrite mismatch (changed=%t)\n got: %s\nwant: %s", changed, got, want)
	}
}

// The raw walk rewrites exactly the positions the decoded walk does.
func TestRewriteToolBooleanSchemaNodesJSON_MatchesDecodedRewrite(t *testing.T) {
	body := `{"tools":[{"name":"f","parameters":` + booleanSchemaPositionsIn + `}]}`
	want := `{"tools":[{"name":"f","parameters":` + booleanSchemaPositionsOut + `}]}`
	got, changed, err := RewriteToolBooleanSchemaNodesJSON([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !changed || string(got) != want {
		t.Fatalf("raw rewrite differs from the decoded rewrite\n got: %s\nwant: %s", got, want)
	}
}

// rawToolsBody builds a body whose tools array holds n copies of each tool in tools.
func rawToolsBody(n int, tools ...string) string {
	var b strings.Builder
	b.WriteString(`{"model":"m","tools":[`)
	for i := range n {
		for j, tool := range tools {
			if i > 0 || j > 0 {
				b.WriteByte(',')
			}
			b.WriteString(tool)
		}
	}
	b.WriteString(`],"stream":true}`)
	return b.String()
}

// rawToolsBodyCases pairs each tool with its rewritten form: Chat and Responses
// function tools, a namespace with members, a tool without a schema and a clean one.
var rawToolsBodyCases = [][2]string{
	{`{"type":"function","function":{"name":"chat","parameters":{"type":"object","properties":{"a":true}}}}`,
		`{"type":"function","function":{"name":"chat","parameters":{"type":"object","properties":{"a":{}}}}}`},
	{`{"name":"resp","parameters":{"type":"array","items":true,"not":true}}`,
		`{"name":"resp","parameters":{"type":"array","items":{},"not":{}}}`},
	{`{"type":"namespace","name":"ns","tools":[{"type":"function","name":"x","parameters":{"properties":{"b":false}}},{},{"name":"y","parameters":{"items":true}}]}`,
		`{"type":"namespace","name":"ns","tools":[{"type":"function","name":"x","parameters":{"properties":{"b":{"not":{}}}}},{},{"name":"y","parameters":{"items":{}}}]}`},
	{`{}`, `{}`},
	{`{"type":"function","name":"clean","parameters":{"type":"object",  "properties":{"c":{"type":"string"}}}}`,
		`{"type":"function","name":"clean","parameters":{"type":"object",  "properties":{"c":{"type":"string"}}}}`},
}

// A large tools array is rewritten at every position and every other byte is kept.
func TestRewriteToolBooleanSchemaNodesJSON_LargeToolsArray(t *testing.T) {
	var in, want []string
	for _, c := range rawToolsBodyCases {
		in = append(in, c[0])
		want = append(want, c[1])
	}
	body := rawToolsBody(2000, in...)
	got, changed, err := RewriteToolBooleanSchemaNodesJSON([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !changed || string(got) != rawToolsBody(2000, want...) {
		t.Fatalf("large raw body rewrite mismatch (changed=%t, got %d bytes)", changed, len(got))
	}
}

// The rewrite walks the tools array once: eight times the tools takes about eight
// times as long, not sixty-four. The bound leaves room for timer noise.
func TestRewriteToolBooleanSchemaNodesJSON_LinearInToolCount(t *testing.T) {
	measure := func(n int) time.Duration {
		body := []byte(rawToolsBody(n, `{}`, `{"type":"function","name":"f","parameters":{"type":"object"}}`))
		best := time.Duration(1<<63 - 1)
		for range 3 {
			start := time.Now()
			if _, changed, err := RewriteToolBooleanSchemaNodesJSON(body); err != nil || changed {
				t.Fatalf("unexpected rewrite: changed=%t err=%v", changed, err)
			}
			best = min(best, time.Since(start))
		}
		return best
	}
	small, large := measure(500), measure(4000)
	if large > 24*small {
		t.Fatalf("rewrite is not linear in the tool count: 1000 tools took %v, 8000 took %v (%.1fx)", small, large, float64(large)/float64(small))
	}
}

// nestedItemsSchema nests depth "items" schemas around a leaf that carries payload, an
// examples array of the given number of strings, ahead of a boolean "not".
func nestedItemsSchema(depth, payload int) string {
	var b strings.Builder
	for range depth {
		b.WriteString(`{"type":"array","items":`)
	}
	b.WriteString(`{"type":"string","examples":[`)
	for i := range payload {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"{\"items\":true} [x]"`)
	}
	b.WriteString(`],"not":true}`)
	for range depth {
		b.WriteByte('}')
	}
	return b.String()
}

// The raw walk reads each byte once, so a fixed-size leaf payload costs about the same
// however deep the schemas around it are nested: sixty-four times the depth takes about
// as long, not sixty-four times as long. The bound leaves room for timer noise.
func TestRewriteToolBooleanSchemaNodesJSON_LinearInSchemaDepth(t *testing.T) {
	measure := func(depth int) time.Duration {
		schema := nestedItemsSchema(depth, 20000)
		body := []byte(`{"tools":[{"name":"f","parameters":` + schema + `}]}`)
		want := `{"tools":[{"name":"f","parameters":` + strings.Replace(schema, `"not":true`, `"not":{}`, 1) + `}]}`
		best := time.Duration(1<<63 - 1)
		for range 3 {
			start := time.Now()
			got, changed, err := RewriteToolBooleanSchemaNodesJSON(body)
			best = min(best, time.Since(start))
			if err != nil || !changed || string(got) != want {
				t.Fatalf("depth %d: unexpected rewrite: changed=%t err=%v", depth, changed, err)
			}
		}
		return best
	}
	shallow, deep := measure(2), measure(128)
	if deep > 3*shallow {
		t.Fatalf("rewrite rescans nested schemas: depth 2 took %v, depth 128 took %v (%.1fx)", shallow, deep, float64(deep)/float64(shallow))
	}
}

// The scanner steps over strings holding brackets, quotes and escapes, reads escaped
// keywords as the decoded walk does, and keeps the whitespace between tokens.
func TestRewriteToolBooleanSchemaNodesJSON_ScansTokensExactly(t *testing.T) {
	body := `{"tools":[{"name":"f","parameters": { "description" : "a \"} ] {\\ , [ \" x" ,` +
		` "propert\u0069es" : { "x\"y" : true , "z" : { "enum" : [ {"items":true} , "]" ] , "items" : [ false , 1 ] } } ,` +
		"\n\t\"not\"\r: false , \"default\" : { \"not\" : true } } }]}"
	want := `{"tools":[{"name":"f","parameters": { "description" : "a \"} ] {\\ , [ \" x" ,` +
		` "propert\u0069es" : { "x\"y" : {} , "z" : { "enum" : [ {"items":true} , "]" ] , "items" : [ {"not":{}} , 1 ] } } ,` +
		"\n\t\"not\"\r: {\"not\":{}} , \"default\" : { \"not\" : true } } }]}"
	got, changed, err := RewriteToolBooleanSchemaNodesJSON([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !changed || string(got) != want {
		t.Fatalf("raw body rewrite mismatch (changed=%t)\n got: %s\nwant: %s", changed, got, want)
	}
}

func BenchmarkRewriteToolBooleanSchemaNodesJSON_LargeToolsArray(b *testing.B) {
	body := []byte(rawToolsBody(20000, `{}`, `{"type":"function","name":"f","parameters":{"type":"object","items":true}}`))
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := RewriteToolBooleanSchemaNodesJSON(body); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRewriteToolBooleanSchemaNodesJSON_NestedSchemas(b *testing.B) {
	body := []byte(`{"tools":[{"name":"f","parameters":` + nestedItemsSchema(64, 20000) + `}]}`)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := RewriteToolBooleanSchemaNodesJSON(body); err != nil {
			b.Fatal(err)
		}
	}
}
