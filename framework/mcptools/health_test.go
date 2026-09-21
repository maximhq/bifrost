package mcptools

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakePinger struct {
	err        error
	sawContext context.Context
}

func (f *fakePinger) Ping(ctx context.Context) error {
	f.sawContext = ctx
	return f.err
}

func TestGetHealthAllOk(t *testing.T) {
	result, err := runTool(t, "get_health", &Deps{
		ConfigPing: &fakePinger{},
		LogsPing:   &fakePinger{},
		VectorPing: &fakePinger{},
	}, map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, "ok", out["status"])
	require.Equal(t, map[string]string{"config_store": "ok", "log_store": "ok", "vector_store": "ok"}, out["components"])
	require.NotContains(t, out, "errors")
}

// A failing store is named, both per component and in the same errors text
// GET /health uses, so the caller can say which store is down.
func TestGetHealthNamesTheFailingStore(t *testing.T) {
	result, err := runTool(t, "get_health", &Deps{
		ConfigPing: &fakePinger{},
		LogsPing:   &fakePinger{err: errors.New("down")},
		VectorPing: &fakePinger{},
	}, map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, "unavailable", out["status"])
	components := out["components"].(map[string]string)
	require.Equal(t, "ok", components["config_store"])
	require.Equal(t, "error", components["log_store"])
	require.Equal(t, []string{"log store not available"}, out["errors"])
}

func TestGetHealthDisabledSkipsPings(t *testing.T) {
	for name, tc := range map[string]struct {
		deps *Deps
		args map[string]any
	}{
		"deployment setting": {deps: &Deps{DisableDBPings: true}, args: map[string]any{}},
		"skip_pings":         {deps: &Deps{}, args: map[string]any{"skip_pings": true}},
	} {
		t.Run(name, func(t *testing.T) {
			failing := &fakePinger{err: errors.New("should not be called")}
			tc.deps.LogsPing = failing
			result, err := runTool(t, "get_health", tc.deps, tc.args)
			require.NoError(t, err)
			out := result.(map[string]any)
			require.Equal(t, "ok", out["status"])
			require.Equal(t, "disabled", out["components"].(map[string]string)["log_store"])
			require.Nil(t, failing.sawContext)
		})
	}
}

// A store that is not configured is reported as such, not as down and not
// silently dropped from the answer.
func TestGetHealthNotConfigured(t *testing.T) {
	result, err := runTool(t, "get_health", &Deps{}, map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, "ok", out["status"])
	require.Equal(t, map[string]string{"config_store": "not_configured", "log_store": "not_configured", "vector_store": "not_configured"}, out["components"])
}

func TestGetHealthPassesCallerContext(t *testing.T) {
	type scopeKey struct{}
	pinger := &fakePinger{}
	ctx := context.WithValue(context.Background(), scopeKey{}, "caller-scope")
	_, err := runToolCtx(t, ctx, "get_health", &Deps{ConfigPing: pinger}, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, "caller-scope", pinger.sawContext.Value(scopeKey{}))
}

func TestGetVersion(t *testing.T) {
	result, err := runTool(t, "get_version", &Deps{Version: "v1.2.3"}, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, "v1.2.3", result.(map[string]any)["version"])
}

func TestGetVersionUnknownWhenEmpty(t *testing.T) {
	result, err := runTool(t, "get_version", &Deps{}, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, "unknown", result.(map[string]any)["version"])
}
