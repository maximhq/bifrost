package mcp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// pending_verification is authoritative: it means a human still has to
// complete a one-time setup step, and nothing automatic may overwrite it with
// a state that hides the CTA the UI shows for it.
// =============================================================================

// newPendingClientConfig builds a client that AddClient would have parked in
// pending_verification: one recorded as still needing its admin step, here an
// OAuth-based client carrying the inline oauth_config block from config.json
// that no admin has authorized yet.
func newPendingClientConfig(id string, authType schemas.MCPAuthType) *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID:                  id,
		Name:                "pending-" + id,
		AuthType:            authType,
		ConnectionType:      schemas.MCPConnectionTypeHTTP,
		ConnectionString:    schemas.NewSecretVar("http://127.0.0.1:0/mcp"),
		PendingOAuthConfig:  &schemas.OAuth2Config{},
		PendingVerification: true,
	}
}

// TestEnableClient_StillPendingVerification_KeepsThatState covers the path that
// loses the signal. AddClient's disabled branch runs before its
// pending_verification branch, so a client declared both disabled and
// unauthorized is registered as Disabled with its PendingVerification intact.
// Enabling it then took the ordinary route: the per-call branch marked it
// Healthy outright, and the sticky branch dialled with a credential that does
// not exist yet. Either way the "an admin must authorize this" state was gone,
// and with it the Verify CTA that is the only way to resolve it.
func TestEnableClient_StillPendingVerification_KeepsThatState(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*schemas.MCPClientConfig)
		authType schemas.MCPAuthType
	}{
		{name: "per_user_oauth", authType: schemas.MCPAuthTypePerUserOauth},
		{name: "oauth", authType: schemas.MCPAuthTypeOauth},
		{
			// The record decides, not the absence of tools or a stash.
			name:     "per_user_headers with no discovery yet",
			authType: schemas.MCPAuthTypePerUserHeaders,
			mutate: func(c *schemas.MCPClientConfig) {
				c.PendingOAuthConfig = nil
				c.DiscoveredTools = nil
			},
		},
		{
			name:     "token_exchange with no discovery yet",
			authType: schemas.MCPAuthTypeTokenExchange,
			mutate: func(c *schemas.MCPClientConfig) {
				c.PendingOAuthConfig = nil
				c.DiscoveredTools = nil
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			defer m.checkerManager.StopAll()

			config := newPendingClientConfig("client-enable-pending", tc.authType)
			if tc.mutate != nil {
				tc.mutate(config)
			}
			config.Disabled = true
			m.mu.Lock()
			m.clientMap[config.ID] = &schemas.MCPClientState{
				Name:            config.Name,
				ExecutionConfig: config,
				State:           schemas.MCPConnectionStateDisabled,
				ConnectionInfo:  &schemas.MCPClientConnectionInfo{Type: config.ConnectionType},
			}
			m.mu.Unlock()

			require.NoError(t, m.EnableClient(config.ID), "enabling must succeed: the client is legitimately enabled, just not authorized yet")

			m.mu.RLock()
			state := m.clientMap[config.ID].State
			disabled := m.clientMap[config.ID].ExecutionConfig.Disabled
			m.mu.RUnlock()

			assert.Equal(t, schemas.MCPConnectionStatePendingVerification, state,
				"an enabled but never-authorized client is awaiting an admin, not healthy and not broken")
			assert.False(t, disabled, "the enable itself still stands")
		})
	}
}

// TestPerformCheck_PendingVerification_StaysQuiet pins the checker half. A
// client awaiting its one-time admin flow has no credential to check with, so
// a check can only rediscover what is already known and would overwrite the
// state with Unstable, replacing an actionable "authorize this" with a generic
// "something is wrong". Same treatment NeedsReauth already gets.
func TestPerformCheck_PendingVerification_StaysQuiet(t *testing.T) {
	cred := &fakeAdminCredStore{err: errors.New("admin credential unavailable")}
	m := &MCPManager{credStore: cred, logger: &MockLogger{}, clientMap: map[string]*schemas.MCPClientState{}}

	config := newPendingClientConfig("client-check-pending", schemas.MCPAuthTypePerUserOauth)
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStatePendingVerification,
	}

	checker := NewClientConnectionChecker(m, config.ID, time.Minute, false, &MockLogger{})
	next, onSteady := checker.performCheck()

	assert.Equal(t, 0, cred.callCount(), "no discovery may be attempted without the credential the admin has yet to supply")
	assert.Equal(t, schemas.MCPConnectionStatePendingVerification, m.clientMap[config.ID].State)
	assert.True(t, onSteady, "and the timer stays on the relaxed cadence, like NeedsReauth")
	assert.Equal(t, time.Minute, next)
}

