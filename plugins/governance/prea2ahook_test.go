package governance

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	a2aTestVKValue = "sk-bf-a2a-test"
	a2aTestVKID    = "vk-a2a"
)

// a2aVK builds the test key with one provider config and a grant for each named agent.
func a2aVK(grantedAgents ...string) *configstoreTables.TableVirtualKey {
	vk := buildVirtualKeyWithProviders(a2aTestVKID, a2aTestVKValue, "a2a-vk", []configstoreTables.TableVirtualKeyProviderConfig{
		buildProviderConfig("openai", []string{"*"}),
	})
	for _, agentName := range grantedAgents {
		vk.AgentGrants = append(vk.AgentGrants, configstoreTables.TableVirtualKeyAgentGrant{
			VirtualKeyID: a2aTestVKID,
			AgentName:    agentName,
		})
	}
	return vk
}

// newPluginForA2A builds a plugin over a store holding vk (a key with no agent grants when nil),
// with agents as the enabled agents. It returns the harness, so a test can rewrite the key and the
// agents.
func newPluginForA2A(t *testing.T, agents map[string]bool, vk *configstoreTables.TableVirtualKey) (*GovernancePlugin, *liveHarness) {
	t.Helper()
	logger := NewMockLogger()
	if vk == nil {
		vk = a2aVK()
	}
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, &liveFixture{enabledAgents: agents})
	store := h.mustGovernanceStore(logger, nil)
	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, store.live)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin, h
}

func a2aVKCtx(value string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetGrant(grant.New())
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, value)
	ctx.Grant().SetIdentity(grant.NewIdentity(grant.NewCredential(grant.CredentialVirtualKey, value), nil, nil, nil, nil, nil, nil))
	return ctx
}

func a2aUserCtx() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetGrant(grant.New())
	ctx.Grant().SetIdentity(grant.NewIdentity(
		grant.NewCredential(grant.CredentialIdentityToken, "user-token"),
		&schemas.UserRef{ID: "user-1"}, nil, nil, nil, nil, nil,
	))
	return ctx
}

// a2aKnownUserCtx is the identity a request settles to when its authenticating layer named the user
// but recorded no credential: a user and nothing presented.
func a2aKnownUserCtx() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetGrant(grant.New())
	ctx.Grant().SetIdentity(grant.NewIdentity(schemas.Credential{}, &schemas.UserRef{ID: "user-1"}, nil, nil, nil, nil, nil))
	return ctx
}

func anonymousA2ACtx() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetGrant(grant.New())
	return ctx
}

func operationReq(agentName string) *schemas.BifrostA2ARequest {
	return &schemas.BifrostA2ARequest{RequestType: schemas.A2ARequestTypeSendMessage, AgentName: agentName}
}

func requireA2AAllowed(t *testing.T, p *GovernancePlugin, ctx *schemas.BifrostContext, req *schemas.BifrostA2ARequest) {
	t.Helper()
	_, short, err := p.PreA2AHook(ctx, req)
	require.NoError(t, err)
	assert.Nil(t, short)
}

func requireA2ADenied(t *testing.T, p *GovernancePlugin, ctx *schemas.BifrostContext, req *schemas.BifrostA2ARequest, decision Decision) {
	t.Helper()
	_, short, err := p.PreA2AHook(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, short)
	require.NotNil(t, short.Error)
	assert.True(t, short.Error.IsBifrostError)
	require.NotNil(t, short.Error.StatusCode)
	assert.Equal(t, 403, *short.Error.StatusCode)
	require.NotNil(t, short.Error.Type)
	assert.Equal(t, string(decision), *short.Error.Type)
	assert.Equal(t, true, ctx.Value(governanceRejectedContextKey))
}

// TestPreA2AHook_ExplicitVirtualKeyGrant checks that a key granted an agent may call it and that the grant is recorded on the request.
func TestPreA2AHook_ExplicitVirtualKeyGrant(t *testing.T) {
	p, _ := newPluginForA2A(t, map[string]bool{"alpha": false}, a2aVK("alpha"))
	ctx := a2aVKCtx(a2aTestVKValue)
	requireA2AAllowed(t, p, ctx, operationReq("alpha"))
	require.NotNil(t, ctx.Grant().Access())
	assert.True(t, ctx.Grant().Access().IsAgentAllowed("alpha"))
}

