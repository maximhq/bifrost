package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

type pushTimingTracer struct {
	schemas.Tracer
	traces []*schemas.Trace
	active map[string]*schemas.Trace
}

func (t *pushTimingTracer) CreateTrace(_ string, requestID ...string) string {
	id := requestID[0]
	t.active[id] = &schemas.Trace{RequestID: id, InternalID: id}
	return id
}

func (t *pushTimingTracer) StartSpanID(ctx context.Context, name string, kind schemas.SpanKind) (string, schemas.SpanHandle) {
	traceID, _ := ctx.Value(schemas.BifrostContextKeyTraceID).(string)
	trace := t.active[traceID]
	if trace == nil {
		return "", nil
	}
	parent, _ := ctx.Value(schemas.BifrostContextKeySpanID).(string)
	span := &schemas.Span{SpanID: name, ParentID: parent, Name: name, Kind: kind, StartTime: time.Now(), Attributes: map[string]any{}}
	trace.Spans = append(trace.Spans, span)
	if trace.RootSpan == nil {
		trace.RootSpan = span
	}
	return span.SpanID, span
}

func (t *pushTimingTracer) StartSpan(ctx context.Context, name string, kind schemas.SpanKind) (context.Context, schemas.SpanHandle) {
	id, handle := t.StartSpanID(ctx, name, kind)
	return context.WithValue(ctx, schemas.BifrostContextKeySpanID, id), handle
}

func (t *pushTimingTracer) EndSpan(handle schemas.SpanHandle, status schemas.SpanStatus, _ string) {
	if span, ok := handle.(*schemas.Span); ok {
		span.EndTime = time.Now()
		span.Status = status
	}
}

func (t *pushTimingTracer) SetAttribute(handle schemas.SpanHandle, key string, value any) {
	if span, ok := handle.(*schemas.Span); ok {
		span.Attributes[key] = value
	}
}

func (t *pushTimingTracer) GetSpanHandleByID(id string, _ *string) schemas.SpanHandle {
	return t.active[id].RootSpan
}

func (t *pushTimingTracer) CompleteAndFlushTrace(id string) {
	t.traces = append(t.traces, t.active[id])
	delete(t.active, id)
}

type pushTimingTransport struct{}

func (pushTimingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	time.Sleep(15 * time.Millisecond)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: req}, nil
}

func TestPushTimingLateTracerProvider(t *testing.T) {
	tracer := &pushTimingTracer{active: map[string]*schemas.Trace{}}
	var current schemas.Tracer
	m := &Manager{tracerProvider: func() schemas.Tracer { return current }}

	ctx := gateContext(context.Background())
	m.startPushTrace(ctx, "before-tracer")(nil)
	require.Empty(t, tracer.traces)

	current = tracer
	ctx = gateContext(context.Background())
	m.startPushTrace(ctx, "after-tracer")(nil)
	require.Len(t, tracer.traces, 1)
	require.Equal(t, "after-tracer", tracer.traces[0].RootSpan.Name)
}

func TestPushTimingIngressAndIndependentAttempts(t *testing.T) {
	store := newMemoryPushStore()
	tracer := &pushTimingTracer{active: map[string]*schemas.Trace{}}
	m := &Manager{ctx: context.Background(), pushStore: store, pushRelayID: "runner", externalURL: "https://gateway.example", tracer: tracer, pushDeliveryClient: &http.Client{Transport: pushTimingTransport{}}}
	m.runtimes.store("agent", &runtimeAgent{})
	require.NoError(t, store.SaveAgentPushConfig(context.Background(), &schemas.AgentPushConfig{AgentName: "agent", TaskID: pendingAgentPushConfigTaskIDPrefix + "timing-test", ConfigID: "config", IngressTokenHash: hashPushIngressToken("token"), URL: "https://callback.example"}))
	body, err := json.Marshal(&a2a.StreamResponse{Event: &a2a.Task{ID: "task"}})
	require.NoError(t, err)
	status, err := m.AcceptPushCallback(context.Background(), "agent", "token", body)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	require.Len(t, tracer.traces, 1)
	assertPhases := func(trace *schemas.Trace, names ...string) {
		t.Helper()
		for _, name := range names {
			found := false
			for _, span := range trace.Spans {
				if span.Name == name {
					found = true
					require.False(t, span.EndTime.IsZero())
					require.True(t, schemas.IsOverheadBreakdownSpan(span))
					require.False(t, trace.RootSpan.EndTime.Before(span.EndTime))
				}
			}
			require.True(t, found, name)
		}
	}
	assertPhases(tracer.traces[0], "a2a.push.db.authenticate", "a2a.push.db.bind", "a2a.push.db.enqueue")
	require.Equal(t, float64(0), tracer.traces[0].RootSpan.Attributes[schemas.AttrBifrostUpstreamDurationMs])
	var delivery schemas.AgentPushDelivery
	for _, row := range store.deliveries {
		delivery = row
	}
	relay := newPushRelay(m)
	for range 2 {
		leaseUntil := time.Now().UTC().Add(pushDeliveryLease)
		claimed, claimErr := store.ClaimAgentPushDelivery(context.Background(), delivery.ID, m.pushRelayID, leaseUntil)
		require.NoError(t, claimErr)
		require.NotNil(t, claimed)
		relay.deliver(claimed, leaseUntil)
		delivery = store.delivery(t, delivery.ID)
		delivery.Status = schemas.AgentPushDeliveryStatusPending
		delivery.NextAttemptAt = time.Now().UTC().Add(-time.Second)
		store.pushMu.Lock()
		store.deliveries[delivery.ID] = delivery
		store.pushMu.Unlock()
	}
	require.Len(t, tracer.traces, 3)
	require.NotEqual(t, tracer.traces[1].InternalID, tracer.traces[2].InternalID)
	for _, trace := range tracer.traces[1:] {
		assertPhases(trace, "a2a.push.db.config", "a2a.push.db.outcome")
		require.GreaterOrEqual(t, trace.RootSpan.Attributes[schemas.AttrBifrostUpstreamDurationMs].(float64), float64(15))
	}
	require.Empty(t, tracer.active)
	require.Equal(t, 2, store.deliveries[delivery.ID].Attempts)
	status, err = m.AcceptPushCallback(context.Background(), "agent", "unknown", body)
	require.Error(t, err)
	require.Equal(t, http.StatusUnauthorized, status)
	require.Len(t, tracer.traces, 4)
	assertPhases(tracer.traces[3], "a2a.push.db.authenticate")
	require.Equal(t, schemas.SpanStatusError, tracer.traces[3].RootSpan.Status)
	require.Empty(t, tracer.active)
}

