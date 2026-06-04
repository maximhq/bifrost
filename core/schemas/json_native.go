//go:build !tinygo && !wasm

package schemas

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/bytedance/sonic"
)

// Marshal encodes v to JSON bytes using the high-performance sonic library.
func Marshal(v interface{}) ([]byte, error) {
	return sonic.Marshal(v)
}

// MarshalString encodes v to a JSON string using sonic.
func MarshalString(v interface{}) (string, error) {
	return sonic.MarshalString(v)
}

// Unmarshal decodes JSON data into v using sonic.
func Unmarshal(data []byte, v interface{}) error {
	return sonic.Unmarshal(data, v)
}

// Compact removes insignificant whitespace from JSON-encoded src
// and appends the result to dst.
func Compact(dst *bytes.Buffer, src []byte) error {
	return json.Compact(dst, src)
}

// MarshalSorted encodes v to JSON with map keys sorted alphabetically.
// Use this when deterministic output is needed (e.g., hashing, caching keys).
// Uses sonic.ConfigStd which has SortMapKeys enabled.
func MarshalSorted(v interface{}) ([]byte, error) {
	return sonic.ConfigStd.Marshal(v)
}

// MarshalSortedIndent encodes v to indented JSON with map keys sorted alphabetically.
func MarshalSortedIndent(v interface{}, prefix, indent string) ([]byte, error) {
	return sonic.ConfigStd.MarshalIndent(v, prefix, indent)
}

// ConvertViaJSON converts src to type T via JSON round-trip using sorted marshaling.
// Use as fallback when direct type assertion fails (e.g., map[string]interface{} from JSON).
func ConvertViaJSON[T any](src interface{}) (T, error) {
	var zero T
	data, err := MarshalSorted(src)
	if err != nil {
		return zero, err
	}
	var result T
	if err := Unmarshal(data, &result); err != nil {
		return zero, err
	}
	return result, nil
}

// MarshalDeeplySorted encodes v to JSON with all map keys sorted alphabetically,
// including nested maps inside OrderedMap and other custom types with MarshalJSON.
// This ensures fully deterministic output for hashing/caching purposes.
//
// Unlike MarshalSorted which relies on sonic's SortMapKeys (which doesn't affect
// types with custom MarshalJSON like OrderedMap), this function first normalizes
// the entire structure to plain maps, then marshals with sorted keys.
func MarshalDeeplySorted(v interface{}) ([]byte, error) {
	normalized := normalizeForSortedMarshal(v)
	return sonic.ConfigStd.Marshal(normalized)
}

// normalizeForSortedMarshal recursively converts OrderedMaps and structs to plain maps
// so that sonic.ConfigStd.Marshal will sort all keys deterministically.
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
		// Intentional round-trip: converts structs with custom MarshalJSON into plain
		// maps so sonic.ConfigStd can sort all keys. Cannot use sjson since input is a Go struct.
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Ptr {
			if rv.IsNil() {
				return nil
			}
			rv = rv.Elem()
		}
		if rv.Kind() == reflect.Struct {
			// Marshal struct to JSON, then unmarshal to map for normalization
			data, err := sonic.Marshal(v)
			if err != nil {
				return v
			}
			var m map[string]interface{}
			if err := sonic.Unmarshal(data, &m); err != nil {
				return v
			}
			// Recursively normalize the resulting map
			return normalizeForSortedMarshal(m)
		}
		return v
	}
}
