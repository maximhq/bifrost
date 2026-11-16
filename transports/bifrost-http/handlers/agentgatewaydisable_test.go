package handlers

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/agent"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib/libtest"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

// TestAgentDisabledWhileItsRecordIsStaleCannotBeCalled settles whether an agent disabled through
// PUT stays usable when the refresh after the write fails. Governance's A2A gate reads the agent
// record's Enabled flag, and a failed refresh keeps that record enabled; but the registration
// write retires the agent's runtime before the refresh runs, so a call with a key the plugin gate
// admits finds no agent and is answered 404.
func TestAgentDisabledWhileItsRecordIsStaleCannotBeCalled(t *testing.T) {
	card := &a2a.AgentCard{Name: "alpha", Version: "1", SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface("http://unused/rpc", a2a.TransportProtocolJSONRPC)}}
	upstream := httptest.NewServer(a2asrv.NewStaticAgentCardHandler(card))
	defer upstream.Close()
	cardURL := upstream.URL + a2asrv.WellKnownAgentCardPath

	ctx := context.Background()
	active := true
	vk := &tables.TableVirtualKey{ID: "vk-1", Value: *schemas.NewSecretVar("sk-bf-one"), IsActive: &active}
	store := &agentAuthStore{byID: map[string]*tables.TableVirtualKey{"vk-1": vk}, byValue: map[string]*tables.TableVirtualKey{"sk-bf-one": vk}}
	manager, err := agent.NewManager(ctx, store, nil, "http://gateway", http.DefaultClient, agent.ManagerConfig{PrivateAllowlistProvider: func() []string { return []string{"127.0.0.1"} }})
	require.NoError(t, err)
	defer manager.Close()
	_, err = manager.Create(ctx, agent.CreateRequest{Name: "alpha", CredentialOrigins: []string{"http://unused"}, AgentCardURL: cardURL, VirtualKeyIDs: []string{"vk-1"}})
	require.NoError(t, err)
	// The plugin gate admits the key, as governance does while the agent's record reads enabled.
	pipeline := &grantPipeline{granted: map[string]bool{"sk-bf-one": true}}
	manager.SetPluginPipeline(func() agent.PluginPipeline { return pipeline }, func(agent.PluginPipeline) {})

	config := libtest.Config(t, nil)
	config.ClientConfig = &configstore.ClientConfig{A2AExternalClientURL: schemas.NewSecretVar("http://gateway"), MCPServerAuthMode: tables.MCPServerAuthModeHeaders, EnforceAuthOnInference: true}
	h := NewAgentGatewayHandler(manager, nil, config, nil)
	require.NoError(t, config.ConfigStore.DB().Create(&tables.TableAgentRegistration{Name: "alpha", AgentCardURL: cardURL, Enabled: true}).Error)
	require.NoError(t, h.refreshAgent(ctx, "alpha", false))

	r := router.New()
	r.GET("/agents/a2a/{name}/.well-known/agent-card.json", func(rctx *fasthttp.RequestCtx) {
		rctx.SetUserValue(schemas.BifrostContextKeyUserID, "user-1")
		rctx.Request.Header.Set("x-bf-vk", "sk-bf-one")
		AgentGatewayAuthenticationMiddleware(h.config, &agentAuthCache{store: store}, nil)(h.card)(rctx)
	})
	server := &fasthttp.Server{Handler: r.Handler}
	ln := fasthttputil.NewInmemoryListener()
	go server.Serve(ln) //nolint:errcheck
	defer ln.Close()
	defer server.Shutdown()
	client := &fasthttp.Client{Dial: func(string) (net.Conn, error) { return ln.Dial() }, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	call := func() int {
		req := fasthttp.AcquireRequest()
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseRequest(req)
		defer fasthttp.ReleaseResponse(resp)
		req.Header.SetMethod(fasthttp.MethodGet)
		req.SetRequestURI("http://test.local/agents/a2a/alpha/.well-known/agent-card.json")
		require.NoError(t, client.Do(req, resp))
		return resp.StatusCode()
	}
	require.Equal(t, fasthttp.StatusOK, call(), "the enabled agent answers a granted key")

	// The PUT's registration write disables the agent. Its refresh then fails, and a failed refresh
	// keeps the previous record (TestAgentRefreshFailureKeepsTheRecord), so none is run here.
	enabled := false
	_, err = h.registrationManager.UpdateAgentRegistration(ctx, "alpha", agent.UpdateRequest{AgentCardURL: &cardURL, Enabled: &enabled, CredentialOrigins: []string{"http://unused"}, VirtualKeyIDs: []string{"vk-1"}})
	require.NoError(t, err)
	record, ok := config.LiveStore.Collections.Agents.Get("alpha")
	require.True(t, ok)
	require.True(t, record.Enabled, "the record governance reads still says enabled")
	require.Equal(t, fasthttp.StatusNotFound, call(), "the retired runtime refuses the call")
}