// TestPreA2AHook_PreAuthenticatedUserAccess checks that access already settled on the request (a user's profile granting the agent) admits the call.
func TestPreA2AHook_PreAuthenticatedUserAccess(t *testing.T) {
	p, _ := newPluginForA2A(t, map[string]bool{"alpha": false}, nil)
	ctx := a2aUserCtx()
	ctx.Grant().SetAccess(grant.NewAccess([]schemas.Permit{
		grant.NewPermit(grant.PermitAccessProfile, "profile-1", "agents", true, false, nil, nil, grant.WithAgentPermits([]string{"alpha"})),
	}, "", nil, "", nil))
	requireA2AAllowed(t, p, ctx, operationReq("alpha"))
}

// TestDefaultAgentPermitDoesNotRestrictLLMRouting checks that the default agent permit admits an A2A call without becoming request access that would narrow LLM routing.
func TestDefaultAgentPermitDoesNotRestrictLLMRouting(t *testing.T) {
	p, _ := newPluginForA2A(t, map[string]bool{"alpha": true}, nil)
	ctx := a2aUserCtx()

	access, err := p.ResolveAccess(ctx)
	require.NoError(t, err)
	assert.Nil(t, access)

	p.PublishRoutingAllowlist(ctx, "gpt-4o")
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyRoutingAllowedProviders))

	requireA2AAllowed(t, p, ctx, operationReq("alpha"))
	assert.Nil(t, ctx.Grant().Access(), "A2A-only default access must not mutate shared request access")
}

// TestDefaultAgentPermitNamesOnlyTheRequestedAgent checks that the default agent permit names the
// agent the request asks for when it is enabled and allowed by default, and no other agent; that
// it is still given, naming none, for an agent that is not while another is; and that there is none
// once no enabled agent is allowed by default, a disabled one included.
func TestDefaultAgentPermitNamesOnlyTheRequestedAgent(t *testing.T) {
	p, h := newPluginForA2A(t, map[string]bool{"alpha": true, "beta": true, "gamma": false}, nil)
	identity := a2aUserCtx().Grant().Identity()

	permit := p.defaultAgentPermit(identity, "alpha")
	require.NotNil(t, permit)
	assert.Equal(t, []string{"alpha"}, permit.AgentPermits())
	permit = p.defaultAgentPermit(identity, "gamma")
	require.NotNil(t, permit, "another agent is allowed by default")
	assert.Empty(t, permit.AgentPermits())
	permit = p.defaultAgentPermit(identity, "ghost")
	require.NotNil(t, permit, "another agent is allowed by default")
	assert.Empty(t, permit.AgentPermits())

	// alpha and beta stay allowed by default but are disabled.
	h.setAgents(map[string]bool{"gamma": false})
	assert.Nil(t, p.defaultAgentPermit(identity, "alpha"))
	assert.Nil(t, p.defaultAgentPermit(identity, "gamma"))
}

// TestPreA2AHook_DefaultAgentAccessShapesTheRefusal checks what an agent allowed by default changes
// for a caller refused another agent: while any agent is allowed by default, the caller holds the
// default agent access, so a caller with no access of its own is refused the agent rather than
// refused for holding no access, and the refusal names the default access among the permits that
// refused. With no agent allowed by default, neither happens.
func TestPreA2AHook_DefaultAgentAccessShapesTheRefusal(t *testing.T) {
	refusal := func(t *testing.T, p *GovernancePlugin, ctx *schemas.BifrostContext, agentName string) (string, string) {
		t.Helper()
		_, short, err := p.PreA2AHook(ctx, operationReq(agentName))
		require.NoError(t, err)
		require.NotNil(t, short)
		require.NotNil(t, short.Error)
		require.NotNil(t, short.Error.Type)
		require.NotNil(t, short.Error.Error)
		return *short.Error.Type, short.Error.Error.Message
	}
	t.Run("no access of its own", func(t *testing.T) {
		p, h := newPluginForA2A(t, map[string]bool{"alpha": true, "beta": false}, nil)
		decision, message := refusal(t, p, a2aUserCtx(), "beta")
		assert.Equal(t, string(DecisionAgentBlocked), decision)
		assert.Equal(t, "Agent 'beta' is not allowed for agent_default 'default agent access'", message)

		h.setAgents(map[string]bool{"alpha": false, "beta": false})
		decision, message = refusal(t, p, a2aUserCtx(), "beta")
		assert.Equal(t, string(DecisionAccessNotFound), decision)
		assert.Equal(t, "Access not found", message)
	})
	t.Run("a key granted another agent", func(t *testing.T) {
		p, h := newPluginForA2A(t, map[string]bool{"alpha": true, "beta": false, "gamma": false}, a2aVK("gamma"))
		decision, message := refusal(t, p, a2aVKCtx(a2aTestVKValue), "beta")
		assert.Equal(t, string(DecisionAgentBlocked), decision)
		assert.Equal(t, "Agent 'beta' is not allowed for virtual keys 'a2a-vk', 'default agent access'", message)

		h.setAgents(map[string]bool{"alpha": false, "beta": false, "gamma": false})
		decision, message = refusal(t, p, a2aVKCtx(a2aTestVKValue), "beta")
		assert.Equal(t, string(DecisionAgentBlocked), decision)
		assert.Equal(t, "Agent 'beta' is not allowed for virtual key 'a2a-vk'", message)
	})
}

