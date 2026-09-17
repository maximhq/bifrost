package bifrost

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// Session state keeps a request that carries a session id on what served that session before.
// The session id is chosen by the caller, so two callers can send the same one; every piece of
// session state is therefore keyed under who the request's grant identity attributes it to,
// the virtual key and user, or under the deployment as a whole when neither is known. State
// lives in the schemas.KVStore the Bifrost instance was given, so it is shared wherever that
// store is.

const (
	sessionStateKeyPrefix = "session:v2:"

	// SessionStateKindRoute is the state kind of a session's route binding: the provider and
	// model that served the session for what the caller asked for.
	SessionStateKindRoute = "route"
	// SessionStateKindKey is the state kind of a session's key binding within one provider and
	// model.
	SessionStateKindKey = "key"

	// sessionAffinityResolvedKey is where the shipped affinity remembers, for the life of one
	// request, what it settled at resolve time, so that the outcome can tell a move from a bind
	// that lost a race.
	sessionAffinityResolvedKey schemas.BifrostContextKey = "bifrost-session-affinity-resolved"
)

// sessionResolution is what sessionAffinity settled for a request before it was dispatched.
type sessionResolution struct {
	route    string        // the route binding the request followed, as routeString spells it
	keyRoute schemas.Route // the route ResolveKey answered for
	keyID    string        // the key binding the request followed on keyRoute
}

// sessionAffinity is the session affinity Bifrost ships, installed when the configuration
// registers none. It knows nothing beyond what requests show it: a session stays on the
// provider that last served it, when routing still offers that provider, and on the key that
// last served it within a provider, while that key stays eligible. Bindings are written when
// a request is served, never when it fails, so a fallback that served becomes the session's
// new home and a key that only ever failed is never bound.
type sessionAffinity struct {
	kv     schemas.KVStore
	logger schemas.Logger
}

// NewSessionAffinity builds the affinity Bifrost ships over kv, the one Init installs when the
// configuration registers none. It is returned as the interface so a deployment that registers
// its own can embed it and delegate: decide first from what it knows, then let this one keep
// the bindings. Without a store it declines every request. The logger may not be nil.
func NewSessionAffinity(kv schemas.KVStore, logger schemas.Logger) schemas.SessionAffinity {
	return &sessionAffinity{kv: kv, logger: logger}
}

// ResolveRoute implements schemas.SessionAffinity: for a request that left the provider to
// routing, the provider that last served the session for that model moves to the front of the
// chain when the chain offers it. A request that named its provider is left as it is.
func (a *sessionAffinity) ResolveRoute(ctx *schemas.BifrostContext, requested schemas.Route, chain []schemas.Route) []schemas.Route {
	if a == nil || a.kv == nil || ctx == nil {
		return chain
	}
	// A context can carry what an earlier request on it followed, so this request starts clean:
	// Observe must never take a stale resolution for its own.
	ctx.ClearValue(sessionAffinityResolvedKey)
	if len(chain) == 0 {
		return chain
	}
	// A caller that named the provider is asking for it, not for whatever served the session
	// last. Such a request is never reordered; only its key within that provider is the
	// session's to keep.
	if requested.Provider != "" {
		return chain
	}
	key := SessionStateKey(ctx, SessionStateKindRoute, string(requested.Provider), requested.Model)
	bound, found := SessionStateString(a.kv, key)
	if !found {
		return chain
	}
	boundRoute, ok := ParseRouteState(bound)
	if !ok {
		// Nothing can follow a binding that names no route, and keeping it would refuse the
		// next served request's first-writer bind until it expired.
		if _, err := a.kv.Delete(key); err != nil {
			a.logger.Warn("error dropping unreadable session route binding: %s", err.Error())
		}
		return chain
	}
	at := slices.IndexFunc(chain, func(route schemas.Route) bool { return route.Provider == boundRoute.Provider })
	if at < 0 {
		// The chain is what this request may use, so a binding outside it is stale. Dropping it
		// lets the next served request bind afresh and the session converge there, instead of
		// scattering across routing's picks until the binding expires.
		if _, err := a.kv.Delete(key); err != nil {
			a.logger.Warn("error dropping session route binding to %s: %s", boundRoute.Provider, err.Error())
		}
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineSessionAffinity, schemas.LogLevelInfo, fmt.Sprintf("Session was last served by %s for %s, which this request cannot use, so the routing decision stands and the session rebinds on its next served request", boundRoute.Provider, requested.Model))
		// Refusing a stale binding, and dropping it, is as much a decision as following one: the
		// engine is listed so a trail entry is never attributed to an engine the request does not
		// record. The paths that write no entry, an unreadable binding and no binding at all, stay
		// silent at both levels.
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, schemas.RoutingEngineSessionAffinity)
		return chain
	}
	a.remember(ctx, func(r *sessionResolution) { r.route = RouteStateValue(chain[at]) })
	schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, schemas.RoutingEngineSessionAffinity)
	if at == 0 {
		// The session and routing agree, so the chain is left as it is. Say so anyway: a trail that
		// falls silent here cannot be told apart from one where the session was never consulted,
		// and the key level below reports every reuse whether or not it changed anything.
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineSessionAffinity, schemas.LogLevelInfo, fmt.Sprintf("Session stays on %s for %s, which routing also proposed", chain[0].Provider, requested.Model))
		return chain
	}
	resolved := make([]schemas.Route, 0, len(chain))
	resolved = append(resolved, chain[at])
	resolved = append(resolved, chain[:at]...)
	resolved = append(resolved, chain[at+1:]...)
	ctx.AppendRoutingEngineLog(schemas.RoutingEngineSessionAffinity, schemas.LogLevelInfo, fmt.Sprintf("Session stays on %s for %s; routing proposed %s", chain[at].Provider, requested.Model, chain[0].Provider))
	return resolved
}

