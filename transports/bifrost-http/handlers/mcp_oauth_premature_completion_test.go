package handlers

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// TestIsPrematureOAuthCompletion pins the signal that catches a
// shared-OAuth reauthorize completing without its callback ever actually
// running (e.g. the upstream/mock authorization server rejected the request
// before redirecting back — a dead-end error page, not an OAuth redirect).
// TableOauthConfig.Status can't be used for this: it's write-once and never
// regresses once "authorized" (see its own doc comment), so a client
// reauthorizing after an earlier successful auth always reads stale
// "authorized" throughout a failed attempt. CompleteOAuthFlow always cleans
// up the flow row on completion, success or failure alike — so a row still
// sitting there pending (and not yet expired) means the callback never ran
// at all, and completion must be rejected rather than reconnecting with
// whatever (possibly still-broken) credential was already on file.
func TestIsPrematureOAuthCompletion(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	tests := []struct {
		name string
		flow *tables.TableMCPOauthFlow
		want bool
	}{
		{
			name: "nil flow (already cleaned up by a genuine completion) is not premature",
			flow: nil,
			want: false,
		},
		{
			name: "pending, unexpired flow means the callback never ran: premature",
			flow: &tables.TableMCPOauthFlow{Status: "pending", ExpiresAt: future},
			want: true,
		},
		{
			name: "pending but expired flow (abandoned attempt) does not block a later fresh completion",
			flow: &tables.TableMCPOauthFlow{Status: "pending", ExpiresAt: past},
			want: false,
		},
		{
			name: "failed flow row is not premature (already resolved, just unsuccessfully)",
			flow: &tables.TableMCPOauthFlow{Status: "failed", ExpiresAt: future},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPrematureOAuthCompletion(tt.flow, now)
			if got != tt.want {
				t.Errorf("isPrematureOAuthCompletion(%+v, %v) = %v, want %v", tt.flow, now, got, tt.want)
			}
		})
	}
}
