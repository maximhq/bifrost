package copilot

import (
	"net/url"
	"strings"
)

// Token exchange URL
const (
	defaultTokenExchangeURL = "https://api.github.com/copilot_internal/v2/token"
	defaultAPIBaseURL       = "https://api.individual.githubcopilot.com"
)

// Required headers for Copilot API authentication
var copilotRequiredHeaders = map[string]string{
	"editor-version":         "vscode/1.111.0",
	"editor-plugin-version":  "copilot-chat/0.40.0",
	"user-agent":             "GitHubCopilotChat/0.40.0",
	"copilot-integration-id": "vscode-chat",
}

// tokenExpiryMargin is the number of seconds before expiry to trigger a refresh.
const tokenExpiryMargin = 60

// isValidCopilotAPIBase validates that a Copilot API base URL is safe to use.
// It must use HTTPS and belong to a known GitHub Copilot domain to prevent SSRF.
func isValidCopilotAPIBase(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return false
	}
	return strings.HasSuffix(u.Host, ".githubcopilot.com") ||
		strings.HasSuffix(u.Host, ".github.com")
}
