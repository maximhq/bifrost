package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/maximhq/bifrost/core/relay"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/require"
)

// With logging off the job lookup still goes in, as it reads only the config store, so a job
// started earlier keeps free status checks; nothing settles until logs are back.
func TestRelayJobLookupWithLoggingOff(t *testing.T) {
	ctx := context.Background()
	cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true, Type: configstore.ConfigStoreTypeSQLite,
		Config: &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, noopTestLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close(ctx) })
	s := &BifrostHTTPServer{Config: &lib.Config{ConfigStore: cs}, relays: relay.NewManager(nil, nil)}
	s.startEndpointJobSweeper(nil)
	require.True(t, s.relays.HasJobLookup(), "status checks can still be confirmed")
	require.Nil(t, s.endpointSweeperCancel, "and no sweeper runs without a log store")
}
