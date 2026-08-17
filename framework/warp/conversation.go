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
}

// ChatResponse is the non-streaming body: the same events, assembled.
type ChatResponse struct {
	Answer         string                   `json:"answer"`
	ToolCalls      []ChatToolCall           `json:"tool_calls"`
	Iterations     int                      `json:"iterations"`
	ConversationID string                   `json:"conversation_id,omitempty"`
	FinishReason   string                   `json:"finish_reason,omitempty"`
	Usage          *schemas.BifrostLLMUsage `json:"usage,omitempty"`
	Error          *ChatError               `json:"error,omitempty"`
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
	if len(messages) > MaxHistoryMessages {
		// Trim from the front, keeping the first turn. The opening question
		// usually carries the framing everything after it depends on, so dropping
		// it is worse than dropping the middle.
		messages = append(messages[:1], messages[len(messages)-(MaxHistoryMessages-1):]...)
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
	return converted, nil
}

var (
	// ErrEmptyConversation is returned for a request with no turns.
	ErrEmptyConversation = errors.New("messages must contain at least one turn")
	// ErrBadRole is returned when a turn carries a role the agent does not accept.
	ErrBadRole = errors.New("message roles must be user or assistant")
)
