package schemas

import (
	"bytes"
	"testing"

	"github.com/bytedance/sonic"
)

// The optimized OrderedMap.MarshalJSON recurses into nested OrderedMap values
// directly instead of routing each through MarshalSorted (sonic's json.Marshaler
// path), which used to re-compact/re-escape every nesting level. That is safe
// iff MarshalJSON already emits what that sonic pass would produce — i.e. its
// output is compaction-stable. MarshalSorted(om) (== sonic.ConfigStd.Marshal) is
// exactly the pass the old code applied to each nested value, so:
//
//	bytes.Equal(om.MarshalJSON(), MarshalSorted(om))
//
// verifying it for a value proves the skipped pass was a no-op there, and because
// JSON compaction/escaping is structurally local, top-level stability implies it
// at every nesting level. This is the byte-identity guardrail for the change.
func assertMarshalStable(t *testing.T, name string, om *OrderedMap) {
	t.Helper()
	direct, err := om.MarshalJSON()
	if err != nil {
		t.Fatalf("%s: MarshalJSON error: %v", name, err)
	}
	viaSonic, err := MarshalSorted(om) // == sonic.ConfigStd.Marshal, the old per-level pass
	if err != nil {
		t.Fatalf("%s: MarshalSorted error: %v", name, err)
	}
	if !bytes.Equal(direct, viaSonic) {
		t.Fatalf("%s: MarshalJSON not compaction-stable\n direct: %s\n sonic:  %s", name, direct, viaSonic)
	}
}

func TestOrderedMapMarshal_ByteIdentical(t *testing.T) {
	cases := map[string]*OrderedMap{
		"empty": NewOrderedMap(),
		"scalars": NewOrderedMapFromPairs(
			KV("b", "x"), KV("a", 1), KV("z", true), KV("n", nil),
		),
		"array": NewOrderedMapFromPairs(
			KV("items", []interface{}{1, "a", true, nil}),
		),
		// #6235 order-sensitive shape: non-alphabetical property order must survive.
		"json_schema_ordered": NewOrderedMapFromPairs(
			KV("type", "object"),
			KV("title", "AssignmentResult"),
			KV("description", "Assignment result for a single activity."),
			KV("properties", NewOrderedMapFromPairs(
				KV("reasoning", NewOrderedMapFromPairs(
					KV("type", "string"),
					KV("title", "Reasoning"),
					KV("description", "Brief explanation"),
				)),
				KV("assigned_group_id", NewOrderedMapFromPairs(
					KV("anyOf", []interface{}{
						NewOrderedMapFromPairs(KV("type", "integer")),
						NewOrderedMapFromPairs(KV("type", "null")),
					}),
					KV("title", "Assigned Group ID"),
				)),
			)),
			KV("required", []interface{}{"assigned_group_id", "reasoning"}),
			KV("additionalProperties", false),
		),
		// Escaping: HTML chars, quotes, backslashes, control chars, unicode/emoji.
		"escaping": NewOrderedMapFromPairs(
			KV("html", "<script>a && b</script>"),
			KV("quote", `he said "hi"\ and left`),
			KV("ctrl", "tab\tnewline\nreturn\r"),
			KV("unicode", "café — 🚀 —   "),
		),
		// Keys that need escaping.
		"tricky_keys": NewOrderedMapFromPairs(
			KV("a<b&c>", 1),
			KV(`a"b\c`, 2),
			KV("emoji🚀key", 3),
		),
		// Numbers: negatives, floats, values above 2^53.
		"numbers": NewOrderedMapFromPairs(
			KV("neg", -42), KV("float", 3.14159), KV("big", int64(9007199254740993)), KV("zero", 0),
		),
		"null_value_map": {values: nil}, // marshals to null
	}
	for name, om := range cases {
		assertMarshalStable(t, name, om)
	}

	// Deeply nested (build 8 levels).
	deep := NewOrderedMapFromPairs(KV("leaf", "end"))
	for i := 0; i < 8; i++ {
		deep = NewOrderedMapFromPairs(KV("child", deep), KV("i", i))
	}
	assertMarshalStable(t, "deep_nesting", deep)
}