type pushMaintenanceStore struct {
	*memoryPushStore
	err error
}

func (s *pushMaintenanceStore) ListDueAgentPushDeliveries(ctx context.Context, _ time.Time, _ int) ([]schemas.AgentPushDelivery, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, s.err
}

func (s *pushMaintenanceStore) PruneAgentPushDeliveries(ctx context.Context, _ time.Time) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return s.err
}

func TestPushMaintenanceTraceClosure(t *testing.T) {
	for _, mode := range []string{"success", "error", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store := &pushMaintenanceStore{memoryPushStore: newMemoryPushStore()}
			if mode == "error" {
				store.err = errors.New("database unavailable")
			}
			if mode == "cancelled" {
				cancel()
			}
			tracer := &pushTimingTracer{active: map[string]*schemas.Trace{}}
			relay := newPushRelay(&Manager{ctx: ctx, pushStore: store, tracer: tracer})
			relay.processDue()
			relay.prune()
			require.Len(t, tracer.traces, 2)
			require.Empty(t, tracer.active)
			require.NotEqual(t, tracer.traces[0].InternalID, tracer.traces[1].InternalID)
			for i, trace := range tracer.traces {
				name := []string{"a2a.push.db.list-due", "a2a.push.db.prune"}[i]
				require.Len(t, trace.Spans, 2)
				require.Equal(t, name, trace.Spans[1].Name)
				require.False(t, schemas.IsOverheadBreakdownSpan(trace.Spans[1]))
				require.False(t, trace.Spans[1].EndTime.IsZero())
				require.False(t, trace.RootSpan.EndTime.Before(trace.Spans[1].EndTime))
				want := schemas.SpanStatusOk
				if mode != "success" {
					want = schemas.SpanStatusError
				}
				require.Equal(t, want, trace.RootSpan.Status)
			}
		})
	}
}

type failingClaimStore struct {
	*memoryPushStore
	listCalls  atomic.Int64
	claimCalls atomic.Int64
}

func (s *failingClaimStore) ListDueAgentPushDeliveries(context.Context, time.Time, int) ([]schemas.AgentPushDelivery, error) {
	s.listCalls.Add(1)
	due := make([]schemas.AgentPushDelivery, pushDeliveryBatchSize)
	for i := range due {
		due[i].ID = fmt.Sprintf("delivery-%d", i)
	}
	return due, nil
}

func (s *failingClaimStore) ClaimAgentPushDelivery(context.Context, string, string, time.Time) (*schemas.AgentPushDelivery, error) {
	s.claimCalls.Add(1)
	return nil, errors.New("database unavailable")
}

func TestPushRelayStopsAfterFullBatchMakesNoClaimProgress(t *testing.T) {
	store := &failingClaimStore{memoryPushStore: newMemoryPushStore()}
	relay := newPushRelay(&Manager{ctx: context.Background(), pushStore: store})

	relay.processDue()

	require.Equal(t, int64(1), store.listCalls.Load())
	require.Equal(t, int64(pushDeliveryBatchSize), store.claimCalls.Load())
}

// memoryPushStore adds in-memory push persistence to the registration store so
// push tests exercise the manager's real store contract.
type memoryPushClaim struct {
	runnerID   string
	leaseUntil time.Time
}

type memoryPushStore struct {
	memoryStore
	pushMu     sync.Mutex
	configs    map[string]schemas.AgentPushConfig
	deliveries map[string]schemas.AgentPushDelivery
	claims     map[string]memoryPushClaim
}

func newMemoryPushStore() *memoryPushStore {
	return &memoryPushStore{
		memoryStore: memoryStore{regs: map[string]schemas.AgentRegistration{}},
		configs:     map[string]schemas.AgentPushConfig{},
		deliveries:  map[string]schemas.AgentPushDelivery{},
		claims:      map[string]memoryPushClaim{},
	}
}

func pushConfigKey(agentName, taskID, configID string) string {
	return agentName + "\x00" + taskID + "\x00" + configID
}

func (s *memoryPushStore) SaveAgentPushConfig(_ context.Context, config *schemas.AgentPushConfig) error {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	s.configs[pushConfigKey(config.AgentName, config.TaskID, config.ConfigID)] = *config
	return nil
}

func (s *memoryPushStore) BindAgentPushConfigTask(_ context.Context, agentName, ingressTokenHash, pendingTaskID, taskID string) error {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	for key, config := range s.configs {
		if config.AgentName == agentName && config.IngressTokenHash == ingressTokenHash && config.TaskID == pendingTaskID {
			config.TaskID = taskID
			delete(s.configs, key)
			s.configs[pushConfigKey(config.AgentName, config.TaskID, config.ConfigID)] = config
		}
	}
	return nil
}

func (s *memoryPushStore) GetAgentPushConfig(_ context.Context, agentName, taskID, configID string) (*schemas.AgentPushConfig, error) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	if config, ok := s.configs[pushConfigKey(agentName, taskID, configID)]; ok {
		return &config, nil
	}
	return nil, nil
}

func (s *memoryPushStore) GetAgentPushConfigByIngressTokenHash(_ context.Context, agentName, hash string) (*schemas.AgentPushConfig, error) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	for _, config := range s.configs {
		if config.AgentName == agentName && config.IngressTokenHash == hash {
			result := config
			return &result, nil
		}
	}
	return nil, nil
}

