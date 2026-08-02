package semanticcache

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sync"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// requestCapturer is an LLMPlugin that records the request it sees in
// PreLLMHook. Placed AFTER semantic_cache in the plugin chain it observes
// the request post-cache-plugin-mutation; we then assert that nothing
// landed in the request that originated from cache-side normalization
// (lowercase, whitespace-trim, system-prompt filtering, etc.).
//
// This complements the in-process unit tests because those exercise the
// helpers that DO normalize (getNormalizedInputForCaching) — what we want
// here is a contract test on the request that flows downstream.
type requestCapturer struct {
	mu       sync.Mutex
	captured *schemas.BifrostRequest
}

func (p *requestCapturer) GetName() string { return "test-request-capturer" }
func (p *requestCapturer) Cleanup() error  { return nil }

func (p *requestCapturer) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *requestCapturer) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	p.mu.Lock()
	// Snapshot the request via JSON round-trip so any later mutation by the
	// pipeline (none expected, but be defensive) can't retroactively change
	// what the test sees.
	data, err := json.Marshal(req)
	if err == nil {
		var snapshot schemas.BifrostRequest
		if jerr := json.Unmarshal(data, &snapshot); jerr == nil {
			p.captured = &snapshot
		}
	}
	if p.captured == nil {
		p.captured = req // fall back to direct reference
	}
	p.mu.Unlock()
	return req, nil, nil
}

func (p *requestCapturer) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, e *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, e, nil
}

// TestCachingDoesNotMutateRequestSentToProvider runs through the full plugin
// pipeline against the real OpenAI API and asserts that nothing the cache
// plugin does internally (text normalization, system-prompt filtering,
// metadata extraction, embedding generation) leaks into the request that
// reaches the provider.
//
// The test is gated on OPENAI_API_KEY because we need a real round-trip; the
// in-process mocker would short-circuit before the request body is finalized.
func TestCachingDoesNotMutateRequestSentToProvider(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-LLM test in -short mode")
	}
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set; needed for live LLM contract test")
	}
	t.Parallel()

	// Stand up the cache plugin against the shared Weaviate test namespace,
	// same as the rest of the integration suite.
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Type:    vectorstore.VectorStoreTypeWeaviate,
		Config:  getWeaviateConfigFromEnv(),
		Enabled: true,
	}, logger)
	if err != nil {
		t.Skipf("Weaviate not available: %v", err)
	}
	cfg := &Config{
		Provider:                     schemas.OpenAI,
		EmbeddingModel:               "text-embedding-3-small",
		Dimension:                    1536,
		Threshold:                    0.8,
		ConversationHistoryThreshold: DefaultConversationHistoryThreshold,
		VectorStoreNamespace:         SharedTestNamespace,
	}
	if err := ensureSharedTestNamespace(context.Background(), store, cfg.Dimension); err != nil {
		t.Fatalf("ensureSharedTestNamespace: %v", err)
	}
	cachePlugin, err := Init(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), cfg, logger, store)
	if err != nil {
		t.Fatalf("cache plugin Init: %v", err)
	}

	capturer := &requestCapturer{}

	// Real OpenAI provider, no mocker — the request must travel end-to-end.
	bctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	client, err := bifrost.Init(bctx, schemas.BifrostConfig{
		Account: &BaseAccount{},
		// Order matters: cache runs first, capturer second so it sees the
		// request as it flows out of the cache plugin.
		LLMPlugins: []schemas.LLMPlugin{cachePlugin, capturer},
		Logger:     logger,
	})
	if err != nil {
		t.Fatalf("bifrost.Init: %v", err)
	}
	defer client.Shutdown()
	cachePlugin.(*Plugin).SetEmbeddingRequestExecutor(client.EmbeddingRequest)

	// Content carefully chosen to surface normalization if it ever leaks:
	//   - leading/trailing whitespace (would be stripped by strings.TrimSpace)
	//   - mixed case (would be lowercased)
	//   - a system prompt (would be stripped if ExcludeSystemPrompt leaked)
	systemContent := "  RESPOND with a SINGLE word.  "
	userContent := "   Hello, World!   PRESERVE_THIS_VERBATIM.   "

	chatReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o-mini",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleSystem,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr(systemContent),
				},
			},
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: bifrost.Ptr(userContent),
				},
			},
		},
		Params: &schemas.ChatParameters{
			Temperature:         bifrost.Ptr(0.0),
			MaxCompletionTokens: bifrost.Ptr(5),
		},
	}

	ctx := newBaseTestContext()
	ctx.SetValue(CacheKey, keyForTest(t, ""))

	// Take a JSON snapshot of the original input as the test sent it.
	originalJSON, err := json.Marshal(chatReq)
	if err != nil {
		t.Fatalf("marshal original: %v", err)
	}

	if _, llmErr := client.ChatCompletionRequest(ctx, chatReq); llmErr != nil {
		// Even if OpenAI errors, the request was already captured by the
		// time the provider call fired. Continue with the assertion.
		t.Logf("upstream LLM error (expected to still proceed with assertion): %v", llmErr)
	}

	capturer.mu.Lock()
	captured := capturer.captured
	capturer.mu.Unlock()
	if captured == nil {
		t.Fatal("capturer never recorded a request — pipeline order or plugin wiring is wrong")
	}

	// 1) The chat input the provider saw must be byte-for-byte identical to
	//    what the caller passed in.
	capturedJSON, err := json.Marshal(captured.ChatRequest)
	if err != nil {
		t.Fatalf("marshal captured: %v", err)
	}
	var origMap, capMap map[string]any
	_ = json.Unmarshal(originalJSON, &origMap)
	_ = json.Unmarshal(capturedJSON, &capMap)
	if !reflect.DeepEqual(origMap["input"], capMap["input"]) {
		t.Fatalf("chat input mutated by cache plugin\noriginal: %s\ncaptured: %s", originalJSON, capturedJSON)
	}

	// 2) Belt-and-suspenders: explicit spot checks on the fields most likely
	//    to be mangled by normalization regressions, with clear failure messages.
	if len(captured.ChatRequest.Input) != len(chatReq.Input) {
		t.Fatalf("system prompt was filtered out: captured=%d messages, original=%d", len(captured.ChatRequest.Input), len(chatReq.Input))
	}
	if got := *captured.ChatRequest.Input[0].Content.ContentStr; got != systemContent {
		t.Fatalf("system content was modified: got %q, want %q", got, systemContent)
	}
	if got := *captured.ChatRequest.Input[1].Content.ContentStr; got != userContent {
		t.Fatalf("user content was modified: got %q, want %q", got, userContent)
	}
	if captured.ChatRequest.Input[0].Role != schemas.ChatMessageRoleSystem {
		t.Fatalf("system role was rewritten: got %q", captured.ChatRequest.Input[0].Role)
	}
}
