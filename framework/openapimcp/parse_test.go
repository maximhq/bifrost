package openapimcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse_OpenAPI30Fixture(t *testing.T) {
	doc := parseFixture(t, "petstore-3.0.yaml")

	assert.Equal(t, "Petstore", doc.Title)
	assert.Equal(t, "1.0.0", doc.Version)
	assert.Equal(t, "3.0.3", doc.OpenAPIVersion)
	assert.Equal(t, []string{"https://petstore.example.com/v1", "https://eu.petstore.example.com/v1"}, doc.Servers, "server variables use their default")

	require.Len(t, doc.SecuritySchemes, 5)
	assert.True(t, doc.SecuritySchemes["ApiKeyAuth"].Supported)
	assert.Equal(t, "X-API-Key", doc.SecuritySchemes["ApiKeyAuth"].ParamName)
	assert.Equal(t, InHeader, doc.SecuritySchemes["ApiKeyAuth"].In)
	assert.True(t, doc.SecuritySchemes["QueryKey"].Supported)
	assert.True(t, doc.SecuritySchemes["Bearer"].Supported)
	assert.Equal(t, "bearer", doc.SecuritySchemes["Bearer"].Scheme)
	assert.True(t, doc.SecuritySchemes["Basic"].Supported)
	assert.False(t, doc.SecuritySchemes["OAuth"].Supported)
	assert.Contains(t, doc.SecuritySchemes["OAuth"].Reason, "oauth2")
	assert.Equal(t, []SecurityRequirement{{"ApiKeyAuth": {}}}, doc.GlobalSecurity)

	// Deterministic walk: sorted paths, fixed method order.
	var shape []string
	for _, op := range doc.Operations {
		shape = append(shape, op.Method+" "+op.Path)
	}
	assert.Equal(t, []string{
		"PUT /notes",
		"GET /pets", "POST /pets",
		"GET /pets/{petId}", "DELETE /pets/{petId}",
		"POST /pets/{petId}/photo",
		"GET /search",
	}, shape)

	list := findOp(t, doc, "GET", "/pets")
	assert.Equal(t, "listPets", list.ID)
	require.Len(t, list.Params, 3)
	assert.Equal(t, "limit", list.Params[0].Name)
	assert.Equal(t, InQuery, list.Params[0].In)
	assert.False(t, list.Params[0].Required)
	assert.Equal(t, "tags", list.Params[1].Name)
	require.NotNil(t, list.Params[1].Explode)
	assert.False(t, *list.Params[1].Explode)
	assert.Equal(t, InHeader, list.Params[2].In)

	create := findOp(t, doc, "POST", "/pets")
	require.NotNil(t, create.Body)
	assert.Equal(t, ContentTypeJSON, create.Body.ContentType)
	assert.True(t, create.Body.Required)
	props := asMap(create.Body.Schema["properties"])
	assert.Contains(t, props, "owner", "$ref to Owner resolved inline")
	ownerPets := asMap(asMap(asMap(props["owner"])["properties"])["pets"])
	items := asMap(ownerPets["items"])
	assert.Contains(t, asString(items["description"]), "recursive reference", "Pet -> Owner -> Pet cycle is cut with a placeholder")
	assert.NotEmpty(t, doc.Warnings)

	get := findOp(t, doc, "GET", "/pets/{petId}")
	assert.True(t, get.HasSecurity, "security: [] is an explicit (anonymous) declaration")
	assert.Empty(t, get.Security)
	require.Len(t, get.Params, 2, "path-level $ref parameter merged with the operation's own")
	assert.Equal(t, InPath, get.Params[0].In)
	assert.True(t, get.Params[0].Required)
	assert.Equal(t, "The pet id", get.Params[0].Description)
	assert.Equal(t, InQuery, get.Params[1].In)

	assert.True(t, findOp(t, doc, "DELETE", "/pets/{petId}").Deprecated)

	photo := findOp(t, doc, "POST", "/pets/{petId}/photo")
	assert.Contains(t, photo.Unsupported, "multipart/form-data")

	note := findOp(t, doc, "PUT", "/notes")
	require.NotNil(t, note.Body)
	assert.Equal(t, ContentTypeText, note.Body.ContentType)

	assert.Empty(t, findOp(t, doc, "GET", "/search").ID, "missing operationId is allowed")
}

