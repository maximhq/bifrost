package semanticcache

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// TestResolveCacheKey pins that the cache key is the caller's own value. Tenant isolation is
// carried by the identity scope folded into the request hash, never by rewriting the key, so the
// stored cache_key stays the raw value and DELETE /api/cache/clear-by-key/{key} reaches every
// identity's entries under it. Pure-logic test - no vector store needed.
func TestResolveCacheKey(t *testing.T) {
	plugin := &Plugin{config: &Config{DefaultCacheKey: "shared-default-bucket"}}

	newCtx := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}

	t.Run("no header falls back to default_cache_key", func(t *testing.T) {
		key, ok := plugin.resolveCacheKey(newCtx())
		if !ok || key != "shared-default-bucket" {
			t.Fatalf("got (%q, %v), want (\"shared-default-bucket\", true)", key, ok)
		}
	})

	t.Run("explicit header wins over default_cache_key", func(t *testing.T) {
		ctx := newCtx()
		ctx.SetValue(CacheKey, "tenant-x")
		key, ok := plugin.resolveCacheKey(ctx)
		if !ok || key != "tenant-x" {
			t.Fatalf("got (%q, %v), want (\"tenant-x\", true)", key, ok)
		}
	})

	t.Run("no cache key configured or supplied, caching skipped", func(t *testing.T) {
		bare := &Plugin{config: &Config{}}
		if _, ok := bare.resolveCacheKey(newCtx()); ok {
			t.Fatal("expected caching to be skipped with no cache key available")
		}
	})

	t.Run("a virtual key on the request leaves the key unchanged", func(t *testing.T) {
		ctx := newCtx()
		ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk-A")
		ctx.SetValue(CacheKey, "session-1")
		key, ok := plugin.resolveCacheKey(ctx)
		if !ok || key != "session-1" {
			t.Fatalf("got (%q, %v), want (\"session-1\", true)", key, ok)
		}
	})

	t.Run("a key shaped like vk:<id>:<key> is an ordinary key", func(t *testing.T) {
		ctx := newCtx()
		ctx.SetValue(CacheKey, "vk:vk-A:shared-default-bucket")
		key, ok := plugin.resolveCacheKey(ctx)
		if !ok || key != "vk:vk-A:shared-default-bucket" {
			t.Fatalf("got (%q, %v), want the key unchanged", key, ok)
		}
	})
}

// TestResolveCacheThreshold covers the floor: a caller may raise the similarity bar but
// never lower it below the operator's configured value.
func TestResolveCacheThreshold(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	plugin := &Plugin{config: &Config{Threshold: 0.8}, logger: logger}

	newCtx := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}

	tests := []struct {
		name      string
		override  any
		wantValue float64
	}{
		{"no override, uses configured threshold", nil, 0.8},
		{"override of 0 (defeat attempt) is floored at configured threshold", 0.0, 0.8},
		{"override below configured is floored at configured threshold", 0.5, 0.8},
		{"override above configured raises the bar", 0.95, 0.95},
		{"override exactly at configured threshold is a no-op", 0.8, 0.8},
		{"non-float override falls back to configured threshold", "not-a-float", 0.8},
		{"override above 1 is capped at 1", 1.7, 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newCtx()
			if tt.override != nil {
				ctx.SetValue(CacheThresholdKey, tt.override)
			}
			got := plugin.resolveCacheThreshold(ctx)
			if got != tt.wantValue {
				t.Errorf("resolveCacheThreshold() = %v, want %v", got, tt.wantValue)
			}
		})
	}
}

