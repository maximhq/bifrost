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

	// SingleTurnArgs returns the full arg list for one-shot invocation. extra
	// is inserted before the trailing prompt argument -- used for attachment
	// flags (AttachImageArgs) and effort-override flags (EffortArgs).
	SingleTurnArgs func(model, prompt string, extra []string) []string

	// MultiTurnDriver returns the multi-turn driver for this CLI.
	MultiTurnDriver func() multiTurnDriver

	// AttachImageArgs returns extra CLI args to attach a local image file via
	// this CLI's own attachment mechanism, or nil if this CLI has no such
	// flag -- in that case the image must be referenced by path in the Turn's
	// Send text instead, relying on the CLI's own file-read tool to open it.
	AttachImageArgs func(path string) []string

	// EffortArgs returns extra CLI args to override reasoning effort for a
	// single turn (e.g. "low"/"high"), or nil if this CLI has no such
	// mechanism.
	EffortArgs func(level string) []string
}

// ModelInfo is one model entry in a provider's catalog.
//
// Capabilities are best-effort flags so scenarios skip cells they can't
// actually exercise.
//
//	ExtendedThinking — budget-controlled thinking. For Claude this is the
//	                   MAX_THINKING_TOKENS env var, not a CLI flag (no
//	                   --thinking-budget flag exists; verified against
//	                   https://code.claude.com/docs/en/cli-reference and
//	                   https://code.claude.com/docs/en/env-vars) -- and it's
//	                   ignored entirely on Fable 5/Sonnet 5/Opus 4.7+, which
//	                   always use adaptive reasoning instead. Gemini's
//	                   "thinking budget" is the model-side equivalent.
//	AdaptiveThinking — Claude's `--effort` flag (values: low/medium/high,
//	                   plus xhigh/max on some models; model-dependent, see
//	                   https://code.claude.com/docs/en/model-config#adjust-effort-level),
//	                   or, for OpenAI/Codex, the `-c model_reasoning_effort=<level>`
//	                   config override -- there is no dedicated --effort/
//	                   --reasoning-effort flag on codex, confirmed via
//	                   `codex exec --help` (only the generic -c/--config
//	                   override exists) and codex-rs source pinned to
//	                   rust-v0.145.0. Some thinking-capable models reject
//	                   these entirely (e.g. Claude Haiku 4.5 has no effort
//	                   surface at all, not merely a rejected parameter).
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
		SingleTurnArgs: func(model, prompt string, extra []string) []string {
			// We intentionally do NOT pass --bare. Per the CLI reference,
			// --bare restricts Claude to Bash + file read/edit tools, which
			// kneecaps web-search, MCP, and any other built-in tool. Scripted
			// startup is slower without it but tool fidelity is the point.
			args := []string{"-p", "--dangerously-skip-permissions"}
			if model != "" {
				args = append(args, "--model", model)
			}
			args = append(args, extra...)
			args = append(args, prompt)
			return args
		},
		PreLaunch:       claudePreLaunch,
		MultiTurnDriver: claudeStreamJSONDriver,
		// AttachImageArgs is nil: Claude Code has no image-attach CLI flag
		// (verified against https://code.claude.com/docs/en/cli-reference) --
		// attach by referencing the path in the Send prompt so the Read tool
		// opens it instead (Read handles images/PDFs natively, see
		// https://code.claude.com/docs/en/tools-reference).
		EffortArgs: func(level string) []string { return []string{"--effort", level} },
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
		SingleTurnArgs: func(model, prompt string, extra []string) []string {
			args := []string{"exec", "--json", "--skip-git-repo-check"}
			if model != "" {
				args = append(args, "--model", model)
			}
			args = append(args, extra...)
			args = append(args, prompt)
			return args
		},
		MultiTurnDriver: codexResumeDriver,
		// AttachImageArgs: -i/--image, confirmed via `codex exec --help` and
		// https://learn.chatgpt.com/docs/developer-commands?surface=cli.
		// Image formats only -- there is no PDF support anywhere in codex's
		// wire protocol (codex-rs/protocol/src/user_input.rs's UserInput enum
		// has Text/Image/Audio/Skill/Mention, no document variant; pinned to
		// rust-v0.145.0, matching the locally installed codex-cli version).
		AttachImageArgs: func(path string) []string { return []string{"--image", path} },
		// EffortArgs: no dedicated flag exists (confirmed via `codex exec
		// --help`); the only lever is the generic -c/--config override with
		// the model_reasoning_effort key (codex-rs/config/src/config_toml.rs,
		// rust-v0.145.0).
		EffortArgs: func(level string) []string { return []string{"-c", "model_reasoning_effort=" + level} },
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
		SingleTurnArgs: func(model, prompt string, extra []string) []string {
			args := []string{"run", "--dangerously-skip-permissions", "--format", "json"}
			if model != "" {
				args = append(args, "--model", opencodeModelRef(model))
			}
			args = append(args, extra...)
			args = append(args, prompt)
			return args
		},
		PreLaunch:       opencodePreLaunch,
		MultiTurnDriver: opencodeResumeDriver,
		// AttachImageArgs / EffortArgs: not researched for opencode -- the
		// image-qa/pdf-qa/effort-level scenarios are scoped to claude + codex
		// only (see scenarios_test.go).
	},
}