func TestParse_Swagger2Fixture(t *testing.T) {
	doc := parseFixture(t, "petstore-swagger2.json")

	assert.Equal(t, "2.0", doc.OpenAPIVersion)
	assert.Equal(t, []string{"https://petstore.example.com/v2"}, doc.Servers, "https preferred over http; basePath appended")

	assert.True(t, doc.SecuritySchemes["ApiKeyAuth"].Supported)
	basic := doc.SecuritySchemes["BasicAuth"]
	assert.Equal(t, "http", basic.Type, "swagger basic maps onto http/basic")
	assert.Equal(t, "basic", basic.Scheme)
	assert.True(t, basic.Supported)
	assert.False(t, doc.SecuritySchemes["Petstore_OAuth"].Supported)

	list := findOp(t, doc, "GET", "/pets")
	require.Len(t, list.Params, 3)
	assert.Equal(t, "integer", list.Params[0].Schema["type"])
	assert.False(t, *list.Params[1].Explode, "collectionFormat csv does not explode")
	assert.Equal(t, "array", list.Params[1].Schema["type"])
	assert.True(t, *list.Params[2].Explode, "collectionFormat multi explodes")

	create := findOp(t, doc, "POST", "/pets")
	require.NotNil(t, create.Body)
	assert.Equal(t, ContentTypeJSON, create.Body.ContentType)
	assert.True(t, create.Body.Required)
	tag := asMap(asMap(create.Body.Schema["properties"])["tag"])
	assert.Equal(t, true, tag["x-nullable"], "raw 2.0 idiom is preserved by the parser")
	normalizedTag := asMap(asMap(NormalizeSchema(create.Body.Schema)["properties"])["tag"])
	assert.Equal(t, true, normalizedTag["nullable"], "x-nullable becomes nullable at normalization")

	get := findOp(t, doc, "GET", "/pets/{petId}")
	require.Len(t, get.Params, 1, "path-level $ref parameter resolved")
	assert.Equal(t, InPath, get.Params[0].In)

	assert.Contains(t, findOp(t, doc, "POST", "/pets/{petId}/photo").Unsupported, "file upload")

	login := findOp(t, doc, "POST", "/login")
	require.NotNil(t, login.Body)
	assert.Equal(t, ContentTypeForm, login.Body.ContentType)
	assert.True(t, login.Body.Required)
	assert.ElementsMatch(t, []string{"username", "password"}, asStringSlice(login.Body.Schema["required"]))
}

func TestParse_Swagger2ServersWithoutHostUseSpecURL(t *testing.T) {
	doc, err := Parse([]byte(`{"swagger":"2.0","info":{"title":"x","version":"1"},"basePath":"/api","paths":{"/a":{"get":{"responses":{"200":{"description":"ok"}}}}}}`), ParseOptions{SpecURL: "http://internal.example.com:8080/swagger.json"})
	require.NoError(t, err)
	assert.Equal(t, []string{"http://internal.example.com:8080/api"}, doc.Servers)

	doc, err = Parse([]byte(`{"swagger":"2.0","info":{"title":"x","version":"1"},"basePath":"/api","paths":{"/a":{"get":{"responses":{"200":{"description":"ok"}}}}}}`), ParseOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"/api"}, doc.Servers)
	assert.NotEmpty(t, doc.Warnings)
}

func TestParse_OpenAPI31Fixture(t *testing.T) {
	doc := parseFixture(t, "petstore-3.1.json")

	assert.Equal(t, "3.1.0", doc.OpenAPIVersion)
	require.Len(t, doc.Operations, 3, "webhooks are not callable operations")
	create := findOp(t, doc, "POST", "/pets")
	require.NotNil(t, create.Body)
	props := asMap(create.Body.Schema["properties"])
	tag := asMap(props["tag"])
	assert.Equal(t, []any{"string", "null"}, tag["type"], "raw $defs ref resolved; normalization happens at synthesis")
	assert.Equal(t, "rex", asMap(props["name"])["const"])
}

