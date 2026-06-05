package schemas

import "strings"

// UserAgentIdentifiers lists substrings that may appear in User-Agent for a given integration.
// Versions of the same client may use different strings; Matches checks any of them.
type UserAgentIdentifiers []string

var (
	// ClaudeCLI — Anthropic Claude Code / Claude CLI (identifiers vary by release).
	ClaudeDesktop = UserAgentIdentifiers{"claude-desktop"}
	ClaudeCLI     = UserAgentIdentifiers{"claude-cli", "claude-code", "claude-vscode"}
	CodexCLI      = UserAgentIdentifiers{"codex-cli", "codex-tui", "codex"}
	Cursor        = UserAgentIdentifiers{"cursor"}
	KiloCode      = UserAgentIdentifiers{"kilo"}
	RooCode       = UserAgentIdentifiers{"roo"}
	Cline         = UserAgentIdentifiers{"cline"}
	OpenCode      = UserAgentIdentifiers{"opencode"}
	Windsurf      = UserAgentIdentifiers{"windsurf"}
	GeminiCLI     = UserAgentIdentifiers{"gemini-cli", "geminicli", "gemini"}
	QwenCodeCLI   = UserAgentIdentifiers{"qwen-code", "qwencode", "qwen"}
)

type UserAgentAppMatcher struct {
	App         string
	Identifiers UserAgentIdentifiers
}

const (
	UserAgentAppOther = "Other"
)

// UserAgentAppMatchers is evaluated top-to-bottom. More specific identifiers
// should appear before generic ancestors.
var UserAgentAppMatchers = []UserAgentAppMatcher{
	{App: "Claude Desktop", Identifiers: ClaudeDesktop},
	{App: "Claude Code", Identifiers: ClaudeCLI},
	{App: "Codex", Identifiers: CodexCLI},
	{App: "Cursor", Identifiers: Cursor},
	{App: "Kilo Code", Identifiers: KiloCode},
	{App: "Roo Code", Identifiers: RooCode},
	{App: "Cline", Identifiers: Cline},
	{App: "OpenCode", Identifiers: OpenCode},
	{App: "Windsurf", Identifiers: Windsurf},
	{App: "Gemini CLI", Identifiers: GeminiCLI},
	{App: "Qwen Code", Identifiers: QwenCodeCLI},
}

// Matches reports whether userAgent starts with or contains any identifier
// (case-insensitive). User-Agent values are commonly versioned, e.g.
// "claude-cli/2.1.168 (external, cli)", so exact matching is intentionally
// avoided.
func (ids UserAgentIdentifiers) Matches(userAgent string) bool {
	if len(ids) == 0 || userAgent == "" {
		return false
	}
	ua := strings.ToLower(userAgent)
	for _, id := range ids {
		if id == "" {
			continue
		}
		normalizedID := strings.ToLower(id)
		if strings.HasPrefix(ua, normalizedID) || strings.Contains(ua, normalizedID) {
			return true
		}
	}
	return false
}

// String returns the first identifier for logging and tests that need a canonical sample value.
func (ids UserAgentIdentifiers) String() string {
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

func DetectAppFromUserAgent(userAgent string) string {
	if strings.TrimSpace(userAgent) == "" {
		return ""
	}
	for _, matcher := range UserAgentAppMatchers {
		if matcher.Identifiers.Matches(userAgent) {
			return matcher.App
		}
	}
	return UserAgentAppOther
}

func ExtractAndSetUserAgentFromHeaders(headers map[string][]string, bifrostCtx *BifrostContext) {
	if len(headers) == 0 {
		return
	}
	if bifrostCtx == nil {
		return
	}
	var userAgent []string
	for key, value := range headers {
		if strings.EqualFold(key, "user-agent") {
			userAgent = value
			break
		}
	}
	if len(userAgent) > 0 {
		ua := userAgent[0]
		bifrostCtx.SetValue(BifrostContextKeyUserAgent, ua)
	}
}
