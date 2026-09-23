package bifrost

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func sessionCtx(sessionID string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if sessionID != "" {
		ctx.SetValue(schemas.BifrostContextKeySessionID, sessionID)
	}
	return ctx
}

// stubIdentity is the least an installing layer could settle: which key and user the request
// is attributed to. Everything else answers as unknown.
type stubIdentity struct {
	virtualKeyID string
	userID       string
}

func (i *stubIdentity) Credential() schemas.Credential { return schemas.Credential{} }
func (i *stubIdentity) Presented() bool                { return i.virtualKeyID != "" || i.userID != "" }
func (i *stubIdentity) User() *schemas.UserRef {
	if i.userID == "" {
		return nil
	}
	return &schemas.UserRef{ID: i.userID}
}
func (i *stubIdentity) VirtualKey() *schemas.EntityRef {
	if i.virtualKeyID == "" {
		return nil
	}
	return &schemas.EntityRef{ID: i.virtualKeyID}
}
func (i *stubIdentity) Teams() []schemas.EntityRef         { return nil }
func (i *stubIdentity) Customers() []schemas.EntityRef     { return nil }
func (i *stubIdentity) BusinessUnits() []schemas.EntityRef { return nil }
func (i *stubIdentity) Project() *schemas.EntityRef        { return nil }

// stubGrant carries only an identity, which is all session state reads.
type stubGrant struct {
	identity schemas.Identity
}

func (g *stubGrant) Identity() schemas.Identity          { return g.identity }
func (g *stubGrant) Access() schemas.Access              { return nil }
func (g *stubGrant) Limits() schemas.Limits              { return nil }
func (g *stubGrant) SetIdentity(i schemas.Identity) bool { g.identity = i; return i != nil }
func (g *stubGrant) SetAccess(schemas.Access) bool       { return false }
func (g *stubGrant) SetLimits(schemas.Limits) bool       { return false }

// attributed installs a grant whose identity names the given key and user on ctx.
func attributed(ctx *schemas.BifrostContext, virtualKeyID, userID string) *schemas.BifrostContext {
	ctx.SetGrant(&stubGrant{identity: &stubIdentity{virtualKeyID: virtualKeyID, userID: userID}})
	return ctx
}

var sessionTestPool = []schemas.Key{
	{ID: "key-a", Name: "Key A"},
	{ID: "key-b", Name: "Key B"},
	{ID: "key-c", Name: "Key C"},
}

func routeOf(provider schemas.ModelProvider, model string) schemas.Route {
	return schemas.Route{Provider: provider, Model: model}
}

func servedBy(route schemas.Route, keyID string, fallback bool) schemas.RouteOutcome {
	return schemas.RouteOutcome{Served: &route, KeyID: keyID, Fallback: fallback}
}

func testAffinity(kv schemas.KVStore) *sessionAffinity {
	return NewSessionAffinity(kv, NewDefaultLogger(schemas.LogLevelError)).(*sessionAffinity)
}

func enginesUsed(ctx *schemas.BifrostContext) []string {
	engines, _ := ctx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string)
	return engines
}

func trailMentions(ctx *schemas.BifrostContext, text string) bool {
	for _, entry := range ctx.GetRoutingEngineLogs() {
		if entry.Engine == schemas.RoutingEngineSessionAffinity && strings.Contains(entry.Message, text) {
			return true
		}
	}
	return false
}

// recordingAffinity answers what a test tells it and records what core asked.
type recordingAffinity struct {
	mu          sync.Mutex
	routeAnswer func(chain []schemas.Route) []schemas.Route
	routeCalls  int
	keyCalls    int
	requested   []schemas.Route
	outcomes    []schemas.RouteOutcome
}

func (r *recordingAffinity) ResolveRoute(_ *schemas.BifrostContext, _ schemas.Route, chain []schemas.Route) []schemas.Route {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routeCalls++
	if r.routeAnswer != nil {
		return r.routeAnswer(chain)
	}
	return chain
}

func (r *recordingAffinity) ResolveKey(*schemas.BifrostContext, schemas.ModelProvider, string, []schemas.Key) (schemas.Key, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keyCalls++
	return schemas.Key{}, false
}

func (r *recordingAffinity) Observe(_ *schemas.BifrostContext, requested schemas.Route, outcome schemas.RouteOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requested = append(r.requested, requested)
	r.outcomes = append(r.outcomes, outcome)
}

func TestSessionStateKeyScopesBySessionAndIdentity(t *testing.T) {
	base := SessionStateKey(attributed(sessionCtx("session-1"), "vk-1", ""), SessionStateKindKey, "openai", "gpt-4o")
	if !strings.HasPrefix(base, "session:v2:key:") {
		t.Fatalf("key %q lacks the kind prefix", base)
	}
	if strings.Contains(base, "session-1") || strings.Contains(base, "vk-1") || strings.Contains(base, "gpt-4o") {
		t.Fatalf("key %q leaks a hashed part", base)
	}
	if base != SessionStateKey(attributed(sessionCtx("session-1"), "vk-1", ""), SessionStateKindKey, "openai", "gpt-4o") {
		t.Fatal("key is not deterministic")
	}

	variants := map[string]string{
		"other virtual key": SessionStateKey(attributed(sessionCtx("session-1"), "vk-2", ""), SessionStateKindKey, "openai", "gpt-4o"),
		"same id as a user": SessionStateKey(attributed(sessionCtx("session-1"), "", "vk-1"), SessionStateKindKey, "openai", "gpt-4o"),
		"key and user":      SessionStateKey(attributed(sessionCtx("session-1"), "vk-1", "u-1"), SessionStateKindKey, "openai", "gpt-4o"),
		"other session":     SessionStateKey(attributed(sessionCtx("session-2"), "vk-1", ""), SessionStateKindKey, "openai", "gpt-4o"),
		"other provider":    SessionStateKey(attributed(sessionCtx("session-1"), "vk-1", ""), SessionStateKindKey, "azure", "gpt-4o"),
		"other model":       SessionStateKey(attributed(sessionCtx("session-1"), "vk-1", ""), SessionStateKindKey, "openai", "gpt-4o-mini"),
		"route kind":        SessionStateKey(attributed(sessionCtx("session-1"), "vk-1", ""), SessionStateKindRoute, "openai", "gpt-4o"),
	}
	for name, v := range variants {
		if v == base {
			t.Fatalf("%s collides with the base key", name)
		}
	}

	// A request nothing governs, one whose grant has no identity, and one whose identity names
	// nothing all scope to the deployment and share a key.
	deployment := SessionStateKey(sessionCtx("session-1"), SessionStateKindKey, "openai", "gpt-4o")
	unsettled := sessionCtx("session-1")
	unsettled.SetGrant(&stubGrant{})
	if got := SessionStateKey(unsettled, SessionStateKindKey, "openai", "gpt-4o"); got != deployment {
		t.Fatal("a grant without identity should scope to the deployment")
	}
	if got := SessionStateKey(attributed(sessionCtx("session-1"), "", ""), SessionStateKindKey, "openai", "gpt-4o"); got != deployment {
		t.Fatal("an identity naming no key and no user should scope to the deployment")
	}
	if deployment == base {
		t.Fatal("deployment scope collides with a virtual key scope")
	}
	if SessionStateKey(nil, SessionStateKindKey, "openai", "gpt-4o") == "" {
		t.Fatal("nil context should still produce a key")
	}
}

