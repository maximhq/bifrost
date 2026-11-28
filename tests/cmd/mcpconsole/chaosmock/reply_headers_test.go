package chaosmock_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

func TestReplyHeadersEchoesHTTPHeaders(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{
		Name:         "header_echo",
		ReplyHeaders: true,
	})
	_ = srv

	c, ctx := func() (*client.Client, context.Context) {
		cl, err := client.NewStreamableHttpClient(fleet.ServerURL("alpha"),
			transport.WithHTTPHeaders(map[string]string{"Mcp-Param-Action": "project.list"}))
		if err != nil {
			t.Fatalf("NewStreamableHttpClient: %v", err)
		}
		return cl, t.Context()
	}()
	defer func() { _ = c.Close() }()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "test", Version: "1"},
		},
	}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	var req mcp.CallToolRequest
	req.Params.Name = "header_echo"
	res, err := c.CallTool(ctx, req)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	text := res.Content[0].(mcp.TextContent).Text
	var decoded struct {
		Tool    string            `json:"tool"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("unmarshal %q: %v", text, err)
	}
	if decoded.Tool != "header_echo" {
		t.Fatalf("got tool %q, want header_echo", decoded.Tool)
	}
	if got := decoded.Headers[http.CanonicalHeaderKey("Mcp-Param-Action")]; got != "project.list" {
		t.Fatalf("got Mcp-Param-Action %q, want project.list (all=%v)", got, decoded.Headers)
	}
}