// ResolveKey implements schemas.SessionAffinity: the key that last served the session on this
// provider and model is used while it is still eligible. Only the primary provider of a
// request answers from a binding; a fallback provider serves because the primary could not,
// and what it serves with is settled by the outcome.
func (a *sessionAffinity) ResolveKey(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, eligible []schemas.Key) (schemas.Key, bool) {
	if a == nil || a.kv == nil || ctx == nil || len(eligible) < 2 {
		return schemas.Key{}, false
	}
	if fallbackIndex, _ := ctx.Value(schemas.BifrostContextKeyFallbackIndex).(int); fallbackIndex > 0 {
		return schemas.Key{}, false
	}
	key := SessionStateKey(ctx, SessionStateKindKey, string(provider), model)
	boundID, found := SessionStateString(a.kv, key)
	if !found {
		return schemas.Key{}, false
	}
	for _, candidate := range eligible {
		if candidate.ID != boundID {
			continue
		}
		a.remember(ctx, func(r *sessionResolution) {
			r.keyRoute = schemas.Route{Provider: provider, Model: model}
			r.keyID = candidate.ID
		})
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineSessionAffinity, schemas.LogLevelInfo, fmt.Sprintf("Session reused key %s for %s/%s", candidate.Name, provider, model))
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, schemas.RoutingEngineSessionAffinity)
		return candidate, true
	}
	if _, err := a.kv.Delete(key); err != nil {
		a.logger.Warn("error deleting stale session key binding for provider=%s: %s", provider, err.Error())
	}
	ctx.AppendRoutingEngineLog(schemas.RoutingEngineSessionAffinity, schemas.LogLevelInfo, fmt.Sprintf("The key this session last used for %s/%s is no longer eligible, so one is being picked", provider, model))
	return schemas.Key{}, false
}