func TestSessionTTLFromContext(t *testing.T) {
	if got := sessionTTLFromContext(nil); got != schemas.DefaultSessionStickyTTL {
		t.Fatalf("nil context TTL = %v, want the default", got)
	}
	if got := sessionTTLFromContext(sessionCtx("s")); got != schemas.DefaultSessionStickyTTL {
		t.Fatalf("unset TTL = %v, want the default", got)
	}
	ctx := sessionCtx("s")
	ctx.SetValue(schemas.BifrostContextKeySessionTTL, 3*time.Minute)
	if got := sessionTTLFromContext(ctx); got != 3*time.Minute {
		t.Fatalf("set TTL = %v, want 3m", got)
	}
	ctx.SetValue(schemas.BifrostContextKeySessionTTL, time.Duration(0))
	if got := sessionTTLFromContext(ctx); got != schemas.DefaultSessionStickyTTL {
		t.Fatalf("zero TTL = %v, want the default", got)
	}
}

func TestIsSessionAffinityActive(t *testing.T) {
	if schemas.IsSessionAffinityActive(nil) {
		t.Fatal("nil context takes part")
	}
	if schemas.IsSessionAffinityActive(sessionCtx("")) {
		t.Fatal("a request without a session takes part")
	}
	if !schemas.IsSessionAffinityActive(sessionCtx("s")) {
		t.Fatal("a request with a session and an unset switch does not take part")
	}
	ctx := sessionCtx("s")
	ctx.SetValue(schemas.BifrostContextKeySessionAffinity, false)
	if schemas.IsSessionAffinityActive(ctx) {
		t.Fatal("a request that switched affinity off takes part")
	}
	ctx.SetValue(schemas.BifrostContextKeySessionAffinity, true)
	if !schemas.IsSessionAffinityActive(ctx) {
		t.Fatal("a request that switched affinity on does not take part")
	}
}

func TestSessionAffinityResolveKeyReadsReplicatedBindings(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"plain string", "key-b", "key-b"},
		{"json bytes", []byte(`"key-c"`), "key-c"},
		{"raw bytes", []byte("key-b"), "key-b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kv := newMockKVStore()
			ctx := sessionCtx("session-1")
			_ = kv.SetWithTTL(SessionStateKey(ctx, SessionStateKindKey, "openai", "gpt-4o"), tc.value, time.Minute)
			key, ok := testAffinity(kv).ResolveKey(ctx, schemas.OpenAI, "gpt-4o", sessionTestPool)
			if !ok || key.ID != tc.want {
				t.Fatalf("got %q ok=%v, want the stored binding %q", key.ID, ok, tc.want)
			}
		})
	}

	// An empty stored value is no binding.
	kv := newMockKVStore()
	ctx := sessionCtx("session-1")
	_ = kv.SetWithTTL(SessionStateKey(ctx, SessionStateKindKey, "openai", "gpt-4o"), "", time.Minute)
	if _, ok := testAffinity(kv).ResolveKey(ctx, schemas.OpenAI, "gpt-4o", sessionTestPool); ok {
		t.Fatal("empty binding produced a key")
	}
}

func TestSessionAffinityBindsOnOutcomeThenReuses(t *testing.T) {
	kv := newMockKVStore()
	a := testAffinity(kv)
	requested := routeOf("", "gpt-4o")
	served := routeOf(schemas.OpenAI, "gpt-4o")

	// Nothing bound yet: the pool builder gets no answer and nothing is written.
	ctx := sessionCtx("session-1")
	ctx.SetValue(schemas.BifrostContextKeySessionTTL, 5*time.Minute)
	if _, ok := a.ResolveKey(ctx, schemas.OpenAI, "gpt-4o", sessionTestPool); ok {
		t.Fatal("an unbound session got a key before anything served it")
	}
	if len(kv.data) != 0 {
		t.Fatalf("resolving wrote state: %v", kv.data)
	}

	// Served by key-b: the route and the key are bound with the request's TTL.
	a.Observe(ctx, requested, servedBy(served, "key-b", false))
	routeKey := SessionStateKey(ctx, SessionStateKindRoute, "", "gpt-4o")
	keyKey := SessionStateKey(ctx, SessionStateKindKey, "openai", "gpt-4o")
	if entry := kv.data[routeKey]; entry.value != "openai/gpt-4o" || entry.ttl != 5*time.Minute {
		t.Fatalf("route binding after first request: %+v", entry)
	}
	if entry := kv.data[keyKey]; entry.value != "key-b" || entry.ttl != 5*time.Minute {
		t.Fatalf("key binding after first request: %+v", entry)
	}

	// The next request reuses key-b and the trail says so.
	next := sessionCtx("session-1")
	next.SetValue(schemas.BifrostContextKeySessionTTL, 7*time.Minute)
	key, ok := a.ResolveKey(next, schemas.OpenAI, "gpt-4o", sessionTestPool)
	if !ok || key.ID != "key-b" {
		t.Fatalf("second request: got %q ok=%v, want key-b", key.ID, ok)
	}
	if !slices.Contains(enginesUsed(next), schemas.RoutingEngineSessionAffinity) || !trailMentions(next, "reused key Key B") {
		t.Fatalf("second request did not record the reuse: engines=%v", enginesUsed(next))
	}

	// Served again by the key it followed: the binding is refreshed, not replaced.
	a.Observe(next, requested, servedBy(served, "key-b", false))
	if entry := kv.data[keyKey]; entry.value != "key-b" || entry.ttl != 7*time.Minute {
		t.Fatalf("reuse did not refresh the key binding: %+v", entry)
	}

	// A fallback provider that served binds its own key, and does not take the route from a
	// request that followed no route binding.
	a.Observe(next, requested, servedBy(routeOf(schemas.Azure, "gpt-4o"), "az-1", true))
	if entry := kv.data[SessionStateKey(next, SessionStateKindKey, "azure", "gpt-4o")]; entry.value != "az-1" {
		t.Fatalf("fallback key binding: %+v", entry)
	}
	if entry := kv.data[routeKey]; entry.value != "openai/gpt-4o" {
		t.Fatalf("route binding overwritten by a request that followed none: %+v", entry)
	}
}

// A fallback picks its key without consulting the session, so a key binding its provider already
// held for the session is older than the key that just served there, and gives way to it.
func TestSessionAffinityFallbackReplacesAnOlderKeyBindingOnItsProvider(t *testing.T) {
	kv := newMockKVStore()
	a := testAffinity(kv)
	requested := routeOf("", "gpt-4o")
	openai, azure := routeOf(schemas.OpenAI, "gpt-4o"), routeOf(schemas.Azure, "gpt-4o")

	// Earlier turns: openai served with key-a, then the session moved to azure on key-b.
	seed := sessionCtx("session-1")
	openaiKey := SessionStateKey(seed, SessionStateKindKey, "openai", "gpt-4o")
	azureKey := SessionStateKey(seed, SessionStateKindKey, "azure", "gpt-4o")
	routeKey := SessionStateKey(seed, SessionStateKindRoute, "", "gpt-4o")
	_ = kv.SetWithTTL(openaiKey, "key-a", time.Hour)
	_ = kv.SetWithTTL(azureKey, "key-b", time.Hour)
	_ = kv.SetWithTTL(routeKey, "azure/gpt-4o", time.Hour)

	// This turn follows azure and its key, azure fails on that key, and the openai fallback serves
	// on key-c, picked freely because a fallback attempt gets no key from the session.
	ctx := sessionCtx("session-1")
	if got := a.ResolveRoute(ctx, requested, []schemas.Route{openai, azure}); got[0] != azure {
		t.Fatalf("session did not follow azure: %v", got)
	}
	if key, ok := a.ResolveKey(ctx, schemas.Azure, "gpt-4o", sessionTestPool); !ok || key.ID != "key-b" {
		t.Fatalf("primary attempt: got %q ok=%v, want key-b", key.ID, ok)
	}
	ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, 1)
	if _, ok := a.ResolveKey(ctx, schemas.OpenAI, "gpt-4o", sessionTestPool); ok {
		t.Fatal("fallback attempt got a key from the session")
	}
	a.Observe(ctx, requested, servedBy(openai, "key-c", true))

	if entry := kv.data[routeKey]; entry.value != "openai/gpt-4o" {
		t.Fatalf("route binding after the fallback served: %+v, want openai/gpt-4o", entry)
	}
	if entry := kv.data[openaiKey]; entry.value != "key-c" {
		t.Fatalf("openai key binding after its fallback served on key-c: %+v, want key-c", entry)
	}
	// Azure failed on the key the session followed, so that binding is dropped rather than kept
	// for the rest of its TTL: the next time azure serves this session, a key is picked afresh.
	if entry, bound := kv.data[azureKey]; bound {
		t.Fatalf("azure key binding survived azure failing on it: %+v", entry)
	}
}

