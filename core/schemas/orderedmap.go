package schemas

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// OrderedMap is a map that preserves insertion order of keys.
// It stores key-value pairs and maintains the order in which keys were first inserted.
// It is NOT safe for concurrent use.
type OrderedMap struct {
	keys   []string
	values map[string]interface{}
}

// Pair is a key-value pair for constructing OrderedMaps with order preserved.
type Pair struct {
	Key   string
	Value interface{}
}

// KV is a shorthand constructor for Pair.
func KV(key string, value interface{}) Pair {
	return Pair{Key: key, Value: value}
}

// NewOrderedMap creates a new empty OrderedMap.
func NewOrderedMap() *OrderedMap {
	return &OrderedMap{
		values: make(map[string]interface{}),
	}
}

// NewOrderedMapWithCapacity creates a new empty OrderedMap with preallocated capacity.
func NewOrderedMapWithCapacity(cap int) *OrderedMap {
	return &OrderedMap{
		keys:   make([]string, 0, cap),
		values: make(map[string]interface{}, cap),
	}
}

// NewOrderedMapFromPairs creates an OrderedMap from key-value pairs, preserving the given order.
func NewOrderedMapFromPairs(pairs ...Pair) *OrderedMap {
	om := &OrderedMap{
		keys:   make([]string, 0, len(pairs)),
		values: make(map[string]interface{}, len(pairs)),
	}
	for _, p := range pairs {
		om.Set(p.Key, p.Value)
	}
	return om
}

// OrderedMapFromMap creates an OrderedMap from a plain map.
// Key order is NOT guaranteed since Go maps have undefined iteration order.
// Use this only when insertion order doesn't matter (e.g., for hashing).
func OrderedMapFromMap(m map[string]interface{}) *OrderedMap {
	if m == nil {
		return nil
	}
	om := &OrderedMap{
		keys:   make([]string, 0, len(m)),
		values: make(map[string]interface{}, len(m)),
	}
	for k, v := range m {
		om.keys = append(om.keys, k)
		om.values[k] = v
	}
	return om
}

// Get returns the value associated with the key and whether the key exists.
func (om *OrderedMap) Get(key string) (interface{}, bool) {
	if om == nil {
		return nil, false
	}
	v, ok := om.values[key]
	return v, ok
}

// Set sets the value for a key. If the key is new, it is appended to the end.
// If the key already exists, its value is updated in place without changing order.
func (om *OrderedMap) Set(key string, value interface{}) {
	if om.values == nil {
		om.values = make(map[string]interface{})
	}
	if _, exists := om.values[key]; !exists {
		om.keys = append(om.keys, key)
	}
	om.values[key] = value
}

// Delete removes a key and its value. The key is also removed from the ordered keys list.
func (om *OrderedMap) Delete(key string) {
	if om == nil {
		return
	}
	if _, exists := om.values[key]; !exists {
		return
	}
	delete(om.values, key)
	for i, k := range om.keys {
		if k == key {
			om.keys = append(om.keys[:i], om.keys[i+1:]...)
			break
		}
	}
}

// Len returns the number of key-value pairs.
func (om *OrderedMap) Len() int {
	if om == nil {
		return 0
	}
	return len(om.keys)
}

// Keys returns the keys in insertion order. The returned slice is a copy.
func (om *OrderedMap) Keys() []string {
	if om == nil {
		return nil
	}
	out := make([]string, len(om.keys))
	copy(out, om.keys)
	return out
}

// Range iterates over key-value pairs in insertion order.
// If fn returns false, iteration stops.
func (om *OrderedMap) Range(fn func(key string, value interface{}) bool) {
	if om == nil {
		return
	}
	for _, k := range om.keys {
		if !fn(k, om.values[k]) {
			break
		}
	}
}

// Clone creates a shallow copy of the OrderedMap (keys and top-level values are copied,
// but nested values share references).
func (om *OrderedMap) Clone() *OrderedMap {
	if om == nil {
		return nil
	}
	clone := &OrderedMap{
		keys:   make([]string, len(om.keys)),
		values: make(map[string]interface{}, len(om.values)),
	}
	copy(clone.keys, om.keys)
	for k, v := range om.values {
		clone.values[k] = v
	}
	return clone
}

