package warp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/mcptools"
)

var (
	// ErrNoModelClient is returned when Warp is configured but has no way to
	// reach a model, which only happens when the service was built without a log
	// reader and therefore without a client.
	ErrNoModelClient = errors.New("warp has no model client available")
	// ErrConversationTooLong is returned when the client-sent history exceeds
	// MaxHistoryBytes. The dashboard starts a new thread in response.
	ErrConversationTooLong = errors.New("conversation is too long")
	// ErrBadConversationID is returned for a conversation id the store cannot
	// hold. Accepted, an oversized id is treated as an existing thread, the
	// model request is paid for, and the append then fails against the
	// varchar(36) column - persistTurn returns an empty id and the turn
	// silently vanishes from history.
	ErrBadConversationID = errors.New("conversation_id is too long")
)

// MaxConversationIDChars matches the warp_conversations id column
// (varchar(36)). Not a UUID check: the local contract accepts ids such as
// "thread-1", only never one the store would truncate or reject.
const MaxConversationIDChars = 36

// Turn is one validated question with everything the loop needs to answer it.
// A transport builds it with NewTurn, snapshots the request context using
// Budget, and hands both to RunTurn.
type Turn struct {
	// Budget is the whole loop's allowance: iterations x per-call timeout.
	// Anything slower is hung, not slow, and holding the connection open past
	// that helps nobody.
	Budget time.Duration
	// ConversationID is the thread this turn belongs to. It is settled before
	// the first model call rather than after the last one, because it travels
	// upstream as a logging header: Warp's own traffic is logged like any other
	// request, and without the id on the way out there is nothing to group those
	// rows by afterwards - a thread's five model calls would sit in the log table
	// as five unrelated requests.
	ConversationID string
	// IsNew records that this turn starts the thread, so history knows to create
	// it. The id alone cannot say so any more, since it is never empty here.
	IsNew bool

	question string
	// questionsAsked is how many clarifying questions this thread has already
	// had in a row, read off the replayed conversation.
	questionsAsked int
	// questionRole is the role the final request message actually carried.
	// NewTurn accepts an assistant turn there, and history has to file it under
	// the role it arrived with rather than assuming "user".
	questionRole string
	messages     []schemas.ResponsesMessage
	config       *schemas.WarpConfig
	chat         ChatFunc
	// hasSemantic records whether a semantic searcher existed when the turn
	// was resolved. Without one semantic_search_logs can only refuse, so it
	// is not offered to the model at all.
	hasSemantic      bool
	utcOffsetMinutes int
	timezone         string
}

// NewTurn validates a chat request and resolves the configuration and model
// client it will run against. ctx is used only for the synchronous config read.
//
// bodyBytes is the raw request size. History is stateless by design - the
// dashboard holds the thread - which means the body is attacker-influenced and
// needs a ceiling.
func (s *Service) NewTurn(ctx context.Context, request *ChatRequest, bodyBytes int) (*Turn, error) {
	config, err := s.Config(ctx)
	if err != nil {
		return nil, err
	}
	messages, err := Conversation(request.Messages)
	if err != nil {
		return nil, err
	}
	if bodyBytes > MaxHistoryBytes {
		return nil, fmt.Errorf("%w: %d bytes exceeds the %d byte limit", ErrConversationTooLong, bodyBytes, MaxHistoryBytes)
	}
	conversationID := strings.TrimSpace(request.ConversationID)
	// Refused before the model runs, not discovered at the append after it.
	if len(conversationID) > MaxConversationIDChars {
		return nil, fmt.Errorf("%w: %d characters exceeds the %d character limit", ErrBadConversationID, len(conversationID), MaxConversationIDChars)
	}
	isNew := conversationID == ""
	if isNew {
		conversationID = uuid.NewString()
	}
	// One snapshot for both, so a turn cannot keep a usable chat func while the
	// reader went nil underneath it.
	chat, logs, semantic := s.turnDeps(ctx, config, conversationID)
	if chat == nil {
		return nil, ErrNoModelClient
	}
	// Refused here rather than mid-turn: CanChat already reports a Warp with no
	// log reader as unavailable, and failing now gives the caller the same 503
	// reason the route reports instead of a turn that cannot answer anything.
	// The same holds for the MCP dispatch and discovery functions: every tool
	// runs through them, so a turn without them cannot run a single one.
	if logs == nil || s.mcp == nil || s.mcpTools == nil {
		return nil, ErrUnavailable
	}
	return &Turn{
		Budget:         time.Duration(config.EffectiveMaxIterations()*config.EffectiveRequestTimeoutSeconds()) * time.Second,
		ConversationID: conversationID,
		IsNew:          isNew,
		// The question is the last turn; history is everything before it.
		question:       request.Messages[len(request.Messages)-1].Content,
		questionRole:   request.Messages[len(request.Messages)-1].Role,
		questionsAsked: consecutiveQuestions(request.Messages),
		messages:       messages,
		config:         config,
		chat:           chat,
		hasSemantic:    semantic != nil,
		// Sanitized here, once, since this is the one place a raw client value
		// exists - everything downstream (NewAgent, systemInstructions) trusts
		// what it's handed rather than re-validating.
		utcOffsetMinutes: sanitizeUTCOffsetMinutes(request.UTCOffsetMinutes),
		timezone:         sanitizeTimezone(request.Timezone),
	}, nil
}

