package requestguard

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestCapabilityGuardBlockAndRecommend(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CapabilityGuard.Mode = PolicyBlock
	cfg.Recommender.Enabled = true
	cfg.Recommender.SameProviderOnly = true
	plugin, err := Init(cfg, nil, requestGuardTestCatalog())
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer plugin.Cleanup()

	ctx := requestGuardTestContext()
	req := requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", true)
	if err := plugin.PreRequestHook(ctx, req); err != nil {
		t.Fatalf("PreRequestHook() error = %v", err)
	}
	gotReq, short, err := plugin.PreLLMHook(ctx, req)
	if err != nil {
		t.Fatalf("PreLLMHook() error = %v", err)
	}
	if gotReq != req || short == nil || short.Error == nil {
		t.Fatalf("PreLLMHook() = (%p, %+v), want blocked request", gotReq, short)
	}
	if short.Error.ExtraFields.ErrorType != schemas.ErrorTypePolicyModelBlocked || short.Error.AllowFallbacks == nil || *short.Error.AllowFallbacks {
		t.Fatalf("block error metadata = %+v, want structured model-policy refusal", short.Error)
	}
	if short.Error.Error == nil {
		t.Fatal("block error has no error field")
	}
	params, ok := short.Error.Error.Param.(map[string]any)
	if !ok {
		t.Fatalf("block error Param = %T, want structured map", short.Error.Error.Param)
	}
	recommendations, ok := params["recommendations"].([]ModelRecommendation)
	if !ok || len(recommendations) != 1 || recommendations[0].Model != "cheap" || recommendations[0].Provider != schemas.OpenAI {
		t.Fatalf("structured recommendations = %#v, want compatible openai/cheap candidate", params["recommendations"])
	}
}

func TestBudgetGuardBlockAndAutoDowngrade(t *testing.T) {
	catalog := requestGuardTestCatalog()
	blockConfig := DefaultConfig()
	blockConfig.CapabilityGuard.Mode = PolicyOff
	blockConfig.BudgetGuard.Mode = PolicyBlock
	blockConfig.BudgetGuard.MaxEstimatedCostUSD = 0.005
	blockConfig.BudgetGuard.DefaultOutputTokens = 128
	blockConfig.Recommender.Enabled = true
	blockConfig.Recommender.SameProviderOnly = true
	blocker, err := Init(blockConfig, nil, catalog)
	if err != nil {
		t.Fatalf("Init(block config) error = %v", err)
	}
	defer blocker.Cleanup()

	blockedCtx := requestGuardTestContext()
	blockedReq := requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", false)
	if err := blocker.PreRequestHook(blockedCtx, blockedReq); err != nil {
		t.Fatalf("PreRequestHook(block) error = %v", err)
	}
	_, short, err := blocker.PreLLMHook(blockedCtx, blockedReq)
	if err != nil || short == nil || short.Error == nil {
		t.Fatalf("PreLLMHook(block) = (%+v, %v), want budget block", short, err)
	}
	if short.Error.ExtraFields.ErrorType != schemas.ErrorTypePolicyBudgetExceeded {
		t.Fatalf("block error type = %q, want budget exceeded", short.Error.ExtraFields.ErrorType)
	}

	downgradeConfig := blockConfig
	downgradeConfig.BudgetGuard.Mode = PolicyDowngrade
	downgradeConfig.Recommender.AutoDowngradeOnBudgetExceeded = true
	downgrader, err := Init(downgradeConfig, nil, catalog)
	if err != nil {
		t.Fatalf("Init(downgrade config) error = %v", err)
	}
	defer downgrader.Cleanup()

	downgradeCtx := requestGuardTestContext()
	downgradeReq := requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", false)
	if err := downgrader.PreRequestHook(downgradeCtx, downgradeReq); err != nil {
		t.Fatalf("PreRequestHook(downgrade) error = %v", err)
	}
	if downgradeReq.ChatRequest.Provider != schemas.OpenAI || downgradeReq.ChatRequest.Model != "cheap" {
		t.Fatalf("downgraded route = %s/%s, want openai/cheap", downgradeReq.ChatRequest.Provider, downgradeReq.ChatRequest.Model)
	}
	decision, ok := guardDecisionFromContext(downgradeCtx)
	if !ok || !decision.Downgraded || decision.OriginalModel != "expensive" || decision.EffectiveModel != "cheap" {
		t.Fatalf("downgrade decision = %+v", decision)
	}
	_, short, err = downgrader.PreLLMHook(downgradeCtx, downgradeReq)
	if err != nil || short != nil {
		t.Fatalf("PreLLMHook(downgraded request) = (%+v, %v), want provider execution", short, err)
	}
}

