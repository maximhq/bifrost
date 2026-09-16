package tables

import (
	"encoding/json"
	"testing"
)

// TestVirtualKeyProviderConfigKeyIDs pins KeyIDs' actual empty-Keys semantics, which the Keys
// field's own comment previously contradicted: AllowAllKeys=false with no Keys rows means no
// keys allowed, not all keys allowed (matching AllowAllKeys' own comment, and the grant layer's
// deny-by-default reading of an empty KeyIDs list).
func TestVirtualKeyProviderConfigKeyIDs(t *testing.T) {
	t.Run("AllowAllKeys true returns the wildcard regardless of Keys", func(t *testing.T) {
		pc := &TableVirtualKeyProviderConfig{AllowAllKeys: true}
		got := pc.KeyIDs()
		if len(got) != 1 || got[0] != "*" {
			t.Fatalf("KeyIDs() = %v, want [\"*\"]", got)
		}
	})

	t.Run("AllowAllKeys false with no Keys means no keys allowed, not all keys", func(t *testing.T) {
		pc := &TableVirtualKeyProviderConfig{AllowAllKeys: false}
		got := pc.KeyIDs()
		if len(got) != 0 {
			t.Fatalf("KeyIDs() = %v, want an empty list (deny-by-default), not the all-keys wildcard", got)
		}
	})

	t.Run("AllowAllKeys false with specific Keys returns exactly those IDs", func(t *testing.T) {
		pc := &TableVirtualKeyProviderConfig{
			AllowAllKeys: false,
			Keys:         []TableKey{{KeyID: "key-1"}, {KeyID: "key-2"}},
		}
		got := pc.KeyIDs()
		if len(got) != 2 || got[0] != "key-1" || got[1] != "key-2" {
			t.Fatalf("KeyIDs() = %v, want [key-1 key-2]", got)
		}
	})
}

// TestVirtualKeyAssignedUserSerialization pins the tri-state assigned_user contract that the
// field's own comment, the TS VirtualKey type and useVirtualKeyUsage all depend on: a resolved
// key carries assigned_user (object or null), and a key whose assignee could not be resolved
// omits the field entirely so the UI can tell "unassigned" from "unknown" and refetch.
func TestVirtualKeyAssignedUserSerialization(t *testing.T) {
	marshalToMap := func(t *testing.T, vk TableVirtualKey) map[string]json.RawMessage {
		t.Helper()
		b, err := json.Marshal(vk)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return m
	}

	t.Run("resolved with an assignee emits the user", func(t *testing.T) {
		m := marshalToMap(t, TableVirtualKey{
			ID:               "vk-1",
			AssigneeResolved: true,
			AssignedUser:     &AssignedUser{ID: "user-1", Name: "Ada", Email: "ada@example.com"},
		})
		raw, ok := m["assigned_user"]
		if !ok {
			t.Fatal("expected assigned_user to be present for a resolved key")
		}
		var got AssignedUser
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal assigned_user: %v", err)
		}
		if got.Email != "ada@example.com" {
			t.Fatalf("assigned_user = %#v, want ada@example.com", got)
		}
	})

	t.Run("resolved with no assignee emits null, not an absent key", func(t *testing.T) {
		m := marshalToMap(t, TableVirtualKey{ID: "vk-1", AssigneeResolved: true})
		raw, ok := m["assigned_user"]
		if !ok {
			t.Fatal("expected assigned_user to be present (null) for a resolved, unassigned key")
		}
		if string(raw) != "null" {
			t.Fatalf("assigned_user = %s, want null", raw)
		}
	})

	t.Run("unresolved omits the key so callers can tell unknown from unassigned", func(t *testing.T) {
		m := marshalToMap(t, TableVirtualKey{ID: "vk-1"})
		if raw, ok := m["assigned_user"]; ok {
			t.Fatalf("expected assigned_user to be absent when unresolved, got %s", raw)
		}
		// The rest of the key must still serialize normally.
		if _, ok := m["id"]; !ok {
			t.Fatal("expected the remaining fields to survive the omission")
		}
	})
}
