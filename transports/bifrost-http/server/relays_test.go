package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/relay"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/tracing"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/handlers"
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

// At boot the relays come up before the tracer, and a job settled with no tracer never reports its
// cost. So the sweeper waits for the tracer, and starts once Bootstrap has built it.
func TestRelaySweeperWaitsForTheTracer(t *testing.T) {
	ctx := context.Background()
	cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true, Type: configstore.ConfigStoreTypeSQLite,
		Config: &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, noopTestLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close(ctx) })
	ls, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true, Type: logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")},
	}, noopTestLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ls.Close(ctx) })
	config := &lib.Config{ConfigStore: cs, LogsStore: ls}
	plugins := []schemas.BasePlugin{&logging.LoggerPlugin{}}
	config.BasePlugins.Store(&plugins)
	s := &BifrostHTTPServer{Config: config, relays: relay.NewManager(nil, nil)}

	s.startEndpointJobSweeper(nil)
	require.True(t, s.relays.HasJobLookup(), "status checks are confirmed from the start")
	require.Nil(t, s.endpointSweeperCancel, "no sweeper before the tracer exists")

	s.TracingMiddleware = handlers.NewTracingMiddleware(tracing.NewTracer(tracing.NewTraceStore(time.Minute, noopTestLogger{}), nil, noopTestLogger{}))
	s.startEndpointJobSweeper(nil)
	require.NotNil(t, s.endpointSweeperCancel, "the sweeper starts once the tracer is there")
	s.endpointSweeperCancel()
}
