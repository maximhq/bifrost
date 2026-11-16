package governance

import (
	"context"
	"sync"
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

// fakeA2AAgents reports the enabled agents to the plugin and counts how often it was asked.
type fakeA2AAgents struct {
	mu     sync.Mutex
	agents map[string]bool
	reads  int
}

// GetEnabledAgents is the plugin's EnabledAgents: the agents enabled now, each with whether it is
// allowed by default.
func (f *fakeA2AAgents) GetEnabledAgents() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	result := make(map[string]bool, len(f.agents))
	for name, allowByDefault := range f.agents {
		result[name] = allowByDefault
	}
	return result
}

// setAgents replaces the enabled agents.
func (f *fakeA2AAgents) setAgents(agents map[string]bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agents = agents
}

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
// with agents as the enabled agents. It returns the harness, so a test can rewrite the key.
func newPluginForA2A(t *testing.T, agents map[string]bool, vk *configstoreTables.TableVirtualKey) (*GovernancePlugin, *liveHarness, *fakeA2AAgents) {
	t.Helper()
	logger := NewMockLogger()
	if vk == nil {
		vk = a2aVK()
	}
	h := newLiveHarness(t, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil)
	store := h.mustGovernanceStore(logger, nil)
	if agents == nil {
		agents = map[string]bool{}
	}
	inMemory := &fakeA2AAgents{agents: agents}
	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false), EnabledAgents: inMemory.GetEnabledAgents}, logger, store, nil, nil, nil, store.live)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin, h, inMemory
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
	p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": false}, a2aVK("alpha"))
	ctx := a2aVKCtx(a2aTestVKValue)
	requireA2AAllowed(t, p, ctx, operationReq("alpha"))
	require.NotNil(t, ctx.Grant().Access())
	assert.True(t, ctx.Grant().Access().IsAgentAllowed("alpha"))
}

// TestPreA2AHook_PreAuthenticatedUserAccess checks that access already settled on the request (a user's profile granting the agent) admits the call.
func TestPreA2AHook_PreAuthenticatedUserAccess(t *testing.T) {
	p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": false}, nil)
	ctx := a2aUserCtx()
	ctx.Grant().SetAccess(grant.NewAccess([]schemas.Permit{
		grant.NewPermit(grant.PermitAccessProfile, "profile-1", "agents", true, false, nil, nil, grant.WithAgentPermits([]string{"alpha"})),
	}, "", nil, "", nil))
	requireA2AAllowed(t, p, ctx, operationReq("alpha"))
}

// TestDefaultAgentPermitDoesNotRestrictLLMRouting checks that the default agent permit admits an A2A call without becoming request access that would narrow LLM routing.
func TestDefaultAgentPermitDoesNotRestrictLLMRouting(t *testing.T) {
	p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": true}, nil)
	ctx := a2aUserCtx()

	access, err := p.ResolveAccess(ctx)
	require.NoError(t, err)
	assert.Nil(t, access)

	p.PublishRoutingAllowlist(ctx, "gpt-4o")
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyRoutingAllowedProviders))

	requireA2AAllowed(t, p, ctx, operationReq("alpha"))
	assert.Nil(t, ctx.Grant().Access(), "A2A-only default access must not mutate shared request access")
}

