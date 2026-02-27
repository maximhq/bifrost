package schemas

import (
	"encoding/json"
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
