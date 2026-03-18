package copilot

import (
	"net/url"
	"os"
	"strings"
)

// Token exchange URL
const (
	defaultTokenExchangeURL = "https://api.github.com/copilot_internal/v2/token"
	defaultAPIBaseURL       = "https://api.individual.githubcopilot.com"
)

// copilotRequiredHeaders returns the headers required by the Copilot API.
// Values can be overridden via environment variables so operators can track
// upstream version bumps without rebuilding:
//
//	BIFROST_COPILOT_EDITOR_VERSION         (default "vscode/1.111.0")
//	BIFROST_COPILOT_EDITOR_PLUGIN_VERSION  (default "copilot-chat/0.40.0")
//	BIFROST_COPILOT_USER_AGENT             (default "GitHubCopilotChat/0.40.0")
//	BIFROST_COPILOT_INTEGRATION_ID         (default "vscode-chat")
var copilotRequiredHeaders = func() map[string]string {
	return map[string]string{
		"editor-version":         envOrDefault("BIFROST_COPILOT_EDITOR_VERSION", "vscode/1.111.0"),
		"editor-plugin-version":  envOrDefault("BIFROST_COPILOT_EDITOR_PLUGIN_VERSION", "copilot-chat/0.40.0"),
		"user-agent":             envOrDefault("BIFROST_COPILOT_USER_AGENT", "GitHubCopilotChat/0.40.0"),
		"copilot-integration-id": envOrDefault("BIFROST_COPILOT_INTEGRATION_ID", "vscode-chat"),
	}
}()

// envOrDefault returns the environment variable value if set, otherwise the fallback.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
