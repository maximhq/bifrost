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

// The decision model's earlier-message count is read in one place, by the
// request path and by a suggestion run's backfill alike: the configured
// count, or the default, within the bounds the window allows.
func TestDecisionPreviousMessageCount(t *testing.T) {
	count := func(n int) *DecisionConfig { return &DecisionConfig{PreviousMessageCount: &n} }
	for name, tc := range map[string]struct {
		cfg  *DecisionConfig
		want int
	}{
		"no decision block": {nil, 1},
		"unset":             {&DecisionConfig{}, 1},
		"configured":        {count(3), 3},
		"zero":              {count(0), 0},
		"below the range":   {count(-2), 0},
		"above the range":   {count(9), 5},
	} {
		if got := DecisionPreviousMessageCount(tc.cfg); got != tc.want {
			t.Errorf("%s: got %d, want %d", name, got, tc.want)
		}
	}
}
