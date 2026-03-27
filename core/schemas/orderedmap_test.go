package schemas

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOrderedMap(t *testing.T) {
	om := NewOrderedMap()
	assert.NotNil(t, om)
	assert.Equal(t, 0, om.Len())
	assert.Empty(t, om.Keys())
}

func TestNewOrderedMapFromPairs(t *testing.T) {
	om := NewOrderedMapFromPairs(
		KV("b", 2),
		KV("a", 1),
		KV("c", 3),
	)
	assert.Equal(t, 3, om.Len())
	assert.Equal(t, []string{"b", "a", "c"}, om.Keys())

	v, ok := om.Get("a")
	assert.True(t, ok)
	assert.Equal(t, 1, v)
}

func TestOrderedMap_SetPreservesInsertionOrder(t *testing.T) {
	om := NewOrderedMap()
	om.Set("z", 1)
	om.Set("a", 2)
	om.Set("m", 3)

	assert.Equal(t, []string{"z", "a", "m"}, om.Keys())
}

func TestOrderedMap_SetUpdateInPlace(t *testing.T) {
	om := NewOrderedMap()
	om.Set("a", 1)
	om.Set("b", 2)
	om.Set("a", 10) // update, not re-append

	assert.Equal(t, []string{"a", "b"}, om.Keys())
	v, ok := om.Get("a")
	assert.True(t, ok)
	assert.Equal(t, 10, v)
}

func TestOrderedMap_Delete(t *testing.T) {
	om := NewOrderedMapFromPairs(
		KV("a", 1),
		KV("b", 2),
		KV("c", 3),
	)
	om.Delete("b")

	assert.Equal(t, 2, om.Len())
	assert.Equal(t, []string{"a", "c"}, om.Keys())

	_, ok := om.Get("b")
	assert.False(t, ok)
}

func TestOrderedMap_DeleteNonExistent(t *testing.T) {
	om := NewOrderedMapFromPairs(KV("a", 1))
	om.Delete("b") // should not panic
	assert.Equal(t, 1, om.Len())
}

func TestOrderedMap_Range(t *testing.T) {
	om := NewOrderedMapFromPairs(
		KV("x", 1),
		KV("y", 2),
		KV("z", 3),
	)

	var keys []string
	var vals []interface{}
	om.Range(func(key string, value interface{}) bool {
		keys = append(keys, key)
		vals = append(vals, value)
		return true
	})

	assert.Equal(t, []string{"x", "y", "z"}, keys)
	assert.Equal(t, []interface{}{1, 2, 3}, vals)
}

func TestOrderedMap_RangeEarlyStop(t *testing.T) {
	om := NewOrderedMapFromPairs(
		KV("a", 1),
		KV("b", 2),
		KV("c", 3),
	)

	var keys []string
	om.Range(func(key string, _ interface{}) bool {
		keys = append(keys, key)
		return key != "b" // stop after "b"
	})

	assert.Equal(t, []string{"a", "b"}, keys)
}

func TestOrderedMap_Clone(t *testing.T) {
	om := NewOrderedMapFromPairs(
		KV("a", 1),
		KV("b", 2),
	)

	clone := om.Clone()
	assert.Equal(t, om.Keys(), clone.Keys())

	// Modifying clone doesn't affect original
	clone.Set("c", 3)
	assert.Equal(t, 2, om.Len())
	assert.Equal(t, 3, clone.Len())
}

func TestOrderedMap_ToMap(t *testing.T) {
	om := NewOrderedMapFromPairs(
		KV("a", 1),
		KV("b", "hello"),
	)

	m := om.ToMap()
	assert.Equal(t, map[string]interface{}{"a": 1, "b": "hello"}, m)
}

func TestOrderedMap_NilSafety(t *testing.T) {
	var om *OrderedMap

	assert.Equal(t, 0, om.Len())
	assert.Nil(t, om.Keys())
	assert.Nil(t, om.Clone())
	assert.Nil(t, om.ToMap())

	v, ok := om.Get("key")
	assert.Nil(t, v)
	assert.False(t, ok)

	// Range on nil should not panic
	om.Range(func(key string, value interface{}) bool {
		t.Fatal("should not be called")
		return true
	})

	// Delete on nil should not panic
	om.Delete("key")
}

func TestOrderedMap_MarshalJSON_PreservesOrder(t *testing.T) {
	om := NewOrderedMapFromPairs(
		KV("z_last", 1),
		KV("a_first", 2),
		KV("m_middle", 3),
	)

	data, err := json.Marshal(om)
	require.NoError(t, err)
	assert.Equal(t, `{"z_last":1,"a_first":2,"m_middle":3}`, string(data))
}