func TestSessionAffinityRebindsWhenBoundKeyLeavesThePool(t *testing.T) {
	kv := newMockKVStore()
	ctx := sessionCtx("session-1")
	stateKey := SessionStateKey(ctx, SessionStateKindKey, "openai", "gpt-4o")
	_ = kv.SetWithTTL(stateKey, "key-gone", time.Minute)

	if _, ok := testAffinity(kv).ResolveKey(ctx, schemas.OpenAI, "gpt-4o", sessionTestPool); ok {
		t.Fatal("a binding to a key outside the pool produced a key")
	}
	if _, present := kv.data[stateKey]; present {
		t.Fatal("stale binding was not deleted")
	}
	if !trailMentions(ctx, "no longer eligible") {
		t.Fatal("stale binding was not explained in the trail")
	}
}

func TestSessionAffinityResolveRoute(t *testing.T) {
	chain := []schemas.Route{routeOf(schemas.Groq, "openai/gpt-4o"), routeOf(schemas.OpenAI, "gpt-4o"), routeOf(schemas.Azure, "gpt-4o")}
	requested := routeOf("", "gpt-4o")
	bind := func(kv *mockKVStore, ctx *schemas.BifrostContext, value string) {
		_ = kv.SetWithTTL(SessionStateKey(ctx, SessionStateKindRoute, "", "gpt-4o"), value, time.Minute)
	}

	t.Run("no binding leaves the chain", func(t *testing.T) {
		kv := newMockKVStore()
		ctx := sessionCtx("session-1")
		if got := testAffinity(kv).ResolveRoute(ctx, requested, chain); !slices.Equal(got, chain) {
			t.Fatalf("got %v, want the chain unchanged", got)
		}
		if _, recorded := ctx.Value(sessionAffinityResolvedKey).(sessionResolution); recorded {
			t.Fatal("nothing was followed, nothing should be recorded")
		}
	})

	t.Run("bound provider moves to the front and the rest keep their order", func(t *testing.T) {
		kv := newMockKVStore()
		ctx := sessionCtx("session-1")
		bind(kv, ctx, "azure/gpt-4o")
		got := testAffinity(kv).ResolveRoute(ctx, requested, chain)
		want := []schemas.Route{chain[2], chain[0], chain[1]}
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		if !slices.Contains(enginesUsed(ctx), schemas.RoutingEngineSessionAffinity) || !trailMentions(ctx, "Session stays on azure") {
			t.Fatalf("decision not recorded: engines=%v", enginesUsed(ctx))
		}
		if res, _ := ctx.Value(sessionAffinityResolvedKey).(sessionResolution); res.route != "azure/gpt-4o" {
			t.Fatalf("followed route not recorded: %+v", res)
		}
	})

	// A binding names a route, provider and model together. A chain that offers the provider on
	// another model does not offer that route: a session served on azure/gpt-4o-old must not be
	// moved onto azure/gpt-4o just because both are azure, the routing decision stands instead.
	t.Run("a bound route whose model the chain does not offer is dropped, provider or not", func(t *testing.T) {
		kv := newMockKVStore()
		a := testAffinity(kv)
		ctx := sessionCtx("session-1")
		bind(kv, ctx, "azure/gpt-4o-old")
		if got := a.ResolveRoute(ctx, requested, chain); !slices.Equal(got, chain) {
			t.Fatalf("got %v, want the chain unchanged", got)
		}
		if _, bound := kv.data[SessionStateKey(ctx, SessionStateKindRoute, "", "gpt-4o")]; bound {
			t.Fatal("a binding to a route the chain does not offer was kept")
		}
		if !trailMentions(ctx, "cannot use") {
			t.Fatal("the dropped binding was not explained in the trail")
		}
		if _, recorded := ctx.Value(sessionAffinityResolvedKey).(sessionResolution); recorded {
			t.Fatal("a binding outside the chain was recorded as followed")
		}
	})

	t.Run("bound provider already first is left alone but still reported", func(t *testing.T) {
		kv := newMockKVStore()
		ctx := sessionCtx("session-1")
		bind(kv, ctx, "groq/openai/gpt-4o")
		if got := testAffinity(kv).ResolveRoute(ctx, requested, chain); !slices.Equal(got, chain) {
			t.Fatalf("got %v, want the chain unchanged", got)
		}
		// Agreeing with routing is a decision the session made, and a trail that omitted it could
		// not be told apart from one where the session was never consulted.
		if !slices.Contains(enginesUsed(ctx), schemas.RoutingEngineSessionAffinity) || !trailMentions(ctx, "which routing also proposed") {
			t.Fatalf("agreement not recorded: engines=%v", enginesUsed(ctx))
		}
		if res, _ := ctx.Value(sessionAffinityResolvedKey).(sessionResolution); res.route != "groq/openai/gpt-4o" {
			t.Fatalf("followed route not recorded: %+v", res)
		}
	})

	t.Run("bound provider the request cannot use leaves the chain and is dropped", func(t *testing.T) {
		kv := newMockKVStore()
		a := testAffinity(kv)
		ctx := sessionCtx("session-1")
		bind(kv, ctx, "anthropic/claude-sonnet")
		if got := a.ResolveRoute(ctx, requested, chain); !slices.Equal(got, chain) {
			t.Fatalf("got %v, want the chain unchanged", got)
		}
		if !trailMentions(ctx, "cannot use") {
			t.Fatal("stale binding was not explained in the trail")
		}
		// Refusing a stale binding is a decision, so it is listed among the engines used: a trail
		// entry attributed to an engine the request does not record cannot be filtered for.
		if !slices.Contains(enginesUsed(ctx), schemas.RoutingEngineSessionAffinity) {
			t.Fatalf("refusing a stale binding was not counted as a session decision: engines=%v", enginesUsed(ctx))
		}
		if _, recorded := ctx.Value(sessionAffinityResolvedKey).(sessionResolution); recorded {
			t.Fatal("a binding outside the chain was recorded as followed")
		}
		routeKey := SessionStateKey(ctx, SessionStateKindRoute, "", "gpt-4o")
		if _, present := kv.data[routeKey]; present {
			t.Fatal("a binding outside the chain was kept")
		}
		// The request that then serves binds the session afresh, so it converges there.
		a.Observe(ctx, requested, servedBy(routeOf(schemas.OpenAI, "gpt-4o"), "oa-1", false))
		if entry := kv.data[routeKey]; entry.value != "openai/gpt-4o" {
			t.Fatalf("session did not rebind to what served: %+v", entry)
		}
	})

	t.Run("a binding that cannot be read is dropped so the session can rebind", func(t *testing.T) {
		for _, unreadable := range []struct{ name, value string }{
			{"no separator between provider and model", "azure"},
			{"no provider", "/gpt-4o"},
		} {
			t.Run(unreadable.name, func(t *testing.T) {
				kv := newMockKVStore()
				a := testAffinity(kv)
				ctx := sessionCtx("session-1")
				bind(kv, ctx, unreadable.value)
				if got := a.ResolveRoute(ctx, requested, chain); !slices.Equal(got, chain) {
					t.Fatalf("got %v, want the chain unchanged", got)
				}
				if _, recorded := ctx.Value(sessionAffinityResolvedKey).(sessionResolution); recorded {
					t.Fatal("a binding that names no route was recorded as followed")
				}
				// Nothing was decided and nothing is said, at either level: a binding that names no
				// route is dropped as corrupt, not refused on the request's behalf.
				if len(ctx.GetRoutingEngineLogs()) != 0 || slices.Contains(enginesUsed(ctx), schemas.RoutingEngineSessionAffinity) {
					t.Fatalf("an unreadable binding was reported as a decision: engines=%v trail=%v", enginesUsed(ctx), ctx.GetRoutingEngineLogs())
				}
				routeKey := SessionStateKey(ctx, SessionStateKindRoute, "", "gpt-4o")
				if _, present := kv.data[routeKey]; present {
					t.Fatal("a binding that names no route was kept")
				}
				// Kept, it would refuse the first-writer bind below and strand the session on
				// routing's pick until the binding expired.
				a.Observe(ctx, requested, servedBy(routeOf(schemas.OpenAI, "gpt-4o"), "oa-1", false))
				if entry := kv.data[routeKey]; entry.value != "openai/gpt-4o" {
					t.Fatalf("session did not rebind to what served: %+v", entry)
				}
			})
		}
	})

	t.Run("empty chain, nil policy and no store leave the chain", func(t *testing.T) {
		kv := newMockKVStore()
		bind(kv, sessionCtx("session-1"), "azure/gpt-4o")
		if got := testAffinity(kv).ResolveRoute(sessionCtx("session-1"), requested, nil); got != nil {
			t.Fatalf("empty chain: got %v", got)
		}
		var none *sessionAffinity
		if got := none.ResolveRoute(sessionCtx("session-1"), requested, chain); !slices.Equal(got, chain) {
			t.Fatalf("nil policy: got %v", got)
		}
		if got := testAffinity(nil).ResolveRoute(sessionCtx("session-1"), requested, chain); !slices.Equal(got, chain) {
			t.Fatalf("no store: got %v", got)
		}
	})
}

