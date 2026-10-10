package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

const petstoreSpecJSON = `{
  "openapi": "3.0.3",
  "info": {"title": "Petstore", "version": "1.0.0"},
  "servers": [{"url": "https://petstore.example.com/v1"}],
  "components": {"securitySchemes": {"ApiKeyAuth": {"type": "apiKey", "in": "header", "name": "X-API-Key"}}},
  "security": [{"ApiKeyAuth": []}],
  "paths": {
    "/pets": {
      "get": {"operationId": "listPets", "summary": "List pets", "responses": {"200": {"description": "ok"}}},
      "post": {"operationId": "createPet", "requestBody": {"required": true, "content": {"application/json": {"schema": {"type": "object", "properties": {"name": {"type": "string"}}}}}}, "responses": {"201": {"description": "created"}}}
    },
    "/pets/{petId}": {
      "get": {"operationId": "getPetById", "parameters": [{"name": "petId", "in": "path", "required": true, "schema": {"type": "string"}}], "responses": {"200": {"description": "ok"}}}
    },
    "/pets/{petId}/photo": {
      "post": {"operationId": "uploadPetPhoto", "requestBody": {"content": {"multipart/form-data": {"schema": {"type": "object"}}}}, "responses": {"200": {"description": "ok"}}}
    }
  }
}`

const petstoreSpecYAML = "openapi: 3.0.3\ninfo:\n  title: Petstore YAML\n  version: 2.0.0\nservers:\n  - url: https://petstore.example.com/v2\npaths:\n  /pets:\n    get:\n      operationId: listPets\n      responses:\n        '200':\n          description: ok\n"

// openAPIRecordingManager stands in for the runtime: it accepts every add/update
// and keeps the configs the handler handed over, so tests can assert on what
// the runtime would have synthesized from.
type openAPIRecordingManager struct {
	MCPManager
	added   []*schemas.MCPClientConfig
	updated []*schemas.MCPClientConfig
}

func (m *openAPIRecordingManager) AddMCPClient(_ context.Context, c *schemas.MCPClientConfig) error {
	m.added = append(m.added, c)
	return nil
}

func (m *openAPIRecordingManager) UpdateMCPClient(_ context.Context, _ string, c *schemas.MCPClientConfig) error {
	m.updated = append(m.updated, c)
	return nil
}

func (m *openAPIRecordingManager) RequiresPerCallConnection(*schemas.MCPClientConfig) bool {
	return false
}

func newOpenAPITestHandler(t *testing.T) (*MCPHandler, *openAPIRecordingManager) {
	t.Helper()
	SetLogger(&mockLogger{})
	mgr := &openAPIRecordingManager{}
	h := &MCPHandler{
		store: &lib.Config{
			ConfigStore:  newRealOAuth2Store(t),
			ClientConfig: &configstore.ClientConfig{},
			MCPConfig:    &schemas.MCPConfig{},
		},
		mcpManager: mgr,
	}
	return h, mgr
}

func doMCPRequest(t *testing.T, handler func(*fasthttp.RequestCtx), method, body, id string, authBypassed bool) (int, string) {
	t.Helper()
	var req fasthttp.Request
	req.Header.SetMethod(method)
	req.Header.SetContentType("application/json")
	req.SetBodyString(body)
	ctx := initCtx(&req)
	if id != "" {
		ctx.SetUserValue("id", id)
	}
	if authBypassed {
		ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
	}
	handler(ctx)
	return ctx.Response.StatusCode(), string(ctx.Response.Body())
}

func decodePreview(t *testing.T, body string) MCPOpenAPIPreviewResponse {
	t.Helper()
	var resp MCPOpenAPIPreviewResponse
	require.NoError(t, json.Unmarshal([]byte(body), &resp), body)
	return resp
}

