package vault

import "encoding/json"

// jsonDecoder returns a decode function for the given raw bytes.
// This avoids importing encoding/json directly in client.go.
func jsonDecoder(data []byte) func(v any) error {
	return func(v any) error {
		return json.Unmarshal(data, v)
	}
}
