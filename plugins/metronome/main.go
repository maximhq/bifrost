// Package metronome exports Bifrost usage to Metronome.
package metronome

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
)

type Config struct {
	APIKey *schemas.SecretVar `json:"api_key,omitempty"`
	// Keys are Bifrost provider/model; values match the Metronome rate card exactly.
	ModelMapping    map[string]string `json:"model_mapping"`
	ProviderMapping map[string]string `json:"provider_mapping"`
}

type Event[T any] struct {
	TransactionID string `json:"transaction_id"`
	CustomerID    string `json:"customer_id"`
	EventType     string `json:"event_type"`
	Timestamp     string `json:"timestamp"`
	Properties    T      `json:"properties"`
}

type TokenUsage struct {
	Model             string `json:"model"`
	Provider          string `json:"provider"`
	InputTokens       int    `json:"input_tokens"`
	OutputTokens      int    `json:"output_tokens"`
	CachedInputTokens int    `json:"cached_input_tokens"`
	CachedWriteTokens int    `json:"cached_write_tokens"`
	// RequestType is set only for decision events ("decisions") so billable
	// metrics can tell them apart; generation events keep their existing shape.
	RequestType string `json:"request_type,omitempty"`
}

type requestKey string

const attemptKey requestKey = "metronome-attempt-id"

const PluginName = "metronome"

const defaultIngestURL = "https://api.metronome.com/v1/ingest"

var _ schemas.LLMPlugin = (*Plugin)(nil)

type Plugin struct {
	logger schemas.Logger
	config Config
	apiKey string
	client *http.Client
	queue  chan Event[TokenUsage]
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.RWMutex
	closed bool

	// Fixed by Init; package tests can point at a local server. Not a config option.
	ingestURL string
}

func (p *Plugin) GetName() string { return PluginName }

func (c *Config) UnmarshalJSON(data []byte) error {
	type plain Config
	value := plain{}
	// Reject removed settings instead of silently changing an existing
	// installation's delivery behavior or billing identity.
	input := struct {
		*plain
		DryRun            json.RawMessage `json:"dry_run"`
		CustomerMapping   json.RawMessage `json:"customer_mapping"`
		DefaultCustomerID json.RawMessage `json:"default_customer_id"`
	}{plain: &value}
	if err := json.Unmarshal(data, &input); err != nil {
		return err
	}
	if len(input.DryRun) != 0 {
		return fmt.Errorf("metronome dry_run is no longer supported: remove it and set enabled to false to stop delivery or true to send usage")
	}
	if len(input.CustomerMapping) != 0 || len(input.DefaultCustomerID) != 0 {
		return fmt.Errorf("metronome customer_mapping and default_customer_id are no longer supported: register authenticated virtual-key UUIDs as Metronome ingest aliases and remove both settings")
	}
	*c = Config(value)
	return nil
}