func previewToolNames(resp MCPOpenAPIPreviewResponse) []string {
	names := make([]string, 0, len(resp.Tools))
	for _, tool := range resp.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestPreviewOpenAPISpec_InlineJSONAndYAML(t *testing.T) {
	h, _ := newOpenAPITestHandler(t)

	body, err := json.Marshal(MCPOpenAPIPreviewRequest{Spec: petstoreSpecJSON})
	require.NoError(t, err)
	status, respBody := doMCPRequest(t, h.previewOpenAPISpec, fasthttp.MethodPost, string(body), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	resp := decodePreview(t, respBody)
	assert.Equal(t, "Petstore", resp.Title)
	assert.Equal(t, "3.0.3", resp.OpenAPIVersion)
	assert.Equal(t, "https://petstore.example.com/v1", resp.BaseURL)
	assert.Equal(t, []string{"listPets", "createPet", "getPetById"}, previewToolNames(resp))
	require.Len(t, resp.Unsupported, 1)
	assert.Equal(t, "uploadPetPhoto", resp.Unsupported[0].OperationID)
	require.Len(t, resp.SecuritySchemes, 1)
	assert.True(t, resp.SecuritySchemes[0].Supported)
	assert.Empty(t, resp.Spec, "an inline spec is not echoed back")
	assert.Equal(t, 3, resp.ToolCount)
	assert.NotEmpty(t, resp.SpecHash)

	body, err = json.Marshal(MCPOpenAPIPreviewRequest{Spec: petstoreSpecYAML, BaseURL: "https://override.example.com/"})
	require.NoError(t, err)
	status, respBody = doMCPRequest(t, h.previewOpenAPISpec, fasthttp.MethodPost, string(body), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	resp = decodePreview(t, respBody)
	assert.Equal(t, "Petstore YAML", resp.Title)
	assert.Equal(t, "https://override.example.com", resp.BaseURL, "a base_url override is previewed as given")
}

func TestPreviewOpenAPISpec_SpecURL(t *testing.T) {
	h, _ := newOpenAPITestHandler(t)
	specServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(petstoreSpecJSON))
	}))
	t.Cleanup(specServer.Close)

	body, _ := json.Marshal(MCPOpenAPIPreviewRequest{SpecURL: specServer.URL + "/openapi.json"})
	status, respBody := doMCPRequest(t, h.previewOpenAPISpec, fasthttp.MethodPost, string(body), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	resp := decodePreview(t, respBody)
	assert.Equal(t, []string{"listPets", "createPet", "getPetById"}, previewToolNames(resp))
	assert.JSONEq(t, petstoreSpecJSON, resp.Spec, "a fetched spec is echoed so the UI can store it inline")

	// An unauthenticated (auth-bypassed) caller may not point spec_url at loopback.
	status, respBody = doMCPRequest(t, h.previewOpenAPISpec, fasthttp.MethodPost, string(body), "", true)
	assert.Equal(t, fasthttp.StatusForbidden, status, respBody)
	assert.Contains(t, respBody, "loopback")
}

func TestPreviewOpenAPISpec_Errors(t *testing.T) {
	h, _ := newOpenAPITestHandler(t)

	status, body := doMCPRequest(t, h.previewOpenAPISpec, fasthttp.MethodPost, `{}`, "", false)
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	assert.Contains(t, body, "spec or spec_url is required")

	status, body = doMCPRequest(t, h.previewOpenAPISpec, fasthttp.MethodPost, `{"spec": "{\"openapi\":\"3.0.0\",\"info\":{\"title\":\"x\",\"version\":\"1\"},\"paths\":{}}"}`, "", false)
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	assert.Contains(t, body, "no operations")

	status, body = doMCPRequest(t, h.previewOpenAPISpec, fasthttp.MethodPost, `{"spec": "{\"title\": \"nope\"}"}`, "", false)
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	assert.Contains(t, body, "not an OpenAPI document")

	status, body = doMCPRequest(t, h.previewOpenAPISpec, fasthttp.MethodPost, `{"spec": "just text"}`, "", false)
	assert.Equal(t, fasthttp.StatusBadRequest, status)
	assert.Contains(t, body, "top level must be a mapping")

	huge, _ := json.Marshal(MCPOpenAPIPreviewRequest{Spec: "{" + strings.Repeat(" ", 5<<20) + "}"})
	status, body = doMCPRequest(t, h.previewOpenAPISpec, fasthttp.MethodPost, string(huge), "", false)
	assert.Equal(t, fasthttp.StatusRequestEntityTooLarge, status, body)

	status, _ = doMCPRequest(t, h.previewOpenAPISpec, fasthttp.MethodPost, `not json`, "", false)
	assert.Equal(t, fasthttp.StatusBadRequest, status)
}

