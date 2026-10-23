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

	strip := func(raw string) string {
		t.Helper()
		out, err := provider.stripCallerQueryCredential(raw)
		require.NoError(t, err)
		return out
	}
	assert.Equal(t, "q=1", strip("q=1"), "untouched when the parameter is absent")
	assert.Equal(t, "q=1", strip("api_key=caller&q=1"))
	assert.Equal(t, "", strip("api_key=caller"))
	assert.Equal(t, "q=1", strip("api%5Fkey=caller&q=1"), "a percent-encoded name is still the credential")
	assert.Equal(t, "z=1&a=b+c&m=%20x&sig=AbC%3D%3D", strip("z=1&a=b+c&api_key=x&m=%20x&sig=AbC%3D%3D"), "every other segment is forwarded byte for byte, in order")
	assert.Equal(t, "x=%zz", strip("api_key=caller&x=%zz"), "a value that does not decode is the upstream's business; the credential is still stripped")
	assert.Equal(t, "x=%zz", strip("x=%zz"), "a value that does not decode cannot hide the parameter")
	assert.Equal(t, "q=1", strip("api_key&q=1"), "a bare name without a value is the credential too")
	_, err := provider.stripCallerQueryCredential("api%zzkey=caller&q=1")
	require.Error(t, err, "a name that cannot be decoded may spell the parameter, so it is refused")
	_, err = provider.stripCallerQueryCredential("q=1;api_key=caller")
	require.Error(t, err, "a ; separator is ambiguous between servers, so it is refused")
	assert.Equal(t, "x=zz", strip("x=zz"), "a query that cannot carry the parameter is never scanned")
	assert.Equal(t, "api_key=s+e%26c", provider.appendAuthQuery("", key))
	assert.Equal(t, "q=1&api_key=s+e%26c", provider.appendAuthQuery("q=1", key))

	other := authProvider(schemas.GenericAuthConfig{Type: schemas.GenericAuthBearer})
	untouched, err := other.stripCallerQueryCredential("api_key=x")
	require.NoError(t, err)
	assert.Equal(t, "api_key=x", untouched, "only the query auth type touches the query string")
	untouched, err = other.stripCallerQueryCredential("api_key=x&y=%zz")
	require.NoError(t, err, "other auth types never parse the query")
	assert.Equal(t, "api_key=x&y=%zz", untouched)
	assert.Equal(t, "api_key=x", other.appendAuthQuery("api_key=x", key))
}

func TestAuthHeaderName(t *testing.T) {
	assert.Equal(t, "Authorization", authProvider(schemas.GenericAuthConfig{Type: schemas.GenericAuthBearer}).authHeader())
	assert.Equal(t, "Authorization", authProvider(schemas.GenericAuthConfig{Type: schemas.GenericAuthBasic}).authHeader())
	assert.Equal(t, "x-api-key", authProvider(schemas.GenericAuthConfig{Type: schemas.GenericAuthHeader, HeaderName: "x-api-key"}).authHeader())
	assert.Equal(t, "", authProvider(schemas.GenericAuthConfig{Type: schemas.GenericAuthQuery, QueryParam: "k"}).authHeader())
	assert.Equal(t, "", authProvider(schemas.GenericAuthConfig{Type: schemas.GenericAuthNone}).authHeader())
}
