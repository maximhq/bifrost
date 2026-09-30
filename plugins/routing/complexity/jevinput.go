package complexity

import "github.com/maximhq/bifrost/framework/configstore"

// JevConversationWindow returns the current user request and a bounded number of earlier user messages.
func JevConversationWindow(input ComplexityInput, previousMessageCount int) []ConversationMessage {
	if previousMessageCount < 0 {
		previousMessageCount = 0
	}
	if previousMessageCount > configstore.MaxComplexityJevPreviousMessageCount {
		previousMessageCount = configstore.MaxComplexityJevPreviousMessageCount
	}

	conversation := input.Conversation
	latestUser := -1
	for i := len(conversation) - 1; i >= 0; i-- {
		if conversation[i].Role == "user" {
			latestUser = i
			break
		}
	}
	if latestUser < 0 {
		if input.LastUserText == "" {
			return nil
		}
		return []ConversationMessage{{Role: "user", Content: input.LastUserText}}
	}

	start := latestUser
	remaining := previousMessageCount
	for i := latestUser - 1; i >= 0 && remaining > 0; i-- {
		if conversation[i].Role == "user" {
			start = i
			remaining--
		}
	}

	window := make([]ConversationMessage, 0, previousMessageCount+1)
	for i := start; i <= latestUser; i++ {
		if conversation[i].Role == "user" {
			window = append(window, conversation[i])
		}
	}
	return window
}
