package alibaba

import "strings"

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
// Any other base URL falls back to appending /apps/anthropic; users on exotic hosts
// can always create a second provider instance with an explicit base URL.
func deriveAnthropicBaseURL(openAIBaseURL string) string {
	base := strings.TrimRight(openAIBaseURL, "/")
	if strings.HasSuffix(base, compatibleModeSuffix) {
		return strings.TrimSuffix(base, compatibleModeSuffix) + anthropicMount
	}
	if strings.HasSuffix(base, "/v1") {
		return strings.TrimSuffix(base, "/v1") + anthropicMount
	}
	return base + anthropicMount
}
