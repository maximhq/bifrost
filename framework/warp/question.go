package warp

import (
	"context"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
)

// Clarifying questions.
//
// Two things make a metric question answerable: when, and whose. Warp can guess
// at neither safely - "what did we spend?" over an unstated window and an
// unstated scope has a dozen correct answers, and picking one silently produces
// a confident number about something nobody asked for.
//
// Rather than a free-text "which period did you mean?", Warp poses a structured
// question with options the UI renders as a picker. Typing "last month" is
// slower than pressing a key, and a picker also means the answer comes back in a
// form Warp can use directly instead of one it has to re-interpret.
//
// The exchange is turn-based, not blocking. The tool records the question, the
// agent finishes its turn, and the answer arrives as an ordinary next message.
// That fits the stateless client-sent history exactly: no second channel, no
// correlation ids, no request held open while someone reads.

const (
	// AskUserTool is the tool name the model calls to pose a question.
	AskUserTool = "ask_user"

	// MaxQuestionOptions bounds an option list. Past this it stops being a
	// picker and becomes a list to read, which is what the question was meant to
	// avoid.
	MaxQuestionOptions = 8
)

// Question is a structured question posed to the person asking.
type Question struct {
	Question string        `json:"question"`
	Options  []QuestionOpt `json:"options"`
	// AllowOther lets the UI keep a free-text escape. Options are a shortcut,
	// never a cage: a list that cannot express what someone meant is worse than
	// no list, because it forces a wrong answer.
	AllowOther bool   `json:"allow_other"`
	Kind       string `json:"kind,omitempty"`
}

type QuestionOpt struct {
	Label string `json:"label"`
	// Hint is the shorter form Warp should receive back, so the answer needs no
	// re-interpretation: "-7d" rather than "Last 7 days".
	Hint string `json:"hint,omitempty"`
}

// AskUserSchema is the tool's argument schema.
//
// kind is advisory: the UI uses it to pick an icon and the model uses it to keep
// its own questions consistent, but nothing branches on it, so an unfamiliar
// value degrades to a plain question rather than an error.
const AskUserSchema = `{
  "type": "object",
  "properties": {
    "question": {"type": "string", "description": "One short question. Ask about one thing only."},
    "kind": {"type": "string", "enum": ["time_range", "scope", "other"]},
    "options": {
      "type": "array",
      "minItems": 2,
      "maxItems": 8,
      "items": {
        "type": "object",
        "properties": {
          "label": {"type": "string", "description": "What the person reads, e.g. 'Last 7 days'."},
          "hint": {"type": "string", "description": "The value you want back, e.g. '-7d' or a team id. Keep it short."}
        },
        "required": ["label"]
      }
    },
    "allow_other": {"type": "boolean", "description": "Whether typing a different answer makes sense. Usually true."}
  },
  "required": ["question", "options"]
}`

// askUserToolDef builds the tool.
//
// Its executor deliberately does no work: posing the question *is* the effect,
// and the value it returns exists only to tell the model to stop talking and
// wait. The agent loop recognises this tool by name and ends the turn.
func askUserToolDef() Tool {
	return Tool{
		name: AskUserTool,
		description: "Ask the person a short multiple-choice question and wait for their answer. " +
			"Use this when a metric question does not say which time range it means, or whose traffic it means - both change the answer, and guessing produces a confident number about the wrong thing. " +
			"Offer concrete options they can pick rather than asking them to type. Ask about one thing at a time, and do not ask again once they have told you.",
		schemaJSON: AskUserSchema,
		execute: func(_ context.Context, _ *ToolDeps, args map[string]any) (any, error) {
			question, err := parseQuestion(args)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"posed":   question.Question,
				"waiting": "The question has been shown. Stop here and wait for the reply; do not answer it yourself or guess.",
			}, nil
		},
	}
}

// parseQuestion validates the model's question.
func parseQuestion(args map[string]any) (*Question, error) {
	text, _ := args["question"].(string)
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("question is required")
	}

	rawOptions, _ := args["options"].([]any)
	if len(rawOptions) < 2 {
		// One option is not a question, it is an assumption with extra steps.
		return nil, fmt.Errorf("give at least two options, or answer without asking")
	}
	if len(rawOptions) > MaxQuestionOptions {
		rawOptions = rawOptions[:MaxQuestionOptions]
	}

	options := make([]QuestionOpt, 0, len(rawOptions))
	for _, raw := range rawOptions {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		label, _ := entry["label"].(string)
		if strings.TrimSpace(label) == "" {
			continue
		}
		hint, _ := entry["hint"].(string)
		options = append(options, QuestionOpt{Label: strings.TrimSpace(label), Hint: strings.TrimSpace(hint)})
	}
	if len(options) < 2 {
		return nil, fmt.Errorf("give at least two options with labels")
	}

	kind, _ := args["kind"].(string)
	allowOther, ok := args["allow_other"].(bool)
	if !ok {
		// Defaulting to true rather than false: a list that cannot express what
		// someone meant forces a wrong answer, and the model omitting the field
		// is not a decision that it should.
		allowOther = true
	}
	return &Question{Question: text, Options: options, AllowOther: allowOther, Kind: kind}, nil
}

// questionFromToolCall extracts a question from a tool call the agent is
// about to execute, or nil when that call is something else.
func questionFromToolCall(name string, arguments string) *Question {
	if name != AskUserTool {
		return nil
	}
	args := map[string]any{}
	if strings.TrimSpace(arguments) != "" {
		if err := sonic.UnmarshalString(arguments, &args); err != nil {
			return nil
		}
	}
	question, err := parseQuestion(args)
	if err != nil {
		return nil
	}
	return question
}

// QuestionGuidance is appended to the system prompt. It lives here beside
// the tool so the rules and the thing they describe cannot drift apart.
const QuestionGuidance = `

Asking before you answer:

- Two things decide a metric answer: which time range, and whose traffic. If the question does not say, ask with ` + AskUserTool + ` rather than choosing for them. A number computed over the wrong window or the wrong scope is not a smaller answer, it is a different one.
- Offer options they can pick. For a time range that is usually: Last 24 hours (-24h), Last 7 days (-7d), Last 30 days (-30d), and a custom range. For scope, use what describe_scope reported - the teams, customers or business units that actually have traffic.
- Ask about one thing at a time. If both the window and the scope are missing, ask the window first, then the scope once they answer.
- Do not ask when you already know. An identified caller's own traffic is the default scope, and a question that names a period ("yesterday", "this month") has already told you the window.
- Never ask more than twice in a row. If it is still unclear, pick the most reasonable option, run the query, and say plainly which one you chose.
- After asking, stop. Do not answer your own question in the same turn.`
