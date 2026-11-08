// Package atr is a Bifrost plugin that screens LLM requests with Agent Threat
// Rules (ATR), an open detection-rule standard for AI-agent / LLM / MCP threats.
//
// The plugin calls an OpenAI-compatible /v1/moderations endpoint backed by ATR
// (e.g. `python -m pyatr.moderation` from pyatr 0.5.0+), so the gateway stays
// language-agnostic — no Go port of the ATR engine is required. When the endpoint flags
// the prompt, the request is short-circuited before the provider call.
package atr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// PluginName is the registered name of the ATR plugin.
const PluginName = "atr"

// maxModerationResponseBytes bounds how much of the moderation response is read.
const maxModerationResponseBytes = 1 << 20

// Plugin satisfies the Bifrost LLM plugin interface.
var _ schemas.LLMPlugin = (*Plugin)(nil)

// Config configures the ATR plugin.
type Config struct {
	// Endpoint is the OpenAI-compatible /v1/moderations URL backed by ATR
	// (e.g. "http://localhost:8000/v1/moderations").
	Endpoint string `json:"endpoint"`
	// FailClosed blocks the request when the moderation endpoint is unreachable.
	// Default false (fail open) keeps the gateway available if ATR is down.
	FailClosed bool `json:"fail_closed"`
}

// warnLogger is the part of schemas.Logger the plugin uses.
type warnLogger interface {
	Warn(msg string, args ...any)
}

// noopLogger is used when no logger is given.
type noopLogger struct{}

func (noopLogger) Warn(string, ...any) {}

// Plugin screens requests against an ATR moderation endpoint.
type Plugin struct {
	endpoint   string
	client     *http.Client
	failClosed bool
	logger     warnLogger
}

// Init constructs the ATR plugin from config. logger may be nil.
func Init(config *Config, logger schemas.Logger) (schemas.LLMPlugin, error) {
	if config == nil || strings.TrimSpace(config.Endpoint) == "" {
		return nil, errors.New("atr: config.endpoint is required")
	}
	p := New(config.Endpoint, config.FailClosed)
	if logger != nil {
		p.logger = logger
	}
	return p, nil
}

// New returns an ATR plugin that calls the given OpenAI-compatible
// /v1/moderations endpoint (e.g. "http://localhost:8000/v1/moderations").
func New(endpoint string, failClosed bool) *Plugin {
	return &Plugin{
		endpoint:   endpoint,
		failClosed: failClosed,
		client:     &http.Client{Timeout: 10 * time.Second},
		logger:     noopLogger{},
	}
}

func (p *Plugin) GetName() string { return PluginName }

func (p *Plugin) Cleanup() error { return nil }

// PreRequestHook does not participate in routing.
func (p *Plugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	return nil
}

// PreLLMHook scans the chat prompt and short-circuits with a 403 when ATR flags it.
func (p *Plugin) PreLLMHook(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostRequest,
) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	text := promptText(req)
	if strings.TrimSpace(text) == "" {
		return req, nil, nil
	}

	// Propagate the request's cancellation/deadline into the moderation call.
	// *BifrostContext implements context.Context; guard the nil pointer.
	reqCtx := context.Background()
	if ctx != nil {
		reqCtx = ctx
	}

	flagged, categories, err := p.moderate(reqCtx, text)
	if err != nil {
		// The error never carries the prompt; it may carry the endpoint URL, which is config.
		p.logger.Warn("atr: moderation request failed (fail_closed=%t): %v", p.failClosed, err)
		if p.failClosed {
			return req, blockShortCircuit("Agent Threat Rules moderation endpoint unreachable"), nil
		}
		return req, nil, nil // fail open
	}
	if flagged {
		msg := "Blocked by Agent Threat Rules"
		if categories != "" {
			msg += ": " + categories
		}
		return req, blockShortCircuit(msg), nil
	}
	return req, nil, nil
}

// PostLLMHook is a pass-through.
func (p *Plugin) PostLLMHook(
	ctx *schemas.BifrostContext,
	resp *schemas.BifrostResponse,
	bifrostErr *schemas.BifrostError,
) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

func blockShortCircuit(message string) *schemas.LLMPluginShortCircuit {
	code := http.StatusForbidden
	return &schemas.LLMPluginShortCircuit{
		Error: &schemas.BifrostError{
			StatusCode:     &code,
			IsBifrostError: true,
			Error:          &schemas.ErrorField{Message: message},
		},
	}
}

// promptText flattens the chat request's messages and function tool
// descriptions into scannable text. Tool descriptions reach the provider and
// can carry tool-poisoning instructions, so they are screened with the prompt.
func promptText(req *schemas.BifrostRequest) string {
	if req == nil || req.ChatRequest == nil {
		return ""
	}
	var sb strings.Builder
	for _, msg := range req.ChatRequest.Input {
		if msg.Content == nil {
			continue
		}
		if msg.Content.ContentStr != nil {
			sb.WriteString(*msg.Content.ContentStr)
			sb.WriteString("\n")
			continue
		}
		for _, block := range msg.Content.ContentBlocks {
			if block.Text != nil {
				sb.WriteString(*block.Text)
				sb.WriteString("\n")
			}
		}
	}
	if req.ChatRequest.Params != nil {
		for _, tool := range req.ChatRequest.Params.Tools {
			if tool.Function != nil && tool.Function.Description != nil {
				sb.WriteString(*tool.Function.Description)
				sb.WriteString("\n")
			}
		}
	}
	return sb.String()
}

type moderationResponse struct {
	Results []struct {
		Flagged    *bool           `json:"flagged"`
		Categories map[string]bool `json:"categories"`
	} `json:"results"`
}

// moderate POSTs the text to the ATR moderation endpoint and reports whether it
// was flagged plus the truthy category names. A non-2xx status or a malformed
// (resultless) body is returned as an error so the caller's fail-closed policy
// applies rather than silently allowing the request; so is a result whose
// flagged value is missing or null.
func (p *Plugin) moderate(ctx context.Context, text string) (bool, string, error) {
	payload, err := json.Marshal(map[string]string{"input": text})
	if err != nil {
		return false, "", err
	}

	httpReq, err := http.NewRequestWithContext(
		ctx, http.MethodPost, p.endpoint, bytes.NewReader(payload),
	)
	if err != nil {
		return false, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return false, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, "", fmt.Errorf("atr: moderation endpoint returned status %d", resp.StatusCode)
	}

	var out moderationResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxModerationResponseBytes)).Decode(&out); err != nil {
		return false, "", err
	}
	if len(out.Results) == 0 {
		return false, "", errors.New("atr: moderation response contained no results")
	}
	if out.Results[0].Flagged == nil {
		return false, "", errors.New("atr: moderation result has no flagged value")
	}
	if !*out.Results[0].Flagged {
		return false, "", nil
	}

	var categories []string
	for name, truthy := range out.Results[0].Categories {
		if truthy {
			categories = append(categories, name)
		}
	}
	sort.Strings(categories)
	return true, strings.Join(categories, ", "), nil
}
