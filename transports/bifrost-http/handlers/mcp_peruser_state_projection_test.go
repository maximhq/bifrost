package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestProjectPerUserAdminCredentialState table-tests the response-only
// needs_reauth projection the registry list applies to per-user clients when
// the retained admin discovery credential needs repair. The projection must
// only ever flip a connected per-user client to needs_reauth: any other
// runtime state passes through untouched (a disconnected or pending client
// has bigger problems than a stale discovery credential), shared-connection
// auth types are never projected (their needs_reauth comes from the runtime
// manager itself), and a missing admin row ("" status) leaves the state
// alone because clients verified before credential retention existed have
// no admin row and are healthy.
func TestProjectPerUserAdminCredentialState(t *testing.T) {
	connected := schemas.MCPConnectionStateHealthy
	needsReauth := schemas.MCPConnectionStateNeedsReauth

	tests := []struct {
		name             string
		authType         schemas.MCPAuthType
		runtimeState     schemas.MCPConnectionState
		adminTokenStatus string
		adminCredStatus  string
		want             schemas.MCPConnectionState
	}{
		{
			name:     "per_user_oauth connected with dead admin token projects needs_reauth",
			authType: schemas.MCPAuthTypePerUserOauth, runtimeState: connected,
			adminTokenStatus: "needs_reauth", want: needsReauth,
		},
		{
			name:     "per_user_oauth connected with active admin token stays connected",
			authType: schemas.MCPAuthTypePerUserOauth, runtimeState: connected,
			adminTokenStatus: "active", want: connected,
		},
		{
			name:     "per_user_oauth connected with no admin token stays connected (pre-retention client)",
			authType: schemas.MCPAuthTypePerUserOauth, runtimeState: connected,
			adminTokenStatus: "", want: connected,
		},
		{
			name:     "per_user_oauth connected with orphaned admin token stays connected",
			authType: schemas.MCPAuthTypePerUserOauth, runtimeState: connected,
			adminTokenStatus: "orphaned", want: connected,
		},
		{
			name:     "per_user_oauth ignores the header credential status",
			authType: schemas.MCPAuthTypePerUserOauth, runtimeState: connected,
			adminCredStatus: "needs_update", want: connected,
		},
		{
			name:     "per_user_headers connected with stale admin credential projects needs_reauth",
			authType: schemas.MCPAuthTypePerUserHeaders, runtimeState: connected,
			adminCredStatus: "needs_update", want: needsReauth,
		},
		{
			name:     "per_user_headers connected with active admin credential stays connected",
			authType: schemas.MCPAuthTypePerUserHeaders, runtimeState: connected,
			adminCredStatus: "active", want: connected,
		},
		{
			name:     "per_user_headers connected with no admin credential stays connected (pre-retention client)",
			authType: schemas.MCPAuthTypePerUserHeaders, runtimeState: connected,
			adminCredStatus: "", want: connected,
		},
		{
			name:     "per_user_headers ignores the oauth token status",
			authType: schemas.MCPAuthTypePerUserHeaders, runtimeState: connected,
			adminTokenStatus: "needs_reauth", want: connected,
		},
		{
			name:     "pending_verification runtime state passes through even with a dead admin token",
			authType: schemas.MCPAuthTypePerUserOauth, runtimeState: schemas.MCPConnectionStatePendingVerification,
			adminTokenStatus: "needs_reauth", want: schemas.MCPConnectionStatePendingVerification,
		},
		{
			name:     "disabled runtime state passes through even with a stale admin credential",
			authType: schemas.MCPAuthTypePerUserHeaders, runtimeState: schemas.MCPConnectionStateDisabled,
			adminCredStatus: "needs_update", want: schemas.MCPConnectionStateDisabled,
		},
		{
			name:     "per_user_oauth unstable with a dead admin token projects needs_reauth (needs_reauth is a bigger indicator than unstable)",
			authType: schemas.MCPAuthTypePerUserOauth, runtimeState: schemas.MCPConnectionStateUnstable,
			adminTokenStatus: "needs_reauth", want: needsReauth,
		},
		{
			name:     "per_user_headers unstable with a stale admin credential projects needs_reauth",
			authType: schemas.MCPAuthTypePerUserHeaders, runtimeState: schemas.MCPConnectionStateUnstable,
			adminCredStatus: "needs_update", want: needsReauth,
		},
		{
			name:     "per_user_oauth unstable with an active admin token stays unstable",
			authType: schemas.MCPAuthTypePerUserOauth, runtimeState: schemas.MCPConnectionStateUnstable,
			adminTokenStatus: "active", want: schemas.MCPConnectionStateUnstable,
		},
		{
			name:     "shared oauth clients are never projected",
			authType: schemas.MCPAuthTypeOauth, runtimeState: connected,
			adminTokenStatus: "needs_reauth", adminCredStatus: "needs_update", want: connected,
		},
		{
			name:     "non-auth clients are never projected",
			authType: schemas.MCPAuthTypeNone, runtimeState: connected,
			adminTokenStatus: "needs_reauth", adminCredStatus: "needs_update", want: connected,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := projectPerUserAdminCredentialState(tt.authType, tt.runtimeState, tt.adminTokenStatus, tt.adminCredStatus)
			if got != tt.want {
				t.Errorf("projectPerUserAdminCredentialState(%q, %q, %q, %q) = %q, want %q",
					tt.authType, tt.runtimeState, tt.adminTokenStatus, tt.adminCredStatus, got, tt.want)
			}
		})
	}
}
