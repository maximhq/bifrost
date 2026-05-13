package oauth2

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildAuthorizeURLWithPKCEPreservesExistingQueryParams(t *testing.T) {
	provider := &OAuth2Provider{}

	authURL, err := provider.buildAuthorizeURLWithPKCE(
		"https://api.notion.com/v1/oauth/authorize?owner=user",
		"client-id",
		"https://example.com/callback",
		"state-value",
		"challenge-value",
		[]string{"read", "write"},
	)
	require.NoError(t, err)

	parsedURL, err := url.Parse(authURL)
	require.NoError(t, err)

	assert.Equal(t, "https", parsedURL.Scheme)
	assert.Equal(t, "api.notion.com", parsedURL.Host)
	assert.Equal(t, "/v1/oauth/authorize", parsedURL.Path)

	query := parsedURL.Query()
	assert.Equal(t, "user", query.Get("owner"))
	assert.Equal(t, "code", query.Get("response_type"))
	assert.Equal(t, "client-id", query.Get("client_id"))
	assert.Equal(t, "https://example.com/callback", query.Get("redirect_uri"))
	assert.Equal(t, "state-value", query.Get("state"))
	assert.Equal(t, "challenge-value", query.Get("code_challenge"))
	assert.Equal(t, "S256", query.Get("code_challenge_method"))
	assert.Equal(t, "read write", query.Get("scope"))
}

func TestBuildAuthorizeURLWithPKCEPreservesMultipleProviderParams(t *testing.T) {
	provider := &OAuth2Provider{}

	authURL, err := provider.buildAuthorizeURLWithPKCE(
		"https://accounts.google.com/o/oauth2/v2/auth?access_type=offline&prompt=consent",
		"client-id",
		"https://example.com/callback",
		"state-value",
		"challenge-value",
		nil,
	)
	require.NoError(t, err)

	query, err := url.ParseQuery(mustParseURL(t, authURL).RawQuery)
	require.NoError(t, err)

	assert.Equal(t, "offline", query.Get("access_type"))
	assert.Equal(t, "consent", query.Get("prompt"))
	assert.Equal(t, "code", query.Get("response_type"))
	assert.Equal(t, "", query.Get("scope"))
}

func TestBuildAuthorizeURLWithPKCERejectsInvalidAuthorizeURL(t *testing.T) {
	provider := &OAuth2Provider{}

	_, err := provider.buildAuthorizeURLWithPKCE(
		"http://[::1",
		"client-id",
		"https://example.com/callback",
		"state-value",
		"challenge-value",
		nil,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid authorize_url")
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()

	parsedURL, err := url.Parse(value)
	require.NoError(t, err)
	return parsedURL
}
