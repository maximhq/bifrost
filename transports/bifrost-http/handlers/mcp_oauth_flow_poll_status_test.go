package handlers

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// TestResolveOAuthFlowPollStatus pins what a reauthorize poll must report.
// The dashboard dialog polls /api/oauth/config/{id}/status every couple of
// seconds after opening the consent popup, and closes the popup the moment
// it reads "authorized". A reauthorizing client's oauth_configs row has been
// "authorized" since its original bootstrap and never regresses, so a poll
// that only read the config status would close the popup on its first tick,
// before the admin had signed in upstream. With the flow row folded in, the
// poll stays "pending" until the callback actually completes the flow.
func TestResolveOAuthFlowPollStatus(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	future := now.Add(10 * time.Minute)
	past := now.Add(-time.Minute)

	tests := []struct {
		name         string
		flow         *tables.TableMCPOauthFlow
		configStatus string
		want         string
	}{
		{
			name:         "reauthorize in flight: pending flow beats the config's stale authorized",
			flow:         &tables.TableMCPOauthFlow{Status: "pending", ExpiresAt: future},
			configStatus: "authorized",
			want:         "pending",
		},
		{
			name:         "callback being processed (claiming) is still pending",
			flow:         &tables.TableMCPOauthFlow{Status: "claiming", ExpiresAt: future},
			configStatus: "authorized",
			want:         "pending",
		},
		{
			name:         "pending flow past its deadline: the callback never came",
			flow:         &tables.TableMCPOauthFlow{Status: "pending", ExpiresAt: past},
			configStatus: "authorized",
			want:         "expired",
		},
		{
			name:         "upstream denied or errored before redirecting back",
			flow:         &tables.TableMCPOauthFlow{Status: "failed", ExpiresAt: future},
			configStatus: "authorized",
			want:         "failed",
		},
		{
			name:         "flow cleaned up by a successful completion: config says authorized",
			flow:         nil,
			configStatus: "authorized",
			want:         "authorized",
		},
		{
			name:         "flow cleaned up by a failed token exchange: config says failed",
			flow:         nil,
			configStatus: "failed",
			want:         "failed",
		},
		{
			name:         "flow gone while the config never left pending: swept bootstrap, not something to wait on",
			flow:         nil,
			configStatus: "pending",
			want:         "expired",
		},
		{
			name:         "bootstrap flow still pending reads pending regardless of config",
			flow:         &tables.TableMCPOauthFlow{Status: "pending", ExpiresAt: future},
			configStatus: "pending",
			want:         "pending",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveOAuthFlowPollStatus(tt.flow, tt.configStatus, now)
			if got != tt.want {
				t.Errorf("resolveOAuthFlowPollStatus(%+v, %q, %v) = %q, want %q", tt.flow, tt.configStatus, now, got, tt.want)
			}
		})
	}
}
