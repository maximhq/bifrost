package schemas

import "strings"

// UserAgentIdentifiers lists substrings that may appear in User-Agent for a given integration.
// Versions of the same client may use different strings; Matches checks any of them.
type UserAgentIdentifiers []string

var (
	// ClaudeCLI — Anthropic Claude Code / Claude CLI (identifiers vary by release).
	ClaudeCLI   = UserAgentIdentifiers{"claude-cli", "claude-code"}
	GeminiCLI   = UserAgentIdentifiers{"geminicli"}
	CodexCLI    = UserAgentIdentifiers{"codex-tui"}
	QwenCodeCLI = UserAgentIdentifiers{"qwencode"}
	OpenCode    = UserAgentIdentifiers{"opencode"}
	Cursor      = UserAgentIdentifiers{"cursor"}
)

// integrationUserAgents is the set of known client User-Agent patterns we persist on the context.
var integrationUserAgents = []UserAgentIdentifiers{
	ClaudeCLI, GeminiCLI, CodexCLI, QwenCodeCLI, OpenCode, Cursor,
}

// Matches reports whether userAgent contains any identifier (case-insensitive substring match).
func (ids UserAgentIdentifiers) Matches(userAgent string) bool {
	if len(ids) == 0 || userAgent == "" {
		return false
	}
	ua := strings.ToLower(userAgent)
	for _, id := range ids {
		if id == "" {
			continue
		}
		if strings.Contains(ua, strings.ToLower(id)) {
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
		for _, ids := range integrationUserAgents {
			if ids.Matches(ua) {
				bifrostCtx.SetValue(BifrostContextKeyUserAgent, ua)
				break
			}
		}
	}
}
