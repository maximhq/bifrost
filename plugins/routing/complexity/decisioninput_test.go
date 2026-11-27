package complexity

import (
	"reflect"
	"testing"
)

// TestDecisionConversationWindowSendsOnlyUserMessages checks that the configured
// history count includes earlier user turns but never assistant messages.
func TestDecisionConversationWindowSendsOnlyUserMessages(t *testing.T) {
	input := ComplexityInput{
		LastUserText: "current question",
		Conversation: []ConversationMessage{
			{Role: "user", Content: "older question"},
			{Role: "assistant", Content: "older answer"},
			{Role: "user", Content: "previous question"},
			{Role: "assistant", Content: "previous answer"},
			{Role: "user", Content: "current question"},
		},
	}
	want := []ConversationMessage{
		{Role: "user", Content: "previous question"},
		{Role: "user", Content: "current question"},
	}
	if got := DecisionConversationWindow(input, 1); !reflect.DeepEqual(got, want) {
		t.Fatalf("DecisionConversationWindow() = %#v, want %#v", got, want)
	}
}

// TestDecisionConversationWindowZeroHistoryKeepsCurrentRequest checks the zero-history boundary.
func TestDecisionConversationWindowZeroHistoryKeepsCurrentRequest(t *testing.T) {
	input := ComplexityInput{
		LastUserText: "current question",
		Conversation: []ConversationMessage{
			{Role: "user", Content: "previous question"},
			{Role: "assistant", Content: "previous answer"},
			{Role: "user", Content: "current question"},
		},
	}
	want := []ConversationMessage{{Role: "user", Content: "current question"}}
	if got := DecisionConversationWindow(input, 0); !reflect.DeepEqual(got, want) {
		t.Fatalf("DecisionConversationWindow() = %#v, want %#v", got, want)
	}
}
