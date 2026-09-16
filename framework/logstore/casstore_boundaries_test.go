package logstore

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type boundaryValuer string

func (v boundaryValuer) Value() (driver.Value, error) { panic("unsupported Valuer must not execute") }

func boundaryStore(t *testing.T, pg bool) (*CasLogStore, *RDBLogStore) {
	t.Helper()
	if !pg {
		return newTestCas(t)
	}
	require.NotEmpty(t, os.Getenv(previewTestPgDSNEnv), "boundary PostgreSQL tests require disposable PG")
	db := previewTestPgDB(t)
	inner := &RDBLogStore{db: db, logger: hybridTestLogger{}}
	cas, err := newCasLogStore(context.Background(), inner, &ContentAddressedConfig{Enabled: true, MinFieldBytes: 64, MinChunkBytes: 32}, hybridTestLogger{})
	require.NoError(t, err)
	return cas, inner
}

// Snapshot every row and CAS table, including inventories and actual blob bytes.
func boundarySnapshot(t *testing.T, db *gorm.DB) map[string][]map[string]interface{} {
	t.Helper()
	out := map[string][]map[string]interface{}{}
	for table, order := range map[string]string{"logs": "id", "cas_payloads": "log_id, field", "cas_refs": "owner_id, target_id", "cas_blobs": "hash", "cas_inventories": "log_id", "cas_inventory_state": "id"} {
		if !db.Migrator().HasTable(table) {
			continue
		}
		var rows []map[string]interface{}
		require.NoError(t, db.Table(table).Order(order).Find(&rows).Error)
		out[table] = rows
	}
	return out
}

func TestCas_BoundariesSQLite(t *testing.T)   { boundaryCases(t, false) }
func TestCas_BoundariesPostgres(t *testing.T) { boundaryCases(t, true) }