func (s *memoryPushStore) ListAgentPushConfigs(_ context.Context, agentName, taskID string) ([]schemas.AgentPushConfig, error) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	var result []schemas.AgentPushConfig
	for _, config := range s.configs {
		if config.AgentName == agentName && config.TaskID == taskID {
			result = append(result, config)
		}
	}
	return result, nil
}

func (s *memoryPushStore) ListAgentPushConfigsPaginated(_ context.Context, query schemas.AgentPushConfigQuery) ([]schemas.AgentPushConfig, int64, error) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	result := make([]schemas.AgentPushConfig, 0, len(s.configs))
	for _, config := range s.configs {
		result = append(result, config)
	}
	return result, int64(len(result)), nil
}

func (s *memoryPushStore) ListAgentPushConfigAgentNames(_ context.Context) ([]string, error) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	names := make([]string, 0, len(s.configs))
	for _, config := range s.configs {
		names = append(names, config.AgentName)
	}
	return names, nil
}

func (s *memoryPushStore) DeleteAgentPushConfig(_ context.Context, agentName, taskID, configID string) (bool, error) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	key := pushConfigKey(agentName, taskID, configID)
	if _, ok := s.configs[key]; !ok {
		return false, nil
	}
	delete(s.configs, key)
	return true, nil
}

func (s *memoryPushStore) DeletePendingAgentPushConfig(_ context.Context, agentName, ingressTokenHash, pendingTaskID string) (bool, error) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	for key, config := range s.configs {
		if config.AgentName == agentName && config.IngressTokenHash == ingressTokenHash && config.TaskID == pendingTaskID {
			delete(s.configs, key)
			return true, nil
		}
	}
	return false, nil
}

func (s *memoryPushStore) CreateAgentPushDeliveryIfNotExists(_ context.Context, delivery *schemas.AgentPushDelivery) (bool, error) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	if _, ok := s.deliveries[delivery.ID]; ok {
		return false, nil
	}
	s.deliveries[delivery.ID] = *delivery
	return true, nil
}

func (s *memoryPushStore) ListDueAgentPushDeliveries(_ context.Context, now time.Time, limit int) ([]schemas.AgentPushDelivery, error) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	var due []schemas.AgentPushDelivery
	for id, delivery := range s.deliveries {
		claim, claimed := s.claims[id]
		if delivery.Status == schemas.AgentPushDeliveryStatusPending && !delivery.NextAttemptAt.After(now) && (!claimed || claim.leaseUntil.Before(now)) {
			due = append(due, delivery)
			if len(due) == limit {
				break
			}
		}
	}
	return due, nil
}

func (s *memoryPushStore) ClaimAgentPushDelivery(_ context.Context, id, runnerID string, leaseUntil time.Time) (*schemas.AgentPushDelivery, error) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	now := time.Now().UTC()
	delivery, exists := s.deliveries[id]
	if !exists || delivery.Status != schemas.AgentPushDeliveryStatusPending || delivery.NextAttemptAt.After(now) {
		return nil, nil
	}
	if claim, claimed := s.claims[id]; claimed && !claim.leaseUntil.Before(now) {
		return nil, nil
	}
	s.claims[id] = memoryPushClaim{runnerID: runnerID, leaseUntil: leaseUntil}
	return &delivery, nil
}

func (s *memoryPushStore) UpdateAgentPushDeliveryOutcome(_ context.Context, delivery *schemas.AgentPushDelivery, runnerID string, leaseUntil time.Time) error {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	claim, claimed := s.claims[delivery.ID]
	if !claimed || claim.runnerID != runnerID || !claim.leaseUntil.Equal(leaseUntil) {
		return errors.New("agent push delivery not found or no longer owned by caller")
	}
	s.deliveries[delivery.ID] = *delivery
	delete(s.claims, delivery.ID)
	return nil
}

func (s *memoryPushStore) PruneAgentPushDeliveries(_ context.Context, before time.Time) error {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	for id, delivery := range s.deliveries {
		if (delivery.Status == schemas.AgentPushDeliveryStatusDelivered || delivery.Status == schemas.AgentPushDeliveryStatusDead) && delivery.UpdatedAt.Before(before) {
			delete(s.deliveries, id)
		}
	}
	return nil
}

func TestPushRelayPrunesOnStartup(t *testing.T) {
	store := newMemoryPushStore()
	now := time.Now().UTC()
	store.deliveries["old"] = schemas.AgentPushDelivery{ID: "old", Status: schemas.AgentPushDeliveryStatusDelivered, UpdatedAt: now.Add(-pushDeliveryRetention - time.Hour)}
	store.deliveries["recent"] = schemas.AgentPushDelivery{ID: "recent", Status: schemas.AgentPushDeliveryStatusDead, UpdatedAt: now}
	store.deliveries["pending"] = schemas.AgentPushDelivery{ID: "pending", Status: schemas.AgentPushDeliveryStatusPending, UpdatedAt: now.Add(-pushDeliveryRetention - time.Hour)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relay := newPushRelay(&Manager{ctx: ctx, pushStore: store})
	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.run()
	}()
	require.Eventually(t, func() bool {
		store.pushMu.Lock()
		defer store.pushMu.Unlock()
		_, exists := store.deliveries["old"]
		return !exists
	}, time.Second, time.Millisecond)
	cancel()
	<-done
	store.pushMu.Lock()
	defer store.pushMu.Unlock()
	require.Contains(t, store.deliveries, "recent")
	require.Contains(t, store.deliveries, "pending")
}

type pruneFailingPushStore struct {
	*memoryPushStore
	pruned chan time.Time
	listed chan struct{}
}

func (s *pruneFailingPushStore) PruneAgentPushDeliveries(_ context.Context, before time.Time) error {
	s.pruned <- before
	return errors.New("cleanup unavailable")
}

func (s *pruneFailingPushStore) ListDueAgentPushDeliveries(context.Context, time.Time, int) ([]schemas.AgentPushDelivery, error) {
	s.listed <- struct{}{}
	return nil, nil
}

