package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	bifrostmcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/mcp_headers"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
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
	added     []*schemas.MCPClientConfig
	updated   []*schemas.MCPClientConfig
	refreshed []string
}

func (m *openAPIRecordingManager) RefreshMCPClientTools(_ context.Context, id string) (int, error) {
	m.refreshed = append(m.refreshed, id)
	return 0, nil
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

// VerifyHeadersConnection mirrors core: verification dials a remote MCP server,
// so a config without a connection URL cannot be verified.
func (m *openAPIRecordingManager) VerifyHeadersConnection(_ context.Context, c *schemas.MCPClientConfig, _ map[string]string) (*schemas.MCPDiscovery, error) {
	if c.ConnectionString == nil || c.ConnectionString.GetValue() == "" {
		return nil, errors.New("connection URL is required for per-user headers verification")
	}
	return &schemas.MCPDiscovery{Tools: map[string]schemas.ChatTool{}, ToolNameMapping: map[string]string{}}, nil
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

// redirectingSpecServer serves a document with a relative server at
// /v2/openapi.json and redirects /openapi.json to it.
func redirectingSpecServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi.json" {
			http.Redirect(w, r, "/v2/openapi.json", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"openapi":"3.0.0","info":{"title":"Moved","version":"1"},"servers":[{"url":"./api"}],
"paths":{"/a":{"get":{"operationId":"getA","responses":{"200":{"description":"ok"}}}}}}`))
	}))
	t.Cleanup(ts.Close)
	return ts
}

// TestOpenAPISpecURL_RedirectResolvesAgainstTheDocument: a spec_url that
// redirects resolves relative servers against the document's final URL, in the
// preview and on create, and create keeps that URL as spec_url so later
// connects resolve the same way.
func TestOpenAPISpecURL_RedirectResolvesAgainstTheDocument(t *testing.T) {
	h, mgr := newOpenAPITestHandler(t)
	specServer := redirectingSpecServer(t)

	body, _ := json.Marshal(MCPOpenAPIPreviewRequest{SpecURL: specServer.URL + "/openapi.json"})
	status, respBody := doMCPRequest(t, h.previewOpenAPISpec, fasthttp.MethodPost, string(body), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	assert.Equal(t, specServer.URL+"/v2/api", decodePreview(t, respBody).BaseURL)
	var echoed struct {
		Spec    string `json:"spec"`
		SpecURL string `json:"spec_url"`
	}
	require.NoError(t, json.Unmarshal([]byte(respBody), &echoed))
	assert.Equal(t, specServer.URL+"/v2/openapi.json", echoed.SpecURL, "the preview names the document's final URL so the UI saves it with the fetched text")

	// The UI saves the fetched text with that URL: create resolves the
	// document's relative server where the preview showed it.
	body, _ = json.Marshal(map[string]any{
		"name": "moved_inline", "connection_type": "openapi", "auth_type": "none",
		"tools_to_execute": []string{"*"},
		"openapi_config":   map[string]any{"spec": echoed.Spec, "spec_url": echoed.SpecURL},
	})
	status, respBody = doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(body), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	require.Len(t, mgr.added, 1)
	require.NotNil(t, mgr.added[0].OpenAPIConfig)
	assert.Equal(t, specServer.URL+"/v2/openapi.json", *mgr.added[0].OpenAPIConfig.SpecURL)
	mgr.added = nil

	body, _ = json.Marshal(map[string]any{
		"name": "moved", "connection_type": "openapi", "auth_type": "none",
		"tools_to_execute": []string{"*"},
		"openapi_config":   map[string]any{"spec_url": specServer.URL + "/openapi.json"},
	})
	status, respBody = doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(body), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	require.Len(t, mgr.added, 1)
	oc := mgr.added[0].OpenAPIConfig
	require.NotNil(t, oc)
	assert.Equal(t, specServer.URL+"/v2/openapi.json", *oc.SpecURL, "the document's final URL is what relative servers resolve against")
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

// TestUpdateMCPClient_OpenAPI_StoredSpecFileIsNotARequestField: a client loaded
// from config.json keeps spec_file as provenance next to the inlined document.
// Only a spec_file sent in the request is refused; the stored one must not
// block every edit.
func TestUpdateMCPClient_OpenAPI_StoredSpecFileIsNotARequestField(t *testing.T) {
	h, mgr := newOpenAPITestHandler(t)
	createBody, _ := json.Marshal(map[string]any{
		"name": "petstore_file", "connection_type": "openapi", "auth_type": "none",
		"tools_to_execute": []string{"listPets"},
		"openapi_config":   map[string]any{"spec": petstoreSpecJSON},
	})
	status, respBody := doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(createBody), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	existing := mgr.added[0]
	existing.OpenAPIConfig.SpecFile = strPtr("specs/petstore.json") // as prepareOpenAPIClientFromFile leaves it
	h.store.MCPConfig.ClientConfigs = []*schemas.MCPClientConfig{existing}

	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"tools_to_execute":["listPets","getPetById"]}`, existing.ID, false)
	assert.Equal(t, fasthttp.StatusOK, status, respBody)

	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"openapi_config":{"include_deprecated":true}}`, existing.ID, false)
	assert.Equal(t, fasthttp.StatusOK, status, "an openapi_config edit on a file-loaded client: %s", respBody)

	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"openapi_config":{"spec_file":"other.json"}}`, existing.ID, false)
	assert.Equal(t, fasthttp.StatusBadRequest, status, respBody)
	assert.Contains(t, respBody, "spec_file is only supported in config.json")
}

