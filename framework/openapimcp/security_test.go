package openapimcp

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func securityDoc() *Document {
	return &Document{
		SecuritySchemes: map[string]SecurityScheme{
			"ApiKey": {Name: "ApiKey", Type: "apiKey", In: InHeader, ParamName: "X-API-Key", Supported: true},
			"Query":  {Name: "Query", Type: "apiKey", In: InQuery, ParamName: "api_key", Supported: true},
			"Cookie": {Name: "Cookie", Type: "apiKey", In: InCookie, ParamName: "sid", Supported: true},
			"Bearer": {Name: "Bearer", Type: "http", Scheme: "bearer", Supported: true},
			"Basic":  {Name: "Basic", Type: "http", Scheme: "basic", Supported: true},
			"OAuth":  {Name: "OAuth", Type: "oauth2"},
		},
		GlobalSecurity: []SecurityRequirement{{"OAuth": {}}, {"ApiKey": {}}, {"Bearer": {}}},
	}
}

func TestSelectSecurity(t *testing.T) {
	doc := securityDoc()
	creds := map[string]schemas.MCPOpenAPICredential{
		"Bearer": {Value: schemas.NewSecretVar("tok")},
		"Basic":  {Username: schemas.NewSecretVar("u"), Password: schemas.NewSecretVar("p")},
	}

	chosen := SelectSecurity(Operation{}, doc, creds, nil)
	require.Len(t, chosen, 1)
	assert.Equal(t, "Bearer", chosen[0].Name, "OAuth is unsupported and ApiKey has no credential, so the third requirement wins")

	creds["ApiKey"] = schemas.MCPOpenAPICredential{Value: schemas.NewSecretVar("k")}
	chosen = SelectSecurity(Operation{}, doc, creds, nil)
	assert.Equal(t, "ApiKey", chosen[0].Name, "first satisfiable requirement wins")

	anon := Operation{HasSecurity: true, Security: nil}
	assert.Empty(t, SelectSecurity(anon, doc, creds, nil), "operation-level empty security means anonymous")

	optional := Operation{HasSecurity: true, Security: []SecurityRequirement{{}, {"ApiKey": {}}}}
	assert.Empty(t, SelectSecurity(optional, doc, creds, nil), "an empty requirement listed first selects nothing")

	both := Operation{HasSecurity: true, Security: []SecurityRequirement{{"ApiKey": {}, "Basic": {}}}}
	chosen = SelectSecurity(both, doc, creds, nil)
	require.Len(t, chosen, 2, "a requirement with two schemes applies both")
	assert.Equal(t, "ApiKey", chosen[0].Name)
	assert.Equal(t, "Basic", chosen[1].Name)

	basicNoUser := Operation{HasSecurity: true, Security: []SecurityRequirement{{"Basic": {}}}}
	assert.Empty(t, SelectSecurity(basicNoUser, doc, map[string]schemas.MCPOpenAPICredential{"Basic": {Password: schemas.NewSecretVar("p")}}, nil), "basic needs a username")

	assert.Empty(t, SelectSecurity(Operation{}, nil, creds, nil))
}

func TestInjectCredentials(t *testing.T) {
	doc := securityDoc()
	creds := map[string]schemas.MCPOpenAPICredential{
		"ApiKey": {Value: schemas.NewSecretVar("key-1")},
		"Query":  {Value: schemas.NewSecretVar("q 1")},
		"Cookie": {Value: schemas.NewSecretVar("c=1")},
		"Bearer": {Value: schemas.NewSecretVar("tok")},
		"Basic":  {Username: schemas.NewSecretVar("user"), Password: schemas.NewSecretVar("pass")},
	}
	req, _ := http.NewRequest(http.MethodGet, "https://x", nil)
	q := url.Values{}
	var cookies []*http.Cookie
	InjectCredentials(req, q, &cookies, []SecurityScheme{doc.SecuritySchemes["ApiKey"], doc.SecuritySchemes["Query"], doc.SecuritySchemes["Cookie"], doc.SecuritySchemes["Bearer"]}, creds)
	assert.Equal(t, "key-1", req.Header.Get("X-API-Key"))
	assert.Equal(t, "q 1", q.Get("api_key"))
	assert.Equal(t, []*http.Cookie{{Name: "sid", Value: "c=1"}}, cookies, "cookie values are sent as configured, not URL-escaped")
	assert.Equal(t, "Bearer tok", req.Header.Get("Authorization"))

	req, _ = http.NewRequest(http.MethodGet, "https://x", nil)
	InjectCredentials(req, url.Values{}, &cookies, []SecurityScheme{doc.SecuritySchemes["Basic"]}, creds)
	user, pass, ok := req.BasicAuth()
	require.True(t, ok)
	assert.Equal(t, "user", user)
	assert.Equal(t, "pass", pass)

	req, _ = http.NewRequest(http.MethodGet, "https://x", nil)
	InjectCredentials(req, url.Values{}, &cookies, []SecurityScheme{doc.SecuritySchemes["Bearer"]}, map[string]schemas.MCPOpenAPICredential{"Bearer": {Value: schemas.NewSecretVar("Bearer already")}})
	assert.Equal(t, "Bearer already", req.Header.Get("Authorization"), "a value already carrying the scheme is not doubled")
}