func TestParse_OpenAPI32Fixture(t *testing.T) {
	doc := parseFixture(t, "petstore-3.2.yaml")

	assert.Equal(t, "3.2.0", doc.OpenAPIVersion)
	assert.Equal(t, "https://api.example.com/specs/openapi.yaml", doc.Self)
	assert.Equal(t, []string{"https://api.example.com/v4"}, doc.Servers, "relative server resolved against $self")

	var shape []string
	for _, op := range doc.Operations {
		shape = append(shape, op.Method+" "+op.Path)
	}
	assert.Equal(t, []string{"POST /events", "GET /pets", "QUERY /pets", "PURGE /pets", "GET /search"}, shape)

	q := findOp(t, doc, "QUERY", "/pets")
	assert.Equal(t, "queryPets", q.ID)
	require.NotNil(t, q.Body)

	purge := findOp(t, doc, "PURGE", "/pets")
	assert.Equal(t, "purgePets", purge.ID)

	search := findOp(t, doc, "GET", "/search")
	require.Len(t, search.Params, 1)
	assert.Equal(t, InQuerystring, search.Params[0].In)

	events := findOp(t, doc, "POST", "/events")
	assert.Contains(t, events.Unsupported, "streaming request body")

	assert.False(t, doc.SecuritySchemes["Device"].Supported)
	assert.True(t, doc.SecuritySchemes["Bearer"].Supported)
}

func TestParse_QueryMethodIsOnly32(t *testing.T) {
	spec := `openapi: 3.1.0
info: {title: x, version: "1"}
paths:
  /pets:
    get:
      operationId: listPets
      responses: {'200': {description: ok}}
    query:
      operationId: queryPets
      responses: {'200': {description: ok}}
    additionalOperations:
      PURGE:
        operationId: purgePets
        responses: {'204': {description: ok}}
`
	doc, err := Parse([]byte(spec), ParseOptions{})
	require.NoError(t, err)
	require.Len(t, doc.Operations, 1, "query/additionalOperations are ignored below 3.2")
}

func TestParse_YAMLIntegerKeysAndBOM(t *testing.T) {
	spec := "\ufeffopenapi: 3.0.0\ninfo: {title: x, version: 1}\npaths:\n  /a:\n    get:\n      responses:\n        200: {description: ok}\n        404: {description: nope}\n"
	doc, err := Parse([]byte(spec), ParseOptions{})
	require.NoError(t, err)
	assert.Equal(t, "1", doc.Version, "numeric info.version is stringified")
	require.Len(t, doc.Operations, 1)
}

func TestParse_Errors(t *testing.T) {
	for _, tc := range []struct {
		name, spec, wantErr string
	}{
		{"empty", "   ", "empty"},
		{"invalid json", `{"openapi": "3.0.0",`, "invalid JSON"},
		{"trailing json", `{"openapi":"3.0.0","info":{"title":"x","version":"1"},"paths":{}} {}`, "trailing data"},
		{"json array", `[1,2]`, "top level must be an object"},
		{"yaml scalar", `just a string`, "top level must be a mapping"},
		{"not openapi", `{"title": "nope"}`, "not an OpenAPI document"},
		{"no operations", `{"openapi":"3.0.0","info":{"title":"x","version":"1"},"paths":{}}`, "no operations"},
		{"bad ref", `{"openapi":"3.0.0","info":{"title":"x","version":"1"},"paths":{"/a":{"$ref":"#/components/pathItems/missing"}}}`, "does not resolve"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.spec), ParseOptions{})
			require.ErrorContains(t, err, tc.wantErr)
		})
	}

	_, err := Parse(make([]byte, 10), ParseOptions{MaxBytes: 5})
	require.ErrorIs(t, err, ErrSpecTooLarge)
}

