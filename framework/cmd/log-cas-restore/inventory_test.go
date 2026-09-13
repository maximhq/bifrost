package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInventoryRecovery(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "partial-pointer-loss"}[lost], func(t *testing.T) {
			source, _ := fixture(t)
			db, e := sql.Open("sqlite3", source)
			require.NoError(t, e)
			defer db.Close()
			// The production fixture creates inventory in the same transactions as CAS.
			// Do not synthesize evidence from the pointers under test.
			ok, e := verifyInventories(db)
			require.NoError(t, e)
			require.True(t, ok)
			var version int
			var provenance, entries string
			require.NoError(t, db.QueryRow("SELECT version,provenance,entries FROM cas_inventories WHERE log_id='hidden'").Scan(&version, &provenance, &entries))
			require.Equal(t, 1, version)
			require.Equal(t, "native", provenance)
			var fields map[string]string
			require.NoError(t, json.Unmarshal([]byte(entries), &fields))
			require.NotEmpty(t, fields["input_history"])
			require.Greater(t, len(fields), 1)
			if lost {
				result, e := db.Exec("DELETE FROM cas_payloads WHERE log_id='hidden' AND field='input_history'")
				require.NoError(t, e)
				affected, e := result.RowsAffected()
				require.NoError(t, e)
				require.EqualValues(t, 1, affected)
				var remaining int
				require.NoError(t, db.QueryRow("SELECT count(*) FROM cas_payloads WHERE log_id='hidden'").Scan(&remaining))
				require.Equal(t, len(fields)-1, remaining)
				require.Positive(t, remaining)
				var unchanged string
				require.NoError(t, db.QueryRow("SELECT entries FROM cas_inventories WHERE log_id='hidden'").Scan(&unchanged))
				require.Equal(t, entries, unchanged)
				_, e = verifyInventories(db)
				require.EqualError(t, e, "CAS inventory pointer missing")
			}
			require.NoError(t, db.Close())
			before, e := hashFile(source)
			require.NoError(t, e)
			out := filepath.Join(filepath.Dir(source), "restored.db")
			e = restore(context.Background(), source, out)
			if lost {
				require.EqualError(t, e, "CAS inventory pointer missing")
				_, e = os.Lstat(out)
				require.True(t, os.IsNotExist(e))
				after, e := hashFile(source)
				require.NoError(t, e)
				require.Equal(t, before, after)
				return
			}
			require.NoError(t, e)
			db, e = sql.Open("sqlite3", out)
			require.NoError(t, e)
			defer db.Close()
			ok, e = verifyInventories(db)
			require.NoError(t, e)
			require.True(t, ok)
			var raw string
			require.NoError(t, db.QueryRow("SELECT entries FROM cas_inventories WHERE log_id='updated'").Scan(&raw))
			require.Equal(t, "{}", raw)
			require.NoError(t, db.QueryRow("SELECT entries FROM cas_inventories WHERE log_id='hidden'").Scan(&raw))
			require.Equal(t, entries, raw)
		})
	}
}
