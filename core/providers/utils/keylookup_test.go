package utils

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func statusErr(code int) *schemas.BifrostError {
	return &schemas.BifrostError{StatusCode: &code, Error: &schemas.ErrorField{Message: "status"}}
}

// TestLookupAcrossKeys pins how a call addressing one stored object walks the keys.
func TestLookupAcrossKeys(t *testing.T) {
	keys := []schemas.Key{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	cases := []struct {
		name      string
		answers   map[string]int // key id -> status; 0 serves the object
		wantIndex int
		wantCode  int
		wantCalls int
	}{
		{"found on a later key", map[string]int{"a": 404, "b": 0, "c": 0}, 1, 0, 2},
		{"found on the first key", map[string]int{"a": 0}, 0, 0, 1},
		{"not found on any key", map[string]int{"a": 404, "b": 404, "c": 404}, -1, 404, 3},
		{"a refused key outranks not found", map[string]int{"a": 404, "b": 401, "c": 404}, -1, 401, 3},
		{"a rate-limited key moves on", map[string]int{"a": 429, "b": 0}, 1, 0, 2},
		{"a server error stops the walk", map[string]int{"a": 404, "b": 500, "c": 0}, 1, 500, 2},
		{"a bad request stops the walk", map[string]int{"a": 400, "b": 0}, 0, 400, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			got, index, err := LookupAcrossKeys(nil, keys, func(k schemas.Key) (string, *schemas.BifrostError) {
				calls++
				if code := tc.answers[k.ID]; code != 0 {
					return "", statusErr(code)
				}
				return k.ID, nil
			})
			if calls != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", calls, tc.wantCalls)
			}
			if tc.wantCode == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err.Error.Message)
				}
				if index != tc.wantIndex || got != keys[tc.wantIndex].ID {
					t.Fatalf("served by %q at %d, want %q at %d", got, index, keys[tc.wantIndex].ID, tc.wantIndex)
				}
				return
			}
			if err == nil || err.StatusCode == nil || *err.StatusCode != tc.wantCode {
				t.Fatalf("error = %+v, want status %d", err, tc.wantCode)
			}
		})
	}
}

// TestBuildNextCursorWithinKey pins that a listing under one object never moves to another key.
func TestBuildNextCursorWithinKey(t *testing.T) {
	if cursor, more := BuildNextCursorWithinKey(2, false, "x"); cursor != "" || more {
		t.Fatalf("exhausted listing returned cursor %q more=%v", cursor, more)
	}
	cursor, more := BuildNextCursorWithinKey(2, true, "file-9")
	if !more {
		t.Fatal("listing with more pages reported none")
	}
	decoded, err := schemas.DecodeSerialCursor(cursor)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.KeyIndex != 2 || decoded.Cursor != "file-9" {
		t.Fatalf("cursor = %+v, want key 2 after file-9", decoded)
	}
}
