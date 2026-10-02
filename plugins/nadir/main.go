// Package nadir routes a request to a model tier chosen by Nadir's decision API.
//
// This is a decision-only integration: the plugin asks Nadir's /v1/bucket endpoint which
// complexity tier a request belongs to and rewrites req.Provider/req.Model to that tier's
// configured model. Everything else stays on Bifrost. The provider call, the keys, the
// fallback chain, governance, caching and telemetry are untouched, and Nadir sees only the
// text it is asked to classify, never the completion.
//
// The plugin acts on exactly one model name (config `trigger_model`, default "nadir-auto"),
// so a caller that names a real model keeps it. A request that names the trigger always
// leaves with a real model on it: a classification that fails, times out, or comes back with
// a tier the config does not define routes to `fallback_model` instead. PreRequestHook errors
// are non-blocking in Bifrost, so nothing here can fail a request.
package nadir

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	// PluginName is the name this plugin registers under.
	PluginName = "nadir"

	// RoutingEngineNadir labels this plugin's decisions in the per-request routing log.
	RoutingEngineNadir = "nadir"

	// DefaultBaseURL is Nadir's hosted decision API.
	DefaultBaseURL = "https://api.getnadir.com"

	// DefaultTriggerModel is the model name that asks for a routed decision.
	DefaultTriggerModel = "nadir-auto"

	// DefaultTimeout bounds the classification call. Past it the request routes to
	// fallback_model rather than waiting.
	DefaultTimeout = 3 * time.Second

	bucketPath = "/v1/bucket"
)

// supportedBuckets are the tiers Nadir grades. A key outside this set is rejected at Init:
// the bucket would never come back, so a typo like "simlpe" would leave that tier silently
// unreachable and quietly serve fallback_model for the traffic it was meant to catch.
var supportedBuckets = []string{"simple", "medium", "complex"}

// Config is the plugin's configuration block.
type Config struct {
	// APIKey attributes decisions to a Nadir account and lifts the anonymous rate limit.
	// Falls back to NADIR_API_KEY. Optional: the endpoint answers without one.
	APIKey string `json:"api_key,omitempty"`
	// BaseURL points at a self-hosted Nadir. Falls back to NADIR_API_BASE, then the hosted API.
	BaseURL string `json:"base_url,omitempty"`
	// TriggerModel is the model name that opts a request into routing. Default "nadir-auto".
	TriggerModel string `json:"trigger_model,omitempty"`
	// Tiers maps a Nadir bucket (simple, medium, complex) to "provider/model". Required.
	Tiers map[string]string `json:"tiers"`
	// FallbackModel is "provider/model" used whenever a tier cannot be decided. Required,
	// because the trigger model is not a real model: without it a failed classification
	// would leave the request pointing at a name no provider serves.
	FallbackModel string `json:"fallback_model"`
	// TimeoutMs bounds the classification call. Default 3000.
	TimeoutMs int `json:"timeout_ms,omitempty"`
}

// target is a parsed "provider/model" pair.
type target struct {
	provider schemas.ModelProvider
	model    string
}

func (t target) String() string { return string(t.provider) + "/" + t.model }

// Plugin implements schemas.BasePlugin and the PreRequestHook routing phase.
type Plugin struct {
	url          string
	apiKey       string
	triggerModel string
	tiers        map[string]target
	fallback     target
	client       *http.Client
	logger       schemas.Logger
}

