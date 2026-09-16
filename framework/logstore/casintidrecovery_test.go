package logstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCasIntegerMigrationExitHelper(t *testing.T) {
	if os.Getenv("CAS_INT_MIG_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	db, err := gorm.Open(sqlite.Open(os.Getenv("CAS_INT_MIG_DB")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Callback().Raw().After("gorm:raw").Register("acceptance:migration-exit", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "DROP TABLE cas_refs") && tx.Error == nil {
			os.Exit(93)
		}
	}))
	require.NoError(t, migrationCasIntegerRefIDs(context.Background(), db, hybridTestLogger{}))
	t.Fatal("migration exit hook was not reached")
}

func TestCasIntegerMigrationExitAndTransactionRecovery(t *testing.T) {
	for _, mode := range []string{"exit", "transaction-error", "sqlite-full"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "migration.db")
			cas, inner := openFaultTestCas(t, path)
			entry := bigChatEntry("migration-survivor", strings.Repeat("synthetic migration history ", 100))
			require.NoError(t, cas.Create(ctx, entry))
			before, err := cas.FindByID(ctx, entry.ID)
			require.NoError(t, err)
			downgradeCasRefsToHex(t, cas, true)
			require.NoError(t, cas.db.Exec("DELETE FROM migrations WHERE id='cas_integer_ref_ids_v1'").Error)
			if mode == "exit" {
				require.NoError(t, cas.Close(ctx))
				cmd := exec.Command(os.Args[0], "-test.run=^TestCasIntegerMigrationExitHelper$")
				cmd.Env = append(os.Environ(), "CAS_INT_MIG_HELPER=1", "CAS_INT_MIG_DB="+path)
				output, err := cmd.CombinedOutput()
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit, string(output))
				require.Equal(t, 93, exit.ExitCode(), string(output))
				// Open the raw SQLite connection without CAS initialization, inspect rollback before retry.
				db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
				require.NoError(t, err)
				cas.db = db
				inner.db = db
			} else if mode == "sqlite-full" {
				require.NoError(t, cas.db.Exec("VACUUM").Error)
				var pages int64
				require.NoError(t, cas.db.Raw("PRAGMA page_count").Scan(&pages).Error)
				require.NoError(t, cas.db.Exec(fmt.Sprintf("PRAGMA max_page_count=%d", pages)).Error)
				require.ErrorContains(t, migrationCasIntegerRefIDs(ctx, cas.db, hybridTestLogger{}), "database or disk is full")
				require.NoError(t, cas.db.Exec("PRAGMA max_page_count=1073741823").Error)
			} else {
				require.NoError(t, cas.db.Callback().Raw().After("gorm:raw").Register("acceptance:migration-error", func(tx *gorm.DB) {
					if strings.Contains(tx.Statement.SQL.String(), "DROP TABLE cas_refs") && tx.Error == nil {
						tx.AddError(errors.New("injected migration transaction failure"))
					}
				}))
				require.ErrorContains(t, migrationCasIntegerRefIDs(ctx, cas.db, hybridTestLogger{}), "injected migration transaction failure")
				require.NoError(t, cas.db.Callback().Raw().Remove("acceptance:migration-error"))
			}
			defer cas.Close(ctx)
			hex, err := casRefsHexLayout(cas.db)
			require.NoError(t, err)
			require.True(t, hex)
			var marker, leftover int64
			require.NoError(t, cas.db.Raw("SELECT count(*) FROM migrations WHERE id='cas_integer_ref_ids_v1'").Scan(&marker).Error)
			require.Zero(t, marker)
			require.NoError(t, cas.db.Raw("SELECT count(*) FROM sqlite_master WHERE name IN ('cas_refs_idmig','cas_blobs_idmig')").Scan(&leftover).Error)
			require.Zero(t, leftover)
			require.NoError(t, migrationCasIntegerRefIDs(ctx, cas.db, hybridTestLogger{}))
			after, err := cas.FindByID(ctx, entry.ID)
			require.NoError(t, err)
			require.Equal(t, before.InputHistory, after.InputHistory)
			require.NoError(t, cas.db.Raw("SELECT count(*) FROM migrations WHERE id='cas_integer_ref_ids_v1'").Scan(&marker).Error)
			require.EqualValues(t, 1, marker)
			requireCasGraphReachable(t, cas)
		})
	}
}
