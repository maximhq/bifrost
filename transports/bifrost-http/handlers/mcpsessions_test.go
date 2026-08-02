package handlers

import (
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestParseMCPSessionsListQuery_Defaults(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/mcp/sessions")
	q, ok := parseMCPSessionsListQuery(ctx)
	if !ok {
		t.Fatalf("expected parse to succeed: body=%s", string(ctx.Response.Body()))
	}
	if q.Limit != mcpSessionsDefaultLimit {
		t.Fatalf("expected default limit %d, got %d", mcpSessionsDefaultLimit, q.Limit)
	}
	if q.Offset != 0 {
		t.Fatalf("expected default offset 0, got %d", q.Offset)
	}
	if q.Filters.Search != "" || len(q.Filters.Statuses) != 0 || len(q.Filters.AuthModes) != 0 || len(q.Filters.MCPClientIDs) != 0 || len(q.Kinds) != 0 {
		t.Fatalf("expected zero-value filters, got %#v", q)
	}
}

func TestParseMCPSessionsListQuery_AllParams(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/mcp/sessions?q=hello&kind=token,header&status=active,orphaned&auth_mode=user,vk&mcp_client_id=c1,c2&limit=25&offset=50")
	q, ok := parseMCPSessionsListQuery(ctx)
	if !ok {
		t.Fatalf("parse failed: body=%s", string(ctx.Response.Body()))
	}
	if q.Filters.Search != "hello" {
		t.Fatalf("search=%q", q.Filters.Search)
	}
	if strings.Join(q.Kinds, ",") != "token,header" {
		t.Fatalf("kinds=%v", q.Kinds)
	}
	if strings.Join(q.Filters.Statuses, ",") != "active,orphaned" {
		t.Fatalf("statuses=%v", q.Filters.Statuses)
	}
	if strings.Join(q.Filters.AuthModes, ",") != "user,vk" {
		t.Fatalf("auth_modes=%v", q.Filters.AuthModes)
	}
	if strings.Join(q.Filters.MCPClientIDs, ",") != "c1,c2" {
		t.Fatalf("mcp_client_ids=%v", q.Filters.MCPClientIDs)
	}
	if q.Limit != 25 {
		t.Fatalf("limit=%d", q.Limit)
	}
	if q.Offset != 50 {
		t.Fatalf("offset=%d", q.Offset)
	}
}

func TestParseMCPSessionsListQuery_LimitCappedAtMax(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/mcp/sessions?limit=9999")
	q, ok := parseMCPSessionsListQuery(ctx)
	if !ok {
		t.Fatalf("parse failed")
	}
	if q.Limit != mcpSessionsMaxLimit {
		t.Fatalf("expected limit capped at %d, got %d", mcpSessionsMaxLimit, q.Limit)
	}
}

func TestParseMCPSessionsListQuery_InvalidLimit(t *testing.T) {
	for _, raw := range []string{"abc", "-5", "0"} {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("/api/mcp/sessions?limit=" + raw)
		if _, ok := parseMCPSessionsListQuery(ctx); ok {
			t.Fatalf("limit=%q should have failed validation", raw)
		}
		if code := ctx.Response.StatusCode(); code != fasthttp.StatusBadRequest {
			t.Fatalf("limit=%q: expected 400, got %d", raw, code)
		}
	}
}

func TestParseMCPSessionsListQuery_InvalidOffset(t *testing.T) {
	for _, raw := range []string{"abc", "-1"} {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("/api/mcp/sessions?offset=" + raw)
		if _, ok := parseMCPSessionsListQuery(ctx); ok {
			t.Fatalf("offset=%q should have failed validation", raw)
		}
		if code := ctx.Response.StatusCode(); code != fasthttp.StatusBadRequest {
			t.Fatalf("offset=%q: expected 400, got %d", raw, code)
		}
	}
}

func TestMCPSessionsListQuery_KindAllowed(t *testing.T) {
	empty := mcpSessionsListQuery{}
	if !empty.kindAllowed("token") || !empty.kindAllowed("flow") {
		t.Fatalf("empty filter must allow all kinds")
	}
	q := mcpSessionsListQuery{Kinds: []string{"token", "header"}}
	if !q.kindAllowed("token") || !q.kindAllowed("header") {
		t.Fatalf("filter should match listed kinds")
	}
	if q.kindAllowed("flow") {
		t.Fatalf("flow should not match {token, header}")
	}
}
