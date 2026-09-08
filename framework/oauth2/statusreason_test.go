package oauth2

import (
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
)

// TestRefreshRejectionReason pins the text recorded on a token row when a
// refresh is permanently rejected: the provider's HTTP status plus the
// standard OAuth error fields, and never the raw body, which may be an
// error page rather than JSON.
func TestRefreshRejectionReason(t *testing.T) {
	assert.Equal(t,
		"provider rejected the refresh (HTTP 400, invalid_grant: Token has been expired or revoked.)",
		refreshRejectionReason(&PermanentOAuthError{StatusCode: 400, Body: `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`}),
	)
	assert.Equal(t,
		"provider rejected the refresh (HTTP 400, unauthorized_client)",
		refreshRejectionReason(&PermanentOAuthError{StatusCode: 400, Body: `{"error":"unauthorized_client"}`}),
	)
	assert.Equal(t,
		"provider rejected the refresh (HTTP 401)",
		refreshRejectionReason(&PermanentOAuthError{StatusCode: 401, Body: "<html><body>Unauthorized</body></html>"}),
		"a non-JSON body contributes nothing beyond the status",
	)
}

// TestInactiveTokenError covers the message every access-token lookup
// returns for a non-active row: it must keep the ErrOAuth2TokenExpired
// sentinel (the connect gate classifies on it) and carry the recorded
// reason when there is one.
func TestInactiveTokenError(t *testing.T) {
	withReason := inactiveTokenError(&tables.TableMCPOauthToken{Status: "needs_reauth", StatusReason: "provider rejected the refresh (HTTP 400, invalid_grant)"})
	assert.True(t, errors.Is(withReason, schemas.ErrOAuth2TokenExpired))
	assert.Equal(t, "oauth token is not active, status: needs_reauth (provider rejected the refresh (HTTP 400, invalid_grant)): oauth2 token expired", withReason.Error())

	bare := inactiveTokenError(&tables.TableMCPOauthToken{Status: "needs_reauth"})
	assert.True(t, errors.Is(bare, schemas.ErrOAuth2TokenExpired))
	assert.Equal(t, "oauth token is not active, status: needs_reauth: oauth2 token expired", bare.Error())
}