// TestResponseHasToolCalls_ShellAndApplyPatch pins that shell and apply_patch calls
// count as client-executed tool calls, so cache_tool_call_responses=false skips them.
func TestResponseHasToolCalls_ShellAndApplyPatch(t *testing.T) {
	for _, itemType := range []schemas.ResponsesMessageType{
		schemas.ResponsesMessageTypeShellCall,
		schemas.ResponsesMessageTypeApplyPatchCall,
	} {
		t.Run(string(itemType), func(t *testing.T) {
			item := schemas.ResponsesMessage{Type: schemas.Ptr(itemType)}
			if !responseHasToolCalls(&schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{Output: []schemas.ResponsesMessage{item}}}) {
				t.Errorf("non-stream %s not treated as a tool call", itemType)
			}
			if !responseHasToolCalls(&schemas.BifrostResponse{ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{Item: &item}}) {
				t.Errorf("stream %s not treated as a tool call", itemType)
			}
		})
	}
}

// identityScopeTestPlugin builds a direct-only plugin over store. configJSON is decoded over the
// defaults the way the plugins API decodes a saved config, so a test can toggle settings by their
// wire names.
func identityScopeTestPlugin(t *testing.T, store vectorstore.VectorStore, configJSON string) *Plugin {
	t.Helper()
	config := &Config{
		Dimension:                    1,
		TTL:                          5 * time.Minute,
		Threshold:                    DefaultCacheThreshold,
		ConversationHistoryThreshold: DefaultConversationHistoryThreshold,
	}
	if configJSON != "" {
		if err := json.Unmarshal([]byte(configJSON), config); err != nil {
			t.Fatalf("decode config %s: %v", configJSON, err)
		}
	}
	return &Plugin{
		store:  store,
		config: config,
		logger: bifrost.NewDefaultLogger(schemas.LogLevelError),
		stopCh: make(chan struct{}),
	}
}

// identityRef names who a test request is made by. Empty fields mean "not known".
type identityRef struct {
	vk   string
	user string
}

// identityContext builds a request context the way the HTTP transport and governance leave it, with
// the identity on the request's grant. A zero identityRef is a request with no identity at all.
func identityContext(t *testing.T, cacheKey string, who identityRef) *schemas.BifrostContext {
	t.Helper()
	ctx := withTestRequestID(schemas.NewBifrostContextWithValue(context.Background(), schemas.NoDeadline, CacheKey, cacheKey))
	var vk *schemas.EntityRef
	if who.vk != "" {
		vk = &schemas.EntityRef{ID: who.vk, Name: who.vk}
	}
	var user *schemas.UserRef
	if who.user != "" {
		user = &schemas.UserRef{ID: who.user}
	}
	g := grant.New()
	g.SetIdentity(grant.NewIdentity(schemas.Credential{}, user, vk, nil, nil, nil, nil))
	if !ctx.SetGrant(g) {
		t.Fatal("SetGrant refused the test grant")
	}
	return ctx
}

func identityTestChatRequest(prompt string, extraParams map[string]interface{}) *schemas.BifrostRequest {
	req := newCrossProviderChatRequest(schemas.OpenAI, "gpt-4o-mini", schemas.ChatCompletionRequest, prompt)
	if extraParams != nil {
		req.ChatRequest.Params = &schemas.ChatParameters{ExtraParams: extraParams}
	}
	return req
}

func identityTestChatResponse(content string) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID: "identity-scope-test",
			Choices: []schemas.BifrostResponseChoice{{
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role:    schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{ContentStr: bifrost.Ptr(content)},
					},
				},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-4o-mini",
				ResolvedModelUsed:      "gpt-4o-mini",
				RequestType:            schemas.ChatCompletionRequest,
			},
		},
	}
}

// writeThroughCache runs one request through PreLLMHook (which must miss) and PostLLMHook, then
// waits for the async store write, so the next lookup sees the entry.
func writeThroughCache(t *testing.T, plugin *Plugin, ctx *schemas.BifrostContext, req *schemas.BifrostRequest) {
	t.Helper()
	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	if err != nil {
		t.Fatalf("write PreLLMHook: %v", err)
	}
	if shortCircuit != nil {
		t.Fatal("write request unexpectedly hit the cache")
	}
	if _, _, err := plugin.PostLLMHook(ctx, identityTestChatResponse("cached answer"), nil); err != nil {
		t.Fatalf("write PostLLMHook: %v", err)
	}
	plugin.WaitForPendingOperations()
}

