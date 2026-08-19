package schemas

import (
	"bytes"
	"encoding/json"
)

// OptionalJSON tracks whether a JSON field was omitted, null, or set to a value.
type OptionalJSON[T any] struct {
	Set   bool
	Null  bool
	Value T
}

// UnmarshalJSON records field presence and decodes a non-null field value.
func (o *OptionalJSON[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	o.Null = false
	var zero T
	o.Value = zero
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		o.Null = true
		return nil
	}
	return json.Unmarshal(data, &o.Value)
}

// MarshalJSON encodes null for omitted/null fields and the wrapped value otherwise.
func (o OptionalJSON[T]) MarshalJSON() ([]byte, error) {
	if !o.Set || o.Null {
		return []byte("null"), nil
	}
	return Marshal(o.Value)
}

// IsZero reports whether the JSON field was omitted during decoding.
func (o OptionalJSON[T]) IsZero() bool {
	return !o.Set
}
