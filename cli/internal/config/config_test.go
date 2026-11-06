package config

import "testing"

func TestResolveProfileMatchesIDAndName(t *testing.T) {
	state := &State{Profiles: []Profile{{ID: "engineering", Name: "Engineering"}}}
	if got := state.ResolveProfile("engineering"); got == nil || got.ID != "engineering" {
		t.Fatalf("ResolveProfile(id) = %#v", got)
	}
	if got := state.ResolveProfile("ENGINEERING"); got == nil || got.Name != "Engineering" {
		t.Fatalf("ResolveProfile(name) = %#v", got)
	}
	if got := state.ResolveProfile("missing"); got != nil {
		t.Fatalf("ResolveProfile(missing) = %#v", got)
	}
}

func TestDeleteProfileRemovesSelectionAndAdvancesCurrent(t *testing.T) {
	state := &State{
		Profiles:      []Profile{{ID: "one", Name: "One"}, {ID: "two", Name: "Two"}},
		LastProfileID: "one",
		Selections:    map[string]Selection{"one": {Harness: "claude"}, "two": {Harness: "codex"}},
	}
	if !state.DeleteProfile("one") {
		t.Fatal("DeleteProfile() = false")
	}
	if len(state.Profiles) != 1 || state.Profiles[0].ID != "two" || state.LastProfileID != "two" {
		t.Fatalf("state after delete = %#v", state)
	}
	if _, exists := state.Selections["one"]; exists {
		t.Fatalf("deleted profile selection remains: %#v", state.Selections)
	}
	if state.DeleteProfile("missing") {
		t.Fatal("DeleteProfile(missing) = true")
	}
}
