package tables

import "testing"

// TestBeforeSave_ProjectScope verifies that a project-scoped model config is accepted by
// BeforeSave. ModelConfigScopeProject is read by the governance store's scope resolution
// (plugins/governance/store.go's modelConfigScopeFor), so it must be a valid, creatable
// scope in its own right, not just a constant referenced for reads.
func TestBeforeSave_ProjectScope(t *testing.T) {
	scopeID := "proj-123"
	mc := &TableModelConfig{
		ID:        "mc-1",
		ModelName: "gpt-4o",
		Scope:     ModelConfigScopeProject,
		ScopeID:   &scopeID,
	}

	if err := mc.BeforeSave(nil); err != nil {
		t.Fatalf("BeforeSave rejected a project-scoped model config: %v", err)
	}
}
