package configstore

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
)

func TestAgentRegistrationPersistenceAndReload(t *testing.T) {
	t.Setenv("AGENT_DISCOVERY_TOKEN", "resolved-discovery-secret")
	ctx := context.Background()
	store := setupRDBTestStore(t)
	for _, id := range []string{"vk-2", "vk-1"} {
		require.NoError(t, store.CreateVirtualKey(ctx, &tables.TableVirtualKey{ID: id, Name: "Key " + id, Value: *schemas.NewSecretVar("value-" + id), IsActive: schemas.Ptr(true)}))
	}
	reg := &schemas.AgentRegistration{
		Name:                                   "fixture",
		AgentCardURL:                           "https://example.com",
		Enabled:                                true,
		ForwardAcceptedCredential:              true,
		ForwardAcceptedCredentialOverridesAuth: true,
		DiscoveryAuth: &schemas.UpstreamAuth{
			Type:    schemas.MCPAuthTypeHeaders,
			Headers: map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar("env.AGENT_DISCOVERY_TOKEN")},
		},
		RuntimeAuth: &schemas.UpstreamAuth{
			Type: schemas.MCPAuthTypeOauth,
			OAuth: &schemas.UpstreamOAuthConfig{
				TokenURL:     "https://oauth.example/token",
				ClientID:     schemas.NewSecretVar("runtime-client"),
				ClientSecret: schemas.NewSecretVar("runtime-secret"),
				Scopes:       []string{"agent.read"},
			},
		},
		VirtualKeyIDs: []string{"vk-2", "vk-1"},
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	require.NoError(t, store.CreateAgentRegistration(ctx, reg))
	discoveryHeaderBeforeSave := reg.DiscoveryAuth.Headers["Authorization"]
	require.Equal(t, "resolved-discovery-secret", discoveryHeaderBeforeSave.GetValue())
	require.Equal(t, "runtime-client", reg.RuntimeAuth.OAuth.ClientID.GetValue())
	require.Equal(t, "runtime-secret", reg.RuntimeAuth.OAuth.ClientSecret.GetValue())
	require.Equal(t, []string{"agent.read"}, reg.RuntimeAuth.OAuth.Scopes)
	got, err := store.GetAgentRegistration(ctx, reg.Name)
	require.NoError(t, err)
	require.Equal(t, []string{"vk-1", "vk-2"}, got.VirtualKeyIDs)
	require.True(t, got.ForwardAcceptedCredential)
	require.True(t, got.ForwardAcceptedCredentialOverridesAuth)
	require.True(t, got.Redacted().ForwardAcceptedCredential)
	require.True(t, got.Redacted().ForwardAcceptedCredentialOverridesAuth)
	listed, err := store.ListAgentRegistrations(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"vk-1", "vk-2"}, listed[0].VirtualKeyIDs)
	require.Equal(t, "runtime-secret", got.RuntimeAuth.OAuth.ClientSecret.GetValue())
	view := got.Redacted()
	discoveryHeader := view.DiscoveryAuth.Headers["Authorization"]
	require.Equal(t, schemas.MCPAuthTypeHeaders, view.DiscoveryAuth.Type)
	require.Equal(t, schemas.SecretTypeEnv, discoveryHeader.Type())
	require.Equal(t, "env.AGENT_DISCOVERY_TOKEN", discoveryHeader.GetRawRef())
	require.Equal(t, "<REDACTED>", discoveryHeader.GetValue())
	require.Equal(t, schemas.MCPAuthTypeOauth, view.RuntimeAuth.Type)
	require.Equal(t, "<REDACTED>", view.RuntimeAuth.OAuth.ClientID.GetValue())
	require.Equal(t, "<REDACTED>", view.RuntimeAuth.OAuth.ClientSecret.GetValue())
	viewJSON, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(viewJSON), "runtime-secret")
	require.NotContains(t, string(viewJSON), "resolved-discovery-secret")
	require.Contains(t, string(viewJSON), `"ref":"env.AGENT_DISCOVERY_TOKEN"`)
	require.NotContains(t, string(viewJSON), "auth_configured")
	duplicate := *reg
	duplicate.Name = "duplicate-grants"
	duplicate.VirtualKeyIDs = []string{"vk-1", "vk-1"}
	require.EqualError(t, store.CreateAgentRegistration(ctx, &duplicate), "duplicate virtual key ID: vk-1")
	_, err = store.GetAgentRegistration(ctx, duplicate.Name)
	require.ErrorIs(t, err, ErrNotFound)
	require.NoError(t, store.DeleteAgentRegistration(ctx, reg.Name))
	_, err = store.GetAgentRegistration(ctx, reg.Name)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestUpdateAgentRegistrationEncryptsUpdatedCredentials(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)
	now := time.Now().UTC()
	require.NoError(t, store.CreateAgentRegistration(ctx, &schemas.AgentRegistration{
		Name:         "encrypted-update",
		AgentCardURL: "https://example.com",
		Enabled:      true,
		RuntimeAuth: &schemas.UpstreamAuth{
			Type: schemas.MCPAuthTypeHeaders,
			Headers: map[string]schemas.SecretVar{
				"Authorization": *schemas.NewSecretVar("old-secret"),
			},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}))

	updatedAt := now.Add(time.Minute)
	require.NoError(t, store.UpdateAgentRegistration(ctx, &schemas.AgentRegistration{
		Name:         "encrypted-update",
		AgentCardURL: "https://example.com",
		Enabled:      true,
		RuntimeAuth: &schemas.UpstreamAuth{
			Type: schemas.MCPAuthTypeHeaders,
			Headers: map[string]schemas.SecretVar{
				"Authorization": *schemas.NewSecretVar("updated-secret"),
			},
		},
		CreatedAt: now,
		UpdatedAt: updatedAt,
	}))

	var raw struct {
		RuntimeAuth      string
		EncryptionStatus string
	}
	require.NoError(t, store.DB().Table("config_agent_registrations").Select("runtime_auth", "encryption_status").Where("name = ?", "encrypted-update").Scan(&raw).Error)
	require.Equal(t, tables.EncryptionStatusEncrypted, raw.EncryptionStatus)
	require.NotContains(t, raw.RuntimeAuth, "updated-secret")

	got, err := store.GetAgentRegistration(ctx, "encrypted-update")
	require.NoError(t, err)
	authorization := got.RuntimeAuth.Headers["Authorization"]
	require.Equal(t, "updated-secret", authorization.GetValue())
}

