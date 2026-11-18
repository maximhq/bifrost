package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tokenExchangeConfig() *OAuthKeyConfig {
	return &OAuthKeyConfig{
		GrantType:        OAuthGrantTokenExchange,
		TokenURL:         *NewSecretVar("https://auth.openai.com/oauth/token"),
		SubjectTokenFile: "/var/run/secrets/openai/token",
		RequestEncoding:  OAuthRequestEncodingJSON,
		ExtraParams:      map[string]string{"identity_provider_id": "idp", "service_account_id": "sa"},
	}
}

func TestOAuthKeyConfigValidateTokenExchange(t *testing.T) {
	t.Setenv(TokenExchangeURLsEnv, "")
	require.NoError(t, tokenExchangeConfig().Validate())

	cases := []struct {
		name    string
		mutate  func(*OAuthKeyConfig)
		wantErr string
	}{
		{"subject token file is required", func(c *OAuthKeyConfig) { c.SubjectTokenFile = " " }, "subject_token_file is required"},
		{"subject token file must be absolute", func(c *OAuthKeyConfig) { c.SubjectTokenFile = "token" }, "must be an absolute path"},
		{"unknown subject token type", func(c *OAuthKeyConfig) { c.SubjectTokenType = "urn:x" }, "subject_token_type must be one of"},
		{"unknown request encoding", func(c *OAuthKeyConfig) { c.RequestEncoding = "xml" }, "request_encoding must be form or json"},
		{"token url off the allowlist", func(c *OAuthKeyConfig) { c.TokenURL = *NewSecretVar("https://attacker.example/token") }, TokenExchangeURLsEnv},
		{"subject token fields on another grant", func(c *OAuthKeyConfig) {
			c.GrantType = OAuthGrantClientCredentials
			c.ClientID, c.ClientSecret = NewSecretVar("id"), NewSecretVar("secret")
		}, "only apply to the token_exchange grant"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tokenExchangeConfig()
			tc.mutate(c)
			err := c.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	t.Run("an env-backed token url is checked at exchange time, not here", func(t *testing.T) {
		c := tokenExchangeConfig()
		c.TokenURL = *NewSecretVar("env.SOME_TOKEN_URL")
		assert.NoError(t, c.Validate())
	})

	t.Run("the operator allowlist replaces the default", func(t *testing.T) {
		t.Setenv(TokenExchangeURLsEnv, " https://sts.example/token , https://auth.openai.com/oauth/token")
		c := tokenExchangeConfig()
		c.TokenURL = *NewSecretVar("https://sts.example/token")
		assert.NoError(t, c.Validate())
		t.Setenv(TokenExchangeURLsEnv, "https://sts.example/token")
		assert.Error(t, tokenExchangeConfig().Validate())
	})
}

func TestOAuthKeyConfigRedactedKeepsTokenExchangeSettings(t *testing.T) {
	c := tokenExchangeConfig()
	r := c.Redacted()
	assert.Equal(t, c.SubjectTokenFile, r.SubjectTokenFile)
	assert.Equal(t, c.RequestEncoding, r.RequestEncoding)
}
