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

// TestAfterFind_StampsCalendarAlignment verifies that reading a model config stamps its
// calendar_aligned setting onto its rate limit as well as its budgets, as TableTeam and
// TableVirtualKey do, so the rate-limit reset path and the quota response follow the
// same windows. A stale stamp is cleared when the config is not aligned.
func TestAfterFind_StampsCalendarAlignment(t *testing.T) {
	for _, aligned := range []bool{true, false} {
		mc := &TableModelConfig{
			ID:              "mc-1",
			ModelName:       "gpt-4o",
			CalendarAligned: aligned,
			Budgets:         []TableBudget{{ID: "b-1", IsCalendarAligned: !aligned}},
			RateLimit:       &TableRateLimit{ID: "rl-1", IsCalendarAligned: !aligned},
		}
		if err := mc.AfterFind(nil); err != nil {
			t.Fatalf("AfterFind returned an error: %v", err)
		}
		if mc.Budgets[0].IsCalendarAligned != aligned {
			t.Errorf("calendar_aligned=%v: budget IsCalendarAligned = %v", aligned, mc.Budgets[0].IsCalendarAligned)
		}
		if mc.RateLimit.IsCalendarAligned != aligned {
			t.Errorf("calendar_aligned=%v: rate limit IsCalendarAligned = %v", aligned, mc.RateLimit.IsCalendarAligned)
		}
	}

	// A model config without a rate limit still reads cleanly.
	mc := &TableModelConfig{ID: "mc-2", ModelName: "gpt-4o", CalendarAligned: true}
	if err := mc.AfterFind(nil); err != nil {
		t.Fatalf("AfterFind without a rate limit returned an error: %v", err)
	}
}
