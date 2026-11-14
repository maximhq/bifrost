package openapimcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	coremcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildServer_ToolsAndInstructions(t *testing.T) {
	syn, err := Synthesize(parseFixture(t, "petstore-3.0.yaml"), SynthesizeOptions{ClientName: "petstore"})
	require.NoError(t, err)
	srv, err := BuildServer(ServerOptions{ClientName: "petstore", Synthesis: syn, BaseURL: "https://petstore.example.com/v1", HTTPClient: &http.Client{}})
	require.NoError(t, err)

	c, err := client.NewInProcessClient(srv)
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))
	initRes, err := c.Initialize(context.Background(), mcp.InitializeRequest{Params: mcp.InitializeParams{ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION}})
	require.NoError(t, err)
	assert.Equal(t, "bifrost-openapi-petstore", initRes.ServerInfo.Name)
	assert.Equal(t, "1.0.0", initRes.ServerInfo.Version)
	assert.Equal(t, "Petstore: A sample pet store API.", initRes.Instructions)

	tools, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	require.NoError(t, err)
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if tool.Name == "createPet" {
			require.NotNil(t, tool.Annotations.DestructiveHint)
			assert.True(t, *tool.Annotations.DestructiveHint)
			schemaJSON, err := json.Marshal(tool.InputSchema)
			require.NoError(t, err)
			assert.Contains(t, string(schemaJSON), `"name"`)
		}
	}
	assert.ElementsMatch(t, syn.ToolNames(), names)

	_, err = BuildServer(ServerOptions{Synthesis: &Synthesis{}})
	require.ErrorContains(t, err, "no tools")
}