// Observe implements schemas.SessionAffinity: a served request binds the session to the route
// and key that served it. A binding the request followed is refreshed when it served and
// replaced when something else did; a session with no binding takes the first that lands. A
// fallback that served replaces the key binding on its provider, since it picked that key
// without the session. Failures write nothing.
func (a *sessionAffinity) Observe(ctx *schemas.BifrostContext, requested schemas.Route, outcome schemas.RouteOutcome) {
	if a == nil || a.kv == nil || ctx == nil || outcome.Served == nil {
		return
	}
	ttl := sessionTTLFromContext(ctx)
	resolved, _ := ctx.Value(sessionAffinityResolvedKey).(sessionResolution)
	served := *outcome.Served

	// Only a request that left the provider to routing binds a route; one that named it has
	// nothing to remember at that level, even when a fallback served.
	if requested.Provider == "" {
		a.settle(SessionStateKey(ctx, SessionStateKindRoute, string(requested.Provider), requested.Model), RouteStateValue(served), resolved.route, ttl)
	}
	if outcome.KeyID != "" {
		key := SessionStateKey(ctx, SessionStateKindKey, string(served.Provider), served.Model)
		followed := ""
		if resolved.keyRoute == served {
			followed = resolved.keyID
		}
		if followed == "" && outcome.Fallback {
			// A fallback attempt gets no key from the session, so a key its provider already held
			// for the session is older than the one that just served there, and gives way to it.
			a.replace(key, outcome.KeyID, ttl)
		} else {
			a.settle(key, outcome.KeyID, followed, ttl)
		}
	}
}

// settle writes what served under key. A request that followed no binding binds only if
// nothing has landed first, so parallel first requests of one session converge on whichever
// finished first. A request that followed a binding refreshes it when it served and moves it
// to what served instead.
func (a *sessionAffinity) settle(key, value, followed string, ttl time.Duration) {
	if followed == "" {
		if _, err := a.kv.SetNXWithTTL(key, value, ttl); err != nil {
			a.logger.Warn("error binding session to %s: %s", value, err.Error())
		}
		return
	}
	if followed != value {
		a.logger.Debug("session moved from %s to %s", followed, value)
	}
	a.replace(key, value, ttl)
}

// replace binds the session to value under key over whatever was there.
func (a *sessionAffinity) replace(key, value string, ttl time.Duration) {
	if err := a.kv.SetWithTTL(key, value, ttl); err != nil {
		a.logger.Warn("error rebinding session to %s: %s", value, err.Error())
	}
}

// remember records part of what this request resolved, for Observe.
func (a *sessionAffinity) remember(ctx *schemas.BifrostContext, update func(*sessionResolution)) {
	resolution, _ := ctx.Value(sessionAffinityResolvedKey).(sessionResolution)
	update(&resolution)
	ctx.SetValue(sessionAffinityResolvedKey, resolution)
}

// RouteStateValue spells a route as a route binding stores it. Provider names carry no slash,
// so the first one separates the provider from the model.
func RouteStateValue(route schemas.Route) string {
	return string(route.Provider) + "/" + route.Model
}

// ParseRouteState reads a route back from what RouteStateValue wrote. ok is false when the
// value names no provider.
func ParseRouteState(value string) (schemas.Route, bool) {
	provider, model, found := strings.Cut(value, "/")
	if !found || provider == "" {
		return schemas.Route{}, false
	}
	return schemas.Route{Provider: schemas.ModelProvider(provider), Model: model}, true
}

// resolveSessionRoute asks the session affinity to settle the chain the routing hooks
// produced for req and applies its answer: the first route becomes the primary, the rest its
// fallbacks. A request no hook could route is left for validation to refuse.
func (bifrost *Bifrost) resolveSessionRoute(ctx *schemas.BifrostContext, requested schemas.Route, req *schemas.BifrostRequest) {
	if !schemas.IsSessionAffinityActive(ctx) {
		// A request that carries a session but switched affinity off is routed as if it had none.
		// Say so: the session id is on the log record either way, so a trail that stayed silent
		// here reads as though affinity was consulted and had nothing to add.
		if sessionIDFromContext(ctx) != "" {
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineSessionAffinity, schemas.LogLevelInfo, fmt.Sprintf("Request carries a session but asked not to follow it, so the routing decision stands for %s", requested.Model))
		}
		return
	}
	provider, model, fallbacks := req.GetRequestFields()
	if provider == "" {
		return
	}
	chain := make([]schemas.Route, 0, 1+len(fallbacks))
	chain = append(chain, schemas.Route{Provider: provider, Model: model})
	for _, fallback := range fallbacks {
		chain = append(chain, schemas.Route{Provider: fallback.Provider, Model: fallback.Model})
	}
	resolved := bifrost.sessionAffinity.ResolveRoute(ctx, requested, chain)
	if len(resolved) == 0 || slices.Equal(resolved, chain) {
		return
	}
	req.SetProvider(resolved[0].Provider)
	req.SetModel(resolved[0].Model)
	next := make([]schemas.Fallback, 0, len(resolved)-1)
	for _, route := range resolved[1:] {
		next = append(next, schemas.Fallback{Provider: route.Provider, Model: route.Model})
	}
	req.SetFallbacks(next)
}