func TestUnaryCoalescingConcurrent(t *testing.T) {
	registry := prometheus.NewRegistry()
	cfg := DefaultConfig()
	cfg.CapabilityGuard.Mode = PolicyOff
	cfg.BudgetGuard.Mode = PolicyOff
	cfg.Coalescer.Enabled = true
	cfg.Coalescer.MaxInFlight = 16
	// Followers wait inside PreLLMHook until the leader completes. Keep their
	// coalescer deadline well beyond the test's registration barrier so a slow
	// race run cannot turn followers into fail-open leaders before we release it.
	cfg.Coalescer.WaitTimeout = 30 * time.Second
	cfg.Metrics.Enabled = true
	cfg.Metrics.Registerer = registry
	plugin, err := Init(cfg, nil, requestGuardTestCatalog())
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer plugin.Cleanup()

	const requestCount = 10
	releaseLeader := make(chan struct{})
	var releaseLeaderOnce sync.Once
	releaseLeaderNow := func() { releaseLeaderOnce.Do(func() { close(releaseLeader) }) }
	t.Cleanup(releaseLeaderNow)
	leaderContexts := make(chan *schemas.BifrostContext, requestCount)
	followerShortCircuits := make(chan *schemas.LLMPluginShortCircuit, requestCount-1)
	leaderResponse := requestGuardTestResponse("leader")
	var requests sync.WaitGroup
	for i := 0; i < requestCount; i++ {
		requests.Add(1)
		go func() {
			defer requests.Done()
			ctx := requestGuardTestContext()
			req := requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", false)
			_, short, hookErr := plugin.PreLLMHook(ctx, req)
			if hookErr != nil {
				t.Errorf("PreLLMHook() error = %v", hookErr)
				return
			}
			if short != nil {
				followerShortCircuits <- short
				return
			}
			leaderContexts <- ctx
			<-releaseLeader
			plugin.PostLLMHook(ctx, leaderResponse, nil)
		}()
	}

	var leaderCtx *schemas.BifrostContext
	select {
	case leaderCtx = <-leaderContexts:
	case <-time.After(2 * time.Second):
		t.Fatal("no coalescer leader started")
	}
	waitForRequestGuardTimeout(t, 5*time.Second, func() bool {
		plugin.coalescer.mu.Lock()
		defer plugin.coalescer.mu.Unlock()
		for _, flight := range plugin.coalescer.flights {
			return flight.waiters == requestCount-1
		}
		return false
	})
	releaseLeaderNow()
	requests.Wait()

	seenResponses := map[*schemas.BifrostResponse]struct{}{leaderResponse: {}}
	followers := 0
	for i := 0; i < requestCount-1; i++ {
		select {
		case short := <-followerShortCircuits:
			if short == nil || short.Error != nil || short.Response == nil {
				t.Fatalf("follower short circuit = %+v, want cloned response", short)
			}
			if _, duplicate := seenResponses[short.Response]; duplicate {
				t.Fatal("follower response shares a response pointer")
			}
			seenResponses[short.Response] = struct{}{}
			followers++
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for coalesced follower response")
		}
	}
	if followers != requestCount-1 {
		t.Fatalf("followers = %d, want %d", followers, requestCount-1)
	}
	if got := testutil.ToFloat64(plugin.metrics.coalescedExecutions.WithLabelValues("openai", "expensive", "false")); got != requestCount-1 {
		t.Fatalf("coalesced_executions_avoided_total = %v, want %d", got, requestCount-1)
	}
	if leaderCtx == nil {
		t.Fatal("leader context was not captured")
	}
}

