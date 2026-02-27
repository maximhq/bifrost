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
