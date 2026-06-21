// Package atr is a Bifrost plugin that screens LLM requests with Agent Threat
// Rules (ATR), an open detection-rule standard for AI-agent / LLM / MCP threats.
//
// The plugin calls an OpenAI-compatible /v1/moderations endpoint backed by ATR
// (e.g. `pyatr.adapters.openai_moderation`), so the gateway stays language-
// agnostic — no Go port of the ATR engine is required. When the endpoint flags
// the prompt, the request is short-circuited before the provider call.
package atr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// PluginName is the registered name of the ATR plugin.
const PluginName = "atr"

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

// Plugin screens requests against an ATR moderation endpoint.
type Plugin struct {
	endpoint   string
	client     *http.Client
	failClosed bool
}

// Init constructs the ATR plugin from config (matches the Bifrost built-in
// plugin convention used by InstantiatePlugin).
func Init(config *Config) (schemas.LLMPlugin, error) {
	if config == nil || strings.TrimSpace(config.Endpoint) == "" {
		return nil, errors.New("atr: config.endpoint is required")
	}
	return New(config.Endpoint, config.FailClosed), nil
}

// New returns an ATR plugin that calls the given OpenAI-compatible
// /v1/moderations endpoint (e.g. "http://localhost:8000/v1/moderations").
func New(endpoint string, failClosed bool) *Plugin {
	return &Plugin{
		endpoint:   endpoint,
		failClosed: failClosed,
		client:     &http.Client{Timeout: 10 * time.Second},
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

	flagged, categories, err := p.moderate(text)
	if err != nil {
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

// promptText flattens the chat request's messages into scannable text.
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
		}
		for _, block := range msg.Content.ContentBlocks {
			if block.Text != nil {
				sb.WriteString(*block.Text)
				sb.WriteString("\n")
			}
		}
	}
	return sb.String()
}

type moderationResponse struct {
	Results []struct {
		Flagged    bool            `json:"flagged"`
		Categories map[string]bool `json:"categories"`
	} `json:"results"`
}

// moderate POSTs the text to the ATR moderation endpoint and reports whether it
// was flagged plus the truthy category names.
func (p *Plugin) moderate(text string) (bool, string, error) {
	payload, err := json.Marshal(map[string]string{"input": text})
	if err != nil {
		return false, "", err
	}

	httpReq, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost, p.endpoint, bytes.NewReader(payload),
	)
	if err != nil {
		return false, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()

	var out moderationResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, "", err
	}
	if len(out.Results) == 0 || !out.Results[0].Flagged {
		return false, "", nil
	}

	var categories []string
	for name, truthy := range out.Results[0].Categories {
		if truthy {
			categories = append(categories, name)
		}
	}
	return true, strings.Join(categories, ", "), nil
}
