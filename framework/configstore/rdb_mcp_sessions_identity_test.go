package configstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixture seeds three tokens: tok-active (vk → virtual_key_id=vk-alpha),
// tok-orphan (user → user_id=user-42), tok-reauth (session → session_id=sess-xyz).

func TestListOauthUserTokens_IdentityExactMatch(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	ctx := context.Background()

	cases := []struct {
		identity string
		wantID   string
	}{
		{"user-42", "tok-orphan"},
		{"vk-alpha", "tok-active"},
		{"sess-xyz", "tok-reauth"},
	}
	for _, tc := range cases {
		t.Run(tc.identity, func(t *testing.T) {
			got, err := store.ListOauthUserTokens(ctx, MCPSessionsFilterParams{Identity: tc.identity})
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.wantID, got[0].ID)
		})
	}
}

// TestListOauthUserTokens_IdentityComposesWithAuthMode pins the parenthesization
// of the identity OR group. With Identity=vk-alpha and AuthModes=[user], the
// only row whose virtual_key_id is vk-alpha is vk-mode, so the auth-mode filter
// must exclude it → zero rows. If the OR group were not parenthesized, the
// trailing `OR virtual_key_id = ?` would escape the AND chain and leak the
// vk-mode row back in.
func TestListOauthUserTokens_IdentityComposesWithAuthMode(t *testing.T) {
	store := setupMCPSessionsTestStore(t)
	seedMCPSessionsFixture(t, store)
	ctx := context.Background()

	leaked, err := store.ListOauthUserTokens(ctx, MCPSessionsFilterParams{
		Identity: "vk-alpha", AuthModes: []string{"user"},
	})
	require.NoError(t, err)
	assert.Empty(t, leaked, "auth_mode filter must AND with the whole identity OR group")

	matched, err := store.ListOauthUserTokens(ctx, MCPSessionsFilterParams{
		Identity: "vk-alpha", AuthModes: []string{"vk"},
	})
	require.NoError(t, err)
	require.Len(t, matched, 1)
	assert.Equal(t, "tok-active", matched[0].ID)
}
