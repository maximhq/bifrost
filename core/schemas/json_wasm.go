//go:build tinygo || wasm

package schemas

import (
	"bytes"
	"encoding/json"
)

// Marshal encodes v to JSON bytes using the standard library.
func Marshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

// MarshalString encodes v to a JSON string using the standard library.
func MarshalString(v interface{}) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Unmarshal decodes JSON data into v using the standard library.
func Unmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

// Compact removes insignificant whitespace from JSON-encoded src
// and appends the result to dst.
func Compact(dst *bytes.Buffer, src []byte) error {
	return json.Compact(dst, src)
}

// MarshalSorted encodes v to JSON with map keys sorted alphabetically.
// Use this when deterministic output is needed (e.g., hashing, caching keys).
// Recursively normalizes OrderedMap values to plain maps so json.Marshal sorts keys.
func MarshalSorted(v interface{}) ([]byte, error) {
	normalized := normalizeForSortedMarshal(v)
	return json.Marshal(normalized)
}

// MarshalDeeplySorted encodes v to JSON with all map keys sorted alphabetically,
// including nested maps inside OrderedMap and other custom types with MarshalJSON.
// This ensures fully deterministic output for hashing/caching purposes.
// In WASM builds, this is equivalent to MarshalSorted since both normalize recursively.
func MarshalDeeplySorted(v interface{}) ([]byte, error) {
	normalized := normalizeForSortedMarshal(v)
	return json.Marshal(normalized)
}

// normalizeForSortedMarshal recursively converts OrderedMaps and structs to plain maps
// so that json.Marshal will sort their keys (Go 1.12+ sorts map keys).
func normalizeForSortedMarshal(v interface{}) interface{} {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case *OrderedMap:
		if val == nil {
			return nil
		}
		result := make(map[string]interface{}, val.Len())
		val.Range(func(k string, v interface{}) bool {
			result[k] = normalizeForSortedMarshal(v)
			return true
		})
		return result
	case OrderedMap:
		result := make(map[string]interface{}, val.Len())
		val.Range(func(k string, v interface{}) bool {
			result[k] = normalizeForSortedMarshal(v)
			return true
		})
		return result
	case map[string]interface{}:
		result := make(map[string]interface{}, len(val))
		for k, v := range val {
			result[k] = normalizeForSortedMarshal(v)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, elem := range val {
			result[i] = normalizeForSortedMarshal(elem)
		}
		return result
	default:
		return v
	}
}