// TestPreA2AHook_AllowByDefaultForIdentifiedCallers checks that an agent allowed by default admits any caller identified by a key or a user.
func TestPreA2AHook_AllowByDefaultForIdentifiedCallers(t *testing.T) {
	p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": true}, nil)
	requireA2AAllowed(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"))
	requireA2AAllowed(t, p, a2aUserCtx(), operationReq("alpha"))
}

// A user named without a recorded credential, as an Enterprise session can be, is governed as that
// user rather than passed through as anonymous, the same as a user who presented an IdP token.
func TestPreA2AHook_KnownUserWithoutCredentialIsGoverned(t *testing.T) {
	t.Run("no access to a deny-by-default agent", func(t *testing.T) {
		p, _, inMemory := newPluginForA2A(t, map[string]bool{"alpha": false}, nil)
		requireA2ADenied(t, p, a2aKnownUserCtx(), operationReq("alpha"), DecisionAccessNotFound)
		assert.NotZero(t, inMemory.reads, "the agent lookup must run for a known user")
	})
	t.Run("granted by an access profile", func(t *testing.T) {
		p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": false}, nil)
		ctx := a2aKnownUserCtx()
		ctx.Grant().SetAccess(grant.NewAccess([]schemas.Permit{
			grant.NewPermit(grant.PermitAccessProfile, "profile-1", "agents", true, false, nil, nil, grant.WithAgentPermits([]string{"alpha"})),
		}, "", nil, "", nil))
		requireA2AAllowed(t, p, ctx, operationReq("alpha"))
	})
	t.Run("allowed by default", func(t *testing.T) {
		p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": true}, nil)
		requireA2AAllowed(t, p, a2aKnownUserCtx(), operationReq("alpha"))
	})
	t.Run("disabled or unknown agent", func(t *testing.T) {
		p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": true}, nil)
		requireA2ADenied(t, p, a2aKnownUserCtx(), operationReq("ghost"), DecisionAgentBlocked)
	})
}

// TestPreA2AHook_UnresolvedCredentialDoesNotGetAllowByDefault checks that a presented key that resolves to nothing is refused rather than given default agent access.
func TestPreA2AHook_UnresolvedCredentialDoesNotGetAllowByDefault(t *testing.T) {
	p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": true}, nil)
	requireA2ADenied(t, p, a2aVKCtx("sk-bf-unknown"), operationReq("alpha"), DecisionAccessNotFound)
}

// TestPreA2AHook_DirectAssignmentAndDenial checks that a key reaches the agent granted to it and is refused one it was not granted.
func TestPreA2AHook_DirectAssignmentAndDenial(t *testing.T) {
	p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": false, "beta": false}, a2aVK("alpha"))
	requireA2AAllowed(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"))
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("beta"), DecisionAgentBlocked)
}

// TestPreA2AHook_ExpiredPermitDenies checks that an expired key is refused even for an agent it was granted.
func TestPreA2AHook_ExpiredPermitDenies(t *testing.T) {
	vk := a2aVK("alpha")
	expired := time.Now().UTC().Add(-time.Second)
	vk.ExpiresAt = &expired
	p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": false}, vk)
	ctx := a2aVKCtx(a2aTestVKValue)
	requireA2ADenied(t, p, ctx, operationReq("alpha"), DecisionAccessBlocked)
	assert.True(t, ctx.Grant().Access().Bases()[0].IsExpired())
}

// TestPreA2AHook_DisabledOrUnknownAgentDenies checks that an agent not enabled and a name nothing registers are both refused, grant or not.
func TestPreA2AHook_DisabledOrUnknownAgentDenies(t *testing.T) {
	p, _, _ := newPluginForA2A(t, map[string]bool{}, a2aVK("alpha"))
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"), DecisionAgentBlocked)
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("ghost"), DecisionAgentBlocked)
}

// TestPreA2AHook_ActualOperationIsGoverned checks that the hook governs the operation the request carries, not only message sends.
func TestPreA2AHook_ActualOperationIsGoverned(t *testing.T) {
	p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": false}, nil)
	req := &schemas.BifrostA2ARequest{
		RequestType:           schemas.A2ARequestTypeCancelTask,
		AgentName:             "alpha",
		BifrostA2ATaskRequest: &schemas.BifrostA2ATaskRequest{TaskID: "task-1"},
	}
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), req, DecisionAgentBlocked)
}

// TestPreA2AHook_ChangesAreEffectiveOnNextRequest checks that a key's agent grants and the enabled agents are read afresh on every request.
func TestPreA2AHook_ChangesAreEffectiveOnNextRequest(t *testing.T) {
	p, h, inMemory := newPluginForA2A(t, map[string]bool{"alpha": false}, nil)
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"), DecisionAgentBlocked)

	h.putVirtualKey(a2aVK("alpha"))
	requireA2AAllowed(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"))

	h.putVirtualKey(a2aVK())
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"), DecisionAgentBlocked)

	inMemory.setAgents(map[string]bool{"alpha": true})
	requireA2AAllowed(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"))

	inMemory.setAgents(map[string]bool{})
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"), DecisionAgentBlocked)
}

// TestPreA2AHook_AnonymousPassesThroughWithoutAgentLookup checks that an anonymous call passes through without an agent lookup or given access.
func TestPreA2AHook_AnonymousPassesThroughWithoutAgentLookup(t *testing.T) {
	p, _, inMemory := newPluginForA2A(t, map[string]bool{"alpha": true}, nil)
	ctx := anonymousA2ACtx()
	requireA2AAllowed(t, p, ctx, operationReq("alpha"))
	assert.Zero(t, inMemory.reads)
	assert.Nil(t, ctx.Grant().Access())
}

// TestPostA2AHook_PassesThrough checks that the post hook returns the response and error it was given unchanged.
func TestPostA2AHook_PassesThrough(t *testing.T) {
	p, _, _ := newPluginForA2A(t, map[string]bool{}, nil)
	resp := &schemas.BifrostA2AResponse{}
	gotResp, gotErr, err := p.PostA2AHook(anonymousA2ACtx(), resp, nil)
	require.NoError(t, err)
	assert.Same(t, resp, gotResp)
	assert.Nil(t, gotErr)
}
