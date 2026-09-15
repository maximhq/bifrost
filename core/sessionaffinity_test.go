package bifrost

import (
	"context"
	"errors"
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

func firstKeySelector(_ *schemas.BifrostContext, keys []schemas.Key, _ schemas.ModelProvider, _ string) (schemas.Key, error) {
	return keys[0], nil
}

func TestSessionStateKeyScopesBySessionAndIdentity(t *testing.T) {
	base := sessionStateKey(attributed(sessionCtx("session-1"), "vk-1", ""), SessionStateKindKey, "openai", "gpt-4o")
	if !strings.HasPrefix(base, "session:v2:key:") {
		t.Fatalf("key %q lacks the kind prefix", base)
	}
	if strings.Contains(base, "session-1") || strings.Contains(base, "vk-1") || strings.Contains(base, "gpt-4o") {
		t.Fatalf("key %q leaks a hashed part", base)
	}
	if base != sessionStateKey(attributed(sessionCtx("session-1"), "vk-1", ""), SessionStateKindKey, "openai", "gpt-4o") {
		t.Fatal("key is not deterministic")
	}

	variants := map[string]string{
		"other virtual key": sessionStateKey(attributed(sessionCtx("session-1"), "vk-2", ""), SessionStateKindKey, "openai", "gpt-4o"),
		"same id as a user": sessionStateKey(attributed(sessionCtx("session-1"), "", "vk-1"), SessionStateKindKey, "openai", "gpt-4o"),
		"key and user":      sessionStateKey(attributed(sessionCtx("session-1"), "vk-1", "u-1"), SessionStateKindKey, "openai", "gpt-4o"),
		"other session":     sessionStateKey(attributed(sessionCtx("session-2"), "vk-1", ""), SessionStateKindKey, "openai", "gpt-4o"),
		"other provider":    sessionStateKey(attributed(sessionCtx("session-1"), "vk-1", ""), SessionStateKindKey, "azure", "gpt-4o"),
		"other model":       sessionStateKey(attributed(sessionCtx("session-1"), "vk-1", ""), SessionStateKindKey, "openai", "gpt-4o-mini"),
		"other kind":        sessionStateKey(attributed(sessionCtx("session-1"), "vk-1", ""), "other", "openai", "gpt-4o"),
	}
	for name, v := range variants {
		if v == base {
			t.Fatalf("%s collides with the base key", name)
		}
	}

	// A request nothing governs, one whose grant has no identity, and one whose identity names
	// nothing all scope to the deployment and share a key.
	deployment := sessionStateKey(sessionCtx("session-1"), SessionStateKindKey, "openai", "gpt-4o")
	unsettled := sessionCtx("session-1")
	unsettled.SetGrant(&stubGrant{})
	if got := sessionStateKey(unsettled, SessionStateKindKey, "openai", "gpt-4o"); got != deployment {
		t.Fatal("a grant without identity should scope to the deployment")
	}
	if got := sessionStateKey(attributed(sessionCtx("session-1"), "", ""), SessionStateKindKey, "openai", "gpt-4o"); got != deployment {
		t.Fatal("an identity naming no key and no user should scope to the deployment")
	}
	if deployment == base {
		t.Fatal("deployment scope collides with a virtual key scope")
	}
	if sessionStateKey(nil, SessionStateKindKey, "openai", "gpt-4o") == "" {
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

func TestKeyAffinityReadsReplicatedBindings(t *testing.T) {
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
			d := newKeyAffinity(kv, firstKeySelector, NewDefaultLogger(schemas.LogLevelError))
			ctx := sessionCtx("session-1")
			_ = kv.SetWithTTL(sessionStateKey(ctx, SessionStateKindKey, "openai", "gpt-4o"), tc.value, time.Minute)
			key, ok := d.ResolveKey(ctx, schemas.OpenAI, "gpt-4o", sessionTestPool)
			if !ok || key.ID != tc.want {
				t.Fatalf("got %q ok=%v, want the stored binding %q", key.ID, ok, tc.want)
			}
		})
	}

	// An empty stored value is no binding: the session binds afresh.
	kv := newMockKVStore()
	d := newKeyAffinity(kv, firstKeySelector, NewDefaultLogger(schemas.LogLevelError))
	ctx := sessionCtx("session-1")
	_ = kv.SetWithTTL(sessionStateKey(ctx, SessionStateKindKey, "openai", "gpt-4o"), "", time.Minute)
	if key, ok := d.ResolveKey(ctx, schemas.OpenAI, "gpt-4o", sessionTestPool); !ok || key.ID != "key-a" {
		t.Fatalf("empty binding: got %q ok=%v, want a fresh pick of key-a", key.ID, ok)
	}
}

