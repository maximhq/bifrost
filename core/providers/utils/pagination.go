package utils

import (
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// SerialListHelper manages serial key pagination for list operations.
// It ensures that all pages from one key are exhausted before moving to the next,
// guaranteeing only one API call per pagination request regardless of key count.
type SerialListHelper struct {
	Keys   []schemas.Key
	Cursor *schemas.SerialCursor
	Logger schemas.Logger
}

// NewSerialListHelper creates a new SerialListHelper from the provided keys and encoded cursor.
// If the cursor is empty or nil, pagination starts from the first key.
// If the cursor is invalid, an error is returned.
func NewSerialListHelper(keys []schemas.Key, encodedCursor *string, logger schemas.Logger) (*SerialListHelper, error) {
	helper := &SerialListHelper{
		Keys:   keys,
		Logger: logger,
	}

	if encodedCursor != nil && *encodedCursor != "" {
		cursor, err := schemas.DecodeSerialCursor(*encodedCursor)
		if err != nil {
			return nil, err
		}
		helper.Cursor = cursor
	}

	return helper, nil
}

// GetCurrentKey returns the key to query and its native cursor.
// Returns (key, nativeCursor, true) if there's a key to query.
// Returns (Key{}, "", false) if all keys are exhausted.
func (h *SerialListHelper) GetCurrentKey() (schemas.Key, string, bool) {
	if len(h.Keys) == 0 {
		return schemas.Key{}, "", false
	}

	keyIndex := 0
	nativeCursor := ""

	if h.Cursor != nil {
		keyIndex = h.Cursor.KeyIndex
		nativeCursor = h.Cursor.Cursor
	}

	// Check if key index is within bounds
	if keyIndex >= len(h.Keys) {
		return schemas.Key{}, "", false
	}

	return h.Keys[keyIndex], nativeCursor, true
}

// BuildNextCursor creates the cursor for the next pagination request.
// Parameters:
//   - hasMore: whether the current key has more pages
//   - nativeCursor: the native cursor returned by the current key's API
//
// Returns:
//   - encodedCursor: the encoded cursor for the next request (empty if all keys exhausted)
//   - moreAvailable: true if there are more results available (either from current key or remaining keys)
func (h *SerialListHelper) BuildNextCursor(hasMore bool, nativeCursor string) (string, bool) {
	if len(h.Keys) == 0 {
		return "", false
	}

	currentKeyIndex := 0
	if h.Cursor != nil {
		currentKeyIndex = h.Cursor.KeyIndex
	}

	if hasMore {
		// Current key has more pages - return cursor for same key
		nextCursor := schemas.NewSerialCursor(currentKeyIndex, nativeCursor)
		return schemas.EncodeSerialCursor(nextCursor), true
	}

	// Current key exhausted - check if there are more keys
	nextKeyIndex := currentKeyIndex + 1
	if nextKeyIndex >= len(h.Keys) {
		// All keys exhausted
		return "", false
	}

	// Move to next key with empty cursor (start fresh)
	nextCursor := schemas.NewSerialCursor(nextKeyIndex, "")
	return schemas.EncodeSerialCursor(nextCursor), true
}

// GetCurrentKeyIndex returns the current key index being processed.
func (h *SerialListHelper) GetCurrentKeyIndex() int {
	if h.Cursor != nil {
		return h.Cursor.KeyIndex
	}
	return 0
}

// HasMoreKeys returns true if there are more keys after the current one.
func (h *SerialListHelper) HasMoreKeys() bool {
	currentKeyIndex := 0
	if h.Cursor != nil {
		currentKeyIndex = h.Cursor.KeyIndex
	}
	return currentKeyIndex < len(h.Keys)-1
}
