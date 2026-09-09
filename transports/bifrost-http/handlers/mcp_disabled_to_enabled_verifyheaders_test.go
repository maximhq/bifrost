package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// fakeMCPManagerVerifyOnly is a minimal MCPManager test double for this path.
// It records VerifyHeadersConnection and UpdateMCPClient calls so the test can
// assert pre-flight verification ran and no live update happened on failure.
type fakeMCPManagerVerifyOnly struct {
	verifyCalls int
	updateCalls int
}

func (m *fakeMCPManagerVerifyOnly) AddMCPClient(_ context.Context, _ *schemas.MCPClientConfig) error {
	return nil
}
func (m *fakeMCPManagerVerifyOnly) RemoveMCPClient(_ context.Context, _ string) error { return nil }
func (m *fakeMCPManagerVerifyOnly) UpdateMCPClient(_ context.Context, _ string, _ *schemas.MCPClientConfig) error {
	m.updateCalls++
	return nil
}
func (m *fakeMCPManagerVerifyOnly) UpdateMCPClientCredentials(_ context.Context, _ string, _ *schemas.MCPClientConfig) error {
	return nil
}
func (m *fakeMCPManagerVerifyOnly) ReconnectMCPClient(_ context.Context, _ string) error { return nil }
func (m *fakeMCPManagerVerifyOnly) CloseAndMarkNeedsReauth(_ context.Context, _ string) error {
	return nil
}
func (m *fakeMCPManagerVerifyOnly) DisableMCPClient(_ context.Context, _ string) error { return nil }
func (m *fakeMCPManagerVerifyOnly) EnableMCPClient(_ context.Context, _ string) error  { return nil }
func (m *fakeMCPManagerVerifyOnly) VerifyPerUserOAuthConnection(_ context.Context, _ *schemas.MCPClientConfig, _ string) (map[string]schemas.ChatTool, map[string]string, error) {
	return nil, nil, nil
}
func (m *fakeMCPManagerVerifyOnly) VerifyHeadersConnection(_ context.Context, _ *schemas.MCPClientConfig, _ map[string]string) (map[string]schemas.ChatTool, map[string]string, error) {
	m.verifyCalls++
	return nil, nil, errors.New("rejected by upstream")
}
func (m *fakeMCPManagerVerifyOnly) SetClientTools(_ string, _ map[string]schemas.ChatTool, _ map[string]string) {
}
func (m *fakeMCPManagerVerifyOnly) RequiresPerCallConnection(_ *schemas.MCPClientConfig) bool {
	return false
}

// mockUpdateConfigStore embeds the interface so unimplemented methods panic if called.
// The test asserts UpdateMCPClientConfig is NOT called when verification fails.
type mockUpdateConfigStore struct {
	configstore.ConfigStore
	updates int
}

func (m *mockUpdateConfigStore) GetMCPClientByID(_ context.Context, id string) (*configstoreTables.TableMCPClient, error) {
	return &configstoreTables.TableMCPClient{ClientID: id}, nil
}

func (m *mockUpdateConfigStore) GetClientConfig(_ context.Context) (*configstore.ClientConfig, error) {
	return nil, nil
}

func (m *mockUpdateConfigStore) UpdateMCPClientConfig(_ context.Context, _ string, _ *configstoreTables.TableMCPClient) error {
	m.updates++
	return nil
}

func TestUpdateMCPClient_DisabledToEnabled_WithInvalidReplacementHeaders_PreflightRejects(t *testing.T) {
	SetLogger(&mockLogger{})
	plain := func(v string) schemas.SecretVar { return *schemas.NewSecretVar(v) }

	// Existing client is disabled with some stored headers.
	existing := &schemas.MCPClientConfig{
		ID:                "client-1",
		Name:              "Test",
		ConnectionType:    schemas.MCPConnectionTypeHTTP,
		AuthType:          schemas.MCPAuthTypeHeaders,
		Disabled:          true,
		Headers:           map[string]schemas.SecretVar{"Authorization": plain("Bearer old")},
		PerUserHeaderKeys: nil,
	}
	store := &lib.Config{MCPConfig: &schemas.MCPConfig{ClientConfigs: []*schemas.MCPClientConfig{existing}}}
	store.ClientConfig = &configstore.ClientConfig{} // avoid nil deref in ConvertToBifrostContext
	cfgStore := &mockUpdateConfigStore{}
	store.ConfigStore = cfgStore
	mgr := &fakeMCPManagerVerifyOnly{}
	h := &MCPHandler{store: store, mcpManager: mgr}

	// Build request body: enable the client and set replacement headers.
	disabled := false
	body, err := json.Marshal(MCPClientUpdateRequest{
		Disabled: &disabled,
		Headers:  map[string]schemas.SecretVar{"Authorization": plain("Bearer new-invalid")},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("id", existing.ID)
	ctx.Request.SetBody(body)

	// Exercise handler.
	h.updateMCPClient(ctx)

	if code := ctx.Response.StatusCode(); code != fasthttp.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", code, string(ctx.Response.Body()))
	}
	if mgr.verifyCalls != 1 {
		t.Fatalf("expected VerifyHeadersConnection to run once, got %d", mgr.verifyCalls)
	}
	if mgr.updateCalls != 0 {
		t.Fatalf("expected no live UpdateMCPClient on pre-flight failure, got %d", mgr.updateCalls)
	}
	if cfgStore.updates != 0 {
		t.Fatalf("expected no DB UpdateMCPClientConfig on pre-flight failure, got %d", cfgStore.updates)
	}
	if !existing.Disabled {
		t.Fatalf("expected in-memory config to remain disabled after rejection, got Disabled=false")
	}
}