func TestParse_ExternalRefMarksOperationUnsupported(t *testing.T) {
	spec := `{"openapi":"3.0.0","info":{"title":"x","version":"1"},"paths":{
	  "/a":{"get":{"operationId":"a","parameters":[{"$ref":"other.yaml#/components/parameters/P"}],"responses":{"200":{"description":"ok"}}}},
	  "/b":{"post":{"operationId":"b","requestBody":{"$ref":"https://example.com/bodies.json#/Body"},"responses":{"200":{"description":"ok"}}}},
	  "/c":{"get":{"operationId":"c","responses":{"200":{"description":"ok"}}}}}}`
	doc, err := Parse([]byte(spec), ParseOptions{})
	require.NoError(t, err)
	assert.Contains(t, findOp(t, doc, "GET", "/a").Unsupported, "external $ref")
	assert.Contains(t, findOp(t, doc, "POST", "/b").Unsupported, "external $ref")
	assert.Empty(t, findOp(t, doc, "GET", "/c").Unsupported)
}

func TestParse_ServerResolutionAgainstSpecURL(t *testing.T) {
	spec := `{"openapi":"3.0.0","info":{"title":"x","version":"1"},"servers":[{"url":"/api/v1"}],"paths":{"/a":{"get":{"responses":{"200":{"description":"ok"}}}}}}`
	doc, err := Parse([]byte(spec), ParseOptions{SpecURL: "https://host.example.com/docs/openapi.json"})
	require.NoError(t, err)
	assert.Equal(t, []string{"https://host.example.com/api/v1"}, doc.Servers)

	doc, err = Parse([]byte(spec), ParseOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"/api/v1"}, doc.Servers)
	require.True(t, len(doc.Warnings) > 0 && strings.Contains(doc.Warnings[0], "relative"))
}

func TestResolverRefLookupEscapes(t *testing.T) {
	root := map[string]any{
		"components": map[string]any{
			"schemas": map[string]any{
				"a/b":        map[string]any{"type": "string"},
				"with~tilde": map[string]any{"type": "integer"},
			},
		},
		"list": []any{"zero", map[string]any{"type": "boolean"}},
	}
	r := &resolver{root: root}
	got, err := r.resolve(map[string]any{"$ref": "#/components/schemas/a~1b"})
	require.NoError(t, err)
	assert.Equal(t, "string", asMap(got)["type"])

	got, err = r.resolve(map[string]any{"$ref": "#/components/schemas/with~0tilde", "description": "overridden"})
	require.NoError(t, err)
	assert.Equal(t, "integer", asMap(got)["type"])
	assert.Equal(t, "overridden", asMap(got)["description"], "3.1 sibling description overrides the target's")

	got, err = r.resolve(map[string]any{"$ref": "#/list/1"})
	require.NoError(t, err)
	assert.Equal(t, "boolean", asMap(got)["type"])

	_, err = r.resolve(map[string]any{"$ref": "#/list/9"})
	require.ErrorContains(t, err, "bad index")
	_, err = r.resolve(map[string]any{"$ref": "#components"})
	require.ErrorContains(t, err, "expected a JSON pointer")
}

// TestParse_RefExpansionIsBounded: every level references the next twice, so a
// spec of a few KB expands to 2^depth copies. Expansion must stop at a budget
// and report the operation unsupported instead of consuming CPU and memory.
func TestParse_RefExpansionIsBounded(t *testing.T) {
	const depth = 18
	var schemas strings.Builder
	for i := 0; i < depth; i++ {
		fmt.Fprintf(&schemas, `"L%d":{"type":"object","properties":{"a":{"$ref":"#/components/schemas/L%d"},"b":{"$ref":"#/components/schemas/L%d"}}},`, i, i+1, i+1)
	}
	fmt.Fprintf(&schemas, `"L%d":{"type":"string"}`, depth)
	spec := `{"openapi":"3.0.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://a"}],
"paths":{"/boom":{"post":{"operationId":"boom","requestBody":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/L0"}}}},"responses":{"200":{"description":"ok"}}}},
"/ok":{"get":{"operationId":"ok","responses":{"200":{"description":"ok"}}}}},
"components":{"schemas":{` + schemas.String() + `}}}`
	start := time.Now()
	doc, err := Parse([]byte(spec), ParseOptions{})
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 2*time.Second, "expansion must be cut off early")
	boom := findOp(t, doc, http.MethodPost, "/boom")
	assert.Contains(t, boom.Unsupported, "expands to more than", "the exploding operation is reported unsupported")
	assert.Empty(t, findOp(t, doc, http.MethodGet, "/ok").Unsupported, "other operations are unaffected")
}

