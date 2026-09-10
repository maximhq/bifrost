package lib

// Regression tests for issue #6623: validateBudgetLinkOwnership checked only
// three of the five budget owner columns TableBudget.BeforeSave enforces, so a
// budget already owned by a customer (customer_id) or through a model config's
// BudgetIDs list (the budget's own model_config_id column, written by
// linkModelConfigBudgets) could be linked to a second governance entity at
// config load. The DB layer considers that state invalid; budget accounting
// for both owners then shared one budget row.

import (
	"testing"

	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

func TestValidateBudgetLinkOwnershipCoversAllOwnerColumns(t *testing.T) {
	initTestLogger()
	db := setupTestDB(t)
	if err := db.AutoMigrate(
		&configstoreTables.TableBudget{},
		&configstoreTables.TableModelConfig{},
		&configstoreTables.TableProvider{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	sp := func(s string) *string { return &s }

	t.Run("customer-owned budget is rejected", func(t *testing.T) {
		if err := db.Create(&configstoreTables.TableBudget{
			ID:            "budget-cust",
			MaxLimit:      100,
			ResetDuration: "1d",
			CustomerID:    sp("customer-1"),
		}).Error; err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := validateBudgetLinkOwnership(db, sp("budget-cust"), "provider", "openai"); err == nil {
			t.Error("customer-owned budget must be rejected as already owned")
		}
	})

	t.Run("budget owned via its own model_config_id is rejected", func(t *testing.T) {
		// The linkage linkModelConfigBudgets writes for BudgetIDs lists: the
		// budget's own model_config_id column, with no TableModelConfig.budget_id
		// row pointing back, so the follow-up query alone cannot catch it.
		if err := db.Create(&configstoreTables.TableBudget{
			ID:            "budget-mc",
			MaxLimit:      100,
			ResetDuration: "1d",
			ModelConfigID: sp("mc-1"),
		}).Error; err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := validateBudgetLinkOwnership(db, sp("budget-mc"), "provider", "openai"); err == nil {
			t.Error("budget owned through model_config_id must be rejected as already owned")
		}
	})

	t.Run("model config re-validating its own BudgetIDs link passes", func(t *testing.T) {
		// Config reload re-validates existing links; a model config's own
		// linkage must not self-conflict (mirrors the id <> ? exclusion on the
		// follow-up query).
		if err := db.Create(&configstoreTables.TableBudget{
			ID:            "budget-mc-self",
			MaxLimit:      100,
			ResetDuration: "1d",
			ModelConfigID: sp("mc-self"),
		}).Error; err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := validateBudgetLinkOwnership(db, sp("budget-mc-self"), "model config", "mc-self"); err != nil {
			t.Errorf("self re-validation must pass, got: %v", err)
		}
		if err := validateBudgetLinkOwnership(db, sp("budget-mc-self"), "model config", "mc-other"); err == nil {
			t.Error("another model config linking the same budget must be rejected")
		}
	})

	t.Run("team-owned budget still rejected", func(t *testing.T) {
		if err := db.Create(&configstoreTables.TableBudget{
			ID:            "budget-team",
			MaxLimit:      100,
			ResetDuration: "1d",
			TeamID:        sp("team-1"),
		}).Error; err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := validateBudgetLinkOwnership(db, sp("budget-team"), "provider", "openai"); err == nil {
			t.Error("team-owned budget must be rejected")
		}
	})

	t.Run("unowned budget still links", func(t *testing.T) {
		if err := db.Create(&configstoreTables.TableBudget{
			ID:            "budget-free",
			MaxLimit:      100,
			ResetDuration: "1d",
		}).Error; err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := validateBudgetLinkOwnership(db, sp("budget-free"), "provider", "openai"); err != nil {
			t.Errorf("unowned budget must be linkable, got: %v", err)
		}
	})

	t.Run("reverse model-config direction still caught", func(t *testing.T) {
		if err := db.Create(&configstoreTables.TableModelConfig{
			ID:        "mc-2",
			ModelName: "gpt-4o",
			BudgetID:  sp("budget-mc2"),
		}).Error; err != nil {
			t.Fatalf("create model config: %v", err)
		}
		if err := db.Create(&configstoreTables.TableBudget{
			ID:            "budget-mc2",
			MaxLimit:      100,
			ResetDuration: "1d",
		}).Error; err != nil {
			t.Fatalf("create budget: %v", err)
		}
		if err := validateBudgetLinkOwnership(db, sp("budget-mc2"), "provider", "openai"); err == nil {
			t.Error("budget referenced by a model config's budget_id must be rejected")
		}
	})
}
