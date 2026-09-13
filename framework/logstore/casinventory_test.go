package logstore

import (
	"context"
	"gorm.io/gorm"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCas_InventoryDetectsEveryMissingPointer(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		for _, field := range []string{"input_history", "tools", "raw_request"} {
			t.Run(field+map[bool]string{false: "visible", true: "hidden"}[hidden], func(t *testing.T) {
				ctx := context.Background()
				cas, _ := newTestCas(t)
				defer cas.Close(ctx)
				entry := bigChatEntry("inventory", strings.Repeat("context ", 80))
				entry.Tools = `[{"name":"` + strings.Repeat("tool", 80) + `"}]`
				entry.RawRequest = strings.Repeat("raw", 100)
				entry.ContentHidden = hidden
				require.NoError(t, cas.Create(ctx, entry))
				require.NoError(t, cas.db.Where("log_id = ? AND field = ?", entry.ID, field).Delete(&casPayload{}).Error)
				_, err := cas.FindByID(ctx, entry.ID)
				require.Error(t, err, "missing any pointer must fail even when other pointers survive")
				require.Error(t, cas.Update(ctx, entry.ID, map[string]interface{}{"status": "success"}), "update must not bless an incomplete pointer set")
			})
		}
	}
}

func TestCas_InventoryLegacyBootstrapAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	cas, _ := openFaultTestCas(t, path)
	entry := bigChatEntry("legacy", strings.Repeat("legacy context ", 80))
	entry.Tools = strings.Repeat("tools", 80)
	require.NoError(t, cas.Create(ctx, entry))
	require.NoError(t, cas.Create(ctx, &Log{ID: "legacy-empty"}))
	// Simulate pre-inventory schema and historical partial loss. Bootstrap
	// cannot discover that tools used to exist and must never claim otherwise.
	require.NoError(t, cas.db.Where("log_id = ? AND field = ?", entry.ID, "tools").Delete(&casPayload{}).Error)
	require.NoError(t, cas.db.Migrator().DropTable(&CASInventory{}, &casInventoryState{}))
	require.NoError(t, cas.Close(ctx))
	cas, _ = openFaultTestCas(t, path)
	var evidence CASInventory
	require.NoError(t, cas.db.Where("log_id = ?", entry.ID).Take(&evidence).Error)
	require.Equal(t, "legacy_bootstrap", evidence.Provenance)
	require.NoError(t, VerifyCASInventory(cas.db, entry.ID))
	require.NoError(t, VerifyCASInventory(cas.db, "legacy-empty"))
	require.NoError(t, cas.Update(ctx, entry.ID, map[string]interface{}{"tools": strings.Repeat("new tools", 80)}))
	require.NoError(t, cas.db.Where("log_id = ?", entry.ID).Take(&evidence).Error)
	require.Equal(t, "legacy_bootstrap", evidence.Provenance, "updates cannot prove historical completeness")
	require.NoError(t, cas.db.Where("log_id = ?", entry.ID).Delete(&CASInventory{}).Error)
	require.NoError(t, cas.Close(ctx))
	cas, _ = openFaultTestCas(t, path)
	defer cas.Close(ctx)
	require.Error(t, VerifyCASInventory(cas.db, entry.ID), "restart must not recreate deleted evidence")
}