func TestPushRelayContinuesAfterPruneError(t *testing.T) {
	store := &pruneFailingPushStore{
		memoryPushStore: newMemoryPushStore(),
		pruned:          make(chan time.Time, 1),
		listed:          make(chan struct{}, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	relay := newPushRelay(&Manager{ctx: ctx, pushStore: store})
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-done
	})
	start := time.Now().UTC()
	go func() {
		defer close(done)
		relay.run()
	}()
	select {
	case cutoff := <-store.pruned:
		require.False(t, cutoff.Before(start.Add(-pushDeliveryRetention)))
		require.False(t, cutoff.After(time.Now().UTC().Add(-pushDeliveryRetention)))
	case <-time.After(time.Second):
		t.Fatal("relay did not prune on startup")
	}
	relay.notify()
	select {
	case <-store.listed:
	case <-time.After(time.Second):
		t.Fatal("cleanup error stopped delivery processing")
	}
}

func (s *memoryPushStore) delivery(t *testing.T, id string) schemas.AgentPushDelivery {
	t.Helper()
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	delivery, ok := s.deliveries[id]
	require.True(t, ok)
	return delivery
}

func (s *memoryPushStore) deliveryStatus(id string) string {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	return s.deliveries[id].Status
}

func (s *memoryPushStore) rescheduleNow(id string) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	delivery := s.deliveries[id]
	delivery.NextAttemptAt = time.Now().UTC().Add(-time.Second)
	s.deliveries[id] = delivery
}

// pushUpstreamClient records the push-config calls the gateway forwards
// upstream.
type pushUpstreamClient struct {
	fakeSDKClient
	mu      sync.Mutex
	created []*a2a.PushConfig
	deleted []*a2a.DeleteTaskPushConfigRequest
}

func (c *pushUpstreamClient) CreateTaskPushConfig(_ context.Context, config *a2a.PushConfig) (*a2a.PushConfig, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	copied := *config
	c.created = append(c.created, &copied)
	return &copied, nil
}

func (c *pushUpstreamClient) DeleteTaskPushConfig(_ context.Context, req *a2a.DeleteTaskPushConfigRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleted = append(c.deleted, req)
	return nil
}

func (c *pushUpstreamClient) lastCreated(t *testing.T) *a2a.PushConfig {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.NotEmpty(t, c.created)
	return c.created[len(c.created)-1]
}

// pushGateway builds a push-enabled manager whose fixture runtime forwards to a
// recording fake upstream, bypassing discovery.
func pushGateway(t *testing.T) (*Manager, *proxyRequestHandler, *memoryPushStore, *pushUpstreamClient) {
	t.Helper()
	store := newMemoryPushStore()
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	// End-to-end relay tests use local callback servers. Individual security
	// tests retain the production client created by NewManager.
	m.pushDeliveryClient = http.DefaultClient
	t.Cleanup(m.Close)
	require.True(t, m.pushEnabled())
	runtime := m.buildRuntime(schemas.AgentRegistration{Name: "fixture", Enabled: true})
	client := &pushUpstreamClient{}
	require.NoError(t, runtime.replaceGeneration(newClientGeneration(&a2a.AgentCard{}, client, string(a2a.TransportProtocolJSONRPC))))
	m.runtimes.store("fixture", runtime)
	return m, &proxyRequestHandler{runtime: runtime, manager: m, config: runtime.config}, store, client
}

func pushEventPayload(t *testing.T, taskID string) []byte {
	t.Helper()
	body, err := json.Marshal(a2a.StreamResponse{Event: &a2a.TaskStatusUpdateEvent{
		TaskID:    a2a.TaskID(taskID),
		ContextID: "ctx-1",
		Status:    a2a.TaskStatus{State: a2a.TaskStateCompleted},
	}})
	require.NoError(t, err)
	return body
}

func TestMarkPushNotificationTransport(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	markPushNotificationTransport(ctx)

	require.Equal(t, pushNotificationTransport, ctx.Value(schemas.BifrostContextKeyA2ADownstreamTransport))
	require.Equal(t, pushNotificationTransport, ctx.Value(schemas.BifrostContextKeyA2AUpstreamTransport))
}

func TestCreateTaskPushConfigRewritesUpstreamAndStoresCallback(t *testing.T) {
	_, handler, store, upstream := pushGateway(t)
	result, err := handler.CreateTaskPushConfig(context.Background(), &a2a.PushConfig{
		TaskID: "task-1",
		ID:     "cfg-1",
		URL:    "http://client.example/callback",
		Token:  "client-token",
		Auth:   &a2a.PushAuthInfo{Scheme: "Bearer", Credentials: "client-credential"},
	})
	require.NoError(t, err)

	// The upstream hop was rewritten to Bifrost's ingress with a minted token,
	// never the client's callback or secrets.
	created := upstream.lastCreated(t)
	require.Equal(t, "http://gateway/agents/a2a/fixture"+GatewayPushCallbackPathSuffix, created.URL)
	require.NotEmpty(t, created.Token)
	require.NotEqual(t, "client-token", created.Token)
	require.Nil(t, created.Auth)

	// The response is the client's own callback, with secrets write-only.
	require.Equal(t, "cfg-1", result.ID)
	require.Equal(t, "http://client.example/callback", result.URL)
	require.Empty(t, result.Token)
	require.Equal(t, "Bearer", result.Auth.Scheme)
	require.Empty(t, result.Auth.Credentials)

	stored, err := store.GetAgentPushConfig(context.Background(), "fixture", "task-1", "cfg-1")
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, hashPushIngressToken(created.Token), stored.IngressTokenHash)
	require.Equal(t, "client-token", stored.Token.GetValue())
	require.Equal(t, "client-credential", stored.AuthCredentials.GetValue())

	// Reads are served from the gateway store and stay redacted.
	got, err := handler.GetTaskPushConfig(context.Background(), &a2a.GetTaskPushConfigRequest{TaskID: "task-1", ID: "cfg-1"})
	require.NoError(t, err)
	require.Empty(t, got.Token)
	require.Equal(t, "http://client.example/callback", got.URL)
	listed, err := handler.ListTaskPushConfigs(context.Background(), &a2a.ListTaskPushConfigRequest{TaskID: "task-1"})
	require.NoError(t, err)
	require.Len(t, listed.Configs, 1)
	require.Empty(t, listed.Configs[0].Token)

	_, err = handler.GetTaskPushConfig(context.Background(), &a2a.GetTaskPushConfigRequest{TaskID: "task-1", ID: "missing"})
	require.ErrorIs(t, err, push.ErrPushConfigNotFound)

	// A callback that is not an absolute HTTP(S) URL is refused before any hop.
	_, err = handler.CreateTaskPushConfig(context.Background(), &a2a.PushConfig{TaskID: "task-1", URL: "ftp://client.example"})
	require.ErrorIs(t, err, a2a.ErrInvalidParams)
}

