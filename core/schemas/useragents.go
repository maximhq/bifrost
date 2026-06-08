package schemas

import (
	"regexp"
	"strings"
)

// UserAgentIdentifiers lists substrings that may appear in User-Agent for a given integration.
// Versions of the same client may use different strings; Matches checks any of them.
type UserAgentIdentifiers []string

var (
	// ClaudeDesktop identifies requests from the Claude Desktop app.
	ClaudeDesktop = UserAgentIdentifiers{"claude-desktop", "claude/"}
	// ClaudeCLI identifies requests from Claude Code / Claude CLI clients.
	ClaudeCLI = UserAgentIdentifiers{"claude-cli", "claude-code", "claude-vscode"}
	// APIClient identifies generic programmatic API clients.
	APIClient = UserAgentIdentifiers{"fasthttp"}
	// CodexCLI identifies requests from Codex CLI clients.
	CodexCLI = UserAgentIdentifiers{"codex-cli", "codex-tui"}
	// CodexDesktop identifies requests from the Codex desktop app.
	CodexDesktop = UserAgentIdentifiers{"codex-desktop", "codex/"}
	// Cursor identifies requests from Cursor clients.
	Cursor = UserAgentIdentifiers{"cursor"}
	// KiloCode identifies requests from Kilo Code clients.
	KiloCode = UserAgentIdentifiers{"kilo"}
	// RooCode identifies requests from Roo Code clients.
	RooCode = UserAgentIdentifiers{"roo"}
	// Cline identifies requests from Cline clients.
	Cline = UserAgentIdentifiers{"cline"}
	// OpenCode identifies requests from OpenCode clients.
	OpenCode = UserAgentIdentifiers{"opencode"}
	// Windsurf identifies requests from Windsurf clients.
	Windsurf = UserAgentIdentifiers{"windsurf"}
	// GeminiCLI identifies requests from Gemini CLI clients.
	GeminiCLI = UserAgentIdentifiers{"gemini-cli", "geminicli", "gemini"}
	// QwenCodeCLI identifies requests from Qwen Code clients.
	QwenCodeCLI = UserAgentIdentifiers{"qwen-code", "qwencode", "qwen"}
)

// UserAgentAppMatcher maps a detected application label to User-Agent identifiers.
type UserAgentAppMatcher struct {
	App         string
	Identifiers UserAgentIdentifiers
}

const (
	// UserAgentAppOther is returned when a non-empty User-Agent has no known app match.
	UserAgentAppOther = "Other"
)

// UserAgentMappingMatchType identifies how a custom mapping pattern should match a User-Agent.
type UserAgentMappingMatchType string

const (
	// UserAgentMappingMatchTypeContains matches when the User-Agent contains the pattern.
	UserAgentMappingMatchTypeContains UserAgentMappingMatchType = "contains"
	// UserAgentMappingMatchTypeStartsWith matches when the User-Agent starts with the pattern.
	UserAgentMappingMatchTypeStartsWith UserAgentMappingMatchType = "starts_with"
	// UserAgentMappingMatchTypeExact matches when the User-Agent equals the pattern.
	UserAgentMappingMatchTypeExact UserAgentMappingMatchType = "exact"
	// UserAgentMappingMatchTypeRegex matches when the regex pattern matches the User-Agent.
	UserAgentMappingMatchTypeRegex UserAgentMappingMatchType = "regex"
)

// UserAgentAppMatchers is evaluated top-to-bottom. More specific identifiers
// should appear before generic ancestors.
var UserAgentAppMatchers = []UserAgentAppMatcher{
	{App: "Claude Desktop", Identifiers: ClaudeDesktop},
	{App: "Claude Code", Identifiers: ClaudeCLI},
	{App: "API", Identifiers: APIClient},
	{App: "Codex CLI", Identifiers: CodexCLI},
	{App: "Codex Desktop", Identifiers: CodexDesktop},
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

// DetectAppFromUserAgent returns the built-in app label for a User-Agent.
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

// MatchUserAgent reports whether a User-Agent matches a pattern using the given match type.
// An empty matchType defaults to UserAgentMappingMatchTypeContains.
//
// For UserAgentMappingMatchTypeRegex the pattern is compiled on every call; performance-sensitive
// callers should pre-compile with regexp.Compile and use Regexp.MatchString directly instead.
func MatchUserAgent(userAgent, pattern string, matchType UserAgentMappingMatchType) bool {
	userAgent = strings.TrimSpace(userAgent)
	pattern = strings.TrimSpace(pattern)
	if userAgent == "" || pattern == "" {
		return false
	}
	ua := strings.ToLower(userAgent)
	p := strings.ToLower(pattern)
	switch matchType {
	case UserAgentMappingMatchTypeExact:
		return ua == p
	case UserAgentMappingMatchTypeStartsWith:
		return strings.HasPrefix(ua, p)
	case UserAgentMappingMatchTypeRegex:
		re, err := regexp.Compile(pattern)
		return err == nil && re.MatchString(userAgent)
	case UserAgentMappingMatchTypeContains, "":
		return strings.Contains(ua, p)
	default:
		return false
	}
}

// ExtractAndSetUserAgentFromHeaders copies a case-insensitive User-Agent header into BifrostContext.
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