func TestCas_InventoryWriteLifecycleAndRollback(t *testing.T) {
	ctx := context.Background()
	cas, _ := newTestCas(t)
	defer cas.Close(ctx)
	entry := bigChatEntry("lifecycle", strings.Repeat("initial ", 80))
	require.NoError(t, cas.BatchCreateIfNotExists(ctx, []*Log{entry, &Log{ID: "empty"}}))
	require.NoError(t, VerifyCASInventory(cas.db, "empty"))
	var before CASInventory
	require.NoError(t, cas.db.Where("log_id = ?", entry.ID).Take(&before).Error)
	require.Equal(t, "native", before.Provenance)
	require.NoError(t, cas.CreateIfNotExists(ctx, bigChatEntry(entry.ID, strings.Repeat("duplicate", 80))))
	var after CASInventory
	require.NoError(t, cas.db.Where("log_id = ?", entry.ID).Take(&after).Error)
	require.Equal(t, before, after)
	require.NoError(t, cas.db.Exec("CREATE TRIGGER fail_inventory BEFORE UPDATE ON cas_inventories BEGIN SELECT RAISE(ABORT, 'injected inventory failure'); END").Error)
	require.Error(t, cas.Update(ctx, entry.ID, map[string]interface{}{"tools": strings.Repeat("new", 100)}))
	require.NoError(t, VerifyCASInventory(cas.db, entry.ID))
	require.NoError(t, cas.db.Where("log_id = ?", entry.ID).Take(&after).Error)
	require.Equal(t, before, after)
	require.NoError(t, cas.db.Exec("DROP TRIGGER fail_inventory").Error)
	require.NoError(t, cas.Update(ctx, entry.ID, map[string]interface{}{"input_history": "small"}))
	require.NoError(t, VerifyCASInventory(cas.db, entry.ID))
	require.NoError(t, cas.DeleteLog(ctx, entry.ID))
	require.ErrorIs(t, cas.db.Where("log_id = ?", entry.ID).Take(&after).Error, gorm.ErrRecordNotFound)
	require.NoError(t, cas.Create(ctx, bigChatEntry(entry.ID, strings.Repeat("recreated", 80))))
	require.NoError(t, VerifyCASInventory(cas.db, entry.ID))
}

func TestCas_InventoryChecksUnrequestedFieldsAndReplacedPointer(t *testing.T) {
	ctx := context.Background()
	cas, _ := newTestCas(t)
	defer cas.Close(ctx)
	entry := bigChatEntry("projection", strings.Repeat("context", 80))
	entry.Tools = strings.Repeat("tools", 80)
	require.NoError(t, cas.Create(ctx, entry))
	require.NoError(t, cas.db.Model(&casPayload{}).Where("log_id = ? AND field = ?", entry.ID, "tools").Update("blob_hash", "wrong hash").Error)
	var row Log
	require.NoError(t, cas.db.Where("id = ?", entry.ID).Take(&row).Error)
	require.Error(t, cas.hydrateFieldsTx(cas.db, &row, true, "input_history"))
}

func TestCas_InventoryMigrationRollback(t *testing.T) {
	ctx := context.Background()
	cas, _ := newTestCas(t)
	defer cas.Close(ctx)
	require.NoError(t, cas.Create(ctx, &Log{ID: "migration"}))
	require.NoError(t, cas.db.Migrator().DropTable(&CASInventory{}, &casInventoryState{}))
	require.NoError(t, cas.db.AutoMigrate(&CASInventory{}, &casInventoryState{}))
	require.NoError(t, cas.db.Exec("CREATE TRIGGER fail_bootstrap BEFORE INSERT ON cas_inventories BEGIN SELECT RAISE(ABORT, 'injected bootstrap failure'); END").Error)
	require.Error(t, initializeCASInventory(cas.db))
	var count int64
	require.NoError(t, cas.db.Model(&casInventoryState{}).Count(&count).Error)
	require.Zero(t, count, "failed bootstrap must not commit its completion marker")
	require.NoError(t, cas.db.Model(&CASInventory{}).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, cas.db.Exec("DROP TRIGGER fail_bootstrap").Error)
	require.NoError(t, initializeCASInventory(cas.db))
	require.NoError(t, VerifyCASInventory(cas.db, "migration"))
}

func TestCas_InventoryMissingNativeEvidenceCannotBeRepairedByUpdate(t *testing.T) {
	ctx := context.Background()
	cas, _ := newTestCas(t)
	defer cas.Close(ctx)
	require.NoError(t, cas.Create(ctx, &Log{ID: "native-empty"}))
	require.NoError(t, cas.db.Where("log_id = ?", "native-empty").Delete(&CASInventory{}).Error)
	_, err := cas.FindByID(ctx, "native-empty")
	require.Error(t, err)
	require.Error(t, cas.Update(ctx, "native-empty", map[string]interface{}{"tools": strings.Repeat("payload", 80)}))
}
