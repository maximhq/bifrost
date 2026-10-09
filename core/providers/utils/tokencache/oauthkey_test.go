package tokencache

import (
	"context"
	"net/http"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ccConfig(tokenURL string) *schemas.OAuthKeyConfig {
	return &schemas.OAuthKeyConfig{
		GrantType:    schemas.OAuthGrantClientCredentials,
		TokenURL:     *schemas.NewSecretVar(tokenURL),
		ClientID:     schemas.NewSecretVar("id"),
		ClientSecret: schemas.NewSecretVar("secret"),
		Scopes:       []string{"all-apis"},
	}
}

func TestResolveBearer(t *testing.T) {
	t.Run("a static value wins and costs no mint", func(t *testing.T) {
		ts := newTokenServer(t)
		cache := New[string](Options{})
		h, bErr := ResolveBearer(context.Background(), cache, "static", ccConfig(ts.URL), authClient(t))
		require.Nil(t, bErr)
		assert.Equal(t, map[string]string{"Authorization": "Bearer static"}, h)
		hits, _, _, _, _ := ts.snapshot()
		assert.Equal(t, 0, hits)
	})

	t.Run("no value and no config is keyless", func(t *testing.T) {
		h, bErr := ResolveBearer(context.Background(), New[string](Options{}), "", nil, nil)
		require.Nil(t, bErr)
		require.NotNil(t, h)
		assert.Empty(t, h)
	})

	t.Run("mints once and reuses across calls", func(t *testing.T) {
		ts := newTokenServer(t)
		cache := New[string](Options{})
		cfg := ccConfig(ts.URL)
		for range 3 {
			h, bErr := ResolveBearer(context.Background(), cache, "", cfg, authClient(t))
			require.Nil(t, bErr)
			assert.Equal(t, "Bearer minted", h["Authorization"])
		}
		hits, form, user, pass, _ := ts.snapshot()
		assert.Equal(t, 1, hits)
		assert.Equal(t, "all-apis", form.Get("scope"))
		assert.Equal(t, "id", user)
		assert.Equal(t, "secret", pass)
	})

	t.Run("a rejected credential is reported once per backoff window", func(t *testing.T) {
		ts := newTokenServer(t)
		ts.set(http.StatusUnauthorized, `{"error":"invalid_client"}`, nil)
		cache := New[string](Options{})
		cfg := ccConfig(ts.URL)
		for range 5 {
			h, bErr := ResolveBearer(context.Background(), cache, "", cfg, authClient(t))
			assert.Nil(t, h)
			require.NotNil(t, bErr)
			assert.Equal(t, http.StatusUnauthorized, *bErr.StatusCode)
		}
		hits, _, _, _, _ := ts.snapshot()
		assert.Equal(t, 1, hits)
	})

	t.Run("a config fault never reaches the network", func(t *testing.T) {
		ts := newTokenServer(t)
		cfg := ccConfig(ts.URL)
		cfg.ClientSecret = nil
		h, bErr := ResolveBearer(context.Background(), New[string](Options{}), "", cfg, authClient(t))
		assert.Nil(t, h)
		require.NotNil(t, bErr)
		assert.Contains(t, bErr.Error.Message, "client_secret")
		require.NotNil(t, bErr.AllowFallbacks)
		assert.False(t, *bErr.AllowFallbacks)
		hits, _, _, _, _ := ts.snapshot()
		assert.Equal(t, 0, hits)
	})
}

func TestMinterForConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*schemas.OAuthKeyConfig)
		want string
	}{
		{"nil config", nil, "oauth_key_config is not set"},
		{"missing token url", func(c *schemas.OAuthKeyConfig) { c.TokenURL = *schemas.NewSecretVar(" ") }, "token_url is required"},
		{"relative token url", func(c *schemas.OAuthKeyConfig) { c.TokenURL = *schemas.NewSecretVar("/oauth/token") }, "must be https"},
		{"ftp token url", func(c *schemas.OAuthKeyConfig) { c.TokenURL = *schemas.NewSecretVar("ftp://x/token") }, "must be https"},
		{"plain http on a public host", func(c *schemas.OAuthKeyConfig) { c.TokenURL = *schemas.NewSecretVar("http://idp.example/oauth2/token") }, "must be https"},
		{"missing grant", func(c *schemas.OAuthKeyConfig) { c.GrantType = "" }, "grant_type is required"},
		{"unknown grant", func(c *schemas.OAuthKeyConfig) { c.GrantType = "password" }, "must be client_credentials or jwt_bearer"},
		{"jwt bearer not yet", func(c *schemas.OAuthKeyConfig) { c.GrantType = schemas.OAuthGrantJWTBearer }, "not supported yet"},
		{"missing client id", func(c *schemas.OAuthKeyConfig) { c.ClientID = schemas.NewSecretVar("") }, "client_id and oauth_key_config.client_secret are required"},
		{"bad auth style", func(c *schemas.OAuthKeyConfig) { c.AuthStyle = "query" }, "auth_style must be header or body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg *schemas.OAuthKeyConfig
			if tc.edit != nil {
				cfg = ccConfig("https://idp.example/token")
				tc.edit(cfg)
			}
			_, mint, bErr := MinterForConfig(cfg, nil)
			assert.Nil(t, mint)
			require.NotNil(t, bErr)
			assert.Contains(t, bErr.Error.Message, tc.want)
			require.NotNil(t, bErr.AllowFallbacks)
			assert.False(t, *bErr.AllowFallbacks)
		})
	}

	t.Run("plain http is accepted for loopback hosts only", func(t *testing.T) {
		for _, u := range []string{"http://127.0.0.1:9/token", "http://localhost:9/token", "http://[::1]:9/token"} {
			_, mint, bErr := MinterForConfig(ccConfig(u), nil)
			assert.Nil(t, bErr, u)
			assert.NotNil(t, mint, u)
		}
	})

	t.Run("plain http to a host that merely looks like loopback is rejected", func(t *testing.T) {
		// The client secret rides on this request, so only a real loopback address may skip TLS.
		// A public name such as 127.attacker.example resolves wherever its owner wants.
		for _, u := range []string{
			"http://127.attacker.example/token",
			"http://127.0.0.1.evil.example/token",
			"http://localhost.evil.example/token",
			"http://[::1]evil/token",
			"http://10.0.0.1/token",
		} {
			_, mint, bErr := MinterForConfig(ccConfig(u), nil)
			require.NotNil(t, bErr, u)
			assert.Nil(t, mint, u)
			assert.Contains(t, bErr.Error.Message, "must be https", u)
		}
		for _, u := range []string{"http://127.0.0.2:9/token", "http://[::1]:9/token", "http://LOCALHOST/token"} {
			_, mint, bErr := MinterForConfig(ccConfig(u), nil)
			assert.Nil(t, bErr, u)
			assert.NotNil(t, mint, u)
		}
	})

	t.Run("valid config yields a stable key and a minter", func(t *testing.T) {
		k1, m1, bErr := MinterForConfig(ccConfig("https://idp.example/token"), nil)
		require.Nil(t, bErr)
		require.NotNil(t, m1)
		k2, _, _ := MinterForConfig(ccConfig("https://idp.example/token"), nil)
		assert.Equal(t, k1, k2)
	})
}