func TestOrderedMap_MarshalJSON_Empty(t *testing.T) {
	om := NewOrderedMap()
	data, err := json.Marshal(om)
	require.NoError(t, err)
	assert.Equal(t, `{}`, string(data))
}

func TestOrderedMap_MarshalJSON_NilValues(t *testing.T) {
	om := OrderedMap{} // zero value, values is nil
	data, err := json.Marshal(om)
	require.NoError(t, err)
	assert.Equal(t, `null`, string(data))
}

func TestOrderedMap_UnmarshalJSON_PreservesOrder(t *testing.T) {
	input := `{"z_last":1,"a_first":"two","m_middle":true}`

	var om OrderedMap
	err := json.Unmarshal([]byte(input), &om)
	require.NoError(t, err)

	assert.Equal(t, []string{"z_last", "a_first", "m_middle"}, om.Keys())

	v, ok := om.Get("z_last")
	assert.True(t, ok)
	assert.Equal(t, float64(1), v) // JSON numbers are float64

	v, ok = om.Get("a_first")
	assert.True(t, ok)
	assert.Equal(t, "two", v)

	v, ok = om.Get("m_middle")
	assert.True(t, ok)
	assert.Equal(t, true, v)
}

func TestOrderedMap_UnmarshalJSON_NestedObjects(t *testing.T) {
	input := `{"outer_b":{"inner_z":1,"inner_a":2},"outer_a":"simple"}`

	var om OrderedMap
	err := json.Unmarshal([]byte(input), &om)
	require.NoError(t, err)

	assert.Equal(t, []string{"outer_b", "outer_a"}, om.Keys())

	nested, ok := om.Get("outer_b")
	assert.True(t, ok)

	nestedOM, ok := nested.(*OrderedMap)
	require.True(t, ok, "nested object should be *OrderedMap, got %T", nested)
	assert.Equal(t, []string{"inner_z", "inner_a"}, nestedOM.Keys())
}

func TestOrderedMap_UnmarshalJSON_Null(t *testing.T) {
	var om OrderedMap
	err := json.Unmarshal([]byte("null"), &om)
	require.NoError(t, err)
	assert.Equal(t, 0, om.Len())
}

func TestOrderedMap_JSONRoundTrip(t *testing.T) {
	original := NewOrderedMapFromPairs(
		KV("answer", map[string]interface{}{
			"type":        "string",
			"description": "The answer to the question",
		}),
		KV("chain_of_thought", map[string]interface{}{
			"type":        "string",
			"description": "Reasoning chain",
		}),
		KV("citations", map[string]interface{}{
			"type":        "array",
			"description": "Sources",
		}),
	)

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var restored OrderedMap
	err = json.Unmarshal(data, &restored)
	require.NoError(t, err)

	assert.Equal(t, original.Keys(), restored.Keys())
}

func TestOrderedMap_MarshalSorted(t *testing.T) {
	om := NewOrderedMapFromPairs(
		KV("z", 1),
		KV("a", 2),
		KV("m", 3),
	)

	data, err := om.MarshalSorted()
	require.NoError(t, err)
	assert.Equal(t, `{"a":2,"m":3,"z":1}`, string(data))
}

func TestOrderedMap_MarshalSorted_Nil(t *testing.T) {
	var om *OrderedMap
	data, err := om.MarshalSorted()
	require.NoError(t, err)
	assert.Equal(t, `null`, string(data))
}

func TestOrderedMapFromMap(t *testing.T) {
	m := map[string]interface{}{"a": 1, "b": 2}
	om := OrderedMapFromMap(m)
	assert.Equal(t, 2, om.Len())

	v, ok := om.Get("a")
	assert.True(t, ok)
	assert.Equal(t, 1, v)
}

func TestOrderedMapFromMap_Nil(t *testing.T) {
	om := OrderedMapFromMap(nil)
	assert.Nil(t, om)
}

func TestOrderedMapFromMap_DefaultSortsKeys(t *testing.T) {
	m := map[string]interface{}{"z": 3, "a": 1, "m": 2}
	om := OrderedMapFromMap(m)

	data, err := json.Marshal(om)
	require.NoError(t, err)
	assert.Equal(t, `{"a":1,"m":2,"z":3}`, string(data))
}