func TestUpdateAgentRegistrationReplacesMutableFieldsAndGrants(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)
	for _, id := range []string{"old", "new-1", "new-2"} {
		require.NoError(t, store.CreateVirtualKey(ctx, &tables.TableVirtualKey{ID: id, Name: "Key " + id, Value: *schemas.NewSecretVar("value-" + id), IsActive: schemas.Ptr(true)}))
	}
	created := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, store.CreateAgentRegistration(ctx, &schemas.AgentRegistration{Name: "fixture", AgentCardURL: "https://old.example", Enabled: true, ForwardAcceptedCredential: true, VirtualKeyIDs: []string{"old"}, CreatedAt: created, UpdatedAt: created}))
	updated := time.Now().UTC()
	require.NoError(t, store.UpdateAgentRegistration(ctx, &schemas.AgentRegistration{Name: "fixture", AgentCardURL: "https://new.example", Tenant: "new", Enabled: false, VirtualKeyIDs: []string{"new-1", "new-2"}, CreatedAt: updated, UpdatedAt: updated}))
	got, err := store.GetAgentRegistration(ctx, "fixture")
	require.NoError(t, err)
	require.Equal(t, "https://new.example", got.AgentCardURL)
	require.Equal(t, "new", got.Tenant)
	require.False(t, got.Enabled)
	require.False(t, got.ForwardAcceptedCredential)
	require.False(t, got.ForwardAcceptedCredentialOverridesAuth)
	require.Equal(t, []string{"new-1", "new-2"}, got.VirtualKeyIDs)
	require.Equal(t, created, got.CreatedAt)
	failed := *got
	failed.AgentCardURL = "https://must-not-persist.example"
	failed.VirtualKeyIDs = []string{"new-1", "new-1"}
	require.EqualError(t, store.UpdateAgentRegistration(ctx, &failed), "duplicate virtual key ID: new-1")
	failed.VirtualKeyIDs = []string{"missing"}
	require.ErrorContains(t, store.UpdateAgentRegistration(ctx, &failed), "virtual key IDs were not found: missing")
	preserved, err := store.GetAgentRegistration(ctx, "fixture")
	require.NoError(t, err)
	require.Equal(t, "https://new.example", preserved.AgentCardURL)
	require.Equal(t, []string{"new-1", "new-2"}, preserved.VirtualKeyIDs)
	require.ErrorIs(t, store.UpdateAgentRegistration(ctx, &schemas.AgentRegistration{Name: "missing"}), ErrNotFound)
}