func TestOAuthKeyCacheKey(t *testing.T) {
	base := func() *schemas.OAuthKeyConfig { return ccConfig("https://idp.example/token") }
	key := func(c *schemas.OAuthKeyConfig) string {
		k, _, bErr := MinterForConfig(c, nil)
		require.Nil(t, bErr)
		return k
	}
	ref := key(base())

	for _, tc := range []struct {
		name string
		edit func(*schemas.OAuthKeyConfig)
	}{
		{"token url", func(c *schemas.OAuthKeyConfig) { c.TokenURL = *schemas.NewSecretVar("https://idp.example/token2") }},
		{"client id", func(c *schemas.OAuthKeyConfig) { c.ClientID = schemas.NewSecretVar("id2") }},
		{"client secret", func(c *schemas.OAuthKeyConfig) { c.ClientSecret = schemas.NewSecretVar("secret2") }},
		{"scopes", func(c *schemas.OAuthKeyConfig) { c.Scopes = []string{"other"} }},
		{"audience", func(c *schemas.OAuthKeyConfig) { c.Audience = "aud" }},
		{"auth style", func(c *schemas.OAuthKeyConfig) { c.AuthStyle = schemas.OAuthAuthStyleBody }},
		{"extra params", func(c *schemas.OAuthKeyConfig) { c.ExtraParams = map[string]string{"resource": "r"} }},
	} {
		t.Run(tc.name+" changes the key", func(t *testing.T) {
			c := base()
			tc.edit(c)
			assert.NotEqual(t, ref, key(c))
		})
	}

	t.Run("extra params hash in a stable order", func(t *testing.T) {
		a, b := base(), base()
		a.ExtraParams = map[string]string{"x": "1", "y": "2"}
		b.ExtraParams = map[string]string{"y": "2", "x": "1"}
		assert.Equal(t, key(a), key(b))
	})

	t.Run("the key never contains the secret", func(t *testing.T) {
		assert.NotContains(t, ref, "secret")
	})
}

func TestDatabricksOAuthConfig(t *testing.T) {
	t.Run("derives the fixed endpoint and scope", func(t *testing.T) {
		cfg, ok := (&schemas.DatabricksKeyConfig{
			ClientID:     schemas.NewSecretVar("sp"),
			ClientSecret: schemas.NewSecretVar("s"),
		}).OAuthConfig("dbc-1.cloud.databricks.com")
		require.True(t, ok)
		assert.Equal(t, schemas.OAuthGrantClientCredentials, cfg.GrantType)
		assert.Equal(t, "https://dbc-1.cloud.databricks.com/oidc/v1/token", cfg.TokenURL.GetValue())
		assert.Equal(t, []string{"all-apis"}, cfg.Scopes)
		assert.Equal(t, schemas.OAuthAuthStyleHeader, cfg.AuthStyle)
		_, mint, bErr := MinterForConfig(&cfg, nil)
		assert.Nil(t, bErr)
		assert.NotNil(t, mint)
	})

	t.Run("a missing half yields nothing", func(t *testing.T) {
		var none *schemas.DatabricksKeyConfig
		_, ok := none.OAuthConfig("h")
		assert.False(t, ok)
		_, ok = (&schemas.DatabricksKeyConfig{ClientID: schemas.NewSecretVar("sp")}).OAuthConfig("h")
		assert.False(t, ok)
		_, ok = (&schemas.DatabricksKeyConfig{ClientID: schemas.NewSecretVar("sp"), ClientSecret: schemas.NewSecretVar("")}).OAuthConfig("h")
		assert.False(t, ok)
	})
}
