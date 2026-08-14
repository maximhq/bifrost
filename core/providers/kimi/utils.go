package kimi

import "strings"

const (
	// defaultBaseURL is the Kimi Open Platform (international) OpenAI-compatible base URL.
	// Kimi Code (subscription) users override it with https://api.kimi.com/coding/v1;
	// CN Open Platform users with https://api.moonshot.cn/v1.
	defaultBaseURL = "https://api.moonshot.ai/v1"

	// chatCompletionsPath is the chat completions path relative to the OpenAI-compatible base URL.
	chatCompletionsPath = "/chat/completions"

	// modelsPath is the list-models path relative to the OpenAI-compatible base URL.
	modelsPath = "/models"

	// openPlatformAnthropicMount is the Anthropic mount suffix on Open Platform hosts
	// (api.moonshot.ai / api.moonshot.cn): /v1 is replaced by /anthropic.
	openPlatformAnthropicMount = "/anthropic"

	// anthropicMessagesPath is the messages path under a derived Anthropic mount base.
	anthropicMessagesPath = "/v1/messages"

	// kimiCodingBaseSuffix marks the Kimi Code OpenAI-compatible base URL. On this
	// mount the Anthropic endpoint shares the same base (messages at /coding/v1/messages).
	kimiCodingBaseSuffix = "/coding/v1"

	// kimiCodingAnthropicMessagesPath is the messages path on the Kimi Code Anthropic mount.
	kimiCodingAnthropicMessagesPath = "/messages"
)

// deriveAnthropicBaseURL derives the Anthropic-compatible mount base URL from the
// configured OpenAI-compatible base URL.
//
//   - Open Platform (api.moonshot.ai / api.moonshot.cn): a trailing /v1 is replaced
//     with /anthropic, so messages live at /anthropic/v1/messages.
//   - Kimi Code (api.kimi.com/coding/v1): the Anthropic mount shares the OpenAI base,
//     with messages at /coding/v1/messages.
//   - Any other base URL falls back to appending /anthropic; users on exotic hosts can
//     always create a second provider instance with an explicit base URL.
func deriveAnthropicBaseURL(openAIBaseURL string) string {
	base := strings.TrimRight(openAIBaseURL, "/")
	if strings.HasSuffix(base, kimiCodingBaseSuffix) {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return strings.TrimSuffix(base, "/v1") + openPlatformAnthropicMount
	}
	return base + openPlatformAnthropicMount
}

// anthropicMessagesPathFor returns the messages path to append to the derived
// Anthropic mount base URL.
func anthropicMessagesPathFor(anthropicBaseURL string) string {
	if strings.HasSuffix(anthropicBaseURL, kimiCodingBaseSuffix) {
		return kimiCodingAnthropicMessagesPath
	}
	return anthropicMessagesPath
}
