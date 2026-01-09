//go:build tinygo || wasm

package schemas

import "encoding/json"

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
