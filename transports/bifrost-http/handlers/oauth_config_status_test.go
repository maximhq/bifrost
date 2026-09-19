package handlers

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// TestResolveOAuthConfigStatus pins the status the
// GET /api/oauth/config/{id}/status endpoint reports while a reauthorization
// is in flight.
//
// The endpoint echoes TableOauthConfig.Status, which is the one-time bootstrap
// lifecycle only: after a first successful auth the row reads "authorized"
// forever, and InitiateUserOAuthFlow never touches it when a reauth starts
// (it rotates the flow row to "pending" instead). So during a reauth the
// authorizer UI's first 2s poll tick saw the stale "authorized", closed the
// consent popup before the user could approve, and fired complete-oauth —
// which completeMCPClientOAuth then rejected with 409, because
// isPrematureOAuthCompletion correctly read the still-pending flow row. The UI
// treats that 409 as a raced double-submit and shows a success toast, so the
// user is told the client was re-authorized while it stays in needs_reauth and
// the upstream token is never refreshed.
//
// The flow row is the only honest source during a reauth — the same reason
// isPrematureOAuthCompletion consults it — so a pending flow has to win over
// the stale config status.
func TestResolveOAuthConfigStatus(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	tests := []struct {
		name         string
		configStatus string
		flow         *tables.TableMCPOauthFlow
		want         string
	}{
		{
			name:         "stale authorized with a reauth in flight reports the flow's pending",
			configStatus: "authorized",
			flow:         &tables.TableMCPOauthFlow{Status: "pending", ExpiresAt: future},
			want:         "pending",
		},
		{
			name:         "authorized with no flow row (a real completion cleaned it up) stays authorized",
			configStatus: "authorized",
			flow:         nil,
			want:         "authorized",
		},
		{
			name:         "authorized with an abandoned, expired pending row stays authorized",
			configStatus: "authorized",
			flow:         &tables.TableMCPOauthFlow{Status: "pending", ExpiresAt: past},
			want:         "authorized",
		},
		{
			name:         "authorized with a resolved-but-unsuccessful row stays authorized",
			configStatus: "authorized",
			flow:         &tables.TableMCPOauthFlow{Status: "failed", ExpiresAt: future},
			want:         "authorized",
		},
		{
			name:         "a first auth already reporting pending is unchanged",
			configStatus: "pending",
			flow:         &tables.TableMCPOauthFlow{Status: "pending", ExpiresAt: future},
			want:         "pending",
		},
		{
			name:         "a failed config status is not masked by a flow row",
			configStatus: "failed",
			flow:         &tables.TableMCPOauthFlow{Status: "pending", ExpiresAt: future},
			want:         "failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveOAuthConfigStatus(tt.configStatus, tt.flow, now)
			if got != tt.want {
				t.Errorf("resolveOAuthConfigStatus(%q, %+v, %v) = %q, want %q",
					tt.configStatus, tt.flow, now, got, tt.want)
			}
		})
	}
}