// TestSetState_PendingVerification_NotOverwritten guards the same invariant at
// the single writer, so a check already in flight when a client is parked in
// pending_verification cannot land on it afterwards.
func TestSetState_PendingVerification_NotOverwritten(t *testing.T) {
	m := &MCPManager{logger: &MockLogger{}, clientMap: map[string]*schemas.MCPClientState{}}
	config := newPendingClientConfig("client-setstate-pending", schemas.MCPAuthTypeOauth)
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStatePendingVerification,
	}

	checker := NewClientConnectionChecker(m, config.ID, time.Minute, false, &MockLogger{})

	checker.setState(schemas.MCPConnectionStateUnstable, 0, schemas.MCPConnectionFailureStageListTools, errors.New("late failure"))
	assert.Equal(t, schemas.MCPConnectionStatePendingVerification, m.clientMap[config.ID].State)
	assert.Nil(t, m.clientMap[config.ID].LastFailure, "a dropped write must not leave a failure record behind either")

	checker.setState(schemas.MCPConnectionStateHealthy, 0, "", nil)
	assert.Equal(t, schemas.MCPConnectionStatePendingVerification, m.clientMap[config.ID].State,
		"a synthetic success must not silently promote an unauthorized client either")
}

// TestAddClient_TokenExchange_VerifiedWithZeroTools_IsNotReParked pins that a
// verified token-exchange client whose server exposes no tools is not sent
// back through admin verification on reload: the client's record of its admin
// step decides, not how many tools it stored.
func TestAddClient_TokenExchange_VerifiedWithZeroTools_IsNotReParked(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	t.Cleanup(func() { _ = m.Cleanup() })
	defer m.checkerManager.StopAll()

	config := newPendingClientConfig("client-tokenexchange-zero-tools", schemas.MCPAuthTypeTokenExchange)
	// AddClient validates the name; the sibling tests here bypass it by writing
	// into clientMap directly, so they never needed a conforming one.
	config.Name = "tokenexchangezerotools"
	config.PendingOAuthConfig = nil
	config.PendingVerification = false
	config.TokenExchange = &schemas.MCPTokenExchangeConfig{
		Audience: "https://example.invalid/api",
		ClientID: schemas.NewSecretVar("exchange-app"),
	}
	// Exactly what AfterFind produces for a verified server with no tools.
	config.DiscoveredTools = map[string]schemas.ChatTool{}

	require.NoError(t, m.AddClient(context.Background(), config))

	m.mu.RLock()
	state := m.clientMap[config.ID].State
	m.mu.RUnlock()

	assert.NotEqual(t, schemas.MCPConnectionStatePendingVerification, state,
		"a verified token-exchange client that simply exposes no tools must not be sent back through admin verification")
}

// TestAwaitsAdminVerification_ReadsTheClientsRecord states the rule directly,
// for every auth type: the client's record of its admin step decides, and
// neither its tools nor an OAuth stash stands in for it.
func TestAwaitsAdminVerification_ReadsTheClientsRecord(t *testing.T) {
	for _, authType := range []schemas.MCPAuthType{
		schemas.MCPAuthTypeOauth, schemas.MCPAuthTypePerUserOauth,
		schemas.MCPAuthTypePerUserHeaders, schemas.MCPAuthTypeTokenExchange,
		schemas.MCPAuthTypeHeaders,
	} {
		t.Run(string(authType), func(t *testing.T) {
			pending := &schemas.MCPClientConfig{AuthType: authType, PendingVerification: true, DiscoveredTools: map[string]schemas.ChatTool{"a": {}}}
			assert.True(t, awaitsAdminVerification(pending), "recorded as pending, whatever it holds")

			verified := &schemas.MCPClientConfig{AuthType: authType, PendingOAuthConfig: &schemas.OAuth2Config{}}
			assert.False(t, awaitsAdminVerification(verified), "no tools and a stash do not make a verified client pending")
		})
	}
	assert.False(t, awaitsAdminVerification(nil))
}

