package schemas

import (
	"bytes"
	"encoding/json"
)

// JSONKeyOrder is a lightweight helper that preserves JSON key ordering through
// struct serialization round-trips. Embed it in any struct with `json:"-"` tag.
//
// LLMs are autoregressive sequence models that are sensitive to JSON key ordering
// in tool schemas. This helper ensures that when Bifrost deserializes and
// re-serializes JSON, the original key order from the client is preserved.
//
// Usage:
//
//	type MyStruct struct {
//	    keyOrder JSONKeyOrder `json:"-"`
//	    Field1   string      `json:"field1"`
//	    Field2   string      `json:"field2"`
//	}
//
//	func (s *MyStruct) UnmarshalJSON(data []byte) error {
//	    type Alias MyStruct
//	    if err := Unmarshal(data, (*Alias)(s)); err != nil { return err }
//	    s.keyOrder.Capture(data)
//	    return nil
//	}
//
//	func (s MyStruct) MarshalJSON() ([]byte, error) {
//	    type Alias MyStruct
//	    data, err := Marshal(Alias(s))
//	    if err != nil { return nil, err }
//	    return s.keyOrder.Apply(data)
//	}
type JSONKeyOrder struct {
	keys []string
}

// Capture extracts and stores the top-level key order from raw JSON data.
// Call this at the end of UnmarshalJSON.
func (o *JSONKeyOrder) Capture(data []byte) {
	o.keys = ExtractTopLevelKeyOrder(data)
}

// Apply reorders the keys in serialized JSON to match the captured order.
// If no order was captured (programmatic construction), returns data unchanged.
// Call this at the end of MarshalJSON.
func (o *JSONKeyOrder) Apply(data []byte) ([]byte, error) {
	if len(o.keys) == 0 {
		return data, nil
	}
	return ReorderJSONKeys(data, o.keys)
}

// ExtractTopLevelKeyOrder parses a JSON object and returns its top-level keys in
// document order. Useful for capturing key order before struct deserialization
// loses it, so that re-serialization can preserve the original order.
func ExtractTopLevelKeyOrder(data []byte) []string {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	// Read opening '{'
	if _, err := dec.Token(); err != nil {
		return nil
	}

	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		key, ok := tok.(string)
		if !ok {
			break
		}
		keys = append(keys, key)
		// Skip the value (handles nested objects/arrays)
		if err := skipJSONValue(dec); err != nil {
			break
		}
	}
	return keys
}

// skipJSONValue reads and discards a single JSON value from a decoder.
func skipJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // primitive value, already consumed
	}
	switch delim {
	case '{':
		for dec.More() {
			// skip key
			if _, err := dec.Token(); err != nil {
				return err
			}
			if err := skipJSONValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token() // closing '}'
		return err
	case '[':
		for dec.More() {
			if err := skipJSONValue(dec); err != nil {
				return err
			}
		}
		_, err = dec.Token() // closing ']'
		return err
	}
	return nil
}

// ReorderJSONKeys takes serialized JSON and a desired key order, and returns the
// same JSON with top-level keys reordered. Keys present in `order` are emitted
// first in that order; any remaining keys follow in their original order.
// This is a general-purpose utility for preserving client-specified key order
// through struct serialization/deserialization round-trips.
func ReorderJSONKeys(data []byte, order []string) ([]byte, error) {
	// Parse into key → raw value pairs, preserving original values as-is
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return data, nil
	}

	// Use encoding/json decoder to get raw key-value pairs while preserving order
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if _, err := dec.Token(); err != nil { // '{'
		return data, nil
	}

	type kvPair struct {
		key string
		val json.RawMessage
	}

	var pairs []kvPair
	pairMap := make(map[string]json.RawMessage)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return data, nil
		}
		key, ok := tok.(string)
		if !ok {
			return data, nil
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return data, nil
		}
		pairs = append(pairs, kvPair{key, val})
		pairMap[key] = val
	}

	// Rebuild JSON: first keys from `order`, then remaining keys in original order
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	emitted := make(map[string]bool, len(order))

	for _, key := range order {
		val, exists := pairMap[key]
		if !exists {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		keyBytes, _ := Marshal(key)
		buf.Write(keyBytes)
		buf.WriteByte(':')
		buf.Write(val)
		emitted[key] = true
	}

	// Remaining keys in their original document order
	for _, kv := range pairs {
		if emitted[kv.key] {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		keyBytes, _ := Marshal(kv.key)
		buf.Write(keyBytes)
		buf.WriteByte(':')
		buf.Write(kv.val)
	}

	buf.WriteByte('}')
	return buf.Bytes(), nil
}
