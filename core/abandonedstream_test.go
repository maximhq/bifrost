package bifrost

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestDrainAbandonedStream_UnblocksProducer covers the stream the caller never
// receives: the worker's 5s delivery timeout fires, or the client context is
// done, and the stream is dropped.
//
// The provider goroutine on the other end is still sending. GateSendChunk only
// escapes on ctx.Done(), so on the timeout path, where the context is very much
// alive, it blocks forever on a channel nobody reads. It never reaches its
// deferred ReleaseStreamingResponse, so its upstream connection is never
// returned and one slot of MaxConnsPerHost is burned permanently.
func TestDrainAbandonedStream_UnblocksProducer(t *testing.T) {
	t.Parallel()

	// Buffer smaller than the number of chunks, so the producer blocks unless
	// something consumes. This is the provider goroutine's shape.
	stream := make(chan *schemas.BifrostStreamChunk, 2)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer close(stream)
		for range 10 {
			stream <- &schemas.BifrostStreamChunk{}
		}
	}()

	drainAbandonedStream(stream)

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("provider goroutine still blocked on an abandoned stream; " +
			"its connection would never be released")
	}
}

// TestDrainAbandonedStream_NilIsSafe guards the delivery path, which can reach
// the abandonment branches with no stream to drain.
func TestDrainAbandonedStream_NilIsSafe(t *testing.T) {
	t.Parallel()
	drainAbandonedStream(nil)
}

// abandonedUpstreamProvider is a schemas.Provider whose ChatCompletion ignores the
// request context: it parks until the test releases it, then returns a result (or an
// error when fail is set). It models an upstream that completes after the caller has
// gone (#6972). Every other method comes from the embedded real OpenAI provider.
type abandonedUpstreamProvider struct {
	schemas.Provider
	started chan struct{} // one send per ChatCompletion entry
	release chan struct{} // ChatCompletion returns after one receive
	fail    bool
}

func (p *abandonedUpstreamProvider) ChatCompletion(_ *schemas.BifrostContext, _ schemas.Key, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	p.started <- struct{}{}
	<-p.release
	if p.fail {
		return nil, &schemas.BifrostError{
			StatusCode: new(500),
			Error:      &schemas.ErrorField{Message: "upstream failed after the caller left"},
		}
	}
	return &schemas.BifrostChatResponse{
		ID:     "chatcmpl-abandoned",
		Object: "chat.completion",
		Model:  "gpt-4o-mini",
		Choices: []schemas.BifrostResponseChoice{{
			FinishReason: new("stop"),
			ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
				Message: &schemas.ChatMessage{
					Role:    schemas.ChatMessageRoleAssistant,
					Content: &schemas.ChatMessageContent{ContentStr: new("ok")},
				},
			},
		}},
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7},
	}, nil
}

// terminalHookCounter counts PostLLMHook invocations, split by result vs error, so a
// test can assert that every abandoned request was billed exactly once. Error
// messages are kept so a test can assert WHICH error was billed, not just that one
// was: the worker's shutdown drain also bills abandoned messages, and a test that
// only counts could be satisfied by that path instead of the one under test.
type terminalHookCounter struct {
	results atomic.Int64
	errors  atomic.Int64

	mu       sync.Mutex
	errorMsg []string
}

// errorMessages returns a copy of every billed error message, in hook order.
func (c *terminalHookCounter) errorMessages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.errorMsg...)
}

func (c *terminalHookCounter) GetName() string { return "terminal-hook-counter" }
func (c *terminalHookCounter) Cleanup() error  { return nil }
func (c *terminalHookCounter) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}
func (c *terminalHookCounter) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}
func (c *terminalHookCounter) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if bifrostErr != nil {
		if bifrostErr.Error != nil {
			c.mu.Lock()
			c.errorMsg = append(c.errorMsg, bifrostErr.Error.Message)
			c.mu.Unlock()
		}
		c.errors.Add(1)
	} else {
		c.results.Add(1)
	}
	return resp, bifrostErr, nil
}