// newVerifiedClientConfig builds a client whose one-time admin verification has
// already run: DiscoveredTools is non-nil, which is the whole of what
// awaitsAdminVerification reads for these auth types.
func newVerifiedClientConfig(id string, authType schemas.MCPAuthType, discovered map[string]schemas.ChatTool) *schemas.MCPClientConfig {
	config := &schemas.MCPClientConfig{
		ID:                        id,
		Name:                      "verified" + id,
		AuthType:                  authType,
		ConnectionType:            schemas.MCPConnectionTypeHTTP,
		ConnectionString:          schemas.NewSecretVar("http://127.0.0.1:0/mcp"),
		DiscoveredTools:           discovered,
		DiscoveredToolNameMapping: map[string]string{},
	}
	if authType == schemas.MCPAuthTypeTokenExchange {
		config.TokenExchange = &schemas.MCPTokenExchangeConfig{
			Audience: "https://example.invalid/api",
			ClientID: schemas.NewSecretVar("exchange-app"),
		}
	}
	return config
}

// TestUpdateClient_VerifiedClientStaysVerified pins that editing a client does
// not un-verify it.
//
// awaitsAdminVerification reads the manager's in-memory config, and for
// per_user_headers and token_exchange the whole of what it reads is whether
// DiscoveredTools is nil. UpdateClient rebuilds that config field by field from
// two lists, "copy from the existing config" and "copy from the update", and
// DiscoveredTools was on neither, so every edit niled it. Nothing read it after
// registration until this predicate did, and from then on an ordinary edit
// (renaming the server, changing a timeout) left a verified, healthy client
// reading as never verified: enabling it parked it back in
// pending_verification, taking away a working client until an admin re-verified
// something they had already verified.
//
// The update handler is the reason the edit carries no DiscoveredTools: it
// builds the config it hands UpdateClient from the request, and only sets the
// field when the same request re-ran verification.
func TestUpdateClient_VerifiedClientStaysVerified(t *testing.T) {
	cases := []struct {
		name       string
		authType   schemas.MCPAuthType
		discovered map[string]schemas.ChatTool
	}{
		{
			name:       "per_user_headers",
			authType:   schemas.MCPAuthTypePerUserHeaders,
			discovered: map[string]schemas.ChatTool{"verifiedclient-lookup": {}},
		},
		{
			name:       "token_exchange",
			authType:   schemas.MCPAuthTypeTokenExchange,
			discovered: map[string]schemas.ChatTool{"verifiedclient-lookup": {}},
		},
		{
			// Verified, and the server exposes nothing. Still verified: nil-ness
			// is the discriminator, so the empty map has to survive as an empty
			// map rather than collapse to nil.
			name:       "verified with zero tools",
			authType:   schemas.MCPAuthTypePerUserHeaders,
			discovered: map[string]schemas.ChatTool{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			defer m.checkerManager.StopAll()

			config := newVerifiedClientConfig("client", tc.authType, tc.discovered)
			m.mu.Lock()
			m.clientMap[config.ID] = &schemas.MCPClientState{
				Name:            config.Name,
				ExecutionConfig: config,
				State:           schemas.MCPConnectionStateHealthy,
				ConnectionInfo:  &schemas.MCPClientConnectionInfo{Type: config.ConnectionType},
			}
			m.mu.Unlock()
			require.False(t, awaitsAdminVerification(config), "fixture check: the client starts out verified")

			// An ordinary edit, shaped the way the update handler shapes it: the
			// editable fields, and no DiscoveredTools.
			edit := *config
			edit.DiscoveredTools = nil
			edit.DiscoveredToolNameMapping = nil
			edit.ToolExecutionTimeout = 45 * time.Second
			require.NoError(t, m.UpdateClient(config.ID, &edit))

			m.mu.RLock()
			updated := m.clientMap[config.ID].ExecutionConfig
			m.mu.RUnlock()
			assert.Equal(t, 45*time.Second, updated.ToolExecutionTimeout, "fixture check: the edit itself applied")
			require.NotNil(t, updated.DiscoveredTools, "an edit must not discard the record that verification ran")
			assert.Equal(t, tc.discovered, updated.DiscoveredTools)
			assert.NotNil(t, updated.DiscoveredToolNameMapping)
			assert.False(t, awaitsAdminVerification(updated), "an edited client is still a verified client")

			// The symptom on this path: disable it, enable it, and it must come
			// back as the working client it was.
			require.NoError(t, m.DisableClient(config.ID))
			require.NoError(t, m.EnableClient(config.ID))
			m.mu.RLock()
			state := m.clientMap[config.ID].State
			m.mu.RUnlock()
			assert.NotEqual(t, schemas.MCPConnectionStatePendingVerification, state,
				"enabling an edited, verified client must not send it back through admin verification")
		})
	}
}

