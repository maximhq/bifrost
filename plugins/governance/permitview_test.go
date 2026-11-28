package governance

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/stretchr/testify/require"
)

// warnLogger is a MockLogger that keeps each warning as written, arguments included.
type warnLogger struct {
	*MockLogger
	mu   sync.Mutex
	seen []string
}

// Warn records the formatted warning.
func (l *warnLogger) Warn(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen = append(l.seen, fmt.Sprintf(format, args...))
}

// warnings returns the warnings recorded so far.
func (l *warnLogger) warnings() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.seen...)
}

// permitOfKey builds the permit of the key gs holds with this ID, on ctx.
func permitOfKey(t *testing.T, gs *LocalGovernanceStore, ctx *schemas.BifrostContext, id string) *grant.Permit {
	t.Helper()
	return gs.permitForVirtualKey(ctx, storedVirtualKey(t, gs, id))
}

// openAIConfig is a provider config granting every OpenAI model and key.
func openAIConfig() []configstoreTables.TableVirtualKeyProviderConfig {
	return []configstoreTables.TableVirtualKeyProviderConfig{{
		Provider: string(schemas.OpenAI), AllowedModels: schemas.WhiteList{"*"}, AllowAllKeys: true,
	}}
}

// TestKeysWithTheSameGrantsShareAPermitView checks that keys granted the same things share one
// computed view, while each permit still carries its own key's identity.
func TestKeysWithTheSameGrantsShareAPermitView(t *testing.T) {
	h := newLiveHarness(t, &configstore.GovernanceConfig{VirtualKeys: []configstoreTables.TableVirtualKey{
		*buildVirtualKeyWithProviders("vk-1", "sk-bf-1", "one", openAIConfig()),
		*buildVirtualKeyWithProviders("vk-2", "sk-bf-2", "two", openAIConfig()),
	}}, nil)
	gs := h.mustGovernanceStore(NewMockLogger(), nil)
	one, two := storedVirtualKey(t, gs, "vk-1"), storedVirtualKey(t, gs, "vk-2")
	require.Same(t, one.Grants, two.Grants, "the stored keys share one copy of their grant set")
	require.Same(t, gs.permitViewOf(one.Grants), gs.permitViewOf(two.Grants), "one view serves both keys")

	p1, p2 := permitOfKey(t, gs, emptyCtx(), "vk-1"), permitOfKey(t, gs, emptyCtx(), "vk-2")
	require.Equal(t, "vk-1", p1.ID())
	require.Equal(t, "two", p2.Name())
	require.Equal(t, p1.ProviderPermits(), p2.ProviderPermits())
}

// TestPermitViewFollowsWhatItReads checks that a key's permit picks up a change to a provider, an
// MCP client or a virtual MCP, none of which touches the key's own record, as a permit built for
// every request always has.
func TestPermitViewFollowsWhatItReads(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk-1", "sk-bf-1", "one", openAIConfig())
	vk.AllowAllProviders = true
	h := newLiveHarness(t, &configstore.GovernanceConfig{VirtualKeys: []configstoreTables.TableVirtualKey{*vk}}, &liveFixture{
		configuredProviders: map[schemas.ModelProvider]configstore.ProviderConfig{schemas.OpenAI: {}},
	})
	gs := h.mustGovernanceStore(NewMockLogger(), nil)
	access := func() schemas.Access {
		return grant.NewAccess([]schemas.Permit{permitOfKey(t, gs, emptyCtx(), "vk-1")}, "", nil, "", nil)
	}
	require.Equal(t, []string{"openai"}, access().GrantedProvidersForModel(""))
	require.False(t, access().IsMCPToolAllowed("search-find"))

	h.putProvider(&configstoreTables.TableProvider{Name: string(schemas.Anthropic)})
	require.ElementsMatch(t, []string{"openai", "anthropic"}, access().GrantedProvidersForModel(""),
		"a provider added later is granted by the allow-all flag")

	_, err := h.ensureMCPClient("client-1", "search", "", true)
	require.NoError(t, err)
	h.refresh(livestore.Ref{Kind: livestore.KindMCPClient, ID: "client-1"})
	require.True(t, access().IsMCPToolAllowed("search-find"), "a client opened to every key is granted")

	_, err = h.ensureMCPClient("client-2", "docs", "", false)
	require.NoError(t, err)
	h.refresh(livestore.Ref{Kind: livestore.KindMCPClient, ID: "client-2"})
	def := &configstoreTables.TableVirtualMCP{Name: "docs-group", Enabled: true,
		ParsedTools: []configstoreTables.MCPToolSpec{spec("client-2", "read")}}
	id := h.putVirtualMCP(def)
	h.attachVirtualMCP("vk-1", id)
	require.True(t, access().IsMCPToolAllowed("docs-read"), "the assigned virtual MCP grants its tools")

	def.Enabled = false
	h.putVirtualMCP(def)
	require.False(t, access().IsMCPToolAllowed("docs-read"), "a disabled virtual MCP grants nothing")
}