func TestKeyAffinityBindsThenReuses(t *testing.T) {
	kv := newMockKVStore()
	calls := 0
	selector := func(ctx *schemas.BifrostContext, keys []schemas.Key, p schemas.ModelProvider, m string) (schemas.Key, error) {
		calls++
		return keys[1], nil
	}
	d := newKeyAffinity(kv, selector, NewDefaultLogger(schemas.LogLevelError))
	ctx := sessionCtx("session-1")
	ctx.SetValue(schemas.BifrostContextKeySessionTTL, 5*time.Minute)

	key, ok := d.ResolveKey(ctx, schemas.OpenAI, "gpt-4o", sessionTestPool)
	if !ok || key.ID != "key-b" {
		t.Fatalf("first request: got %q ok=%v, want the selector's pick key-b", key.ID, ok)
	}
	stateKey := sessionStateKey(ctx, SessionStateKindKey, "openai", "gpt-4o")
	if entry := kv.data[stateKey]; entry.value != "key-b" || entry.ttl != 5*time.Minute {
		t.Fatalf("binding after first request: %+v", entry)
	}

	// Later requests reuse the binding without asking the selector and refresh its TTL.
	ctx.SetValue(schemas.BifrostContextKeySessionTTL, 7*time.Minute)
	key, ok = d.ResolveKey(ctx, schemas.OpenAI, "gpt-4o", sessionTestPool)
	if !ok || key.ID != "key-b" || calls != 1 {
		t.Fatalf("second request: got %q ok=%v selector calls=%d", key.ID, ok, calls)
	}
	if entry := kv.data[stateKey]; entry.ttl != 7*time.Minute {
		t.Fatalf("reuse did not refresh the TTL: %v", entry.ttl)
	}

	// Another provider for the same session binds separately.
	if key, ok := d.ResolveKey(ctx, schemas.Azure, "gpt-4o", sessionTestPool); !ok || key.ID != "key-b" || calls != 2 {
		t.Fatalf("other provider: got %q ok=%v selector calls=%d", key.ID, ok, calls)
	}
}

func TestKeyAffinityRebindsWhenBoundKeyLeavesThePool(t *testing.T) {
	kv := newMockKVStore()
	d := newKeyAffinity(kv, firstKeySelector, NewDefaultLogger(schemas.LogLevelError))
	ctx := sessionCtx("session-1")
	stateKey := sessionStateKey(ctx, SessionStateKindKey, "openai", "gpt-4o")
	_ = kv.SetWithTTL(stateKey, "key-gone", time.Minute)

	key, ok := d.ResolveKey(ctx, schemas.OpenAI, "gpt-4o", sessionTestPool)
	if !ok || key.ID != "key-a" {
		t.Fatalf("got %q ok=%v, want a fresh pick of key-a", key.ID, ok)
	}
	if entry := kv.data[stateKey]; entry.value != "key-a" {
		t.Fatalf("stale binding was not replaced: %v", entry.value)
	}
}

