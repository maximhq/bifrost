package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// odinChatRequest is the POST body. Conversation history is client-sent and the
// server keeps no session: the dashboard already holds the thread, and a session
// table would need TTLs, cleanup and cross-node coordination for no user-visible
// gain at this scale.
type odinChatRequest struct {
	Messages []odinChatMessage `json:"messages"`
	// ConversationID continues an existing thread. Empty starts a new one, and
	// the id of the thread that was created comes back on the done event.
	ConversationID string `json:"conversation_id,omitempty"`
	// Stream selects the transport, not the behaviour. Both paths run the same
	// loop; only the sink differs.
	Stream *bool `json:"stream,omitempty"`
}

type odinChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// odinChatResponse is the non-streaming body: the same events, assembled.
type odinChatResponse struct {
	Answer         string                   `json:"answer"`
	ToolCalls      []odinChatToolCall       `json:"tool_calls"`
	Iterations     int                      `json:"iterations"`
	ConversationID string                   `json:"conversation_id,omitempty"`
	FinishReason   string                   `json:"finish_reason,omitempty"`
	Usage          *schemas.BifrostLLMUsage `json:"usage,omitempty"`
	Error          *odinChatError           `json:"error,omitempty"`
}

type odinChatToolCall struct {
	Name       string `json:"name"`
	Arguments  string `json:"arguments,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	Failed     bool   `json:"failed,omitempty"`
}

type odinChatError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// snapshotOdinContext lifts everything the agent needs out of the RequestCtx and
// into a plain context.Context.
//
// This is not a tidiness measure. fasthttp recycles *RequestCtx once the handler
// returns, and the agent goroutine outlives it - so reading the scope later
// reads freed memory or nothing at all. And "nothing at all" is the dangerous
// outcome: queryscope.FromContext treats a missing scope as no restriction, so a
// dropped scope silently returns every row in the deployment to whoever asked,
// with no error and no log line.
//
// If you add anything to this function, add it here rather than reading ctx
// inside the goroutine.
func snapshotOdinContext(ctx *fasthttp.RequestCtx, timeout time.Duration) (context.Context, context.CancelFunc) {
	base := context.Background()

	if scope, ok := ctx.UserValue(schemas.BifrostContextKeyQueryScope).(queryscope.QueryScope); ok && scope != nil {
		base = queryscope.WithQueryScope(base, scope)
	}
	for _, key := range []any{
		schemas.IsLocalAdminContextKey,
		schemas.BifrostContextKeyUserID,
		schemas.BifrostContextKeyUserRoleID,
	} {
		if value := ctx.UserValue(key); value != nil {
			base = context.WithValue(base, key, value)
		}
	}
	return context.WithTimeout(base, timeout)
}

// chat is the agent endpoint. It is registered only when a log store exists -
// see OdinService.RegisterChatRoute - so reaching it always means Odin has
// something to read.
func (s *OdinService) chat(ctx *fasthttp.RequestCtx) {
	config, err := s.GetConfig(ctx)
	if err != nil {
		s.sendOdinUnavailable(ctx, schemas.OdinUnavailableNotConfigured,
			"Odin is not configured. Set a provider and model in Settings to enable it.")
		return
	}

	var request odinChatRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &request); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}
	messages, err := odinConversation(request.Messages)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if len(ctx.PostBody()) > odinMaxHistoryBytes {
		SendError(ctx, fasthttp.StatusRequestEntityTooLarge, "Conversation is too long. Start a new chat.")
		return
	}

	chatFunc := s.chatFuncFor(ctx, config)
	if chatFunc == nil {
		s.sendOdinUnavailable(ctx, schemas.OdinUnavailableNotConfigured, "Odin has no model client available.")
		return
	}

	// The whole loop gets iterations x per-call timeout. Anything slower is hung,
	// not slow, and holding the connection open past that helps nobody.
	maxIterations := config.EffectiveMaxIterations()
	budget := time.Duration(maxIterations*config.EffectiveRequestTimeoutSeconds()) * time.Second
	agentCtx, cancel := snapshotOdinContext(ctx, budget)

	agent := &odinAgent{
		chat:  chatFunc,
		tools: buildOdinTools(),
		// The scope is read off the snapshotted context, same as the row-level
		// queryscope, so it is a fact about who asked rather than anything the
		// request body could claim.
		deps:          &odinToolDeps{logManager: s.logManager, scope: odinScopeFromContext(agentCtx)},
		config:        config,
		maxIterations: maxIterations,
	}

	// The question is the last turn; history is everything before it.
	question := request.Messages[len(request.Messages)-1].Content

	if request.Stream != nil && !*request.Stream {
		defer cancel()
		s.chatBuffered(ctx, agent, agentCtx, messages, request.ConversationID, question)
		return
	}
	s.chatStreaming(ctx, agent, agentCtx, cancel, messages, request.ConversationID, question)
}

// chatBuffered drains the event channel and returns one JSON body.
func (s *OdinService) chatBuffered(ctx *fasthttp.RequestCtx, agent *odinAgent, agentCtx context.Context, messages []schemas.ChatMessage, conversationID, question string) {
	events := make(chan odinEvent, 16)
	go agent.run(agentCtx, messages, events)

	response := odinChatResponse{ToolCalls: []odinChatToolCall{}}
	var answer []byte
	pending := map[string]odinChatToolCall{}

	for event := range events {
		switch event.Type {
		case odinEventDelta:
			answer = append(answer, event.Delta...)
		case odinEventToolCallStart:
			pending[event.ToolID] = odinChatToolCall{Name: event.ToolName, Arguments: event.Arguments}
		case odinEventToolCallEnd:
			call := pending[event.ToolID]
			call.Name, call.DurationMs, call.Failed = event.ToolName, event.DurationMs, event.Failed
			response.ToolCalls = append(response.ToolCalls, call)
			delete(pending, event.ToolID)
		case odinEventDone:
			response.FinishReason, response.Iterations, response.Usage = event.FinishReason, event.Iterations, event.Usage
		case odinEventError:
			response.Error = &odinChatError{Code: event.Code, Message: event.Message}
		}
	}
	response.Answer = string(answer)
	response.ConversationID = s.recordOdinTurn(agentCtx, conversationID, question, response)
	SendJSON(ctx, response)
}

// chatStreaming formats each event as an SSE frame.
func (s *OdinService) chatStreaming(ctx *fasthttp.RequestCtx, agent *odinAgent, agentCtx context.Context, cancel context.CancelFunc, messages []schemas.ChatMessage, conversationID, question string) {
	ctx.Response.Header.Set("Content-Type", "text/event-stream")
	ctx.Response.Header.Set("Cache-Control", "no-cache")
	ctx.Response.Header.Set("Connection", "keep-alive")

	reader := lib.NewSSEStreamReader()
	ctx.Response.SetBodyStream(reader, -1)

	events := make(chan odinEvent, 16)
	go agent.run(agentCtx, messages, events)

	go func() {
		defer cancel()
		// A heartbeat forces periodic writes during otherwise-idle gaps. Client
		// disconnects are only detected when a write fails, and a slow upstream
		// turn can leave minutes with no frames to send - long enough for a closed
		// tab to go unnoticed while the request keeps spending tokens.
		heartbeatDone, heartbeatExited := lib.StartSSEHeartbeat(lib.DefaultSSEHeartbeatInterval, reader.SendHeartbeat, cancel)
		defer func() {
			// Must run before reader.Done(): closing the event channel while the
			// heartbeat goroutine could still be mid-send panics.
			lib.StopSSEHeartbeat(reader, heartbeatDone, heartbeatExited)
			reader.Done()
		}()

		// Accumulated so the finished turn can be filed once the stream ends. The
		// SSE writer emits as it goes; history needs the whole answer.
		turn := odinChatResponse{ToolCalls: []odinChatToolCall{}}
		var answer []byte
		pending := map[string]odinChatToolCall{}

		for event := range events {
			switch event.Type {
			case odinEventDelta:
				answer = append(answer, event.Delta...)
			case odinEventToolCallStart:
				pending[event.ToolID] = odinChatToolCall{Name: event.ToolName, Arguments: event.Arguments}
			case odinEventToolCallEnd:
				call := pending[event.ToolID]
				call.Name, call.DurationMs, call.Failed = event.ToolName, event.DurationMs, event.Failed
				turn.ToolCalls = append(turn.ToolCalls, call)
				delete(pending, event.ToolID)
			case odinEventError:
				turn.Error = &odinChatError{Code: event.Code, Message: event.Message}
			case odinEventDone:
				// The id is stamped onto the done frame, so a client that started a
				// new thread learns what to send next without a second request.
				turn.Answer = string(answer)
				event.ConversationID = s.recordOdinTurn(agentCtx, conversationID, question, turn)
			}

			payload, err := sonic.Marshal(event)
			if err != nil {
				continue
			}
			if !reader.SendEvent(string(event.Type), payload) {
				// The client is gone. Cancelling stops the loop and, importantly,
				// stops paying the provider for an answer nobody will read. Anything
				// already filed stays filed - a thread the reader never saw is still
				// worth keeping.
				cancel()
				return
			}
		}
	}()
}

// sendOdinUnavailable answers 503 with a machine-readable reason. The dashboard
// branches on it: an unconfigured Odin stays visible with a link to settings,
// while a deployment with no log store hides the launcher entirely.
func (s *OdinService) sendOdinUnavailable(ctx *fasthttp.RequestCtx, reason schemas.OdinUnavailableReason, message string) {
	SendJSONWithStatus(ctx, schemas.OdinUnavailableResponse{Reason: reason, Message: message}, fasthttp.StatusServiceUnavailable)
}

// odinConversation validates and converts the client's history.
func odinConversation(messages []odinChatMessage) ([]schemas.ChatMessage, error) {
	if len(messages) == 0 {
		return nil, errOdinEmptyConversation
	}
	if len(messages) > odinMaxHistoryMessages {
		// Trim from the front, keeping the first turn. The opening question
		// usually carries the framing everything after it depends on, so dropping
		// it is worse than dropping the middle.
		messages = append(messages[:1], messages[len(messages)-(odinMaxHistoryMessages-1):]...)
	}
	converted := make([]schemas.ChatMessage, 0, len(messages))
	for _, message := range messages {
		role := schemas.ChatMessageRole(message.Role)
		if role != schemas.ChatMessageRoleUser && role != schemas.ChatMessageRoleAssistant {
			return nil, errOdinBadRole
		}
		content := message.Content
		converted = append(converted, schemas.ChatMessage{
			Role:    role,
			Content: &schemas.ChatMessageContent{ContentStr: &content},
		})
	}
	return converted, nil
}

var (
	errOdinEmptyConversation = errors.New("messages must contain at least one turn")
	errOdinBadRole           = errors.New("message roles must be user or assistant")
)
