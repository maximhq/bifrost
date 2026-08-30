// PreMCPConnectionHook resolves identity for a real caller's MCP connect, but it is also on the
// path Bifrost's own internal connection checks run through (periodic health checks, and the
// per-call admin verification VerifyPerUserOAuthConnection/VerifyHeadersConnection use to probe a
// client). Those checks build their own context and never carry a grant — that is expected, not a
// wiring fault, and must not warn the way a real ungranted caller request would.
package governance

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/require"
)

// newPluginAndLoggerForConnectionHook builds a governance plugin with no VKs configured (this
// suite never resolves a real permit) and returns the MockLogger alongside it so tests can assert
// on what got logged.
func newPluginAndLoggerForConnectionHook(t *testing.T) (*GovernancePlugin, *MockLogger) {
	t.Helper()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{}, nil, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{
		IsVkMandatory: boolPtr(false),
	}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	// InitFromStore itself warns about the catalogs this test never wires up (cost calculation is
	// not what's under test here); reset so assertions below see only what the hook under test logs.
	logger.warnings = nil
	logger.debugs = nil
	return plugin, logger
}

// ungrantedCtx mirrors what VerifyPerUserOAuthConnection/VerifyHeadersConnection build for an
// admin verification probe: a fresh BifrostContext with no grant ever attached.
func ungrantedCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func connectReq(clientName string) *schemas.BifrostMCPConnectRequest {
	return &schemas.BifrostMCPConnectRequest{ClientName: clientName}
}

// A real caller's connect request reaching PreMCPConnectionHook with no grant at all is a wiring
// fault (the transport should have installed one) and stays at Warn.
func TestPreMCPConnectionHook_NoHealthCheckMarker_NoGrant_Warns(t *testing.T) {
	plugin, logger := newPluginAndLoggerForConnectionHook(t)

	_, shortCircuit, err := plugin.PreMCPConnectionHook(ungrantedCtx(), connectReq("sentry"))

	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	require.Len(t, logger.warnings, 1, "an ungranted connect with no health-check marker is a real wiring fault")
	require.Empty(t, logger.debugs)
}

// Bifrost's own internal connection check marks its context so this exact case can be told apart
// from a real caller's ungranted request. It must not warn.
func TestPreMCPConnectionHook_HealthCheckMarker_NoGrant_DoesNotWarn(t *testing.T) {
	plugin, logger := newPluginAndLoggerForConnectionHook(t)
	ctx := ungrantedCtx()
	ctx.SetValue(schemas.BifrostContextKeyMCPHealthCheckRequest, true)

	_, shortCircuit, err := plugin.PreMCPConnectionHook(ctx, connectReq("sentry"))

	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	require.Empty(t, logger.warnings, "an internal health/admin-verification check has no caller identity by design")
}