// TestParse_RefSiblingsApplyIn31: from 3.1 on, keywords beside a $ref apply
// alongside the target, so a body extending a base schema keeps both.
func TestParse_RefSiblingsApplyIn31(t *testing.T) {
	spec := `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://a"}],
"paths":{"/pets":{"post":{"operationId":"createPet","requestBody":{"required":true,"content":{"application/json":{"schema":{
  "$ref":"#/components/schemas/Base","required":["name","extra"],"properties":{"extra":{"type":"string","maxLength":5}}}}}},"responses":{"200":{"description":"ok"}}}}},
"components":{"schemas":{"Base":{"type":"object","required":["id"],"properties":{"id":{"type":"integer"},"name":{"type":"string"}}}}}}`
	syn := synthesizeSpec(t, spec)
	tool := findTool(t, syn, "createPet")
	var schema map[string]any
	require.NoError(t, json.Unmarshal(tool.InputSchema, &schema))
	props := schema["properties"].(map[string]any)
	assert.Contains(t, props, "id")
	assert.Contains(t, props, "name")
	assert.Contains(t, props, "extra", "a property declared beside the $ref is kept")
	assert.ElementsMatch(t, []any{"extra", "id", "name"}, schema["required"], "required lists are unioned")

	// 3.0 ignores siblings of $ref, as its spec says.
	syn30 := synthesizeSpec(t, strings.Replace(spec, `"3.1.0"`, `"3.0.3"`, 1))
	var schema30 map[string]any
	require.NoError(t, json.Unmarshal(findTool(t, syn30, "createPet").InputSchema, &schema30))
	assert.NotContains(t, schema30["properties"], "extra")
}

// TestParse_IgnoredHeaderParameters: OpenAPI says header parameters named
// Accept, Content-Type or Authorization are ignored. Exposing them would make
// the model supply the token and override the configured credential.
func TestParse_IgnoredHeaderParameters(t *testing.T) {
	spec := `{"openapi":"3.0.0","info":{"title":"t","version":"1"},"servers":[{"url":"https://a"}],
"paths":{"/a":{"get":{"operationId":"getA","parameters":[
  {"name":"Authorization","in":"header","required":true,"schema":{"type":"string"}},
  {"name":"accept","in":"header","schema":{"type":"string"}},
  {"name":"Content-Type","in":"header","schema":{"type":"string"}},
  {"name":"X-Real","in":"header","schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}}}}`
	tool := findTool(t, synthesizeSpec(t, spec), "getA")
	names := make([]string, 0, len(tool.Args))
	for _, a := range tool.Args {
		names = append(names, a.Name)
	}
	assert.Equal(t, []string{"X-Real"}, names)
}

// TestParse_Swagger2HostWithoutSchemesUsesSpecURLScheme: without schemes,
// Swagger 2.0 defaults to the scheme the document was fetched with, even when
// host is declared.
func TestParse_Swagger2HostWithoutSchemesUsesSpecURLScheme(t *testing.T) {
	spec := []byte(`{"swagger":"2.0","info":{"title":"x","version":"1"},"host":"api.example.com","basePath":"/v1","paths":{"/a":{"get":{"responses":{"200":{"description":"ok"}}}}}}`)
	doc, err := Parse(spec, ParseOptions{SpecURL: "http://api.example.com/swagger.json"})
	require.NoError(t, err)
	assert.Equal(t, []string{"http://api.example.com/v1"}, doc.Servers)

	doc, err = Parse(spec, ParseOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"https://api.example.com/v1"}, doc.Servers, "https without a fetch URL")
}