func boundaryCases(t *testing.T, pg bool) {
	cas, inner := boundaryStore(t, pg)
	ctx := context.Background()
	defer cas.Close(ctx)
	require.NoError(t, cas.Create(ctx, previewTestEntry("boundary-update")))
	long := strings.Repeat("a", 2045) + "é€" + strings.Repeat("z", 5000)
	var nilString *string
	values := []struct {
		name  string
		value any
		want  string
		null  bool
	}{
		{"string", long, truncateTag(long, 2048), false}, {"bytes", []byte(long), truncateTag(long, 2048), false},
		{"pointer", &long, truncateTag(long, 2048), false}, {"nil", nil, "", true}, {"typed_nil", nilString, "", true},
		{"nil_bytes", []byte(nil), "", false}, {"empty", "", "", false},
	}
	for _, key := range []string{"content_summary", "ContentSummary"} {
		for _, tc := range values {
			t.Run(key+"/"+tc.name, func(t *testing.T) {
				require.NoError(t, cas.Update(ctx, "boundary-update", map[string]interface{}{key: tc.value}))
				row, err := inner.FindByID(ctx, "boundary-update")
				require.NoError(t, err)
				require.Equal(t, tc.want, row.ContentSummary)
				require.True(t, utf8.ValidString(row.ContentSummary))
				var null bool
				require.NoError(t, cas.db.Raw("SELECT content_summary IS NULL FROM logs WHERE id = ?", "boundary-update").Scan(&null).Error)
				require.Equal(t, tc.null, null)
			})
		}
		for i, value := range []any{gorm.Expr("?", long), boundaryValuer(long), 123, &[]byte{1}, new(any)} {
			t.Run(fmt.Sprintf("reject_%s_%d", key, i), func(t *testing.T) {
				before := boundarySnapshot(t, cas.db)
				require.Error(t, cas.Update(ctx, "boundary-update", map[string]interface{}{key: value, "status": "changed", "output_message": long, "content_hidden": true}))
				require.Equal(t, before, boundarySnapshot(t, cas.db))
			})
		}
	}
	t.Run("duplicate_alias_atomic", func(t *testing.T) {
		before := boundarySnapshot(t, cas.db)
		require.Error(t, cas.Update(ctx, "boundary-update", map[string]interface{}{"content_summary": long, "ContentSummary": long, "status": "changed", "output_message": long}))
		require.Equal(t, before, boundarySnapshot(t, cas.db))
	})
	anon := struct {
		ContentSummary string
		OutputMessage  string
		Status         string
	}{long, long, "changed"}
	m := map[string]interface{}{"content_summary": long}
	var nilLog *Log
	for i, value := range []any{anon, &anon, map[string]string{"content_summary": long, "output_message": long}, &m, nil, nilLog, (*map[string]interface{})(nil), (*struct{ ContentSummary string })(nil)} {
		t.Run(fmt.Sprintf("unsupported_shape_%d", i), func(t *testing.T) {
			before := boundarySnapshot(t, cas.db)
			require.Error(t, cas.Update(ctx, "boundary-update", value))
			require.Equal(t, before, boundarySnapshot(t, cas.db))
		})
	}
	for i, value := range []any{Log{ContentSummary: long}, &Log{ContentSummary: long}, map[string]interface{}(nil)} {
		t.Run(fmt.Sprintf("supported_shape_%d", i), func(t *testing.T) {
			require.NoError(t, cas.Update(ctx, "boundary-update", value))
			row, err := inner.FindByID(ctx, "boundary-update")
			require.NoError(t, err)
			require.LessOrEqual(t, len(row.ContentSummary), 2048)
		})
	}
	t.Run("hidden_clear", func(t *testing.T) {
		require.NoError(t, cas.Update(ctx, "boundary-update", map[string]interface{}{"content_hidden": true, "ContentSummary": &long}))
		for _, tc := range values {
			require.NoError(t, cas.Update(ctx, "boundary-update", map[string]interface{}{"ContentSummary": tc.value}))
			row, err := inner.FindByID(ctx, "boundary-update")
			require.NoError(t, err)
			require.Empty(t, row.ContentSummary)
		}
		before := boundarySnapshot(t, cas.db)
		require.Error(t, cas.Update(ctx, "boundary-update", map[string]interface{}{"ContentSummary": gorm.Expr("?", long), "status": "changed"}))
		require.Equal(t, before, boundarySnapshot(t, cas.db))
	})
	for _, mode := range []string{"high_threshold", "exclude_output"} {
		for _, input := range []string{"empty", "attachment"} {
			for _, method := range []string{"create", "idempotent", "batch"} {
				for _, hidden := range []bool{false, true} {
					name := fmt.Sprintf("empty_preview/%s/%s/%s/hidden_%t", mode, input, method, hidden)
					t.Run(name, func(t *testing.T) {
						oldMin, oldExcluded := cas.minFieldBytes, cas.excluded
						defer func() { cas.minFieldBytes = oldMin; cas.excluded = oldExcluded }()
						if mode == "high_threshold" {
							cas.minFieldBytes = 1 << 20
						} else {
							cas.excluded = map[string]struct{}{"output_message": {}}
						}
						e := previewTestEntry(name)
						e.Timestamp = e.Timestamp.Truncate(time.Microsecond)
						e.ContentHidden = hidden
						e.InputHistoryParsed = []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtr("")}}}
						if input == "attachment" {
							require.NoError(t, json.Unmarshal([]byte(`[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,YWJj"}}]}]`), &e.InputHistoryParsed))
						}
						fixed := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
						if method == "idempotent" {
							e.CreatedAt = fixed
						}
						e.ToolCallNames = []string{"tool_a", "tool_b"}
						e.RoutingEnginesUsed = []string{"route_a"}
						require.NoError(t, e.SerializeFields())
						payload := ExtractPayload(e)
						prepared, err := cas.prepareCreate(e)
						require.NoError(t, err)
						require.Empty(t, prepared.dbEntry.ContentSummary)
						if !hidden {
							hookEntry := prepared.dbEntry
							require.NoError(t, hookEntry.BeforeCreate(cas.db))
							require.Greater(t, len(hookEntry.ContentSummary), 2048, "actual baseline hook rebuilds retained output; intended preview stays empty")
						}
						before := time.Now().UTC()
						switch method {
						case "create":
							err = cas.Create(ctx, e)
						case "idempotent":
							err = cas.CreateIfNotExists(ctx, e)
						case "batch":
							err = cas.BatchCreateIfNotExists(ctx, []*Log{e})
						}
						require.NoError(t, err)
						row, err := inner.FindByID(ctx, e.ID)
						require.NoError(t, err)
						require.Equal(t, prepared.dbEntry.ContentSummary, row.ContentSummary)
						require.Equal(t, row.ContentSummary, e.ContentSummary)
						require.Equal(t, e.Timestamp.UnixNano(), row.Timestamp.UnixNano())
						if method == "idempotent" {
							require.True(t, row.CreatedAt.Equal(fixed))
						} else {
							require.False(t, row.CreatedAt.Before(before.Add(-time.Second)))
							require.False(t, row.CreatedAt.After(time.Now().UTC().Add(time.Second)))
						}
						require.Equal(t, e.ToolCallNamesStr, row.ToolCallNamesStr)
						require.Equal(t, e.RoutingEnginesUsedStr, row.RoutingEnginesUsedStr)
						hydrated, err := cas.FindByID(ctx, e.ID)
						require.NoError(t, err)
						if hidden {
							require.Empty(t, hydrated.InputHistory)
							require.Empty(t, hydrated.OutputMessage)
							require.NoError(t, cas.db.Transaction(func(tx *gorm.DB) error { return cas.hydrateFieldsTx(tx, hydrated, true) }))
						}
						hydrated.Timestamp = hydrated.Timestamp.UTC()
						require.Equal(t, payload, ExtractPayload(hydrated))
						if !hidden {
							require.Equal(t, payload["output_message"], row.OutputMessage)
						} else {
							require.Empty(t, row.OutputMessage)
						}
					})
				}
			}
		}
	}
}