func TestOrderedMapFromMap_CustomSortFn(t *testing.T) {
	m := map[string]interface{}{"z": 3, "a": 1, "m": 2}
	reverse := func(keys []string) {
		sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	}
	om := OrderedMapFromMap(m, reverse)

	data, err := json.Marshal(om)
	require.NoError(t, err)
	assert.Equal(t, `{"z":3,"m":2,"a":1}`, string(data))
}

func TestOrderedMap_NestedOrderedMapMarshal(t *testing.T) {
	inner := NewOrderedMapFromPairs(
		KV("z_prop", "last"),
		KV("a_prop", "first"),
	)
	outer := NewOrderedMapFromPairs(
		KV("properties", inner),
		KV("type", "object"),
	)

	data, err := json.Marshal(outer)
	require.NoError(t, err)
	assert.Equal(t, `{"properties":{"z_prop":"last","a_prop":"first"},"type":"object"}`, string(data))
}

func TestOrderedMap_UnmarshalThenMarshalPreservesOrder(t *testing.T) {
	// This is the core use case: JSON comes in with a specific order,
	// we deserialize, then re-serialize and the order is preserved.
	input := `{"answer":"string","chain_of_thought":"string","citations":"array","is_unanswered":"boolean"}`

	var om OrderedMap
	err := json.Unmarshal([]byte(input), &om)
	require.NoError(t, err)

	output, err := json.Marshal(om)
	require.NoError(t, err)
	assert.Equal(t, input, string(output))
}

func TestOrderedMap_SortKeys_PlainMapValues(t *testing.T) {
	om := NewOrderedMapFromPairs(
		KV("properties", map[string]interface{}{
			"z_field": map[string]interface{}{
				"type":        "string",
				"description": "last field",
			},
			"a_field": map[string]interface{}{
				"type":        "number",
				"description": "first field",
			},
		}),
		KV("type", "object"),
	)

	om.SortKeys()

	assert.Equal(t, []string{"type", "properties"}, om.Keys(), "top-level keys should be schema-sorted")

	props, ok := om.Get("properties")
	require.True(t, ok)
	propsOM, ok := props.(*OrderedMap)
	require.True(t, ok, "plain map should be converted to *OrderedMap, got %T", props)

	assert.Equal(t, []string{"a_field", "z_field"}, propsOM.Keys(), "nested plain map keys should be sorted")

	zField, ok := propsOM.Get("z_field")
	require.True(t, ok)
	zFieldOM, ok := zField.(*OrderedMap)
	require.True(t, ok, "deeply nested plain map should be converted to *OrderedMap, got %T", zField)
	assert.Equal(t, []string{"type", "description"}, zFieldOM.Keys(), "deeply nested keys should be schema-sorted")
}

func TestOrderedMap_SortKeys_PlainMapInSlice(t *testing.T) {
	om := NewOrderedMapFromPairs(
		KV("anyOf", []interface{}{
			map[string]interface{}{
				"description": "first option",
				"type":        "string",
			},
		}),
	)

	om.SortKeys()

	anyOf, ok := om.Get("anyOf")
	require.True(t, ok)
	slice, ok := anyOf.([]interface{})
	require.True(t, ok)
	require.Len(t, slice, 1)

	elemOM, ok := slice[0].(*OrderedMap)
	require.True(t, ok, "plain map in slice should be converted to *OrderedMap, got %T", slice[0])
	assert.Equal(t, []string{"type", "description"}, elemOM.Keys())
}

func TestOrderedMap_SortedCopy_PlainMapValues(t *testing.T) {
	om := NewOrderedMapFromPairs(
		KV("properties", map[string]interface{}{
			"z_field": "string",
			"a_field": "number",
		}),
		KV("type", "object"),
	)

	sorted := om.SortedCopy()

	assert.Equal(t, []string{"type", "properties"}, sorted.Keys())

	props, ok := sorted.Get("properties")
	require.True(t, ok)
	propsOM, ok := props.(*OrderedMap)
	require.True(t, ok, "plain map should be converted to *OrderedMap in SortedCopy, got %T", props)
	assert.Equal(t, []string{"a_field", "z_field"}, propsOM.Keys())

	origProps, _ := om.Get("properties")
	_, isMap := origProps.(map[string]interface{})
	assert.True(t, isMap, "original should be unmodified (still a plain map)")
}

// --- SortedCopyPreservingProperties tests ---

