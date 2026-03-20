package proxy

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/maximhq/bifrost/agent/config"
)

// RewriteRequest modifies an HTTP request's headers and URL to route it through
// the Bifrost gateway. The request body is never modified.
//
// Transformations:
//   - URL scheme/host → gateway URL
//   - URL path → integration prefix + original path
//   - Host header → gateway hostname
//   - X-Forwarded-Host → original hostname
//   - x-bf-vk → virtual key from config
//   - X-Bifrost-Agent → agent version identifier
func RewriteRequest(req *http.Request, rule *config.DomainRule, gatewayURL string, virtualKey string) error {
	originalHost := req.Host
	if originalHost == "" {
		originalHost = req.URL.Host
	}

	// Parse gateway URL
	gw, err := url.Parse(gatewayURL)
	if err != nil {
		return err
	}

	// Rewrite URL: scheme + host from gateway, path from integration prefix + original
	req.URL.Scheme = gw.Scheme
	req.URL.Host = gw.Host

	if rule.PreservePath {
		// e.g., /v1/chat/completions → /openai/v1/chat/completions
		originalPath := req.URL.Path
		req.URL.Path = rule.IntegrationPrefix + originalPath
	} else {
		req.URL.Path = rule.IntegrationPrefix
	}

	// Clean double slashes
	req.URL.Path = strings.ReplaceAll(req.URL.Path, "//", "/")

	// Update Host header to gateway
	req.Host = gw.Host

	// Add Bifrost headers
	if virtualKey != "" {
		req.Header.Set("x-bf-vk", virtualKey)
	}

	// Preserve the original host so the gateway knows the intended provider
	req.Header.Set("X-Forwarded-Host", originalHost)

	// Identify traffic as coming from the agent
	req.Header.Set("X-Bifrost-Agent", "bifrost-agent/0.1.0")

	return nil
}