// ToMap returns a plain map[string]interface{} with the same key-value pairs.
// The returned map does not preserve insertion order.
func (om *OrderedMap) ToMap() map[string]interface{} {
	if om == nil {
		return nil
	}
	m := make(map[string]interface{}, len(om.values))
	for k, v := range om.values {
		m[k] = v
	}
	return m
}

// MarshalJSON serializes the OrderedMap to JSON, preserving insertion order of keys.
// Uses a value receiver so that both OrderedMap and *OrderedMap invoke this method
// (critical for []OrderedMap slices like AnyOf/OneOf/AllOf in ToolFunctionParameters).
func (om OrderedMap) MarshalJSON() ([]byte, error) {
	if om.values == nil {
		return []byte("null"), nil
	}

	var buf bytes.Buffer
	buf.WriteByte('{')

	for i, k := range om.keys {
		if i > 0 {
			buf.WriteByte(',')
		}

		// key
		keyBytes, err := Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(keyBytes)
		buf.WriteByte(':')

		// value — nested *OrderedMap values will use their own MarshalJSON
		valBytes, err := Marshal(om.values[k])
		if err != nil {
			return nil, err
		}
		buf.Write(valBytes)
	}

	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// MarshalSorted serializes the OrderedMap to JSON with keys sorted alphabetically.
// Use this when deterministic output is needed regardless of insertion order (e.g., hashing).
func (om *OrderedMap) MarshalSorted() ([]byte, error) {
	if om == nil {
		return []byte("null"), nil
	}

	keys := make([]string, len(om.keys))
	copy(keys, om.keys)
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteByte('{')

	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}

		keyBytes, err := Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(keyBytes)
		buf.WriteByte(':')

		valBytes, err := Marshal(om.values[k])
		if err != nil {
			return nil, err
		}
		buf.Write(valBytes)
	}

	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// UnmarshalJSON deserializes JSON into the OrderedMap, preserving the key order
// from the JSON document. Nested objects are also deserialized as *OrderedMap.
// Note: uses encoding/json.Decoder (not sonic) because token-by-token decoding
// is required to preserve key order from the JSON document.
func (om *OrderedMap) UnmarshalJSON(data []byte) error {
	// Handle null
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		om.keys = nil
		om.values = nil
		return nil
	}

	dec := json.NewDecoder(bytes.NewReader(data))

	// Read opening brace
	t, err := dec.Token()
	if err != nil {
		return fmt.Errorf("orderedmap: expected '{': %w", err)
	}
	delim, ok := t.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("orderedmap: expected '{', got %v", t)
	}

	om.keys = om.keys[:0]
	if om.values == nil {
		om.values = make(map[string]interface{})
	} else {
		for k := range om.values {
			delete(om.values, k)
		}
	}

	for dec.More() {
		// Read key
		keyToken, err := dec.Token()
		if err != nil {
			return fmt.Errorf("orderedmap: reading key: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("orderedmap: expected string key, got %T", keyToken)
		}

		// Read value, preserving nested object order
		value, err := decodeOrderedValue(dec)
		if err != nil {
			return fmt.Errorf("orderedmap: reading value for key %q: %w", key, err)
		}

		om.Set(key, value)
	}

	// Read closing brace
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("orderedmap: expected '}': %w", err)
	}

	return nil
}

// jsonSchemaPriority maps JSON Schema keywords to their preferred
// serialization position. Keys present in this map are emitted first
// (in the given order), followed by all remaining keys alphabetically.
// This matches the optimal ordering for LLM tool schemas: the model
// sees type and description before properties, constraints, etc.
var jsonSchemaPriority = map[string]int{
	"type":        0,
	"description": 1,
	"properties":  2,
	"required":    3,
}