// TestUpdateMCPClient_OpenAPI_ToolOnlyEditLeavesLiveConfigAlone: validating a
// tool-only edit must not write into the live openapi_config, even when it
// fails. For a spec_url-only client (config.json) the document is fetched to
// validate against, and after a successful save the runtime is asked to
// rebuild so it serves that same document.
func TestUpdateMCPClient_OpenAPI_ToolOnlyEditLeavesLiveConfigAlone(t *testing.T) {
	h, mgr := newOpenAPITestHandler(t)
	specServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(petstoreSpecJSON))
	}))
	t.Cleanup(specServer.Close)
	createBody, _ := json.Marshal(map[string]any{
		"name": "petstore_remote", "connection_type": "openapi", "auth_type": "none",
		"tools_to_execute": []string{"listPets"},
		"openapi_config":   map[string]any{"spec_url": specServer.URL + "/openapi.json", "base_url": "https://petstore.example.com/v1/"},
	})
	status, respBody := doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(createBody), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	existing := mgr.added[0]
	// A config.json spec_url client: fetched at connect, never inlined.
	existing.OpenAPIConfig.Spec = ""
	existing.OpenAPIConfig.SpecTitle = ""
	h.store.MCPConfig.ClientConfigs = []*schemas.MCPClientConfig{existing}
	live := *existing.OpenAPIConfig

	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"tools_to_execute":["listPets","nope"]}`, existing.ID, false)
	require.Equal(t, fasthttp.StatusBadRequest, status, respBody)
	assert.Equal(t, live, *existing.OpenAPIConfig, "a rejected edit leaves the live block untouched")

	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"tools_to_execute":["listPets","getPetById"]}`, existing.ID, false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	assert.Equal(t, live, *existing.OpenAPIConfig, "validation does not inline the fetched spec into the live block")
	assert.Equal(t, []string{existing.ID}, mgr.refreshed, "the runtime rebuilds from the document the allow-list was checked against")

	// A client being disabled has no live server to rebuild; enabling it later
	// connects from the document fresh.
	mgr.refreshed = nil
	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"disabled":true,"tools_to_execute":["listPets"]}`, existing.ID, false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	assert.Empty(t, mgr.refreshed, "a disabled client is not rebuilt")
	assert.NotContains(t, respBody, "partial_success")

	// An inline-spec client has nothing newer to rebuild from.
	mgr.refreshed = nil
	existing.OpenAPIConfig.Spec = petstoreSpecJSON
	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"tools_to_execute":["listPets"]}`, existing.ID, false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	assert.Empty(t, mgr.refreshed)
}

