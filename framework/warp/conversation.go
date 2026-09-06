package warp

import (
	"errors"

	"github.com/maximhq/bifrost/core/schemas"
)

// ChatRequest is the chat body. Conversation history is client-sent and the
// server keeps no session: the dashboard already holds the thread, and a session
// table would need TTLs, cleanup and cross-node coordination for no user-visible
// gain at this scale.
type ChatRequest struct {
	Messages []ChatMessage `json:"messages"`
	// Stream selects the transport, not the behaviour. Both paths run the same
	// loop; only the sink differs.
	Stream *bool `json:"stream,omitempty"`
}

// ChatMessage is one client-sent turn.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatResponse is the non-streaming body: the same events, assembled.
type ChatResponse struct {
	Answer       string                   `json:"answer"`
	ToolCalls    []ChatToolCall           `json:"tool_calls"`
	Iterations   int                      `json:"iterations"`
	FinishReason string                   `json:"finish_reason,omitempty"`
	Usage        *schemas.BifrostLLMUsage `json:"usage,omitempty"`
	Error        *ChatError               `json:"error,omitempty"`
}

// ChatToolCall summarises one tool call the agent made while answering.
type ChatToolCall struct {
	Name       string `json:"name"`
	Arguments  string `json:"arguments,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	Failed     bool   `json:"failed,omitempty"`
}

// ChatError is the terminal error of a turn, when it had one.
type ChatError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

var (
	// ErrEmptyConversation is returned for a request with no turns.
	ErrEmptyConversation = errors.New("messages must contain at least one turn")
	// ErrBadRole is returned when a turn carries a role the agent does not accept.
	ErrBadRole = errors.New("message roles must be user or assistant")
)

// Conversation validates and converts the client's history.
func Conversation(messages []ChatMessage) ([]schemas.ChatMessage, error) {
	if len(messages) == 0 {
		return nil, ErrEmptyConversation
	}
	if len(messages) > MaxHistoryMessages {
		// Trim from the front, keeping the first turn. The opening question
		// usually carries the framing everything after it depends on, so dropping
		// it is worse than dropping the middle.
		messages = append(messages[:1], messages[len(messages)-(MaxHistoryMessages-1):]...)
	}
	converted := make([]schemas.ChatMessage, 0, len(messages))
	for _, message := range messages {
		role := schemas.ChatMessageRole(message.Role)
		if role != schemas.ChatMessageRoleUser && role != schemas.ChatMessageRoleAssistant {
			return nil, ErrBadRole
		}
		content := message.Content
		converted = append(converted, schemas.ChatMessage{
			Role:    role,
			Content: &schemas.ChatMessageContent{ContentStr: &content},
		})
	}
	return converted, nil
}