func TestStreamingCoalescingConcurrent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CapabilityGuard.Mode = PolicyOff
	cfg.BudgetGuard.Mode = PolicyOff
	cfg.Coalescer.Enabled = true
	cfg.Coalescer.EnableStreaming = true
	cfg.Coalescer.MaxInFlight = 8
	cfg.Coalescer.MaxBufferedChunks = 8
	cfg.Coalescer.MaxBufferedBytes = 1 << 16
	plugin, err := Init(cfg, nil, requestGuardTestCatalog())
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer plugin.Cleanup()

	leaderCtx := requestGuardTestContext()
	_, short, err := plugin.PreLLMHook(leaderCtx, requestGuardTestRequest(schemas.ChatCompletionStreamRequest, schemas.OpenAI, "expensive", false))
	if err != nil || short != nil {
		t.Fatalf("leader PreLLMHook() = (%+v, %v), want provider execution", short, err)
	}

	const followerCount = 4
	followerChunks := make(chan []*schemas.BifrostStreamChunk, followerCount)
	var followers sync.WaitGroup
	for i := 0; i < followerCount; i++ {
		followers.Add(1)
		go func() {
			defer followers.Done()
			ctx := requestGuardTestContext()
			_, short, hookErr := plugin.PreLLMHook(ctx, requestGuardTestRequest(schemas.ChatCompletionStreamRequest, schemas.OpenAI, "expensive", false))
			if hookErr != nil || short == nil || short.Stream == nil {
				t.Errorf("follower PreLLMHook() = (%+v, %v), want stream short circuit", short, hookErr)
				return
			}
			var chunks []*schemas.BifrostStreamChunk
			for chunk := range short.Stream {
				chunks = append(chunks, chunk)
			}
			followerChunks <- chunks
		}()
	}
	waitForRequestGuard(t, func() bool {
		plugin.coalescer.mu.Lock()
		defer plugin.coalescer.mu.Unlock()
		for _, flight := range plugin.coalescer.flights {
			return len(flight.subscribers) == followerCount
		}
		return false
	})

	for i := 0; i < 4; i++ {
		if i == 3 {
			leaderCtx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		} else {
			leaderCtx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, false)
		}
		plugin.PostLLMHook(leaderCtx, requestGuardTestResponse(fmt.Sprintf("chunk-%d", i)), nil)
	}

	seenChunks := make(map[*schemas.BifrostStreamChunk]struct{})
	for i := 0; i < followerCount; i++ {
		select {
		case chunks := <-followerChunks:
			if len(chunks) != 4 {
				t.Fatalf("follower received %d chunks, want 4", len(chunks))
			}
			for index, chunk := range chunks {
				if chunk == nil || chunk.BifrostChatResponse == nil || chunk.BifrostChatResponse.ID != fmt.Sprintf("chunk-%d", index) {
					t.Fatalf("follower chunk %d = %+v", index, chunk)
				}
				if _, duplicate := seenChunks[chunk]; duplicate {
					t.Fatal("stream followers shared a chunk pointer")
				}
				seenChunks[chunk] = struct{}{}
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for follower stream")
		}
	}
	followers.Wait()
}

