package clis

import (
	"fmt"
	"os"
	"strings"
)

// CLI describes how to drive a coding-assistant CLI in non-interactive mode.
// We support two invocation modes:
//
//	single-turn:  one process per prompt, prompt as arg, response on stdout
//	multi-turn:   one long-running process, prompts written to stdin as
//	              JSON-Lines (Claude) or via resume subcommands (Codex/OpenCode),
//	              events read from stdout as JSON-Lines.
//
// The runner picks the mode based on len(scenario.Turns).
type CLI struct {
	ID         string
	Binary     string
	InstallPkg string
	BasePath   string // Bifrost base-path the CLI's wire format targets
	BaseURLEnv string
	APIKeyEnv  string
	ExtraEnv   map[string]string

	// PreLaunch can add per-cell environment or temporary config files.
	PreLaunch func(baseURL, apiKey, model string) ([]string, func(), error)

	// SingleTurnArgs returns the full arg list for one-shot invocation.
	SingleTurnArgs func(model, prompt string) []string

	// MultiTurnDriver returns the multi-turn driver for this CLI.
	MultiTurnDriver func() multiTurnDriver
}

// ModelInfo is one model entry in a provider's catalog.
//
// Capabilities are best-effort flags so scenarios skip cells they can't
// actually exercise.
//
//	ExtendedThinking — budget-controlled thinking (claude --thinking-budget,
//	                   gemini thinking budget, OpenAI reasoning_effort path).
//	AdaptiveThinking — the --effort flag (Anthropic adaptive thinking,
//	                   OpenAI o-series reasoning_effort levels). Some
//	                   thinking-capable models reject the effort parameter
//	                   (e.g. Claude Haiku 4.5).
//	WebSearch        — model has access to a built-in web search tool.
type ModelInfo struct {
	ID               string
	ExtendedThinking bool
	AdaptiveThinking bool
	WebSearch        bool
	Env              map[string]string
}

func isBedrockAnthropicModel(modelID string) bool {
	return strings.Contains(modelID, "anthropic.claude")
}

func isBedrockNovaModel(modelID string) bool {
	return strings.Contains(modelID, "amazon.nova")
}

// Provider describes a Bifrost-configured provider and its model catalog.
//
// Models lists the top chat-capable model IDs we want the harness to exercise.
// Native providers are capped at five entries. Azure, Bedrock, and Vertex also
// include the top five Anthropic models because those clouds can proxy Claude.
type Provider struct {
	ID     string
	Models []ModelInfo
}

// chatModel is the first entry in Models; used as the "default" model when
// scenarios don't have a specific reasoning requirement.
func (p Provider) chatModel() ModelInfo {
	if len(p.Models) == 0 {
		return ModelInfo{}
	}
	return p.Models[0]
}

