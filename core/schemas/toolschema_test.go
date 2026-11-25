package schemas

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The customer's tagged union: two object alternatives sharing a const discriminator.
func TestFlattenRootCompositionMergesAlternatives(t *testing.T) {
	var params ToolFunctionParameters
	if err := Unmarshal([]byte(`{"type":"object","oneOf":[
		{"type":"object","properties":{"kind":{"type":"string","const":"sales"},"region":{"type":"string"}},"required":["kind"]},
		{"type":"object","properties":{"kind":{"type":"string","const":"inventory"},"warehouse":{"type":"string"}},"required":["kind"]}
	]}`), &params); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	flat, err := FlattenRootComposition(&params)
	if err != nil {
		t.Fatalf("flatten: %v", err)
	}
	if flat.OneOf != nil || flat.AnyOf != nil || flat.AllOf != nil {
		t.Fatalf("root composition left in place: %+v", flat)
	}
	if got := strings.Join(flat.Properties.Keys(), ","); got != "kind,region,warehouse" {
		t.Fatalf("properties = %s, want kind,region,warehouse", got)
	}
	if got := strings.Join(flat.Required, ","); got != "kind" {
		t.Fatalf("required = %s, want only the field every alternative requires", got)
	}
	kind, _ := flat.Properties.Get("kind")
	kindSchema, ok := kind.(*OrderedMap)
	if !ok {
		t.Fatalf("kind = %T, want the two consts merged under anyOf", kind)
	}
	if members, _ := kindSchema.Get("anyOf"); len(members.([]interface{})) != 2 {
		t.Fatalf("kind anyOf = %v, want both consts", members)
	}
	if len(params.OneOf) != 2 || params.Properties != nil && params.Properties.Len() != 0 {
		t.Fatalf("the caller's schema was mutated: %+v", params)
	}
}

func TestFlattenRootCompositionWithoutCompositionIsUnchanged(t *testing.T) {
	params := &ToolFunctionParameters{Type: "object", Properties: NewOrderedMap()}
	flat, err := FlattenRootComposition(params)
	if err != nil || flat != params {
		t.Fatalf("got (%p, %v), want the same schema back", flat, err)
	}
}

func TestFlattenRootCompositionReportsExcessiveComplexity(t *testing.T) {
	members := make([]string, 7) // 2^7 combinations exceed the 64-branch limit.
	for i := range members {
		members[i] = `{"anyOf":[{"type":"object","properties":{"left":{"type":"string"}}},{"type":"object","properties":{"right":{"type":"string"}}}]}`
	}
	var params ToolFunctionParameters
	if err := Unmarshal([]byte(`{"type":"object","allOf":[`+strings.Join(members, ",")+`]}`), &params); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, err := FlattenRootComposition(&params); !errors.Is(err, ErrToolSchemaTooComplex) {
		t.Fatalf("got %v, want ErrToolSchemaTooComplex", err)
	}
}

// Repeated $refs inside nested allOf members expand exponentially while every step yields one
// branch, so a crafted tool schema could stall request conversion; the work budget stops it.
func TestFlattenRootCompositionBoundsExpansionWork(t *testing.T) {
	for _, fanout := range []int{2, 3} {
		t.Run(fmt.Sprintf("fanout %d", fanout), func(t *testing.T) {
			defs := `"d0":{"type":"object","properties":{"x":{"type":"string"}}}`
			for i := 1; i <= 15; i++ {
				refs := make([]string, fanout)
				for j := range refs {
					refs[j] = fmt.Sprintf(`{"$ref":"#/$defs/d%d"}`, i-1)
				}
				defs += fmt.Sprintf(`,"d%d":{"allOf":[%s]}`, i, strings.Join(refs, ","))
			}
			var params ToolFunctionParameters
			if err := Unmarshal([]byte(`{"type":"object","$defs":{`+defs+`},"oneOf":[{"$ref":"#/$defs/d15"}]}`), &params); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			// Without the budget this flattens successfully after exponential work.
			if _, err := FlattenRootComposition(&params); !errors.Is(err, ErrToolSchemaTooComplex) {
				t.Fatalf("got %v, want ErrToolSchemaTooComplex", err)
			}
		})
	}
}

// Keywords beside a $ref apply together with its target; annotations beside it change nothing.
func TestFlattenRootCompositionKeepsRefSiblings(t *testing.T) {
	flatten := func(member string) *ToolFunctionParameters {
		t.Helper()
		var params ToolFunctionParameters
		if err := Unmarshal([]byte(`{"type":"object",
			"$defs":{"A":{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}},
			"allOf":[`+member+`]}`), &params); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		flat, err := FlattenRootComposition(&params)
		if err != nil {
			t.Fatalf("flatten: %v", err)
		}
		return flat
	}

	flat := flatten(`{"$ref":"#/$defs/A","properties":{"format":{"type":"string"}},"required":["format"]}`)
	if got := strings.Join(flat.Properties.Keys(), ","); got != "id,format" {
		t.Fatalf("properties = %s, want id,format", got)
	}
	if got := strings.Join(flat.Required, ","); got != "id,format" {
		t.Fatalf("required = %s, want id,format", got)
	}

	flat = flatten(`{"$ref":"#/$defs/A","description":"the record"}`)
	if got := strings.Join(flat.Properties.Keys(), ","); got != "id" {
		t.Fatalf("an annotation beside $ref changed the properties to %s", got)
	}
}