// TestParse_Swagger2InvalidBodyParametersAreUnsupported: Swagger 2.0 allows one
// body parameter and no body next to formData; such an operation is reported
// unsupported instead of silently dropping part of its input.
func TestParse_Swagger2InvalidBodyParametersAreUnsupported(t *testing.T) {
	for label, params := range map[string]string{
		"two body parameters": `[{"name":"a","in":"body","schema":{"type":"object"}},{"name":"b","in":"body","schema":{"type":"object"}}]`,
		"body and formData":   `[{"name":"a","in":"body","schema":{"type":"object"}},{"name":"f","in":"formData","type":"string"}]`,
	} {
		doc, err := Parse([]byte(`{"swagger":"2.0","info":{"title":"x","version":"1"},"host":"api.example.com","paths":{"/a":{"post":{"operationId":"op","parameters":`+params+`,"responses":{"200":{"description":"ok"}}}}}}`), ParseOptions{})
		require.NoError(t, err, label)
		require.Len(t, doc.Operations, 1, label)
		assert.NotEmpty(t, doc.Operations[0].Unsupported, label)
	}
}

// TestParse_BooleanAndEmptyParameterSchemas: in OpenAPI 3.1 a parameter schema
// of true or {} accepts any value and is kept unconstrained, not narrowed to a
// string; schema false accepts nothing, so the operation is unsupported.
func TestParse_BooleanAndEmptyParameterSchemas(t *testing.T) {
	doc, err := Parse([]byte(`{"openapi":"3.1.0","info":{"title":"x","version":"1"},"servers":[{"url":"https://api.example.com"}],"paths":{
"/a":{"get":{"operationId":"a","parameters":[
{"name":"any","in":"query","schema":true},
{"name":"empty","in":"query","schema":{}},
{"name":"json","in":"query","content":{"application/json":{"schema":true}}}],"responses":{"200":{"description":"ok"}}}},
"/b":{"get":{"operationId":"b","parameters":[{"name":"never","in":"query","required":true,"schema":false}],"responses":{"200":{"description":"ok"}}}}}}`), ParseOptions{})
	require.NoError(t, err)
	for _, p := range findOp(t, doc, "GET", "/a").Params {
		assert.NotContains(t, p.Schema, "type", "%s must stay unconstrained", p.Name)
	}
	assert.NotEmpty(t, findOp(t, doc, "GET", "/b").Unsupported)
}

// TestParse_OperationReplacesUnsupportedPathParameter: a path-level parameter
// that accepts no value (schema false) only hides the operations that keep it;
// one that replaces it with a usable parameter of the same name and location
// stays a tool.
func TestParse_OperationReplacesUnsupportedPathParameter(t *testing.T) {
	doc, err := Parse([]byte(`{"openapi":"3.1.0","info":{"title":"x","version":"1"},"servers":[{"url":"https://api.example.com"}],"paths":{
"/a/{mode}":{"parameters":[{"name":"mode","in":"path","required":true,"schema":false}],
  "get":{"operationId":"replaces","parameters":[{"name":"mode","in":"path","required":true,"schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}},
  "post":{"operationId":"keeps","responses":{"200":{"description":"ok"}}}}}}`), ParseOptions{})
	require.NoError(t, err)
	assert.Empty(t, findOp(t, doc, "GET", "/a/{mode}").Unsupported, "the operation's own parameter replaces the path-level one")
	assert.Contains(t, findOp(t, doc, "POST", "/a/{mode}").Unsupported, "schema: false")
}

// TestParse_OptionalInputsThatAcceptNoValueAreLeftOut: an optional parameter
// or request body whose schema is false can only be omitted, so the operation
// stays callable without it instead of being rejected. A required one still
// makes the operation unsupported.
func TestParse_OptionalInputsThatAcceptNoValueAreLeftOut(t *testing.T) {
	doc, err := Parse([]byte(`{"openapi":"3.1.0","info":{"title":"x","version":"1"},"servers":[{"url":"https://api.example.com"}],"paths":{
"/q":{"get":{"operationId":"optionalQuery","parameters":[{"name":"never","in":"query","schema":false},{"name":"h","in":"header","schema":false},{"name":"c","in":"cookie","schema":false},{"name":"q","in":"query","schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}},
"/rq":{"get":{"operationId":"requiredQuery","parameters":[{"name":"never","in":"query","required":true,"schema":false}],"responses":{"200":{"description":"ok"}}}},
"/b":{"post":{"operationId":"optionalBody","requestBody":{"content":{"application/json":{"schema":false}}},"responses":{"200":{"description":"ok"}}}},
"/rb":{"post":{"operationId":"requiredBody","requestBody":{"required":true,"content":{"application/json":{"schema":false}}},"responses":{"200":{"description":"ok"}}}}}}`), ParseOptions{})
	require.NoError(t, err)
	q := findOp(t, doc, "GET", "/q")
	assert.Empty(t, q.Unsupported)
	require.Len(t, q.Params, 1, "only the parameter that accepts a value is offered")
	assert.Equal(t, "q", q.Params[0].Name)
	assert.Contains(t, findOp(t, doc, "GET", "/rq").Unsupported, "schema: false")
	b := findOp(t, doc, "POST", "/b")
	assert.Empty(t, b.Unsupported)
	assert.Nil(t, b.Body, "an optional body that accepts no value is not sent")
	assert.Contains(t, findOp(t, doc, "POST", "/rb").Unsupported, "schema: false")
}