func TestStreamingEmbeddedPushConfigKeepsInternalIngressOutOfPluginEnvelope(t *testing.T) {
	m, handler, _, upstream := pushGateway(t)
	var order []string
	recorder := &recordingA2APlugin{name: "recorder", order: &order}
	pipeline := &fakePipeline{plugins: []*recordingA2APlugin{recorder}}
	m.SetPluginPipeline(func() PluginPipeline { return pipeline }, func(PluginPipeline) {})

	var upstreamSaw *a2a.PushConfig
	upstream.stream = func(_ context.Context, req *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		copied := *req.Config.PushConfig
		upstreamSaw = &copied
		return func(yield func(a2a.Event, error) bool) {
			yield(&a2a.Task{ID: "task-stream", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}, nil)
		}
	}

	stream := handler.SendStreamingMessage(context.Background(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi")),
		Config: &a2a.SendMessageConfig{PushConfig: &a2a.PushConfig{
			ID:    "stream-config",
			URL:   "https://client.example/callback",
			Token: "client-token",
		}},
	})
	for _, err := range stream {
		require.NoError(t, err)
	}

	require.NotNil(t, recorder.request)
	require.NotNil(t, recorder.request.RequestBody)
	require.Contains(t, *recorder.request.RequestBody, "https://client.example/callback")
	require.NotNil(t, upstreamSaw)
	require.Equal(t, "http://gateway/agents/a2a/fixture"+GatewayPushCallbackPathSuffix, upstreamSaw.URL)
	require.NotEmpty(t, upstreamSaw.Token)
	require.NotContains(t, *recorder.request.RequestBody, upstreamSaw.URL)
	require.NotContains(t, *recorder.request.RequestBody, upstreamSaw.Token)
}

func TestStreamingEmbeddedPushConfigIsLazyAndCleansUpBeforeUpstreamCall(t *testing.T) {
	m, handler, store, _ := pushGateway(t)
	var order []string
	denier := &recordingA2APlugin{
		name:  "denier",
		order: &order,
		shortCircuit: &schemas.A2APluginShortCircuit{
			Error: &schemas.BifrostError{StatusCode: schemas.Ptr(403), Error: &schemas.ErrorField{Message: "denied"}},
		},
	}
	pipeline := &fakePipeline{plugins: []*recordingA2APlugin{denier}}
	m.SetPluginPipeline(func() PluginPipeline { return pipeline }, func(PluginPipeline) {})

	stream := handler.SendStreamingMessage(context.Background(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi")),
		Config: &a2a.SendMessageConfig{PushConfig: &a2a.PushConfig{
			ID:  "stream-config",
			URL: "https://client.example/callback",
		}},
	})
	configs, total, err := store.ListAgentPushConfigsPaginated(context.Background(), schemas.AgentPushConfigQuery{})
	require.NoError(t, err)
	require.Empty(t, configs, "constructing an unconsumed stream must not persist a pending push config")
	require.Zero(t, total)

	var streamErr error
	stream(func(_ a2a.Event, err error) bool {
		streamErr = err
		return true
	})
	require.ErrorIs(t, streamErr, a2a.ErrUnauthorized)

	configs, total, err = store.ListAgentPushConfigsPaginated(context.Background(), schemas.AgentPushConfigQuery{})
	require.NoError(t, err)
	require.Empty(t, configs, "a pre-hook denial must clean up the pending push config")
	require.Zero(t, total)
}

func TestSendMessageRewritesEmbeddedPushConfigAndStoresCallback(t *testing.T) {
	_, handler, store, upstream := pushGateway(t)
	var upstreamSaw *a2a.PushConfig
	upstream.send = func(_ context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		copied := *req.Config.PushConfig
		upstreamSaw = &copied
		return &a2a.Task{ID: "task-emb", ContextID: "ctx-1", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}, nil
	}

	result, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: &a2a.Message{ID: "m1", Role: a2a.MessageRoleUser, Parts: []*a2a.Part{a2a.NewTextPart("hi")}},
		Config: &a2a.SendMessageConfig{PushConfig: &a2a.PushConfig{
			URL:   "http://client.example/embedded",
			Token: "client-token",
			Auth:  &a2a.PushAuthInfo{Scheme: "Bearer", Credentials: "client-credential"},
		}},
	})
	require.NoError(t, err)
	task, ok := result.(*a2a.Task)
	require.True(t, ok)
	require.Equal(t, a2a.TaskID("task-emb"), task.ID)

	// The upstream saw only Bifrost's ingress with a minted token — never the
	// client's callback or secrets.
	require.NotNil(t, upstreamSaw)
	require.NotEmpty(t, upstreamSaw.ID)
	require.Equal(t, "http://gateway/agents/a2a/fixture"+GatewayPushCallbackPathSuffix, upstreamSaw.URL)
	require.NotEmpty(t, upstreamSaw.Token)
	require.NotEqual(t, "client-token", upstreamSaw.Token)
	require.Nil(t, upstreamSaw.Auth)

	// The client's real callback was durably stored and bound to the born
	// task id (the config id is minted, so look it up by task).
	storedList, err := store.ListAgentPushConfigs(context.Background(), "fixture", "task-emb")
	require.NoError(t, err)
	require.Len(t, storedList, 1)
	stored := &storedList[0]
	require.Equal(t, upstreamSaw.ID, stored.ConfigID)
	require.Equal(t, "http://client.example/embedded", stored.URL)
	require.Equal(t, hashPushIngressToken(upstreamSaw.Token), stored.IngressTokenHash)
	require.Equal(t, "client-token", stored.Token.GetValue())
	require.Equal(t, "client-credential", stored.AuthCredentials.GetValue())

	// An invalid embedded callback is refused before any upstream hop.
	_, err = handler.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: &a2a.Message{ID: "m2", Role: a2a.MessageRoleUser, Parts: []*a2a.Part{a2a.NewTextPart("hi")}},
		Config:  &a2a.SendMessageConfig{PushConfig: &a2a.PushConfig{URL: "ftp://client.example"}},
	})
	require.ErrorIs(t, err, a2a.ErrInvalidParams)
}