// cacheHits reports whether a lookup for req under ctx is served from the cache.
func cacheHits(t *testing.T, plugin *Plugin, ctx *schemas.BifrostContext, req *schemas.BifrostRequest) bool {
	t.Helper()
	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	if err != nil {
		t.Fatalf("read PreLLMHook: %v", err)
	}
	plugin.clearCacheState(ctx.Value(schemas.BifrostContextKeyRequestID).(string))
	return shortCircuit != nil && shortCircuit.Response != nil
}

// TestIdentityScopedDirectCache pins identity isolation on the direct path with scoping on (the
// default): an entry is only ever served to the identity the request's grant names, and the stored
// cache_key stays the caller's raw key.
func TestIdentityScopedDirectCache(t *testing.T) {
	const prompt = "Name the largest moon of Saturn."
	vkA := identityRef{vk: "vk-A"}
	vkB := identityRef{vk: "vk-B"}
	anonymous := identityRef{}

	t.Run("same virtual key hits, other identities miss", func(t *testing.T) {
		plugin := identityScopeTestPlugin(t, newObservableStore(), "")
		key := keyForTest(t, "")
		writeThroughCache(t, plugin, identityContext(t, key, vkA), identityTestChatRequest(prompt, nil))
		if !cacheHits(t, plugin, identityContext(t, key, vkA), identityTestChatRequest(prompt, nil)) {
			t.Fatal("same virtual key should hit its own entry")
		}
		if cacheHits(t, plugin, identityContext(t, key, vkB), identityTestChatRequest(prompt, nil)) {
			t.Fatal("another virtual key must not be served vk-A's entry")
		}
		if cacheHits(t, plugin, identityContext(t, key, anonymous), identityTestChatRequest(prompt, nil)) {
			t.Fatal("a request with no identity must not be served vk-A's entry")
		}
	})

	// The grant is the only source of identity. The deprecated per-field context keys are not
	// read, so a request that carries only those is a request with no identity.
	t.Run("legacy context keys alone carry no identity", func(t *testing.T) {
		plugin := identityScopeTestPlugin(t, newObservableStore(), "")
		key := keyForTest(t, "")
		legacyOnly := func(vk, user string) *schemas.BifrostContext {
			ctx := withTestRequestID(schemas.NewBifrostContextWithValue(context.Background(), schemas.NoDeadline, CacheKey, key))
			ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, vk)
			ctx.SetValue(schemas.BifrostContextKeyUserID, user)
			return ctx
		}
		writeThroughCache(t, plugin, legacyOnly("vk-A", "user-1"), identityTestChatRequest(prompt, nil))
		if !cacheHits(t, plugin, legacyOnly("vk-B", "user-2"), identityTestChatRequest(prompt, nil)) {
			t.Fatal("legacy keys alone must not scope the entry: both requests carry no identity on their grant")
		}
		if cacheHits(t, plugin, identityContext(t, key, vkA), identityTestChatRequest(prompt, nil)) {
			t.Fatal("a request whose grant names vk-A must not be served an entry written with no identity")
		}
	})

	t.Run("user identity scopes the entry", func(t *testing.T) {
		plugin := identityScopeTestPlugin(t, newObservableStore(), "")
		key := keyForTest(t, "")
		writeThroughCache(t, plugin, identityContext(t, key, identityRef{user: "user-1"}), identityTestChatRequest(prompt, nil))
		if !cacheHits(t, plugin, identityContext(t, key, identityRef{user: "user-1"}), identityTestChatRequest(prompt, nil)) {
			t.Fatal("same user should hit their own entry")
		}
		if cacheHits(t, plugin, identityContext(t, key, identityRef{user: "user-2"}), identityTestChatRequest(prompt, nil)) {
			t.Fatal("another user must not be served user-1's entry")
		}
		if cacheHits(t, plugin, identityContext(t, key, anonymous), identityTestChatRequest(prompt, nil)) {
			t.Fatal("a request with no identity must not be served user-1's entry")
		}
	})

	t.Run("virtual key and user together form one identity", func(t *testing.T) {
		plugin := identityScopeTestPlugin(t, newObservableStore(), "")
		key := keyForTest(t, "")
		both := identityRef{vk: "vk-A", user: "user-1"}
		writeThroughCache(t, plugin, identityContext(t, key, both), identityTestChatRequest(prompt, nil))
		if !cacheHits(t, plugin, identityContext(t, key, both), identityTestChatRequest(prompt, nil)) {
			t.Fatal("same virtual key and user should hit their own entry")
		}
		if cacheHits(t, plugin, identityContext(t, key, vkA), identityTestChatRequest(prompt, nil)) {
			t.Fatal("the same virtual key under no user must not be served the user's entry")
		}
		if cacheHits(t, plugin, identityContext(t, key, identityRef{vk: "vk-A", user: "user-2"}), identityTestChatRequest(prompt, nil)) {
			t.Fatal("the same virtual key under another user must not be served the user's entry")
		}
	})

	t.Run("stored cache_key is the raw key for every identity", func(t *testing.T) {
		store := newObservableStore()
		plugin := identityScopeTestPlugin(t, store, "")
		key := keyForTest(t, "")
		for _, who := range []identityRef{vkA, vkB, anonymous} {
			writeThroughCache(t, plugin, identityContext(t, key, who), identityTestChatRequest(prompt, nil))
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		if len(store.chunks) != 3 {
			t.Fatalf("expected one entry per identity, got %d", len(store.chunks))
		}
		for id, chunk := range store.chunks {
			if got := chunk.Properties["cache_key"]; got != key {
				t.Fatalf("entry %s stored cache_key %v, want the raw key %q so clear-by-key reaches it", id, got, key)
			}
		}
	})

	t.Run("a request body field named identity_scope cannot borrow another identity's entry", func(t *testing.T) {
		plugin := identityScopeTestPlugin(t, newObservableStore(), "")
		key := keyForTest(t, "")
		writeThroughCache(t, plugin, identityContext(t, key, vkA), identityTestChatRequest(prompt, nil))
		forged := identityTestChatRequest(prompt, map[string]interface{}{"identity_scope": map[string]interface{}{"vk": "vk-A"}})
		if cacheHits(t, plugin, identityContext(t, key, vkB), forged) {
			t.Fatal("vk-B claimed vk-A's scope through a body field and was served vk-A's entry")
		}
		if cacheHits(t, plugin, identityContext(t, key, anonymous), forged) {
			t.Fatal("an anonymous caller claimed vk-A's scope through a body field and was served vk-A's entry")
		}
	})
}