func TestAddMCPClient_OpenAPI_CreatesClientFromSpec(t *testing.T) {
	h, mgr := newOpenAPITestHandler(t)
	payload := map[string]any{
		"name":             "petstore",
		"connection_type":  "openapi",
		"auth_type":        "headers",
		"headers":          map[string]string{"X-Tenant": "acme"},
		"tools_to_execute": []string{"listPets", "getPetById"},
		"openapi_config": map[string]any{
			"spec":                 petstoreSpecJSON,
			"base_url":             "https://petstore.example.com/v1/",
			"security_credentials": map[string]any{"ApiKeyAuth": map[string]string{"value": "k-123"}},
		},
	}
	body, _ := json.Marshal(payload)
	status, respBody := doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(body), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)

	require.Len(t, mgr.added, 1)
	cfg := mgr.added[0]
	assert.Equal(t, schemas.MCPConnectionTypeOpenAPI, cfg.ConnectionType)
	assert.Nil(t, cfg.ConnectionString)
	require.NotNil(t, cfg.OpenAPIConfig)
	assert.JSONEq(t, petstoreSpecJSON, cfg.OpenAPIConfig.Spec)
	assert.Equal(t, "https://petstore.example.com/v1", *cfg.OpenAPIConfig.BaseURL, "base_url is trimmed")
	assert.Equal(t, "k-123", cfg.OpenAPIConfig.SecurityCredentials["ApiKeyAuth"].Value.GetValue())
	assert.Equal(t, "Petstore", cfg.OpenAPIConfig.SpecTitle)
	assert.Equal(t, "3.0.3", cfg.OpenAPIConfig.OpenAPIVersion)
	assert.Equal(t, 3, cfg.OpenAPIConfig.OperationCount)
	assert.NotEmpty(t, cfg.OpenAPIConfig.SpecHash)
	assert.Equal(t, schemas.WhiteList{"listPets", "getPetById"}, cfg.ToolsToExecute)

	stored, err := h.store.ConfigStore.GetMCPClientByName(context.Background(), "petstore")
	require.NoError(t, err)
	require.NotNil(t, stored.OpenAPIConfig)
	assert.JSONEq(t, petstoreSpecJSON, stored.OpenAPIConfig.Spec, "the row carries the document")
}

func TestAddMCPClient_OpenAPI_SpecURLIsInlinedOnCreate(t *testing.T) {
	h, mgr := newOpenAPITestHandler(t)
	specServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(petstoreSpecYAML))
	}))
	t.Cleanup(specServer.Close)

	body, _ := json.Marshal(map[string]any{
		"name": "petstore_url", "connection_type": "openapi", "auth_type": "none",
		"tools_to_execute": []string{"*"},
		"openapi_config":   map[string]any{"spec_url": specServer.URL + "/openapi.yaml"},
	})
	status, respBody := doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(body), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	require.Len(t, mgr.added, 1)
	oc := mgr.added[0].OpenAPIConfig
	require.NotNil(t, oc)
	assert.Equal(t, petstoreSpecYAML, oc.Spec, "the fetched document is stored inline")
	assert.Equal(t, specServer.URL+"/openapi.yaml", *oc.SpecURL, "spec_url is kept as provenance")
	assert.Equal(t, "Petstore YAML", oc.SpecTitle)
}

func TestAddMCPClient_OpenAPI_PerUserHeadersSkipsUpstreamVerification(t *testing.T) {
	h, mgr := newOpenAPITestHandler(t)
	body, _ := json.Marshal(map[string]any{
		"name": "petstore_pu", "connection_type": "openapi", "auth_type": "per_user_headers",
		"per_user_header_keys": []string{"X-User-Token", "X-User-Token"},
		"tools_to_execute":     []string{"*"},
		"openapi_config":       map[string]any{"spec": petstoreSpecJSON},
	})
	status, respBody := doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(body), "", false)
	assert.Equal(t, fasthttp.StatusBadRequest, status, respBody)
	assert.Contains(t, respBody, "duplicate")

	body, _ = json.Marshal(map[string]any{
		"name": "petstore_pu", "connection_type": "openapi", "auth_type": "per_user_headers",
		"per_user_header_keys": []string{"X-User-Token"},
		"tools_to_execute":     []string{"*"},
		"openapi_config":       map[string]any{"spec": petstoreSpecJSON},
	})
	status, respBody = doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(body), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	require.Len(t, mgr.added, 1, "no VerifyHeadersConnection round-trip: the client is added directly")
	assert.Equal(t, []string{"x-user-token"}, mgr.added[0].PerUserHeaderKeys, "keys are canonicalized")
}