// runAbandonedRequests drives the real requestWorker with an upstream that finishes
// after the caller has gone. Each iteration: enqueue, wait for the upstream call to
// start, cancel and abandon the handoff exactly as tryRequest does on ctx.Done, then
// release the upstream. Without the handoff claim the worker's delivery select has
// both a ready send (cap-1 channel, drained on acquire) and a ready ctx.Done(); Go
// picks uniformly among ready cases, so n iterations expose that with probability
// 1 - 2^-n.
func runAbandonedRequests(t *testing.T, n int, fail bool) *terminalHookCounter {
	t.Helper()

	counter := &terminalHookCounter{}
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, n)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{{
		ID:     "abandoned-key",
		Value:  *schemas.NewSecretVar("sk-test"),
		Models: schemas.WhiteList{"*"},
		Weight: 100,
	}})
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:    account,
		Logger:     NewNoOpLogger(),
		LLMPlugins: []schemas.LLMPlugin{counter},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(client.Shutdown)

	cfg, err := account.GetConfigForProvider(schemas.OpenAI)
	if err != nil {
		t.Fatalf("GetConfigForProvider: %v", err)
	}
	cfg.NetworkConfig.MaxRetries = 0

	upstream := &abandonedUpstreamProvider{
		Provider: openai.NewOpenAIProvider(cfg, NewNoOpLogger()),
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		fail:     fail,
	}
	pq := &ProviderQueue{queue: make(chan *ChannelMessage, n), done: make(chan struct{})}
	var wg sync.WaitGroup
	wg.Add(1)
	go client.requestWorker(upstream, cfg, pq, &wg)

	for i := 0; i < n; i++ {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		// tryRequest stamps the tracer before enqueue; the retry loop refuses a context without one.
		ctx.SetValue(schemas.BifrostContextKeyTracer, client.getTracer())
		msg := client.getChannelMessage(schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: []schemas.ChatMessage{{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: new("hi")},
				}},
			},
		})
		msg.Context = ctx
		pq.queue <- msg

		select {
		case <-upstream.started: // the upstream call is in flight
		case e := <-msg.Err:
			t.Fatalf("iteration %d: worker replied with error before the provider: %+v / %+v", i, e, e.Error)
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: worker never reached the provider", i)
		}
		cancel() // the caller disconnects...
		// ...and tryRequest's ctx.Done branch abandons the handoff. The upstream is
		// still parked, so the worker cannot have claimed yet.
		if !msg.abandonDelivery() {
			t.Fatalf("iteration %d: worker claimed delivery before the caller left", i)
		}
		upstream.release <- struct{}{} // the upstream completes anyway
	}
	pq.signalClosing()
	wg.Wait() // every delivery is synchronous inside the worker loop
	return counter
}

// TestRequestWorkerBillsEveryAbandonedResult locks #6972 on the success-delivery path:
// a caller that disconnected must still get its completed upstream result billed and
// logged exactly once. Without a ctx check before the delivery select, roughly half of
// these sends win the race into a buffer nobody reads and the terminal hooks never run.
func TestRequestWorkerBillsEveryAbandonedResult(t *testing.T) {
	const n = 64
	c := runAbandonedRequests(t, n, false)
	if got := c.results.Load(); got != n {
		t.Fatalf("PostLLMHook ran for %d of %d abandoned results; every completed upstream call must be billed once", got, n)
	}
	if got := c.errors.Load(); got != 0 {
		t.Fatalf("PostLLMHook saw %d errors for successful upstream calls, want 0", got)
	}
}

// TestRequestWorkerBillsEveryAbandonedError is the same contract on the error-delivery
// path, which is the common production path once the transport cancels the context on
// a client socket close: the upstream call is cut with a 499 and that error must still
// reach the terminal hooks exactly once.
func TestRequestWorkerBillsEveryAbandonedError(t *testing.T) {
	const n = 64
	c := runAbandonedRequests(t, n, true)
	if got := c.errors.Load(); got != n {
		t.Fatalf("PostLLMHook ran for %d of %d abandoned errors; every failed upstream call must be logged once", got, n)
	}
	if got := c.results.Load(); got != 0 {
		t.Fatalf("PostLLMHook saw %d results for failed upstream calls, want 0", got)
	}
}