func TestFactory_EndToEndThroughCoreManager(t *testing.T) {
	var got struct {
		path, query, auth, static string
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.query = r.URL.Path, r.URL.RawQuery
		got.auth, got.static = r.Header.Get("X-API-Key"), r.Header.Get("X-Static")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1,"name":"Rex"}]`))
	}))
	t.Cleanup(upstream.Close)

	manager := coremcp.NewMCPManager(context.Background(), schemas.MCPConfig{InProcessServerFactory: Factory()}, nil, nil, nil)
	config := &schemas.MCPClientConfig{
		ID:             "petstore-id",
		Name:           "petstore",
		ConnectionType: schemas.MCPConnectionTypeOpenAPI,
		AuthType:       schemas.MCPAuthTypeHeaders,
		Headers:        map[string]schemas.SecretVar{"X-Static": *schemas.NewSecretVar("yes")},
		ToolsToExecute: schemas.WhiteList{"listPets", "getPetById"},
		OpenAPIConfig: &schemas.MCPOpenAPIConfig{
			Spec:                string(fixture(t, "petstore-3.0.yaml")),
			BaseURL:             str(upstream.URL + "/v1"),
			SecurityCredentials: map[string]schemas.MCPOpenAPICredential{"ApiKeyAuth": {Value: schemas.NewSecretVar("k-123")}},
		},
	}
	require.NoError(t, manager.AddClient(context.Background(), config))
	t.Cleanup(func() { _ = manager.RemoveClient(config.ID) })

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	tools := manager.GetAvailableTools(ctx)
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Function.Name)
	}
	assert.ElementsMatch(t, []string{"petstore-listPets", "petstore-getPetById"}, names, "tools_to_execute filters the synthesized tools, and names carry the client prefix")

	name := "petstore-listPets"
	callID := "c1"
	msg, bifrostErr := manager.ExecuteChatTool(ctx, &schemas.ChatAssistantMessageToolCall{ID: &callID, Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name, Arguments: `{"limit": 5, "tags": ["a","b"]}`}})
	require.Nil(t, bifrostErr, "%+v", bifrostErr)
	require.NotNil(t, msg)
	require.NotNil(t, msg.Content)
	require.NotNil(t, msg.Content.ContentStr)
	assert.Contains(t, *msg.Content.ContentStr, `"name":"Rex"`)
	assert.Equal(t, "/v1/pets", got.path)
	assert.Equal(t, "limit=5&tags=a%2Cb", got.query)
	assert.Equal(t, "k-123", got.auth, "spec-mapped credential reached the upstream")
	assert.Equal(t, "yes", got.static, "core's static headers reached the upstream through the resolver")
}

func TestFactory_SpecURLAndFile(t *testing.T) {
	specServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture(t, "petstore-3.1.json"))
	}))
	t.Cleanup(specServer.Close)
	factory := Factory(WithConfigDir(t.TempDir()))
	deps := schemas.InProcessServerDeps{HTTPClient: specServer.Client()}

	srv, err := factory(context.Background(), &schemas.MCPClientConfig{Name: "u", OpenAPIConfig: &schemas.MCPOpenAPIConfig{SpecURL: str(specServer.URL + "/openapi.json")}}, deps)
	require.NoError(t, err)
	assert.NotNil(t, srv)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "spec.yaml"), fixture(t, "petstore-3.2.yaml"), 0o644))
	srv, err = Factory(WithConfigDir(dir))(context.Background(), &schemas.MCPClientConfig{Name: "f", OpenAPIConfig: &schemas.MCPOpenAPIConfig{SpecFile: str("spec.yaml")}}, deps)
	require.NoError(t, err)
	assert.NotNil(t, srv)
}

func TestFactory_ErrorClassification(t *testing.T) {
	factory := Factory()
	deps := schemas.InProcessServerDeps{}

	_, err := factory(context.Background(), &schemas.MCPClientConfig{Name: "x"}, deps)
	require.ErrorIs(t, err, schemas.ErrMCPOpenAPIConfigInvalid)

	_, err = factory(context.Background(), &schemas.MCPClientConfig{Name: "x", OpenAPIConfig: &schemas.MCPOpenAPIConfig{Spec: "not: openapi"}}, deps)
	require.ErrorIs(t, err, schemas.ErrMCPOpenAPIConfigInvalid)
	assert.Contains(t, err.Error(), "not an OpenAPI document")

	onlyUnsupported := `{"openapi":"3.0.0","info":{"title":"x","version":"1"},"servers":[{"url":"https://a"}],"paths":{"/u":{"post":{"requestBody":{"content":{"multipart/form-data":{}}},"responses":{"200":{"description":"ok"}}}}}}`
	_, err = factory(context.Background(), &schemas.MCPClientConfig{Name: "x", OpenAPIConfig: &schemas.MCPOpenAPIConfig{Spec: onlyUnsupported}}, deps)
	require.ErrorIs(t, err, schemas.ErrMCPOpenAPIConfigInvalid)
	assert.Contains(t, err.Error(), "no operation")

	noServer := `{"openapi":"3.0.0","info":{"title":"x","version":"1"},"paths":{"/a":{"get":{"responses":{"200":{"description":"ok"}}}}}}`
	_, err = factory(context.Background(), &schemas.MCPClientConfig{Name: "x", OpenAPIConfig: &schemas.MCPOpenAPIConfig{Spec: noServer}}, deps)
	require.ErrorIs(t, err, schemas.ErrMCPOpenAPIConfigInvalid)
	assert.Contains(t, err.Error(), "base_url")

	// A failed fetch is transient: not wrapped as permanent.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	_, err = factory(context.Background(), &schemas.MCPClientConfig{Name: "x", OpenAPIConfig: &schemas.MCPOpenAPIConfig{SpecURL: str(deadURL + "/openapi.json")}}, schemas.InProcessServerDeps{HTTPClient: &http.Client{}})
	require.Error(t, err)
	assert.False(t, errors.Is(err, schemas.ErrMCPOpenAPIConfigInvalid), "network failures keep retrying: %v", err)
	assert.True(t, strings.Contains(err.Error(), "loading spec_url"))

	// But an oversized fetched spec is permanent.
	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(make([]byte, MaxSpecBytes+1)) }))
	t.Cleanup(huge.Close)
	_, err = factory(context.Background(), &schemas.MCPClientConfig{Name: "x", OpenAPIConfig: &schemas.MCPOpenAPIConfig{SpecURL: str(huge.URL)}}, schemas.InProcessServerDeps{HTTPClient: huge.Client()})
	require.ErrorIs(t, err, schemas.ErrMCPOpenAPIConfigInvalid)
	require.ErrorIs(t, err, ErrSpecTooLarge)
}

// TestFactory_SpecURLConfigErrorsArePermanent: a spec_url that is not an
// http(s) URL, or cannot form a request, fails the same way on every attempt.
func TestFactory_SpecURLConfigErrorsArePermanent(t *testing.T) {
	factory := Factory()
	for _, bad := range []string{"ftp://example.com/openapi.json", "http://[::1"} {
		_, err := factory(context.Background(), &schemas.MCPClientConfig{Name: "x", OpenAPIConfig: &schemas.MCPOpenAPIConfig{SpecURL: str(bad)}}, schemas.InProcessServerDeps{})
		require.ErrorIs(t, err, schemas.ErrMCPOpenAPIConfigInvalid, "spec_url %q", bad)
	}
}

// TestFactory_OperationLevelServersSuffice: a spec without top-level servers
// whose every operation declares an absolute server can be served; each call
// goes to its operation's server.
func TestFactory_OperationLevelServersSuffice(t *testing.T) {
	ts, cap := newCaptureServer(t, nil)
	spec := `{"openapi":"3.0.0","info":{"title":"t","version":"1"},
"paths":{"/a":{"servers":[{"url":"` + ts.URL + `/svc"}],"get":{"operationId":"getA","responses":{"200":{"description":"ok"}}}}}}`
	cfg := &schemas.MCPClientConfig{Name: "opsrv", OpenAPIConfig: &schemas.MCPOpenAPIConfig{Spec: spec}}
	srv, err := Factory()(context.Background(), cfg, schemas.InProcessServerDeps{HTTPClient: ts.Client()})
	require.NoError(t, err)
	c, err := client.NewInProcessClient(srv)
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))
	_, err = c.Initialize(context.Background(), mcp.InitializeRequest{Params: mcp.InitializeParams{ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION}})
	require.NoError(t, err)
	req := mcp.CallToolRequest{}
	req.Params.Name = "getA"
	res, err := c.CallTool(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.Equal(t, "/svc/a", cap.path)

	// A spec mixing an operation with a server and one without still needs a base.
	mixed := `{"openapi":"3.0.0","info":{"title":"t","version":"1"},
"paths":{"/a":{"servers":[{"url":"https://a.example"}],"get":{"operationId":"getA","responses":{"200":{"description":"ok"}}}},
"/b":{"get":{"operationId":"getB","responses":{"200":{"description":"ok"}}}}}}`
	_, err = Factory()(context.Background(), &schemas.MCPClientConfig{Name: "mixed", OpenAPIConfig: &schemas.MCPOpenAPIConfig{Spec: mixed}}, schemas.InProcessServerDeps{})
	require.ErrorIs(t, err, schemas.ErrMCPOpenAPIConfigInvalid)
	assert.Contains(t, err.Error(), "base_url")
}

// TestFactory_RedirectedSpecURLResolvesServersAgainstTheDocument: a spec_url
// that redirects is resolved against the document's final URL, so a relative
// server calls the API next to the document, not next to the typed URL.
func TestFactory_RedirectedSpecURLResolvesServersAgainstTheDocument(t *testing.T) {
	var called string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi.json":
			http.Redirect(w, r, "/v2/openapi.json", http.StatusFound)
		case "/v2/openapi.json":
			_, _ = w.Write([]byte(`{"openapi":"3.0.0","info":{"title":"t","version":"1"},"servers":[{"url":"./api"}],
"paths":{"/a":{"get":{"operationId":"getA","responses":{"200":{"description":"ok"}}}}}}`))
		default:
			called = r.URL.Path
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(ts.Close)

	data, finalURL, err := LoadURL(context.Background(), ts.Client(), ts.URL+"/openapi.json")
	require.NoError(t, err)
	assert.NotEmpty(t, data)
	assert.Equal(t, ts.URL+"/v2/openapi.json", finalURL)

	cfg := &schemas.MCPClientConfig{Name: "redir", OpenAPIConfig: &schemas.MCPOpenAPIConfig{SpecURL: str(ts.URL + "/openapi.json")}}
	srv, err := Factory()(context.Background(), cfg, schemas.InProcessServerDeps{HTTPClient: ts.Client()})
	require.NoError(t, err)
	c, err := client.NewInProcessClient(srv)
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))
	_, err = c.Initialize(context.Background(), mcp.InitializeRequest{Params: mcp.InitializeParams{ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION}})
	require.NoError(t, err)
	req := mcp.CallToolRequest{}
	req.Params.Name = "getA"
	res, err := c.CallTool(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, res.IsError)
	assert.Equal(t, "/v2/api/a", called)
}

// TestFactory_SpecURLWithoutHTTPClientIsPermanent: a spec_url with no HTTP
// client to fetch it is a configuration fault, so connects do not retry it.
func TestFactory_SpecURLWithoutHTTPClientIsPermanent(t *testing.T) {
	_, err := Factory()(context.Background(), &schemas.MCPClientConfig{Name: "x", OpenAPIConfig: &schemas.MCPOpenAPIConfig{SpecURL: str("https://example.com/openapi.json")}}, schemas.InProcessServerDeps{})
	require.ErrorIs(t, err, schemas.ErrMCPOpenAPIConfigInvalid)
}

// TestFactory_RequiresHTTPClient: the HTTP client carries the network policy
// (SSRF guard, proxy, TLS), so a factory call without one fails rather than
// calling the upstream through an unguarded default client.
func TestFactory_RequiresHTTPClient(t *testing.T) {
	spec := `{"openapi":"3.0.3","info":{"title":"x","version":"1"},"servers":[{"url":"https://api.example.com"}],"paths":{"/a":{"get":{"operationId":"a","responses":{"200":{"description":"ok"}}}}}}`
	_, err := Factory()(context.Background(), &schemas.MCPClientConfig{Name: "x", OpenAPIConfig: &schemas.MCPOpenAPIConfig{Spec: spec}}, schemas.InProcessServerDeps{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP client")
}
