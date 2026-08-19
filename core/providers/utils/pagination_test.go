package utils

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// makeKeys returns a slice of n dummy keys for use in pagination tests.
func makeKeys(n int) []schemas.Key {
	keys := make([]schemas.Key, n)
	for i := range keys {
		keys[i] = schemas.Key{}
	}
	return keys
}

func strPtr(s string) *string { return &s }

// --- SerialCursor encode/decode ---

func TestEncodeDecodeSerialCursor_RoundTrip(t *testing.T) {
	original := schemas.NewSerialCursor(2, "native-abc")
	encoded := schemas.EncodeSerialCursor(original)
	if encoded == "" {
		t.Fatal("expected non-empty encoded cursor")
	}

	decoded, err := schemas.DecodeSerialCursor(encoded)
	if err != nil {
		t.Fatalf("unexpected decode error: %v", err)
	}
	if decoded.KeyIndex != 2 || decoded.Cursor != "native-abc" || decoded.Version != 1 {
		t.Fatalf("decoded cursor mismatch: got %+v", decoded)
	}
}

func TestDecodeSerialCursor_Empty(t *testing.T) {
	cursor, err := schemas.DecodeSerialCursor("")
	if err != nil {
		t.Fatalf("expected nil error for empty string, got: %v", err)
	}
	if cursor != nil {
		t.Fatalf("expected nil cursor for empty string, got: %+v", cursor)
	}
}

