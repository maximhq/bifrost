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
	srv, err := BuildServer(ServerOptions{ClientName: "petstore", Synthesis: syn, BaseURL: "https://petstore.example.com/v1"})
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
	_, err = factory(context.Background(), &schemas.MCPClientConfig{Name: "x", OpenAPIConfig: &schemas.MCPOpenAPIConfig{SpecURL: str(deadURL + "/openapi.json")}}, deps)
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