func TestSendMessageBindsEmbeddedPushConfigFromMessageTaskID(t *testing.T) {
	_, handler, store, upstream := pushGateway(t)
	upstream.send = func(_ context.Context, _ *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		return &a2a.Message{ID: "response", TaskID: "task-existing", Role: a2a.MessageRoleAgent}, nil
	}

	result, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi")),
		Config: &a2a.SendMessageConfig{PushConfig: &a2a.PushConfig{
			ID:  "message-config",
			URL: "http://client.example/embedded",
		}},
	})
	require.NoError(t, err)
	message, ok := result.(*a2a.Message)
	require.True(t, ok)
	require.Equal(t, a2a.TaskID("task-existing"), message.TaskID)

	stored, err := store.GetAgentPushConfig(context.Background(), "fixture", "task-existing", "message-config")
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, "http://client.example/embedded", stored.URL)
}

type settlementFailingPushStore struct {
	*memoryPushStore
}

func (s *settlementFailingPushStore) BindAgentPushConfigTask(context.Context, string, string, string, string) error {
	return errors.New("bind unavailable")
}

func (s *settlementFailingPushStore) DeletePendingAgentPushConfig(context.Context, string, string, string) (bool, error) {
	return false, errors.New("delete unavailable")
}

func TestEmbeddedPushSettlementErrorsDoNotRequireLogger(t *testing.T) {
	store := &settlementFailingPushStore{memoryPushStore: newMemoryPushStore()}
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	t.Cleanup(m.Close)
	handler := &proxyRequestHandler{manager: m, config: runtimeConfig{name: "fixture"}}

	finish, err := handler.rewriteEmbeddedPushConfig(context.Background(), &a2a.SendMessageRequest{
		Config: &a2a.SendMessageConfig{PushConfig: &a2a.PushConfig{URL: "http://client.example/failed-delete"}},
	})
	require.NoError(t, err)
	require.NotPanics(t, func() { finish(context.Background(), "") })

	finish, err = handler.rewriteEmbeddedPushConfig(context.Background(), &a2a.SendMessageRequest{
		Config: &a2a.SendMessageConfig{PushConfig: &a2a.PushConfig{URL: "http://client.example/failed-bind"}},
	})
	require.NoError(t, err)
	require.NotPanics(t, func() { finish(context.Background(), "task") })
}

func TestConcurrentSendMessagesKeepEmbeddedPushConfigsIndependent(t *testing.T) {
	_, handler, store, upstream := pushGateway(t)

	arrived := make(chan *a2a.PushConfig, 2)
	releases := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var calls atomic.Int32
	upstream.send = func(_ context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		call := int(calls.Add(1)) - 1
		copied := *req.Config.PushConfig
		arrived <- &copied
		<-releases[call]
		if call == 0 {
			return nil, errors.New("send failed")
		}
		return &a2a.Task{ID: "task-concurrent", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}, nil
	}

	type sendResult struct {
		result a2a.SendMessageResult
		err    error
	}
	results := make(chan sendResult, 2)
	send := func(messageID string) {
		result, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{
			Message: &a2a.Message{ID: messageID, Role: a2a.MessageRoleUser, Parts: []*a2a.Part{a2a.NewTextPart("hi")}},
			Config: &a2a.SendMessageConfig{PushConfig: &a2a.PushConfig{
				ID:  "shared-config",
				URL: "http://client.example/embedded",
			}},
		})
		results <- sendResult{result: result, err: err}
	}
	go send("message-a")
	first := <-arrived
	go send("message-b")
	second := <-arrived

	require.Equal(t, "shared-config", first.ID)
	require.Equal(t, "shared-config", second.ID)
	require.NotEqual(t, first.Token, second.Token)

	firstHash := hashPushIngressToken(first.Token)
	secondHash := hashPushIngressToken(second.Token)
	firstPending, err := store.GetAgentPushConfigByIngressTokenHash(context.Background(), "fixture", firstHash)
	require.NoError(t, err)
	require.NotNil(t, firstPending)
	secondPending, err := store.GetAgentPushConfigByIngressTokenHash(context.Background(), "fixture", secondHash)
	require.NoError(t, err)
	require.NotNil(t, secondPending)
	require.NotEqual(t, firstPending.TaskID, secondPending.TaskID)
	require.True(t, isPendingAgentPushConfig(firstPending.TaskID))
	require.True(t, isPendingAgentPushConfig(secondPending.TaskID))

	close(releases[0])
	failed := <-results
	require.Error(t, failed.err)
	firstPending, err = store.GetAgentPushConfigByIngressTokenHash(context.Background(), "fixture", firstHash)
	require.NoError(t, err)
	require.Nil(t, firstPending)
	secondPending, err = store.GetAgentPushConfigByIngressTokenHash(context.Background(), "fixture", secondHash)
	require.NoError(t, err)
	require.NotNil(t, secondPending)

	close(releases[1])
	succeeded := <-results
	require.NoError(t, succeeded.err)
	task, ok := succeeded.result.(*a2a.Task)
	require.True(t, ok)
	require.Equal(t, a2a.TaskID("task-concurrent"), task.ID)
	stored, err := store.GetAgentPushConfig(context.Background(), "fixture", "task-concurrent", "shared-config")
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, secondHash, stored.IngressTokenHash)
}