// TestPermitViewReportsPerRequest checks what each permit built from a shared view still does for
// its own request: it records its assigned virtual MCPs as addressable on its own request, and warns
// about an MCP client with no configured name under its own key's name.
func TestPermitViewReportsPerRequest(t *testing.T) {
	h := newLiveHarness(t, &configstore.GovernanceConfig{}, nil)
	logger := &warnLogger{MockLogger: NewMockLogger()}
	gs := h.mustGovernanceStore(logger, nil)
	// The virtual MCP grants a client no row names, so the client has no configured name.
	id := h.putVirtualMCP(&configstoreTables.TableVirtualMCP{Name: "grp", EndpointSlug: "grp", Enabled: true,
		ParsedTools: []configstoreTables.MCPToolSpec{spec("ghost", "*")}})
	first := virtualKeyRecord(gs.live, buildVirtualKey("vk-1", "sk-bf-1", "first", true), id)
	second := virtualKeyRecord(gs.live, buildVirtualKey("vk-2", "sk-bf-2", "second", true), id)
	require.Equal(t, first.Grants, second.Grants, "both keys hold one grant set")

	for _, key := range []*registry.VirtualKey{first, second} {
		ctx := gatewayCtx()
		gs.permitForVirtualKey(ctx, key)
		_, recorded := grantedVirtualMCP(ctx, "grp")
		require.True(t, recorded, "%s's request records the virtual MCP as addressable", key.Name)
	}
	warned := logger.warnings()
	require.Len(t, warned, 2, "one warning per permit built")
	require.Contains(t, warned[0], `virtual key "first"`)
	require.Contains(t, warned[1], `virtual key "second"`)
	require.Contains(t, warned[1], `"ghost"`)
}

// TestMissingGrantSetGrantsNothing checks that a key naming a grant set the store does not hold is
// granted nothing: no provider and no MCP client, not even one allowed by default to every key.
func TestMissingGrantSetGrantsNothing(t *testing.T) {
	h := newLiveHarness(t, &configstore.GovernanceConfig{}, nil)
	gs := h.mustGovernanceStore(NewMockLogger(), nil)
	_, err := h.ensureMCPClient("client-1", "search", "", true)
	require.NoError(t, err)
	h.refresh(livestore.Ref{Kind: livestore.KindMCPClient, ID: "client-1"})
	held := virtualKeyRecord(gs.live, buildVirtualKey("vk-1", "sk-bf-1", "held", true))
	require.NotEmpty(t, gs.permitForVirtualKey(emptyCtx(), held).MCPPermits(), "a held set is granted the client allowed by default")

	missing := *held
	missing.Grants = nil
	permit := gs.permitForVirtualKey(emptyCtx(), &missing)
	require.Empty(t, permit.MCPPermits(), "a key with no set grants no MCP client, not even one allowed by default")
	require.Empty(t, permit.ProviderPermits())
}

// TestPermitViewExpiresAfterItsTTL checks that a permit view is kept for permitViewTTL and no
// longer, so the view of a set whose only key is gone leaves the table within it even when no
// provider, MCP client or virtual MCP changes, and a live set's view is built again once per TTL.
func TestPermitViewExpiresAfterItsTTL(t *testing.T) {
	h := newLiveHarness(t, &configstore.GovernanceConfig{VirtualKeys: []configstoreTables.TableVirtualKey{
		*buildVirtualKeyWithProviders("vk-1", "sk-bf-1", "one", openAIConfig()),
	}}, nil)
	gs := h.mustGovernanceStore(NewMockLogger(), nil)
	set := storedVirtualKey(t, gs, "vk-1").Grants
	builtAt := time.Now()
	require.NotNil(t, gs.permitViewOf(set))
	h.deleteVirtualKey("vk-1")

	_, held := gs.permitViews.Get(livestore.Hash(set.ID))
	require.True(t, held, "the view is still kept: no provider, MCP client or virtual MCP changed")
	expires, ok := gs.permitViews.Expiry(livestore.Hash(set.ID))
	require.True(t, ok, "the view of a set whose only key is gone expires")
	require.WithinDuration(t, builtAt.Add(permitViewTTL), expires, time.Second, "after the TTL")
}