// TestIdentityScopingDisabledSharesEntries pins the opt-out: with disable_identity_scoping set,
// every identity shares entries by cache key again, and entries written in that mode are never
// served once scoping is back on.
func TestIdentityScopingDisabledSharesEntries(t *testing.T) {
	const prompt = "Name the largest moon of Jupiter."
	vkA := identityRef{vk: "vk-A"}
	vkB := identityRef{vk: "vk-B"}
	anonymous := identityRef{}

	t.Run("every identity shares an entry", func(t *testing.T) {
		plugin := identityScopeTestPlugin(t, newObservableStore(), `{"disable_identity_scoping": true}`)
		key := keyForTest(t, "")
		writeThroughCache(t, plugin, identityContext(t, key, vkA), identityTestChatRequest(prompt, nil))
		if !cacheHits(t, plugin, identityContext(t, key, vkB), identityTestChatRequest(prompt, nil)) {
			t.Fatal("with identity scoping disabled, vk-B should be served vk-A's entry")
		}
		if !cacheHits(t, plugin, identityContext(t, key, anonymous), identityTestChatRequest(prompt, nil)) {
			t.Fatal("with identity scoping disabled, a request with no identity should be served vk-A's entry")
		}
	})

	t.Run("shared entries are not served once scoping is back on", func(t *testing.T) {
		store := newObservableStore()
		shared := identityScopeTestPlugin(t, store, `{"disable_identity_scoping": true}`)
		scoped := identityScopeTestPlugin(t, store, "")
		key := keyForTest(t, "")
		writeThroughCache(t, shared, identityContext(t, key, anonymous), identityTestChatRequest(prompt, nil))
		if cacheHits(t, scoped, identityContext(t, key, anonymous), identityTestChatRequest(prompt, nil)) {
			t.Fatal("an entry written while sharing must not be served to an anonymous request once scoping is on")
		}
		if cacheHits(t, scoped, identityContext(t, key, vkB), identityTestChatRequest(prompt, nil)) {
			t.Fatal("an entry written while sharing must not be served to vk-B once scoping is on")
		}
	})
}