// grantNames extracts the agent names from a virtual key's serialized agent grants.
// The preload does not impose an ordering, so names are sorted here to keep
// assertions deterministic while comparing set membership.
func grantNames(vk *tables.TableVirtualKey) []string {
	names := make([]string, 0, len(vk.AgentGrants))
	for _, grant := range vk.AgentGrants {
		names = append(names, grant.AgentName)
	}
	sort.Strings(names)
	return names
}

func TestReplaceVirtualKeyAgentGrants(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)
	vk := &tables.TableVirtualKey{ID: "vk-agent-grants", Name: "Agent Grants", Value: *schemas.NewSecretVar("vk-agent-grants-value"), IsActive: schemas.Ptr(true)}
	require.NoError(t, store.CreateVirtualKey(ctx, vk))
	for _, name := range []string{"alpha", "beta"} {
		require.NoError(t, store.CreateAgentRegistration(ctx, &schemas.AgentRegistration{Name: name, AgentCardURL: "https://example.com/" + name, Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}))
	}

	require.NoError(t, store.ReplaceVirtualKeyAgentGrants(ctx, vk.ID, []string{"beta", "alpha"}))
	stored, err := store.GetVirtualKey(ctx, vk.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "beta"}, grantNames(stored))
	alpha, err := store.GetAgentRegistration(ctx, "alpha")
	require.NoError(t, err)
	require.Equal(t, []string{vk.ID}, alpha.VirtualKeyIDs)

	for _, test := range []struct {
		name  string
		names []string
		text  string
	}{
		{name: "empty", names: []string{""}, text: "agent name cannot be empty"},
		{name: "duplicate", names: []string{"alpha", "alpha"}, text: "duplicate agent name: alpha"},
		{name: "unknown", names: []string{"missing"}, text: "agent names were not found: missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := store.ReplaceVirtualKeyAgentGrants(ctx, vk.ID, test.names)
			require.ErrorIs(t, err, ErrInvalidAgentGrant)
			require.ErrorContains(t, err, test.text)
		})
	}
	stored, err = store.GetVirtualKey(ctx, vk.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "beta"}, grantNames(stored), "failed replacement must preserve existing grants")

	require.NoError(t, store.ReplaceVirtualKeyAgentGrants(ctx, vk.ID, []string{}))
	stored, err = store.GetVirtualKey(ctx, vk.ID)
	require.NoError(t, err)
	require.Empty(t, grantNames(stored))
}

func TestDeleteVirtualKeyCleansUpAgentGrants(t *testing.T) {
	ctx := context.Background()
	store := setupRDBTestStore(t)
	vk := &tables.TableVirtualKey{ID: "vk-agent-delete", Name: "Agent Delete", Value: *schemas.NewSecretVar("vk-agent-delete-value"), IsActive: schemas.Ptr(true)}
	require.NoError(t, store.CreateVirtualKey(ctx, vk))
	now := time.Now().UTC()
	require.NoError(t, store.CreateAgentRegistration(ctx, &schemas.AgentRegistration{Name: "fixture", AgentCardURL: "https://example.com", Enabled: true, VirtualKeyIDs: []string{vk.ID}, CreatedAt: now, UpdatedAt: now}))

	require.NoError(t, store.DeleteVirtualKey(ctx, vk.ID))
	var count int64
	require.NoError(t, store.DB().Model(&tables.TableVirtualKeyAgentGrant{}).Where("virtual_key_id = ?", vk.ID).Count(&count).Error)
	require.Zero(t, count)
}
