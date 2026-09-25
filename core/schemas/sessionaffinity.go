package schemas

// Route is one provider and model a request can be sent to, with the provider key pinned for
// that attempt when a routing rule named one. An empty KeyID leaves the key to selection.
// The pin belongs to the route: an implementation that reorders a chain moves each route
// whole, so a pin stays with the provider it was decided for wherever that provider ends up.
type Route struct {
	Provider ModelProvider
	Model    string
	KeyID    string
}

// RouteOutcome is how a request ended.
type RouteOutcome struct {
	// Served is the route that answered, nil when no route did.
	Served *Route
	// KeyID is the key Served used, when key selection ran for it.
	KeyID string
	// Fallback reports that Served was not the first route of the dispatched chain.
	Fallback bool
	// Err is the failure the request ended in, nil when it was served.
	Err *BifrostError
}

// IsSessionAffinityActive reports whether a request takes part in session affinity: it carries
// a session id and has not switched affinity off (BifrostContextKeySessionAffinity). Core
// consults it before every SessionAffinity call, so an implementation never sees a request
// that does not take part.
func IsSessionAffinityActive(ctx *BifrostContext) bool {
	if ctx == nil {
		return false
	}
	if enabled, ok := ctx.Value(BifrostContextKeySessionAffinity).(bool); ok && !enabled {
		return false
	}
	sessionID, _ := ctx.Value(BifrostContextKeySessionID).(string)
	return sessionID != ""
}

// SessionAffinity keeps a request that carries a session id on what served that session
// before. Core asks it at three points and applies the answers; the policy behind them
// belongs to the implementation. The one Bifrost ships keeps a session on the provider and
// key that last served it, as far as the request's own outcome shows, forgets a binding the
// request followed into a failure, and is installed when nothing else is. A deployment that knows more, such as the health of its providers and
// keys, registers its own through BifrostConfig.SessionAffinity.
//
// Core asks only for requests IsSessionAffinityActive reports as taking part: ones that carry
// a session id and have not switched affinity off. Every method receives the request context,
// which carries the session id (BifrostContextKeySessionID), the session TTL
// (BifrostContextKeySessionTTL) and the identity that scopes the session to its caller.
type SessionAffinity interface {
	// ResolveRoute is asked once every routing hook has run, with the route the caller asked
	// for and the chain the hooks produced: chain[0] is the primary, the rest its fallbacks in
	// order. It answers with the chain to dispatch. The chain unchanged, or nil, leaves the
	// request as the hooks left it.
	ResolveRoute(ctx *BifrostContext, requested Route, chain []Route) []Route
	// ResolveKey is asked once the eligible key pool for a provider attempt has been built
	// with two or more keys in it. It answers with the key the session's state settles on, or
	// with ok=false to leave the choice to ordinary key selection. A returned key is used for
	// every attempt of the request.
	ResolveKey(ctx *BifrostContext, provider ModelProvider, model string, eligible []Key) (key Key, ok bool)
	// Observe is told how the request ended, once: for a unary request when its response is
	// in, for a stream once the upstream has accepted it and started streaming.
	Observe(ctx *BifrostContext, requested Route, outcome RouteOutcome)
}
