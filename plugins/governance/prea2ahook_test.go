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

type fakeA2AInMemoryStore struct {
	mu     sync.Mutex
	agents map[string]bool
	reads  int
}

func (f *fakeA2AInMemoryStore) GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig {
	return nil
}
func (f *fakeA2AInMemoryStore) GetConfiguredProviderNames() []string             { return nil }
func (f *fakeA2AInMemoryStore) GetMCPClientsAllowedByDefault() map[string]string { return nil }
func (f *fakeA2AInMemoryStore) GetMCPClientNames() map[string]string             { return nil }
func (f *fakeA2AInMemoryStore) GetMCPClientBySlug(string) (string, string, bool) {
	return "", "", false
}
func (f *fakeA2AInMemoryStore) GetEnabledAgents() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	result := make(map[string]bool, len(f.agents))
	for name, allowByDefault := range f.agents {
		result[name] = allowByDefault
	}
	return result
}
func (f *fakeA2AInMemoryStore) setAgents(agents map[string]bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agents = agents
}

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

func newPluginForA2A(t *testing.T, agents map[string]bool, vk *configstoreTables.TableVirtualKey) (*GovernancePlugin, *LocalGovernanceStore, *fakeA2AInMemoryStore) {
	t.Helper()
	logger := NewMockLogger()
	if vk == nil {
		vk = a2aVK()
	}
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, nil)
	require.NoError(t, err)
	if agents == nil {
		agents = map[string]bool{}
	}
	inMemory := &fakeA2AInMemoryStore{agents: agents}
	plugin, err := InitFromStore(context.Background(), &Config{IsVkMandatory: boolPtr(false)}, logger, store, nil, nil, nil, nil)
	require.NoError(t, err)
	plugin.inMemoryStore = inMemory
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin, store, inMemory
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
	require.NotNil(t, short.Error.StatusCode)
	assert.Equal(t, 403, *short.Error.StatusCode)
	require.NotNil(t, short.Error.Type)
	assert.Equal(t, string(decision), *short.Error.Type)
	assert.Equal(t, true, ctx.Value(governanceRejectedContextKey))
}

func TestPreA2AHook_ExplicitVirtualKeyGrant(t *testing.T) {
	p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": false}, a2aVK("alpha"))
	ctx := a2aVKCtx(a2aTestVKValue)
	requireA2AAllowed(t, p, ctx, operationReq("alpha"))
	require.NotNil(t, ctx.Grant().Access())
	assert.True(t, ctx.Grant().Access().IsAgentAllowed("alpha"))
}

func TestPreA2AHook_PreAuthenticatedUserAccess(t *testing.T) {
	p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": false}, nil)
	ctx := a2aUserCtx()
	ctx.Grant().SetAccess(grant.NewAccess([]schemas.Permit{
		grant.NewPermit(grant.PermitAccessProfile, "profile-1", "agents", true, false, nil, nil, grant.WithAgentPermits([]string{"alpha"})),
	}, nil, "", nil))
	requireA2AAllowed(t, p, ctx, operationReq("alpha"))
}

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

func TestPreA2AHook_AllowByDefaultForIdentifiedCallers(t *testing.T) {
	p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": true}, nil)
	requireA2AAllowed(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"))
	requireA2AAllowed(t, p, a2aUserCtx(), operationReq("alpha"))
}

func TestPreA2AHook_UnresolvedCredentialDoesNotGetAllowByDefault(t *testing.T) {
	p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": true}, nil)
	requireA2ADenied(t, p, a2aVKCtx("sk-bf-unknown"), operationReq("alpha"), DecisionAccessNotFound)
}

func TestPreA2AHook_DirectAssignmentAndDenial(t *testing.T) {
	p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": false, "beta": false}, a2aVK("alpha"))
	requireA2AAllowed(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"))
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("beta"), DecisionAgentBlocked)
}

func TestPreA2AHook_ExpiredPermitDenies(t *testing.T) {
	vk := a2aVK("alpha")
	expired := time.Now().UTC().Add(-time.Second)
	vk.ExpiresAt = &expired
	p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": false}, vk)
	ctx := a2aVKCtx(a2aTestVKValue)
	requireA2ADenied(t, p, ctx, operationReq("alpha"), DecisionAccessBlocked)
	assert.True(t, ctx.Grant().Access().Bases()[0].IsExpired())
}

func TestPreA2AHook_DisabledOrUnknownAgentDenies(t *testing.T) {
	p, _, _ := newPluginForA2A(t, map[string]bool{}, a2aVK("alpha"))
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"), DecisionAgentBlocked)
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("ghost"), DecisionAgentBlocked)
}

func TestPreA2AHook_ActualOperationIsGoverned(t *testing.T) {
	p, _, _ := newPluginForA2A(t, map[string]bool{"alpha": false}, nil)
	req := &schemas.BifrostA2ARequest{
		RequestType:           schemas.A2ARequestTypeCancelTask,
		AgentName:             "alpha",
		BifrostA2ATaskRequest: &schemas.BifrostA2ATaskRequest{TaskID: "task-1"},
	}
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), req, DecisionAgentBlocked)
}

func TestPreA2AHook_ChangesAreEffectiveOnNextRequest(t *testing.T) {
	p, store, inMemory := newPluginForA2A(t, map[string]bool{"alpha": false}, nil)
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"), DecisionAgentBlocked)

	store.UpdateVirtualKeyInMemory(context.Background(), a2aVK("alpha"), nil, nil, nil)
	requireA2AAllowed(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"))

	store.UpdateVirtualKeyInMemory(context.Background(), a2aVK(), nil, nil, nil)
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"), DecisionAgentBlocked)

	inMemory.setAgents(map[string]bool{"alpha": true})
	requireA2AAllowed(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"))

	inMemory.setAgents(map[string]bool{})
	requireA2ADenied(t, p, a2aVKCtx(a2aTestVKValue), operationReq("alpha"), DecisionAgentBlocked)
}

func TestPreA2AHook_AnonymousPassesThroughWithoutAgentLookup(t *testing.T) {
	p, _, inMemory := newPluginForA2A(t, map[string]bool{"alpha": true}, nil)
	ctx := anonymousA2ACtx()
	requireA2AAllowed(t, p, ctx, operationReq("alpha"))
	assert.Zero(t, inMemory.reads)
	assert.Nil(t, ctx.Grant().Access())
}

func TestPostA2AHook_PassesThrough(t *testing.T) {
	p, _, _ := newPluginForA2A(t, map[string]bool{}, nil)
	resp := &schemas.BifrostA2AResponse{}
	gotResp, gotErr, err := p.PostA2AHook(anonymousA2ACtx(), resp, nil)
	require.NoError(t, err)
	assert.Same(t, resp, gotResp)
	assert.Nil(t, gotErr)
}
