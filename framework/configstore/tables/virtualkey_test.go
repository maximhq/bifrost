package tables

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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

// TestVirtualKeyOwnerMutualExclusion pins that a key belongs to at most one owner. The owner is
// what decides whose money a request spends and whose access profile the key answers to, so a key
// claiming two of them has no answer to either question.
//
// Every pair is exercised rather than just the team/customer one the check originally covered:
// business_unit_id joined team_id and customer_id as an owner, and a pairwise check is exactly the
// kind that leaves a new pair unguarded.
func TestVirtualKeyOwnerMutualExclusion(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&TableVirtualKey{}))

	ptr := func(s string) *string { return &s }
	newKey := func(name string) TableVirtualKey {
		return TableVirtualKey{
			ID:    name,
			Name:  name,
			Value: *schemas.NewSecretVar("sk-bf-" + name),
		}
	}

	t.Run("one owner is allowed", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			apply func(*TableVirtualKey)
		}{
			{"team", func(vk *TableVirtualKey) { vk.TeamID = ptr("team-1") }},
			{"customer", func(vk *TableVirtualKey) { vk.CustomerID = ptr("cust-1") }},
			{"business unit", func(vk *TableVirtualKey) { vk.BusinessUnitID = ptr("bu-1") }},
			{"none", func(vk *TableVirtualKey) {}},
		} {
			vk := newKey("solo-" + tc.name)
			tc.apply(&vk)
			require.NoError(t, db.Create(&vk).Error, "a key owned by %s alone must save", tc.name)
		}
	})

	t.Run("a blank owner id is no owner", func(t *testing.T) {
		// JSON decoding turns "team_id": "" into a pointer to an empty string. Counted as an owner it
		// would store an owner nothing resolves, and would make a body naming one real owner beside a
		// blank one read as two.
		vk := newKey("blank-owner")
		vk.TeamID, vk.CustomerID, vk.BusinessUnitID = ptr(""), ptr("   "), ptr("bu-1")
		require.NoError(t, db.Create(&vk).Error, "blank owner ids must not count as owners")

		var stored TableVirtualKey
		require.NoError(t, db.First(&stored, "id = ?", vk.ID).Error)
		assert.Nil(t, stored.TeamID, "a blank team id is stored as no team")
		assert.Nil(t, stored.CustomerID, "a whitespace customer id is stored as no customer")
		require.NotNil(t, stored.BusinessUnitID)
		assert.Equal(t, "bu-1", *stored.BusinessUnitID, "the one real owner survives")
	})

	t.Run("two owners are rejected", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			apply func(*TableVirtualKey)
		}{
			{"team and customer", func(vk *TableVirtualKey) {
				vk.TeamID, vk.CustomerID = ptr("team-1"), ptr("cust-1")
			}},
			{"team and business unit", func(vk *TableVirtualKey) {
				vk.TeamID, vk.BusinessUnitID = ptr("team-1"), ptr("bu-1")
			}},
			{"customer and business unit", func(vk *TableVirtualKey) {
				vk.CustomerID, vk.BusinessUnitID = ptr("cust-1"), ptr("bu-1")
			}},
			{"all three", func(vk *TableVirtualKey) {
				vk.TeamID, vk.CustomerID, vk.BusinessUnitID = ptr("team-1"), ptr("cust-1"), ptr("bu-1")
			}},
		} {
			vk := newKey("dual-" + tc.name)
			tc.apply(&vk)
			err := db.Create(&vk).Error
			require.Error(t, err, "a key owned by %s must be rejected", tc.name)
			assert.Contains(t, err.Error(), "more than one of team, customer or business unit")
		}
	})
}
