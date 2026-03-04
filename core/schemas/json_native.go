//go:build !tinygo && !wasm

package schemas

import (
	"bytes"
	"encoding/json"

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