// semanticFilterStore records the filters of every similarity search and reports no match, so a
// test can see what a semantic lookup would be scoped by.
type semanticFilterStore struct {
	*observableStore
	searches [][]vectorstore.Query
}

func (s *semanticFilterStore) GetNearest(ctx context.Context, ns string, v []float32, q []vectorstore.Query, sf []string, th float64, lim int64) ([]vectorstore.SearchResult, error) {
	s.mu.Lock()
	s.searches = append(s.searches, append([]vectorstore.Query(nil), q...))
	s.mu.Unlock()
	return nil, nil
}

func filterValue(queries []vectorstore.Query, field string) (interface{}, bool) {
	for _, q := range queries {
		if q.Field == field {
			return q.Value, true
		}
	}
	return nil, false
}

// TestIdentityScopeSemanticSearchFilters pins identity isolation on the semantic path: a
// similarity search is filtered by the raw cache key and by a params hash that differs per
// identity, so a paraphrase from one identity can never match another identity's entry.
func TestIdentityScopeSemanticSearchFilters(t *testing.T) {
	store := &semanticFilterStore{observableStore: newObservableStore()}
	plugin := identityScopeTestPlugin(t, store, `{"provider": "openai", "embedding_model": "text-embedding-3-small", "dimension": 3}`)
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		return &schemas.BifrostEmbeddingResponse{
			Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{0.1, 0.2, 0.3}}}},
		}, nil
	})
	key := keyForTest(t, "")
	const prompt = "Name the tallest mountain in Africa."

	for _, who := range []identityRef{{vk: "vk-A"}, {vk: "vk-A"}, {vk: "vk-B"}} {
		ctx := identityContext(t, key, who)
		ctx.SetValue(CacheTypeKey, CacheTypeSemantic)
		if cacheHits(t, plugin, ctx, identityTestChatRequest(prompt, nil)) {
			t.Fatal("the recording store never matches, so no lookup should hit")
		}
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.searches) != 3 {
		t.Fatalf("expected 3 similarity searches, got %d", len(store.searches))
	}
	for i, search := range store.searches {
		if got, _ := filterValue(search, "cache_key"); got != key {
			t.Fatalf("search %d filtered cache_key on %v, want the raw key %q", i, got, key)
		}
	}
	hashA1, _ := filterValue(store.searches[0], "params_hash")
	hashA2, _ := filterValue(store.searches[1], "params_hash")
	hashB, _ := filterValue(store.searches[2], "params_hash")
	if hashA1 != hashA2 {
		t.Fatalf("the same identity must search the same params hash, got %v and %v", hashA1, hashA2)
	}
	if hashA1 == hashB {
		t.Fatalf("vk-A and vk-B searched the same params hash %v, so a paraphrase could cross identities", hashA1)
	}
}

