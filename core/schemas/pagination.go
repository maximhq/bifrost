package schemas

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// SerialCursor tracks pagination state for serial key exhaustion.
// When paginating across multiple keys, we exhaust all pages from one key
// before moving to the next, ensuring only one API call per pagination request.
type SerialCursor struct {
	Version  int    `json:"v"` // Version for compatibility
	KeyIndex int    `json:"i"` // Current key index in sorted keys array
	Cursor   string `json:"c"` // Native cursor for current key (empty = start fresh)
}

// EncodeSerialCursor encodes a SerialCursor to a base64 string for transport.
func EncodeSerialCursor(cursor *SerialCursor) string {
	if cursor == nil {
		return ""
	}
	data, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.URLEncoding.EncodeToString(data)
}

// DecodeSerialCursor decodes a base64 string back to a SerialCursor.
// Returns (nil, nil) if the encoded string is empty; returns an error for invalid data.
func DecodeSerialCursor(encoded string) (*SerialCursor, error) {
	if encoded == "" {
		return nil, nil
	}

	data, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("failed to decode cursor: %w", err)
	}

	var cursor SerialCursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return nil, fmt.Errorf("failed to unmarshal cursor: %w", err)
	}

	// Validate version
	if cursor.Version != 1 {
		return nil, fmt.Errorf("unsupported cursor version: %d", cursor.Version)
	}

	return &cursor, nil
}

// NewSerialCursor creates a new SerialCursor with version 1.
func NewSerialCursor(keyIndex int, cursor string) *SerialCursor {
	return &SerialCursor{
		Version:  1,
		KeyIndex: keyIndex,
		Cursor:   cursor,
	}
}