func TestDecodeSerialCursor_InvalidBase64(t *testing.T) {
	_, err := schemas.DecodeSerialCursor("not-valid-base64!!!")
	if err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

func TestDecodeSerialCursor_InvalidVersion(t *testing.T) {
	// Manually encode a cursor with version 0.
	bad := &schemas.SerialCursor{Version: 0, KeyIndex: 0, Cursor: "x"}
	encoded := schemas.EncodeSerialCursor(bad)
	_, err := schemas.DecodeSerialCursor(encoded)
	if err == nil {
		t.Fatal("expected error for unsupported cursor version")
	}
}

// --- NewSerialListHelper ---

func TestNewSerialListHelper_NilCursor(t *testing.T) {
	helper, err := NewSerialListHelper(makeKeys(1), nil, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if helper.Cursor != nil {
		t.Fatalf("expected nil cursor, got %+v", helper.Cursor)
	}
}

func TestNewSerialListHelper_EmptyCursor(t *testing.T) {
	helper, err := NewSerialListHelper(makeKeys(1), strPtr(""), nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if helper.Cursor != nil {
		t.Fatalf("expected nil cursor, got %+v", helper.Cursor)
	}
}

func TestNewSerialListHelper_ValidCursor(t *testing.T) {
	encoded := schemas.EncodeSerialCursor(schemas.NewSerialCursor(1, "file-xyz"))
	helper, err := NewSerialListHelper(makeKeys(3), &encoded, nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if helper.Cursor.KeyIndex != 1 || helper.Cursor.Cursor != "file-xyz" {
		t.Fatalf("unexpected cursor: %+v", helper.Cursor)
	}
}

func TestNewSerialListHelper_InvalidCursor_FallbackEnabled_SingleKey(t *testing.T) {
	raw := "string"
	helper, err := NewSerialListHelper(makeKeys(1), &raw, nil, true)
	if err != nil {
		t.Fatalf("expected fallback to succeed, got error: %v", err)
	}
	if helper.Cursor == nil {
		t.Fatal("expected cursor to be set via fallback")
	}
	if helper.Cursor.KeyIndex != 0 || helper.Cursor.Cursor != "string" {
		t.Fatalf("unexpected fallback cursor: %+v", helper.Cursor)
	}
}

func TestNewSerialListHelper_InvalidCursor_FallbackEnabled_MultiKey(t *testing.T) {
	raw := "string"
	_, err := NewSerialListHelper(makeKeys(3), &raw, nil, true)
	if err == nil {
		t.Fatal("expected error for invalid cursor with multiple keys, even with fallback enabled")
	}
}

func TestNewSerialListHelper_InvalidCursor_FallbackDisabled_SingleKey(t *testing.T) {
	raw := "string"
	_, err := NewSerialListHelper(makeKeys(1), &raw, nil, false)
	if err == nil {
		t.Fatal("expected error for invalid cursor with fallback disabled")
	}
}

func TestNewSerialListHelper_InvalidCursor_FallbackDisabled_MultiKey(t *testing.T) {
	raw := "string"
	_, err := NewSerialListHelper(makeKeys(3), &raw, nil, false)
	if err == nil {
		t.Fatal("expected error for invalid cursor with fallback disabled and multiple keys")
	}
}

// --- GetCurrentKey ---

func TestGetCurrentKey_NoCursor_ReturnsFirstKey(t *testing.T) {
	helper, _ := NewSerialListHelper(makeKeys(2), nil, nil, false)
	_, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		t.Fatal("expected a key to be returned")
	}
	if nativeCursor != "" {
		t.Fatalf("expected empty native cursor, got %q", nativeCursor)
	}
	if helper.GetCurrentKeyIndex() != 0 {
		t.Fatalf("expected key index 0, got %d", helper.GetCurrentKeyIndex())
	}
}

func TestGetCurrentKey_WithCursor_ReturnsCorrectKeyAndNativeCursor(t *testing.T) {
	encoded := schemas.EncodeSerialCursor(schemas.NewSerialCursor(1, "native-token"))
	helper, _ := NewSerialListHelper(makeKeys(3), &encoded, nil, false)
	_, nativeCursor, ok := helper.GetCurrentKey()
	if !ok {
		t.Fatal("expected a key to be returned")
	}
	if nativeCursor != "native-token" {
		t.Fatalf("expected native cursor %q, got %q", "native-token", nativeCursor)
	}
}

func TestGetCurrentKey_ExhaustedKeys(t *testing.T) {
	// Cursor pointing past the last key.
	encoded := schemas.EncodeSerialCursor(schemas.NewSerialCursor(5, ""))
	helper, _ := NewSerialListHelper(makeKeys(2), &encoded, nil, false)
	_, _, ok := helper.GetCurrentKey()
	if ok {
		t.Fatal("expected no key when cursor index is out of bounds")
	}
}

func TestGetCurrentKey_EmptyKeyList(t *testing.T) {
	helper, _ := NewSerialListHelper([]schemas.Key{}, nil, nil, false)
	_, _, ok := helper.GetCurrentKey()
	if ok {
		t.Fatal("expected no key for empty key list")
	}
}

// --- BuildNextCursor ---

func TestBuildNextCursor_HasMore_SameKey(t *testing.T) {
	helper, _ := NewSerialListHelper(makeKeys(3), nil, nil, false)
	next, more := helper.BuildNextCursor(true, "file-next")
	if !more {
		t.Fatal("expected more=true")
	}
	if next == "" {
		t.Fatal("expected non-empty cursor")
	}
	decoded, err := schemas.DecodeSerialCursor(next)
	if err != nil {
		t.Fatalf("failed to decode next cursor: %v", err)
	}
	if decoded.KeyIndex != 0 || decoded.Cursor != "file-next" {
		t.Fatalf("unexpected next cursor: %+v", decoded)
	}
}

func TestBuildNextCursor_NoMore_AdvancesToNextKey(t *testing.T) {
	helper, _ := NewSerialListHelper(makeKeys(3), nil, nil, false)
	next, more := helper.BuildNextCursor(false, "")
	if !more {
		t.Fatal("expected more=true when more keys remain")
	}
	decoded, err := schemas.DecodeSerialCursor(next)
	if err != nil {
		t.Fatalf("failed to decode next cursor: %v", err)
	}
	if decoded.KeyIndex != 1 || decoded.Cursor != "" {
		t.Fatalf("expected advance to key 1 with empty cursor, got: %+v", decoded)
	}
}

func TestBuildNextCursor_NoMore_LastKey_ReturnsEmpty(t *testing.T) {
	helper, _ := NewSerialListHelper(makeKeys(1), nil, nil, false)
	next, more := helper.BuildNextCursor(false, "")
	if more {
		t.Fatal("expected more=false when all keys exhausted")
	}
	if next != "" {
		t.Fatalf("expected empty cursor, got %q", next)
	}
}

func TestBuildNextCursor_EmptyKeyList(t *testing.T) {
	helper, _ := NewSerialListHelper([]schemas.Key{}, nil, nil, false)
	next, more := helper.BuildNextCursor(true, "x")
	if more || next != "" {
		t.Fatal("expected empty result for empty key list")
	}
}

// --- HasMoreKeys ---

func TestHasMoreKeys(t *testing.T) {
	helper, _ := NewSerialListHelper(makeKeys(3), nil, nil, false)
	if !helper.HasMoreKeys() {
		t.Fatal("expected HasMoreKeys=true when on key 0 of 3")
	}

	encoded := schemas.EncodeSerialCursor(schemas.NewSerialCursor(2, ""))
	helper2, _ := NewSerialListHelper(makeKeys(3), &encoded, nil, false)
	if helper2.HasMoreKeys() {
		t.Fatal("expected HasMoreKeys=false when on last key")
	}
}