func TestSessionAffinityObserveRoute(t *testing.T) {
	chain := []schemas.Route{routeOf(schemas.OpenAI, "gpt-4o"), routeOf(schemas.Azure, "gpt-4o")}
	requested := routeOf("", "gpt-4o")
	routeKeyOf := func(ctx *schemas.BifrostContext) string {
		return SessionStateKey(ctx, SessionStateKindRoute, "", "gpt-4o")
	}

	t.Run("a followed route that served is refreshed", func(t *testing.T) {
		kv := newMockKVStore()
		a := testAffinity(kv)
		ctx := sessionCtx("session-1")
		_ = kv.SetWithTTL(routeKeyOf(ctx), "azure/gpt-4o", time.Minute)
		ctx.SetValue(schemas.BifrostContextKeySessionTTL, 9*time.Minute)
		a.ResolveRoute(ctx, requested, chain)
		a.Observe(ctx, requested, servedBy(routeOf(schemas.Azure, "gpt-4o"), "az-1", false))
		if entry := kv.data[routeKeyOf(ctx)]; entry.value != "azure/gpt-4o" || entry.ttl != 9*time.Minute {
			t.Fatalf("route binding after reuse: %+v", entry)
		}
	})

	t.Run("a fallback that served moves the route", func(t *testing.T) {
		kv := newMockKVStore()
		a := testAffinity(kv)
		ctx := sessionCtx("session-1")
		_ = kv.SetWithTTL(routeKeyOf(ctx), "azure/gpt-4o", time.Minute)
		a.ResolveRoute(ctx, requested, chain)
		a.Observe(ctx, requested, servedBy(routeOf(schemas.OpenAI, "gpt-4o"), "oa-1", true))
		if entry := kv.data[routeKeyOf(ctx)]; entry.value != "openai/gpt-4o" {
			t.Fatalf("route binding after a fallback served: %+v", entry)
		}
	})

	t.Run("a request that followed no route binding does not overwrite one", func(t *testing.T) {
		kv := newMockKVStore()
		a := testAffinity(kv)
		ctx := sessionCtx("session-1")
		_ = kv.SetWithTTL(routeKeyOf(ctx), "azure/gpt-4o", time.Minute)
		a.Observe(ctx, requested, servedBy(routeOf(schemas.OpenAI, "gpt-4o"), "oa-1", false))
		if entry := kv.data[routeKeyOf(ctx)]; entry.value != "azure/gpt-4o" {
			t.Fatalf("route binding overwritten: %+v", entry)
		}
	})

	t.Run("a failure that followed nothing writes nothing", func(t *testing.T) {
		kv := newMockKVStore()
		ctx := sessionCtx("session-1")
		testAffinity(kv).Observe(ctx, requested, schemas.RouteOutcome{Err: &schemas.BifrostError{}})
		if len(kv.data) != 0 {
			t.Fatalf("a failure wrote state: %v", kv.data)
		}
	})

	t.Run("a served route without a key binds only the route", func(t *testing.T) {
		kv := newMockKVStore()
		ctx := sessionCtx("session-1")
		testAffinity(kv).Observe(ctx, requested, servedBy(routeOf(schemas.OpenAI, "gpt-4o"), "", false))
		if len(kv.data) != 1 {
			t.Fatalf("a served route without a key should bind only the route: %v", kv.data)
		}
	})
}

