package generic

import (
	"net/http"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func authProvider(auth schemas.GenericAuthConfig) *GenericProvider {
	return &GenericProvider{cfg: &schemas.GenericProviderConfig{Auth: auth}}
}

func TestApplyAuthHeaders(t *testing.T) {
	key := schemas.Key{Value: *schemas.NewSecretVar("secret")}
	cases := []struct {
		name   string
		auth   schemas.GenericAuthConfig
		header string
		want   string
	}{
		{"bearer", schemas.GenericAuthConfig{Type: schemas.GenericAuthBearer}, "Authorization", "Bearer secret"},
		{"header", schemas.GenericAuthConfig{Type: schemas.GenericAuthHeader, HeaderName: "x-api-key"}, "X-Api-Key", "secret"},
		{"header with prefix", schemas.GenericAuthConfig{Type: schemas.GenericAuthHeader, HeaderName: "x-api-key", ValuePrefix: "Token "}, "X-Api-Key", "Token secret"},
		{"basic", schemas.GenericAuthConfig{Type: schemas.GenericAuthBasic}, "Authorization", "Basic c2VjcmV0"},
		{"none", schemas.GenericAuthConfig{Type: schemas.GenericAuthNone}, "Authorization", ""},
		{"query sets no header", schemas.GenericAuthConfig{Type: schemas.GenericAuthQuery, QueryParam: "k"}, "Authorization", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/x", nil)
			require.Nil(t, authProvider(tc.auth).applyAuth(nil, req, key))
			assert.Equal(t, tc.want, req.Header.Get(tc.header))
		})
	}

	t.Run("empty key value injects nothing", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/x", nil)
		require.Nil(t, authProvider(schemas.GenericAuthConfig{Type: schemas.GenericAuthBearer}).applyAuth(nil, req, schemas.Key{}))
		assert.Empty(t, req.Header.Get("Authorization"))
	})
}

func TestQueryCredentialHandling(t *testing.T) {
	provider := authProvider(schemas.GenericAuthConfig{Type: schemas.GenericAuthQuery, QueryParam: "api_key"})
	key := schemas.Key{Value: *schemas.NewSecretVar("s e&c")}

	assert.Equal(t, "q=1", provider.stripCallerQueryCredential("q=1"), "untouched when the parameter is absent")
	assert.Equal(t, "q=1", provider.stripCallerQueryCredential("api_key=caller&q=1"))
	assert.Equal(t, "", provider.stripCallerQueryCredential("api_key=caller"))
	assert.Equal(t, "api_key=s+e%26c", provider.appendAuthQuery("", key))
	assert.Equal(t, "q=1&api_key=s+e%26c", provider.appendAuthQuery("q=1", key))

	other := authProvider(schemas.GenericAuthConfig{Type: schemas.GenericAuthBearer})
	assert.Equal(t, "api_key=x", other.stripCallerQueryCredential("api_key=x"), "only the query auth type touches the query string")
	assert.Equal(t, "api_key=x", other.appendAuthQuery("api_key=x", key))
}

func TestAuthHeaderName(t *testing.T) {
	assert.Equal(t, "Authorization", authProvider(schemas.GenericAuthConfig{Type: schemas.GenericAuthBearer}).authHeader())
	assert.Equal(t, "Authorization", authProvider(schemas.GenericAuthConfig{Type: schemas.GenericAuthBasic}).authHeader())
	assert.Equal(t, "x-api-key", authProvider(schemas.GenericAuthConfig{Type: schemas.GenericAuthHeader, HeaderName: "x-api-key"}).authHeader())
	assert.Equal(t, "", authProvider(schemas.GenericAuthConfig{Type: schemas.GenericAuthQuery, QueryParam: "k"}).authHeader())
	assert.Equal(t, "", authProvider(schemas.GenericAuthConfig{Type: schemas.GenericAuthNone}).authHeader())
}