func TestSendMessageRetryKeepsEmbeddedPushConfigUntilFinalOutcome(t *testing.T) {
	store := newMemoryPushStore()
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	t.Cleanup(m.Close)

	var attempts []*a2a.PushConfig
	first := &fakeSDKClient{send: func(_ context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		copied := *req.Config.PushConfig
		attempts = append(attempts, &copied)
		return nil, dialFailure()
	}}
	second := &fakeSDKClient{send: func(_ context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
		copied := *req.Config.PushConfig
		attempts = append(attempts, &copied)
		return &a2a.Task{ID: "task-retry", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}, nil
	}}
	runtime := m.buildRuntime(schemas.AgentRegistration{Name: "fixture", Enabled: true})
	require.NoError(t, runtime.replaceGeneration(newCandidateGeneration(&a2a.AgentCard{}, []upstreamCandidate{
		{client: first, transport: string(a2a.TransportProtocolJSONRPC)},
		{client: second, transport: string(a2a.TransportProtocolJSONRPC)},
	})))
	m.runtimes.store("fixture", runtime)
	handler := &proxyRequestHandler{runtime: runtime, manager: m, config: runtime.config}

	result, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi")),
		Config:  &a2a.SendMessageConfig{PushConfig: &a2a.PushConfig{URL: "http://client.example/embedded"}},
	})
	require.NoError(t, err)
	task, ok := result.(*a2a.Task)
	require.True(t, ok)
	require.Equal(t, a2a.TaskID("task-retry"), task.ID)
	require.Len(t, attempts, 2)
	require.Equal(t, attempts[0], attempts[1], "every candidate must receive the same rewritten push configuration")

	stored, err := store.ListAgentPushConfigs(context.Background(), "fixture", "task-retry")
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Equal(t, attempts[0].ID, stored[0].ConfigID)
	require.Equal(t, hashPushIngressToken(attempts[0].Token), stored[0].IngressTokenHash)
}

func TestDeleteTaskPushConfigRemovesBothHops(t *testing.T) {
	_, handler, store, upstream := pushGateway(t)
	_, err := handler.CreateTaskPushConfig(context.Background(), &a2a.PushConfig{TaskID: "task-1", ID: "cfg-1", URL: "http://client.example/callback"})
	require.NoError(t, err)

	require.NoError(t, handler.DeleteTaskPushConfig(context.Background(), &a2a.DeleteTaskPushConfigRequest{TaskID: "task-1", ID: "cfg-1"}))
	upstream.mu.Lock()
	require.Len(t, upstream.deleted, 1)
	require.Equal(t, "cfg-1", upstream.deleted[0].ID)
	upstream.mu.Unlock()
	stored, err := store.GetAgentPushConfig(context.Background(), "fixture", "task-1", "cfg-1")
	require.NoError(t, err)
	require.Nil(t, stored)

	// Deleting again is idempotent locally; the upstream remains the authority.
	require.NoError(t, handler.DeleteTaskPushConfig(context.Background(), &a2a.DeleteTaskPushConfigRequest{TaskID: "task-1", ID: "cfg-1"}))
}

func TestPushDeliveryIDsAreScopedToAgent(t *testing.T) {
	store := newMemoryPushStore()
	m, err := NewManager(context.Background(), store, nil, "http://gateway", nil)
	require.NoError(t, err)
	t.Cleanup(m.Close)

	payload := pushEventPayload(t, "task-1")
	for _, agentName := range []string{"agent-a", "agent-b"} {
		token := "token-" + agentName
		m.runtimes.store(agentName, m.buildRuntime(schemas.AgentRegistration{Name: agentName, Enabled: true}))
		require.NoError(t, store.SaveAgentPushConfig(context.Background(), &schemas.AgentPushConfig{
			AgentName:        agentName,
			TaskID:           "task-1",
			ConfigID:         "cfg-1",
			URL:              "http://client.example/callback",
			IngressTokenHash: hashPushIngressToken(token),
		}))

		status, err := m.AcceptPushCallback(context.Background(), agentName, token, payload)
		require.NoError(t, err)
		require.Equal(t, http.StatusAccepted, status)
	}

	store.pushMu.Lock()
	defer store.pushMu.Unlock()
	require.Len(t, store.deliveries, 2)
	ids := make(map[string]struct{}, len(store.deliveries))
	for id, delivery := range store.deliveries {
		ids[id] = struct{}{}
		require.Equal(t, "cfg-1", delivery.ConfigID)
		require.Equal(t, string(payload), delivery.Payload)
	}
	require.Len(t, ids, 2)
}

func TestAcceptPushCallbackAuthenticatesAndDeduplicates(t *testing.T) {
	m, handler, store, upstream := pushGateway(t)
	_, err := handler.CreateTaskPushConfig(context.Background(), &a2a.PushConfig{TaskID: "task-1", ID: "cfg-1", URL: "http://client.example/callback"})
	require.NoError(t, err)
	token := upstream.lastCreated(t).Token
	payload := pushEventPayload(t, "task-1")

	status, err := m.AcceptPushCallback(context.Background(), "fixture", token, payload)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	// A retransmission of the same event is accepted but deduplicated.
	status, err = m.AcceptPushCallback(context.Background(), "fixture", token, payload)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	store.pushMu.Lock()
	require.Len(t, store.deliveries, 1)
	store.pushMu.Unlock()

	status, _ = m.AcceptPushCallback(context.Background(), "fixture", "", payload)
	require.Equal(t, http.StatusUnauthorized, status)
	status, _ = m.AcceptPushCallback(context.Background(), "fixture", "wrong-token", payload)
	require.Equal(t, http.StatusUnauthorized, status)
	status, _ = m.AcceptPushCallback(context.Background(), "unknown-agent", token, payload)
	require.Equal(t, http.StatusNotFound, status)
	status, _ = m.AcceptPushCallback(context.Background(), "fixture", token, pushEventPayload(t, "other-task"))
	require.Equal(t, http.StatusBadRequest, status)
	status, _ = m.AcceptPushCallback(context.Background(), "fixture", token, []byte("not-json"))
	require.Equal(t, http.StatusBadRequest, status)
}

