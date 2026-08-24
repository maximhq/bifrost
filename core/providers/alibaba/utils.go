package alibaba

import (
	"net/url"
	"strings"
)

const (
	// defaultBaseURL is the Alibaba Cloud Model Studio international (Singapore) legacy
	// host, OpenAI-compatible mount. Token Plan users override it with
	// https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1, and
	// workspace-dedicated hosts follow https://{WorkspaceId}.{region}.maas.aliyuncs.com/compatible-mode/v1.
	defaultBaseURL = "https://dashscope-intl.aliyuncs.com/compatible-mode/v1"

	// chatCompletionsPath is the chat completions path relative to the OpenAI-compatible base URL.
	chatCompletionsPath = "/chat/completions"

	// modelsPath is the list-models path relative to the OpenAI-compatible base URL.
	modelsPath = "/models"

	// responsesPath is the Responses API path relative to the OpenAI-compatible base URL.
	responsesPath = "/responses"

	// embeddingsPath is the embeddings path relative to the OpenAI-compatible base URL.
	embeddingsPath = "/embeddings"

	// compatibleModeSuffix is the pay-as-you-go / Token Plan OpenAI mount suffix.
	compatibleModeSuffix = "/compatible-mode/v1"

	// anthropicMount is the Anthropic mount suffix on all Model Studio hosts.
	anthropicMount = "/apps/anthropic"

	// anthropicMessagesPath is the messages path under the derived Anthropic mount base.
	anthropicMessagesPath = "/v1/messages"
)

// isKnownAlibabaHost reports whether the base URL sits on one of Alibaba
// Cloud's own Model Studio hosts. Every host shape — dashscope[-intl|-us],
// coding[-intl].dashscope, and the {WorkspaceId}.{region}.maas workspace and
// token-plan hosts — lives under aliyuncs.com, so the check is a domain-boundary
// suffix match. Unparseable or scheme-less inputs are treated as custom hosts.
func isKnownAlibabaHost(base string) bool {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	return parsed.Host == "aliyuncs.com" || strings.HasSuffix(parsed.Host, ".aliyuncs.com")
}

// deriveAnthropicBaseURL derives the Anthropic-compatible mount base URL from the
// configured OpenAI-compatible base URL.
//
// Every Model Studio host shape mounts Messages at /apps/anthropic:
//
//   - https://dashscope-intl.aliyuncs.com/compatible-mode/v1
//     -> https://dashscope-intl.aliyuncs.com/apps/anthropic
//   - https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1
//     -> https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic
//   - https://{WorkspaceId}.{region}.maas.aliyuncs.com/compatible-mode/v1
//     -> https://{WorkspaceId}.{region}.maas.aliyuncs.com/apps/anthropic
//   - Coding Plan hosts (coding[-intl].dashscope.aliyuncs.com/v1)
//     -> same host + /apps/anthropic
//
// The path rewrites only apply on Alibaba's own hosts (*.aliyuncs.com). Any other
// base URL — including a custom or proxied base that merely ends in
// /compatible-mode/v1 or /v1 — keeps its configured path and only gets
// /apps/anthropic appended, so a foreign URL shape is never silently rewritten;
// users on exotic hosts can always create a second provider instance with an
// explicit base URL.
//
// Idempotent: a base that already ends with the mount suffix is returned unchanged —
// use_anthropic_endpoints with a base_url set to the mount itself must not append
// /apps/anthropic a second time.
func deriveAnthropicBaseURL(openAIBaseURL string) string {
	base := strings.TrimRight(openAIBaseURL, "/")
	if strings.HasSuffix(base, anthropicMount) {
		return base
	}
	if !isKnownAlibabaHost(base) {
		return base + anthropicMount
	}
	if strings.HasSuffix(base, compatibleModeSuffix) {
		return strings.TrimSuffix(base, compatibleModeSuffix) + anthropicMount
	}
	if strings.HasSuffix(base, "/v1") {
		return strings.TrimSuffix(base, "/v1") + anthropicMount
	}
	return base + anthropicMount
}
