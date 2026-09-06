package warp

import (
	"context"
	"errors"
	"fmt"
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
		Budget:   time.Duration(config.EffectiveMaxIterations()*config.EffectiveRequestTimeoutSeconds()) * time.Second,
		messages: messages,
		config:   config,
		chat:     chat,
	}, nil
}

// RunTurn drives the agent and folds its events into one response.
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

	agent := NewAgent(turn.chat, s.logs, turn.config)
	events := make(chan Event, 16)
	go agent.Run(runCtx, turn.messages, events)

	f := newFold()
	for event := range events {
		f.apply(event)
		if sink != nil && !sink(event) {
			// Cancelling unblocks the agent's next emit so the goroutine exits;
			// draining is not needed because Run selects on ctx.Done.
			stop()
			return f.result()
		}
	}
	return f.result()
}