// A binding the request followed into a failure is dropped, at both levels, so the next request
// is routed and keyed afresh instead of returning to what just failed for up to the TTL. A binding
// the request did not follow says nothing about the failure and is left alone.
func TestSessionAffinityFailureDropsWhatTheRequestFollowed(t *testing.T) {
	requested := routeOf("", "gpt-4o")
	chain := []schemas.Route{routeOf(schemas.OpenAI, "gpt-4o"), routeOf(schemas.Azure, "gpt-4o")}
	failed := schemas.RouteOutcome{Err: &schemas.BifrostError{}}
	keys := func(ctx *schemas.BifrostContext) (route, openaiKey, azureKey string) {
		return SessionStateKey(ctx, SessionStateKindRoute, "", "gpt-4o"),
			SessionStateKey(ctx, SessionStateKindKey, "openai", "gpt-4o"),
			SessionStateKey(ctx, SessionStateKindKey, "azure", "gpt-4o")
	}

	t.Run("a failed request drops the route and the key it followed", func(t *testing.T) {
		kv := newMockKVStore()
		a := testAffinity(kv)
		ctx := sessionCtx("session-1")
		route, openaiKey, azureKey := keys(ctx)
		_ = kv.SetWithTTL(route, "openai/gpt-4o", time.Minute)
		_ = kv.SetWithTTL(openaiKey, "key-b", time.Minute)
		_ = kv.SetWithTTL(azureKey, "az-1", time.Minute)
		a.ResolveRoute(ctx, requested, chain)
		if key, ok := a.ResolveKey(ctx, schemas.OpenAI, "gpt-4o", sessionTestPool); !ok || key.ID != "key-b" {
			t.Fatalf("setup: the session did not follow key-b: %q ok=%v", key.ID, ok)
		}
		a.Observe(ctx, requested, failed)
		if _, bound := kv.data[route]; bound {
			t.Fatalf("the route binding survived the failure of the provider it named: %+v", kv.data[route])
		}
		if _, bound := kv.data[openaiKey]; bound {
			t.Fatalf("the key binding survived the failure of the key it named: %+v", kv.data[openaiKey])
		}
		if entry := kv.data[azureKey]; entry.value != "az-1" {
			t.Fatalf("a key binding the request never followed was touched: %+v", entry)
		}
		if !trailMentions(ctx, "failed") {
			t.Fatalf("dropping the bindings left no trace in the trail: %v", ctx.GetRoutingEngineLogs())
		}
	})

	// A caller that gives up on a request says nothing about the provider or key it followed, so
	// a cancellation keeps both bindings. A deadline is the provider's failure to answer and drops.
	t.Run("a request the caller cancelled keeps what it followed, a timeout does not", func(t *testing.T) {
		for _, tc := range []struct {
			errType string
			kept    bool
		}{{schemas.RequestCancelled, true}, {schemas.RequestTimedOut, false}} {
			kv := newMockKVStore()
			a := testAffinity(kv)
			ctx := sessionCtx("session-1")
			route, openaiKey, _ := keys(ctx)
			_ = kv.SetWithTTL(route, "openai/gpt-4o", time.Minute)
			_ = kv.SetWithTTL(openaiKey, "key-b", time.Minute)
			a.ResolveRoute(ctx, requested, chain)
			a.ResolveKey(ctx, schemas.OpenAI, "gpt-4o", sessionTestPool)
			errType := tc.errType
			a.Observe(ctx, requested, schemas.RouteOutcome{Err: &schemas.BifrostError{Error: &schemas.ErrorField{Type: &errType}}})
			_, routeBound := kv.data[route]
			_, keyBound := kv.data[openaiKey]
			if routeBound != tc.kept || keyBound != tc.kept {
				t.Fatalf("%s: route bound=%v key bound=%v, want both %v", tc.errType, routeBound, keyBound, tc.kept)
			}
		}
	})

	t.Run("a failed request that followed nothing leaves bindings alone", func(t *testing.T) {
		kv := newMockKVStore()
		a := testAffinity(kv)
		ctx := sessionCtx("session-1")
		route, openaiKey, _ := keys(ctx)
		_ = kv.SetWithTTL(route, "openai/gpt-4o", time.Minute)
		_ = kv.SetWithTTL(openaiKey, "key-b", time.Minute)
		// The caller named its provider, so the route binding was never consulted, and the key
		// pool had one key, so neither was the key binding.
		named := routeOf(schemas.OpenAI, "gpt-4o")
		a.ResolveRoute(ctx, named, chain[:1])
		a.ResolveKey(ctx, schemas.OpenAI, "gpt-4o", sessionTestPool[:1])
		a.Observe(ctx, named, failed)
		if entry := kv.data[route]; entry.value != "openai/gpt-4o" {
			t.Fatalf("route binding touched by a request that never followed it: %+v", entry)
		}
		if entry := kv.data[openaiKey]; entry.value != "key-b" {
			t.Fatalf("key binding touched by a request that never followed it: %+v", entry)
		}
	})

	t.Run("a fallback that served drops the key the primary followed and binds its own", func(t *testing.T) {
		kv := newMockKVStore()
		a := testAffinity(kv)
		ctx := sessionCtx("session-1")
		route, openaiKey, azureKey := keys(ctx)
		_ = kv.SetWithTTL(route, "openai/gpt-4o", time.Minute)
		_ = kv.SetWithTTL(openaiKey, "key-b", time.Minute)
		a.ResolveRoute(ctx, requested, chain)
		a.ResolveKey(ctx, schemas.OpenAI, "gpt-4o", sessionTestPool)
		a.Observe(ctx, requested, servedBy(routeOf(schemas.Azure, "gpt-4o"), "az-1", true))
		if _, bound := kv.data[openaiKey]; bound {
			t.Fatalf("the key the primary followed into its failure survived: %+v", kv.data[openaiKey])
		}
		if entry := kv.data[azureKey]; entry.value != "az-1" {
			t.Fatalf("the fallback's key was not bound: %+v", entry)
		}
		if entry := kv.data[route]; entry.value != "azure/gpt-4o" {
			t.Fatalf("the route did not move to the fallback that served: %+v", entry)
		}
	})

	t.Run("a failed request that followed only the route keeps a key binding it never used", func(t *testing.T) {
		kv := newMockKVStore()
		a := testAffinity(kv)
		ctx := sessionCtx("session-1")
		route, openaiKey, _ := keys(ctx)
		_ = kv.SetWithTTL(route, "openai/gpt-4o", time.Minute)
		_ = kv.SetWithTTL(openaiKey, "key-b", time.Minute)
		// The route was followed; the key was decided by a pin, so ResolveKey was never asked.
		a.ResolveRoute(ctx, requested, chain)
		a.Observe(ctx, requested, failed)
		if _, bound := kv.data[route]; bound {
			t.Fatalf("the route binding survived the failure: %+v", kv.data[route])
		}
		if entry := kv.data[openaiKey]; entry.value != "key-b" {
			t.Fatalf("a key binding a pinned request never consulted was touched: %+v", entry)
		}
	})
}

func TestSessionAffinityResolveKeyDeclines(t *testing.T) {
	kv := newMockKVStore()
	a := testAffinity(kv)
	bound := sessionCtx("session-1")
	_ = kv.SetWithTTL(SessionStateKey(bound, SessionStateKindKey, "openai", "gpt-4o"), "key-a", time.Minute)

	if _, ok := a.ResolveKey(nil, schemas.OpenAI, "gpt-4o", sessionTestPool); ok {
		t.Fatal("nil context got a key")
	}
	if _, ok := a.ResolveKey(sessionCtx("session-1"), schemas.OpenAI, "gpt-4o", sessionTestPool[:1]); ok {
		t.Fatal("single-key pool got a key")
	}
	fallback := sessionCtx("session-1")
	fallback.SetValue(schemas.BifrostContextKeyFallbackIndex, 1)
	if _, ok := a.ResolveKey(fallback, schemas.OpenAI, "gpt-4o", sessionTestPool); ok {
		t.Fatal("fallback attempt got a key")
	}
	if key, ok := a.ResolveKey(sessionCtx("session-1"), schemas.OpenAI, "gpt-4o", sessionTestPool); !ok || key.ID != "key-a" {
		t.Fatalf("bound session: got %q ok=%v, want key-a", key.ID, ok)
	}
	var none *sessionAffinity
	if _, ok := none.ResolveKey(sessionCtx("session-1"), schemas.OpenAI, "gpt-4o", sessionTestPool); ok {
		t.Fatal("nil policy got a key")
	}
	if _, ok := testAffinity(nil).ResolveKey(sessionCtx("session-1"), schemas.OpenAI, "gpt-4o", sessionTestPool); ok {
		t.Fatal("policy without a store got a key")
	}
}

