package telemetry

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/prometheus/client_golang/prometheus"
)

// newTestPlugin builds a PrometheusPlugin on a fresh registry with no pricing manager (cost
// skipped) and no custom labels, so each test's counters start at zero and are unambiguous.
func newTestPlugin(t *testing.T) *PrometheusPlugin {
	t.Helper()
	p, err := Init(&Config{}, nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	return p
}

// newHookContext returns a BifrostContext primed the way the plugin pipeline primes it before
// PostLLMHook: PreLLMHook has run, so startTimeKey and activeRequestTypeKey are set. Without
// startTimeKey, PostLLMHook logs a warning and records nothing.
func newHookContext(reqType schemas.RequestType) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	ctx.SetValue(startTimeKey, time.Now())
	ctx.SetValue(activeRequestTypeKey, reqType)
	return ctx
}

// counterTotal gathers the named counter family from the registry and sums every series'
// value. Summing over labels keeps the assertion independent of the exact label ordering.
func counterTotal(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range fams {
		if mf.GetName() != name {
			continue
		}
		var sum float64
		for _, m := range mf.GetMetric() {
			sum += m.GetCounter().GetValue()
		}
		return sum
	}
	return 0
}

// waitForCounter polls until the named counter reaches want (PostLLMHook records tokens in a
// background goroutine, so the write is not synchronous with the hook returning).
func waitForCounter(t *testing.T, reg *prometheus.Registry, name string, want float64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got := counterTotal(t, reg, name); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("counter %s = %v, want %v (timed out)", name, counterTotal(t, reg, name), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// usageCase describes one usage-bearing response type and the input/output tokens the plugin
// must record for it.
type usageCase struct {
	name     string
	reqType  schemas.RequestType
	response *schemas.BifrostResponse
	wantIn   float64
	wantOut  float64
}

// tokenUsageCases enumerates every non-streaming, usage-bearing response type that the logging
// plugin records token usage for in plugins/logging/operations.go (applyNonStreamingOutputToEntry).
// Telemetry MUST record tokens for the same set, or Bifrost logs will report usage that never
// reaches the Prometheus counters (and therefore Grafana) — the Grafana-vs-logs mismatch.
//
// When logging learns a new usage-bearing response type, add it here AND to the switch in
// PostLLMHook; this list is the contract between the two plugins.
func tokenUsageCases() []usageCase {
	return []usageCase{
		{
			name:    "chat",
			reqType: schemas.ChatCompletionRequest,
			response: &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
				Usage: &schemas.BifrostLLMUsage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18},
			}},
			wantIn: 11, wantOut: 7,
		},
		{
			name:    "text_completion",
			reqType: schemas.TextCompletionRequest,
			response: &schemas.BifrostResponse{TextCompletionResponse: &schemas.BifrostTextCompletionResponse{
				Usage: &schemas.BifrostLLMUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
			}},
			wantIn: 5, wantOut: 3,
		},
		{
			name:    "responses",
			reqType: schemas.ResponsesRequest,
			response: &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{
				Usage: &schemas.ResponsesResponseUsage{InputTokens: 9, OutputTokens: 4, TotalTokens: 13},
			}},
			wantIn: 9, wantOut: 4,
		},
		{
			name:    "embedding",
			reqType: schemas.EmbeddingRequest,
			response: &schemas.BifrostResponse{EmbeddingResponse: &schemas.BifrostEmbeddingResponse{
				Usage: &schemas.BifrostLLMUsage{PromptTokens: 6, CompletionTokens: 0, TotalTokens: 6},
			}},
			wantIn: 6, wantOut: 0,
		},
		// --- The three below regressed the Grafana-vs-logs parity before the fix. ---
		{
			name:    "compaction",
			reqType: schemas.CompactionRequest,
			response: &schemas.BifrostResponse{CompactionResponse: &schemas.BifrostCompactionResponse{
				Usage: &schemas.ResponsesResponseUsage{InputTokens: 20, OutputTokens: 8, TotalTokens: 28},
			}},
			wantIn: 20, wantOut: 8,
		},
		{
			name:    "image_generation",
			reqType: schemas.ImageGenerationRequest,
			response: &schemas.BifrostResponse{ImageGenerationResponse: &schemas.BifrostImageGenerationResponse{
				Usage: &schemas.ImageUsage{InputTokens: 15, OutputTokens: 2, TotalTokens: 17},
			}},
			wantIn: 15, wantOut: 2,
		},
		{
			name:    "passthrough",
			reqType: schemas.PassthroughRequest,
			response: &schemas.BifrostResponse{PassthroughResponse: &schemas.BifrostPassthroughResponse{
				PassthroughUsage: &schemas.BifrostPassthroughUsage{
					LLMUsage: &schemas.BifrostLLMUsage{PromptTokens: 30, CompletionTokens: 12, TotalTokens: 42},
				},
			}},
			wantIn: 30, wantOut: 12,
		},
	}
}

