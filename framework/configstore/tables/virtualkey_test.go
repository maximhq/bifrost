package tables

import "testing"

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

// TestVirtualKeyProviderConfigBeforeSaveValidatesModelLists pins that the save hook applies the
// shared list rules to the exact lists and their pattern twins.
func TestVirtualKeyProviderConfigBeforeSaveValidatesModelLists(t *testing.T) {
	ok := &TableVirtualKeyProviderConfig{Provider: "openai", AllowedModels: []string{"gpt-4o"}, AllowedModelsPatterns: []string{"^claude-3-.*"}, BlacklistedModelsPatterns: []string{".*-preview$"}}
	if err := ok.BeforeSave(nil); err != nil {
		t.Fatalf("valid patterns should save: %v", err)
	}

	badAllowed := &TableVirtualKeyProviderConfig{Provider: "openai", AllowedModelsPatterns: []string{"("}}
	if err := badAllowed.BeforeSave(nil); err == nil {
		t.Fatalf("an invalid allowed_models_patterns entry should be rejected")
	}

	badBlocked := &TableVirtualKeyProviderConfig{Provider: "openai", AllowedModels: []string{"*"}, BlacklistedModelsPatterns: []string{""}}
	if err := badBlocked.BeforeSave(nil); err == nil {
		t.Fatalf("an empty blacklisted_models_patterns entry should be rejected")
	}

	wildcard := &TableVirtualKeyProviderConfig{Provider: "openai", AllowedModelsPatterns: []string{"*"}}
	if err := wildcard.BeforeSave(nil); err == nil {
		t.Fatalf("the wildcard is not a pattern")
	}

	// A regex-looking string in the exact list is an ordinary literal and saves fine.
	literal := &TableVirtualKeyProviderConfig{Provider: "openai", AllowedModels: []string{"regex:("}}
	if err := literal.BeforeSave(nil); err != nil {
		t.Fatalf("a regex-looking literal in allowed_models is just a name: %v", err)
	}
}
