package configstore

import (
	"encoding/json"
)

// marshalToString marshals the given value to a JSON string.
func marshalToString(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// marshalToStringPtr marshals the given value to a JSON string and returns a pointer to the string.
func marshalToStringPtr(v any) (*string, error) {
	if v == nil {
		return nil, nil
	}
	data, err := marshalToString(v)
	if err != nil {
		return nil, err
	}
	return &data, nil
}

// deepCopy creates a deep copy of a given type
func deepCopy[T any](in T) (T, error) {
	var out T
	b, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}
