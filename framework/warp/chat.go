package warp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

var (
	// ErrNoModelClient is returned when Warp is configured but has no way to
	// reach a model, which only happens when the service was built without a log
	// reader and therefore without a client.
	ErrNoModelClient = errors.New("warp has no model client available")
	// ErrConversationTooLong is returned when the client-sent history exceeds
	// MaxHistoryBytes. The dashboard starts a new thread in response.
	ErrConversationTooLong = errors.New("conversation is too long")
)

// Turn is one validated question with everything the loop needs to answer it.
// A transport builds it with NewTurn, snapshots the request context using
// Budget, and hands both to RunTurn.
type Turn struct {
	// Budget is the whole loop's allowance: iterations x per-call timeout.
	// Anything slower is hung, not slow, and holding the connection open past
	// that helps nobody.
	Budget time.Duration
	// ConversationID is the thread this turn continues, or empty for a new one.
	ConversationID string

	question string
	messages []schemas.ChatMessage
	config   *schemas.WarpConfig
	chat     ChatFunc
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
	chat := s.chatFuncFor(ctx, config)
	if chat == nil {
		return nil, ErrNoModelClient
	}
	return &Turn{
		Budget:         time.Duration(config.EffectiveMaxIterations()*config.EffectiveRequestTimeoutSeconds()) * time.Second,
		ConversationID: strings.TrimSpace(request.ConversationID),
		// The question is the last turn; history is everything before it.
		question: request.Messages[len(request.Messages)-1].Content,
		messages: messages,
		config:   config,
		chat:     chat,
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
	agent := NewAgent(turn.chat, s.logs, ScopeFromContext(runCtx), turn.config)
	events := make(chan Event, 16)
	go agent.Run(runCtx, turn.messages, events)

	f := newFold()
	for event := range events {
		f.apply(event)
		if event.Type == EventDone {
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
	if !f.sawDone {
		// An error-terminal run has no done frame to carry the id, but the
		// exchange is still worth filing: what was asked, and how it failed.
		f.response.ConversationID = s.recordTurn(runCtx, turn, f.result())
	}
	return f.result()
}
