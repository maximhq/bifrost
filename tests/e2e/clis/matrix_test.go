package clis

// CLI describes how to drive a coding-assistant CLI in non-interactive mode.
// We support two invocation modes:
//
//   single-turn:  one process per prompt, prompt as arg, response on stdout
//   multi-turn:   one long-running process, prompts written to stdin as
//                 JSON-Lines (Claude) or via resume subcommands (Codex),
//                 events read from stdout as JSON-Lines.
//
// The runner picks the mode based on len(scenario.Turns).
type CLI struct {
	ID         string
	Binary     string
	BasePath   string // Bifrost base-path the CLI's wire format targets
	BaseURLEnv string
	APIKeyEnv  string
	ExtraEnv   map[string]string

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
}

// Provider describes a Bifrost-configured provider and its model catalog.
//
// Models lists the ~10 latest chat-capable model IDs we want the harness to
// exercise. Sources are documented per-provider below; refresh periodically
// as providers deprecate / add models.
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
		MultiTurnDriver: claudeStreamJSONDriver,
	},
	"codex": {
		ID:         "codex",
		Binary:     "codex",
		BasePath:   "/openai",
		BaseURLEnv: "OPENAI_BASE_URL",
		APIKeyEnv:  "OPENAI_API_KEY",
		ExtraEnv:   map[string]string{},
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
}

// Anthropic — sourced from
// https://platform.claude.com/docs/en/docs/about-claude/models/overview
// (the "Latest models comparison" + "Legacy models" tables explicitly list
// Extended thinking and Adaptive thinking support per model).
var anthropicModels = []ModelInfo{
	// Current
	{ID: "claude-opus-4-7", ExtendedThinking: false, AdaptiveThinking: true, WebSearch: true},
	{ID: "claude-sonnet-4-6", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
	{ID: "claude-haiku-4-5", ExtendedThinking: true, AdaptiveThinking: false, WebSearch: true},
	// Legacy (all listed with Extended thinking: Yes; doc doesn't tabulate
	// Adaptive thinking for legacy entries, so we leave it false to be safe).
	{ID: "claude-opus-4-6", ExtendedThinking: true, WebSearch: true},
	{ID: "claude-sonnet-4-5", ExtendedThinking: true, WebSearch: true},
	{ID: "claude-opus-4-5", ExtendedThinking: true, WebSearch: true},
	{ID: "claude-opus-4-1", ExtendedThinking: true, WebSearch: true},
	{ID: "claude-sonnet-4-0", ExtendedThinking: true, WebSearch: true},
	{ID: "claude-opus-4-0", ExtendedThinking: true, WebSearch: true},
	{ID: "claude-3-5-sonnet-20241022"}, // pre-thinking generation
}

// OpenAI — model IDs from the public OpenAI model catalog as of 2026-Q1.
// Refresh against https://platform.openai.com/docs/models when models change.
// AdaptiveThinking maps to the OpenAI `reasoning_effort` parameter (low/
// medium/high), supported on the o-series and gpt-5 reasoning lines.
var openaiModels = []ModelInfo{
	{ID: "gpt-5", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
	{ID: "gpt-5-mini", ExtendedThinking: true, AdaptiveThinking: true, WebSearch: true},
	{ID: "gpt-5-nano"},
	{ID: "gpt-4.1", WebSearch: true},
	{ID: "gpt-4.1-mini", WebSearch: true},
	{ID: "gpt-4o", WebSearch: true},
	{ID: "gpt-4o-mini", WebSearch: true},
	{ID: "o3", ExtendedThinking: true, AdaptiveThinking: true},
	{ID: "o3-mini", ExtendedThinking: true, AdaptiveThinking: true},
	{ID: "o1", ExtendedThinking: true, AdaptiveThinking: true},
}

// Gemini — sourced from https://ai.google.dev/gemini-api/docs/models
// (fetched 2026-05). Gemini's "thinking budget" maps to ExtendedThinking;
// no native AdaptiveThinking equivalent today, so we leave it false.
var geminiModels = []ModelInfo{
	{ID: "gemini-3.1-pro-preview", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-3-flash-preview", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-3.1-flash-lite-preview", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.5-pro", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.5-flash", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.5-flash-lite", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.0-flash", WebSearch: true},      // deprecated, no thinking
	{ID: "gemini-2.0-flash-lite", WebSearch: true}, // deprecated, no thinking
	{ID: "gemini-1.5-pro"},
	{ID: "gemini-1.5-flash"},
}

// Bedrock — Anthropic-on-Bedrock IDs from the Anthropic models doc.
// Capability flags mirror the native Anthropic entries above (same models,
// different routing). Amazon Nova / Meta Llama / Mistral catalog entries
// have no thinking surface today.
var bedrockModels = []ModelInfo{
	{ID: "global.anthropic.claude-opus-4-7", AdaptiveThinking: true},
	{ID: "global.anthropic.claude-sonnet-4-6", ExtendedThinking: true, AdaptiveThinking: true},
	{ID: "anthropic.claude-haiku-4-5-20251001-v1:0", ExtendedThinking: true},
	{ID: "anthropic.claude-opus-4-6-v1", ExtendedThinking: true},
	{ID: "anthropic.claude-sonnet-4-5-20250929-v1:0", ExtendedThinking: true},
	{ID: "anthropic.claude-opus-4-5-20251101-v1:0", ExtendedThinking: true},
	{ID: "amazon.nova-pro-v1:0"},
	{ID: "amazon.nova-lite-v1:0"},
	{ID: "meta.llama3-3-70b-instruct-v1:0"},
	{ID: "mistral.mistral-large-2407-v1:0"},
}

// Vertex AI — Gemini natively + Anthropic-on-Vertex IDs from the Anthropic doc.
var vertexModels = []ModelInfo{
	{ID: "gemini-2.5-pro", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.5-flash", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.5-flash-lite", ExtendedThinking: true, WebSearch: true},
	{ID: "gemini-2.0-flash", WebSearch: true},
	{ID: "gemini-1.5-pro"},
	{ID: "gemini-1.5-flash"},
	{ID: "claude-opus-4-7", AdaptiveThinking: true},
	{ID: "claude-sonnet-4-6", ExtendedThinking: true, AdaptiveThinking: true},
	{ID: "claude-haiku-4-5@20251001", ExtendedThinking: true},
	{ID: "claude-opus-4-6", ExtendedThinking: true},
}

var providers = map[string]Provider{
	"openai":    {ID: "openai", Models: openaiModels},
	"anthropic": {ID: "anthropic", Models: anthropicModels},
	"gemini":    {ID: "gemini", Models: geminiModels},
	"bedrock":   {ID: "bedrock", Models: bedrockModels},
	"vertex":    {ID: "vertex", Models: vertexModels},
}

// bifrostModelRef is the provider-prefixed model id Bifrost expects when
// routing across the wire-format boundary.
func bifrostModelRef(provider, model string) string {
	return provider + "/" + model
}