// TestPreA2AHook_AllowByDefaultForIdentifiedCallers checks that an agent allowed by default admits any caller identified by a key or a user.
func TestPreA2AHook_AllowByDefaultForIdentifiedCallers(t *testing.T) {
	p, _ := newPluginForA2A(t, map[string]bool{"alpha": true}, nil)
	requireA2AAllowed(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"))
	requireA2AAllowed(t, p, a2aUserCtx(), operationReq("alpha"))
}

// A user named without a recorded credential, as an Enterprise session can be, is governed as that
// user rather than passed through as anonymous, the same as a user who presented an IdP token.
func TestPreA2AHook_KnownUserWithoutCredentialIsGoverned(t *testing.T) {
	t.Run("no access to a deny-by-default agent", func(t *testing.T) {
		p, _ := newPluginForA2A(t, map[string]bool{"alpha": false}, nil)
		requireA2ADenied(t, p, a2aKnownUserCtx(), operationReq("alpha"), DecisionAccessNotFound)
	})
	t.Run("granted by an access profile", func(t *testing.T) {
		p, _ := newPluginForA2A(t, map[string]bool{"alpha": false}, nil)
		ctx := a2aKnownUserCtx()
		ctx.Grant().SetAccess(grant.NewAccess([]schemas.Permit{
			grant.NewPermit(grant.PermitAccessProfile, "profile-1", "agents", true, false, nil, nil, grant.WithAgentPermits([]string{"alpha"})),
		}, "", nil, "", nil))
		requireA2AAllowed(t, p, ctx, operationReq("alpha"))
	})
	t.Run("allowed by default", func(t *testing.T) {
		p, _ := newPluginForA2A(t, map[string]bool{"alpha": true}, nil)
		requireA2AAllowed(t, p, a2aKnownUserCtx(), operationReq("alpha"))
	})
	t.Run("disabled or unknown agent", func(t *testing.T) {
		p, _ := newPluginForA2A(t, map[string]bool{"alpha": true}, nil)
		requireA2ADenied(t, p, a2aKnownUserCtx(), operationReq("ghost"), DecisionAgentBlocked)
	})
}

// TestPreA2AHook_UnresolvedCredentialDoesNotGetAllowByDefault checks that a presented key that resolves to nothing is refused rather than given default agent access.
func TestPreA2AHook_UnresolvedCredentialDoesNotGetAllowByDefault(t *testing.T) {
	p, _ := newPluginForA2A(t, map[string]bool{"alpha": true}, nil)
	requireA2ADenied(t, p, a2aVKCtx("sk-bf-unknown"), operationReq("alpha"), DecisionAccessNotFound)
}

// TestPreA2AHook_DirectAssignmentAndDenial checks that a key reaches the agent granted to it and is refused one it was not granted.
func TestPreA2AHook_DirectAssignmentAndDenial(t *testing.T) {
	p, _ := newPluginForA2A(t, map[string]bool{"alpha": false, "beta": false}, a2aVK("alpha"))
	requireA2AAllowed(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"))
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("beta"), DecisionAgentBlocked)
}

// TestPreA2AHook_ExpiredPermitDenies checks that an expired key is refused even for an agent it was granted.
func TestPreA2AHook_ExpiredPermitDenies(t *testing.T) {
	vk := a2aVK("alpha")
	expired := time.Now().UTC().Add(-time.Second)
	vk.ExpiresAt = &expired
	p, _ := newPluginForA2A(t, map[string]bool{"alpha": false}, vk)
	ctx := a2aVKCtx(a2aTestVKValue)
	requireA2ADenied(t, p, ctx, operationReq("alpha"), DecisionAccessBlocked)
	assert.True(t, ctx.Grant().Access().Bases()[0].IsExpired())
}

// TestPreA2AHook_DisabledOrUnknownAgentDenies checks that a disabled registration and a name no registration holds are both refused, grant or not.
func TestPreA2AHook_DisabledOrUnknownAgentDenies(t *testing.T) {
	p, _ := newPluginForA2A(t, map[string]bool{}, a2aVK("alpha"))
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"), DecisionAgentBlocked)
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("ghost"), DecisionAgentBlocked)
}

