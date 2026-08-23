package zhipu

import "strings"

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
// with a General API key are rejected upstream with 401. Any other base URL falls
// back to appending /anthropic; users on exotic hosts can always create a second
// provider instance with an explicit base URL.
//
// Idempotent: a base that already ends with the mount suffix is returned unchanged —
// use_anthropic_endpoints with a base_url set to the mount itself must not append
// /anthropic a second time.
func deriveAnthropicBaseURL(openAIBaseURL string) string {
	base := strings.TrimRight(openAIBaseURL, "/")
	if strings.HasSuffix(base, anthropicMount) {
		return base
	}
	if strings.HasSuffix(base, codingPlanSuffix) {
		return strings.TrimSuffix(base, codingPlanSuffix) + anthropicMount
	}
	if strings.HasSuffix(base, generalAPISuffix) {
		return strings.TrimSuffix(base, generalAPISuffix) + anthropicMount
	}
	return base + anthropicMount
}
