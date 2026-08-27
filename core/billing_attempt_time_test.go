package bifrost

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestClearBillingAttemptStartTimeMasksInheritedValue ensures a child context
// cannot expose a stale attempt timestamp inherited from its parent.
func TestClearBillingAttemptStartTimeMasksInheritedValue(t *testing.T) {
	parent := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stamp := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	parent.SetBillingAttemptStartTime(stamp)

	child := schemas.NewBifrostContext(parent, schemas.NoDeadline)
	if got, ok := child.Value(schemas.BifrostContextKeyBillingAttemptStartTime).(time.Time); !ok || !got.Equal(stamp) {
		t.Fatalf("child should inherit the parent timestamp, got %#v", got)
	}

	child.ClearBillingAttemptStartTime()
	if _, ok := child.Value(schemas.BifrostContextKeyBillingAttemptStartTime).(time.Time); ok {
		t.Fatal("cleared child context exposed an inherited billing timestamp")
	}
}

// TestNilUnaryRequestsUseIsolatedContexts pins that nil-context callers no longer
// share the mutable Bifrost-wide context used for request-scoped pricing state.
func TestNilUnaryRequestsUseIsolatedContexts(t *testing.T) {
	newBifrost := func() *Bifrost {
		return &Bifrost{
			ctx:             schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
			logger:          NewNoOpLogger(),
			account:         NewMockAccount(),
			providerMutexes: sync.Map{},
		}
	}
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "test-model",
		Input:    []schemas.ChatMessage{{}},
	}
	bifrost := newBifrost()
	bifrost.bifrostRequestPool = sync.Pool{New: func() interface{} { return &schemas.BifrostRequest{} }}
	bifrost.llmPlugins.Store(&[]schemas.LLMPlugin{})
	bifrost.mcpPlugins.Store(&[]schemas.MCPPlugin{})
	bifrost.pluginPipelinePool = sync.Pool{New: func() interface{} {
		return &PluginPipeline{
			preHookErrors:  make([]error, 0),
			postHookErrors: make([]error, 0),
		}
	}}
	bifrost.tracer.Store(&tracerWrapper{tracer: &schemas.NoOpTracer{}})
	_, err := bifrost.ChatCompletionRequest(nil, req)
	if err == nil || err.Error == nil {
		t.Fatalf("expected request to fail without providers, got %#v", err)
	}
	if bifrost.ctx.Value(schemas.BifrostContextKeyRequestStartTime) != nil {
		t.Fatal("nil unary caller wrote a request start time to the shared Bifrost context")
	}
	if _, ok := bifrost.ctx.Value(schemas.BifrostContextKeyBillingAttemptStartTime).(time.Time); ok {
		t.Fatal("nil unary caller left a billing timestamp on the shared Bifrost context")
	}
	if v, ok := bifrost.ctx.Value(schemas.BifrostContextKeyRequestID).(string); ok && v != "" {
		t.Fatalf("nil unary caller changed the shared Bifrost request ID: %q", v)
	}
}
