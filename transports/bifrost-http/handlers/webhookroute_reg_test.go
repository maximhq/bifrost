package handlers

import (
	"testing"

	"github.com/fasthttp/router"
	"github.com/valyala/fasthttp"
)

// TestWebhookRoutesRegister pins that the static /api/webhooks/deliveries route
// can coexist with the /api/webhooks/{id} wildcard without the router panicking.
func TestWebhookRoutesRegister(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("webhook route registration panicked: %v", r)
		}
	}()
	h := &WebhookHandler{}
	r := router.New()
	h.RegisterRoutes(r)

	for _, tc := range []struct{ method, path string }{
		{fasthttp.MethodGet, "/api/webhooks/deliveries"},
		{fasthttp.MethodGet, "/api/webhooks/some-id"},
		{fasthttp.MethodGet, "/api/webhooks/some-id/deliveries"},
		{fasthttp.MethodPost, "/api/webhooks/deliveries/some-id/redeliver"},
	} {
		if handler, _ := r.Lookup(tc.method, tc.path, &fasthttp.RequestCtx{}); handler == nil {
			t.Errorf("no handler resolved for %s %s", tc.method, tc.path)
		}
	}
}
