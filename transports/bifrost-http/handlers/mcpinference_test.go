package handlers

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib/libtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// newPricedToolHandler serves POST /v1/mcp/tool/execute over a real core client connected to a
// local MCP server, alpha, whose echo and fail tools are priced at 0.003; fail always errors.
func newPricedToolHandler(t *testing.T, sendBackCost bool) *MCPInferenceHandler {
	t.Helper()
	upstream := server.NewMCPServer("alpha", "1.0.0")
	upstream.AddTool(mcp.NewTool("echo", mcp.WithString("text")), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(req.GetString("text", "")), nil
	})
	upstream.AddTool(mcp.NewTool("fail"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("upstream failed")
	})
	ts := httptest.NewServer(server.NewStreamableHTTPServer(upstream))
	t.Cleanup(ts.Close)

	config := libtest.MCPClients(t, &lib.Config{ClientConfig: &configstore.ClientConfig{}}, &schemas.MCPClientConfig{
		ID:               "alpha-id",
		Name:             "alpha",
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL),
		AuthType:         schemas.MCPAuthTypeNone,
		ToolsToExecute:   schemas.WhiteList{"*"},
		ToolPricing:      map[string]float64{"echo": 0.003, "fail": 0.003},
	})
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account:      lib.NewBaseAccount(config),
		Logger:       bifrost.NewDefaultLogger(schemas.LogLevelError),
		MCPConfig:    &schemas.MCPConfig{},
		SendBackCost: sendBackCost,
	})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)
	client.ConnectConfiguredMCPClients(context.Background(), nil)
	return &MCPInferenceHandler{client: client, config: config}
}

func executeToolRequest(h *MCPInferenceHandler, format, body string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetBodyString(body)
	if format == "responses" {
		h.executeResponsesMCPTool(ctx)
	} else {
		h.executeChatMCPTool(ctx)
	}
	return ctx
}

// The tool execute body is the bare tool message, so a priced call's send-back cost goes out as
// x-bifrost-cost, in both formats. No header when the setting is off or the call fails.
func TestExecuteMCPToolSendsBackCostHeader(t *testing.T) {
	const chatBody = `{"id":"c1","type":"function","function":{"name":"alpha-echo","arguments":"{\"text\":\"hi\"}"}}`
	const responsesBody = `{"call_id":"r1","type":"function_call","name":"alpha-echo","arguments":"{\"text\":\"hi\"}"}`

	h := newPricedToolHandler(t, true)
	for format, body := range map[string]string{"chat": chatBody, "responses": responsesBody} {
		ctx := executeToolRequest(h, format, body)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "%s: %s", format, ctx.Response.Body())
		assert.Equal(t, "0.003", string(ctx.Response.Header.Peek(lib.HeaderBifrostCost)), format)
		assert.NotContains(t, string(ctx.Response.Body()), "cost", "%s: the body stays the bare tool message", format)
	}

	failed := executeToolRequest(h, "chat", `{"id":"c2","type":"function","function":{"name":"alpha-fail","arguments":"{}"}}`)
	assert.NotEqual(t, fasthttp.StatusOK, failed.Response.StatusCode(), string(failed.Response.Body()))
	assert.Nil(t, failed.Response.Header.Peek(lib.HeaderBifrostCost), "a failed call of a priced tool has no cost")

	offHandler := newPricedToolHandler(t, false)
	off := executeToolRequest(offHandler, "chat", chatBody)
	require.Equal(t, fasthttp.StatusOK, off.Response.StatusCode(), string(off.Response.Body()))
	assert.Nil(t, off.Response.Header.Peek(lib.HeaderBifrostCost), "no header with the setting off")

	// The per-request header turns it on for one call, as on the model routes.
	optIn := &fasthttp.RequestCtx{}
	optIn.Request.Header.SetMethod(fasthttp.MethodPost)
	optIn.Request.Header.Set("x-bf-send-back-cost", "true")
	optIn.Request.SetBodyString(chatBody)
	offHandler.executeChatMCPTool(optIn)
	require.Equal(t, fasthttp.StatusOK, optIn.Response.StatusCode(), string(optIn.Response.Body()))
	assert.Equal(t, "0.003", string(optIn.Response.Header.Peek(lib.HeaderBifrostCost)), "x-bf-send-back-cost: true prices the call")
}