// TestAddMCPClient_OpenAPI_OperationServersNeedNoBaseURL: a spec whose every
// operation declares an absolute server is served without a base URL; for an
// unauthenticated caller each of those servers passes the public-target gate.
func TestAddMCPClient_OpenAPI_OperationServersNeedNoBaseURL(t *testing.T) {
	spec := func(server string) string {
		return `{"openapi":"3.0.0","info":{"title":"ops","version":"1"},"paths":{"/a":{"servers":[{"url":"` + server + `"}],"get":{"operationId":"getA","responses":{"200":{"description":"ok"}}}}}}`
	}
	h, mgr := newOpenAPITestHandler(t)
	body, _ := json.Marshal(map[string]any{
		"name": "opservers", "connection_type": "openapi", "auth_type": "none",
		"tools_to_execute": []string{"getA"},
		"openapi_config":   map[string]any{"spec": spec("https://a.example.com/svc")},
	})
	status, respBody := doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(body), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	require.Len(t, mgr.added, 1)

	private, _ := json.Marshal(map[string]any{
		"name": "opservers_private", "connection_type": "openapi", "auth_type": "none",
		"tools_to_execute": []string{"getA"},
		"openapi_config":   map[string]any{"spec": spec("http://127.0.0.1:9/svc")},
	})
	status, respBody = doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(private), "", true)
	assert.Equal(t, fasthttp.StatusForbidden, status, respBody)
}

// createPerUserHeadersOpenAPIClient registers an openapi client that collects
// a per-user header and returns its stored config.
func createPerUserHeadersOpenAPIClient(t *testing.T, h *MCPHandler, mgr *openAPIRecordingManager) *schemas.MCPClientConfig {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"name": "petstore_peruser", "connection_type": "openapi", "auth_type": "per_user_headers",
		"per_user_header_keys": []string{"x-user-token"},
		"tools_to_execute":     []string{"*"},
		"openapi_config":       map[string]any{"spec": petstoreSpecJSON},
	})
	status, respBody := doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(body), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	require.Len(t, mgr.added, 1)
	h.store.MCPConfig.ClientConfigs = []*schemas.MCPClientConfig{mgr.added[0]}
	return mgr.added[0]
}

