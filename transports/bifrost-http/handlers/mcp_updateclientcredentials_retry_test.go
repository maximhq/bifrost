package handlers

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// fakeUpdateCredsMCPManager is a minimal MCPManager test double: only
// UpdateMCPClientCredentials has real behavior (scripted per call, tracked by
// call count); every other method is an unused no-op, since
// updateMCPClientCredentialsWithRetry only ever calls that one method.
type fakeUpdateCredsMCPManager struct {
	err       error // returned on every call
	callCount int
}

func (m *fakeUpdateCredsMCPManager) UpdateMCPClientCredentials(_ context.Context, _ string, _ *schemas.MCPClientConfig) error {
	m.callCount++
	return m.err
}

func (m *fakeUpdateCredsMCPManager) AddMCPClient(_ context.Context, _ *schemas.MCPClientConfig) error {
	return nil
}
func (m *fakeUpdateCredsMCPManager) RemoveMCPClient(_ context.Context, _ string) error { return nil }
func (m *fakeUpdateCredsMCPManager) UpdateMCPClient(_ context.Context, _ string, _ *schemas.MCPClientConfig) error {
	return nil
}
func (m *fakeUpdateCredsMCPManager) ReconnectMCPClient(_ context.Context, _ string) error { return nil }
func (m *fakeUpdateCredsMCPManager) CloseAndMarkNeedsReauth(_ context.Context, _ string) error {
	return nil
}
func (m *fakeUpdateCredsMCPManager) DisableMCPClient(_ context.Context, _ string) error { return nil }
func (m *fakeUpdateCredsMCPManager) EnableMCPClient(_ context.Context, _ string) error  { return nil }
func (m *fakeUpdateCredsMCPManager) VerifyPerUserOAuthConnection(_ context.Context, _ *schemas.MCPClientConfig, _ string) (map[string]schemas.ChatTool, map[string]string, error) {
	return nil, nil, nil
}
func (m *fakeUpdateCredsMCPManager) VerifyHeadersConnection(_ context.Context, _ *schemas.MCPClientConfig, _ map[string]string) (map[string]schemas.ChatTool, map[string]string, error) {
	return nil, nil, nil
}
func (m *fakeUpdateCredsMCPManager) SetClientTools(_ string, _ map[string]schemas.ChatTool, _ map[string]string) {
}
func (m *fakeUpdateCredsMCPManager) RequiresPerCallConnection(_ *schemas.MCPClientConfig) bool {
	return false
}

// TestUpdateMCPClientCredentialsWithRetry_NotApplicable_ReturnsImmediately pins
// the fix for the shared-OAuth-per-call reconnect bug: ErrMCPReconnectNotApplicable
// (a per-call client — no persistent connection to update) is never transient,
// so it must be returned on the first attempt, not funneled into the
// "blocked by in-flight reconnect" retry loop — which the sentinel's own
// message text would otherwise match, since it contains "reconnect" and the
// loop's fallback classification is a bare substring check.
func TestUpdateMCPClientCredentialsWithRetry_NotApplicable_ReturnsImmediately(t *testing.T) {
	mgr := &fakeUpdateCredsMCPManager{
		err: fmt.Errorf("client uses per-call connections; there is no persistent connection to update: %w", schemas.ErrMCPReconnectNotApplicable),
	}
	h := &MCPHandler{mcpManager: mgr}

	err := h.updateMCPClientCredentialsWithRetry(context.Background(), "client-1", &schemas.MCPClientConfig{})
	if err == nil {
		t.Fatal("expected the not-applicable sentinel to be returned as an error")
	}
	if !errors.Is(err, schemas.ErrMCPReconnectNotApplicable) {
		t.Fatalf("expected errors.Is match against ErrMCPReconnectNotApplicable, got: %v", err)
	}
	if mgr.callCount != 1 {
		t.Errorf("expected exactly 1 call (no retry for a non-transient not-applicable error), got %d", mgr.callCount)
	}
}

// TestUpdateMCPClientCredentialsWithRetry_TransientReconnectError_StillRetries
// is a regression guard for the pre-existing retry behavior: an in-flight
// "reconnect already in progress" error (transient, unrelated to
// ErrMCPReconnectNotApplicable) must still go through the retry loop as
// before.
func TestUpdateMCPClientCredentialsWithRetry_TransientReconnectError_StillRetries(t *testing.T) {
	SetLogger(&mockLogger{}) // the retry loop logs a warning per retry attempt
	mgr := &fakeUpdateCredsMCPManager{
		err: errors.New("reconnect already in progress for this client"),
	}
	h := &MCPHandler{mcpManager: mgr}

	err := h.updateMCPClientCredentialsWithRetry(context.Background(), "client-1", &schemas.MCPClientConfig{})
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if mgr.callCount != 3 {
		t.Errorf("expected all 3 attempts to run for a transient 'reconnect' error, got %d", mgr.callCount)
	}
}
