package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// mockOAuthConfigStore embeds the interface so unimplemented methods panic.
// Only the lookups getOAuthConfigStatus reaches are implemented.
type mockOAuthConfigStore struct {
	configstore.ConfigStore

	config     *configstoreTables.TableOauthConfig
	configErr  error
	client     *configstoreTables.TableMCPClient
	clientErr  error
	flow       *configstoreTables.TableMCPOauthFlow
	flowErr    error
	clientRead bool
	flowRead   bool
}

func (m *mockOAuthConfigStore) GetOauthConfigByID(_ context.Context, _ string) (*configstoreTables.TableOauthConfig, error) {
	return m.config, m.configErr
}

func (m *mockOAuthConfigStore) GetMCPClientByOauthConfigID(_ context.Context, _ string) (*configstoreTables.TableMCPClient, error) {
	m.clientRead = true
	return m.client, m.clientErr
}

func (m *mockOAuthConfigStore) GetOauthUserSessionByModeIdentityAndMCPClient(_ context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (*configstoreTables.TableMCPOauthFlow, error) {
	if mode != schemas.MCPAuthModeAdmin {
		return nil, fmt.Errorf("unexpected mode %q", mode)
	}
	if identity != "" {
		return nil, fmt.Errorf("unexpected identity %q", identity)
	}
	if m.client != nil && mcpClientID != m.client.ClientID {
		return nil, fmt.Errorf("looked up flow for client %q, want %q", mcpClientID, m.client.ClientID)
	}
	m.flowRead = true
	return m.flow, m.flowErr
}

func (m *mockOAuthConfigStore) GetSharedOauthTokenByConfigID(_ context.Context, _ string) (*configstoreTables.TableMCPOauthToken, error) {
	return nil, nil
}

// callGetOAuthConfigStatus drives the handler the way the router does.
func callGetOAuthConfigStatus(t *testing.T, store configstore.ConfigStore) (int, map[string]interface{}) {
	t.Helper()
	h := &OAuthHandler{store: &lib.Config{ConfigStore: store}}
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("id", "cfg-1")
	h.getOAuthConfigStatus(ctx)

	var body map[string]interface{}
	if len(ctx.Response.Body()) > 0 {
		if err := json.Unmarshal(ctx.Response.Body(), &body); err != nil {
			t.Fatalf("unmarshal response body: %v (%s)", err, ctx.Response.Body())
		}
	}
	return ctx.Response.StatusCode(), body
}

// The wiring the pure resolver test cannot reach: an authorized config whose
// client has a pending, unexpired admin flow must come back as pending — not
// as the config's own stale "authorized", which is what let the authorizer UI
// complete a reauth before the user had consented.
func TestGetOAuthConfigStatus_ReportsPendingFlowForAuthorizedConfig(t *testing.T) {
	store := &mockOAuthConfigStore{
		config: &configstoreTables.TableOauthConfig{ID: "cfg-1", Status: "authorized"},
		client: &configstoreTables.TableMCPClient{ClientID: "client-1"},
		flow: &configstoreTables.TableMCPOauthFlow{
			ID:          "flow-1",
			MCPClientID: "client-1",
			Status:      "pending",
			ExpiresAt:   time.Now().Add(time.Hour),
		},
	}

	code, body := callGetOAuthConfigStatus(t, store)

	if code != fasthttp.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got := body["status"]; got != "pending" {
		t.Errorf("status field = %v, want pending", got)
	}
	if !store.clientRead || !store.flowRead {
		t.Errorf("handler did not read the flow (clientRead=%v flowRead=%v)", store.clientRead, store.flowRead)
	}
}

// ...and the same config with no flow row is reported as it always was, so the
// lookup does not change the non-reauth path.
func TestGetOAuthConfigStatus_KeepsAuthorizedWithoutFlow(t *testing.T) {
	store := &mockOAuthConfigStore{
		config: &configstoreTables.TableOauthConfig{ID: "cfg-1", Status: "authorized"},
		client: &configstoreTables.TableMCPClient{ClientID: "client-1"},
		flow:   nil,
	}

	code, body := callGetOAuthConfigStatus(t, store)

	if code != fasthttp.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got := body["status"]; got != "authorized" {
		t.Errorf("status field = %v, want authorized", got)
	}
}

// A config with no owning MCP client is not a failure: the admin-test popup
// validates a template that may have no client yet, so the flow lookup must
// not turn ErrNotFound into a 500.
func TestGetOAuthConfigStatus_NoOwningClientIsNotAnError(t *testing.T) {
	store := &mockOAuthConfigStore{
		config:    &configstoreTables.TableOauthConfig{ID: "cfg-1", Status: "authorized"},
		clientErr: configstore.ErrNotFound,
	}

	code, body := callGetOAuthConfigStatus(t, store)

	if code != fasthttp.StatusOK {
		t.Fatalf("status = %d, want 200 (a template config has no client)", code)
	}
	if got := body["status"]; got != "authorized" {
		t.Errorf("status field = %v, want authorized", got)
	}
}

// A lookup failure must not be folded into "no reauth in flight": that reports
// the stale authorized status and reopens the premature-completion path this
// endpoint exists to close. Fail closed instead — the UI's poll treats the 500
// as transient and keeps polling.
func TestGetOAuthConfigStatus_FlowLookupFailureIs500(t *testing.T) {
	for name, store := range map[string]*mockOAuthConfigStore{
		"client lookup fails": {
			config:    &configstoreTables.TableOauthConfig{ID: "cfg-1", Status: "authorized"},
			clientErr: errors.New("store unavailable"),
		},
		"flow lookup fails": {
			config:  &configstoreTables.TableOauthConfig{ID: "cfg-1", Status: "authorized"},
			client:  &configstoreTables.TableMCPClient{ClientID: "client-1"},
			flowErr: errors.New("store unavailable"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			code, _ := callGetOAuthConfigStatus(t, store)
			if code != fasthttp.StatusInternalServerError {
				t.Errorf("status = %d, want 500", code)
			}
		})
	}
}

// The lookup is gated on an already-authorized config, so a first auth pays no
// extra query and its own status is reported untouched.
func TestGetOAuthConfigStatus_SkipsFlowLookupForNonAuthorizedConfig(t *testing.T) {
	store := &mockOAuthConfigStore{
		config: &configstoreTables.TableOauthConfig{ID: "cfg-1", Status: "pending"},
	}

	code, body := callGetOAuthConfigStatus(t, store)

	if code != fasthttp.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got := body["status"]; got != "pending" {
		t.Errorf("status field = %v, want pending", got)
	}
	if store.clientRead || store.flowRead {
		t.Errorf("handler read the flow for a non-authorized config (clientRead=%v flowRead=%v)", store.clientRead, store.flowRead)
	}
}