var clis = map[string]CLI{
	"claude": {
		ID:         "claude",
		Binary:     "claude",
		InstallPkg: "@anthropic-ai/claude-code",
		BasePath:   "/anthropic",
		BaseURLEnv: "ANTHROPIC_BASE_URL",
		APIKeyEnv:  "ANTHROPIC_API_KEY",
		ExtraEnv: map[string]string{
			"CLAUDE_CODE_DISABLE_TELEMETRY": "1",
		},
		SingleTurnArgs: func(model, prompt string) []string {
			// We intentionally do NOT pass --bare. Per the CLI reference,
			// --bare restricts Claude to Bash + file read/edit tools, which
			// kneecaps web-search, MCP, and any other built-in tool. Scripted
			// startup is slower without it but tool fidelity is the point.
			args := []string{"-p", "--dangerously-skip-permissions"}
			if model != "" {
				args = append(args, "--model", model)
			}
			args = append(args, prompt)
			return args
		},
		PreLaunch:       claudePreLaunch,
		MultiTurnDriver: claudeStreamJSONDriver,
	},
	"codex": {
		ID:         "codex",
		Binary:     "codex",
		InstallPkg: "@openai/codex",
		BasePath:   "/openai",
		BaseURLEnv: "OPENAI_BASE_URL",
		APIKeyEnv:  "OPENAI_API_KEY",
		ExtraEnv: map[string]string{
			"CODEX_DISABLE_TELEMETRY": "1",
		},
		SingleTurnArgs: func(model, prompt string) []string {
			args := []string{"exec", "--json", "--skip-git-repo-check"}
			if model != "" {
				args = append(args, "--model", model)
			}
			args = append(args, prompt)
			return args
		},
		MultiTurnDriver: codexResumeDriver,
	},
	"opencode": {
		ID:         "opencode",
		Binary:     "opencode",
		InstallPkg: "opencode-ai",
		BasePath:   "/openai",
		BaseURLEnv: "OPENAI_BASE_URL",
		APIKeyEnv:  "OPENAI_API_KEY",
		ExtraEnv: map[string]string{
			"OPENCODE_DISABLE_AUTOUPDATE": "1",
		},
		SingleTurnArgs: func(model, prompt string) []string {
			args := []string{"run", "--dangerously-skip-permissions", "--format", "json"}
			if model != "" {
				args = append(args, "--model", opencodeModelRef(model))
			}
			args = append(args, prompt)
			return args
		},
		PreLaunch:       opencodePreLaunch,
		MultiTurnDriver: opencodeResumeDriver,
	},
}

