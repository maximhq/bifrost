package handlers

import (
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

// RegisterBinaryAudioRoute registers a dedicated route for Realtime providers
// whose protocol sends client audio as raw binary WebSocket frames (see
// schemas.RealtimeBinaryAudioProvider), e.g. Deepgram's Voice Agent API.
//
// This reuses the exact same handleUpgrade/session/relay logic as the base
// /v1/realtime route (RegisterRoutes in wsrealtime.go) — the auth, governance,
// key-selection, and turn-hook logic is not duplicated. Only the binary-frame
// branch inside relayClientToRealtimeProvider differs in behavior, and only
// for providers that opt in via RealtimeBinaryAudioProvider; every existing
// text-only provider is unaffected on both routes.
//
// Kept as a separate route (rather than only relying on the base route's
// dynamic per-provider binary support) so binary-audio-capable Realtime usage
// has its own clearly documented, independently discoverable entry point.
func (h *WSRealtimeHandler) RegisterBinaryAudioRoute(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	handler := lib.ChainMiddlewares(h.handleUpgrade, middlewares...)
	r.GET("/v1/realtime/audio", handler)
}