func TestHTTPResponseHeadersAndMetrics(t *testing.T) {
	registry := prometheus.NewRegistry()
	cfg := DefaultConfig()
	cfg.CapabilityGuard.Mode = PolicyBlock
	cfg.Recommender.Enabled = true
	cfg.Recommender.SameProviderOnly = true
	cfg.Headers.Enabled = true
	cfg.Metrics.Enabled = true
	cfg.Metrics.Registerer = registry
	catalog := requestGuardTestCatalog()
	plugin, err := Init(cfg, nil, catalog)
	if err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	defer plugin.Cleanup()

	ctx := requestGuardTestContext()
	req := requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", true)
	if err := plugin.PreRequestHook(ctx, req); err != nil {
		t.Fatalf("PreRequestHook() error = %v", err)
	}
	_, short, err := plugin.PreLLMHook(ctx, req)
	if err != nil || short == nil || short.Error == nil {
		t.Fatalf("PreLLMHook() = (%+v, %v), want block", short, err)
	}

	headers := &schemas.HTTPResponseMetadata{Headers: map[string]string{}}
	if err := plugin.HTTPTransportResponseHeadersHook(ctx, &schemas.HTTPRequest{}, headers); err != nil {
		t.Fatalf("HTTPTransportResponseHeadersHook() error = %v", err)
	}
	for header, want := range map[string]string{
		"X-Bifrost-RequestGuard-Action":         "block",
		"X-Bifrost-RequestGuard-Estimated-Cost": "0.02000000",
		"X-Bifrost-RequestGuard-Recommendation": "openai/cheap",
		"X-Bifrost-RequestGuard-Coalesced":      "none",
	} {
		if got := headers.Header(header); got != want {
			t.Errorf("header %s = %q, want %q", header, got, want)
		}
	}
	decision, _ := guardDecisionFromContext(ctx)
	if got := testutil.ToFloat64(plugin.metrics.requestsBlocked.WithLabelValues(decision.BlockReason, "openai", "expensive")); got != 1 {
		t.Errorf("requests_blocked_total = %v, want 1", got)
	}
	if got := testutil.ToFloat64(plugin.metrics.capabilityFailures.WithLabelValues("tools", "openai", "expensive")); got != 1 {
		t.Errorf("tools capability failures = %v, want 1", got)
	}
	if got := testutil.ToFloat64(plugin.metrics.capabilityFailures.WithLabelValues("vision", "openai", "expensive")); got != 1 {
		t.Errorf("vision capability failures = %v, want 1", got)
	}
	if got := histogramSampleCount(t, registry, "bifrost_requestguard_eval_duration_seconds"); got == 0 {
		t.Fatal("request-guard evaluation histogram has no observations")
	}

	downgradeConfig := cfg
	downgradeConfig.Recommender.AutoDowngradeOnCapabilityFailure = true
	downgrader, err := Init(downgradeConfig, nil, catalog)
	if err != nil {
		t.Fatalf("Init(downgrade config) error = %v", err)
	}
	defer downgrader.Cleanup()
	downgradeCtx := requestGuardTestContext()
	downgradeReq := requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", true)
	if err := downgrader.PreRequestHook(downgradeCtx, downgradeReq); err != nil {
		t.Fatalf("PreRequestHook(downgrade) error = %v", err)
	}
	_, short, err = downgrader.PreLLMHook(downgradeCtx, downgradeReq)
	if err != nil || short != nil {
		t.Fatalf("PreLLMHook(downgrade) = (%+v, %v), want provider execution", short, err)
	}
	downgradeHeaders := &schemas.HTTPResponseMetadata{}
	if err := downgrader.HTTPTransportResponseHeadersHook(downgradeCtx, nil, downgradeHeaders); err != nil {
		t.Fatalf("HTTPTransportResponseHeadersHook(downgrade) error = %v", err)
	}
	if got := downgradeHeaders.Header("X-Bifrost-RequestGuard-Original-Model"); got != "expensive" {
		t.Errorf("original model header = %q, want expensive", got)
	}
	if got := testutil.ToFloat64(downgrader.metrics.downgrades.WithLabelValues("capability_failure", "expensive", "cheap")); got != 1 {
		t.Errorf("downgrades_total = %v, want 1", got)
	}
}

func BenchmarkEstimateAndBudget(b *testing.B) {
	catalog := requestGuardTestCatalog()
	config := DefaultConfig()
	features := ExtractRequestFeatures(requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", true))
	req := requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		estimate, _ := EstimateTokens(req, features, config.BudgetGuard, catalog)
		_ = EvaluateBudget(config.BudgetGuard, estimate)
	}
}

func BenchmarkRecommendModels(b *testing.B) {
	catalog := requestGuardBenchmarkCatalog(24)
	config := DefaultConfig()
	config.Recommender.Enabled = true
	config.Recommender.SameProviderOnly = true
	features := RequestFeatures{RequestType: schemas.ChatCompletionRequest, Provider: schemas.OpenAI, Model: "model-0", Supported: true}
	estimate := CostEstimate{InputTokens: 128, OutputTokens: 128, TotalTokens: 256, EstimatedCostUSD: 0.1, PricingAvailable: true}
	usage := &schemas.BifrostLLMUsage{PromptTokens: estimate.InputTokens, CompletionTokens: estimate.OutputTokens, TotalTokens: estimate.TotalTokens}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = RecommendModels(catalog, config.CapabilityGuard, config.BudgetGuard, config.Recommender, features, estimate, usage, nil, estimate.EstimatedCostUSD, false)
	}
}

func BenchmarkComputeFingerprint(b *testing.B) {
	req := requestGuardTestRequest(schemas.ChatCompletionRequest, schemas.OpenAI, "expensive", true)
	features := ExtractRequestFeatures(req)
	config := DefaultConfig().Coalescer
	config.Enabled = true
	ctx := requestGuardTestContext()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = ComputeFingerprint(ctx, req, features, config)
	}
}

func requestGuardTestContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Time{})
}