// TestRequestWorkerDeliversWhenCallerClaimsLate pins the other order of the handoff:
// the worker claims delivery first, so a caller whose context ends afterwards must
// receive the value itself (tryRequest does exactly that) and the worker must not
// bill it. Otherwise a disconnect landing between the worker's claim and its send
// would be billed twice or not at all.
func TestRequestWorkerDeliversWhenCallerClaimsLate(t *testing.T) {
	counter := &terminalHookCounter{}
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, 1)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{{
		ID: "k", Value: *schemas.NewSecretVar("sk-test"), Models: schemas.WhiteList{"*"}, Weight: 100,
	}})
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account: account, Logger: NewNoOpLogger(), LLMPlugins: []schemas.LLMPlugin{counter},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(client.Shutdown)
	cfg, _ := account.GetConfigForProvider(schemas.OpenAI)
	cfg.NetworkConfig.MaxRetries = 0
	upstream := &abandonedUpstreamProvider{
		Provider: openai.NewOpenAIProvider(cfg, NewNoOpLogger()),
		started:  make(chan struct{}), release: make(chan struct{}),
	}
	pq := &ProviderQueue{queue: make(chan *ChannelMessage, 1), done: make(chan struct{})}
	var wg sync.WaitGroup
	wg.Add(1)
	go client.requestWorker(upstream, cfg, pq, &wg)

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyTracer, client.getTracer())
	msg := client.getChannelMessage(schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o-mini",
			Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hi")}}}},
	})
	msg.Context = ctx
	pq.queue <- msg
	<-upstream.started
	upstream.release <- struct{}{}

	// The caller is still listening: the worker claims and delivers.
	select {
	case got := <-msg.Response:
		if got == nil {
			t.Fatal("worker delivered a nil result")
		}
	case e := <-msg.Err:
		t.Fatalf("worker delivered an error: %+v", e.Error)
	case <-time.After(5 * time.Second):
		t.Fatal("worker never delivered the result")
	}
	// A late ctx.Done in tryRequest must lose the handoff and receive instead.
	cancel()
	if msg.abandonDelivery() {
		t.Fatal("caller abandoned a message the worker had already claimed")
	}
	pq.signalClosing()
	wg.Wait()
	if got := counter.results.Load() + counter.errors.Load(); got != 0 {
		t.Fatalf("worker ran terminal hooks %d times for a delivered result; the caller owns that", got)
	}
}

