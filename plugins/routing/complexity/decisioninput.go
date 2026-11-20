package complexity

import "github.com/maximhq/bifrost/framework/configstore"

// DecisionPreviousMessageCount is how many earlier user messages the decision
// model reads with the current one: the configured count, or the default
// when none is set, within the bounds the window allows. The request path
// and a suggestion run's backfill both read it, so a backfilled request is
// embedded from the same turns the decision model saw.
func DecisionPreviousMessageCount(cfg *DecisionConfig) int {
	count := configstore.DefaultComplexityDecisionPreviousMessageCount
	if cfg != nil && cfg.PreviousMessageCount != nil {
		count = *cfg.PreviousMessageCount
	}
	return min(max(count, 0), configstore.MaxComplexityDecisionPreviousMessageCount)
}

// DecisionConversationWindow returns the current user request and a bounded number of earlier user messages.
func DecisionConversationWindow(input ComplexityInput, previousMessageCount int) []ConversationMessage {
	previousMessageCount = min(max(previousMessageCount, 0), configstore.MaxComplexityDecisionPreviousMessageCount)

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