// TestParse_UnusedBodyAlternativeDoesNotRejectOperation: request-body media
// types are resolved one by one, so an external $ref in an alternative that is
// not chosen (XML here) does not hide the JSON body the tool sends.
func TestParse_UnusedBodyAlternativeDoesNotRejectOperation(t *testing.T) {
	doc, err := Parse([]byte(`{"openapi":"3.0.3","info":{"title":"x","version":"1"},"servers":[{"url":"https://api.example.com"}],"paths":{
"/p":{"post":{"operationId":"create","requestBody":{"content":{
  "application/json":{"schema":{"type":"object","properties":{"name":{"type":"string"}}}},
  "application/xml":{"schema":{"$ref":"./pet.xsd.json"}}}},"responses":{"200":{"description":"ok"}}}},
"/x":{"post":{"operationId":"xmlOnly","requestBody":{"content":{"application/json":{"schema":{"$ref":"./pet.json"}}}},"responses":{"200":{"description":"ok"}}}}}}`), ParseOptions{})
	require.NoError(t, err)
	p := findOp(t, doc, "POST", "/p")
	assert.Empty(t, p.Unsupported)
	require.NotNil(t, p.Body)
	assert.Equal(t, ContentTypeJSON, p.Body.ContentType)
	assert.Contains(t, findOp(t, doc, "POST", "/x").Unsupported, "external", "a chosen alternative that cannot be resolved still rejects the operation")
}

// TestParse_Swagger2OperationSchemes: an operation's own schemes override the
// document's, keeping its host and base path.
func TestParse_Swagger2OperationSchemes(t *testing.T) {
	doc, err := Parse([]byte(`{"swagger":"2.0","info":{"title":"x","version":"1"},"host":"api.example.com","basePath":"/v1","schemes":["https"],"paths":{
"/plain":{"get":{"operationId":"plain","schemes":["http"],"responses":{"200":{"description":"ok"}}}},
"/same":{"get":{"operationId":"same","responses":{"200":{"description":"ok"}}}}}}`), ParseOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{"http://api.example.com/v1"}, findOp(t, doc, "GET", "/plain").Servers)
	assert.Empty(t, findOp(t, doc, "GET", "/same").Servers, "no operation schemes: the document server applies")
}

// TestParse_LiteralRefInInstanceDataIsNotAReference: $ref is only followed
// where a reference can appear; a property named $ref and example or default
// data carrying a $ref key are left alone instead of failing the operation.
func TestParse_LiteralRefInInstanceDataIsNotAReference(t *testing.T) {
	doc, err := Parse([]byte(`{"openapi":"3.0.3","info":{"title":"t","version":"1"},"servers":[{"url":"https://api.example.com"}],"paths":{"/s":{"post":{"operationId":"s",
"requestBody":{"content":{"application/json":{"schema":{"type":"object","properties":{"$ref":{"type":"string"},"link":{"type":"object","default":{"$ref":"resource/1"}}},"example":{"$ref":"resource/123"},"examples":[{"$ref":"./examples/a.json"}]},"examples":{"pet":{"$ref":"./examples/pet.json"}}}}},
"responses":{"200":{"description":"ok"}}}}}}`), ParseOptions{})
	require.NoError(t, err)
	assert.Empty(t, findOp(t, doc, "POST", "/s").Unsupported)
}