// TestUpdateClient_ReverifiedToolsReplaceTheOldOnes is the other half of the
// carry-forward rule. An update whose request re-ran verification arrives with
// the tools that run discovered, and those are the current truth: keeping the
// old set over them would pin a stale tool list to the config.
func TestUpdateClient_ReverifiedToolsReplaceTheOldOnes(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	defer m.checkerManager.StopAll()

	config := newVerifiedClientConfig("client", schemas.MCPAuthTypePerUserHeaders,
		map[string]schemas.ChatTool{"verifiedclient-old": {}})
	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateHealthy,
		ConnectionInfo:  &schemas.MCPClientConnectionInfo{Type: config.ConnectionType},
	}
	m.mu.Unlock()

	edit := *config
	edit.DiscoveredTools = map[string]schemas.ChatTool{"verifiedclient-new": {}}
	edit.DiscoveredToolNameMapping = map[string]string{"new": "new"}
	require.NoError(t, m.UpdateClient(config.ID, &edit))

	m.mu.RLock()
	updated := m.clientMap[config.ID].ExecutionConfig
	m.mu.RUnlock()
	assert.Equal(t, map[string]schemas.ChatTool{"verifiedclient-new": {}}, updated.DiscoveredTools)
	assert.Equal(t, map[string]string{"new": "new"}, updated.DiscoveredToolNameMapping)
}

// TestUpdateClient_RenameRebindsTheVerifiedTools pins the second job
// DiscoveredTools has. It is not only the "verification ran" marker: a per-call
// client's stored discovery is restored from it, keys and all. UpdateClient
// rebinds that discovery to the new name on a rename, so a verification record carried
// forward under the old name would disagree with it, and the next restore
// (enable, after a disable) would bring the tools back under a name the client
// no longer has.
func TestUpdateClient_RenameRebindsTheVerifiedTools(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	defer m.checkerManager.StopAll()

	lookup := schemas.ChatTool{Function: &schemas.ChatToolFunction{Name: "verifiedclient-lookup"}}
	config := newVerifiedClientConfig("client", schemas.MCPAuthTypePerUserHeaders,
		map[string]schemas.ChatTool{"verifiedclient-lookup": lookup})
	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateHealthy,
		ConnectionInfo:  &schemas.MCPClientConnectionInfo{Type: config.ConnectionType},
	}
	m.mu.Unlock()
	m.discoveryStore.SetDiscovery(context.Background(), config, &schemas.MCPDiscovery{
		Tools:           map[string]schemas.ChatTool{"verifiedclient-lookup": lookup},
		ToolNameMapping: map[string]string{},
	})

	edit := *config
	edit.Name = "renamedclient"
	edit.DiscoveredTools = nil
	edit.DiscoveredToolNameMapping = nil
	require.NoError(t, m.UpdateClient(config.ID, &edit))

	m.mu.RLock()
	updated := m.clientMap[config.ID].ExecutionConfig
	m.mu.RUnlock()
	require.Contains(t, updated.DiscoveredTools, "renamedclient-lookup", "the verification record follows the rename")
	assert.NotContains(t, updated.DiscoveredTools, "verifiedclient-lookup")
	assert.Equal(t, "renamedclient-lookup", updated.DiscoveredTools["renamedclient-lookup"].Function.Name)
	assert.Equal(t, "verifiedclient-lookup", lookup.Function.Name, "the tool the old config still points at is left as it was")

	// And the restore that reads it installs the tool under the current name.
	require.NoError(t, m.DisableClient(config.ID))
	require.NoError(t, m.EnableClient(config.ID))
	toolMap := m.discoveryStore.GetDiscovery(context.Background(), config).GetTools()
	assert.Contains(t, toolMap, "renamedclient-lookup")
	assert.NotContains(t, toolMap, "verifiedclient-lookup")
}