func TestAddMCPClient_OpenAPI_Rejections(t *testing.T) {
	h, mgr := newOpenAPITestHandler(t)
	post := func(payload map[string]any, bypass bool) (int, string) {
		body, _ := json.Marshal(payload)
		return doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(body), "", bypass)
	}
	base := func(mutate func(m map[string]any)) map[string]any {
		m := map[string]any{
			"name": "petstore", "connection_type": "openapi", "auth_type": "none",
			"tools_to_execute": []string{"*"},
			"openapi_config":   map[string]any{"spec": petstoreSpecJSON},
		}
		mutate(m)
		return m
	}
	for _, tc := range []struct {
		name    string
		payload map[string]any
		bypass  bool
		status  int
		want    string
	}{
		{"oauth auth refused", base(func(m map[string]any) { m["auth_type"] = "oauth" }), false, fasthttp.StatusBadRequest, "not supported for connection_type 'openapi'"},
		{"token exchange refused", base(func(m map[string]any) { m["auth_type"] = "token_exchange" }), false, fasthttp.StatusBadRequest, "not supported for connection_type 'openapi'"},
		{"missing openapi_config", base(func(m map[string]any) { delete(m, "openapi_config") }), false, fasthttp.StatusBadRequest, "openapi_config is required"},
		{"connection_string set", base(func(m map[string]any) { m["connection_string"] = "https://mcp.example.com" }), false, fasthttp.StatusBadRequest, "connection_string must not be set"},
		{"spec_file over the API", base(func(m map[string]any) { m["openapi_config"] = map[string]any{"spec_file": "x.yaml"} }), false, fasthttp.StatusBadRequest, "spec_file is only supported in config.json"},
		{"invalid spec", base(func(m map[string]any) { m["openapi_config"] = map[string]any{"spec": `{"title":"nope"}`} }), false, fasthttp.StatusBadRequest, "not an OpenAPI document"},
		{"unknown tool", base(func(m map[string]any) { m["tools_to_execute"] = []string{"listPets", "fetchPets"} }), false, fasthttp.StatusBadRequest, "no tool named fetchPets"},
		{"unknown auto-execute tool", base(func(m map[string]any) { m["tools_to_auto_execute"] = []string{"nope"} }), false, fasthttp.StatusBadRequest, "no tool named nope"},
		{"private base_url when unauthenticated", base(func(m map[string]any) {
			m["openapi_config"] = map[string]any{"spec": petstoreSpecJSON, "base_url": "http://127.0.0.1:1"}
		}), true, fasthttp.StatusForbidden, "loopback"},
		{"only unsupported operations", base(func(m map[string]any) {
			m["openapi_config"] = map[string]any{"spec": `{"openapi":"3.0.0","info":{"title":"x","version":"1"},"servers":[{"url":"https://a.example.com"}],"paths":{"/u":{"post":{"requestBody":{"content":{"multipart/form-data":{}}},"responses":{"200":{"description":"ok"}}}}}}`}
		}), false, fasthttp.StatusBadRequest, "No operation in the spec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := post(tc.payload, tc.bypass)
			assert.Equal(t, tc.status, status, body)
			assert.Contains(t, body, tc.want)
		})
	}
	assert.Empty(t, mgr.added, "no rejected request reached the runtime")
}