func TestKeyAffinityDeclines(t *testing.T) {
	kv := newMockKVStore()
	d := newKeyAffinity(kv, firstKeySelector, NewDefaultLogger(schemas.LogLevelError))

	if _, ok := d.ResolveKey(sessionCtx(""), schemas.OpenAI, "gpt-4o", sessionTestPool); ok {
		t.Fatal("request without a session got a preference")
	}
	if _, ok := d.ResolveKey(nil, schemas.OpenAI, "gpt-4o", sessionTestPool); ok {
		t.Fatal("nil context got a preference")
	}
	if _, ok := d.ResolveKey(sessionCtx("session-1"), schemas.OpenAI, "gpt-4o", sessionTestPool[:1]); ok {
		t.Fatal("single-key pool got a preference")
	}
	fallback := sessionCtx("session-1")
	fallback.SetValue(schemas.BifrostContextKeyFallbackIndex, 1)
	if _, ok := d.ResolveKey(fallback, schemas.OpenAI, "gpt-4o", sessionTestPool); ok {
		t.Fatal("fallback attempt got a preference")
	}
	if len(kv.data) != 0 {
		t.Fatalf("declined requests wrote state: %v", kv.data)
	}

	failing := func(*schemas.BifrostContext, []schemas.Key, schemas.ModelProvider, string) (schemas.Key, error) {
		return schemas.Key{}, errors.New("no pick")
	}
	if _, ok := newKeyAffinity(kv, failing, NewDefaultLogger(schemas.LogLevelError)).ResolveKey(sessionCtx("session-1"), schemas.OpenAI, "gpt-4o", sessionTestPool); ok {
		t.Fatal("selector error still produced a preference")
	}

	var none *keyAffinity
	if _, ok := none.ResolveKey(sessionCtx("session-1"), schemas.OpenAI, "gpt-4o", sessionTestPool); ok {
		t.Fatal("nil policy got a preference")
	}
	if _, ok := newKeyAffinity(nil, firstKeySelector, NewDefaultLogger(schemas.LogLevelError)).ResolveKey(sessionCtx("session-1"), schemas.OpenAI, "gpt-4o", sessionTestPool); ok {
		t.Fatal("policy without a store got a preference")
	}
}

func TestKeyAffinityConcurrentFirstRequestsConverge(t *testing.T) {
	kv := newMockKVStore()
	// The selector answers differently per call, as a weighted pick would.
	var mu sync.Mutex
	next := 0
	selector := func(_ *schemas.BifrostContext, keys []schemas.Key, _ schemas.ModelProvider, _ string) (schemas.Key, error) {
		mu.Lock()
		defer mu.Unlock()
		k := keys[next%len(keys)]
		next++
		return k, nil
	}
	d := newKeyAffinity(kv, selector, NewDefaultLogger(schemas.LogLevelError))

	results := make([]string, 8)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key, ok := d.ResolveKey(sessionCtx("session-1"), schemas.OpenAI, "gpt-4o", sessionTestPool)
			if ok {
				results[i] = key.ID
			}
		}(i)
	}
	wg.Wait()
	bound, _ := kv.data[sessionStateKey(sessionCtx("session-1"), SessionStateKindKey, "openai", "gpt-4o")].value.(string)
	for i, r := range results {
		if r != bound {
			t.Fatalf("request %d used %q while the session is bound to %q (all: %v)", i, r, bound, results)
		}
	}
}

func TestKeyAffinityScopesSessionsByCaller(t *testing.T) {
	kv := newMockKVStore()
	d := newKeyAffinity(kv, firstKeySelector, NewDefaultLogger(schemas.LogLevelError))
	a := attributed(sessionCtx("shared-session"), "vk-a", "")
	b := attributed(sessionCtx("shared-session"), "vk-b", "")

	if _, ok := d.ResolveKey(a, schemas.OpenAI, "gpt-4o", sessionTestPool); !ok {
		t.Fatal("caller a got no key")
	}
	if _, ok := d.ResolveKey(b, schemas.OpenAI, "gpt-4o", sessionTestPool); !ok {
		t.Fatal("caller b got no key")
	}
	if len(kv.data) != 2 {
		t.Fatalf("two callers sharing a session id should hold two bindings, got %d", len(kv.data))
	}
}

// foreignKeyAffinity answers with a key core never offered it, as a misbehaving custom
// SessionAffinity could.
type foreignKeyAffinity struct{}

func (foreignKeyAffinity) ResolveKey(*schemas.BifrostContext, schemas.ModelProvider, string, []schemas.Key) (schemas.Key, bool) {
	return schemas.Key{ID: "key-foreign", Name: "Foreign"}, true
}

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