// TestSetClientDiscovery_RecordsTheVerificationOnTheConfig pins that a client
// verified while the process is running reads as verified while the process is
// running.
//
// SetClientDiscovery is what the admin verification handlers call once the admin's
// credential has been checked against the upstream: it installs the tools,
// marks the client healthy, and fires the callback that persists those same
// tools to the client's row. The handler records the verification on the row;
// this process reads the manager's in-memory config, so SetClientDiscovery must
// record it there too, or a server verified a moment ago went on reading as
// never verified: healthy, serving tools, refused by every path that consults
// the predicate, and parked back in pending_verification by a disable and enable.
//
// Nothing automatic reaches SetClientDiscovery for a client still awaiting its
// admin (the checker stays quiet for one and a refresh refuses it), so the only
// callers that ever move a pending client here are the admin's own.
func TestSetClientDiscovery_RecordsTheVerificationOnTheConfig(t *testing.T) {
	cases := []struct {
		name     string
		authType schemas.MCPAuthType
		tools    map[string]schemas.ChatTool
		// wantNames is compared rather than the tools themselves: an installed tool
		// carries its cached serialization, which a literal never has.
		wantNames []string
	}{
		{
			name:      "per_user_headers",
			authType:  schemas.MCPAuthTypePerUserHeaders,
			tools:     map[string]schemas.ChatTool{"verifiedclient-lookup": {}},
			wantNames: []string{"verifiedclient-lookup"},
		},
		{
			name:      "token_exchange",
			authType:  schemas.MCPAuthTypeTokenExchange,
			tools:     map[string]schemas.ChatTool{"verifiedclient-lookup": {}},
			wantNames: []string{"verifiedclient-lookup"},
		},
		{
			// A verification that found nothing is still a verification.
			name:      "verified with zero tools",
			authType:  schemas.MCPAuthTypePerUserHeaders,
			tools:     nil,
			wantNames: []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			defer m.checkerManager.StopAll()

			config := newVerifiedClientConfig("client", tc.authType, nil)
			config.DiscoveredToolNameMapping = nil
			config.PendingVerification = true
			require.True(t, awaitsAdminVerification(config), "fixture check: the client starts out awaiting its admin")
			m.mu.Lock()
			m.clientMap[config.ID] = &schemas.MCPClientState{
				Name:            config.Name,
				ExecutionConfig: config,
				State:           schemas.MCPConnectionStatePendingVerification,
				ConnectionInfo:  &schemas.MCPClientConnectionInfo{Type: config.ConnectionType},
			}
			m.mu.Unlock()

			// What the verify handlers do once the admin's credential checks out.
			m.SetClientDiscovery(config.ID, &schemas.MCPDiscovery{Tools: tc.tools, ToolNameMapping: map[string]string{"lookup": "lookup"}})

			m.mu.RLock()
			recorded := m.clientMap[config.ID].ExecutionConfig
			state := m.clientMap[config.ID].State
			m.mu.RUnlock()
			require.Equal(t, schemas.MCPConnectionStateHealthy, state, "fixture check: verification marks the client healthy")
			require.NotNil(t, recorded.DiscoveredTools, "a verification that just ran must be on the config the predicate reads")
			assert.Equal(t, tc.wantNames, slices.AppendSeq([]string{}, maps.Keys(recorded.DiscoveredTools)))
			assert.Equal(t, map[string]string{"lookup": "lookup"}, recorded.DiscoveredToolNameMapping)
			assert.False(t, recorded.PendingVerification, "the verification is recorded on the config the predicate reads")
			assert.False(t, awaitsAdminVerification(recorded), "a client verified in this process is verified in this process")

			// Recorded by swapping the config, never by writing into it: readers
			// may still hold the old pointer.
			assert.NotSame(t, config, recorded)
			assert.Nil(t, config.DiscoveredTools, "the config a concurrent reader may still hold is left as it was")
			assert.True(t, config.PendingVerification)

			// The symptom on this path: disable it, enable it, and it must come
			// back as the working client it was.
			require.NoError(t, m.DisableClient(config.ID))
			require.NoError(t, m.EnableClient(config.ID))
			m.mu.RLock()
			state = m.clientMap[config.ID].State
			m.mu.RUnlock()
			assert.NotEqual(t, schemas.MCPConnectionStatePendingVerification, state,
				"enabling a client verified moments ago must not send it back through admin verification")
		})
	}
}

