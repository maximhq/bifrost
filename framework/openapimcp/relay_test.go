package openapimcp

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestImportRelayOpenAPI pins how a parsed document maps onto relay routes, auth and base URL.
func TestImportRelayOpenAPI(t *testing.T) {
	importDoc := func(t *testing.T, spec, specURL string) RelayImport {
		t.Helper()
		doc, err := Parse([]byte(spec), ParseOptions{SpecURL: specURL})
		require.NoError(t, err)
		return ImportRelay(doc, specURL)
	}
	codes := func(r RelayImport) []string {
		out := []string{}
		for _, w := range r.Warnings {
			out = append(out, w.Code)
		}
		return out
	}
	route := func(method, path string) schemas.RelayRouteConfig {
		return schemas.RelayRouteConfig{Method: method, Path: path}
	}

	t.Run("operations become routes with templated segments as globs", func(t *testing.T) {
		r := importDoc(t, `{"openapi":"3.0.3","info":{"title":"Petstore","version":"1.0.0"},
		  "servers":[{"url":"https://{region}.petstore.example.com/v1"},{"url":"https://petstore.example.com/v1/"}],
		  "components":{"securitySchemes":{"ApiKey":{"type":"apiKey","in":"header","name":"X-Api-Key"}}},
		  "paths":{"/pets":{"get":{},"post":{},"options":{}},"/pets/{petId}":{"get":{},"delete":{}},"/pets/{petId}/photo":{"post":{}},
		    "/pets/{other}":{"get":{}}}}`, "")
		assert.Equal(t, "Petstore", r.Title)
		assert.Equal(t, []schemas.RelayRouteConfig{
			route("GET", "/pets"), route("POST", "/pets"),
			route("GET", "/pets/*"), route("DELETE", "/pets/*"),
			route("POST", "/pets/*/photo"),
		}, r.Routes, "OPTIONS is not a relay method, and /pets/{other} duplicates /pets/{petId}")
		assert.Equal(t, "https://petstore.example.com/v1", r.BaseURL, "a templated server without a default is skipped, a trailing slash stripped")
		assert.Equal(t, &schemas.RelayAuthConfig{Type: schemas.RelayAuthHeader, HeaderName: "X-Api-Key"}, r.Auth, "a lone declared scheme")
		assert.Empty(t, r.Warnings)
		assert.False(t, r.Partial)
	})

	t.Run("base URL resolves as for an MCP OpenAPI server", func(t *testing.T) {
		paths := `"paths":{"/a":{"get":{}}}`
		assert.Equal(t, "https://docs.example.com/v2/api", importDoc(t, `{"openapi":"3.0.0","servers":[{"url":"./api/"}],`+paths+`}`, "https://docs.example.com/v2/openapi.json").BaseURL)
		assert.Empty(t, importDoc(t, `{"openapi":"3.0.0","servers":[{"url":"./api/"}],`+paths+`}`, "").BaseURL, "a relative server without a document URL")
		assert.Equal(t, "https://eu.example.com", importDoc(t, `{"openapi":"3.0.0","servers":[{"url":"https://{r}.example.com","variables":{"r":{"default":"eu"}}}],`+paths+`}`, "").BaseURL, "server variables take their default")
		assert.Equal(t, "http://api.example.com/v2", importDoc(t, `{"swagger":"2.0","host":"api.example.com","basePath":"/v2","schemes":["http"],`+paths+`}`, "").BaseURL)
		assert.Equal(t, "https://pets.example.com/v3", importDoc(t, `{"swagger":"2.0","basePath":"/v3",`+paths+`}`, "https://pets.example.com/swagger.json").BaseURL, "Swagger 2 without a host takes the document's")
		assert.Equal(t, "http://docs.example.com/v1", importDoc(t, `{"swagger":"2.0","schemes":["http"],"basePath":"/v1",`+paths+`}`, "https://docs.example.com/swagger.json").BaseURL, "a declared scheme stands")
		assert.Equal(t, "https://docs.example.com:8080", importDoc(t, `{"swagger":"2.0","schemes":["http","https"],`+paths+`}`, "http://docs.example.com:8080/swagger.json").BaseURL)
	})

	t.Run("a server's query is left out of the base URL and named", func(t *testing.T) {
		r := importDoc(t, `{"openapi":"3.0.0","servers":[{"url":"https://api.example.com/v1/?version=2#x"}],"paths":{"/a":{"get":{}}}}`, "")
		assert.Equal(t, "https://api.example.com/v1", r.BaseURL)
		require.Equal(t, []string{relayImportWarnServerQuery}, codes(r))
		assert.Equal(t, "?version=2", r.Warnings[0].Detail)
		r = importDoc(t, `{"openapi":"3.0.0","servers":[{"url":"./api?version=2"}],"paths":{"/a":{"get":{}}}}`, "https://example.com/openapi.json")
		assert.Equal(t, "https://example.com/api", r.BaseURL)
		assert.Equal(t, []string{relayImportWarnServerQuery}, codes(r))
	})

	t.Run("security schemes map onto relay auth types", func(t *testing.T) {
		for name, tc := range map[string]struct {
			schemes string
			want    *schemas.RelayAuthConfig
			warning string
		}{
			"bearer":                 {`{"b":{"type":"http","scheme":"bearer"}}`, &schemas.RelayAuthConfig{Type: schemas.RelayAuthBearer}, ""},
			"basic":                  {`{"b":{"type":"http","scheme":"basic"}}`, &schemas.RelayAuthConfig{Type: schemas.RelayAuthBasic}, ""},
			"query key":              {`{"q":{"type":"apiKey","in":"query","name":"api_key"}}`, &schemas.RelayAuthConfig{Type: schemas.RelayAuthQuery, QueryParam: "api_key"}, ""},
			"authorization":          {`{"a":{"type":"apiKey","in":"header","name":"Authorization"}}`, &schemas.RelayAuthConfig{Type: schemas.RelayAuthHeader, HeaderName: "Authorization"}, ""},
			"cookie key":             {`{"c":{"type":"apiKey","in":"cookie","name":"sid"}}`, nil, relayImportWarnAPIKeyLocation},
			"digest":                 {`{"d":{"type":"http","scheme":"digest"}}`, nil, relayImportWarnHTTPScheme},
			"oauth2":                 {`{"o":{"type":"oauth2"}}`, nil, relayImportWarnSchemeType},
			"several, none required": {`{"b":{"type":"http","scheme":"bearer"},"k":{"type":"apiKey","in":"header","name":"X-Key"}}`, nil, relayImportWarnSeveralSchemes},
		} {
			t.Run(name, func(t *testing.T) {
				r := importDoc(t, `{"openapi":"3.0.0","components":{"securitySchemes":`+tc.schemes+`},"paths":{"/a":{"get":{}}}}`, "")
				assert.Equal(t, tc.want, r.Auth)
				if tc.warning == "" {
					assert.Empty(t, r.Warnings)
				} else {
					assert.Equal(t, []string{tc.warning}, codes(r))
				}
			})
		}
		r := importDoc(t, `{"swagger":"2.0","securityDefinitions":{"s":{"type":"basic"}},"paths":{"/a":{"get":{}}}}`, "")
		assert.Equal(t, &schemas.RelayAuthConfig{Type: schemas.RelayAuthBasic}, r.Auth, "Swagger 2 basic")
	})

	t.Run("the required scheme wins, and auth is left alone when it is unclear", func(t *testing.T) {
		two := `"components":{"securitySchemes":{"b":{"type":"http","scheme":"bearer"},"k":{"type":"apiKey","in":"header","name":"X-Key"},"o":{"type":"oauth2"}}},"paths":{"/a":{"get":{}}}`
		assert.Equal(t, &schemas.RelayAuthConfig{Type: schemas.RelayAuthHeader, HeaderName: "X-Key"}, importDoc(t, `{"openapi":"3.0.0","security":[{"k":[]}],`+two+`}`, "").Auth)
		assert.Equal(t, &schemas.RelayAuthConfig{Type: schemas.RelayAuthHeader, HeaderName: "X-Key"}, importDoc(t, `{"openapi":"3.0.0","security":[{"o":[]},{"k":[]}],`+two+`}`, "").Auth, "the first supported alternative")
		joint := importDoc(t, `{"openapi":"3.0.0","security":[{"b":[],"k":[]}],`+two+`}`, "")
		assert.Nil(t, joint.Auth, "jointly required schemes")
		assert.Equal(t, []string{relayImportWarnNoSingleScheme}, codes(joint))
		undeclared := importDoc(t, `{"openapi":"3.0.0","security":[{"x":[]}],`+two+`}`, "")
		assert.Nil(t, undeclared.Auth)
		assert.Equal(t, []string{relayImportWarnSchemeUndeclared}, codes(undeclared))
		assert.Equal(t, "x", undeclared.Warnings[0].Name)
	})

	t.Run("each operation's own security is read, and disagreement leaves auth alone", func(t *testing.T) {
		schemes := `"components":{"securitySchemes":{"b":{"type":"http","scheme":"bearer"},"k":{"type":"apiKey","in":"header","name":"X-Api-Key"}}},"security":[{"b":[]}]`
		mixed := importDoc(t, `{"openapi":"3.0.0",`+schemes+`,"paths":{"/a":{"get":{}},"/b":{"post":{"security":[{"k":[]}]}}}}`, "")
		assert.Nil(t, mixed.Auth, "bearer by default, X-Api-Key on /b")
		assert.Equal(t, []string{relayImportWarnDifferentSchemes}, codes(mixed))
		overridden := importDoc(t, `{"openapi":"3.0.0",`+schemes+`,"paths":{"/a":{"get":{"security":[{"k":[]}]}},"/health":{"get":{"security":[]}}}}`, "")
		assert.Equal(t, &schemas.RelayAuthConfig{Type: schemas.RelayAuthHeader, HeaderName: "X-Api-Key"}, overridden.Auth, "the public operation needs none")
		assert.Equal(t, &schemas.RelayAuthConfig{Type: schemas.RelayAuthBearer}, importDoc(t, `{"openapi":"3.0.0",`+schemes+`,"paths":{"/a":{"get":{}}}}`, "").Auth)
	})

	t.Run("no auth is inferred when every operation is public", func(t *testing.T) {
		paths := `"components":{"securitySchemes":{"b":{"type":"http","scheme":"bearer"}}},"paths":{"/a":{"get":{"security":[]}},"/b":{"post":{"security":[]}}}`
		for _, global := range []string{`"security":[{"b":[]}],`, `"security":[],`} {
			r := importDoc(t, `{"openapi":"3.0.0",`+global+paths+`}`, "")
			assert.Nil(t, r.Auth, global)
			assert.Empty(t, r.Warnings, global)
		}
		r := importDoc(t, `{"openapi":"3.0.0","security":[],"components":{"securitySchemes":{"b":{"type":"http","scheme":"bearer"}}},"paths":{"/a":{"get":{}}}}`, "")
		assert.Nil(t, r.Auth, "an empty top-level list declares the API public, unlike a missing one")
	})

	t.Run("an empty security alternative makes auth optional", func(t *testing.T) {
		schemes := `"components":{"securitySchemes":{"k":{"type":"apiKey","in":"header","name":"X-Api-Key"}}}`
		r := importDoc(t, `{"openapi":"3.0.0",`+schemes+`,"security":[{},{"k":[]}],"paths":{"/a":{"get":{}}}}`, "")
		assert.Nil(t, r.Auth, "an operation that works without a key does not make the relay require one")
		mixed := importDoc(t, `{"openapi":"3.0.0",`+schemes+`,"paths":{"/a":{"get":{"security":[{},{"k":[]}]}},"/b":{"post":{"security":[{"k":[]}]}}}}`, "")
		assert.Equal(t, &schemas.RelayAuthConfig{Type: schemas.RelayAuthHeader, HeaderName: "X-Api-Key"}, mixed.Auth, "the operation that requires the key still sets it")
	})

	t.Run("local path item $refs are resolved, external ones flagged", func(t *testing.T) {
		r := importDoc(t, `{"openapi":"3.1.0","components":{"pathItems":{"B":{"put":{}}}},"paths":{"/a":{"get":{}},"/b":{"$ref":"#/components/pathItems/B"},"/c":{"$ref":"other.yaml#/C"}}}`, "")
		assert.Equal(t, []schemas.RelayRouteConfig{route("GET", "/a"), route("PUT", "/b")}, r.Routes)
		assert.True(t, r.Partial)
		require.Equal(t, []string{relayImportWarnRefPaths}, codes(r))
		assert.Equal(t, []string{"/c"}, r.Warnings[0].Paths)
	})

	t.Run("a Swagger 2 path whose local $ref does not resolve is flagged, not silently dropped", func(t *testing.T) {
		r := importDoc(t, `{"swagger":"2.0","info":{"title":"x","version":"1"},"paths":{"/a":{"get":{}},"/m":{"$ref":"#/x-missing"}}}`, "")
		assert.Equal(t, []schemas.RelayRouteConfig{route("GET", "/a")}, r.Routes)
		assert.True(t, r.Partial)
		require.Equal(t, []string{relayImportWarnRefPaths}, codes(r))
		assert.Equal(t, []string{"/m"}, r.Warnings[0].Paths)
		assert.NotContains(t, r.Warnings[0].Message, "another document")
	})

	t.Run("operations an MCP tool cannot carry still become routes", func(t *testing.T) {
		r := importDoc(t, `{"openapi":"3.0.0","paths":{"/a":{"get":{"parameters":[{"$ref":"other.yaml#/P"}]}}}}`, "")
		assert.Equal(t, []schemas.RelayRouteConfig{route("GET", "/a")}, r.Routes)
	})

	t.Run("a document with no relay method says so", func(t *testing.T) {
		r := importDoc(t, `{"openapi":"3.0.0","paths":{"/a":{"options":{},"trace":{}}}}`, "")
		assert.Empty(t, r.Routes)
		assert.Equal(t, []string{relayImportWarnNoMethods}, codes(r))
	})

	t.Run("AWS X-Amz-Target path keys become header-matched routes", func(t *testing.T) {
		r := importDoc(t, `{"openapi":"3.0.0","paths":{"/#X-Amz-Target=Textract.GetDocumentAnalysis":{"post":{}},"/#X-Amz-Target=Textract.StartDocumentAnalysis":{"post":{}}}}`, "")
		assert.Equal(t, []schemas.RelayRouteConfig{
			{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.GetDocumentAnalysis"}},
			{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.StartDocumentAnalysis"}},
		}, r.Routes)
		assert.Equal(t, schemas.RelayRouteConfig{Method: "POST", Path: "/v1/jobs/*"}, relayRouteFromTemplate("POST", "/v1/jobs/{id}"))
		assert.Equal(t, schemas.RelayRouteConfig{Method: "POST", Path: "/x"}, relayRouteFromTemplate("POST", "/x#fragment"))
	})
}

func TestRelayPathGlob(t *testing.T) {
	for template, want := range map[string]string{
		"/users/{id}/posts/{postId}": "/users/*/posts/*",
		"users":                      "/users",
		"/":                          "/",
		"/files/{id}.json":           "/files/*.json",
		"/v1/models/{model}:predict": "/v1/models/*:predict",
		"/{owner}-{repo}/issues":     "/*-*/issues",
		"/items[0]/what?":            `/items\[0\]/what\?`,
		"/a*b":                       `/a\*b`,
		`/a\b`:                       `/a\\b`,
		"/files/{name}{ext}/meta":    "/files/*/meta",
		"/files/{name}{ext}":         "/files/*",
		"/a/x{one}{two}y":            "/a/x*y",
		"/a/*{id}":                   `/a/\**`,
		"/files/hello%20world/{id}":  "/files/hello world/*",
		"/caf%C3%A9/menu":            "/café/menu",
		"/café/menu":                 "/café/menu",
		"/a%2A/b":                    `/a\*/b`,
		"/bad%ZZ/x":                  "/bad%ZZ/x",
		"/half%C3/x":                 "/half%C3/x",
	} {
		assert.Equal(t, want, relayPathGlob(template), template)
	}
	// Every glob the import writes is one the relay accepts.
	for _, template := range []string{"/items[0]/what?", "/a/*{id}", "/files/{name}{ext}", "/{owner}-{repo}/issues"} {
		cfg := &schemas.RelayConfig{Name: "x", BaseURL: "https://example.com", Auth: schemas.RelayAuthConfig{Type: schemas.RelayAuthNone}, Routes: []schemas.RelayRouteConfig{relayRouteFromTemplate("GET", template)}}
		assert.NoError(t, cfg.Validate(), template)
	}
}