func TestSessionAffinityConcurrentFirstRequestsConverge(t *testing.T) {
	kv := newMockKVStore()
	a := testAffinity(kv)
	requested := routeOf("", "gpt-4o")
	served := routeOf(schemas.OpenAI, "gpt-4o")

	// Eight first requests of one session resolve before any of them is served: none finds a
	// binding, and each is then served by whatever key selection gave it.
	contexts := make([]*schemas.BifrostContext, 8)
	for i := range contexts {
		contexts[i] = sessionCtx("session-1")
		if _, ok := a.ResolveKey(contexts[i], schemas.OpenAI, "gpt-4o", sessionTestPool); ok {
			t.Fatal("a first request found a binding")
		}
	}
	var wg sync.WaitGroup
	for i, ctx := range contexts {
		wg.Add(1)
		go func(i int, ctx *schemas.BifrostContext) {
			defer wg.Done()
			a.Observe(ctx, requested, servedBy(served, sessionTestPool[i%len(sessionTestPool)].ID, false))
		}(i, ctx)
	}
	wg.Wait()

	bound, _ := kv.data[SessionStateKey(sessionCtx("session-1"), SessionStateKindKey, "openai", "gpt-4o")].value.(string)
	if bound == "" {
		t.Fatal("no key binding after eight served requests")
	}
	key, ok := a.ResolveKey(sessionCtx("session-1"), schemas.OpenAI, "gpt-4o", sessionTestPool)
	if !ok || key.ID != bound {
		t.Fatalf("session resolves to %q ok=%v, want the first bound key %q", key.ID, ok, bound)
	}
}

func TestSessionAffinityScopesSessionsByCaller(t *testing.T) {
	kv := newMockKVStore()
	a := testAffinity(kv)
	requested := routeOf("", "gpt-4o")
	served := routeOf(schemas.OpenAI, "gpt-4o")
	a.Observe(attributed(sessionCtx("shared-session"), "vk-a", ""), requested, servedBy(served, "key-a", false))
	a.Observe(attributed(sessionCtx("shared-session"), "vk-b", ""), requested, servedBy(served, "key-b", false))
	if len(kv.data) != 4 {
		t.Fatalf("two callers sharing a session id should hold two route and two key bindings, got %d", len(kv.data))
	}
	if key, ok := a.ResolveKey(attributed(sessionCtx("shared-session"), "vk-b", ""), schemas.OpenAI, "gpt-4o", sessionTestPool); !ok || key.ID != "key-b" {
		t.Fatalf("caller b resolves to %q ok=%v, want its own key-b", key.ID, ok)
	}
}

func TestResolveSessionRouteAppliesTheAnswer(t *testing.T) {
	reverse := func(chain []schemas.Route) []schemas.Route {
		out := slices.Clone(chain)
		slices.Reverse(out)
		return out
	}
	newRequest := func(provider schemas.ModelProvider) *schemas.BifrostRequest {
		req := &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{Provider: provider, Model: "gpt-4o"},
		}
		req.SetFallbacks([]schemas.Fallback{{Provider: schemas.Azure, Model: "gpt-4o"}, {Provider: schemas.Groq, Model: "openai/gpt-4o"}})
		return req
	}
	setup := func(t *testing.T, answer func([]schemas.Route) []schemas.Route) (*Bifrost, *recordingAffinity) {
		t.Helper()
		fake := &recordingAffinity{routeAnswer: answer}
		client, err := Init(context.Background(), schemas.BifrostConfig{
			Account:         NewMockAccount(),
			Logger:          NewDefaultLogger(schemas.LogLevelError),
			SessionAffinity: fake,
		})
		if err != nil {
			t.Fatalf("Init: %v", err)
		}
		t.Cleanup(client.Shutdown)
		return client, fake
	}

	t.Run("the answer becomes primary and fallbacks", func(t *testing.T) {
		client, fake := setup(t, reverse)
		req := newRequest(schemas.OpenAI)
		client.resolveSessionRoute(sessionCtx("s"), routeOf("", "gpt-4o"), req)
		provider, model, fallbacks := req.GetRequestFields()
		if provider != schemas.Groq || model != "openai/gpt-4o" {
			t.Fatalf("primary = %s/%s, want groq/openai/gpt-4o", provider, model)
		}
		want := []schemas.Fallback{{Provider: schemas.Azure, Model: "gpt-4o"}, {Provider: schemas.OpenAI, Model: "gpt-4o"}}
		if !slices.Equal(fallbacks, want) {
			t.Fatalf("fallbacks = %v, want %v", fallbacks, want)
		}
		if fake.routeCalls != 1 {
			t.Fatalf("ResolveRoute called %d times, want 1", fake.routeCalls)
		}
	})

	t.Run("the same chain, or no answer, leaves the request untouched", func(t *testing.T) {
		for name, answer := range map[string]func([]schemas.Route) []schemas.Route{
			"same":  nil,
			"empty": func([]schemas.Route) []schemas.Route { return nil },
		} {
			client, _ := setup(t, answer)
			req := newRequest(schemas.OpenAI)
			before, beforeModel, beforeFallbacks := req.GetRequestFields()
			client.resolveSessionRoute(sessionCtx("s"), routeOf("", "gpt-4o"), req)
			provider, model, fallbacks := req.GetRequestFields()
			if provider != before || model != beforeModel || !slices.Equal(fallbacks, beforeFallbacks) {
				t.Fatalf("%s: request changed to %s/%s %v", name, provider, model, fallbacks)
			}
		}
	})

	t.Run("a request no hook could route is not offered", func(t *testing.T) {
		client, fake := setup(t, reverse)
		client.resolveSessionRoute(sessionCtx("s"), routeOf("", "gpt-4o"), newRequest(""))
		if fake.routeCalls != 0 {
			t.Fatal("ResolveRoute was asked about a request with no provider")
		}
	})

	t.Run("a request that takes no part is not offered", func(t *testing.T) {
		client, fake := setup(t, reverse)
		off := sessionCtx("s")
		off.SetValue(schemas.BifrostContextKeySessionAffinity, false)
		noSession := sessionCtx("")
		for _, ctx := range []*schemas.BifrostContext{noSession, off} {
			req := newRequest(schemas.OpenAI)
			client.resolveSessionRoute(ctx, routeOf("", "gpt-4o"), req)
			if provider, _, _ := req.GetRequestFields(); provider != schemas.OpenAI {
				t.Fatalf("request changed to %s", provider)
			}
		}
		if fake.routeCalls != 0 {
			t.Fatal("ResolveRoute was asked about a request that takes no part")
		}
		// A request that carries a session and switched affinity off says so, because its session
		// id is on the log record and silence would read as affinity having had nothing to add.
		if !trailMentions(off, "asked not to follow") {
			t.Fatal("a request that switched affinity off did not say so in the trail")
		}
		// One with no session at all has nothing to explain.
		if len(noSession.GetRoutingEngineLogs()) != 0 {
			t.Fatalf("a request with no session wrote a trail: %v", noSession.GetRoutingEngineLogs())
		}
	})
}

