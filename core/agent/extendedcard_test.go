package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// identityContext reproduces what the HTTP transport publishes for an
// authenticated caller: the raw virtual key on a BifrostContext. It is the only
// identity signal the extended-card path reads, and it deliberately carries no
// grant information — authorization stays with the governance plugin.
func identityContext(t *testing.T, virtualKey string) context.Context {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, virtualKey)
	return ctx
}

// extendedCardFixture runs an upstream agent whose public and extended cards
// differ, so a test can tell which one the gateway actually served. With
// withExtended false the upstream still advertises the capability but exposes no
// extended card, which is how a real agent reports that it has none; the returned
// extended card is nil in that case.
func extendedCardFixture(t *testing.T, withExtended bool) (*httptest.Server, *a2a.AgentCard, *a2a.AgentCard) {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	upstreamSchemes := func() a2a.NamedSecuritySchemes {
		return a2a.NamedSecuritySchemes{"upstreamKey": a2a.APIKeySecurityScheme{Name: "x-api-key", Location: a2a.APIKeySecuritySchemeLocationHeader}}
	}
	public := &a2a.AgentCard{
		Name:                 "fixture",
		Version:              "1",
		SupportedInterfaces:  []*a2a.AgentInterface{a2a.NewAgentInterface(server.URL+"/rpc", a2a.TransportProtocolJSONRPC)},
		Capabilities:         a2a.AgentCapabilities{ExtendedAgentCard: true},
		DefaultInputModes:    []string{"text/plain"},
		DefaultOutputModes:   []string{"text/plain"},
		SecuritySchemes:      upstreamSchemes(),
		SecurityRequirements: a2a.SecurityRequirementsOptions{{"upstreamKey": {}}},
		Signatures:           []a2a.AgentCardSignature{{Protected: "public-protected", Signature: "public-signature"}},
	}
	mux.Handle(agentCardPath, a2asrv.NewStaticAgentCardHandler(public))

	options := []a2asrv.RequestHandlerOption{a2asrv.WithCapabilityChecks(&a2a.AgentCapabilities{ExtendedAgentCard: true})}
	var extended *a2a.AgentCard
	if withExtended {
		// The upstream's extended card is richer and is described in the
		// upstream's own trust domain: upstream URL, upstream signature.
		extended = &a2a.AgentCard{
			Name:                 "fixture-extended",
			Version:              "1",
			Description:          "extended detail",
			SupportedInterfaces:  []*a2a.AgentInterface{a2a.NewAgentInterface(server.URL+"/rpc", a2a.TransportProtocolJSONRPC)},
			Capabilities:         a2a.AgentCapabilities{ExtendedAgentCard: true, PushNotifications: true},
			DefaultInputModes:    []string{"text/plain"},
			DefaultOutputModes:   []string{"text/plain"},
			Skills:               []a2a.AgentSkill{{ID: "private-skill", Name: "private"}},
			SecuritySchemes:      upstreamSchemes(),
			SecurityRequirements: a2a.SecurityRequirementsOptions{{"upstreamKey": {}}},
			Signatures:           []a2a.AgentCardSignature{{Protected: "extended-protected", Signature: "extended-signature"}},
		}
		options = append(options, a2asrv.WithExtendedAgentCard(extended))
	}
	mux.Handle("/rpc", a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(&fixtureExecutor{}, options...)))
	return server, public, extended
}

// extendedCardGateway registers the fixture agent and returns the gateway's own
// request handler, which is the object the SDK transports dispatch onto.
func extendedCardGateway(t *testing.T, upstream *httptest.Server, policy schemas.AgentGatewayAuthPolicy) (*Manager, *proxyRequestHandler) {
	t.Helper()
	store := &memoryStore{regs: map[string]schemas.AgentRegistration{}}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil, ManagerConfig{AuthPolicy: policy})
	require.NoError(t, err)
	t.Cleanup(m.Close)
	_, err = m.Create(context.Background(), CreateRequest{Name: "fixture", AgentCardURL: upstream.URL + agentCardPath})
	require.NoError(t, err)
	runtime, ok := m.runtimes.load("fixture")
	require.True(t, ok)
	return m, &proxyRequestHandler{runtime: runtime, manager: m, config: runtime.config}
}