// observeSessionOutcome tells the session affinity how a request ended: the route that served
// it and the key that route used, or nothing served and the error it ended in.
func (bifrost *Bifrost) observeSessionOutcome(ctx *schemas.BifrostContext, requested schemas.Route, served *schemas.Route, fallback bool, err *schemas.BifrostError) {
	if !schemas.IsSessionAffinityActive(ctx) {
		return
	}
	outcome := schemas.RouteOutcome{Served: served, Fallback: fallback, Err: err}
	if served != nil {
		outcome.KeyID, _ = ctx.Value(schemas.BifrostContextKeySelectedKeyID).(string)
	}
	bifrost.sessionAffinity.Observe(ctx, requested, outcome)
}

// SessionStateKey builds the store key for one piece of session state: its kind, the request's
// session, who that session belongs to, and whatever further parts tell instances of that kind
// apart (a provider and model for a key binding). The session and its owner come from ctx: the
// session id, and the virtual key and user the grant identity attributes the request to. A
// request nothing governs, or whose identity is not settled, is scoped to the deployment.
// Everything but the kind is hashed, so the key is bounded in size and carries no
// caller-supplied text. Any component that keeps per-session state keys it this way, so one
// session id means one thing everywhere.
func SessionStateKey(ctx *schemas.BifrostContext, kind string, parts ...string) string {
	virtualKeyID, userID := sessionIdentityParts(ctx)
	h := sha256.New()
	for _, part := range append([]string{virtualKeyID, userID, sessionIDFromContext(ctx)}, parts...) {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return sessionStateKeyPrefix + kind + ":" + hex.EncodeToString(h.Sum(nil))
}

// sessionIdentityParts is who the request is attributed to, as far as its grant identity has
// settled it: the virtual key it was made with and the user behind it, either possibly empty.
func sessionIdentityParts(ctx *schemas.BifrostContext) (virtualKeyID, userID string) {
	if ctx == nil {
		return "", ""
	}
	grant := ctx.Grant()
	if grant == nil {
		return "", ""
	}
	identity := grant.Identity()
	if identity == nil {
		return "", ""
	}
	if key := identity.VirtualKey(); key != nil {
		virtualKeyID = key.ID
	}
	if user := identity.User(); user != nil {
		userID = user.ID
	}
	return virtualKeyID, userID
}

// sessionIDFromContext returns the request's session id, or "" when it carries none.
func sessionIDFromContext(ctx *schemas.BifrostContext) string {
	if ctx == nil {
		return ""
	}
	sessionID, _ := ctx.Value(schemas.BifrostContextKeySessionID).(string)
	return sessionID
}

// sessionTTLFromContext returns how long session state written for this request stays alive:
// the request's own TTL when it set one, otherwise schemas.DefaultSessionStickyTTL.
func sessionTTLFromContext(ctx *schemas.BifrostContext) time.Duration {
	if ctx != nil {
		if ttl, ok := ctx.Value(schemas.BifrostContextKeySessionTTL).(time.Duration); ok && ttl > 0 {
			return ttl
		}
	}
	return schemas.DefaultSessionStickyTTL
}

// SessionStateString reads the string under key. A missing key, a read error and an empty
// value all read as not found. Values come back as strings from a local store and as JSON
// bytes from one that replicated them, so both are accepted.
func SessionStateString(kv schemas.KVStore, key string) (string, bool) {
	raw, err := kv.Get(key)
	if err != nil {
		return "", false
	}
	var value string
	switch v := raw.(type) {
	case string:
		value = v
	case []byte:
		if err := sonic.Unmarshal(v, &value); err != nil {
			value = string(v)
		}
	}
	if value == "" {
		return "", false
	}
	return value, true
}