// Anthropic — sourced from
// https://platform.claude.com/docs/en/docs/about-claude/models/overview
// (the "Latest models comparison" + "Legacy models" tables explicitly list
// Extended thinking and Adaptive thinking support per model).
var anthropicModels = []ModelInfo{
	{ID: "claude-opus-4-7", ExtendedThinking: false, AdaptiveThinking: true, WebSearch: true},
	{ID: "claude-sonnet-4-6", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
	{ID: "claude-haiku-4-5", ExtendedThinking: true, AdaptiveThinking: false, WebSearch: true},
	{ID: "claude-opus-4-6", ExtendedThinking: true, WebSearch: true},
	{ID: "claude-sonnet-4-5", ExtendedThinking: true, WebSearch: true},
}

// OpenAI — model IDs from the public OpenAI model catalog as of 2026-Q1.
// Refresh against https://platform.openai.com/docs/models when models change.
// AdaptiveThinking maps to the OpenAI `reasoning_effort` parameter (low/
// medium/high), supported on the o-series and gpt-5 reasoning lines.
var openaiModels = []ModelInfo{
	{ID: "gpt-5.2", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
	{ID: "gpt-5.2-pro", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
	{ID: "gpt-5.1", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
	{ID: "gpt-5-mini", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
	{ID: "gpt-4.1", WebSearch: true},
}

// Gemini — sourced from https://ai.google.dev/gemini-api/docs/models
// (fetched 2026-05). Gemini's "thinking budget" maps to ExtendedThinking;
// no native AdaptiveThinking equivalent today, so we leave it false.
var geminiModels = []ModelInfo{
	{ID: "gemini-3-pro-preview", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.5-pro", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.5-flash", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.5-flash-lite", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.0-flash", WebSearch: true}, // deprecated, no thinking
}

var azureModels = append([]ModelInfo{
	{ID: "gpt-5.4", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
	{ID: "gpt-5.4-pro", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
	{ID: "gpt-5.4-mini", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
	{ID: "gpt-5.4-nano", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
	{ID: "gpt-5.3-chat", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
}, anthropicModels...)

var bedrockAnthropicModels = []ModelInfo{
	{ID: "global.anthropic.claude-opus-4-7", AdaptiveThinking: true},
	{ID: "global.anthropic.claude-sonnet-4-6", ExtendedThinking: true, AdaptiveThinking: true},
	{ID: "global.anthropic.claude-haiku-4-5-20251001-v1:0", ExtendedThinking: true},
	{ID: "global.anthropic.claude-opus-4-6-v1", ExtendedThinking: true},
	{ID: "global.anthropic.claude-sonnet-4-5-20250929-v1:0", ExtendedThinking: true},
}

// Bedrock — four native Bedrock models plus the top five Anthropic-on-Bedrock
// IDs. Capability flags mirror the native Anthropic entries where applicable.
var bedrockModels = append([]ModelInfo{
	{ID: "us.amazon.nova-pro-v1:0"},
	{ID: "us.amazon.nova-lite-v1:0"},
	{ID: "meta.llama3-3-70b-instruct-v1:0"},
	{ID: "mistral.mistral-large-2407-v1:0"},
}, bedrockAnthropicModels...)

var vertexAnthropicModels = []ModelInfo{
	{ID: "claude-opus-4-7", AdaptiveThinking: true, Env: vertexRegionEnv("global")},
	{ID: "claude-sonnet-4-6", ExtendedThinking: true, AdaptiveThinking: true, Env: vertexRegionEnv("us-west1")},
	{ID: "claude-haiku-4-5@20251001", ExtendedThinking: true, Env: vertexGlobalEnv()},
	{ID: "claude-opus-4-6", ExtendedThinking: true, Env: vertexRegionEnv("us-west1")},
	{ID: "claude-sonnet-4-5@20250929", ExtendedThinking: true, Env: vertexRegionEnv("us-west1")},
}

// Vertex AI — five native Gemini models plus the top five Anthropic-on-Vertex
// IDs.
var vertexModels = append([]ModelInfo{
	{ID: "gemini-3-pro-preview", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.5-pro", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.5-flash", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.5-flash-lite", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.0-flash", WebSearch: true},
}, vertexAnthropicModels...)

var providers = map[string]Provider{
	"openai":    {ID: "openai", Models: openaiModels},
	"anthropic": {ID: "anthropic", Models: anthropicModels},
	"azure":     {ID: "azure", Models: azureModels},
	"gemini":    {ID: "gemini", Models: geminiModels},
	"bedrock":   {ID: "bedrock", Models: bedrockModels},
	"vertex":    {ID: "vertex", Models: vertexModels},
}

// bifrostModelRef is the provider-prefixed model id Bifrost expects when
// routing across the wire-format boundary.
func bifrostModelRef(provider, model string) string {
	return provider + "/" + model
}

func vertexGlobalEnv() map[string]string {
	return vertexRegionEnv("global")
}

func vertexRegionEnv(region string) map[string]string {
	return map[string]string{"CLOUD_ML_REGION": region}
}

func claudePreLaunch(_, _, model string) ([]string, func(), error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, func() {}, nil
	}
	return []string{
		"ANTHROPIC_DEFAULT_SONNET_MODEL=" + model,
		"ANTHROPIC_DEFAULT_OPUS_MODEL=" + model,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=" + model,
	}, func() {}, nil
}

func opencodePreLaunch(baseURL, apiKey, model string) ([]string, func(), error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, func() {}, nil
	}
	modelRef := opencodeModelRef(model)
	cfg := fmt.Sprintf(`{
  "$schema": "https://opencode.ai/config.json",
  "model": %q,
  "provider": {
    "bifrost": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Bifrost",
      "options": {
        "baseURL": %q,
        "apiKey": %q
      },
      "models": {
        %q: {
          "name": %q
        }
      }
    }
  }
}`, modelRef, strings.TrimSpace(baseURL), strings.TrimSpace(apiKey), model, model)

	f, err := os.CreateTemp("", "bifrost-e2e-opencode-*.json")
	if err != nil {
		return nil, nil, fmt.Errorf("create opencode config: %w", err)
	}
	if _, err := f.WriteString(cfg); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, nil, fmt.Errorf("write opencode config: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return nil, nil, fmt.Errorf("close opencode config: %w", err)
	}
	return []string{"OPENCODE_CONFIG=" + f.Name()}, func() { os.Remove(f.Name()) }, nil
}

func opencodeModelRef(model string) string {
	return "bifrost/" + strings.TrimSpace(model)
}
