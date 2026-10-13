package configstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// decodeBackfillBaseURL mirrors how add_ollama_sgl_config_columns reads a
// provider's stored base_url, so the decision the migration makes is testable
// without standing up a database.
func decodeBackfillBaseURL(t *testing.T, networkConfigJSON string) (*schemas.SecretVar, bool) {
	t.Helper()
	var stored struct {
		BaseURL *schemas.SecretVar `json:"base_url,omitempty"`
	}
	if err := json.Unmarshal([]byte(networkConfigJSON), &stored); err != nil {
		return nil, true // the migration logs and skips
	}
	skip := stored.BaseURL == nil || (!stored.BaseURL.IsFromSecret() && strings.TrimSpace(stored.BaseURL.GetValue()) == "")
	return stored.BaseURL, skip
}

// TestBaseURLBackfillMigratesUnresolvedSecretReference pins the decode the
// ollama/sgl backfill makes.
//
// It must not go through NetworkConfig.UnmarshalJSON, which fails loud when a
// reference resolves to an empty value. That is the right stance at serve time,
// where the alternative is dialing an empty host, but inside the migration the
// error becomes a skip: no key created, the old JSON left in place, and only a
// log line to say so. An operator whose base_url is env.OLLAMA_URL, with the
// variable absent from the migration's own environment, would silently keep an
// unmigrated provider.
func TestBaseURLBackfillMigratesUnresolvedSecretReference(t *testing.T) {
	t.Run("a reference migrates on the reference alone, resolved or not", func(t *testing.T) {
		// Deliberately not set in the environment.
		baseURL, skip := decodeBackfillBaseURL(t, `{"base_url":"env.BIFROST_TEST_BACKFILL_UNSET"}`)
		require.False(t, skip, "a present secret reference must migrate even when it currently resolves to nothing")
		require.NotNil(t, baseURL)
		assert.Equal(t, "env.BIFROST_TEST_BACKFILL_UNSET", baseURL.GetRawRef(),
			"the reference itself is what gets carried onto the new key, not its resolved value")

		// The old path is the one that would have skipped it.
		var nc schemas.NetworkConfig
		assert.Error(t, json.Unmarshal([]byte(`{"base_url":"env.BIFROST_TEST_BACKFILL_UNSET"}`), &nc),
			"NetworkConfig.UnmarshalJSON is expected to fail loud here; that is why the migration decodes base_url on its own")
	})

	t.Run("a resolved reference migrates", func(t *testing.T) {
		t.Setenv("BIFROST_TEST_BACKFILL_SET", "http://ollama.internal:11434")
		baseURL, skip := decodeBackfillBaseURL(t, `{"base_url":"env.BIFROST_TEST_BACKFILL_SET"}`)
		require.False(t, skip)
		assert.Equal(t, "http://ollama.internal:11434", baseURL.GetValue())
		assert.Equal(t, "env.BIFROST_TEST_BACKFILL_SET", baseURL.GetRawRef())
	})

	t.Run("a plain url migrates", func(t *testing.T) {
		baseURL, skip := decodeBackfillBaseURL(t, `{"base_url":"http://localhost:11434"}`)
		require.False(t, skip)
		assert.Equal(t, "http://localhost:11434", baseURL.GetValue())
	})

	t.Run("an absent or blank plain url is still skipped", func(t *testing.T) {
		for _, cfg := range []string{`{}`, `{"base_url":""}`, `{"base_url":"   "}`} {
			_, skip := decodeBackfillBaseURL(t, cfg)
			assert.True(t, skip, "nothing worth carrying in %s", cfg)
		}
	})
}

// TestBaseURLBackfillReadBypassesTheAfterFindHook is the end-to-end companion to
// the decode test above, and covers the layer that test missed.
//
// Decoding base_url on its own is not enough on its own: the rows still have to
// be READ. Reading them as []tables.TableProvider runs TableProvider.AfterFind,
// which decodes the whole NetworkConfig through NetworkConfig.UnmarshalJSON --
// the very thing that fails loud on a reference resolving to nothing. That error
// surfaces on the Find call, aborting the migration before the per-row decode
// ever runs, so the careful decode would be dead code behind a failing read.
// The migration therefore reads plain columns, and this pins that.
func TestBaseURLBackfillReadBypassesTheAfterFindHook(t *testing.T) {
	// Deliberately absent from the environment, so the reference resolves to nothing.
	const networkConfigJSON = `{"base_url":"env.BIFROST_TEST_BACKFILL_ABSENT_URL"}`

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&tables.TableProvider{}))
	// Seeded with raw SQL so the write does not go through BeforeSave either: the
	// point is a stored row the hooked read cannot handle.
	require.NoError(t, db.Exec(
		"INSERT INTO config_providers (name, network_config_json, created_at, updated_at) VALUES (?, ?, ?, ?)",
		"ollama", networkConfigJSON, time.Now(), time.Now(),
	).Error)

	t.Run("the hooked read is the hazard", func(t *testing.T) {
		// Not an assertion about desired behaviour -- it records WHY the migration
		// cannot use this read. If NetworkConfig ever stops failing loud here, this
		// leg turns red and the comment above needs revisiting.
		var hooked []tables.TableProvider
		err := db.Where("name IN ?", []string{"ollama", "sgl"}).Find(&hooked).Error
		require.Error(t, err, "AfterFind is expected to reject an unresolvable base_url reference")
	})

	t.Run("the plain-column read the migration uses succeeds", func(t *testing.T) {
		type providerRow struct {
			ID                uint
			Name              string
			NetworkConfigJSON string
		}
		var rows []providerRow
		require.NoError(t, db.Table("config_providers").
			Select("id, name, network_config_json").
			Where("name IN ?", []string{"ollama", "sgl"}).
			Scan(&rows).Error)
		require.Len(t, rows, 1)
		assert.Equal(t, "ollama", rows[0].Name)
		assert.Equal(t, networkConfigJSON, rows[0].NetworkConfigJSON)

		// And the row that read returns is one the backfill migrates.
		baseURL, skip := decodeBackfillBaseURL(t, rows[0].NetworkConfigJSON)
		assert.False(t, skip, "an unresolvable reference must still migrate")
		require.NotNil(t, baseURL)
		assert.Equal(t, "env.BIFROST_TEST_BACKFILL_ABSENT_URL", baseURL.GetRawRef())
	})
}