// runClaimedDeliveriesWithDeadCaller drives the real requestWorker into the third
// handoff interleaving (#7308): the worker claims delivery FIRST and the caller's
// context is already done when the delivery select runs. tryRequest models this
// caller exactly: its ctx.Done branch loses the abandon CAS and then commits to an
// inner receive with no ctx escape (core/bifrost.go tryRequest ctx.Done branch), so
// the worker's claimed send must be unconditional. A ctx.Done arm in that select is
// a uniform coin flip against the always-ready cap-1 send: n iterations expose a
// discarded value, and the caller it strands, with probability 1 - 2^-n.
func runClaimedDeliveriesWithDeadCaller(t *testing.T, n int, fail bool) {
	t.Helper()

	counter := &terminalHookCounter{}
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, n)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{{
		ID:     "claimed-key",
		Value:  *schemas.NewSecretVar("sk-test"),
		Models: schemas.WhiteList{"*"},
		Weight: 100,
	}})
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account:    account,
		Logger:     NewNoOpLogger(),
		LLMPlugins: []schemas.LLMPlugin{counter},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(client.Shutdown)

	cfg, err := account.GetConfigForProvider(schemas.OpenAI)
	if err != nil {
		t.Fatalf("GetConfigForProvider: %v", err)
	}
	cfg.NetworkConfig.MaxRetries = 0

	upstream := &abandonedUpstreamProvider{
		Provider: openai.NewOpenAIProvider(cfg, NewNoOpLogger()),
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		fail:     fail,
	}
	pq := &ProviderQueue{queue: make(chan *ChannelMessage, n), done: make(chan struct{})}
	var wg sync.WaitGroup
	wg.Add(1)
	go client.requestWorker(upstream, cfg, pq, &wg)

	for i := 0; i < n; i++ {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		ctx.SetValue(schemas.BifrostContextKeyTracer, client.getTracer())
		msg := client.getChannelMessage(schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{
				Provider: schemas.OpenAI,
				Model:    "gpt-4o-mini",
				Input: []schemas.ChatMessage{{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: new("hi")},
				}},
			},
		})
		msg.Context = ctx
		pq.queue <- msg

		select {
		case <-upstream.started: // the upstream call is in flight; the worker cannot have claimed yet
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: worker never reached the provider", i)
		}
		cancel()                      // the caller's context ends...
		upstream.release <- struct{}{} // ...and the upstream completes at that same instant

		// The caller has NOT abandoned: this models tryRequest losing the CAS race.
		// Wait until the worker's claim lands so every iteration deterministically
		// enters the claimed-send window with a dead context.
		deadline := time.Now().Add(5 * time.Second)
		for msg.handoff.Load() == handoffOpen {
			if time.Now().After(deadline) {
				t.Fatalf("iteration %d: worker never claimed delivery", i)
			}
			time.Sleep(50 * time.Microsecond)
		}
		if msg.abandonDelivery() {
			t.Fatalf("iteration %d: caller abandoned a message the worker had claimed", i)
		}

		// Commit to the receive exactly as tryRequest does after a failed abandon:
		// an inner select over Response/Err with no ctx arm. The timeout stands in
		// for the goroutine that production leaks permanently.
		select {
		case got := <-msg.Response:
			if fail {
				t.Fatalf("iteration %d: got a result from a failing upstream: %+v", i, got)
			}
			client.releaseChannelMessage(msg)
		case e := <-msg.Err:
			if !fail {
				t.Fatalf("iteration %d: got an error from a succeeding upstream: %+v", i, e.Error)
			}
			client.releaseChannelMessage(msg)
		case <-time.After(3 * time.Second):
			t.Fatalf("iteration %d: worker claimed delivery, then discarded the value; "+
				"the committed caller would block forever (#7308)", i)
		}
	}
	pq.signalClosing()
	wg.Wait()
	if got := counter.results.Load() + counter.errors.Load(); got != 0 {
		t.Fatalf("worker ran terminal hooks %d times for claimed deliveries; the caller owns those", got)
	}
}

// TestRequestWorkerDeliversClaimedResultAfterCallerCtxEnds pins #7308 on the result
// path (core/bifrost.go requestWorker claimed result send): once the worker claims,
// the caller is committed and the send must be unconditional even though ctx.Done()
// is also ready.
func TestRequestWorkerDeliversClaimedResultAfterCallerCtxEnds(t *testing.T) {
	runClaimedDeliveriesWithDeadCaller(t, 24, false)
}

// TestRequestWorkerDeliversClaimedErrorAfterCallerCtxEnds is the same contract on
// the error path, the common production shape once the transport cancels the
// context on a client socket close and the provider returns its terminal error in
// that same window.
func TestRequestWorkerDeliversClaimedErrorAfterCallerCtxEnds(t *testing.T) {
	runClaimedDeliveriesWithDeadCaller(t, 24, true)
}

