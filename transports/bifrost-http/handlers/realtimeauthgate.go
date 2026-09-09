package handlers

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance"
)

// realtimeAuthRefusalMessage is what an anonymous realtime caller is told. It names the two
// credentials that work, because the realtime flow has a second one (the ephemeral client
// secret) that the generic inference message does not mention.
const realtimeAuthRefusalMessage = "authentication is required for realtime connections. Provide a virtual key (x-bf-vk or an sk-bf- bearer token), or an ephemeral client secret minted by POST /v1/realtime/client_secrets."

// refuseUnauthenticatedRealtime reports whether a realtime connection must be refused before it
// is established, and is the admission gate for every realtime transport (WebSocket upgrade and
// both WebRTC SDP routes).
//
// Realtime needs this gate because it is the one inference surface where accepting the
// connection already spends something: both transports select the operator's provider key and
// open an upstream session before any turn exists. The per-turn pipeline
// (RunRealtimeTurnPreHooks -> governance PreLLMHook) is the authoritative check for access and
// limits and keeps that job, but it first runs a turn too late to stop an anonymous client from
// standing up a session on the operator's key. Nothing in the transport middleware covers the
// gap either: inference auth is delegated wholesale to governance, whose refusal arrives with
// the first turn.
//
// The question asked here is deliberately the narrow one - "was any credential presented?" -
// answered by governance's own exported predicate rather than a second implementation of it, so
// admission cannot drift from the funnel's first step. It settles no limits, so it cannot
// double-count usage against the turns that follow.
//
// A valid ephemeral client secret counts as a credential. That is the whole point of
// POST /v1/realtime/client_secrets: a browser cannot hold a virtual key, so it is handed a
// short-lived ek_ token instead, and the mapping back to the real key is restored after the
// connection is established. Requiring a virtual key here would break that documented flow.
// An ek_ token that resolves to no mapping is expired, revoked, or forged, and is refused.
//
// Returns nil when the connection may proceed.
func refuseUnauthenticatedRealtime(
	enforceAuthOnInference bool,
	kv schemas.KVStore,
	bifrostCtx *schemas.BifrostContext,
	authorizationHeader string,
) *schemas.BifrostError {
	// The operator has not asked for authentication on inference, so realtime is open for the
	// same reason every other inference route is. Closing it here regardless would make realtime
	// stricter than /v1/chat/completions on the same deployment.
	if !enforceAuthOnInference {
		return nil
	}
	if governance.PresentedAnyCredential(bifrostCtx) {
		return nil
	}
	if token := extractRealtimeBearerTokenFromHeader(authorizationHeader); isRealtimeEphemeralToken(token) {
		if _, ok := lookupRealtimeEphemeralKeyMapping(kv, token); ok {
			return nil
		}
	}
	return newRealtimeWireBifrostError(401, "invalid_request_error", realtimeAuthRefusalMessage)
}