// TestPreLLMHookRecordsCacheLookup pins the lookup outcome the plugin records for response
// headers. It is recorded during the lookup, before any response exists, because a stream's
// headers are sent before any chunk carries cache metadata; and it must agree with what
// cache_debug reports for the same request.
func TestPreLLMHookRecordsCacheLookup(t *testing.T) {
	const prompt = "Name the smallest planet in the solar system."
	anonymous := identityRef{}

	t.Run("a miss records the id the entry will be written under", func(t *testing.T) {
		plugin := identityScopeTestPlugin(t, newObservableStore(), "")
		ctx := identityContext(t, keyForTest(t, ""), anonymous)
		if _, sc, err := plugin.PreLLMHook(ctx, identityTestChatRequest(prompt, nil)); err != nil || sc != nil {
			t.Fatalf("expected a miss, got short-circuit %v err %v", sc, err)
		}
		lookup, ok := schemas.CacheLookupFromContext(ctx)
		if !ok {
			t.Fatal("expected the lookup outcome recorded during PreLLMHook")
		}
		if lookup.CacheHit || lookup.HitType != nil || lookup.CacheID == nil {
			t.Fatalf("expected a miss with a cache id and no hit type, got %+v", lookup)
		}

		res := identityTestChatResponse("answer")
		if _, _, err := plugin.PostLLMHook(ctx, res, nil); err != nil {
			t.Fatalf("PostLLMHook: %v", err)
		}
		plugin.WaitForPendingOperations()
		debug := res.ChatResponse.ExtraFields.CacheDebug
		if debug == nil || debug.CacheHit || debug.CacheID == nil || *debug.CacheID != *lookup.CacheID {
			t.Fatalf("cache_debug %+v does not name the entry the lookup recorded (%s)", debug, *lookup.CacheID)
		}
	})

	t.Run("a hit records what cache_debug reports", func(t *testing.T) {
		plugin := identityScopeTestPlugin(t, newObservableStore(), "")
		key := keyForTest(t, "")
		writeThroughCache(t, plugin, identityContext(t, key, anonymous), identityTestChatRequest(prompt, nil))

		ctx := identityContext(t, key, anonymous)
		_, sc, err := plugin.PreLLMHook(ctx, identityTestChatRequest(prompt, nil))
		if err != nil || sc == nil || sc.Response == nil {
			t.Fatalf("expected a hit, got short-circuit %v err %v", sc, err)
		}
		lookup, ok := schemas.CacheLookupFromContext(ctx)
		if !ok {
			t.Fatal("expected the lookup outcome recorded during PreLLMHook")
		}
		debug := sc.Response.ChatResponse.ExtraFields.CacheDebug
		if !lookup.CacheHit || lookup.HitType == nil || *lookup.HitType != string(CacheTypeDirect) {
			t.Fatalf("expected a direct hit, got %+v", lookup)
		}
		if *lookup.CacheID != *debug.CacheID || *lookup.RequestedProvider != *debug.RequestedProvider || *lookup.RequestedModel != *debug.RequestedModel {
			t.Fatalf("lookup %+v disagrees with cache_debug %+v", lookup, debug)
		}
		if lookup.CacheHitLatency == nil {
			t.Fatal("expected the hit latency recorded")
		}
	})

	t.Run("a stream hit is recorded before any chunk is read", func(t *testing.T) {
		store := newObservableStore()
		plugin := identityScopeTestPlugin(t, store, "")
		key := keyForTest(t, "")
		streamRequest := func() *schemas.BifrostRequest {
			return newCrossProviderChatRequest(schemas.OpenAI, "gpt-4o-mini", schemas.ChatCompletionStreamRequest, prompt)
		}

		seed := identityContext(t, key, anonymous)
		if _, sc, err := plugin.PreLLMHook(seed, streamRequest()); err != nil || sc != nil {
			t.Fatalf("expected a miss, got short-circuit %v err %v", sc, err)
		}
		entryID := plugin.getCacheState(seed.Value(schemas.BifrostContextKeyRequestID).(string)).DirectCacheID
		chunk, err := json.Marshal(identityTestChatResponse("streamed answer"))
		if err != nil {
			t.Fatalf("marshal chunk: %v", err)
		}
		store.mu.Lock()
		store.chunks[entryID] = vectorstore.SearchResult{ID: entryID, Properties: map[string]interface{}{
			"stream_chunks": []interface{}{string(chunk)},
			"expires_at":    time.Now().Add(time.Hour).Unix(),
		}}
		store.mu.Unlock()

		ctx := identityContext(t, key, anonymous)
		_, sc, err := plugin.PreLLMHook(ctx, streamRequest())
		if err != nil || sc == nil || sc.Stream == nil {
			t.Fatalf("expected a stream replay, got short-circuit %v err %v", sc, err)
		}
		lookup, ok := schemas.CacheLookupFromContext(ctx)
		for range sc.Stream {
		}
		if !ok || !lookup.CacheHit || lookup.HitType == nil || *lookup.HitType != string(CacheTypeDirect) || *lookup.CacheID != entryID {
			t.Fatalf("expected a direct hit on %s recorded before the replay was read, got %+v (recorded=%v)", entryID, lookup, ok)
		}
	})

	t.Run("a semantic search records its embedding usage", func(t *testing.T) {
		store := &semanticFilterStore{observableStore: newObservableStore()}
		plugin := identityScopeTestPlugin(t, store, `{"provider": "openai", "embedding_model": "text-embedding-3-small", "dimension": 3}`)
		plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
			return &schemas.BifrostEmbeddingResponse{
				Data:  []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{0.1, 0.2, 0.3}}}},
				Usage: &schemas.BifrostLLMUsage{TotalTokens: 7},
			}, nil
		})
		ctx := identityContext(t, keyForTest(t, ""), anonymous)
		if _, sc, err := plugin.PreLLMHook(ctx, identityTestChatRequest(prompt, nil)); err != nil || sc != nil {
			t.Fatalf("expected a miss, got short-circuit %v err %v", sc, err)
		}
		lookup, ok := schemas.CacheLookupFromContext(ctx)
		if !ok || lookup.ProviderUsed == nil || *lookup.ProviderUsed != "openai" || lookup.ModelUsed == nil || lookup.InputTokens == nil || *lookup.InputTokens != 7 {
			t.Fatalf("expected the embedding provider, model and tokens recorded, got %+v (recorded=%v)", lookup, ok)
		}
	})

	t.Run("no lookup, nothing recorded", func(t *testing.T) {
		plugin := identityScopeTestPlugin(t, newObservableStore(), "")
		ctx := withTestRequestID(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))
		if _, sc, err := plugin.PreLLMHook(ctx, identityTestChatRequest(prompt, nil)); err != nil || sc != nil {
			t.Fatalf("expected the cache to be skipped, got short-circuit %v err %v", sc, err)
		}
		if _, ok := schemas.CacheLookupFromContext(ctx); ok {
			t.Fatal("a request without a cache key ran no lookup, so none should be recorded")
		}
	})
}

