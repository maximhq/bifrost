package zhipu

import (
	"net/url"
	"strings"
)

const (
	// defaultBaseURL is the Z.AI international General API (pay-as-you-go) base URL.
	// GLM Coding Plan users override it with https://api.z.ai/api/coding/paas/v4;
	// CN BigModel platform users with https://open.bigmodel.cn/api/paas/v4.
	defaultBaseURL = "https://api.z.ai/api/paas/v4"

	// chatCompletionsPath is the chat completions path relative to the OpenAI-compatible base URL.
	chatCompletionsPath = "/chat/completions"

	// modelsPath is the list-models path relative to the OpenAI-compatible base URL.
	modelsPath = "/models"

	// codingPlanSuffix is the Coding Plan OpenAI mount suffix.
	codingPlanSuffix = "/coding/paas/v4"

	// generalAPISuffix is the General API OpenAI mount suffix.
	generalAPISuffix = "/paas/v4"

	// anthropicMount is the Anthropic mount suffix (Coding Plan surface only).
	anthropicMount = "/anthropic"

	// anthropicMessagesPath is the messages path under the derived Anthropic mount base.
	anthropicMessagesPath = "/v1/messages"
)

// zhipuKnownHosts are the upstream hosts whose URL shapes the suffix rewrites
// below rely on. Custom or proxied hosts never get their paths rewritten.
var zhipuKnownHosts = map[string]bool{
	"api.z.ai":         true, // General API + Coding Plan (international)
	"open.bigmodel.cn": true, // BigModel platform (China)
}

// isKnownZhipuHost reports whether the base URL sits on one of Zhipu's own hosts.
// Unparseable or scheme-less inputs are treated as custom hosts.
func isKnownZhipuHost(base string) bool {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	return zhipuKnownHosts[parsed.Host]
}

// deriveAnthropicBaseURL derives the Anthropic-compatible mount base URL from the
// configured OpenAI-compatible base URL.
//
// Both the General API and Coding Plan OpenAI mounts resolve to the same
// Anthropic mount host path:
//
//   - https://api.z.ai/api/coding/paas/v4 -> https://api.z.ai/api/anthropic
//   - https://api.z.ai/api/paas/v4        -> https://api.z.ai/api/anthropic
//
// (same for open.bigmodel.cn). The mount requires a Coding Plan key; requests made
// with a General API key are rejected upstream with 401. The path rewrites only
// apply on Zhipu's own hosts — any other base URL, including a custom or proxied
// base that merely ends in /coding/paas/v4 or /paas/v4, keeps its configured path
// and only gets /anthropic appended; users on exotic hosts can always create a
// second provider instance with an explicit base URL.
//
// Idempotent: a base that already ends with the mount suffix is returned unchanged —
// use_anthropic_endpoints with a base_url set to the mount itself must not append
// /anthropic a second time.
func deriveAnthropicBaseURL(openAIBaseURL string) string {
	base := strings.TrimRight(openAIBaseURL, "/")
	if strings.HasSuffix(base, anthropicMount) {
		return base
	}
	if !isKnownZhipuHost(base) {
		return base + anthropicMount
	}
	if strings.HasSuffix(base, codingPlanSuffix) {
		return strings.TrimSuffix(base, codingPlanSuffix) + anthropicMount
	}
	if strings.HasSuffix(base, generalAPISuffix) {
		return strings.TrimSuffix(base, generalAPISuffix) + anthropicMount
	}
	return base + anthropicMount
}
