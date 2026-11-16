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
// Any ephemeral client secret counts as a credential. Bifrost-minted tokens resolve through the
// shared mapping after admission. Unmapped tokens remain opaque provider credentials and are sent
// upstream so the provider remains authoritative about whether they are valid.
//
// Returns nil when the connection may proceed.
func refuseUnauthenticatedRealtime(
	enforceAuthOnInference bool,
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
		return nil
	}
	return newRealtimeWireBifrostError(401, "invalid_request_error", realtimeAuthRefusalMessage)
}

// realtimeUnresolvedCredentialMessage is what a caller presenting a credential governance could
// not resolve is told, mirroring the per-turn refusal so admission and turns name the same fault.
const realtimeUnresolvedCredentialMessage = "the provided credential does not exist, has expired, or has been revoked"

// refuseUnresolvedRealtimeCredential is the second admission question, asked after the
// per-request pipeline (RunPreRequestHooks) has resolved the request's access: the credential
// that was presented, did it turn out to exist? Without this, a forged sk-bf-* key passes the
// presence gate above, and the connection selects the operator's provider key and opens a real
// upstream session that only the first turn refuses - by which point it has already consumed a
// session slot and an upstream connection.
//
// It reads the answer governance recorded on the grant through governance's own exported
// predicate; nothing is resolved or re-implemented here, so admission cannot drift from what the
// per-turn pipeline enforces.
//
// token is the credential the transport extracted from whichever auth header carried it.
// Unmapped ephemeral client secrets (ek_*) are exempt: Bifrost may not have minted them, so
// absence of a local resolution proves nothing and the provider stays authoritative upstream.
// A mapped token whose mapping restored a virtual key is different: that key was placed on the
// context before the pipeline ran, so it is locally resolvable and must still resolve -
// otherwise a token minted before its virtual key was revoked would keep admitting connections
// for the remainder of its TTL. Callers therefore pass mappedVirtualKey only when the mapping
// carries a virtual key; a provider-token-only mapping (minted on an open deployment or with a
// direct provider key) restores nothing locally resolvable and keeps the exemption.
//
// Returns nil when the connection may proceed.
func refuseUnresolvedRealtimeCredential(
	enforceAuthOnInference bool,
	bifrostCtx *schemas.BifrostContext,
	token string,
	mappedVirtualKey bool,
) *schemas.BifrostError {
	if !enforceAuthOnInference {
		return nil
	}
	if isRealtimeEphemeralToken(token) && !mappedVirtualKey {
		return nil
	}
	if governance.PresentedCredentialResolved(bifrostCtx) {
		return nil
	}
	return newRealtimeWireBifrostError(401, "invalid_request_error", realtimeUnresolvedCredentialMessage)
}