func TestOrderedMap_SortedCopyPreservingProperties_Basic(t *testing.T) {
	// Schema with structural keys in wrong order and user properties in non-alpha order
	om := NewOrderedMapFromPairs(
		KV("required", []interface{}{"chain_of_thought"}),
		KV("properties", NewOrderedMapFromPairs(
			KV("chain_of_thought", NewOrderedMapFromPairs(
				KV("description", "Reasoning steps"),
				KV("type", "string"),
			)),
			KV("answer", NewOrderedMapFromPairs(
				KV("description", "The answer"),
				KV("type", "string"),
			)),
			KV("citations", NewOrderedMapFromPairs(
				KV("type", "array"),
			)),
		)),
		KV("type", "object"),
	)

	result := om.SortedCopyPreservingProperties()

	// Caching: structural keys sorted by JSON Schema priority
	assert.Equal(t, []string{"type", "properties", "required"}, result.Keys())

	// CoT: user-defined property names preserved in original order
	props, ok := result.Get("properties")
	require.True(t, ok)
	propsOM := props.(*OrderedMap)
	assert.Equal(t, []string{"chain_of_thought", "answer", "citations"}, propsOM.Keys())

	// Caching: structural keys within each property are sorted
	cot, _ := propsOM.Get("chain_of_thought")
	cotOM := cot.(*OrderedMap)
	assert.Equal(t, []string{"type", "description"}, cotOM.Keys(), "structural keys within property should be sorted")

	// Immutability: original unchanged
	assert.Equal(t, []string{"required", "properties", "type"}, om.Keys())
	origProps, _ := om.Get("properties")
	origPropsOM := origProps.(*OrderedMap)
	assert.Equal(t, []string{"chain_of_thought", "answer", "citations"}, origPropsOM.Keys())
}

func TestOrderedMap_SortedCopyPreservingProperties_NestedObjects(t *testing.T) {
	// Schema where a property is itself an object with nested properties
	om := NewOrderedMapFromPairs(
		KV("type", "object"),
		KV("properties", NewOrderedMapFromPairs(
			KV("reasoning", NewOrderedMapFromPairs(
				KV("type", "string"),
			)),
			KV("address", NewOrderedMapFromPairs(
				KV("type", "object"),
				KV("properties", NewOrderedMapFromPairs(
					KV("street", NewOrderedMapFromPairs(KV("type", "string"))),
					KV("city", NewOrderedMapFromPairs(KV("type", "string"))),
					KV("zip", NewOrderedMapFromPairs(KV("type", "string"))),
				)),
			)),
			KV("answer", NewOrderedMapFromPairs(
				KV("type", "string"),
			)),
		)),
	)

	result := om.SortedCopyPreservingProperties()

	// CoT: outer property names preserved
	props, _ := result.Get("properties")
	propsOM := props.(*OrderedMap)
	assert.Equal(t, []string{"reasoning", "address", "answer"}, propsOM.Keys())

	// CoT: inner nested property names preserved
	addr, _ := propsOM.Get("address")
	addrOM := addr.(*OrderedMap)
	innerProps, _ := addrOM.Get("properties")
	innerPropsOM := innerProps.(*OrderedMap)
	assert.Equal(t, []string{"street", "city", "zip"}, innerPropsOM.Keys())
}

func TestOrderedMap_SortedCopyPreservingProperties_ThreeLevelNesting(t *testing.T) {
	om := NewOrderedMapFromPairs(
		KV("type", "object"),
		KV("properties", NewOrderedMapFromPairs(
			KV("organization", NewOrderedMapFromPairs(
				KV("type", "object"),
				KV("properties", NewOrderedMapFromPairs(
					KV("department", NewOrderedMapFromPairs(
						KV("type", "object"),
						KV("properties", NewOrderedMapFromPairs(
							KV("team_lead", NewOrderedMapFromPairs(KV("type", "string"))),
							KV("team_name", NewOrderedMapFromPairs(KV("type", "string"))),
						)),
					)),
					KV("budget", NewOrderedMapFromPairs(KV("type", "number"))),
				)),
			)),
			KV("summary", NewOrderedMapFromPairs(KV("type", "string"))),
		)),
	)

	result := om.SortedCopyPreservingProperties()

	// Level 1: organization, summary
	props, _ := result.Get("properties")
	propsOM := props.(*OrderedMap)
	assert.Equal(t, []string{"organization", "summary"}, propsOM.Keys())

	// Level 2: department, budget
	org, _ := propsOM.Get("organization")
	orgProps, _ := org.(*OrderedMap).Get("properties")
	orgPropsOM := orgProps.(*OrderedMap)
	assert.Equal(t, []string{"department", "budget"}, orgPropsOM.Keys())

	// Level 3: team_lead, team_name
	dept, _ := orgPropsOM.Get("department")
	deptProps, _ := dept.(*OrderedMap).Get("properties")
	deptPropsOM := deptProps.(*OrderedMap)
	assert.Equal(t, []string{"team_lead", "team_name"}, deptPropsOM.Keys())
}