// RunTurn drives the agent, folds its events into one response and files the
// exchange in history. The thread id is stamped onto the done frame and onto
// the response, so a client that started a new thread learns what to send next
// without a second request.
//
// ctx must already carry the caller's query scope and identity: every tool
// executes against it, and queryscope treats a missing scope as "no restriction",
// so a context that lost it returns the whole deployment to whoever asked.
//
// sink, when non-nil, sees every event as it happens; that is the streaming
// transport. Returning false from sink stops the loop - the client is gone, and
// there is no point paying the provider for an answer nobody will read. The
// buffered transport passes nil and uses only the returned response.
func (s *Service) RunTurn(ctx context.Context, turn *Turn, sink func(Event) bool) ChatResponse {
	runCtx, stop := context.WithCancel(ctx)
	defer stop()

	// The scope is read off the snapshotted context, same as the row-level
	// queryscope, so it is a fact about who asked rather than anything the
	// request body could claim.
	tools := s.mcpTools
	if !turn.hasSemantic {
		tools = withoutTool(tools, mcptools.SemanticSearchToolName)
	}
	agent := NewAgent(turn.chat, s.costFuncFor(turn.config), s.mcp, tools, ScopeFromContext(runCtx), turn.config, turn.utcOffsetMinutes, turn.timezone)
	agent.questionsAsked = turn.questionsAsked
	events := make(chan Event, 16)
	go agent.Run(runCtx, turn.messages, events)

	f := newFold()
	for event := range events {
		f.apply(event)
		// Both terminal frames file the turn and carry the id, because both are
		// the last thing a streaming client will see. An error frame without it
		// left the client unable to name the thread its failed exchange was filed
		// under, so the next question opened a new one and the failure was
		// orphaned in a history the client could not reach.
		if event.Type == EventDone || event.Type == EventError {
			f.response.ConversationID = s.recordTurn(runCtx, turn, f.result())
			event.ConversationID = f.response.ConversationID
		}
		if sink != nil && !sink(event) {
			// Cancelling unblocks the agent's next emit so the goroutine exits;
			// draining is not needed because Run selects on ctx.Done. Anything
			// already filed stays filed - a thread the reader never saw is still
			// worth keeping.
			stop()
			return f.result()
		}
	}
	// Only for a run that ended without any terminal frame at all - the buffered
	// path, or an agent that stopped emitting. A streamed error already filed
	// itself above, and filing again would store the exchange twice.
	if !f.sawDone && f.response.ConversationID == "" {
		f.response.ConversationID = s.recordTurn(runCtx, turn, f.result())
	}
	return f.result()
}