// Init validates the configuration and returns the plugin.
//
// Everything that can be wrong is rejected here rather than on the first routed request:
// a tier or fallback naming an unknown provider is a config error the operator can fix at
// boot, and a request-time failure would be indistinguishable from Nadir being down.
func Init(cfg *Config, logger schemas.Logger) (*Plugin, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%s: config is required", PluginName)
	}
	if len(cfg.Tiers) == 0 {
		return nil, fmt.Errorf("%s: tiers is required, e.g. {\"simple\": \"openai/gpt-4o-mini\"}", PluginName)
	}
	tiers := make(map[string]target, len(cfg.Tiers))
	for bucket, model := range cfg.Tiers {
		name := strings.ToLower(strings.TrimSpace(bucket))
		if !slices.Contains(supportedBuckets, name) {
			return nil, fmt.Errorf("%s: tier %q is not a bucket Nadir returns; use one of %s",
				PluginName, bucket, strings.Join(supportedBuckets, ", "))
		}
		parsed, err := parseTarget(model)
		if err != nil {
			return nil, fmt.Errorf("%s: tier %q: %w", PluginName, bucket, err)
		}
		tiers[name] = parsed
	}
	if cfg.FallbackModel == "" {
		return nil, fmt.Errorf("%s: fallback_model is required, so a failed classification still has somewhere to go", PluginName)
	}
	fallback, err := parseTarget(cfg.FallbackModel)
	if err != nil {
		return nil, fmt.Errorf("%s: fallback_model: %w", PluginName, err)
	}
	timeout := DefaultTimeout
	if cfg.TimeoutMs > 0 {
		timeout = time.Duration(cfg.TimeoutMs) * time.Millisecond
	}
	triggerModel := cfg.TriggerModel
	if triggerModel == "" {
		triggerModel = DefaultTriggerModel
	}
	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("NADIR_API_KEY")
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = os.Getenv("NADIR_API_BASE")
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Plugin{
		url:          bucketURL(baseURL),
		apiKey:       apiKey,
		triggerModel: strings.ToLower(triggerModel),
		tiers:        tiers,
		fallback:     fallback,
		client:       &http.Client{Timeout: timeout},
		logger:       logger,
	}, nil
}

// GetName implements schemas.BasePlugin.
func (p *Plugin) GetName() string { return PluginName }

// Cleanup implements schemas.BasePlugin.
func (p *Plugin) Cleanup() error {
	p.client.CloseIdleConnections()
	return nil
}

// PreRequestHook rewrites the request to the tier Nadir picks, for requests that asked for it.
//
// Runs in the phase whose mutations are committed before any fan-out, so the model chosen here
// is what every later plugin, the provider call and every fallback attempt sees.
func (p *Plugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	if req == nil || req.RequestType == schemas.PassthroughRequest || req.RequestType == schemas.PassthroughStreamRequest {
		return nil
	}
	_, model, _ := req.GetRequestFields()
	if !p.isTrigger(model) {
		return nil
	}

	messages := extractMessages(req)
	if len(messages) == 0 {
		p.route(ctx, req, p.fallback, "no classifiable text in the request")
		return nil
	}
	// ctx is a typed pointer, so handing it straight to classify would pass a non-nil
	// context.Context wrapping a nil pointer and panic on the first Deadline() call.
	var callCtx context.Context = context.Background()
	if ctx != nil {
		callCtx = ctx
	}
	bucket, err := p.classify(callCtx, messages)
	if err != nil {
		p.route(ctx, req, p.fallback, fmt.Sprintf("classification failed (%v)", err))
		return nil
	}
	tier, ok := p.tiers[bucket]
	if !ok {
		p.route(ctx, req, p.fallback, fmt.Sprintf("bucket %q has no tier configured", bucket))
		return nil
	}
	p.route(ctx, req, tier, fmt.Sprintf("bucket %q", bucket))
	return nil
}

// isTrigger reports whether this request asked to be routed. A provider prefix is tolerated,
// so both "nadir-auto" and "openai/nadir-auto" opt in.
func (p *Plugin) isTrigger(model string) bool {
	if model == "" {
		return false
	}
	_, bare := schemas.ParseModelString(model, "")
	return strings.EqualFold(strings.TrimSpace(bare), p.triggerModel)
}

// route commits the decision to the request and records why it was made.
func (p *Plugin) route(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, t target, reason string) {
	req.SetProvider(t.provider)
	req.SetModel(t.model)
	level := schemas.LogLevelInfo
	if t == p.fallback {
		// Every fallback is a decision that did not happen; say so at a level an
		// operator sees, since a router silently serving one tier looks like working
		// software until the bill arrives.
		level = schemas.LogLevelWarn
	}
	if ctx != nil {
		ctx.AppendRoutingEngineLog(RoutingEngineNadir, level, fmt.Sprintf("Nadir routed to %s: %s", t, reason))
		schemas.AppendToContextList(ctx, schemas.BifrostContextKeyRoutingEnginesUsed, RoutingEngineNadir)
	}
}

// bucketResponse is the part of /v1/bucket's answer this plugin reads.
type bucketResponse struct {
	Bucket string `json:"bucket"`
}

