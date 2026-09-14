package bifrost

import (
	"crypto/sha256"
	"encoding/hex"
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

	// SessionStateKindKey is the state kind of a session's key binding within one provider and
	// model.
	SessionStateKindKey = "key"
)

// keyAffinity is the session affinity Bifrost ships, installed when the configuration
// registers none: a session stays on the key it was first served by, per provider and model,
// for as long as that key remains eligible. The binding is written when the key is chosen,
// kept fresh on every reuse, and replaced only once the bound key has left the eligible pool.
//
// Only the primary provider of a request is bound. A fallback provider serves because the
// primary could not, and binding the session to it would keep the session there.
type keyAffinity struct {
	kv       schemas.KVStore
	selector schemas.KeySelector
	logger   schemas.Logger
}

// newKeyAffinity builds key affinity over kv, choosing an unbound session's key with selector.
// Neither selector nor logger may be nil.
func newKeyAffinity(kv schemas.KVStore, selector schemas.KeySelector, logger schemas.Logger) *keyAffinity {
	return &keyAffinity{kv: kv, selector: selector, logger: logger}
}

// ResolveKey implements schemas.SessionAffinity.
func (k *keyAffinity) ResolveKey(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, eligible []schemas.Key) (schemas.Key, bool) {
	if k == nil || k.kv == nil || ctx == nil || len(eligible) < 2 {
		return schemas.Key{}, false
	}
	if sessionIDFromContext(ctx) == "" {
		return schemas.Key{}, false
	}
	if fallbackIndex, _ := ctx.Value(schemas.BifrostContextKeyFallbackIndex).(int); fallbackIndex > 0 {
		return schemas.Key{}, false
	}

	key := sessionStateKey(ctx, SessionStateKindKey, string(provider), model)
	ttl := sessionTTLFromContext(ctx)

	if bound, found := k.lookup(key, provider, eligible); found {
		if err := k.kv.SetWithTTL(key, bound.ID, ttl); err != nil {
			k.logger.Warn("error refreshing session key binding for provider=%s key_id=%s: %s", provider, bound.ID, err.Error())
		}
		return bound, true
	}

	selected, err := k.selector(ctx, eligible, provider, model)
	if err != nil {
		k.logger.Warn("error selecting a key to bind the session to for provider=%s: %s", provider, err.Error())
		return schemas.Key{}, false
	}

	wrote, err := k.kv.SetNXWithTTL(key, selected.ID, ttl)
	if err != nil {
		k.logger.Warn("error binding session to provider=%s key_id=%s: %s", provider, selected.ID, err.Error())
		return selected, true
	}
	if wrote {
		return selected, true
	}

	// Another request for the same session bound first. Its key is the session's key.
	if bound, found := k.lookup(key, provider, eligible); found {
		return bound, true
	}
	return selected, true
}

// sessionStateKey builds the store key for one piece of session state: its kind, the request's
// session, who that session belongs to, and whatever further parts tell instances of that kind
// apart (a provider and model for a key binding). The session and its owner come from ctx: the
// session id, and the virtual key and user the grant identity attributes the request to. A
// request nothing governs, or whose identity is not settled, is scoped to the deployment.
// Everything but the kind is hashed, so the key is bounded in size and carries no
// caller-supplied text.
func sessionStateKey(ctx *schemas.BifrostContext, kind string, parts ...string) string {
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

// sessionStateString reads the string under key. A missing key, a read error and an empty
// value all read as not found. Values come back as strings from a local store and as JSON
// bytes from one that replicated them, so both are accepted.
func sessionStateString(kv schemas.KVStore, key string) (string, bool) {
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

// lookup reads the session's binding and resolves it against the eligible pool. A binding to
// a key no longer in the pool is deleted, so the caller binds afresh.
func (k *keyAffinity) lookup(key string, provider schemas.ModelProvider, eligible []schemas.Key) (schemas.Key, bool) {
	boundID, found := sessionStateString(k.kv, key)
	if !found {
		return schemas.Key{}, false
	}
	for _, candidate := range eligible {
		if candidate.ID == boundID {
			return candidate, true
		}
	}
	if _, err := k.kv.Delete(key); err != nil {
		k.logger.Warn("error deleting stale session key binding for provider=%s: %s", provider, err.Error())
	}
	return schemas.Key{}, false
}
