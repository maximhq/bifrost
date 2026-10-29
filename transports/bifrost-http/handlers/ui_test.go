package handlers

import (
	"embed"
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

func TestServeDashboardAPIPathsReturnJSONNotFound(t *testing.T) {
	h := &UIHandler{uiContent: embed.FS{}}

	for _, path := range []string{"/api", "/api/logs", "/api/logs/stats", "/api/mcp-logs/anything"} {
		var ctx fasthttp.RequestCtx
		ctx.Request.SetRequestURI(path)

		h.serveDashboard(&ctx)

		if got := ctx.Response.StatusCode(); got != fasthttp.StatusNotFound {
			t.Errorf("%s: status = %d, want %d", path, got, fasthttp.StatusNotFound)
		}
		if got := string(ctx.Response.Header.ContentType()); !strings.HasPrefix(got, "application/json") {
			t.Errorf("%s: content type = %q, want application/json", path, got)
		}
		if body := string(ctx.Response.Body()); !strings.Contains(body, "Route not found: "+path) {
			t.Errorf("%s: body = %q, want it to name the route", path, body)
		}
	}
}

func TestServeDashboardKeepsNonAPIPathsOnTheSPAPath(t *testing.T) {
	h := &UIHandler{uiContent: embed.FS{}}

	for _, path := range []string{"/apiary", "/dashboard/api-keys"} {
		var ctx fasthttp.RequestCtx
		ctx.Request.SetRequestURI(path)

		h.serveDashboard(&ctx)

		if body := string(ctx.Response.Body()); strings.Contains(body, "Route not found") {
			t.Errorf("%s: answered with the API JSON 404: %q", path, body)
		}
	}
}
