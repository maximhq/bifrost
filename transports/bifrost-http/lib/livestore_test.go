package lib

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/stretchr/testify/require"
)

// bootLive runs LoadConfig over a config.json holding fields, either with an SQLite config store
// in a fresh directory or with the config store disabled, and closes the config when the test
// ends. fields is a JSON object body without braces; empty means no other settings.
func bootLive(t *testing.T, withStore bool, fields string) *Config {
	t.Helper()
	dir := createTempDir(t)
	store := `"config_store": {"enabled": false}`
	if withStore {
		store = fmt.Sprintf(`"config_store": {"enabled": true, "type": "sqlite", "config": {"path": %q}}`, filepath.Join(dir, "config.db"))
	}
	body := `"$schema": "https://www.getbifrost.ai/schema", ` + store
	if fields != "" {
		body += ", " + fields
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.json"), []byte("{"+body+"}"), 0o600))
	ctx := context.Background()
	config, err := LoadConfig(ctx, dir)
	require.NoError(t, err)
	t.Cleanup(func() { config.Close(ctx) })
	return config
}

// bootBothModes boots the same config.json twice, over a config store and with none, so a test
// can check that both modes put the same records in the live store.
func bootBothModes(t *testing.T, fields string) (withStore, fileOnly *Config) {
	t.Helper()
	return bootLive(t, true, fields), bootLive(t, false, fields)
}

// TestLoadConfigFillsLiveStore checks that LoadConfig leaves a loaded live store in both modes:
// reading the config store when there is one, and with no database otherwise.
func TestLoadConfigFillsLiveStore(t *testing.T) {
	initTestLogger()
	ctx := context.Background()
	withStore, fileOnly := bootBothModes(t, "")

	require.NotNil(t, withStore.LiveStore)
	require.NotNil(t, withStore.LiveStore.Collections)
	require.NotNil(t, fileOnly.LiveStore)
	// A loaded store with a database reaches the kind check; a store with none stops before it.
	require.ErrorContains(t, withStore.LiveStore.RefreshKind(ctx, livestore.Kind("missing")), "unknown kind")
	require.ErrorIs(t, fileOnly.LiveStore.RefreshKind(ctx, livestore.Kind("missing")), livestore.ErrNoDatabase)
}

// TestSetLiveStoreIsTakenOnce checks that LoadConfig loads the store SetLiveStore handed over, and
// that the next LoadConfig creates its own.
func TestSetLiveStoreIsTakenOnce(t *testing.T) {
	initTestLogger()
	preset, err := livestore.New()
	require.NoError(t, err)
	SetLiveStore(preset)
	t.Cleanup(func() { SetLiveStore(nil) })

	first := bootLive(t, true, "")
	require.Same(t, preset, first.LiveStore)
	second := bootLive(t, true, "")
	require.NotSame(t, preset, second.LiveStore)
}
