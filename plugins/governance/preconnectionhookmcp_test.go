// PreMCPConnectionHook is the connect-time half of governance's identity work: it stamps the
// context keys that downstream credential resolution reads (virtual key, team, customer) from the
// key the request presented, and does nothing else.
//
// It deliberately does not resolve access. An earlier version did, which made every internal
// connection check (periodic health checks, and the admin verification probes
// VerifyPerUserOAuthConnection/VerifyHeadersConnection use) log a warning about its intentionally
// grant-free context. PR #6706 replaced that with the direct lookup tested here, which returns
// early when no key is present and so has nothing to warn about. These tests pin the stamping,
// which is the whole of the hook's contract.
package governance

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPluginForConnectionHook builds a governance plugin over the given governance config. Callers
// that need no virtual key pass an empty one.
func newPluginForConnectionHook(t *testing.T, config *configstore.GovernanceConfig) *GovernancePlugin {
	t.Helper()
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, config, nil, nil)
	require.NoError(t, err)

	plugin, err := InitFromStore(context.Background(), &Config{
		IsVkMandatory: boolPtr(false),
	}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin
}

// connectCtx is what an MCP connect arrives with: a fresh context carrying at most the key the
// transport authenticated, under the key's own name. No grant, the same as the internal
// health-check and admin-verification probes build.
func connectCtx(virtualKeyValue string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if virtualKeyValue != "" {
		ctx.SetValue(schemas.BifrostContextKeyVirtualKey, virtualKeyValue)
	}
	return ctx
}

func connectReq(clientName string) *schemas.BifrostMCPConnectRequest {
	return &schemas.BifrostMCPConnectRequest{ClientName: clientName}
}

// assertNoIdentityStamped is the shape of "the hook left identity alone": every key it can set is
// still unset, so a later reader cannot mistake a partial stamp for a resolved one.
func assertNoIdentityStamped(t *testing.T, ctx *schemas.BifrostContext) {
	t.Helper()
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyName))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceTeamID))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceTeamName))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceCustomerID))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceCustomerName))
}

// A connect carrying no key at all is the internal probe case: Bifrost's own health checks and
// admin verification build a fresh context with no caller to have a key. There is nothing to stamp
// and nothing to complain about, so the hook passes the request straight through.
func TestPreMCPConnectionHook_NoVirtualKeyStampsNothing(t *testing.T) {
	plugin := newPluginForConnectionHook(t, &configstore.GovernanceConfig{})
	ctx := connectCtx("")

	req := connectReq("sentry")
	got, shortCircuit, err := plugin.PreMCPConnectionHook(ctx, req)

	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	assert.Same(t, req, got, "the request is passed through untouched")
	assertNoIdentityStamped(t, ctx)
}

// A key the store has never heard of leaves identity unset rather than being refused here. Connect
// is transport setup; the per-user auth resolver surfaces the error on the path that needs it, and
// shared-connection auth types never read these keys at all.
func TestPreMCPConnectionHook_UnknownVirtualKeyStampsNothing(t *testing.T) {
	plugin := newPluginForConnectionHook(t, &configstore.GovernanceConfig{})
	ctx := connectCtx("sk-bf-not-a-real-key")

	_, shortCircuit, err := plugin.PreMCPConnectionHook(ctx, connectReq("sentry"))

	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	assertNoIdentityStamped(t, ctx)
}

// The hook's actual job: a key the store knows stamps the identity keys downstream credential
// resolution reads, without resolving access.
func TestPreMCPConnectionHook_KnownVirtualKeyStampsIdentity(t *testing.T) {
	vk := buildVKForMCPStamping(nil)

	plugin := newPluginForConnectionHook(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	})
	ctx := connectCtx(mcpTestVKValue)

	_, shortCircuit, err := plugin.PreMCPConnectionHook(ctx, connectReq("sentry"))

	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	assert.Equal(t, vk.ID, ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID))
	assert.Equal(t, vk.Name, ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyName))
	// A key owned by nobody stamps no owner, rather than stamping an empty one. Names as well as
	// ids: a stamped empty name is the same bug and would otherwise go unseen.
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceTeamID))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceTeamName))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceCustomerID))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceCustomerName))
}

// A key owned by a team stamps the team, and the customer that team belongs to. The customer is
// reached through the team rather than off the key, which is the case a key holding a direct
// customer cannot cover.
func TestPreMCPConnectionHook_TeamOwnedVirtualKeyStampsTeamAndCustomer(t *testing.T) {
	customer := buildCustomer("cust-1", "Acme", nil)
	team := buildTeam("team-1", "Platform", nil)
	team.CustomerID = &customer.ID
	team.Customer = customer
	vk := buildVKForMCPStamping(nil)
	vk.TeamID = &team.ID
	vk.Team = team

	plugin := newPluginForConnectionHook(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Teams:       []configstoreTables.TableTeam{*team},
		Customers:   []configstoreTables.TableCustomer{*customer},
	})
	ctx := connectCtx(mcpTestVKValue)

	_, shortCircuit, err := plugin.PreMCPConnectionHook(ctx, connectReq("sentry"))

	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	assert.Equal(t, vk.ID, ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID))
	assert.Equal(t, "team-1", ctx.Value(schemas.BifrostContextKeyGovernanceTeamID))
	assert.Equal(t, "Platform", ctx.Value(schemas.BifrostContextKeyGovernanceTeamName))
	assert.Equal(t, "cust-1", ctx.Value(schemas.BifrostContextKeyGovernanceCustomerID))
	assert.Equal(t, "Acme", ctx.Value(schemas.BifrostContextKeyGovernanceCustomerName))
}

// A key owned directly by a customer stamps that customer and no team. Team and customer ownership
// are mutually exclusive on a virtual key, so this is the other whole shape.
func TestPreMCPConnectionHook_CustomerOwnedVirtualKeyStampsCustomerOnly(t *testing.T) {
	customer := buildCustomer("cust-2", "Globex", nil)
	vk := buildVKForMCPStamping(nil)
	vk.CustomerID = &customer.ID
	vk.Customer = customer

	plugin := newPluginForConnectionHook(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Customers:   []configstoreTables.TableCustomer{*customer},
	})
	ctx := connectCtx(mcpTestVKValue)

	_, shortCircuit, err := plugin.PreMCPConnectionHook(ctx, connectReq("sentry"))

	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	assert.Equal(t, "cust-2", ctx.Value(schemas.BifrostContextKeyGovernanceCustomerID))
	assert.Equal(t, "Globex", ctx.Value(schemas.BifrostContextKeyGovernanceCustomerName))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceTeamID))
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyGovernanceTeamName))
}