// classify asks Nadir which bucket these messages belong to.
func (p *Plugin) classify(ctx context.Context, messages []map[string]string) (string, error) {
	body, err := json.Marshal(map[string]any{"messages": messages, "source": "bifrost"})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		request.Header.Set("X-API-Key", p.apiKey)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close() //nolint:errcheck // read-only body
	if response.StatusCode != http.StatusOK {
		// The body carries Nadir's own reason (a rate limit, a rejected key); truncated
		// because it lands in a log line, not a debugger.
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 256))
		return "", fmt.Errorf("status %d: %s", response.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var decoded bucketResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return "", err
	}
	bucket := strings.ToLower(strings.TrimSpace(decoded.Bucket))
	if bucket == "" {
		return "", fmt.Errorf("response carried no bucket")
	}
	return bucket, nil
}

// extractMessages pulls the text Nadir classifies out of whichever request shape arrived.
//
// Chat, Responses and text completions. Every other request type (embeddings, speech, images...)
// has no complexity tier to choose and routes to fallback_model, which is also what a caller
// naming the trigger model on one of those endpoints should get.
func extractMessages(req *schemas.BifrostRequest) []map[string]string {
	switch {
	case req.ChatRequest != nil:
		messages := make([]map[string]string, 0, len(req.ChatRequest.Input))
		for _, message := range req.ChatRequest.Input {
			text := messageText(message.Content)
			if text == "" {
				continue
			}
			messages = append(messages, map[string]string{"role": string(message.Role), "content": text})
		}
		return messages
	case req.ResponsesRequest != nil:
		messages := make([]map[string]string, 0, len(req.ResponsesRequest.Input))
		for _, message := range req.ResponsesRequest.Input {
			text := responsesText(message.Content)
			if text == "" {
				continue
			}
			// Role is optional on a Responses item (a bare input item carries none), and an
			// item with text but no role is still the caller's ask, so it is classified as one.
			role := "user"
			if message.Role != nil {
				role = string(*message.Role)
			}
			messages = append(messages, map[string]string{"role": role, "content": text})
		}
		return messages
	case req.TextCompletionRequest != nil && req.TextCompletionRequest.Input != nil:
		input := req.TextCompletionRequest.Input
		if input.PromptStr != nil && *input.PromptStr != "" {
			return []map[string]string{{"role": "user", "content": *input.PromptStr}}
		}
		if joined := strings.TrimSpace(strings.Join(input.PromptArray, "\n")); joined != "" {
			return []map[string]string{{"role": "user", "content": joined}}
		}
	}
	return nil
}

// messageText flattens a message's content to the text a classifier can read, dropping
// image, audio and file blocks.
func messageText(content *schemas.ChatMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return strings.TrimSpace(*content.ContentStr)
	}
	parts := make([]string, 0, len(content.ContentBlocks))
	for _, block := range content.ContentBlocks {
		if block.Text != nil && *block.Text != "" {
			parts = append(parts, *block.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// responsesText is messageText for the Responses API's own content shape.
func responsesText(content *schemas.ResponsesMessageContent) string {
	if content == nil {
		return ""
	}
	if content.ContentStr != nil {
		return strings.TrimSpace(*content.ContentStr)
	}
	parts := make([]string, 0, len(content.ContentBlocks))
	for _, block := range content.ContentBlocks {
		if block.Text != nil && *block.Text != "" {
			parts = append(parts, *block.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// parseTarget splits "provider/model" into its parts, rejecting anything without a known
// provider prefix: an unprefixed model here would route to whatever the catalog resolves
// later, which is not the deterministic tier the operator configured.
func parseTarget(raw string) (target, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return target{}, fmt.Errorf("model is empty")
	}
	provider, model := schemas.ParseModelString(trimmed, "")
	if provider == "" || model == "" {
		return target{}, fmt.Errorf("%q must be \"provider/model\" with a known provider, e.g. \"openai/gpt-4o-mini\"", raw)
	}
	return target{provider: provider, model: model}, nil
}

// bucketURL joins the endpoint path to a host, tolerating a base that already ends in /v1.
//
// Nadir's own docs advertise https://api.getnadir.com/v1 as the base URL, because the
// OpenAI-compatible clients that consume it append /chat/completions. An operator copying
// that value into base_url would otherwise send /v1/v1/bucket and get a 404 that reads like
// the endpoint does not exist.
func bucketURL(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	trimmed = strings.TrimSuffix(trimmed, "/v1")
	return trimmed + bucketPath
}