// TestPreA2AHook_ActualOperationIsGoverned checks that the hook governs the operation the request carries, not only message sends.
func TestPreA2AHook_ActualOperationIsGoverned(t *testing.T) {
	p, _ := newPluginForA2A(t, map[string]bool{"alpha": false}, nil)
	req := &schemas.BifrostA2ARequest{
		RequestType:           schemas.A2ARequestTypeCancelTask,
		AgentName:             "alpha",
		BifrostA2ATaskRequest: &schemas.BifrostA2ATaskRequest{TaskID: "task-1"},
	}
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), req, DecisionAgentBlocked)
}

// TestPreA2AHook_ChangesAreEffectiveOnNextRequest checks that a key's agent grants and the enabled agents are read afresh on every request.
func TestPreA2AHook_ChangesAreEffectiveOnNextRequest(t *testing.T) {
	p, h := newPluginForA2A(t, map[string]bool{"alpha": false}, nil)
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"), DecisionAgentBlocked)

	h.putVirtualKey(a2aVK("alpha"))
	requireA2AAllowed(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"))

	h.putVirtualKey(a2aVK())
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"), DecisionAgentBlocked)

	h.setAgents(map[string]bool{"alpha": true})
	requireA2AAllowed(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"))

	h.setAgents(map[string]bool{})
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"), DecisionAgentBlocked)
}

// TestPreA2AHook_AnonymousPassesThroughWithoutAgentLookup checks that an anonymous call passes through without being checked against the registrations or given access.
func TestPreA2AHook_AnonymousPassesThroughWithoutAgentLookup(t *testing.T) {
	p, _ := newPluginForA2A(t, map[string]bool{"alpha": true}, nil)
	ctx := anonymousA2ACtx()
	requireA2AAllowed(t, p, ctx, operationReq("alpha"))
	// An agent no registration names passes too: an anonymous caller is never checked against the
	// registrations.
	requireA2AAllowed(t, p, anonymousA2ACtx(), operationReq("ghost"))
	assert.Nil(t, ctx.Grant().Access())
}

// TestPostA2AHook_PassesThrough checks that the post hook returns the response and error it was given unchanged.
func TestPostA2AHook_PassesThrough(t *testing.T) {
	p, _ := newPluginForA2A(t, map[string]bool{}, nil)
	resp := &schemas.BifrostA2AResponse{}
	gotResp, gotErr, err := p.PostA2AHook(anonymousA2ACtx(), resp, nil)
	require.NoError(t, err)
	assert.Same(t, resp, gotResp)
	assert.Nil(t, gotErr)
}

// BenchmarkPreA2AHook measures governing one agent call from a user whose access profile grants one
// agent, over 300 enabled agents of which every third is allowed by default: a call to an agent
// allowed by default and a call to the agent the profile grants. Both look the requested agent up
// among the enabled ones and build the default agent access, so the per-request cost of that
// lookup is what the two cases share.
func BenchmarkPreA2AHook(b *testing.B) {
	agents := make(map[string]bool, 300)
	for i := range 300 {
		agents[fmt.Sprintf("agent-%03d", i)] = i%3 == 0
	}
	p := newPluginWithFixture(b, &liveFixture{enabledAgents: agents})
	ctx := a2aUserCtx()
	ctx.Grant().SetAccess(grant.NewAccess([]schemas.Permit{
		grant.NewPermit(grant.PermitAccessProfile, "profile-1", "agents", true, false, nil, nil, grant.WithAgentPermits([]string{"agent-001"})),
	}, "", nil, "", nil))
	for _, tc := range []struct{ name, agent string }{
		{"allowed-by-default", "agent-150"},
		{"granted-by-profile", "agent-001"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			req := operationReq(tc.agent)
			b.ReportAllocs()
			for b.Loop() {
				if _, short, err := p.PreA2AHook(ctx, req); err != nil || short != nil {
					b.Fatalf("PreA2AHook(%s): %v %v", tc.agent, err, short)
				}
			}
		})
	}
}
