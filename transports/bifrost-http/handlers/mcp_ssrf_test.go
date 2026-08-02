package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TestRejectPrivateMCPTargetIfAuthBypassed_UnauthenticatedLoopbackRejected proves
// the unauthenticated (default-open) path cannot register an HTTP MCP client
// pointing at loopback - the reproduction target from the reported SSRF.
func TestRejectPrivateMCPTargetIfAuthBypassed_UnauthenticatedLoopbackRejected(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)

	rejected := rejectPrivateMCPTargetIfAuthBypassed(ctx, string(schemas.MCPConnectionTypeHTTP), schemas.NewSecretVar("http://127.0.0.1:8080/health"))

	if !rejected {
		t.Fatal("expected an unauthenticated loopback target to be rejected")
	}
	if ctx.Response.StatusCode() != fasthttp.StatusForbidden {
		t.Errorf("expected status %d, got %d", fasthttp.StatusForbidden, ctx.Response.StatusCode())
	}
}

// TestRejectPrivateMCPTargetIfAuthBypassed_UnauthenticatedLinkLocalRejected proves
// the blind-SSRF target from the second report (loopback standing in for cloud
// metadata) is also rejected for an unauthenticated caller.
func TestRejectPrivateMCPTargetIfAuthBypassed_UnauthenticatedLinkLocalRejected(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)

	rejected := rejectPrivateMCPTargetIfAuthBypassed(ctx, string(schemas.MCPConnectionTypeSSE), schemas.NewSecretVar("http://169.254.169.254/latest/meta-data/"))

	if !rejected {
		t.Fatal("expected an unauthenticated link-local target to be rejected")
	}
}

// TestRejectPrivateMCPTargetIfAuthBypassed_AuthenticatedLoopbackAllowed proves a
// genuinely authenticated caller keeps the documented ability to register a
// local MCP server (docs/mcp/connecting-to-servers.mdx uses
// http://localhost:3001/mcp as its own example) - this fix must not break that
// flow, only the unauthenticated one.
func TestRejectPrivateMCPTargetIfAuthBypassed_AuthenticatedLoopbackAllowed(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	// AuthBypassed intentionally left unset, as it would be for a genuinely
	// authenticated request.

	rejected := rejectPrivateMCPTargetIfAuthBypassed(ctx, string(schemas.MCPConnectionTypeHTTP), schemas.NewSecretVar("http://127.0.0.1:3001/mcp"))

	if rejected {
		t.Fatal("expected an authenticated caller's loopback target to be allowed")
	}
}

// TestRejectPrivateMCPTargetIfAuthBypassed_UnauthenticatedPublicTargetAllowed
// proves an unauthenticated caller can still register a normal internet-hosted
// MCP server - this fix only restricts private/loopback/link-local targets,
// not HTTP/SSE clients in general. Uses an IP literal (not a hostname) so the
// test doesn't depend on DNS being reachable in the test environment.
func TestRejectPrivateMCPTargetIfAuthBypassed_UnauthenticatedPublicTargetAllowed(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)

	rejected := rejectPrivateMCPTargetIfAuthBypassed(ctx, string(schemas.MCPConnectionTypeSSE), schemas.NewSecretVar("https://1.1.1.1/mcp/sse"))

	if rejected {
		t.Fatal("expected a public target to be allowed even for an unauthenticated caller")
	}
}

// TestRejectPrivateMCPTargetIfAuthBypassed_STDIOUnaffected proves this check is
// scoped to HTTP/SSE only; STDIO registration has its own separate gate.
func TestRejectPrivateMCPTargetIfAuthBypassed_STDIOUnaffected(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)

	rejected := rejectPrivateMCPTargetIfAuthBypassed(ctx, string(schemas.MCPConnectionTypeSTDIO), nil)

	if rejected {
		t.Fatal("expected STDIO connection type to be unaffected by this check")
	}
}