// TestUpdateClient_KeepsTheClientsVerificationRecord: an edit is not the admin
// step, so it neither clears nor sets the client's record of it. An ordinary
// edit arrives without the record (and without the OAuth stash), and taking that
// as "verified" un-parked a client still waiting for its admin.
func TestUpdateClient_KeepsTheClientsVerificationRecord(t *testing.T) {
	for _, pending := range []bool{true, false} {
		t.Run(fmt.Sprintf("pending=%v", pending), func(t *testing.T) {
			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			defer m.checkerManager.StopAll()

			config := newVerifiedClientConfig("client-edit", schemas.MCPAuthTypePerUserHeaders, map[string]schemas.ChatTool{})
			config.Name = "clientedit"
			config.PerUserHeaderKeys = []string{"x-api-key"}
			config.PendingVerification = pending
			state := schemas.MCPConnectionStateHealthy
			if pending {
				state = schemas.MCPConnectionStatePendingVerification
			}
			m.mu.Lock()
			m.clientMap[config.ID] = &schemas.MCPClientState{
				Name:            config.Name,
				ExecutionConfig: config,
				State:           state,
				ConnectionInfo:  &schemas.MCPClientConnectionInfo{Type: config.ConnectionType},
			}
			m.mu.Unlock()

			edit := *config
			edit.ToolsToExecute = []string{"*"}
			edit.PendingVerification = !pending
			require.NoError(t, m.UpdateClient(config.ID, &edit))

			m.mu.RLock()
			updated := m.clientMap[config.ID].ExecutionConfig
			m.mu.RUnlock()
			assert.Equal(t, []string{"*"}, []string(updated.ToolsToExecute), "fixture check: the edit applied")
			assert.Equal(t, pending, awaitsAdminVerification(updated), "only the admin step changes the record")
		})
	}
}

// TestUpdateClientCredentials_RecordsACompletedAdminStep: the config recording
// a client's completed admin step is handed over through the credential update
// (a client with per_user_discovery on has no tools to set instead). A pending
// client moves out of pending_verification. A disabled one stays disabled, but
// is no longer read as pending, so enabling it connects it instead of parking
// it again behind the step it already passed.
func TestUpdateClientCredentials_RecordsACompletedAdminStep(t *testing.T) {
	for _, tc := range []struct {
		name     string
		authType schemas.MCPAuthType
		perUser  bool
		state    schemas.MCPConnectionState
	}{
		{"per_user_discovery pending", schemas.MCPAuthTypePerUserOauth, true, schemas.MCPConnectionStatePendingVerification},
		{"per_user_discovery disabled", schemas.MCPAuthTypePerUserOauth, true, schemas.MCPConnectionStateDisabled},
		{"shared oauth disabled", schemas.MCPAuthTypeOauth, false, schemas.MCPConnectionStateDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
			defer m.checkerManager.StopAll()

			config := newPendingClientConfig("client-verified", tc.authType)
			config.PerUserDiscovery = schemas.Ptr(tc.perUser)
			config.Disabled = tc.state == schemas.MCPConnectionStateDisabled
			m.mu.Lock()
			m.clientMap[config.ID] = &schemas.MCPClientState{
				Name:            config.Name,
				ExecutionConfig: config,
				State:           tc.state,
				ConnectionInfo:  &schemas.MCPClientConnectionInfo{Type: config.ConnectionType},
			}
			m.mu.Unlock()

			verified := *config
			verified.PendingOAuthConfig = nil
			verified.PendingVerification = false
			require.NoError(t, m.UpdateClientCredentials(config.ID, &verified))

			m.mu.RLock()
			state := m.clientMap[config.ID].State
			recorded := m.clientMap[config.ID].ExecutionConfig
			m.mu.RUnlock()
			assert.False(t, awaitsAdminVerification(recorded))
			assert.NotSame(t, config, recorded, "recorded by swapping the config: readers may still hold the old pointer")
			if tc.state == schemas.MCPConnectionStateDisabled {
				assert.Equal(t, schemas.MCPConnectionStateDisabled, state, "the step does not enable it")
				require.NoError(t, m.EnableClient(config.ID))
				m.mu.RLock()
				state = m.clientMap[config.ID].State
				m.mu.RUnlock()
			}
			assert.Equal(t, schemas.MCPConnectionStateHealthy, state)
			if tc.perUser {
				assert.Empty(t, storedTools(m, config.ID), "nothing is stored for a client that keeps a discovery per caller")
			}
		})
	}
}

