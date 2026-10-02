package configstore

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// newSQLiteRoutingStore opens a fresh sqlite config store in a temp dir.
func newSQLiteRoutingStore(t *testing.T) *RDBConfigStore {
	t.Helper()
	store, err := NewConfigStore(context.Background(), &Config{
		Enabled: true,
		Type:    ConfigStoreTypeSQLite,
		Config:  &SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	rdb, ok := store.(*RDBConfigStore)
	require.True(t, ok)
	return rdb
}

// insertRawRoutingRule writes a routing_rules row the way a version before key-pinned fallbacks
// did: through SQL, with the fallbacks column holding whatever that version stored.
func insertRawRoutingRule(t *testing.T, store *RDBConfigStore, id string, fallbacks *string) {
	t.Helper()
	require.NoError(t, store.DB().Exec(
		`INSERT INTO routing_rules (id, config_hash, name, description, enabled, cel_expression, fallbacks, scope, scope_id, chain_rule, priority, created_at, updated_at)
		 VALUES (?, '', ?, '', 1, 'true', ?, 'global', NULL, 0, 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, id, id, fallbacks).Error)
	require.NoError(t, store.DB().Exec(
		`INSERT INTO routing_targets (rule_id, provider, model, key_id, weight) VALUES (?, 'openai', 'gpt-4o', NULL, 1)`, id).Error)
}

// TestGetRoutingRules_RowsWrittenByPreviousVersion pins backward compatibility for rows the
// previous release stored: legacy string lists, a custom provider that is only registered after
// the rows were decoded (the boot order behind #7538), and empty or NULL columns.
func TestGetRoutingRules_RowsWrittenByPreviousVersion(t *testing.T) {
	const custom = "legacy-row-custom"
	schemas.UnregisterKnownProvider(custom)
	t.Cleanup(func() { schemas.UnregisterKnownProvider(custom) })

	store := newSQLiteRoutingStore(t)
	insertRawRoutingRule(t, store, "strings", bifrost.Ptr(`["anthropic/claude-sonnet-4", "azure/", "`+custom+`/m"]`))
	insertRawRoutingRule(t, store, "null", nil)
	insertRawRoutingRule(t, store, "empty", bifrost.Ptr(""))
	insertRawRoutingRule(t, store, "blank", bifrost.Ptr("   "))
	insertRawRoutingRule(t, store, "empty-list", bifrost.Ptr("[]"))

	byID := func() map[string][]string {
		rules, err := store.GetRoutingRules(context.Background())
		require.NoError(t, err)
		out := map[string][]string{}
		for _, r := range rules {
			var resolved []string
			for _, fb := range r.ParsedFallbacks {
				res := fb.Resolved()
				resolved = append(resolved, string(res.Provider)+"|"+res.Model+"|"+res.KeyID)
			}
			out[r.ID] = resolved
		}
		return out
	}

	// Decoded before the custom provider exists: the custom entry has no provider yet, the rest
	// are already usable, and the column bytes are untouched.
	got := byID()
	require.Equal(t, []string{"anthropic|claude-sonnet-4|", "azure||", "|" + custom + "/m|"}, got["strings"])
	for _, id := range []string{"null", "empty", "blank", "empty-list"} {
		require.Empty(t, got[id], id)
	}

	// Once the provider is registered, the same rows resolve the custom entry without a re-read.
	rules, err := store.GetRoutingRules(context.Background())
	require.NoError(t, err)
	schemas.RegisterKnownProvider(custom)
	for _, r := range rules {
		if r.ID != "strings" {
			continue
		}
		require.Equal(t, custom, string(r.ParsedFallbacks[2].Resolved().Provider))
		require.Equal(t, "m", r.ParsedFallbacks[2].Resolved().Model)
		require.NotNil(t, r.Fallbacks)
		require.Equal(t, `["anthropic/claude-sonnet-4", "azure/", "`+custom+`/m"]`, *r.Fallbacks, "the legacy column must not be rewritten by a read")
	}
}

// docsDowngradeSQL returns the statement published in docs/providers/routing-rules.mdx for the
// given tab, so the test fails if the documented migration and the stored shape drift apart.
func docsDowngradeSQL(t *testing.T, tab string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "providers", "routing-rules.mdx"))
	require.NoError(t, err)
	re := regexp.MustCompile(`(?s)<Tab title="` + tab + `">\s*` + "```sql\n(.*?)```")
	m := re.FindStringSubmatch(string(raw))
	require.Len(t, m, 2, "docs page has no %s downgrade statement", tab)
	return strings.TrimSpace(m[1])
}

// TestRoutingRulesDowngradeSQLite runs the SQLite rollback statement from the docs against rows
// this version writes: every object entry becomes its "provider/model" string, order is kept,
// string entries and rows without objects are left byte-identical.
func TestRoutingRulesDowngradeSQLite(t *testing.T) {
	store := newSQLiteRoutingStore(t)
	insertRawRoutingRule(t, store, "pinned", bifrost.Ptr(`[{"provider":"vertex","model":"gemini-2.5-pro","key_id":"k-1"}]`))
	insertRawRoutingRule(t, store, "mixed", bifrost.Ptr(`["anthropic/claude-sonnet-4",{"provider":"bedrock","model":"","key_id":"k-2"},{"provider":"groq","key_id":"k-3"},"azure/"]`))
	insertRawRoutingRule(t, store, "strings", bifrost.Ptr(`["anthropic/claude-sonnet-4", "azure/"]`))
	insertRawRoutingRule(t, store, "null", nil)

	require.NoError(t, store.DB().Exec(docsDowngradeSQL(t, "SQLite")).Error)

	rules, err := store.GetRoutingRules(context.Background())
	require.NoError(t, err)
	got := map[string]*string{}
	for _, r := range rules {
		got[r.ID] = r.Fallbacks
	}
	require.Equal(t, `["vertex/gemini-2.5-pro"]`, *got["pinned"])
	require.Equal(t, `["anthropic/claude-sonnet-4","bedrock/","groq/","azure/"]`, *got["mixed"])
	require.Equal(t, `["anthropic/claude-sonnet-4", "azure/"]`, *got["strings"], "rows without an object entry must not be touched")
	require.Nil(t, got["null"])

	// The rewritten rows decode as plain legacy strings, which is all a pre-2.2.3 build accepts.
	for _, r := range rules {
		for _, fb := range r.ParsedFallbacks {
			require.False(t, fb.IsKeyPinned(), "%s still carries a pin after downgrade", r.ID)
		}
	}
}