// TestFlowSubmit_OpenAPIPerUserHeadersSavesWithoutMCPHandshake: an openapi
// client has no remote MCP server to verify against, so a user's header
// submission is saved once the required keys are present instead of failing
// the handshake for want of a connection URL.
func TestFlowSubmit_OpenAPIPerUserHeadersSavesWithoutMCPHandshake(t *testing.T) {
	h, mgr := newOpenAPITestHandler(t)
	h.store.MCPHeadersProvider = mcp_headers.NewProvider(h.store.ConfigStore, &mockLogger{})
	client := createPerUserHeadersOpenAPIClient(t, h, mgr)

	now := time.Now()
	flow := &configstoreTables.TableMCPPerUserHeaderFlow{
		ID: "flow-openapi-1", MCPClientID: client.ID, SessionID: "session-1", FlowMode: string(schemas.MCPAuthModeSession),
		Status: "pending", ExpiresAt: now.Add(10 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, h.store.ConfigStore.CreateMCPPerUserHeaderFlow(context.Background(), flow))

	headersHandler := NewMCPPerUserHeadersHandler(mgr, nil, h.store, nil)
	status, respBody := doMCPRequest(t, headersHandler.flowSubmit, fasthttp.MethodPost, `{"headers":{"x-user-token":"abc"}}`, flow.ID, false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	cred, err := h.store.MCPHeadersProvider.GetCredentialByMode(context.Background(), schemas.MCPAuthModeSession, "session-1", client.ID)
	require.NoError(t, err)
	require.NotNil(t, cred)
	assert.Equal(t, "abc", cred.Headers["x-user-token"])
}

// TestVerifyMCPClientHeaders_OpenAPIIsNotApplicable: admin header verification
// bootstraps tool discovery from a remote MCP server; an openapi client never
// needs it, and says so instead of failing with a connection-URL error.
func TestVerifyMCPClientHeaders_OpenAPIIsNotApplicable(t *testing.T) {
	h, mgr := newOpenAPITestHandler(t)
	client := createPerUserHeadersOpenAPIClient(t, h, mgr)
	status, respBody := doMCPRequest(t, h.verifyMCPClientHeaders, fasthttp.MethodPost, `{"user_headers":{"x-user-token":"abc"}}`, client.ID, false)
	assert.Equal(t, fasthttp.StatusBadRequest, status, respBody)
	assert.Contains(t, respBody, "openapi")
}

// TestPreviewOpenAPISpec_ClientNameMatchesCreateNaming: tool names are cut so
// "<client>-<tool>" fits 64 characters, so the preview must name tools with the
// client name create will use; otherwise a long operationId is cut differently
// and the selection the UI sends back is refused.
func TestPreviewOpenAPISpec_ClientNameMatchesCreateNaming(t *testing.T) {
	h, _ := newOpenAPITestHandler(t)
	longID := strings.Repeat("listAllThePets", 5)[:60]
	spec := `{"openapi":"3.0.3","info":{"title":"Long","version":"1"},"servers":[{"url":"https://petstore.example.com/v1"}],"paths":{"/pets":{"get":{"operationId":"` + longID + `","responses":{"200":{"description":"ok"}}}}}}`
	body, _ := json.Marshal(map[string]any{"spec": spec, "client_name": "petstore"})
	status, respBody := doMCPRequest(t, h.previewOpenAPISpec, fasthttp.MethodPost, string(body), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	names := previewToolNames(decodePreview(t, respBody))
	require.Len(t, names, 1)
	assert.LessOrEqual(t, len("petstore")+1+len(names[0]), 64, "the previewed name fits next to the client prefix")

	h2, _ := newOpenAPITestHandler(t)
	create, _ := json.Marshal(map[string]any{
		"name": "petstore", "connection_type": "openapi", "auth_type": "none",
		"tools_to_execute": []string{names[0]},
		"openapi_config":   map[string]any{"spec": spec},
	})
	status, respBody = doMCPRequest(t, h2.addMCPClient, fasthttp.MethodPost, string(create), "", false)
	assert.Equal(t, fasthttp.StatusOK, status, "create accepts the previewed name: %s", respBody)
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

	kept := mergeOpenAPIConfigUpdate(existing, &schemas.MCPOpenAPIConfig{}, openAPIOptionFields{})
	assert.Equal(t, "spec-v1", kept.Spec, "omitted spec keeps the stored document")
	assert.Equal(t, existing.SecurityCredentials, kept.SecurityCredentials, "omitted credentials are kept")
	assert.Equal(t, "https://api.example.com", *kept.BaseURL)
	assert.True(t, kept.IncludeDeprecated, "an omitted include_deprecated keeps the stored value")
	assert.Equal(t, 10, kept.MaxResponseBytes, "an omitted max_response_bytes keeps the stored value")

	reset := mergeOpenAPIConfigUpdate(existing, &schemas.MCPOpenAPIConfig{}, openAPIOptionFields{includeDeprecated: true, maxResponseBytes: true})
	assert.False(t, reset.IncludeDeprecated, "an explicit false is written")
	assert.Zero(t, reset.MaxResponseBytes, "an explicit 0 is written")

	masked := mergeOpenAPIConfigUpdate(existing, &schemas.MCPOpenAPIConfig{
		Spec: "spec-v2",
		SecurityCredentials: map[string]schemas.MCPOpenAPICredential{
			"ApiKey": {Value: existing.SecurityCredentials["ApiKey"].Value.Redacted()},
			"Basic":  {Username: schemas.NewSecretVar("u2"), Password: schemas.NewSecretVar("")},
		},
	}, openAPIOptionFields{})
	assert.Equal(t, "spec-v2", masked.Spec)
	assert.Equal(t, "k-1", masked.SecurityCredentials["ApiKey"].Value.GetValue(), "masked value preserves the stored secret")
	assert.Equal(t, "u2", masked.SecurityCredentials["Basic"].Username.GetValue())
	assert.Equal(t, "p", masked.SecurityCredentials["Basic"].Password.GetValue(), "an empty value preserves the stored secret")
	assert.True(t, masked.IncludeDeprecated, "options the caller left out keep the stored value")

	dropped := mergeOpenAPIConfigUpdate(existing, &schemas.MCPOpenAPIConfig{SecurityCredentials: map[string]schemas.MCPOpenAPICredential{}}, openAPIOptionFields{})
	assert.Empty(t, dropped.SecurityCredentials, "an explicit empty map clears the credentials")

	newSource := mergeOpenAPIConfigUpdate(existing, &schemas.MCPOpenAPIConfig{SpecURL: strPtr("https://x/v2.json")}, openAPIOptionFields{})
	assert.Empty(t, newSource.Spec, "a new spec_url without an inline spec invalidates the stored document")
	assert.Equal(t, "https://x/v2.json", *newSource.SpecURL)

	cleared := mergeOpenAPIConfigUpdate(existing, &schemas.MCPOpenAPIConfig{BaseURL: strPtr("")}, openAPIOptionFields{})
	assert.Equal(t, "", *cleared.BaseURL, "an explicit empty base_url is passed through (prepare drops it to use the spec servers)")

	fresh := mergeOpenAPIConfigUpdate(nil, &schemas.MCPOpenAPIConfig{Spec: "s"}, openAPIOptionFields{})
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

// TestGetMCPClients_OpenAPIConfigIsReturnedRedacted: the list carries an
// openapi client's openapi_config (options, metadata and masked credentials)
// so the edit sheet can show and round-trip it, without the document or the
// raw secrets.
func TestGetMCPClients_OpenAPIConfigIsReturnedRedacted(t *testing.T) {
	h, _ := newOpenAPITestHandler(t)
	h.client = &bifrost.Bifrost{MCPManager: bifrostmcp.NewMCPManager(context.Background(), schemas.MCPConfig{}, nil, &mockLogger{}, nil)}
	createBody, _ := json.Marshal(map[string]any{
		"name": "petstore", "connection_type": "openapi", "auth_type": "none",
		"tools_to_execute": []string{"listPets"},
		"openapi_config": map[string]any{
			"spec":                 petstoreSpecJSON,
			"max_response_bytes":   2048,
			"security_credentials": map[string]any{"ApiKeyAuth": map[string]string{"value": "k-secret-123"}},
		},
	})
	status, respBody := doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(createBody), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)

	status, respBody = doMCPRequest(t, h.getMCPClients, fasthttp.MethodGet, "", "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	var list struct {
		Clients []MCPClientResponse `json:"clients"`
	}
	require.NoError(t, json.Unmarshal([]byte(respBody), &list), respBody)
	require.Len(t, list.Clients, 1)
	cfg := list.Clients[0].Config.OpenAPIConfig
	require.NotNil(t, cfg, "the list must carry openapi_config")
	assert.Equal(t, "Petstore", cfg.SpecTitle)
	assert.Equal(t, 3, cfg.OperationCount)
	assert.Equal(t, 2048, cfg.MaxResponseBytes)
	assert.Empty(t, cfg.Spec, "the document is left out of the list")
	require.Contains(t, cfg.SecurityCredentials, "ApiKeyAuth")
	assert.NotContains(t, respBody, "k-secret-123", "credentials are masked")
}

// TestAddMCPClient_OpenAPI_OperationServersAreTargetChecked: with dashboard
// auth bypassed, an operation's own server is checked like the top-level one
// whenever no base_url overrides it, since that is where its calls go.
func TestAddMCPClient_OpenAPI_OperationServersAreTargetChecked(t *testing.T) {
	h, _ := newOpenAPITestHandler(t)
	mixed := `{"openapi":"3.0.3","info":{"title":"Mixed","version":"1"},"servers":[{"url":"https://8.8.8.8"}],"paths":{
"/a":{"get":{"operationId":"getA","responses":{"200":{"description":"ok"}}}},
"/b":{"servers":[{"url":"http://127.0.0.1:9"}],"get":{"operationId":"getB","responses":{"200":{"description":"ok"}}}}}}`
	body, _ := json.Marshal(map[string]any{
		"name": "mixed", "connection_type": "openapi", "auth_type": "none", "tools_to_execute": []string{"getA"},
		"openapi_config": map[string]any{"spec": mixed},
	})
	status, respBody := doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(body), "", true)
	assert.Equal(t, fasthttp.StatusForbidden, status, respBody)

	// An explicit base_url overrides every operation server, so only it is checked.
	body, _ = json.Marshal(map[string]any{
		"name": "mixed_override", "connection_type": "openapi", "auth_type": "none", "tools_to_execute": []string{"getA"},
		"openapi_config": map[string]any{"spec": mixed, "base_url": "https://8.8.8.8"},
	})
	status, respBody = doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(body), "", true)
	assert.Equal(t, fasthttp.StatusOK, status, respBody)
}

// TestUpdateMCPClient_OpenAPI_RenameRevalidatesToolNames: tool names are
// shortened to fit beside the client name, so a rename is checked against the
// tool names the new name produces.
func TestUpdateMCPClient_OpenAPI_RenameRevalidatesToolNames(t *testing.T) {
	h, mgr := newOpenAPITestHandler(t)
	longOp := strings.Repeat("a", 40)
	spec := `{"openapi":"3.0.3","info":{"title":"Long","version":"1"},"servers":[{"url":"https://api.example.com"}],"paths":{"/a":{"get":{"operationId":"` + longOp + `","responses":{"200":{"description":"ok"}}}},"/b":{"get":{"operationId":"short","responses":{"200":{"description":"ok"}}}}}}`
	createBody, _ := json.Marshal(map[string]any{
		"name": "p", "connection_type": "openapi", "auth_type": "none", "tools_to_execute": []string{longOp},
		"openapi_config": map[string]any{"spec": spec},
	})
	status, respBody := doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(createBody), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	h.store.MCPConfig.ClientConfigs = []*schemas.MCPClientConfig{mgr.added[0]}
	id := mgr.added[0].ID

	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"name":"p_`+strings.Repeat("x", 30)+`"}`, id, false)
	assert.Equal(t, fasthttp.StatusBadRequest, status, respBody)
	assert.Contains(t, respBody, "no tool named "+longOp)

	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"name":"`+strings.Repeat("n", 50)+`"}`, id, false)
	assert.Equal(t, fasthttp.StatusBadRequest, status, respBody)
	assert.Contains(t, respBody, "too long")

	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"name":"q"}`, id, false)
	assert.Equal(t, fasthttp.StatusOK, status, respBody)
}

// TestUpdateMCPClient_OpenAPI_OmittedOptionsKeepStoredValues: changing one
// field of openapi_config leaves include_deprecated and max_response_bytes as
// stored; sending them explicitly (even false or 0) writes them.
func TestUpdateMCPClient_OpenAPI_OmittedOptionsKeepStoredValues(t *testing.T) {
	h, mgr := newOpenAPITestHandler(t)
	createBody, _ := json.Marshal(map[string]any{
		"name": "petstore", "connection_type": "openapi", "auth_type": "none", "tools_to_execute": []string{"listPets"},
		"openapi_config": map[string]any{"spec": petstoreSpecJSON, "include_deprecated": true, "max_response_bytes": 10},
	})
	status, respBody := doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(createBody), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	h.store.MCPConfig.ClientConfigs = []*schemas.MCPClientConfig{mgr.added[0]}
	id := mgr.added[0].ID

	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"openapi_config":{"base_url":"https://other.example.com"}}`, id, false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	got := mgr.updated[len(mgr.updated)-1].OpenAPIConfig
	assert.True(t, got.IncludeDeprecated)
	assert.Equal(t, 10, got.MaxResponseBytes)

	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"openapi_config":{"include_deprecated":false,"max_response_bytes":0}}`, id, false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	got = mgr.updated[len(mgr.updated)-1].OpenAPIConfig
	assert.False(t, got.IncludeDeprecated)
	assert.Zero(t, got.MaxResponseBytes)
}

// TestUpdateMCPClient_OpenAPI_AuthBypassedDocumentChangeRequiresPublicTarget:
// an unauthenticated caller who changes where an OpenAPI client connects also
// turns on the public-target restriction, in the stored row and the runtime
// config, so a redirect or DNS change cannot later reach a private address.
// An authenticated edit leaves the flag as it was.
func TestUpdateMCPClient_OpenAPI_AuthBypassedDocumentChangeRequiresPublicTarget(t *testing.T) {
	h, mgr := newOpenAPITestHandler(t)
	createBody, _ := json.Marshal(map[string]any{
		"name": "petstore", "connection_type": "openapi", "auth_type": "none", "tools_to_execute": []string{"listPets"},
		"openapi_config": map[string]any{"spec": petstoreSpecJSON},
	})
	status, respBody := doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(createBody), "", false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	existing := mgr.added[0]
	require.False(t, existing.RequirePublicTarget)
	h.store.MCPConfig.ClientConfigs = []*schemas.MCPClientConfig{existing}

	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"tools_to_execute":["listPets","createPet"]}`, existing.ID, false)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	assert.False(t, mgr.updated[len(mgr.updated)-1].RequirePublicTarget, "an authenticated edit leaves the flag alone")

	update, _ := json.Marshal(map[string]any{"openapi_config": map[string]any{"base_url": "https://8.8.8.8"}})
	status, respBody = doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, string(update), existing.ID, true)
	require.Equal(t, fasthttp.StatusOK, status, respBody)
	assert.True(t, mgr.updated[len(mgr.updated)-1].RequirePublicTarget, "the runtime config gets the restriction")
	row, err := h.store.ConfigStore.GetMCPClientByID(context.Background(), existing.ID)
	require.NoError(t, err)
	assert.True(t, row.RequirePublicTarget, "the stored row gets the restriction")
}