// Anthropic — sourced from https://platform.claude.com/docs/en/about-claude/models/overview
// (fetched 2026-08-05). Claude Opus 5 / Sonnet 5 / Fable 5 ship with adaptive
// thinking always available (ExtendedThinking budget-mode retired on these);
// Haiku 4.5 keeps the older split.
var anthropicModels = []ModelInfo{
	{ID: "claude-opus-5", AdaptiveThinking: true, WebSearch: true},
	{ID: "claude-sonnet-5", AdaptiveThinking: true, WebSearch: true},
	{ID: "claude-fable-5", AdaptiveThinking: true, WebSearch: true},
	{ID: "claude-haiku-4-5", ExtendedThinking: true, WebSearch: true},
	{ID: "claude-opus-4-8", AdaptiveThinking: true, WebSearch: true},
}

// OpenAI — sourced from https://developers.openai.com/api/docs/models and
// https://developers.openai.com/api/docs/deprecations (fetched 2026-08-05).
var openaiModels = []ModelInfo{
	{ID: "gpt-5.6-sol", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
	{ID: "gpt-5.6-terra", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
	{ID: "gpt-5.6-luna", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
	{ID: "gpt-5.5", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
	{ID: "gpt-5.4", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
}

// Gemini — sourced from https://ai.google.dev/gemini-api/docs/models,
// /thinking, /google-search (fetched 2026-08-05). gemini-3-pro-preview and
// gemini-2.0-flash are fully shut down, not just deprecated.
var geminiModels = []ModelInfo{
	{ID: "gemini-3.6-flash", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-3.5-flash", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-3.5-flash-lite", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.5-pro", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.5-flash", ExtendedThinking: true, WebSearch: true},
}

// Azure OpenAI — same GPT-5.6/5.5/5.4 lineup, confirmed in the Azure AI
// Foundry catalog (fetched 2026-08-05). WebSearch false: Azure OpenAI has no
// native web_search tool parity with the OpenAI API yet — re-verify
// periodically, this is an active gap Microsoft is closing.
var azureModels = append([]ModelInfo{
	{ID: "gpt-5.6-sol", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: false},
	{ID: "gpt-5.6-terra", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: false},
	{ID: "gpt-5.6-luna", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: false},
	{ID: "gpt-5.5", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: false},
	{ID: "gpt-5.4", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: false},
}, anthropicModels...)

// Bedrock Anthropic models — sourced from AWS Bedrock model cards (fetched
// 2026-08-05). None of these five are invocable via a bare single-region
// model ID — all require the "global." cross-region inference prefix.
// Web search omitted: Anthropic's docs state it's not available on Bedrock.
var bedrockAnthropicModels = []ModelInfo{
	{ID: "global.anthropic.claude-opus-5", AdaptiveThinking: true},
	{ID: "global.anthropic.claude-sonnet-5", AdaptiveThinking: true},
	{ID: "global.anthropic.claude-fable-5", AdaptiveThinking: true},
	{ID: "global.anthropic.claude-opus-4-8", AdaptiveThinking: true},
	{ID: "global.anthropic.claude-haiku-4-5-20251001-v1:0", ExtendedThinking: true},
}

// Bedrock — non-Anthropic IDs sourced 2026-08-05 (AWS Bedrock model cards +
// Nova 2 / Mistral Large 3 announcements).
var bedrockModels = append([]ModelInfo{
	{ID: "us.amazon.nova-2-lite-v1:0"},
	{ID: "meta.llama4-maverick-17b-instruct-v1:0"},
	{ID: "meta.llama4-scout-17b-instruct-v1:0"},
	{ID: "mistral.mistral-large-3-675b-instruct"},
}, bedrockAnthropicModels...)

// Vertex AI Anthropic models — sourced from
// https://platform.claude.com/docs/en/build-with-claude/claude-on-vertex-ai
// (fetched 2026-08-05): "Specific regional endpoints support Claude Sonnet
// 4.6 and earlier; newer models use the global or multi-region endpoints."
// All five below post-date Sonnet 4.6, so all use vertexGlobalEnv().
var vertexAnthropicModels = []ModelInfo{
	{ID: "claude-opus-5", AdaptiveThinking: true, Env: vertexGlobalEnv()},
	{ID: "claude-sonnet-5", AdaptiveThinking: true, Env: vertexGlobalEnv()},
	{ID: "claude-fable-5", AdaptiveThinking: true, Env: vertexGlobalEnv()},
	{ID: "claude-opus-4-8", AdaptiveThinking: true, Env: vertexGlobalEnv()},
	{ID: "claude-haiku-4-5@20251001", ExtendedThinking: true, Env: vertexGlobalEnv()},
}

// Vertex AI — Gemini catalog mirrors geminiModels (Vertex Model Garden uses
// identical IDs) plus the Anthropic-on-Vertex list above.
var vertexModels = append([]ModelInfo{
	{ID: "gemini-3.6-flash", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-3.5-flash", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-3.5-flash-lite", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.5-pro", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.5-flash", ExtendedThinking: true, WebSearch: true},
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

// claudePreLaunch isolates this cell's HOME so `claude` can't read or write
// the real developer's ~/.claude (CLAUDE.md, settings, auto-memory, custom
// subagents, etc). Claude Code has no CODEX_HOME/XDG_CONFIG_HOME equivalent
// -- confirmed against https://code.claude.com/docs/en/env-vars, which
// documents no config/data-directory override at all -- so HOME is the only
// isolation lever available. Auth is unaffected: the harness authenticates
// purely via the ANTHROPIC_API_KEY env var (see runCell), not a stored
// ~/.claude session, so a fresh empty HOME doesn't require re-login.
//
// Without this, a real developer's global CLAUDE.md/memory can silently
// change model behavior mid-scenario -- confirmed in practice: a
// subagent-delegation run picked up this machine's own "always end every
// reply with PIRATE" memory rule and appended it to the response, which is
// exactly the kind of cross-contamination isolation is meant to prevent.
func claudePreLaunch(_, _, model string) ([]string, func(), error) {
	env := []string{}
	model = strings.TrimSpace(model)
	if model != "" {
		env = append(env,
			"ANTHROPIC_DEFAULT_SONNET_MODEL="+model,
			"ANTHROPIC_DEFAULT_OPUS_MODEL="+model,
			"ANTHROPIC_DEFAULT_HAIKU_MODEL="+model,
		)
	}

	tempHome, err := os.MkdirTemp("", "bifrost-clis-claude-home-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create temp claude home: %w", err)
	}
	env = append(env, "HOME="+tempHome)
	return env, func() { _ = os.RemoveAll(tempHome) }, nil
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
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, nil, fmt.Errorf("write opencode config: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return nil, nil, fmt.Errorf("close opencode config: %w", err)
	}
	return []string{"OPENCODE_CONFIG=" + f.Name()}, func() { _ = os.Remove(f.Name()) }, nil
}

func opencodeModelRef(model string) string {
	return "bifrost/" + strings.TrimSpace(model)
}