// SortKeys sorts the keys of this OrderedMap using JSON Schema priority
// ordering (type, description, properties, required first), with remaining
// keys sorted alphabetically. Nested *OrderedMap values are also sorted
// recursively.
func (om *OrderedMap) SortKeys() {
	if om == nil || len(om.keys) == 0 {
		return
	}
	sort.Slice(om.keys, func(i, j int) bool {
		pi, okI := jsonSchemaPriority[om.keys[i]]
		pj, okJ := jsonSchemaPriority[om.keys[j]]
		switch {
		case okI && okJ:
			return pi < pj
		case okI:
			return true
		case okJ:
			return false
		default:
			return om.keys[i] < om.keys[j]
		}
	})
	for k, v := range om.values {
		switch nested := v.(type) {
		case *OrderedMap:
			nested.SortKeys()
		case map[string]interface{}:
			converted := OrderedMapFromMap(nested)
			converted.SortKeys()
			om.values[k] = converted
		case []interface{}:
			sortOrderedMapsInSlice(nested)
		}
	}
}

func sortOrderedMapsInSlice(s []interface{}) {
	for i, item := range s {
		switch v := item.(type) {
		case *OrderedMap:
			v.SortKeys()
		case map[string]interface{}:
			converted := OrderedMapFromMap(v)
			converted.SortKeys()
			s[i] = converted
		case []interface{}:
			sortOrderedMapsInSlice(v)
		}
	}
}

// SortedCopy returns a new OrderedMap with keys sorted using JSON Schema
// priority ordering. Nested *OrderedMap values are recursively copied and
// sorted. Primitive values (strings, numbers, bools) are shared, not cloned.
// This is much cheaper than a full JSON marshal/unmarshal Clone because it
// only allocates new key slices and value maps.
func (om *OrderedMap) SortedCopy() *OrderedMap {
	if om == nil {
		return nil
	}
	if len(om.keys) == 0 {
		return &OrderedMap{values: make(map[string]interface{})}
	}

	newKeys := make([]string, len(om.keys))
	copy(newKeys, om.keys)
	sort.Slice(newKeys, func(i, j int) bool {
		pi, okI := jsonSchemaPriority[newKeys[i]]
		pj, okJ := jsonSchemaPriority[newKeys[j]]
		switch {
		case okI && okJ:
			return pi < pj
		case okI:
			return true
		case okJ:
			return false
		default:
			return newKeys[i] < newKeys[j]
		}
	})

	newValues := make(map[string]interface{}, len(om.values))
	for k, v := range om.values {
		switch nested := v.(type) {
		case *OrderedMap:
			newValues[k] = nested.SortedCopy()
		case map[string]interface{}:
			newValues[k] = OrderedMapFromMap(nested).SortedCopy()
		case []interface{}:
			newValues[k] = sortedCopySlice(nested)
		default:
			newValues[k] = v
		}
	}

	return &OrderedMap{keys: newKeys, values: newValues}
}

func sortedCopySlice(s []interface{}) []interface{} {
	out := make([]interface{}, len(s))
	for i, item := range s {
		switch v := item.(type) {
		case *OrderedMap:
			out[i] = v.SortedCopy()
		case map[string]interface{}:
			out[i] = OrderedMapFromMap(v).SortedCopy()
		case []interface{}:
			out[i] = sortedCopySlice(v)
		default:
			out[i] = item
		}
	}
	return out
}

// SortedCopyPreservingProperties is like SortedCopy but preserves the key
// order of user-defined property names inside "properties" maps. Structural
// JSON Schema keys (type, description, properties, required) are still sorted
// by priority, and all other keys alphabetically. When the key "properties"
// is encountered, its value (an OrderedMap of user-defined field names) has
// its top-level key order preserved while each nested schema value is
// recursively processed with the same property-aware logic.
//
// This ensures deterministic serialization for prompt caching (structural keys
// are always in the same order) while preserving the client's intended field
// generation order for LLM structured output.
func (om *OrderedMap) SortedCopyPreservingProperties() *OrderedMap {
	if om == nil {
		return nil
	}
	if len(om.keys) == 0 {
		return &OrderedMap{values: make(map[string]interface{})}
	}

	newKeys := make([]string, len(om.keys))
	copy(newKeys, om.keys)
	sort.Slice(newKeys, func(i, j int) bool {
		pi, okI := jsonSchemaPriority[newKeys[i]]
		pj, okJ := jsonSchemaPriority[newKeys[j]]
		switch {
		case okI && okJ:
			return pi < pj
		case okI:
			return true
		case okJ:
			return false
		default:
			return newKeys[i] < newKeys[j]
		}
	})

	newValues := make(map[string]interface{}, len(om.values))
	for k, v := range om.values {
		if k == "properties" {
			// User-defined property names: preserve key order, sort nested schemas
			newValues[k] = preserveKeysOrderedCopyWithAwareness(v)
		} else {
			switch nested := v.(type) {
			case *OrderedMap:
				newValues[k] = nested.SortedCopyPreservingProperties()
			case map[string]interface{}:
				newValues[k] = OrderedMapFromMap(nested).SortedCopyPreservingProperties()
			case []interface{}:
				newValues[k] = sortedCopySlicePreservingProperties(nested)
			default:
				newValues[k] = v
			}
		}
	}

	return &OrderedMap{keys: newKeys, values: newValues}
}