func requestGuardTestRequest(requestType schemas.RequestType, provider schemas.ModelProvider, model string, toolsAndVision bool) *schemas.BifrostRequest {
	request := &schemas.BifrostRequest{
		RequestType: requestType,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: provider,
			Model:    model,
			Input: []schemas.ChatMessage{{
				Role:    "user",
				Content: &schemas.ChatMessageContent{ContentStr: stringPtr("hello world")},
			}},
			Params: &schemas.ChatParameters{},
		},
	}
	if toolsAndVision {
		request.ChatRequest.Params.Tools = []schemas.ChatTool{{}}
		request.ChatRequest.Input[0].Content.ContentBlocks = []schemas.ChatContentBlock{{
			Type:           schemas.ChatContentBlockTypeImage,
			ImageURLStruct: &schemas.ChatInputImage{URL: "https://example.test/image.png"},
		}}
	}
	return request
}

func requestGuardTestCatalog() *fakeCatalog {
	return &fakeCatalog{
		capabilitiesByRoute: map[string]*schemas.ModelCapabilities{
			routeKey(schemas.OpenAI, "expensive"): {SupportsFunctionCalling: boolPtr(false)},
			routeKey(schemas.OpenAI, "cheap"): {
				SupportsFunctionCalling:         boolPtr(true),
				SupportsParallelFunctionCalling: boolPtr(true),
				SupportsToolChoice:              boolPtr(true),
				SupportsForcedToolChoice:        boolPtr(true),
				SupportsResponseSchema:          boolPtr(true),
				SupportsNativeStructuredOutput:  boolPtr(true),
				SupportsResponseSchemaWithTools: boolPtr(true),
				MaxOutputTokens:                 intPtr(4096),
			},
		},
		modelInfoByRoute: map[string]*schemas.Model{
			routeKey(schemas.OpenAI, "expensive"): {ID: "expensive", ContextLength: intPtr(4096), MaxInputTokens: intPtr(4096), Architecture: &schemas.Architecture{InputModalities: []string{"text"}}},
			routeKey(schemas.OpenAI, "cheap"):     {ID: "cheap", ContextLength: intPtr(4096), MaxInputTokens: intPtr(4096), Architecture: &schemas.Architecture{InputModalities: []string{"text", "image"}}},
		},
		pricingByRoute: map[string]*modelcatalog.PricingEntry{
			routeKey(schemas.OpenAI, "expensive"): {},
			routeKey(schemas.OpenAI, "cheap"):     {},
		},
		costByRoute: map[string]float64{
			routeKey(schemas.OpenAI, "expensive"): 0.02,
			routeKey(schemas.OpenAI, "cheap"):     0.002,
		},
		modelsByProvider: map[schemas.ModelProvider][]string{
			schemas.OpenAI: {"expensive", "cheap"},
		},
		providersByModel: map[string][]schemas.ModelProvider{
			"expensive": {schemas.OpenAI},
			"cheap":     {schemas.OpenAI},
		},
	}
}

func requestGuardBenchmarkCatalog(modelCount int) *fakeCatalog {
	catalog := requestGuardTestCatalog()
	catalog.modelsByProvider[schemas.OpenAI] = make([]string, 0, modelCount)
	for i := 0; i < modelCount; i++ {
		model := fmt.Sprintf("model-%d", i)
		catalog.modelsByProvider[schemas.OpenAI] = append(catalog.modelsByProvider[schemas.OpenAI], model)
		key := routeKey(schemas.OpenAI, model)
		catalog.capabilitiesByRoute[key] = &schemas.ModelCapabilities{}
		catalog.modelInfoByRoute[key] = &schemas.Model{ID: model, ContextLength: intPtr(8192), MaxInputTokens: intPtr(8192)}
		catalog.pricingByRoute[key] = &modelcatalog.PricingEntry{}
		catalog.costByRoute[key] = 0.001 + float64(i)*0.00001
	}
	return catalog
}

func requestGuardTestResponse(id string) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
		ID:      id,
		Choices: []schemas.BifrostResponseChoice{},
		Usage:   &schemas.BifrostLLMUsage{},
	}}
}

func waitForRequestGuard(t *testing.T, ready func() bool) {
	waitForRequestGuardTimeout(t, 2*time.Second, ready)
}

func waitForRequestGuardTimeout(t *testing.T, timeout time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for requestguard state")
}

func histogramSampleCount(t *testing.T, registry *prometheus.Registry, name string) uint64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	for _, family := range families {
		if family.GetName() == name {
			var count uint64
			for _, metric := range family.Metric {
				if metric.Histogram != nil {
					count += metric.Histogram.GetSampleCount()
				}
			}
			return count
		}
	}
	return 0
}