// TestTokenExtractionParityWithLogging is the regression guard for the customer-reported
// Grafana-vs-Bifrost-logs usage mismatch: it drives PostLLMHook with one response per
// usage-bearing type that logging records, and asserts bifrost_input_tokens_total /
// bifrost_output_tokens_total reflect the exact token counts. A response type that logging
// records but PostLLMHook's switch omits records zero here and fails the test.
func TestTokenExtractionParityWithLogging(t *testing.T) {
	for _, tc := range tokenUsageCases() {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPlugin(t)
			tc.response.PopulateExtraFields(tc.reqType, schemas.ModelProvider("openai"), "test-model", "test-model")

			ctx := newHookContext(tc.reqType)
			if _, _, err := p.PostLLMHook(ctx, tc.response, nil); err != nil {
				t.Fatalf("PostLLMHook: %v", err)
			}

			waitForCounter(t, p.registry, "bifrost_input_tokens_total", tc.wantIn)
			waitForCounter(t, p.registry, "bifrost_output_tokens_total", tc.wantOut)
		})
	}
}

// TestPostLLMHookRequiresStartTime asserts the documented early-return: without startTimeKey in
// context (PreLLMHook never ran) PostLLMHook records nothing rather than panicking or recording
// with a bogus latency.
func TestPostLLMHookRequiresStartTime(t *testing.T) {
	p := newTestPlugin(t)
	resp := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10},
	}}
	resp.PopulateExtraFields(schemas.ChatCompletionRequest, "openai", "m", "m")

	ctx := schemas.NewBifrostContext(context.Background(), time.Now().Add(time.Minute))
	// Deliberately omit startTimeKey.
	if _, _, err := p.PostLLMHook(ctx, resp, nil); err != nil {
		t.Fatalf("PostLLMHook: %v", err)
	}
	// Give any (incorrectly-spawned) goroutine a chance to write before asserting zero.
	time.Sleep(50 * time.Millisecond)
	if got := counterTotal(t, p.registry, "bifrost_input_tokens_total"); got != 0 {
		t.Errorf("input tokens = %v, want 0 (no start time -> no recording)", got)
	}
}