// TestUpdateMCPClient_OpenAPI_AuthBypassedRetargetCannotReuseStoredSecrets:
// with dashboard auth off, a caller who changes where an OpenAPI client
// connects must not inherit the secrets stored for the old target: stored
// security credentials, stored static headers, or a per-user-headers client's
// callers' tokens. Replacing the credentials, or an authenticated edit, is fine.
func TestUpdateMCPClient_OpenAPI_AuthBypassedRetargetCannotReuseStoredSecrets(t *testing.T) {
	retarget := `{"openapi_config":{"base_url":"https://8.8.8.8"}}`
	// A public literal server, so the unauthenticated re-check of the current
	// target needs no DNS.
	spec := strings.Replace(petstoreSpecJSON, "https://petstore.example.com/v1", "https://8.8.4.4/v1", 1)
	setup := func(t *testing.T, extra map[string]any) (*MCPHandler, string) {
		h, mgr := newOpenAPITestHandler(t)
		body := map[string]any{"name": "petstore", "connection_type": "openapi", "auth_type": "none", "tools_to_execute": []string{"listPets"},
			"openapi_config": map[string]any{"spec": spec, "security_credentials": map[string]any{"ApiKeyAuth": map[string]string{"value": "stored-secret"}}}}
		for k, v := range extra {
			body[k] = v
		}
		createBody, _ := json.Marshal(body)
		status, respBody := doMCPRequest(t, h.addMCPClient, fasthttp.MethodPost, string(createBody), "", false)
		require.Equal(t, fasthttp.StatusOK, status, respBody)
		h.store.MCPConfig.ClientConfigs = []*schemas.MCPClientConfig{mgr.added[0]}
		return h, mgr.added[0].ID
	}

	t.Run("stored credentials", func(t *testing.T) {
		h, id := setup(t, nil)
		status, respBody := doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, retarget, id, true)
		assert.Equal(t, fasthttp.StatusForbidden, status, respBody)
	})
	t.Run("stored headers", func(t *testing.T) {
		h, id := setup(t, map[string]any{"auth_type": "headers", "headers": map[string]string{"X-Tenant-Key": "stored-header"},
			"openapi_config": map[string]any{"spec": spec}})
		status, respBody := doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, retarget, id, true)
		assert.Equal(t, fasthttp.StatusForbidden, status, respBody)
	})
	t.Run("per-user headers", func(t *testing.T) {
		h, id := setup(t, map[string]any{"auth_type": "per_user_headers", "per_user_header_keys": []string{"X-User-Token"},
			"openapi_config": map[string]any{"spec": spec}})
		status, respBody := doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, retarget, id, true)
		assert.Equal(t, fasthttp.StatusForbidden, status, respBody)
	})
	t.Run("credentials replaced", func(t *testing.T) {
		h, id := setup(t, nil)
		status, respBody := doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"openapi_config":{"base_url":"https://8.8.8.8","security_credentials":{}}}`, id, true)
		assert.Equal(t, fasthttp.StatusOK, status, respBody)
	})
	t.Run("authenticated", func(t *testing.T) {
		h, id := setup(t, nil)
		status, respBody := doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, retarget, id, false)
		assert.Equal(t, fasthttp.StatusOK, status, respBody)
	})
	t.Run("no target change", func(t *testing.T) {
		h, id := setup(t, nil)
		status, respBody := doMCPRequest(t, h.updateMCPClient, fasthttp.MethodPut, `{"openapi_config":{"include_deprecated":true}}`, id, true)
		assert.Equal(t, fasthttp.StatusOK, status, respBody)
	})
}

// TestMergeOpenAPIConfigUpdate_MaskedSpecURLKeepsStored: a spec_url sent back
// in its masked read form (the edit sheet round-trips it) keeps the stored
// URL and its document, like a masked credential keeps the stored secret.
func TestMergeOpenAPIConfigUpdate_MaskedSpecURLKeepsStored(t *testing.T) {
	stored := "https://specs.example.com/petstore.json?token=abc123"
	existing := &schemas.MCPOpenAPIConfig{Spec: "stored document", SpecURL: &stored}
	masked := lib.RedactURLCredentials(stored)
	require.NotEqual(t, stored, masked)
	merged := mergeOpenAPIConfigUpdate(existing, &schemas.MCPOpenAPIConfig{SpecURL: &masked}, openAPIOptionFields{})
	assert.Equal(t, stored, *merged.SpecURL)
	assert.Equal(t, "stored document", merged.Spec, "the stored document is kept")
}

// TestMergeOpenAPIConfigUpdate_MaskedBaseURLKeepsStored: a base_url sent back
// in its masked read form keeps the stored URL; a real change is applied.
func TestMergeOpenAPIConfigUpdate_MaskedBaseURLKeepsStored(t *testing.T) {
	stored := "https://api.example.com/v1?api_key=secret-key"
	existing := &schemas.MCPOpenAPIConfig{Spec: "doc", BaseURL: &stored}
	masked := lib.RedactURLCredentials(stored)
	require.NotEqual(t, stored, masked)
	merged := mergeOpenAPIConfigUpdate(existing, &schemas.MCPOpenAPIConfig{BaseURL: &masked}, openAPIOptionFields{})
	assert.Equal(t, stored, *merged.BaseURL)
	other := "https://other.example.com"
	merged = mergeOpenAPIConfigUpdate(existing, &schemas.MCPOpenAPIConfig{BaseURL: &other}, openAPIOptionFields{})
	assert.Equal(t, other, *merged.BaseURL)
}