// TestOrderedMapMarshal_InvalidUTF8 pins that invalid UTF-8 in keys and values is
// emitted as the escaped replacement rune, byte-identical to sonic.ConfigStd (and
// encoding/json). assertMarshalStable cannot catch this: MarshalSorted(om) also runs
// om.MarshalJSON (OrderedMap is a json.Marshaler) and sonic's compaction accepts a
// literal replacement rune, so both sides would agree even while both diverge from
// sonic marshaling the equivalent plain map. Comparing against sonic.ConfigStd on a
// plain map is the real oracle here.
func TestOrderedMapMarshal_InvalidUTF8(t *testing.T) {
	bad := string([]byte{'a', 0xff, 'b'}) // lone 0xff -> invalid UTF-8

	cases := []struct {
		name string
		om   *OrderedMap
		ref  map[string]interface{}
	}{
		{"invalid_value", NewOrderedMapFromPairs(KV("k", bad)), map[string]interface{}{"k": bad}},
		{"invalid_key", NewOrderedMapFromPairs(KV(bad, "v")), map[string]interface{}{bad: "v"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.om.MarshalJSON()
			if err != nil {
				t.Fatalf("MarshalJSON: %v", err)
			}
			want, err := sonic.ConfigStd.Marshal(tc.ref)
			if err != nil {
				t.Fatalf("sonic.ConfigStd.Marshal: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("invalid-UTF-8 output diverges from sonic.ConfigStd:\n  got  %s\n  want %s", got, want)
			}
		})
	}
}

// FuzzOrderedMapMarshal parses arbitrary JSON objects into an OrderedMap (which
// preserves key order and nests OrderedMaps) and asserts MarshalJSON stays
// compaction-stable — catching any escaping/number/edge case the corpus misses.
func FuzzOrderedMapMarshal(f *testing.F) {
	seeds := []string{
		`{}`,
		`{"a":1,"b":"x"}`,
		`{"o":{"n":{"deep":true}},"arr":[1,{"k":"<v>"},null]}`,
		`{"html":"<a>&</a>","q":"\"x\"","u":"café 🚀"}`,
		`{"z":1,"a":2,"m":3}`,
		`{"big":9007199254740993,"f":1.5,"neg":-7}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		om := &OrderedMap{}
		if err := om.UnmarshalJSON(data); err != nil {
			return // only JSON objects round-trip through OrderedMap
		}
		direct, err := om.MarshalJSON()
		if err != nil {
			t.Fatalf("MarshalJSON error on %q: %v", data, err)
		}
		viaSonic, err := MarshalSorted(om)
		if err != nil {
			t.Fatalf("MarshalSorted error on %q: %v", data, err)
		}
		if !bytes.Equal(direct, viaSonic) {
			t.Fatalf("not compaction-stable\n input:  %s\n direct: %s\n sonic:  %s", data, direct, viaSonic)
		}
	})
}

// buildBigSchema builds a large, deeply-nested JSON-Schema-shaped OrderedMap:
// nProps top-level properties, each an object with a few structural keys, and
// every `depth`-th one nested `depth` levels deep with an array-of-objects —
// closer to a real tool-heavy / MCP-injected request.
func buildBigSchema(nProps, depth int) *OrderedMap {
	leaf := func(desc string) *OrderedMap {
		return NewOrderedMapFromPairs(
			KV("type", "string"),
			KV("title", desc),
			KV("description", "Field "+desc+" with a reasonably long description to size the payload"),
			KV("enum", []interface{}{"alpha", "beta", "gamma", "delta"}),
		)
	}
	nest := func(d int, inner *OrderedMap) *OrderedMap {
		for i := 0; i < d; i++ {
			inner = NewOrderedMapFromPairs(
				KV("type", "object"),
				KV("description", "nested level"),
				KV("properties", NewOrderedMapFromPairs(
					KV("child", inner),
					KV("items", NewOrderedMapFromPairs(
						KV("type", "array"),
						KV("items", inner),
					)),
				)),
				KV("required", []interface{}{"child"}),
				KV("additionalProperties", false),
			)
		}
		return inner
	}
	props := NewOrderedMapWithCapacity(nProps)
	req := make([]interface{}, 0, nProps)
	for i := 0; i < nProps; i++ {
		name := "param_" + string(rune('a'+i%26)) + string(rune('0'+i%10))
		if i%depth == 0 {
			props.Set(name, nest(depth, leaf(name)))
		} else {
			props.Set(name, leaf(name))
		}
		req = append(req, name)
	}
	return NewOrderedMapFromPairs(
		KV("type", "object"),
		KV("title", "BigTool"),
		KV("description", "A large tool schema for benchmarking"),
		KV("properties", props),
		KV("required", req),
		KV("additionalProperties", false),
	)
}

func TestOrderedMapMarshal_BigSchemaStable(t *testing.T) {
	assertMarshalStable(t, "big_schema", buildBigSchema(24, 4))
}

func BenchmarkOrderedMapMarshalBig(b *testing.B) {
	om := buildBigSchema(24, 4)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := om.MarshalJSON(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkOrderedMapMarshal marshals a realistic, tool-schema-shaped nested
// OrderedMap. Run against HEAD vs this change to quantify the win.
func BenchmarkOrderedMapMarshal(b *testing.B) {
	om := NewOrderedMapFromPairs(
		KV("type", "object"),
		KV("properties", NewOrderedMapFromPairs(
			KV("location", NewOrderedMapFromPairs(KV("type", "string"), KV("description", "City and state"))),
			KV("unit", NewOrderedMapFromPairs(
				KV("type", "string"),
				KV("enum", []interface{}{"celsius", "fahrenheit"}),
				KV("description", "Temperature unit"),
			)),
			KV("days", NewOrderedMapFromPairs(
				KV("type", "array"),
				KV("items", NewOrderedMapFromPairs(KV("type", "integer"))),
				KV("description", "Forecast horizon in days"),
			)),
		)),
		KV("required", []interface{}{"location"}),
		KV("additionalProperties", false),
	)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := om.MarshalJSON(); err != nil {
			b.Fatal(err)
		}
	}
}