func TestOrderedMap_SortedCopyPreservingProperties_WithDefs(t *testing.T) {
	// $defs definition names should be sorted (for caching), but properties
	// within each definition should be preserved
	om := NewOrderedMapFromPairs(
		KV("$defs", NewOrderedMapFromPairs(
			KV("Metadata", NewOrderedMapFromPairs(
				KV("type", "object"),
				KV("properties", NewOrderedMapFromPairs(
					KV("latency_ms", NewOrderedMapFromPairs(KV("type", "number"))),
					KV("model_version", NewOrderedMapFromPairs(KV("type", "string"))),
				)),
			)),
			KV("Citation", NewOrderedMapFromPairs(
				KV("type", "object"),
				KV("properties", NewOrderedMapFromPairs(
					KV("url", NewOrderedMapFromPairs(KV("type", "string"))),
					KV("text", NewOrderedMapFromPairs(KV("type", "string"))),
				)),
			)),
		)),
		KV("type", "object"),
		KV("properties", NewOrderedMapFromPairs(
			KV("answer", NewOrderedMapFromPairs(KV("type", "string"))),
		)),
	)

	result := om.SortedCopyPreservingProperties()

	// Caching: $defs definition names are sorted alphabetically
	defs, _ := result.Get("$defs")
	defsOM := defs.(*OrderedMap)
	assert.Equal(t, []string{"Citation", "Metadata"}, defsOM.Keys())

	// CoT: properties within each $def are preserved
	meta, _ := defsOM.Get("Metadata")
	metaProps, _ := meta.(*OrderedMap).Get("properties")
	metaPropsOM := metaProps.(*OrderedMap)
	assert.Equal(t, []string{"latency_ms", "model_version"}, metaPropsOM.Keys())

	citation, _ := defsOM.Get("Citation")
	citProps, _ := citation.(*OrderedMap).Get("properties")
	citPropsOM := citProps.(*OrderedMap)
	assert.Equal(t, []string{"url", "text"}, citPropsOM.Keys())
}

func TestOrderedMap_SortedCopyPreservingProperties_NilAndEmpty(t *testing.T) {
	// nil returns nil
	var nilOM *OrderedMap
	assert.Nil(t, nilOM.SortedCopyPreservingProperties())

	// Empty returns empty
	empty := &OrderedMap{keys: []string{}, values: make(map[string]interface{})}
	result := empty.SortedCopyPreservingProperties()
	assert.NotNil(t, result)
	assert.Equal(t, 0, result.Len())

	// No "properties" key behaves like SortedCopy
	noProps := NewOrderedMapFromPairs(
		KV("required", []interface{}{"a"}),
		KV("type", "object"),
		KV("description", "test"),
	)
	result = noProps.SortedCopyPreservingProperties()
	assert.Equal(t, []string{"type", "description", "required"}, result.Keys())
}

func TestOrderedMap_SortedCopyPreservingProperties_PlainMapInsideProperties(t *testing.T) {
	// When properties contains a plain map (not OrderedMap), it should be
	// converted and have its nested values processed
	om := NewOrderedMapFromPairs(
		KV("type", "object"),
		KV("properties", map[string]interface{}{
			"field_a": map[string]interface{}{"description": "first", "type": "string"},
		}),
	)

	result := om.SortedCopyPreservingProperties()

	// The properties value should be converted to *OrderedMap
	props, ok := result.Get("properties")
	require.True(t, ok)
	_, isOM := props.(*OrderedMap)
	assert.True(t, isOM, "plain map should be converted to *OrderedMap")
}

func TestOrderedMap_EmptyArray(t *testing.T) {
	input := `{"items":[],"name":"test"}`

	var om OrderedMap
	err := json.Unmarshal([]byte(input), &om)
	require.NoError(t, err)

	v, ok := om.Get("items")
	assert.True(t, ok)
	assert.Equal(t, []interface{}{}, v)

	output, err := json.Marshal(om)
	require.NoError(t, err)
	assert.Equal(t, input, string(output))
}
