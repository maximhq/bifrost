package copilot

import (
	"net/url"
	"os"
	"strings"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
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

// Per-request Copilot headers. Unlike copilotRequiredHeaders these are derived
// from the request payload and are only sent on inference calls, never on the
// OAuth token exchange.
const (
	// copilotInitiatorHeader tells Copilot whether a turn was initiated by the
	// human ("user") or by an autonomous agent continuation ("agent"). Copilot
	// meters and rate-limits the two differently, so sending it makes Bifrost's
	// traffic accounted for the same way a first-party editor's traffic is.
	copilotInitiatorHeader = "x-initiator"
	copilotInitiatorUser   = "user"
	copilotInitiatorAgent  = "agent"

	// copilotVisionHeader must be set when the payload carries image content,
	// otherwise Copilot rejects or silently drops the images for some models.
	copilotVisionHeader = "Copilot-Vision-Request"

	// copilotIntentHeader declares what the request is for. Editors send this on
	// every chat/responses call.
	copilotIntentHeader = "Openai-Intent"
)

// copilotIntent is the value sent in the Openai-Intent header, overridable via
// BIFROST_COPILOT_OPENAI_INTENT for operators tracking upstream changes.
var copilotIntent = envOrDefault("BIFROST_COPILOT_OPENAI_INTENT", "conversation-edits")

// chatRequestHeaders derives the per-request Copilot headers for a chat
// completion request.
func chatRequestHeaders(request *schemas.BifrostChatRequest) map[string]string {
	headers := map[string]string{
		copilotIntentHeader:    copilotIntent,
		copilotInitiatorHeader: copilotInitiatorUser,
	}
	if request == nil || len(request.Input) == 0 {
		return headers
	}

	// A turn whose final message is not from the user is an agent continuation
	// (tool result being fed back, or assistant prefill).
	if request.Input[len(request.Input)-1].Role != schemas.ChatMessageRoleUser {
		headers[copilotInitiatorHeader] = copilotInitiatorAgent
	}

	for i := range request.Input {
		content := request.Input[i].Content
		if content == nil {
			continue
		}
		for j := range content.ContentBlocks {
			if content.ContentBlocks[j].Type == schemas.ChatContentBlockTypeImage {
				headers[copilotVisionHeader] = "true"
				return headers
			}
		}
	}
	return headers
}

// responsesRequestHeaders derives the per-request Copilot headers for a
// Responses API request.
func responsesRequestHeaders(request *schemas.BifrostResponsesRequest) map[string]string {
	headers := map[string]string{
		copilotIntentHeader:    copilotIntent,
		copilotInitiatorHeader: copilotInitiatorUser,
	}
	if request == nil || len(request.Input) == 0 {
		return headers
	}

	// Responses items that are not messages (e.g. function_call_output) carry no
	// role at all, which is itself the agent-continuation signal.
	last := request.Input[len(request.Input)-1]
	if last.Role == nil || *last.Role != schemas.ResponsesInputMessageRoleUser {
		headers[copilotInitiatorHeader] = copilotInitiatorAgent
	}

	for i := range request.Input {
		content := request.Input[i].Content
		if content == nil {
			continue
		}
		for j := range content.ContentBlocks {
			if content.ContentBlocks[j].Type == schemas.ResponsesInputMessageContentBlockTypeImage {
				headers[copilotVisionHeader] = "true"
				return headers
			}
		}
	}
	return headers
}

// tokenExpiryMargin is the number of seconds before expiry to trigger a refresh.
const tokenExpiryMargin = 60

// fallbackTokenTTL bounds how long a JWT is cached when the token exchange
// response carries no usable expires_at. Kept short so a wrong guess costs an
// extra exchange rather than a window of requests with a dead token.
const fallbackTokenTTL = 5 * time.Minute

// tokenManagerSwapAttempts caps how many times a caller retries installing its own
// token manager before giving up on caching and using an uncached one. Must be >= 1.
const tokenManagerSwapAttempts = 3

// tokenExchangeMaxResponseBytes bounds the HTTP response size for the Copilot
// token-exchange endpoint. The legitimate response is a small JSON document
// (~1 KB); 64 KiB leaves generous slack while preventing a hostile or
// misbehaving upstream from forcing arbitrary allocations at the transport
// layer (fasthttp's default cap is 4 MiB).
const tokenExchangeMaxResponseBytes = 64 * 1024

// isValidCopilotAPIBase validates that a Copilot API base URL is safe to use.
// It must use HTTPS and belong to a known GitHub Copilot domain to prevent SSRF.
// Uses u.Hostname() (not u.Host) so URLs with an explicit port — e.g. enterprise
// or proxied Copilot deployments returning "api.githubcopilot.com:443" — are not
// silently rejected by the suffix check.
func isValidCopilotAPIBase(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := u.Hostname()
	return strings.HasSuffix(host, ".githubcopilot.com") ||
		strings.HasSuffix(host, ".github.com")
}
