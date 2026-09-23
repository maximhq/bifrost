package warp

import (
	"errors"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// ChatRequest is the POST body. Conversation history is client-sent and the
// server keeps no session: the dashboard already holds the thread, and a session
// table would need TTLs, cleanup and cross-node coordination for no user-visible
// gain at this scale.
type ChatRequest struct {
	Messages []ChatMessage `json:"messages"`
	// ConversationID continues an existing thread. Empty starts a new one, and
	// the id of the thread that was created comes back on the done event.
	ConversationID string `json:"conversation_id,omitempty"`
	// Stream selects the transport, not the behaviour. Both paths run the same
	// loop; only the sink differs.
	Stream *bool `json:"stream,omitempty"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// Question marks an assistant turn that was a clarifying question rather
	// than an answer. The dashboard already records this per turn and replays
	// it, which is what lets the server cap how many times in a row Warp may
	// ask. Client-supplied, so it bounds a conversation's shape rather than
	// enforcing a permission - there is nothing here worth lying about.
	Question bool `json:"question,omitempty"`
}

// consecutiveQuestions counts the clarifying questions at the tail of a
// conversation, stopping at the first assistant turn that was a real answer.
//
// A model that keeps asking never gets anywhere, and Run only stops it within a
// single turn - the next request builds a fresh agent, so without this the loop
// can continue for as long as somebody keeps replying.
func consecutiveQuestions(messages []ChatMessage) int {
	count := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "assistant" {
			continue
		}
		// Skipped, not counted as an answer. Conversation drops an empty assistant
		// turn before it ever reaches the model - that is what a failed turn looks
		// like on replay - so treating it as a real reply here reset the streak
		// against a message the model was never shown, and the agent could ask
		// past MaxConsecutiveQuestions.
		// Skipped, not counted as an answer. Conversation drops an empty assistant
		// turn before it ever reaches the model - that is what a failed turn looks
		// like on replay - so treating it as a real reply here reset the streak
		// against a message the model was never shown, and the agent could ask
		// past MaxConsecutiveQuestions.
		if strings.TrimSpace(messages[i].Content) == "" {
			continue
		}
		if !messages[i].Question {
			break
		}
		count++
	}
	return count
}

// ChatResponse is the non-streaming body: the same events, assembled.
type ChatResponse struct {
	Answer         string                   `json:"answer"`
	ToolCalls      []ChatToolCall           `json:"tool_calls"`
	Iterations     int                      `json:"iterations"`
	ConversationID string                   `json:"conversation_id,omitempty"`
	FinishReason   string                   `json:"finish_reason,omitempty"`
	Usage          *schemas.BifrostLLMUsage `json:"usage,omitempty"`
	// Question is set when the turn ended by asking rather than answering. The
	// JSON transport needs it to show the picker, and history needs it so the
	// thread is filed from its first turn.
	Question *Question  `json:"question,omitempty"`
	Error    *ChatError `json:"error,omitempty"`
}

type ChatToolCall struct {
	Name       string `json:"name"`
	Arguments  string `json:"arguments,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	Failed     bool   `json:"failed,omitempty"`
}

type ChatError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Conversation validates and converts the client's history.
func Conversation(messages []ChatMessage) ([]schemas.ResponsesMessage, error) {
	if len(messages) == 0 {
		return nil, ErrEmptyConversation
	}
	converted := make([]schemas.ResponsesMessage, 0, len(messages))
	itemType := schemas.ResponsesMessageTypeMessage
	for _, message := range messages {
		role := schemas.ResponsesMessageRoleType(message.Role)
		if role != schemas.ResponsesInputMessageRoleUser && role != schemas.ResponsesInputMessageRoleAssistant {
			return nil, ErrBadRole
		}
		content := message.Content
		// An empty turn is dropped, not replayed. A turn that failed is recorded
		// with an error and no text, so the client sends it back as an assistant
		// message with empty content - and Anthropic rejects that outright:
		// "messages: text content blocks must be non-empty". One failed turn would
		// otherwise poison the whole thread, with every retry failing on the
		// previous failure rather than on anything the retry itself did.
		//
		// Dropping rather than erroring is deliberate: an empty turn carries no
		// information, so there is nothing to tell the caller about and nothing
		// lost by leaving it out.
		if strings.TrimSpace(content) == "" {
			continue
		}
		converted = append(converted, schemas.ResponsesMessage{
			Type:    &itemType,
			Role:    &role,
			Content: &schemas.ResponsesMessageContent{ContentStr: &content},
		})
	}
	if len(converted) == 0 {
		return nil, ErrEmptyConversation
	}
	// Trimmed after the empties are dropped, not before. The cap bounds what is
	// actually replayed, and a failed turn comes back with no text - so counting
	// those against the budget threw away real turns there was room for.
	//
	// From the front, keeping the first turn: the opening question usually
	// carries the framing everything after it depends on, so dropping it is
	// worse than dropping the middle.
	if len(converted) > MaxHistoryMessages {
		converted = append(converted[:1], converted[len(converted)-(MaxHistoryMessages-1):]...)
	}
	// The final turn is the question being asked, so it is required rather than
	// droppable. Dropping it left the agent answering the previous message while
	// history filed a blank bubble under a blank title - an exchange that looks
	// like it worked and answers something nobody asked. Checked after the loop
	// so a wholly blank request still reports the more useful "empty
	// conversation" rather than singling out its last line.
	if strings.TrimSpace(messages[len(messages)-1].Content) == "" {
		return nil, ErrEmptyFinalTurn
	}
	return converted, nil
}

var (
	// ErrEmptyConversation is returned for a request with no turns.
	ErrEmptyConversation = errors.New("messages must contain at least one turn")
	// ErrBadRole is returned when a turn carries a role the agent does not accept.
	ErrBadRole = errors.New("message roles must be user or assistant")
	// ErrEmptyFinalTurn is returned when the message being asked is blank.
	ErrEmptyFinalTurn = errors.New("the final message must not be empty")
)

// Conversation validates and converts the client's history.