// TestPerUserDiscoveryClient_GetsNoAdminDiscoveryOrChecker: a client with
// per_user_discovery on keeps no admin credential, so nothing may try to
// discover or health-check with one. Enabling it and completing its first
// connection start no checker and run no admin discovery.
func TestPerUserDiscoveryClient_GetsNoAdminDiscoveryOrChecker(t *testing.T) {
	newManager := func(t *testing.T) (*MCPManager, *fakeAdminCredStore, *schemas.MCPClientConfig) {
		cred := &fakeAdminCredStore{err: errors.New("no admin credential is kept")}
		m := NewMCPManager(context.Background(), schemas.MCPConfig{}, cred, &MockLogger{}, nil)
		t.Cleanup(m.checkerManager.StopAll)
		config := newVerifiedClientConfig("client-per-user", schemas.MCPAuthTypePerUserOauth, nil)
		config.PerUserDiscovery = schemas.Ptr(true)
		return m, cred, config
	}
	hasChecker := func(m *MCPManager, id string) bool {
		m.checkerManager.mu.RLock()
		defer m.checkerManager.mu.RUnlock()
		_, ok := m.checkerManager.checkers[id]
		return ok
	}

	t.Run("enable", func(t *testing.T) {
		m, cred, config := newManager(t)
		config.Disabled = true
		m.mu.Lock()
		m.clientMap[config.ID] = &schemas.MCPClientState{Name: config.Name, ExecutionConfig: config, State: schemas.MCPConnectionStateDisabled, ConnectionInfo: &schemas.MCPClientConnectionInfo{Type: config.ConnectionType}}
		m.mu.Unlock()

		require.NoError(t, m.EnableClient(config.ID))
		assert.False(t, hasChecker(m, config.ID))
		assert.Zero(t, cred.callCount())
	})

	t.Run("first connection after the admin step", func(t *testing.T) {
		m, cred, config := newManager(t)
		config.PendingVerification = true
		m.mu.Lock()
		m.clientMap[config.ID] = &schemas.MCPClientState{Name: config.Name, ExecutionConfig: config, State: schemas.MCPConnectionStatePendingVerification, ConnectionInfo: &schemas.MCPClientConnectionInfo{Type: config.ConnectionType}}
		m.mu.Unlock()

		verified := *config
		verified.PendingVerification = false
		require.NoError(t, m.UpdateClientCredentials(config.ID, &verified))
		assert.False(t, hasChecker(m, config.ID))
		assert.Zero(t, cred.callCount())
	})

	t.Run("a client without the flag still gets a checker", func(t *testing.T) {
		m, _, config := newManager(t)
		config.PerUserDiscovery = nil
		config.Disabled = true
		m.mu.Lock()
		m.clientMap[config.ID] = &schemas.MCPClientState{Name: config.Name, ExecutionConfig: config, State: schemas.MCPConnectionStateDisabled, ConnectionInfo: &schemas.MCPClientConnectionInfo{Type: config.ConnectionType}}
		m.mu.Unlock()

		require.NoError(t, m.EnableClient(config.ID))
		assert.True(t, hasChecker(m, config.ID))
	})
}

// TestReconnectClient_RefusesAClientStillPendingVerification: a client recorded
// as still needing its admin sign-in has no credential to connect with, so a
// reconnect is refused rather than flipping it out of pending_verification.
func TestReconnectClient_RefusesAClientStillPendingVerification(t *testing.T) {
	m := NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &MockLogger{}, nil)
	defer m.checkerManager.StopAll()

	config := newPendingClientConfig("client-reconnect-pending", schemas.MCPAuthTypeOauth)
	config.PendingOAuthConfig = nil
	config.NeedsSessionStickiness = schemas.Ptr(true)
	m.mu.Lock()
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStatePendingVerification,
		ConnectionInfo:  &schemas.MCPClientConnectionInfo{Type: config.ConnectionType},
	}
	m.mu.Unlock()

	err := m.ReconnectClient(config.ID)
	require.ErrorIs(t, err, schemas.ErrMCPReconnectNotApplicable)
	m.mu.RLock()
	state := m.clientMap[config.ID].State
	m.mu.RUnlock()
	assert.Equal(t, schemas.MCPConnectionStatePendingVerification, state)
}
