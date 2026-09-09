package handlers

import "testing"

// TestIsConcurrentVerifyReplay table-tests the guard that decides whether a
// reloaded row's DiscoveredTools reflects a genuinely concurrent
// verify-headers request finishing this client's one-time bootstrap during
// this request's own upstream round-trip (the only case that should
// short-circuit as success) versus this client simply already being
// verified and undergoing a legitimate repair (which must run through to
// completion — in particular the admin-credential retention step — rather
// than bailing out early). Confusing the two is exactly what left a
// per-user-headers client's admin credential permanently stuck at
// needs_update: every repair attempt hit the "already verified" guard
// before ever reaching UpsertCredential.
func TestIsConcurrentVerifyReplay(t *testing.T) {
	tests := []struct {
		name                                    string
		wasPendingVerificationBeforeThisRequest bool
		reloadedHasTools                        bool
		want                                    bool
	}{
		{
			name:                                    "genuine concurrent race: started pending_verification, another request finished the bootstrap first",
			wasPendingVerificationBeforeThisRequest: true,
			reloadedHasTools:                        true,
			want:                                    true,
		},
		{
			name:                                    "repair on an already-verified client: not pending_verification, tools already present",
			wasPendingVerificationBeforeThisRequest: false,
			reloadedHasTools:                        true,
			want:                                    false,
		},
		{
			name:                                    "first-time verification with no race: started pending_verification, still no tools after reload",
			wasPendingVerificationBeforeThisRequest: true,
			reloadedHasTools:                        false,
			want:                                    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isConcurrentVerifyReplay(tt.wasPendingVerificationBeforeThisRequest, tt.reloadedHasTools)
			if got != tt.want {
				t.Errorf("isConcurrentVerifyReplay(%v, %v) = %v, want %v", tt.wasPendingVerificationBeforeThisRequest, tt.reloadedHasTools, got, tt.want)
			}
		})
	}
}
