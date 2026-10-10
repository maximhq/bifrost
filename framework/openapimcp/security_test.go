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

	chosen := SelectSecurity(Operation{}, doc, creds)
	require.Len(t, chosen, 1)
	assert.Equal(t, "Bearer", chosen[0].Name, "OAuth is unsupported and ApiKey has no credential, so the third requirement wins")

	creds["ApiKey"] = schemas.MCPOpenAPICredential{Value: schemas.NewSecretVar("k")}
	chosen = SelectSecurity(Operation{}, doc, creds)
	assert.Equal(t, "ApiKey", chosen[0].Name, "first satisfiable requirement wins")

	anon := Operation{HasSecurity: true, Security: nil}
	assert.Empty(t, SelectSecurity(anon, doc, creds), "operation-level empty security means anonymous")

	optional := Operation{HasSecurity: true, Security: []SecurityRequirement{{}, {"ApiKey": {}}}}
	assert.Empty(t, SelectSecurity(optional, doc, creds), "an empty requirement listed first selects nothing")

	both := Operation{HasSecurity: true, Security: []SecurityRequirement{{"ApiKey": {}, "Basic": {}}}}
	chosen = SelectSecurity(both, doc, creds)
	require.Len(t, chosen, 2, "a requirement with two schemes applies both")
	assert.Equal(t, "ApiKey", chosen[0].Name)
	assert.Equal(t, "Basic", chosen[1].Name)

	basicNoUser := Operation{HasSecurity: true, Security: []SecurityRequirement{{"Basic": {}}}}
	assert.Empty(t, SelectSecurity(basicNoUser, doc, map[string]schemas.MCPOpenAPICredential{"Basic": {Password: schemas.NewSecretVar("p")}}), "basic needs a username")

	assert.Empty(t, SelectSecurity(Operation{}, nil, creds))
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
	var cookies []string
	InjectCredentials(req, q, &cookies, []SecurityScheme{doc.SecuritySchemes["ApiKey"], doc.SecuritySchemes["Query"], doc.SecuritySchemes["Cookie"], doc.SecuritySchemes["Bearer"]}, creds)
	assert.Equal(t, "key-1", req.Header.Get("X-API-Key"))
	assert.Equal(t, "q 1", q.Get("api_key"))
	assert.Equal(t, []string{"sid=c%3D1"}, cookies)
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