func Init(config *Config, logger schemas.Logger) (*Plugin, error) {
	cfg := Config{}
	if config != nil {
		cfg = *config
	}
	cfg.ModelMapping = maps.Clone(cfg.ModelMapping)
	cfg.ProviderMapping = maps.Clone(cfg.ProviderMapping)
	if cfg.APIKey != nil && (!cfg.APIKey.IsFromEnv() || !strings.HasPrefix(cfg.APIKey.GetRawRef(), "env.")) {
		return nil, fmt.Errorf("metronome api_key must be an env.VARIABLE_NAME reference")
	}
	key := strings.TrimSpace(cfg.APIKey.GetValue())
	if key == "" {
		return nil, fmt.Errorf("metronome api_key is required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Plugin{ingestURL: defaultIngestURL, config: cfg, logger: logger, apiKey: key, queue: make(chan Event[TokenUsage], 1000), ctx: ctx, cancel: cancel,
		client: &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	p.wg.Add(1)
	go p.run()
	return p, nil
}

func (p *Plugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *Plugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	// A small scalar only: no request or stream content is retained in context.
	if ctx != nil {
		ctx.SetValue(attemptKey, uuid.NewString())
	}
	return req, nil, nil
}

func (p *Plugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, upstreamErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if p == nil || ctx == nil || resp == nil || upstreamErr != nil {
		return resp, upstreamErr, nil
	}
	extra := resp.GetExtraFields()
	if extra == nil || (extra.CacheDebug != nil && extra.CacheDebug.CacheHit) {
		return resp, upstreamErr, nil
	}
	switch extra.RequestType {
	case schemas.ChatCompletionStreamRequest, schemas.TextCompletionStreamRequest, schemas.ResponsesStreamRequest:
		final, _ := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator).(bool)
		if !final {
			return resp, upstreamErr, nil
		}
	case schemas.ChatCompletionRequest, schemas.TextCompletionRequest, schemas.ResponsesRequest:
	case schemas.DecisionRequest:
	default:
		return resp, upstreamErr, nil
	}
	usage, ok := extractUsage(resp)
	if !ok {
		return resp, upstreamErr, nil
	}
	if extra.RequestType == schemas.DecisionRequest {
		usage.RequestType = string(schemas.DecisionRequest)
		// TypeSafe-family endpoints charge input tokens only; their output
		// tokens are answer bookkeeping, not generated output. A decision
		// emulated through an LLM keeps the output tokens that LLM generated
		// and its provider bills.
		if schemas.ResolveBaseProvider(ctx, extra.Provider) == schemas.Typesafe {
			usage.OutputTokens = 0
		}
	}
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CachedInputTokens < 0 || usage.CachedWriteTokens < 0 {
		p.logger.Warn("[metronome] skipped inconsistent token counts")
		return resp, upstreamErr, nil
	}
	if usage.InputTokens+usage.OutputTokens+usage.CachedInputTokens+usage.CachedWriteTokens == 0 {
		return resp, upstreamErr, nil
	}
	customer, _ := ctx.Value(schemas.BifrostContextKeyGovernanceVirtualKeyID).(string)
	if strings.TrimSpace(customer) == "" {
		p.logger.Warn("[metronome] skipped usage: missing authenticated virtual-key ID")
		return resp, upstreamErr, nil
	}
	provider, model := string(extra.RoutingInfo.Provider), extra.RoutingInfo.Model
	if provider == "" {
		provider = string(extra.Provider)
	}
	if alias := extra.RoutingInfo.ResolvedKeyAlias; alias != nil {
		model = alias.ModelID
	}
	if model == "" {
		model = extra.ResolvedModelUsed
	}
	if model == "" {
		model = extra.OriginalModelRequested
	}
	if served := extra.RoutingInfo.ServerSideFallbackModel; served != nil && *served != "" {
		model = *served
	}
	if provider == "" || model == "" {
		return resp, upstreamErr, nil
	}
	usage.Provider = provider
	if mapped := p.config.ProviderMapping[provider]; mapped != "" {
		usage.Provider = mapped
	}
	usage.Model = model
	if mapped := p.config.ModelMapping[provider+"/"+model]; mapped != "" {
		usage.Model = mapped
	} else if !strings.Contains(model, "/") {
		// For hosted/cloud/custom providers, configure model_mapping explicitly.
		usage.Model = usage.Provider + "/" + model
	}
	attempt, _ := ctx.Value(attemptKey).(string)
	if attempt == "" {
		p.logger.Warn("[metronome] skipped usage: missing pre-hook attempt ID")
		return resp, upstreamErr, nil
	}
	retries, _ := ctx.Value(schemas.BifrostContextKeyNumberOfRetries).(int)
	id := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s:%s", attempt, retries, provider, model)))
	transactionID := fmt.Sprintf("bifrost-%x", id)
	// A System One decision with an Idempotency-Key is billed at most once per
	// (virtual key, provider, key): a replay whose first event was delivered
	// is deduplicated by Metronome, while a replay whose first response was
	// lost is still billed because no event exists yet. Without a key, every
	// attempt keeps its own ID.
	if extra.RequestType == schemas.DecisionRequest && schemas.ResolveBaseProvider(ctx, extra.Provider) == schemas.Typesafe {
		if key := schemas.DecisionIdempotencyKey(ctx); key != "" {
			transactionID = "bifrost-idem-" + schemas.DecisionIdempotencyScope(customer, provider, model, key)
		}
	}
	event := Event[TokenUsage]{TransactionID: transactionID, CustomerID: customer, EventType: "token-billing", Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Properties: usage}
	// Copy scalar usage before returning: Bifrost may release pooled responses.
	p.mu.RLock()
	if !p.closed {
		select {
		case p.queue <- event:
		default:
			p.logger.Warn("[metronome] queue full; dropped transaction=%s", event.TransactionID)
		}
	}
	p.mu.RUnlock()
	return resp, upstreamErr, nil
}

func extractUsage(resp *schemas.BifrostResponse) (TokenUsage, bool) {
	var chat *schemas.BifrostLLMUsage
	var responses *schemas.ResponsesResponseUsage
	switch {
	case resp.ChatResponse != nil:
		chat = resp.ChatResponse.Usage
	case resp.TextCompletionResponse != nil:
		chat = resp.TextCompletionResponse.Usage
	case resp.DecisionResponse != nil:
		// Nil for a free idempotent replay, which is then not exported.
		chat = resp.DecisionResponse.Usage
	case resp.ResponsesResponse != nil:
		responses = resp.ResponsesResponse.Usage
	case resp.ResponsesStreamResponse != nil && resp.ResponsesStreamResponse.Response != nil:
		responses = resp.ResponsesStreamResponse.Response.Usage
	}
	u := TokenUsage{}
	if chat != nil {
		u.InputTokens, u.OutputTokens = chat.PromptTokens, chat.CompletionTokens
		if d := chat.PromptTokensDetails; d != nil {
			u.CachedInputTokens, u.CachedWriteTokens = d.CachedReadTokens, d.CachedWriteTokens
		}
	} else if responses != nil {
		u.InputTokens, u.OutputTokens = responses.InputTokens, responses.OutputTokens
		if d := responses.InputTokensDetails; d != nil {
			u.CachedInputTokens, u.CachedWriteTokens = d.CachedReadTokens, d.CachedWriteTokens
		}
	} else {
		return u, false
	}
	// Bifrost normalizes input totals to include cache reads and writes. Metronome
	// uses separate metrics, so subtract both to avoid billing them twice.
	u.InputTokens -= u.CachedInputTokens + u.CachedWriteTokens
	return u, true
}

func (p *Plugin) run() {
	defer p.wg.Done()
	for event := range p.queue {
		if p.ctx.Err() != nil {
			p.logger.Warn("[metronome] shutdown deadline reached; pending events dropped")
			return
		}
		payload, err := schemas.MarshalSorted([]Event[TokenUsage]{event})
		if err != nil {
			p.logger.Warn("[metronome] unable to encode event")
			continue
		}
		if err := p.send(p.ctx, event.TransactionID, payload); err != nil {
			p.logger.Warn("[metronome] dropped transaction=%s: %v", event.TransactionID, err)
		}
	}
}

// send retries the identical serialized payload. External usage propagates errors
// to its caller; token usage still uses its background queue.
func (p *Plugin) send(ctx context.Context, id string, payload []byte) error {
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.ingestURL, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("create ingest request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := p.client.Do(req)
		status := 0
		if resp != nil {
			status = resp.StatusCode
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
		}
		if err == nil && status == http.StatusOK {
			p.logger.Info("[metronome] accepted transaction=%s", id)
			return nil
		}
		// Keep upstream bodies, credentials and network error URLs out of logs.
		p.logger.Warn("[metronome] ingest failed transaction=%s status=%d attempt=%d", id, status, attempt+1)
		if err == nil && status != http.StatusTooManyRequests && status < 500 {
			return fmt.Errorf("metronome rejected ingestion (HTTP %d)", status)
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
			}
		}
	}
	return fmt.Errorf("metronome retry limit reached")
}

func (p *Plugin) Cleanup() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.queue)
	}
	p.mu.Unlock()
	deadline := time.AfterFunc(5*time.Second, p.cancel)
	p.wg.Wait()
	deadline.Stop()
	p.cancel()
	p.client.CloseIdleConnections()
	return nil
}