// TestMetricsEnabledGating covers the pull-gateway (/metrics scrape) on/off switch: default-on
// when the config omits the field (back-compat), and honoring an explicit value.
func TestMetricsEnabledGating(t *testing.T) {
	cases := []struct {
		name string
		set  *bool
		want bool
	}{
		{"default omitted -> on", nil, true},
		{"explicit true", boolPtr(true), true},
		{"explicit false", boolPtr(false), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Init(&Config{MetricsEnabled: tc.set}, nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			if got := p.IsMetricsEnabled(); got != tc.want {
				t.Errorf("IsMetricsEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGetMetricsGathererCombinesRegistries asserts the /metrics scrape gatherer exposes both
// Bifrost metrics (from p.registry) and the Go/process runtime collectors (from p.systemRegistry).
func TestGetMetricsGathererCombinesRegistries(t *testing.T) {
	p := newTestPlugin(t)
	// Record one Bifrost metric so its family is present in the gather output.
	resp := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}}
	resp.PopulateExtraFields(schemas.ChatCompletionRequest, "openai", "m", "m")
	ctx := newHookContext(schemas.ChatCompletionRequest)
	if _, _, err := p.PostLLMHook(ctx, resp, nil); err != nil {
		t.Fatalf("PostLLMHook: %v", err)
	}
	waitForCounter(t, p.registry, "bifrost_input_tokens_total", 1)

	fams, err := p.GetMetricsGatherer().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	present := map[string]bool{}
	for _, mf := range fams {
		present[mf.GetName()] = true
	}
	if !present["bifrost_input_tokens_total"] {
		t.Error("/metrics gatherer missing Bifrost metric bifrost_input_tokens_total")
	}
	if !present["go_goroutines"] {
		t.Error("/metrics gatherer missing Go runtime collector go_goroutines (systemRegistry not combined)")
	}
}

// TestPushGatewayLifecycle covers the push-gateway config plumbing: defaults are applied, the
// running flag toggles, and re-enabling replaces the previous pusher cleanly.
func TestPushGatewayLifecycle(t *testing.T) {
	p := newTestPlugin(t)
	if p.IsPushGatewayRunning() {
		t.Fatal("push gateway should not be running before EnablePushGateway")
	}

	cfg := &PushGatewayConfig{
		Enabled:        true,
		PushGatewayURL: schemas.NewSecretVar("http://127.0.0.1:0"), // never actually reached in this test
	}
	if err := p.EnablePushGateway(cfg); err != nil {
		t.Fatalf("EnablePushGateway: %v", err)
	}
	defer p.DisablePushGateway()

	if !p.IsPushGatewayRunning() {
		t.Error("push gateway should be running after EnablePushGateway")
	}
	got := p.GetPushGatewayConfig()
	if got.JobName != "bifrost" {
		t.Errorf("default JobName = %q, want bifrost", got.JobName)
	}
	if got.PushInterval != 15 {
		t.Errorf("default PushInterval = %d, want 15", got.PushInterval)
	}
	if got.InstanceID == "" {
		t.Error("default InstanceID should be the hostname, got empty")
	}

	// Re-enable must stop the previous loop and start a new one without leaking / hanging.
	if err := p.EnablePushGateway(cfg); err != nil {
		t.Fatalf("re-EnablePushGateway: %v", err)
	}
	if !p.IsPushGatewayRunning() {
		t.Error("push gateway should still be running after re-enable")
	}

	p.DisablePushGateway()
	if p.IsPushGatewayRunning() {
		t.Error("push gateway should be stopped after DisablePushGateway")
	}
}

// TestPushGatewayPushesBifrostButNotRuntimeCollectors stands up a fake push gateway and asserts
// the initial push carries Bifrost metrics but NOT the Go/process runtime collectors — the
// documented reason those live in a separate registry (they would collide with the gateway's own
// go_/process_ series). This exercises the real push path end-to-end without a live gateway.
func TestPushGatewayPushesBifrostButNotRuntimeCollectors(t *testing.T) {
	p := newTestPlugin(t)

	// Record a Bifrost metric so the push has a non-trivial payload.
	resp := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	}}
	resp.PopulateExtraFields(schemas.ChatCompletionRequest, "openai", "m", "m")
	if _, _, err := p.PostLLMHook(newHookContext(schemas.ChatCompletionRequest), resp, nil); err != nil {
		t.Fatalf("PostLLMHook: %v", err)
	}
	waitForCounter(t, p.registry, "bifrost_input_tokens_total", 3)

	bodies := make(chan []byte, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case bodies <- body:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &PushGatewayConfig{
		Enabled:        true,
		PushGatewayURL: schemas.NewSecretVar(srv.URL),
		PushInterval:   3600, // long, so only the immediate initial push fires during the test
	}
	if err := p.EnablePushGateway(cfg); err != nil {
		t.Fatalf("EnablePushGateway: %v", err)
	}
	defer p.DisablePushGateway()

	select {
	case body := <-bodies:
		if !bytes.Contains(body, []byte("bifrost_input_tokens_total")) {
			t.Error("pushed payload missing Bifrost metric bifrost_input_tokens_total")
		}
		if bytes.Contains(body, []byte("go_goroutines")) || bytes.Contains(body, []byte("process_cpu_seconds_total")) {
			t.Error("pushed payload unexpectedly contains Go/process runtime collectors (should be push-excluded)")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fake push gateway received no push within 5s")
	}
}

func boolPtr(b bool) *bool { return &b }
