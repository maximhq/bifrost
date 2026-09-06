package schemas

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// UserAgentIdentifiers lists substrings that may appear in User-Agent for a given integration.
// Versions of the same client may use different strings; Matches checks any of them.
type UserAgentIdentifiers []string

var (
	// ClaudeChatWeb identifies requests from the Claude web app.
	ClaudeChatWeb = UserAgentIdentifiers{"claude-chat-web", "claude-web"}
	// ClaudeDesktop identifies requests from the Claude Desktop app.
	ClaudeDesktop = UserAgentIdentifiers{"claude-desktop", "claude/"}
	// ClaudeCLI identifies requests from Claude Code / Claude CLI clients.
	ClaudeCLI = UserAgentIdentifiers{"claude-cli", "claude-code", "claude-vscode"}
	// APIClient identifies generic programmatic API clients.
	APIClient = UserAgentIdentifiers{"fasthttp"}
	// Warp identifies the dashboard's own data agent talking to this gateway.
	// Deliberately prefixed rather than a bare "warp": the identifiers are matched
	// as substrings, and a four-letter word appears inside plenty of unrelated
	// User-Agent strings.
	Warp = UserAgentIdentifiers{"bifrost-warp"}
	// CodexCLI identifies requests from Codex CLI clients.
	CodexCLI = UserAgentIdentifiers{"codex-cli", "codex-tui"}
	// CodexDesktop identifies requests from the Codex desktop app.
	CodexDesktop = UserAgentIdentifiers{"codex-desktop", "codex desktop/", "codex/"}
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

// MaxSessionIDLength caps every caller-asserted session identifier Bifrost
// ingests, counted in runes rather than bytes so the limit means the same thing
// for a non-ASCII identifier as for an ASCII one.
//
// Session IDs become KV lookup keys and exported trace attributes, and the
// OpenTelemetry attribute value length limit defaults to unlimited
// (https://opentelemetry.io/docs/specs/otel/common/#attribute-limits), so
// nothing downstream bounds a caller-controlled value for us. Every ingestion
// path (x-bf-session-id, the baggage session-id member, x-bf-mcp-session-id, and
// the harness headers below) goes through NormalizeSessionID.
const MaxSessionIDLength = 255

// NormalizeSessionID trims a caller-asserted session identifier and reports
// whether it is usable: non-empty and no longer than MaxSessionIDLength runes.
// Every session ingestion path shares it so one rule governs what can become a
// KV lookup key or an exported trace attribute.
func NormalizeSessionID(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > MaxSessionIDLength {
		return "", false
	}
	return value, true
}

// HarnessSessionHeaders lists the headers coding harnesses use to carry their
// own session identifier, in priority order. Bifrost falls back to these when
// x-bf-session-id is absent, so harness traffic gets provider key stickiness and
// OTEL session grouping without the caller renaming anything.
//
// Claude Code documents x-claude-code-session-id for exactly this purpose and
// classifies it as a header a gateway may consume rather than forward:
// https://code.claude.com/docs/en/llm-gateway-protocol#request-headers
// That reference also treats x-claude-code-* as an open list that grows with
// releases, so revisit this table when Claude Code ships new attribution headers.
//
// Only headers a harness verifiably sends belong here. Deliberately absent:
// x-parent-session-id (OpenCode) would collapse every sub-agent onto its
// parent's key, and x-qwen-code-session-id was designed but never shipped.
var HarnessSessionHeaders = []string{
	"x-claude-code-session-id", // Claude Code, and wrappers that spawn it (e.g. Conductor)
	"x-session-affinity",       // OpenCode
	"x-session-id",             // OpenCode sends this alongside the above; also the generic convention
	"session-id",               // Codex CLI
	"session_id",               // Codex CLI, builds predating the dashed rename
	"thread-id",                // Codex CLI thread: coarser, only when no session header is sent
	"conversation_id",          // Codex CLI, builds predating the dashed rename
}

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
	{App: "Claude Chat Web", Identifiers: ClaudeChatWeb},
	{App: "Claude Desktop", Identifiers: ClaudeDesktop},
	{App: "Claude Code", Identifiers: ClaudeCLI},
	// Ahead of API: Warp reaches the gateway over fasthttp, so the generic
	// matcher would otherwise claim it first and label it a plain API client.
	{App: "Warp", Identifiers: Warp},
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

// AppKeyFromName returns the canonical policy key for a detected app name.
func AppKeyFromName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || name == UserAgentAppOther {
		return ""
	}
	return strings.Join(strings.Fields(strings.ToLower(name)), "-")
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
		re, err := regexp.Compile("(?i)" + pattern)
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