func TestUpdateMCPClient_OpenAPI_ReplacesSpecAndValidatesTools(t *testing.T) {
	h, mgr := newOpenAPITestHandler(t)
	createBody, _ := json.Marshal(map[string]any{
		"name": "petstore", "connection_type": "openapi", "auth_type": "none",
		"tools_to_execute": []string{"listPets"},
		"openapi_config": map[string]any{
			"spec":                 petstoreSpecJSON,
			"security_credentials": map[string]any{"ApiKeyAuth": map[string]string{"value": "k-123"}},
		},
	})
	status, respBody := doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(createBody), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	require.Len(t, mgr.added, 1)
	existing := mgr.added[0]
	// The real server mirrors the runtime config into the in-memory MCPConfig;
	// the stub manager does not, so do it here.
	h.store.MCPConfig.ClientConfigs = []*schemas.MCPClientConfig{existing}

	// Unknown tool against the stored spec.
	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"tools_to_execute":["listPets","nope"]}`, existing.ID, false)
	assert.Equal(t, fasthttp.StatusBadRequest, status, respBody)
	assert.Contains(t, respBody, "no tool named nope")

	// Replace the spec with a smaller one and keep the masked credential.
	redacted := h.store.RedactMCPClientConfig(existing)
	maskedKey := redacted.OpenAPIConfig.SecurityCredentials["ApiKeyAuth"].Value.GetValue()
	require.NotEqual(t, "k-123", maskedKey)
	updateBody, _ := json.Marshal(map[string]any{
		"tools_to_execute": []string{"listPets"},
		"openapi_config": map[string]any{
			"spec":                 petstoreSpecYAML,
			"security_credentials": map[string]any{"ApiKeyAuth": map[string]string{"value": maskedKey}},
		},
	})
	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, string(updateBody), existing.ID, false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	require.Len(t, mgr.updated, 1)
	updated := mgr.updated[0].OpenAPIConfig
	require.NotNil(t, updated)
	assert.Equal(t, petstoreSpecYAML, updated.Spec)
	assert.Equal(t, "Petstore YAML", updated.SpecTitle)
	assert.Equal(t, 1, updated.OperationCount)
	assert.Equal(t, "k-123", updated.SecurityCredentials["ApiKeyAuth"].Value.GetValue(), "a masked credential keeps the stored secret")

	stored, err := h.store.ConfigStore.GetMCPClientByID(context.Background(), existing.ID)
	require.NoError(t, err)
	assert.Equal(t, petstoreSpecYAML, stored.OpenAPIConfig.Spec)

	// An update without openapi_config leaves it alone (nil to the runtime).
	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"name":"petstore_renamed"}`, existing.ID, false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	require.Len(t, mgr.updated, 2)
	assert.Nil(t, mgr.updated[1].OpenAPIConfig)

	// openapi_config on a non-openapi client is refused.
	httpClient := &schemas.MCPClientConfig{ID: "http-1", Name: "remote", ConnectionType: schemas.MCPConnectionTypeHTTP, ConnectionString: schemas.NewSecretVar("https://mcp.example.com"), AuthType: schemas.MCPAuthTypeNone}
	h.store.MCPConfig.ClientConfigs = append(h.store.MCPConfig.ClientConfigs, httpClient)
	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"openapi_config":{"spec":"x"}}`, "http-1", false)
	assert.Equal(t, fasthttp.StatusBadRequest, status, respBody)
	assert.Contains(t, respBody, "connection_type 'openapi'")
}

func TestGetMCPClientOpenAPISpec(t *testing.T) {
	h, _ := newOpenAPITestHandler(t)
	h.store.MCPConfig.ClientConfigs = []*schemas.MCPClientConfig{
		{ID: "oa-json", Name: "a", ConnectionType: schemas.MCPConnectionTypeOpenAPI, OpenAPIConfig: &schemas.MCPOpenAPIConfig{Spec: petstoreSpecJSON}},
		{ID: "oa-yaml", Name: "b", ConnectionType: schemas.MCPConnectionTypeOpenAPI, OpenAPIConfig: &schemas.MCPOpenAPIConfig{Spec: petstoreSpecYAML}},
		{ID: "oa-url", Name: "c", ConnectionType: schemas.MCPConnectionTypeOpenAPI, OpenAPIConfig: &schemas.MCPOpenAPIConfig{SpecURL: strPtr("https://x/openapi.json")}},
		{ID: "http-1", Name: "d", ConnectionType: schemas.MCPConnectionTypeHTTP},
	}
	get := func(id string) (int, string, string) {
		var req fasthttp.Request
		req.Header.SetMethod(fasthttp.MethodGet)
		ctx := initCtx(&req)
		ctx.SetUserValue("id", id)
		h.getMCPClientOpenAPISpec(ctx)
		return ctx.Response.StatusCode(), string(ctx.Response.Header.ContentType()), string(ctx.Response.Body())
	}
	status, ct, body := get("oa-json")
	assert.Equal(t, fasthttp.StatusOK, status)
	assert.Contains(t, ct, "application/json")
	assert.JSONEq(t, petstoreSpecJSON, body)

	status, ct, body = get("oa-yaml")
	assert.Equal(t, fasthttp.StatusOK, status)
	assert.Contains(t, ct, "yaml")
	assert.Equal(t, petstoreSpecYAML, body)

	status, _, _ = get("oa-url")
	assert.Equal(t, fasthttp.StatusNotFound, status, "a spec_url-only client has no stored document")
	status, _, _ = get("http-1")
	assert.Equal(t, fasthttp.StatusNotFound, status)
	status, _, _ = get("missing")
	assert.Equal(t, fasthttp.StatusNotFound, status)
}

func TestMergeOpenAPIConfigUpdate(t *testing.T) {
	existing := &schemas.MCPOpenAPIConfig{
		Spec:    "spec-v1",
		SpecURL: strPtr("https://x/v1.json"),
		BaseURL: strPtr("https://api.example.com"),
		SecurityCredentials: map[string]schemas.MCPOpenAPICredential{
			"ApiKey": {Value: schemas.NewSecretVar("k-1")},
			"Basic":  {Username: schemas.NewSecretVar("u"), Password: schemas.NewSecretVar("p")},
		},
		IncludeDeprecated: true,
		MaxResponseBytes:  10,
	}

	kept := mergeOpenAPIConfigUpdate(existing, &schemas.MCPOpenAPIConfig{IncludeDeprecated: true, MaxResponseBytes: 10})
	assert.Equal(t, "spec-v1", kept.Spec, "omitted spec keeps the stored document")
	assert.Equal(t, existing.SecurityCredentials, kept.SecurityCredentials, "omitted credentials are kept")
	assert.Equal(t, "https://api.example.com", *kept.BaseURL)

	masked := mergeOpenAPIConfigUpdate(existing, &schemas.MCPOpenAPIConfig{
		Spec: "spec-v2",
		SecurityCredentials: map[string]schemas.MCPOpenAPICredential{
			"ApiKey": {Value: existing.SecurityCredentials["ApiKey"].Value.Redacted()},
			"Basic":  {Username: schemas.NewSecretVar("u2"), Password: schemas.NewSecretVar("")},
		},
	})
	assert.Equal(t, "spec-v2", masked.Spec)
	assert.Equal(t, "k-1", masked.SecurityCredentials["ApiKey"].Value.GetValue(), "masked value preserves the stored secret")
	assert.Equal(t, "u2", masked.SecurityCredentials["Basic"].Username.GetValue())
	assert.Equal(t, "p", masked.SecurityCredentials["Basic"].Password.GetValue(), "an empty value preserves the stored secret")
	assert.False(t, masked.IncludeDeprecated, "the options block is replaced as a whole")

	dropped := mergeOpenAPIConfigUpdate(existing, &schemas.MCPOpenAPIConfig{SecurityCredentials: map[string]schemas.MCPOpenAPICredential{}})
	assert.Empty(t, dropped.SecurityCredentials, "an explicit empty map clears the credentials")

	newSource := mergeOpenAPIConfigUpdate(existing, &schemas.MCPOpenAPIConfig{SpecURL: strPtr("https://x/v2.json")})
	assert.Empty(t, newSource.Spec, "a new spec_url without an inline spec invalidates the stored document")
	assert.Equal(t, "https://x/v2.json", *newSource.SpecURL)

	cleared := mergeOpenAPIConfigUpdate(existing, &schemas.MCPOpenAPIConfig{BaseURL: strPtr("")})
	assert.Equal(t, "", *cleared.BaseURL, "an explicit empty base_url is passed through (prepare drops it to use the spec servers)")

	fresh := mergeOpenAPIConfigUpdate(nil, &schemas.MCPOpenAPIConfig{Spec: "s"})
	assert.Equal(t, "s", fresh.Spec)
}

func TestUnknownOpenAPITools(t *testing.T) {
	names := map[string]bool{"listPets": true, "createPet": true}
	assert.Empty(t, unknownOpenAPITools(schemas.WhiteList{"*"}, names))
	assert.Empty(t, unknownOpenAPITools(nil, names))
	assert.Equal(t, []string{"a", "zzz"}, unknownOpenAPITools(schemas.WhiteList{"zzz", "listPets", "a"}, names))
}

func TestOpenAPICredentialSecretReferences(t *testing.T) {
	env := secretVarFromJSON(t, `"env.BIFROST_TEST_UNSET_SECRET"`)
	plain := secretVarFromJSON(t, `"literal"`)
	refs := openAPICredentialSecretReferences(&schemas.MCPOpenAPIConfig{SecurityCredentials: map[string]schemas.MCPOpenAPICredential{
		"ApiKey": {Value: &env},
		"Basic":  {Username: &plain, Password: &env},
	}})
	assert.Equal(t, []string{"openapi_config.security_credentials.ApiKey.value", "openapi_config.security_credentials.Basic.password"}, refs)
	assert.Nil(t, openAPICredentialSecretReferences(nil))

	full := mcpClientSecretReferences(nil, nil, nil, nil, nil, &schemas.MCPOpenAPIConfig{SecurityCredentials: map[string]schemas.MCPOpenAPICredential{"ApiKey": {Value: &env}}})
	assert.Equal(t, []string{"openapi_config.security_credentials.ApiKey.value"}, full)
}

func strPtr(s string) *string { return &s }