// TestMain drops the shared test namespace BEFORE the run starts (in case a
// previous run was interrupted and left stale entries) AND once after — both
// matter: tests share one namespace + one cache_key prefix per t.Name(),
// so stale writes from a prior interrupted run would surface as spurious
// cache hits on the first request of the next run.
func TestMain(m *testing.M) {
	dropSharedTestNamespace() // pre-run sweep
	code := m.Run()
	dropSharedTestNamespace() // post-run sweep
	os.Exit(code)
}

// dropSharedTestNamespace removes the shared test namespace from EVERY vector
// store backend the suite exercises - not just Weaviate. Redis, Qdrant, and
// Pinecone are persistent external services, so a deterministic per-t.Name()
// cache_key written by one run is still present on the next run (within TTL)
// and surfaces as a spurious cache hit on the first request. Sweeping all
// backends here is the suite's only cleanup, since clearTestKeysWithStore is a
// no-op. Stores that aren't configured/reachable in this environment are
// skipped silently.
func dropSharedTestNamespace() {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	for _, tc := range getVectorStoreTestCases() {
		storeConfig, ok := storeConfigForType(tc.StoreType)
		if !ok {
			continue
		}
		func() {
			store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
				Type:    tc.StoreType,
				Config:  storeConfig,
				Enabled: true,
			}, logger)
			if err != nil {
				return // backend not configured/available in this environment
			}
			defer store.Close(context.Background(), SharedTestNamespace)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = store.DeleteNamespace(ctx, SharedTestNamespace)
		}()
	}
}
