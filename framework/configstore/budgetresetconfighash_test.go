package configstore

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyBudgetDigest restates the pre-quarter hashing contract - ID, then the
// marshalled MaxLimit, then ResetDuration - independently of the implementation.
// A budget with no quarter definition must keep producing exactly this digest,
// so an unguarded new field in GenerateBudgetHash fails here rather than
// silently forcing every deployment to resync its budgets on upgrade.
func legacyBudgetDigest(t *testing.T, b tables.TableBudget) string {
	t.Helper()
	h := sha256.New()
	h.Write([]byte(b.ID))
	data, err := sonic.Marshal(b.MaxLimit)
	require.NoError(t, err)
	h.Write(data)
	h.Write([]byte(b.ResetDuration))
	return hex.EncodeToString(h.Sum(nil))
}

// TestGenerateBudgetHashUnchangedForNonQuarterlyBudgets pins the back-compat
// contract: adding the quarter definition must not shift the digest of any
// budget that has none, or every upgraded gateway would see a spurious
// "config.json changed" on first boot.
func TestGenerateBudgetHashUnchangedForNonQuarterlyBudgets(t *testing.T) {
	for _, duration := range []string{"5m", "1h", "1d", "1w", "1M"} {
		budget := tables.TableBudget{ID: "budget-1", MaxLimit: 1000, ResetDuration: duration}
		got, err := GenerateBudgetHash(budget)
		require.NoError(t, err)
		assert.Equal(t, legacyBudgetDigest(t, budget), got, "duration %s", duration)
	}
}

// TestGenerateBudgetHashAgreesAcrossConfigAndDatabaseSources is the test that
// matters most here.
//
// GenerateBudgetHash exists to decide whether a budget in config.json differs
// from the one in the database. The two sources populate the reset config
// differently: a budget parsed from config.json has ResetConfig set and
// ResetConfigJSON still empty, because BeforeSave has not run on it, while a
// budget read back from the database has both. Hashing the raw JSON blob would
// therefore make the two sides disagree for every quarterly budget - not a
// missed change, but a change detected on every single comparison, resyncing
// forever. The hash has to read the effective quarter month instead.
func TestGenerateBudgetHashAgreesAcrossConfigAndDatabaseSources(t *testing.T) {
	fromConfigFile := tables.TableBudget{
		ID:            "budget-quarterly",
		MaxLimit:      5000,
		ResetDuration: "1Q",
		ResetConfig:   &tables.BudgetResetConfig{QuarterStartMonth: int(time.April)},
	}
	fromDatabase := tables.TableBudget{
		ID:              "budget-quarterly",
		MaxLimit:        5000,
		ResetDuration:   "1Q",
		ResetConfig:     &tables.BudgetResetConfig{QuarterStartMonth: int(time.April)},
		ResetConfigJSON: `{"quarter_start_month":4}`,
	}

	configHash, err := GenerateBudgetHash(fromConfigFile)
	require.NoError(t, err)
	dbHash, err := GenerateBudgetHash(fromDatabase)
	require.NoError(t, err)

	assert.Equal(t, configHash, dbHash,
		"a config.json budget and its persisted counterpart must hash identically, or change detection resyncs on every comparison")
}

// TestGenerateBudgetHashTracksQuarterStartMonth verifies the whole point of the
// change: editing the quarter definition in config.json is detected.
func TestGenerateBudgetHashTracksQuarterStartMonth(t *testing.T) {
	april := tables.TableBudget{
		ID: "budget-quarterly", MaxLimit: 5000, ResetDuration: "1Q",
		ResetConfig: &tables.BudgetResetConfig{QuarterStartMonth: int(time.April)},
	}
	october := tables.TableBudget{
		ID: "budget-quarterly", MaxLimit: 5000, ResetDuration: "1Q",
		ResetConfig: &tables.BudgetResetConfig{QuarterStartMonth: int(time.October)},
	}
	unset := tables.TableBudget{ID: "budget-quarterly", MaxLimit: 5000, ResetDuration: "1Q"}
	january := tables.TableBudget{
		ID: "budget-quarterly", MaxLimit: 5000, ResetDuration: "1Q",
		ResetConfig: &tables.BudgetResetConfig{QuarterStartMonth: int(time.January)},
	}

	aprilHash, err := GenerateBudgetHash(april)
	require.NoError(t, err)
	octoberHash, err := GenerateBudgetHash(october)
	require.NoError(t, err)
	unsetHash, err := GenerateBudgetHash(unset)
	require.NoError(t, err)
	januaryHash, err := GenerateBudgetHash(january)
	require.NoError(t, err)

	assert.NotEqual(t, aprilHash, octoberHash, "a quarter-start edit must be detected as a change")
	assert.Equal(t, unsetHash, januaryHash, "an unset quarter start and an explicit January are the same window")
}