// preserveKeysOrderedCopyWithAwareness copies an OrderedMap preserving its
// top-level key order (these are user-defined property names) while
// recursively applying SortedCopyPreservingProperties to each value (each
// value is a schema that may itself contain "properties").
// If the input is not an *OrderedMap, it falls back to SortedCopyPreservingProperties.
func preserveKeysOrderedCopyWithAwareness(v interface{}) interface{} {
	switch om := v.(type) {
	case *OrderedMap:
		return om.preserveKeysWithPropertyAwareness()
	case map[string]interface{}:
		// Plain maps have non-deterministic iteration order in Go;
		// convert and sort since we can't preserve an order that doesn't exist.
		return OrderedMapFromMap(om).SortedCopyPreservingProperties()
	default:
		return v
	}
}

// preserveKeysWithPropertyAwareness preserves the top-level key order of this
// OrderedMap while recursively applying SortedCopyPreservingProperties to each
// nested value.
func (om *OrderedMap) preserveKeysWithPropertyAwareness() *OrderedMap {
	if om == nil {
		return nil
	}
	if len(om.keys) == 0 {
		return &OrderedMap{values: make(map[string]interface{})}
	}

	// Preserve original key order (no sorting)
	newKeys := make([]string, len(om.keys))
	copy(newKeys, om.keys)

	newValues := make(map[string]interface{}, len(om.values))
	for k, v := range om.values {
		switch nested := v.(type) {
		case *OrderedMap:
			newValues[k] = nested.SortedCopyPreservingProperties()
		case map[string]interface{}:
			newValues[k] = OrderedMapFromMap(nested).SortedCopyPreservingProperties()
		case []interface{}:
			newValues[k] = sortedCopySlicePreservingProperties(nested)
		default:
			newValues[k] = v
		}
	}

	return &OrderedMap{keys: newKeys, values: newValues}
}

func sortedCopySlicePreservingProperties(s []interface{}) []interface{} {
	out := make([]interface{}, len(s))
	for i, item := range s {
		switch v := item.(type) {
		case *OrderedMap:
			out[i] = v.SortedCopyPreservingProperties()
		case map[string]interface{}:
			out[i] = OrderedMapFromMap(v).SortedCopyPreservingProperties()
		case []interface{}:
			out[i] = sortedCopySlicePreservingProperties(v)
		default:
			out[i] = item
		}
	}
	return out
}

// decodeOrderedValue reads a single JSON value from the decoder.
// Objects are decoded as *OrderedMap (preserving key order).
// Arrays are decoded as []interface{} with each element recursively decoded.
// Primitives are returned as their Go equivalents.
func decodeOrderedValue(dec *json.Decoder) (interface{}, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}

	switch v := t.(type) {
	case json.Delim:
		if v == '{' {
			// Recursively parse nested object as *OrderedMap
			nested := NewOrderedMap()
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, fmt.Errorf("expected string key, got %T", keyToken)
				}
				val, err := decodeOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				nested.Set(key, val)
			}
			// Consume closing '}'
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return nested, nil
		}
		if v == '[' {
			// Parse array elements recursively
			var arr []interface{}
			for dec.More() {
				val, err := decodeOrderedValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			// Consume closing ']'
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			if arr == nil {
				arr = []interface{}{}
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected delimiter: %v", v)

	case string:
		return v, nil
	case float64:
		return v, nil
	case bool:
		return v, nil
	case nil:
		return nil, nil
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return v.String(), nil
		}
		return f, nil
	default:
		return v, nil
	}
}