// TestResolveSessionRouteMovesKeyPinsWithTheirRoutes pins the bug where a routing rule's key
// pin stayed on the request when the session moved the primary to another provider: a Vertex
// key looked up among Anthropic's keys can only fail, and the attempt died with "no supported
// key found" before Anthropic was ever called. A pin belongs to the route it was decided for,
// so it follows that route wherever the session puts it in the chain, and a fallback the
// session promotes brings its own pin along.
func TestResolveSessionRouteMovesKeyPinsWithTheirRoutes(t *testing.T) {
	const vertexPin, anthropicPin = "vertex-key-id", "anthropic-key-id"
	reverse := func(chain []schemas.Route) []schemas.Route {
		out := slices.Clone(chain)
		slices.Reverse(out)
		return out
	}
	newRequest := func(fallbackPin string) *schemas.BifrostRequest {
		req := &schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.Vertex, Model: "claude-opus-5"},
		}
		req.SetFallbacks([]schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-opus-5", KeyID: fallbackPin}})
		return req
	}
	setup := func(t *testing.T, answer func([]schemas.Route) []schemas.Route) *Bifrost {
		t.Helper()
		client, err := Init(context.Background(), schemas.BifrostConfig{
			Account:         NewMockAccount(),
			Logger:          NewDefaultLogger(schemas.LogLevelError),
			SessionAffinity: &recordingAffinity{routeAnswer: answer},
		})
		if err != nil {
			t.Fatalf("Init: %v", err)
		}
		t.Cleanup(client.Shutdown)
		return client
	}
	// pinned is a context as RunPreRequestHooks leaves it once a rule pinned a key for the
	// primary: the routing pin recorded, and committed into the api-key-id key selection reads.
	pinned := func() *schemas.BifrostContext {
		ctx := sessionCtx("s")
		ctx.SetValue(schemas.BifrostContextKeyRoutingPinnedAPIKeyID, vertexPin)
		ctx.SetValue(schemas.BifrostContextKeyAPIKeyID, vertexPin)
		return ctx
	}
	// primaryPin is the pin the primary attempt will read, checking that the routing pin and
	// the committed api-key-id never disagree.
	primaryPin := func(t *testing.T, ctx *schemas.BifrostContext) string {
		t.Helper()
		routing, _ := ctx.Value(schemas.BifrostContextKeyRoutingPinnedAPIKeyID).(string)
		apiKey, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string)
		if routing != apiKey {
			t.Fatalf("routing pin %q and api-key-id %q disagree", routing, apiKey)
		}
		return apiKey
	}
	fallbacksOf := func(req *schemas.BifrostRequest) []schemas.Fallback {
		_, _, fallbacks := req.GetRequestFields()
		return fallbacks
	}

	t.Run("the primary's pin follows it when the session demotes it to a fallback", func(t *testing.T) {
		client := setup(t, reverse)
		ctx := pinned()
		req := newRequest("")
		client.resolveSessionRoute(ctx, routeOf("", "claude-opus-5"), req)
		if provider, _, _ := req.GetRequestFields(); provider != schemas.Anthropic {
			t.Fatalf("primary = %s, want anthropic", provider)
		}
		if pin := primaryPin(t, ctx); pin != "" {
			t.Fatalf("the moved primary still reads pin %q, which names a vertex key", pin)
		}
		want := []schemas.Fallback{{Provider: schemas.Vertex, Model: "claude-opus-5", KeyID: vertexPin}}
		if got := fallbacksOf(req); !slices.Equal(got, want) {
			t.Fatalf("fallbacks = %+v, want the demoted vertex route carrying its pin %+v", got, want)
		}
		if !trailMentions(ctx, "pinned") {
			t.Fatalf("moving the pin left no trace in the trail: %v", ctx.GetRoutingEngineLogs())
		}
	})

	t.Run("a promoted fallback's own pin becomes the primary pin", func(t *testing.T) {
		client := setup(t, reverse)
		ctx := pinned()
		req := newRequest(anthropicPin)
		client.resolveSessionRoute(ctx, routeOf("", "claude-opus-5"), req)
		if pin := primaryPin(t, ctx); pin != anthropicPin {
			t.Fatalf("primary pin = %q, want the promoted route's own %q", pin, anthropicPin)
		}
		want := []schemas.Fallback{{Provider: schemas.Vertex, Model: "claude-opus-5", KeyID: vertexPin}}
		if got := fallbacksOf(req); !slices.Equal(got, want) {
			t.Fatalf("fallbacks = %+v, want %+v", got, want)
		}
	})

	t.Run("an unpinned primary demoted behind a pinned fallback claims no pin of its own", func(t *testing.T) {
		client := setup(t, reverse)
		ctx := sessionCtx("s")
		req := newRequest(anthropicPin)
		client.resolveSessionRoute(ctx, routeOf("", "claude-opus-5"), req)
		if pin := primaryPin(t, ctx); pin != anthropicPin {
			t.Fatalf("primary pin = %q, want the promoted route's own %q", pin, anthropicPin)
		}
		want := []schemas.Fallback{{Provider: schemas.Vertex, Model: "claude-opus-5"}}
		if got := fallbacksOf(req); !slices.Equal(got, want) {
			t.Fatalf("fallbacks = %+v, want the demoted vertex route with no pin %+v", got, want)
		}
		if trailMentions(ctx, "applies if") {
			t.Fatalf("the trail claims a pin for vertex that the rule never set: %v", ctx.GetRoutingEngineLogs())
		}
	})

	t.Run("pins stay where they are when the session agrees with routing", func(t *testing.T) {
		for name, answer := range map[string]func([]schemas.Route) []schemas.Route{
			"same":  nil,
			"empty": func([]schemas.Route) []schemas.Route { return nil },
		} {
			client := setup(t, answer)
			ctx := pinned()
			req := newRequest(anthropicPin)
			client.resolveSessionRoute(ctx, routeOf("", "claude-opus-5"), req)
			if pin := primaryPin(t, ctx); pin != vertexPin {
				t.Fatalf("%s: primary pin = %q, want %q untouched", name, pin, vertexPin)
			}
			want := []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-opus-5", KeyID: anthropicPin}}
			if got := fallbacksOf(req); !slices.Equal(got, want) {
				t.Fatalf("%s: fallbacks = %+v, want %+v untouched", name, got, want)
			}
			if trailMentions(ctx, "pinned") {
				t.Fatalf("%s: the trail claims a pin moved: %v", name, ctx.GetRoutingEngineLogs())
			}
		}
	})

	t.Run("a caller's own pins are left alone", func(t *testing.T) {
		client := setup(t, reverse)
		ctx := sessionCtx("s")
		ctx.SetValue(schemas.BifrostContextKeyAPIKeyID, "caller-key")
		ctx.SetValue(schemas.BifrostContextKeyAPIKeyName, "caller-name")
		client.resolveSessionRoute(ctx, routeOf("", "claude-opus-5"), newRequest(""))
		if got, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyID).(string); got != "caller-key" {
			t.Fatalf("api-key-id = %q after a session move, want the caller's own pin kept", got)
		}
		if got, _ := ctx.Value(schemas.BifrostContextKeyAPIKeyName).(string); got != "caller-name" {
			t.Fatalf("api-key name = %q after a session move, want the caller's own pin kept", got)
		}
		if trailMentions(ctx, "pinned") {
			t.Fatalf("the trail mentions a routing pin the request never had: %v", ctx.GetRoutingEngineLogs())
		}
	})
}

