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