// A false (never-matching) alternative is dropped instead of loosening what the others require,
// and a true member adds no constraint.
func TestFlattenRootCompositionBooleanMembers(t *testing.T) {
	for name, tc := range map[string]struct{ schema, required string }{
		"false alternative at the root": {`{"type":"object","oneOf":[{"not":{}},{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}]}`, "id"},
		"false alternative nested":      {`{"type":"object","oneOf":[{"anyOf":[false,{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}]}]}`, "id"},
		"true member in a nested allOf": {`{"type":"object","oneOf":[{"allOf":[true,{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}]}]}`, "id"},
	} {
		t.Run(name, func(t *testing.T) {
			var params ToolFunctionParameters
			if err := Unmarshal([]byte(tc.schema), &params); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			flat, err := FlattenRootComposition(&params)
			if err != nil {
				t.Fatalf("flatten: %v", err)
			}
			if got := strings.Join(flat.Required, ","); got != tc.required {
				t.Fatalf("required = %q, want %q", got, tc.required)
			}
		})
	}
}

// A $ref is a URI fragment holding a JSON Pointer: generic type names arrive percent-encoded
// (typescript-json-schema writes Foo<Bar> as Foo%3CBar%3E) and "/" and "~" as ~1 and ~0.
func TestFlattenRootCompositionDecodesRefPointers(t *testing.T) {
	for name, tc := range map[string]struct{ key, ref string }{
		"plain key":       {"A", "#/$defs/A"},
		"escaped slash":   {"a/b", "#/$defs/a~1b"},
		"escaped tilde":   {"a~b", "#/$defs/a~0b"},
		"percent-encoded": {"Foo<Bar>", "#/definitions/Foo%3CBar%3E"},
		"encoded prefix":  {"A", "#/%24defs/A"},
	} {
		t.Run(name, func(t *testing.T) {
			collection := "$defs"
			if strings.HasPrefix(tc.ref, "#/definitions/") {
				collection = "definitions"
			}
			var params ToolFunctionParameters
			if err := Unmarshal([]byte(fmt.Sprintf(`{"type":"object","%s":{%q:{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}},"oneOf":[{"$ref":%q}]}`,
				collection, tc.key, tc.ref)), &params); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			flat, err := FlattenRootComposition(&params)
			if err != nil {
				t.Fatalf("flatten: %v", err)
			}
			if got := strings.Join(flat.Required, ","); got != "id" {
				t.Fatalf("required = %q, want the referenced definition's id", got)
			}
		})
	}
}

// A definition that is itself a $ref with constraining siblings keeps those siblings when a branch
// reaches it through a reference; a bare chain still resolves to its end.
func TestFlattenRootCompositionKeepsSiblingsAlongRefChains(t *testing.T) {
	flatten := func(a string) *ToolFunctionParameters {
		t.Helper()
		var params ToolFunctionParameters
		if err := Unmarshal([]byte(`{"type":"object","$defs":{"A":`+a+`,
			"B":{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}},
			"oneOf":[{"$ref":"#/$defs/A"}]}`), &params); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		flat, err := FlattenRootComposition(&params)
		if err != nil {
			t.Fatalf("flatten: %v", err)
		}
		return flat
	}

	flat := flatten(`{"$ref":"#/$defs/B","properties":{"format":{"type":"string"}},"required":["format"]}`)
	if got := strings.Join(flat.Required, ","); got != "id,format" {
		t.Fatalf("required = %q, want id,format", got)
	}

	flat = flatten(`{"$ref":"#/$defs/B"}`)
	if got := strings.Join(flat.Properties.Keys(), ","); got != "id" {
		t.Fatalf("a bare chain resolved to %q, want id", got)
	}

	// A definition that refers to itself beside its own fields ends instead of recursing forever.
	var cyclic ToolFunctionParameters
	if err := Unmarshal([]byte(`{"type":"object","$defs":{"A":{"$ref":"#/$defs/A","properties":{"x":{"type":"string"}}}},
		"oneOf":[{"$ref":"#/$defs/A"}]}`), &cyclic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	flat, err := FlattenRootComposition(&cyclic)
	if err != nil {
		t.Fatalf("a self-referencing definition failed with %v", err)
	}
	if got := strings.Join(flat.Properties.Keys(), ","); got != "x" {
		t.Fatalf("properties = %q, want the definition's own x", got)
	}
}

// BenchmarkFlattenRootComposition times the flattener Anthropic and Bedrock Claude run on every
// request with tools, and code-mode stubs run per read. "plain" is the no-union fast path.
func BenchmarkFlattenRootComposition(b *testing.B) {
	for _, tc := range []struct{ name, schema string }{
		{"plain", `{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer"}},"required":["query"]}`},
		{"oneOf", `{"type":"object","oneOf":[
			{"type":"object","properties":{"kind":{"type":"string","const":"sales"},"region":{"type":"string"}},"required":["kind"]},
			{"type":"object","properties":{"kind":{"type":"string","const":"inventory"},"warehouse":{"type":"string"}},"required":["kind"]}]}`},
		{"oneOf via $defs", `{"type":"object",
			"$defs":{"Sales":{"type":"object","properties":{"kind":{"const":"sales"},"region":{"type":"string"}},"required":["kind"]},
				"Inventory":{"type":"object","properties":{"kind":{"const":"inventory"},"warehouse":{"type":"string"}},"required":["kind"]}},
			"oneOf":[{"$ref":"#/$defs/Sales"},{"$ref":"#/$defs/Inventory"}]}`},
	} {
		var params ToolFunctionParameters
		if err := Unmarshal([]byte(tc.schema), &params); err != nil {
			b.Fatal(err)
		}
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := FlattenRootComposition(&params); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