func TestObserveSessionOutcomeReportsServedRouteAndKey(t *testing.T) {
	fake := &recordingAffinity{}
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:         NewMockAccount(),
		Logger:          NewDefaultLogger(schemas.LogLevelError),
		SessionAffinity: fake,
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(client.Shutdown)

	ctx := sessionCtx("s")
	ctx.SetValue(schemas.BifrostContextKeySelectedKeyID, "key-a")
	requested := routeOf("", "gpt-4o")
	client.observeSessionOutcome(ctx, requested, &schemas.Route{Provider: schemas.Azure, Model: "gpt-4o"}, true, nil)
	client.observeSessionOutcome(ctx, requested, nil, false, &schemas.BifrostError{})

	if len(fake.outcomes) != 2 || fake.requested[0] != requested {
		t.Fatalf("outcomes recorded: %+v for %v", fake.outcomes, fake.requested)
	}
	served := fake.outcomes[0]
	if served.Served == nil || served.Served.Provider != schemas.Azure || served.KeyID != "key-a" || !served.Fallback || served.Err != nil {
		t.Fatalf("served outcome: %+v", served)
	}
	failed := fake.outcomes[1]
	if failed.Served != nil || failed.KeyID != "" || failed.Err == nil {
		t.Fatalf("failed outcome: %+v", failed)
	}

	// A request that takes no part is not reported.
	off := sessionCtx("s")
	off.SetValue(schemas.BifrostContextKeySessionAffinity, false)
	client.observeSessionOutcome(off, requested, &schemas.Route{Provider: schemas.Azure, Model: "gpt-4o"}, false, nil)
	client.observeSessionOutcome(sessionCtx(""), requested, &schemas.Route{Provider: schemas.Azure, Model: "gpt-4o"}, false, nil)
	if len(fake.outcomes) != 2 {
		t.Fatalf("a request that takes no part was reported: %d outcomes", len(fake.outcomes))
	}
}

func TestKeyPoolAsksAffinityOnlyForRequestsThatTakePart(t *testing.T) {
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 5, 1000)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "key-a", Name: "Key A", Value: *schemas.NewSecretVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 1},
		{ID: "key-b", Name: "Key B", Value: *schemas.NewSecretVar("sk-b"), Models: schemas.WhiteList{"*"}, Weight: 1},
	})
	fake := &recordingAffinity{}
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:         account,
		Logger:          NewDefaultLogger(schemas.LogLevelError),
		KVStore:         newMockKVStore(),
		SessionAffinity: fake,
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(client.Shutdown)

	off := sessionCtx("s")
	off.SetValue(schemas.BifrostContextKeySessionAffinity, false)
	for name, ctx := range map[string]*schemas.BifrostContext{"no session": sessionCtx(""), "switched off": off} {
		keys, canRotate, err := client.selectKeyFromProviderForModelWithPool(ctx, schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
		if err != nil || !canRotate || len(keys) != 2 {
			t.Fatalf("%s: got %d keys canRotate=%v err=%v, want the full rotating pool", name, len(keys), canRotate, err)
		}
	}
	if fake.keyCalls != 0 {
		t.Fatalf("ResolveKey was asked %d times about requests that take no part", fake.keyCalls)
	}
	if _, _, err := client.selectKeyFromProviderForModelWithPool(sessionCtx("s"), schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI); err != nil {
		t.Fatalf("pool build: %v", err)
	}
	if fake.keyCalls != 1 {
		t.Fatalf("ResolveKey was asked %d times about a request that takes part, want 1", fake.keyCalls)
	}
}

// foreignKeyAffinity answers with a key core never offered it, as a misbehaving custom
// SessionAffinity could.
type foreignKeyAffinity struct{}

func (foreignKeyAffinity) ResolveRoute(_ *schemas.BifrostContext, _ schemas.Route, chain []schemas.Route) []schemas.Route {
	return chain
}

func (foreignKeyAffinity) ResolveKey(*schemas.BifrostContext, schemas.ModelProvider, string, []schemas.Key) (schemas.Key, bool) {
	return schemas.Key{ID: "key-foreign", Name: "Foreign"}, true
}

func (foreignKeyAffinity) Observe(*schemas.BifrostContext, schemas.Route, schemas.RouteOutcome) {}

func TestKeyPoolIgnoresAnAffinityKeyOutsideThePool(t *testing.T) {
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 5, 1000)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{
		{ID: "key-a", Name: "Key A", Value: *schemas.NewSecretVar("sk-a"), Models: schemas.WhiteList{"*"}, Weight: 1},
		{ID: "key-b", Name: "Key B", Value: *schemas.NewSecretVar("sk-b"), Models: schemas.WhiteList{"*"}, Weight: 1},
	})
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:         account,
		Logger:          NewDefaultLogger(schemas.LogLevelError),
		KVStore:         newMockKVStore(),
		SessionAffinity: foreignKeyAffinity{},
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(client.Shutdown)

	keys, canRotate, err := client.selectKeyFromProviderForModelWithPool(sessionCtx("session-1"), schemas.ChatCompletionRequest, schemas.OpenAI, "gpt-4", schemas.OpenAI)
	if err != nil {
		t.Fatalf("pool build: %v", err)
	}
	if !canRotate || len(keys) != 2 {
		t.Fatalf("got %d keys canRotate=%v, want the full rotating pool when the affinity names a key outside it", len(keys), canRotate)
	}
	for _, key := range keys {
		if key.ID == "key-foreign" {
			t.Fatal("a key the pool never held was handed to the request")
		}
	}
}

func TestSessionAffinityForgetsWhatAnEarlierRequestFollowed(t *testing.T) {
	kv := newMockKVStore()
	a := testAffinity(kv)
	requested := routeOf("", "gpt-4o")
	chain := []schemas.Route{routeOf(schemas.OpenAI, "gpt-4o"), routeOf(schemas.Azure, "gpt-4o")}
	ctx := sessionCtx("session-1")
	routeKey := SessionStateKey(ctx, SessionStateKindRoute, "", "gpt-4o")

	// An earlier request on this context followed a binding to azure.
	_ = kv.SetWithTTL(routeKey, "azure/gpt-4o", time.Minute)
	a.ResolveRoute(ctx, requested, chain)

	// The binding expires. The next request on the same context finds none, and while it is
	// in flight another request of the session binds openai first.
	_, _ = kv.Delete(routeKey)
	a.ResolveRoute(ctx, requested, chain)
	_ = kv.SetWithTTL(routeKey, "openai/gpt-4o", time.Minute)

	a.Observe(ctx, requested, servedBy(routeOf(schemas.Azure, "gpt-4o"), "az-1", false))
	if entry := kv.data[routeKey]; entry.value != "openai/gpt-4o" {
		t.Fatalf("a request that followed nothing overwrote another request's binding: %+v", entry)
	}
}

func TestSessionAffinityLeavesAnExplicitProviderAlone(t *testing.T) {
	kv := newMockKVStore()
	a := testAffinity(kv)
	// The caller named openai; routing kept it first and added azure as a fallback.
	requested := routeOf(schemas.OpenAI, "gpt-4o")
	chain := []schemas.Route{routeOf(schemas.OpenAI, "gpt-4o"), routeOf(schemas.Azure, "gpt-4o")}
	ctx := sessionCtx("session-1")
	routeKey := SessionStateKey(ctx, SessionStateKindRoute, "openai", "gpt-4o")

	// A fallback that served does not become the session's home when the caller named the
	// provider: no route binding is written, only the key the fallback used.
	a.Observe(ctx, requested, servedBy(routeOf(schemas.Azure, "gpt-4o"), "az-1", true))
	if _, present := kv.data[routeKey]; present {
		t.Fatal("a route binding was written for a request that named its provider")
	}
	if entry := kv.data[SessionStateKey(ctx, SessionStateKindKey, "azure", "gpt-4o")]; entry.value != "az-1" {
		t.Fatalf("the fallback's key binding was not written: %+v", entry)
	}

	// Even with a binding in the store, a request that named its provider is not reordered.
	_ = kv.SetWithTTL(routeKey, "azure/gpt-4o", time.Minute)
	if got := a.ResolveRoute(ctx, requested, chain); !slices.Equal(got, chain) {
		t.Fatalf("a request that named openai was sent to %v", got)
	}
	if slices.Contains(enginesUsed(ctx), schemas.RoutingEngineSessionAffinity) {
		t.Fatal("the session was recorded as deciding a route the caller named")
	}
}