// TestSelectSecurity_OAuthSchemeMetByAuthorizationHeader: an oauth2 or
// openIdConnect scheme has no stored credential; it is met by the bearer the
// header resolver supplies (the client's OAuth token) and is then left to that
// header, while the other schemes of the same requirement still apply.
func TestSelectSecurity_OAuthSchemeMetByAuthorizationHeader(t *testing.T) {
	doc := &Document{
		SecuritySchemes: map[string]SecurityScheme{
			"OAuth":  {Name: "OAuth", Type: "oauth2", Supported: true},
			"OIDC":   {Name: "OIDC", Type: "openIdConnect", Supported: true},
			"AppKey": {Name: "AppKey", Type: "apiKey", In: InHeader, ParamName: "X-App-Key", Supported: true},
		},
	}
	creds := map[string]schemas.MCPOpenAPICredential{"AppKey": {Value: schemas.NewSecretVar("k")}}
	bearer := http.Header{"Authorization": {"Bearer user-token"}}

	op := Operation{HasSecurity: true, Security: []SecurityRequirement{{"OAuth": {"read"}, "AppKey": {}}}}
	assert.Empty(t, SelectSecurity(op, doc, creds, nil), "without a bearer the oauth2 requirement is not met")
	chosen := SelectSecurity(op, doc, creds, bearer)
	require.Len(t, chosen, 1)
	assert.Equal(t, "AppKey", chosen[0].Name, "the bearer meets OAuth, the stored app key is still applied")

	oidc := Operation{HasSecurity: true, Security: []SecurityRequirement{{"OIDC": {}}}}
	assert.Empty(t, SelectSecurity(oidc, doc, creds, bearer), "openIdConnect is met by the bearer and selects nothing to inject")

	stored := map[string]schemas.MCPOpenAPICredential{"OAuth": {Value: schemas.NewSecretVar("static")}}
	assert.Empty(t, SelectSecurity(Operation{HasSecurity: true, Security: []SecurityRequirement{{"OAuth": {}}}}, doc, stored, nil),
		"a stored value does not stand in for an OAuth token")
}

func TestSpecOAuthEndpoints(t *testing.T) {
	doc := &Document{
		SecuritySchemes: map[string]SecurityScheme{
			"B_Code":   {Name: "B_Code", Type: "oauth2", Supported: true, OAuth: &OAuthFlow{AuthorizationURL: "https://b/authorize", TokenURL: "https://b/token", Scopes: []string{"all"}}},
			"A_Code":   {Name: "A_Code", Type: "oauth2", Supported: true, OAuth: &OAuthFlow{AuthorizationURL: "https://a/authorize", TokenURL: "https://a/token", Scopes: []string{"read", "write", "admin"}}},
			"Implicit": {Name: "Implicit", Type: "oauth2", Supported: true},
		},
		GlobalSecurity: []SecurityRequirement{{"A_Code": {"read"}}},
		Operations: []Operation{
			{HasSecurity: true, Security: []SecurityRequirement{{"A_Code": {"write", "read"}}}},
			{HasSecurity: true, Security: []SecurityRequirement{{"B_Code": {"all"}}}},
		},
	}
	got, ok := SpecOAuthEndpoints(doc)
	require.True(t, ok)
	assert.Equal(t, "A_Code", got.SchemeName, "the first authorization code scheme by name")
	assert.Equal(t, "https://a/authorize", got.AuthorizeURL)
	assert.Equal(t, "https://a/token", got.TokenURL)
	assert.Equal(t, []string{"read", "write"}, got.Scopes, "the scopes the document's requirements ask for, not every declared scope")

	authorize, token, scopes := "https://admin/authorize", "", []string(nil)
	got.Fill(&authorize, &token, &scopes)
	assert.Equal(t, "https://admin/authorize", authorize, "a value the admin set is kept")
	assert.Equal(t, "https://a/token", token)
	assert.Equal(t, []string{"read", "write"}, scopes)
	adminScopes := []string{"custom"}
	got.Fill(&authorize, &token, &adminScopes)
	assert.Equal(t, []string{"custom"}, adminScopes)

	_, ok = SpecOAuthEndpoints(&Document{SecuritySchemes: map[string]SecurityScheme{"Implicit": {Name: "Implicit", Type: "oauth2", Supported: true}}})
	assert.False(t, ok, "no authorization code flow, nothing to default from")
	_, ok = SpecOAuthEndpoints(nil)
	assert.False(t, ok)
}