func TestPushDeliveryBlocksPrivateDestination(t *testing.T) {
	called := atomic.Bool{}
	downstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called.Store(true)
	}))
	defer downstream.Close()

	m, err := NewManager(context.Background(), newMemoryPushStore(), nil, "http://gateway", nil)
	require.NoError(t, err)
	t.Cleanup(m.Close)

	err = m.sendPushDownstream(context.Background(), &schemas.AgentPushConfig{URL: downstream.URL}, `{}`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "blocked connection to non-public address")
	require.False(t, called.Load())
}

type pushRoundTripFunc func(*http.Request) (*http.Response, error)

func (f pushRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestConcurrentPushRelaysDeliverOnce(t *testing.T) {
	store := newMemoryPushStore()
	now := time.Now().UTC()
	require.NoError(t, store.SaveAgentPushConfig(context.Background(), &schemas.AgentPushConfig{
		AgentName: "agent", TaskID: "task", ConfigID: "config", URL: "https://callback.example",
	}))
	created, err := store.CreateAgentPushDeliveryIfNotExists(context.Background(), &schemas.AgentPushDelivery{
		ID: "delivery", AgentName: "agent", TaskID: "task", ConfigID: "config",
		Payload: `{}`, Status: schemas.AgentPushDeliveryStatusPending, NextAttemptAt: now,
		CreatedAt: now, UpdatedAt: now,
	})
	require.NoError(t, err)
	require.True(t, created)

	var requests atomic.Int32
	client := &http.Client{Transport: pushRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		time.Sleep(20 * time.Millisecond)
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: req}, nil
	})}
	newRelay := func(id string) *pushRelay {
		return newPushRelay(&Manager{
			ctx: context.Background(), pushStore: store, pushRelayID: id,
			pushDeliveryClient: client,
		})
	}
	start := make(chan struct{})
	done := make(chan struct{}, 2)
	for _, relay := range []*pushRelay{newRelay("node-1"), newRelay("node-2")} {
		go func() {
			<-start
			relay.processDue()
			done <- struct{}{}
		}()
	}
	close(start)
	<-done
	<-done

	require.EqualValues(t, 1, requests.Load())
	delivery := store.delivery(t, "delivery")
	require.Equal(t, schemas.AgentPushDeliveryStatusDelivered, delivery.Status)
	require.Equal(t, 1, delivery.Attempts)
}

func TestPushRelayDeliversWithClientCredentials(t *testing.T) {
	var gotHeaders atomic.Value
	var gotBody atomic.Value
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotHeaders.Store(r.Header.Clone())
		gotBody.Store(string(body))
		w.WriteHeader(http.StatusOK)
	}))
	defer downstream.Close()

	m, handler, store, upstream := pushGateway(t)
	_, err := handler.CreateTaskPushConfig(context.Background(), &a2a.PushConfig{
		TaskID: "task-1",
		ID:     "cfg-1",
		URL:    downstream.URL,
		Token:  "client-token",
		Auth:   &a2a.PushAuthInfo{Scheme: "Bearer", Credentials: "client-credential"},
	})
	require.NoError(t, err)
	payload := pushEventPayload(t, "task-1")
	status, err := m.AcceptPushCallback(context.Background(), "fixture", upstream.lastCreated(t).Token, payload)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)

	var deliveryID string
	store.pushMu.Lock()
	for id := range store.deliveries {
		deliveryID = id
	}
	store.pushMu.Unlock()
	require.Eventually(t, func() bool {
		return store.deliveryStatus(deliveryID) == schemas.AgentPushDeliveryStatusDelivered
	}, 5*time.Second, 10*time.Millisecond)

	require.Equal(t, string(payload), gotBody.Load())
	headers := gotHeaders.Load().(http.Header)
	require.Equal(t, "client-token", headers.Get(PushNotificationTokenHeader))
	require.Equal(t, "Bearer client-credential", headers.Get("Authorization"))
	delivered := store.delivery(t, deliveryID)
	require.Equal(t, 1, delivered.Attempts)
	require.Empty(t, delivered.LastError)
}

func TestPushRelayRetriesThenDeadLetters(t *testing.T) {
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer downstream.Close()

	m, handler, store, upstream := pushGateway(t)
	_, err := handler.CreateTaskPushConfig(context.Background(), &a2a.PushConfig{TaskID: "task-1", ID: "cfg-1", URL: downstream.URL})
	require.NoError(t, err)
	payload := pushEventPayload(t, "task-1")
	status, err := m.AcceptPushCallback(context.Background(), "fixture", upstream.lastCreated(t).Token, payload)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)

	var deliveryID string
	store.pushMu.Lock()
	for id := range store.deliveries {
		deliveryID = id
	}
	store.pushMu.Unlock()

	// The first attempt fails and is rescheduled with attempt accounting.
	require.Eventually(t, func() bool {
		return store.delivery(t, deliveryID).Attempts >= 1
	}, 5*time.Second, 10*time.Millisecond)
	failed := store.delivery(t, deliveryID)
	require.Equal(t, schemas.AgentPushDeliveryStatusPending, failed.Status)
	require.Contains(t, failed.LastError, "500")
	require.True(t, failed.NextAttemptAt.After(time.Now().UTC().Add(-time.Second)))

	// Once the push configuration disappears the delivery dead-letters
	// immediately instead of retrying forever.
	_, err = store.DeleteAgentPushConfig(context.Background(), "fixture", "task-1", "cfg-1")
	require.NoError(t, err)
	store.rescheduleNow(deliveryID)
	m.pushRelay.notify()
	require.Eventually(t, func() bool {
		return store.deliveryStatus(deliveryID) == schemas.AgentPushDeliveryStatusDead
	}, 5*time.Second, 10*time.Millisecond)
	dead := store.delivery(t, deliveryID)
	require.Contains(t, dead.LastError, "push configuration no longer exists")
}
