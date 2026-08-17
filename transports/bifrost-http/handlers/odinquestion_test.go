package handlers

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestOdinQuestionParsing(t *testing.T) {
	t.Run("valid question", func(t *testing.T) {
		question, err := parseOdinQuestion(map[string]any{
			"question": "Which period do you mean?",
			"kind":     "time_range",
			"options": []any{
				map[string]any{"label": "Last 7 days", "hint": "-7d"},
				map[string]any{"label": "Last 30 days", "hint": "-30d"},
			},
		})
		require.NoError(t, err)
		require.Equal(t, "Which period do you mean?", question.Question)
		require.Len(t, question.Options, 2)
		require.Equal(t, "-7d", question.Options[0].Hint)
		require.Equal(t, "time_range", question.Kind)
	})

	// One option is not a question, it is an assumption with extra steps.
	t.Run("rejects a single option", func(t *testing.T) {
		_, err := parseOdinQuestion(map[string]any{
			"question": "Which period?",
			"options":  []any{map[string]any{"label": "Last 7 days"}},
		})
		require.ErrorContains(t, err, "at least two options")
	})

	t.Run("rejects an empty question", func(t *testing.T) {
		_, err := parseOdinQuestion(map[string]any{
			"question": "   ",
			"options":  []any{map[string]any{"label": "a"}, map[string]any{"label": "b"}},
		})
		require.ErrorContains(t, err, "question is required")
	})

	// A list that cannot express what someone meant forces a wrong answer, and
	// the model omitting the field is not a decision that it should.
	t.Run("allows other by default", func(t *testing.T) {
		question, err := parseOdinQuestion(map[string]any{
			"question": "Which team?",
			"options":  []any{map[string]any{"label": "a"}, map[string]any{"label": "b"}},
		})
		require.NoError(t, err)
		require.True(t, question.AllowOther)
	})

	// Past the cap it stops being a picker and becomes a list to read, which is
	// what the question was meant to avoid.
	t.Run("caps the option list", func(t *testing.T) {
		options := make([]any, 0, 20)
		for i := 0; i < 20; i++ {
			options = append(options, map[string]any{"label": string(rune('a' + i))})
		}
		question, err := parseOdinQuestion(map[string]any{"question": "Which?", "options": options})
		require.NoError(t, err)
		require.Len(t, question.Options, odinMaxQuestionOptions)
	})
}

func TestOdinQuestionFromToolCall(t *testing.T) {
	question := odinQuestionFromToolCall(odinAskUserTool,
		`{"question":"Which period?","options":[{"label":"7d","hint":"-7d"},{"label":"30d","hint":"-30d"}]}`)
	require.NotNil(t, question)
	require.Equal(t, "Which period?", question.Question)

	require.Nil(t, odinQuestionFromToolCall("query_metrics", `{}`), "other tools are not questions")
	require.Nil(t, odinQuestionFromToolCall(odinAskUserTool, `{not json`), "malformed args must not panic")
	require.Nil(t, odinQuestionFromToolCall(odinAskUserTool, `{"question":"x","options":[]}`), "an invalid question is not posed")
}

// Posing a question ends the turn. The reply arrives as an ordinary next
// message, which is what keeps the exchange stateless - no second channel, no
// request held open while somebody reads.
func TestOdinAgentEndsTurnOnQuestion(t *testing.T) {
	model := &scriptedOdinModel{turns: []*schemas.BifrostResponsesResponse{
		odinToolTurn("ask-1", odinAskUserTool,
			`{"question":"Which period?","kind":"time_range","options":[{"label":"Last 7 days","hint":"-7d"},{"label":"Last 30 days","hint":"-30d"}]}`),
		odinTextTurn("should never be reached"),
	}}
	agent := newOdinTestAgent(model, &fakeOdinLogManager{}, 8)

	events := collectOdinEvents(t, agent, context.Background())

	require.Equal(t, []odinEventType{odinEventStart, odinEventQuestion, odinEventDone}, eventTypes(events))
	require.Equal(t, 1, model.calls, "the model must not be called again after asking")

	question := events[1].Question
	require.NotNil(t, question)
	require.Equal(t, "Which period?", question.Question)
	require.Len(t, question.Options, 2)
	require.Equal(t, "-7d", question.Options[0].Hint)

	require.Equal(t, "question", events[2].FinishReason,
		"the client needs to tell a question apart from a finished answer")
}

// A malformed question must not silently become an unanswerable turn: it falls
// through to normal tool execution, where the validation error tells the model
// how to fix the call.
func TestOdinAgentTreatsInvalidQuestionAsAToolError(t *testing.T) {
	model := &scriptedOdinModel{turns: []*schemas.BifrostResponsesResponse{
		odinToolTurn("ask-bad", odinAskUserTool, `{"question":"Which?","options":[]}`),
		odinTextTurn("recovered"),
	}}
	agent := newOdinTestAgent(model, &fakeOdinLogManager{}, 8)

	events := collectOdinEvents(t, agent, context.Background())

	require.Equal(t, odinEventToolCallEnd, events[2].Type)
	require.True(t, events[2].Failed)
	require.Equal(t, odinEventDone, events[len(events)-1].Type)
}

func TestOdinPromptCarriesQuestionRules(t *testing.T) {
	content := odinSystemInstructions(&schemas.OdinConfig{})

	require.Contains(t, content, odinAskUserTool)
	require.Contains(t, content, "Ask about one thing at a time")
	require.Contains(t, content, "Last 7 days")
	// Two questions in a row is a conversation; three is an interrogation.
	require.Contains(t, content, "Never ask more than twice in a row")
	require.Contains(t, content, "Do not ask when you already know")
}