// The extended card must describe Bifrost exactly as strongly as the public card
// does: same gateway interfaces, no top-level url, no upstream signature, the
// Bifrost virtual-key scheme advertised, and the same security requirements —
// while still carrying the upstream's extended content.
func TestExtendedCardReceivesTheSameGatewayTransformationAsThePublicCard(t *testing.T) {
	upstream, _, extended := extendedCardFixture(t, true)
	var enforce atomic.Bool
	m, handler := extendedCardGateway(t, upstream, schemas.AgentGatewayAuthPolicy{EnforceAuthentication: enforce.Load})

	cardHandler, ok := m.CardHandler("fixture", "")
	require.True(t, ok)
	cardServer := httptest.NewServer(cardHandler)
	defer cardServer.Close()
	publicCard, err := agentcard.NewResolver(http.DefaultClient).Resolve(context.Background(), cardServer.URL)
	require.NoError(t, err)

	served, err := handler.GetExtendedAgentCard(identityContext(t, "sk-bf-one"), &a2a.GetExtendedAgentCardRequest{})
	require.NoError(t, err)

	require.Equal(t, publicCard.SupportedInterfaces, served.SupportedInterfaces, "the extended card points at the gateway, not the upstream agent")
	require.Equal(t, "http://gateway/agents/a2a/fixture"+GatewayJSONRPCPathSuffix, served.SupportedInterfaces[0].URL)
	require.Equal(t, a2a.TransportProtocolJSONRPC, served.SupportedInterfaces[0].ProtocolBinding)
	require.Equal(t, "http://gateway/agents/a2a/fixture"+GatewayRESTPathSuffix, served.SupportedInterfaces[1].URL)
	require.Equal(t, a2a.TransportProtocolHTTPJSON, served.SupportedInterfaces[1].ProtocolBinding)
	require.Nil(t, served.Signatures, "a modified card must not keep the upstream signature")
	require.Equal(t, publicCard.SecuritySchemes, served.SecuritySchemes)
	require.Contains(t, served.SecuritySchemes, a2a.SecuritySchemeName("bifrostVirtualKey"))
	require.Equal(t, publicCard.SecurityRequirements, served.SecurityRequirements)
	require.False(t, served.Capabilities.PushNotifications, "the gateway advertises only what it proxies")

	encoded, err := json.Marshal(served)
	require.NoError(t, err)
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &payload))
	require.NotContains(t, payload, "url", "strict A2A v1 has no top-level url field")

	// The content is still the upstream's extended card, not the public one.
	require.Equal(t, "fixture-extended", served.Name)
	require.Equal(t, "extended detail", served.Description)
	require.Len(t, served.Skills, 1)
	require.Equal(t, "private-skill", served.Skills[0].ID)
	require.NotEqual(t, publicCard.Name, served.Name)

	// The upstream card object is never mutated by the transformation.
	require.Equal(t, []a2a.AgentCardSignature{{Protected: "extended-protected", Signature: "extended-signature"}}, extended.Signatures)
	require.Equal(t, a2a.SecurityRequirementsOptions{{"upstreamKey": {}}}, extended.SecurityRequirements)
}

// Extended-card security requirements are composed from the live enforcement
// setting, exactly like the public card: the Bifrost scheme is ANDed into every
// upstream requirement when enforcement is on, and absent when it is off.
func TestExtendedCardSecurityRequirementComposition(t *testing.T) {
	upstream, _, _ := extendedCardFixture(t, true)
	var enforce atomic.Bool
	_, handler := extendedCardGateway(t, upstream, schemas.AgentGatewayAuthPolicy{EnforceAuthentication: enforce.Load})
	ctx := identityContext(t, "sk-bf-one")

	served, err := handler.GetExtendedAgentCard(ctx, &a2a.GetExtendedAgentCardRequest{})
	require.NoError(t, err)
	require.Equal(t, a2a.SecurityRequirementsOptions{{"upstreamKey": make(a2a.SecuritySchemeScopes, 0)}}, served.SecurityRequirements)

	enforce.Store(true)
	served, err = handler.GetExtendedAgentCard(ctx, &a2a.GetExtendedAgentCardRequest{})
	require.NoError(t, err)
	require.Equal(t, a2a.SecurityRequirementsOptions{{
		"upstreamKey":       make(a2a.SecuritySchemeScopes, 0),
		"bifrostVirtualKey": make(a2a.SecuritySchemeScopes, 0),
	}}, served.SecurityRequirements, "both identities are required together, never as alternatives")
}

// The extended card follows the global enforcement setting like every other
// protocol operation. With enforcement disabled Bifrost does not interfere: a
// request carrying no Bifrost identity is forwarded anonymously and the upstream
// agent — which still enforces its own authentication — decides. The 401 for an
// anonymous request under enforcement is applied at the transport boundary, not
// here.
func TestExtendedCardFollowsEnforcementSettingForAnonymousCallers(t *testing.T) {
	upstream, _, _ := extendedCardFixture(t, true)
	var enforce atomic.Bool // stays false: enforcement disabled
	m, handler := extendedCardGateway(t, upstream, schemas.AgentGatewayAuthPolicy{EnforceAuthentication: enforce.Load})

	// The gateway is otherwise fully anonymous-capable here: the public card is
	// served without any identity at all.
	cardHandler, ok := m.CardHandler("fixture", "")
	require.True(t, ok)
	cardServer := httptest.NewServer(cardHandler)
	defer cardServer.Close()
	_, err := agentcard.NewResolver(http.DefaultClient).Resolve(context.Background(), cardServer.URL)
	require.NoError(t, err)

	for _, test := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "no bifrost context", ctx: context.Background()},
		{name: "empty identity", ctx: identityContext(t, "")},
	} {
		t.Run(test.name, func(t *testing.T) {
			served, extendedErr := handler.GetExtendedAgentCard(test.ctx, &a2a.GetExtendedAgentCardRequest{})
			require.NoError(t, extendedErr, "with enforcement disabled the gateway does not gate the extended card")
			require.NotNil(t, served)
			require.Equal(t, "fixture-extended", served.Name, "the upstream extended content is forwarded")
		})
	}
}

// An upstream that exposes no extended card must surface its own SDK-native
// error; the gateway must not invent a card or translate the failure.
func TestExtendedCardUpstreamUnsupportedErrorPropagatesUnchanged(t *testing.T) {
	upstream, _, _ := extendedCardFixture(t, false)
	var enforce atomic.Bool
	_, handler := extendedCardGateway(t, upstream, schemas.AgentGatewayAuthPolicy{EnforceAuthentication: enforce.Load})

	served, err := handler.GetExtendedAgentCard(identityContext(t, "sk-bf-one"), &a2a.GetExtendedAgentCardRequest{})
	require.Nil(t, served, "no card is fabricated for an upstream that has none")
	require.ErrorIs(t, err, a2a.ErrExtendedCardNotConfigured)
}