// TestMigrationAddOllamaSGLConfigColumnsBackfillsReferences runs the registered
// migration itself, which the two tests above deliberately do not.
//
// They cover the decision (through a mirror of the migration's decode) and the
// read (plain columns, so TableProvider.AfterFind stays out of it). Neither one
// calls migrationAddOllamaSGLConfigColumns, so the leg that actually carries the
// endpoint forward -- tx.Create of a key whose OllamaKeyConfig.URL holds the
// REFERENCE rather than its resolved value -- was untested, and the mirror could
// drift from the code it mirrors without anything turning red.
//
// The ollama provider's reference is deliberately absent from the environment:
// that is the case the whole backfill exists for, and the one where a resolved
// value would be an empty string.
func TestMigrationAddOllamaSGLConfigColumnsBackfillsReferences(t *testing.T) {
	const ollamaRef = "env.BIFROST_TEST_BACKFILL_MIGRATION_UNSET"
	const sglLiteral = "http://sgl.internal:30000"

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&tables.TableProvider{}, &tables.TableKey{}))
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)`).Error)

	// AutoMigrate derives config_keys from the current struct, so it already has
	// the two columns this migration adds. Drop them, so the add-column leg runs
	// for real against a table that predates it -- the shape every upgrading
	// deployment actually has.
	for _, col := range []string{"ollama_url", "sgl_url"} {
		require.NoError(t, db.Migrator().DropColumn(&tables.TableKey{}, col))
		require.False(t, db.Migrator().HasColumn(&tables.TableKey{}, col),
			"precondition: %s must be absent before the migration runs", col)
	}

	// Seeded with raw SQL so neither the write nor the read goes through the
	// model hooks, matching how these rows exist in an upgraded database.
	for _, p := range []struct{ name, networkConfigJSON string }{
		{"ollama", `{"base_url":"` + ollamaRef + `"}`},
		{"sgl", `{"base_url":"` + sglLiteral + `"}`},
		{"openai", `{"base_url":"https://api.openai.com"}`}, // not in scope, must not mint a key
	} {
		require.NoError(t, db.Exec(
			"INSERT INTO config_providers (name, network_config_json, created_at, updated_at) VALUES (?, ?, ?, ?)",
			p.name, p.networkConfigJSON, time.Now(), time.Now(),
		).Error)
	}

	require.NoError(t, migrationAddOllamaSGLConfigColumns(context.Background(), db, testMigrationLogger))

	for _, col := range []string{"ollama_url", "sgl_url"} {
		assert.True(t, db.Migrator().HasColumn(&tables.TableKey{}, col), "%s must exist after the migration", col)
	}

	t.Run("an unresolvable ollama reference is carried onto the key as a reference", func(t *testing.T) {
		var key tables.TableKey
		require.NoError(t, db.Where("provider = ?", "ollama").First(&key).Error,
			"the backfill must create a key for the ollama provider")
		require.NotNil(t, key.OllamaKeyConfig, "the generated key must carry an OllamaKeyConfig")
		assert.Equal(t, ollamaRef, key.OllamaKeyConfig.URL.GetRawRef(),
			"the reference itself must be stored, not its resolved value")
		assert.True(t, key.OllamaKeyConfig.URL.IsFromSecret())
		assert.Empty(t, key.OllamaKeyConfig.URL.GetValue(),
			"the variable is deliberately unset, so a resolved value would have been empty -- storing it would have lost the endpoint")
	})

	t.Run("an sgl literal is carried onto the key", func(t *testing.T) {
		var key tables.TableKey
		require.NoError(t, db.Where("provider = ?", "sgl").First(&key).Error)
		require.NotNil(t, key.SGLKeyConfig, "the generated key must carry an SGLKeyConfig")
		assert.Equal(t, sglLiteral, key.SGLKeyConfig.URL.GetValue())
		assert.False(t, key.SGLKeyConfig.URL.IsFromSecret())
	})

	t.Run("providers outside ollama and sgl are left alone", func(t *testing.T) {
		var n int64
		require.NoError(t, db.Model(&tables.TableKey{}).Where("provider = ?", "openai").Count(&n).Error)
		assert.Zero(t, n, "the backfill is scoped to ollama and sgl")
	})

	t.Run("a second run does not mint a duplicate key", func(t *testing.T) {
		// Migrations are replayed on every boot. A second default key per provider
		// is the specific hazard the migration's non-rollbackable Rollback is
		// reasoned from, so it is worth pinning here.
		require.NoError(t, migrationAddOllamaSGLConfigColumns(context.Background(), db, testMigrationLogger))

		var n int64
		require.NoError(t, db.Model(&tables.TableKey{}).Count(&n).Error)
		assert.EqualValues(t, 2, n, "one key for ollama and one for sgl, after two runs")
	})
}
