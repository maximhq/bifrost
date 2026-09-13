package main

import (
	"context"
	"database/sql"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
)

func TestInventoryRecovery(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "partial-pointer-loss"}[lost], func(t *testing.T) {
			source, _ := fixture(t)
			db, e := sql.Open("sqlite3", source)
			require.NoError(t, e)
			_, e = db.Exec(`CREATE TABLE cas_inventories(log_id TEXT PRIMARY KEY,version INTEGER,provenance TEXT,entries TEXT);CREATE TABLE cas_inventory_state(id INTEGER PRIMARY KEY,version INTEGER);INSERT INTO cas_inventory_state VALUES(1,1);INSERT INTO cas_inventories SELECT log_id,1,'native',json_group_object(field,blob_hash) FROM cas_payloads GROUP BY log_id;`)
			require.NoError(t, e)
			if lost {
				_, e = db.Exec("DELETE FROM cas_payloads WHERE log_id='hidden' AND field='input_history'")
				require.NoError(t, e)
			}
			require.NoError(t, db.Close())
			out := filepath.Join(filepath.Dir(source), "restored.db")
			e = restore(context.Background(), source, out)
			if lost {
				require.Error(t, e)
				return
			}
			require.NoError(t, e)
			db, e = sql.Open("sqlite3", out)
			require.NoError(t, e)
			defer db.Close()
			ok, e := verifyInventories(db)
			require.NoError(t, e)
			require.True(t, ok)
			var raw string
			require.NoError(t, db.QueryRow("SELECT entries FROM cas_inventories WHERE log_id='updated'").Scan(&raw))
			require.Equal(t, "{}", raw)
		})
	}
}