// TestChannelMessageHandoffIsExclusive pins the claim/abandon state machine: exactly
// one side wins, and a fresh message from the pool starts open again.
func TestChannelMessageHandoffIsExclusive(t *testing.T) {
	t.Parallel()
	m := &ChannelMessage{}
	if !m.claimDelivery() || m.abandonDelivery() || m.claimDelivery() {
		t.Fatal("claim must win once and block a later abandon or second claim")
	}
	m = &ChannelMessage{}
	if !m.abandonDelivery() || m.claimDelivery() || m.abandonDelivery() {
		t.Fatal("abandon must win once and block a later claim or second abandon")
	}
	b := &Bifrost{}
	b.responseChannelPool = sync.Pool{New: func() any { return make(chan *schemas.BifrostResponse, 1) }}
	b.errorChannelPool = sync.Pool{New: func() any { return make(chan schemas.BifrostError, 1) }}
	b.responseStreamPool = sync.Pool{New: func() any { return make(chan chan *schemas.BifrostStreamChunk, 1) }}
	b.channelMessagePool = sync.Pool{New: func() any { return &ChannelMessage{} }}
	msg := b.getChannelMessage(schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest})
	msg.abandonDelivery()
	b.releaseChannelMessage(msg)
	reused := b.getChannelMessage(schemas.BifrostRequest{RequestType: schemas.ChatCompletionRequest})
	if !reused.claimDelivery() {
		t.Fatal("a pooled message must come back with an open handoff")
	}
}

// TestRequestWorkerBillsAbandonedKeySelectionError covers the errors the worker
// produces before any provider call (here: no key serves the model). They are
// sent on the same channel as terminal errors and must obey the same ownership
// rule: a caller that already abandoned the handoff gets its terminal post-hooks
// run by the worker instead of a send into a buffer nobody reads.
func TestRequestWorkerBillsAbandonedKeySelectionError(t *testing.T) {
	const n = 8
	counter := &terminalHookCounter{}
	account := NewMockAccount()
	account.AddProvider(schemas.OpenAI, 1, n)
	account.SetKeysForProvider(schemas.OpenAI, []schemas.Key{{
		ID: "other", Value: *schemas.NewSecretVar("sk-test"), Models: schemas.WhiteList{"some-other-model"}, Weight: 100,
	}})
	client, err := Init(context.Background(), schemas.BifrostConfig{
		Account: account, Logger: NewNoOpLogger(), LLMPlugins: []schemas.LLMPlugin{counter},
	})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	t.Cleanup(client.Shutdown)
	cfg, _ := account.GetConfigForProvider(schemas.OpenAI)
	cfg.NetworkConfig.MaxRetries = 0
	upstream := &abandonedUpstreamProvider{
		Provider: openai.NewOpenAIProvider(cfg, NewNoOpLogger()),
		started:  make(chan struct{}, n), release: make(chan struct{}, n),
	}
	pq := &ProviderQueue{queue: make(chan *ChannelMessage, n), done: make(chan struct{})}
	var wg sync.WaitGroup
	wg.Add(1)
	go client.requestWorker(upstream, cfg, pq, &wg)

	for i := 0; i < n; i++ {
		ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
		ctx.SetValue(schemas.BifrostContextKeyTracer, client.getTracer())
		msg := client.getChannelMessage(schemas.BifrostRequest{
			RequestType: schemas.ChatCompletionRequest,
			ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o-mini",
				Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hi")}}}},
		})
		msg.Context = ctx
		// The caller has already left by the time the worker dequeues.
		cancel()
		if !msg.abandonDelivery() {
			t.Fatalf("iteration %d: message claimed before it was even enqueued", i)
		}
		pq.queue <- msg
	}
	// Wait for every message to be billed BEFORE signalling shutdown. The worker's
	// dequeue select picks uniformly between a ready queue and a closed done channel,
	// so closing early would let the shutdown drain bill some messages with
	// "provider is shutting down" instead of the key-selection error under test.
	deadline := time.Now().Add(5 * time.Second)
	for counter.errors.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d abandoned key-selection errors were billed within 5s", counter.errors.Load(), n)
		}
		time.Sleep(time.Millisecond)
	}
	pq.signalClosing()
	wg.Wait()
	if len(upstream.started) != 0 {
		t.Fatalf("provider was called %d times although no key serves the model", len(upstream.started))
	}
	if got := counter.errors.Load(); got != n {
		t.Fatalf("PostLLMHook ran for %d of %d abandoned key-selection errors; each must be logged once", got, n)
	}
	for i, msg := range counter.errorMessages() {
		if !strings.Contains(msg, "no keys found that support model: gpt-4o-mini") {
			t.Fatalf("billed error %d is %q, want the key-selection error", i, msg)
		}
	}
}
