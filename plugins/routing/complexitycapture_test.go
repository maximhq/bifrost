package routing

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func captureTestEmbedding(_ context.Context, _ *complexity.SemanticConfig, text string) ([]float32, error) {
	switch text {
	case "simple exemplar", "simple request":
		return []float32{1, 0}, nil
	case "medium exemplar":
		return []float32{0, 1}, nil
	default:
		return []float32{-1, 0}, nil
	}
}

func captureTestConfig(suggestions *configstore.ComplexitySuggestionsConfig) *complexity.AnalyzerConfig {
	return &complexity.AnalyzerConfig{
		TierBoundaries: complexity.DefaultTierBoundaries(),
		Keywords: complexity.EditableKeywordConfig{
			SimpleKeywords:  []string{"simple exemplar"},
			MediumKeywords:  []string{"medium exemplar"},
			ComplexKeywords: []string{"complex exemplar"},
		},
		Semantic: &complexity.SemanticConfig{
			Provider: "openai", EmbeddingModel: "test-embedding-model", Timeout: time.Second,
			VectorStore: configstore.ComplexitySemanticVectorStoreEmbedded,
		},
		// Enabled suggestions need a model; with none of their own they use this.
		LLM:         &complexity.LLMConfig{Provider: "openai", Model: "gpt-4.1-mini", Timeout: time.Second},
		Suggestions: suggestions,
	}
}

// captureRouterID is the model router the capture tests serve.
const captureRouterID = "router-under-test"

// newCapturePlugin builds a plugin serving one model router, captureRouterID,
// with a ready semantic classifier and capture wired, without the upkeep timer
// (tests drive it). Capture only writes to Bifrost's configured vector store,
// never the classifier's embedded one, and never to Chromem, so the plugin
// gets an in-memory store standing in for a shared one.
func newCapturePlugin(t *testing.T, suggestions *configstore.ComplexitySuggestionsConfig) *RoutingPlugin {
	t.Helper()
	return newCapturePluginWithStore(t, suggestions, true)
}

// sharedTestStore stands in for a shared vector store in tests: Chromem
// underneath, but not of Chromem's type, which capture refuses.
type sharedTestStore struct{ vectorstore.VectorStore }

// newCapturePluginWithStore builds the capture plugin over an in-memory
// Chromem store, presented as a shared store when shared is set and as
// Chromem itself otherwise.
func newCapturePluginWithStore(t *testing.T, suggestions *configstore.ComplexitySuggestionsConfig, shared bool) *RoutingPlugin {
	t.Helper()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	p := &RoutingPlugin{logger: logger, semanticClassifier: complexity.NewSemanticClassifier(context.Background(), logger)}
	matcher, err := rules.NewModelRouterMatcher()
	require.NoError(t, err)
	p.routerMatcher = matcher
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Enabled: true, Type: vectorstore.VectorStoreTypeChromem, Config: vectorstore.ChromemConfig{},
	}, logger)
	require.NoError(t, err)
	if shared {
		store = &sharedTestStore{store}
	}
	p.SetComplexityVectorStore(store)
	p.startComplexityCapture(context.Background(), time.Hour, time.Hour)
	t.Cleanup(func() { _ = p.Cleanup() })
	serveCaptureRouter(t, p, captureRouterID, suggestions)
	return p
}

// serveCaptureRouter serves (or reconfigures) a semantic model router with
// the capture tests' phrases and the given suggestions settings, and waits
// for it to be ready.
func serveCaptureRouter(t *testing.T, p *RoutingPlugin, id string, suggestions *configstore.ComplexitySuggestionsConfig) {
	t.Helper()
	_, existed := p.modelRouter(id)
	require.NoError(t, p.ReloadModelRouter(&configstore.ModelRouter{ID: id, Name: id, ComplexityAnalyzerConfig: *captureTestConfig(suggestions)}))
	rt, ok := p.modelRouter(id)
	require.True(t, ok)
	if !existed {
		rt.semantic.SetEmbeddingFunc(captureTestEmbedding)
	}
	require.Eventually(t, func() bool { return rt.semantic.Status().State == complexity.SemanticStatusReady },
		2*time.Second, 5*time.Millisecond)
}

// captureRouter is the router the capture tests serve.
func captureRouter(t *testing.T, p *RoutingPlugin) *modelRouterRuntime {
	t.Helper()
	rt, ok := p.modelRouter(captureRouterID)
	require.True(t, ok)
	return rt
}

func classifyAs(t *testing.T, p *RoutingPlugin, id string) {
	t.Helper()
	classifyWith(t, captureRouter(t, p), id)
}

func classifyWith(t *testing.T, rt *modelRouterRuntime, id string) {
	t.Helper()
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyRequestID, id)
	_, err := rt.semantic.Classify(ctx, complexity.ComplexityInput{LastUserText: "simple request"})
	require.NoError(t, err)
}

// captureNamespacesOf lists the capture namespaces in the plugin's store.
func captureNamespacesOf(t *testing.T, p *RoutingPlugin) []string {
	t.Helper()
	namespaces, err := complexity.ListCaptureNamespaces(context.Background(), p.captureStore())
	require.NoError(t, err)
	return namespaces
}

func TestComplexityCaptureFollowsSuggestionsConfig(t *testing.T) {
	p := newCapturePlugin(t, nil)
	classifyAs(t, p, "off")
	assert.False(t, p.ComplexityCaptureStats().Enabled, "capture is off without a suggestions block")
	assert.Zero(t, p.ComplexityCaptureStats().Queued)

	serveCaptureRouter(t, p, captureRouterID, &configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1})
	classifyAs(t, p, "on")
	require.Eventually(t, func() bool { return p.ComplexityCaptureStats().Written == 1 }, 2*time.Second, 5*time.Millisecond)
	assert.Equal(t, complexity.DefaultSuggestionsLookbackDays, p.capture.keepDays(routerLookbackDays(captureRouter(t, p))), "an unset lookback takes the default")

	serveCaptureRouter(t, p, captureRouterID, &configstore.ComplexitySuggestionsConfig{Enabled: false})
	classifyAs(t, p, "off-again")
	assert.False(t, p.ComplexityCaptureStats().Enabled)
	assert.EqualValues(t, 1, p.ComplexityCaptureStats().Queued)
}

// Turning sampling off keeps the lookback the operator set for what was
// already captured: a 3-day one must not be stretched to the 7-day default,
// nor a 30-day one cut to it.
func TestComplexityCaptureKeepsConfiguredLookbackWhenSamplingIsOff(t *testing.T) {
	p := newCapturePlugin(t, nil)
	for _, days := range []int{3, 30} {
		serveCaptureRouter(t, p, captureRouterID, &configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1, LookbackDays: days})
		serveCaptureRouter(t, p, captureRouterID, &configstore.ComplexitySuggestionsConfig{Enabled: false, LookbackDays: days})
		assert.False(t, p.ComplexityCaptureStats().Enabled, "sampling is off")
		assert.Equal(t, days, p.capture.keepDays(routerLookbackDays(captureRouter(t, p))), "cleanup keeps the configured %d-day lookback", days)
	}

	// No lookback configured: the default still bounds what was captured.
	serveCaptureRouter(t, p, captureRouterID, &configstore.ComplexitySuggestionsConfig{Enabled: false})
	assert.Equal(t, complexity.DefaultSuggestionsLookbackDays, p.capture.keepDays(routerLookbackDays(captureRouter(t, p))))
}

func TestComplexityCaptureKeepsVectorsNoLongerThanLogs(t *testing.T) {
	cc := &complexityCapture{}
	assert.Equal(t, complexity.DefaultSuggestionsLookbackDays, cc.keepDays(0), "suggestions off still bounds what was captured")
	assert.Equal(t, 14, cc.keepDays(14), "logs kept forever: the lookback bounds vectors")

	days := 30
	retention := func() int { return days }
	cc.logRetention.Store(&retention)
	assert.Equal(t, 14, cc.keepDays(14))
	days = 3
	assert.Equal(t, 3, cc.keepDays(14), "a vector must not outlive its log")
}

func TestComplexityCaptureUpkeepSweepsAndForgets(t *testing.T) {
	p := newCapturePlugin(t, &configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1, LookbackDays: 7})
	for _, id := range []string{"a", "b", "c"} {
		classifyAs(t, p, id)
	}
	require.Eventually(t, func() bool { return p.ComplexityCaptureStats().Written == 3 }, 2*time.Second, 5*time.Millisecond)
	ctx := context.Background()
	rt := captureRouter(t, p)

	p.ForgetComplexityCapturedRequests(ctx, []string{"a"})

	// A pass now keeps everything younger than the lookback: b and c are
	// still there for a sweep of everything to find, a is not.
	p.captureUpkeepOnce(ctx, time.Now())
	n, err := rt.semantic.SweepCapturedRequests(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 2, n, "upkeep kept b and c; a was forgotten with its log")

	// Eight days on, the 7-day lookback has passed: the pass sweeps it.
	classifyAs(t, p, "d")
	require.Eventually(t, func() bool { return p.ComplexityCaptureStats().Written == 4 }, 2*time.Second, 5*time.Millisecond)
	p.captureUpkeepOnce(ctx, time.Now().Add(8*24*time.Hour))
	n, err = rt.semantic.SweepCapturedRequests(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Zero(t, n, "the upkeep pass should already have swept d")
}

func TestComplexityCaptureClosesWithPlugin(t *testing.T) {
	p := newCapturePlugin(t, &configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1})
	rt := captureRouter(t, p)
	classifyAs(t, p, "a")
	require.NoError(t, p.Cleanup())
	st := rt.capture.Stats()
	assert.False(t, st.Enabled)
	assert.EqualValues(t, 1, st.Queued)
	assert.EqualValues(t, st.Queued, st.Written+st.Failed, "cleanup drains accepted samples")
}

// Each model router captures into namespaces of its own, at its own rate, so
// one router's runs never read another's requests.
func TestComplexityCaptureIsPerRouter(t *testing.T) {
	p := newCapturePlugin(t, &configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1})
	serveCaptureRouter(t, p, "other-router", nil)
	other, ok := p.modelRouter("other-router")
	require.True(t, ok)

	classifyAs(t, p, "mine")
	classifyWith(t, other, "theirs")
	require.Eventually(t, func() bool { return p.ComplexityCaptureStats().Written == 1 }, 2*time.Second, 5*time.Millisecond)
	assert.Zero(t, other.capture.Stats().Queued, "a router without suggestions captures nothing")

	serveCaptureRouter(t, p, "other-router", &configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1})
	classifyWith(t, other, "theirs-too")
	require.Eventually(t, func() bool { return p.ComplexityCaptureStats().Written == 2 }, 2*time.Second, 5*time.Millisecond)

	namespaces := captureNamespacesOf(t, p)
	require.Len(t, namespaces, 2)
	for _, ns := range namespaces {
		key, ok := complexity.CaptureNamespaceRouterKey(ns)
		require.True(t, ok, ns)
		assert.Contains(t, []string{complexity.CaptureRouterKey(captureRouterID), complexity.CaptureRouterKey("other-router")}, key)
	}
	mine, err := p.captureStore().ListNamespaces(context.Background(), complexity.CaptureRouterPrefix(captureRouterID))
	require.NoError(t, err)
	require.Len(t, mine, 1)
	_, err = p.captureStore().GetChunk(context.Background(), mine[0], "theirs-too")
	assert.Error(t, err, "another router's request is not in this router's namespace")
}

// The Complexity Router page's config makes no suggestions, so its classifier
// captures nothing, whatever its suggestions block says.
func TestComplexityConfigCapturesNothing(t *testing.T) {
	p := newCapturePlugin(t, nil)
	p.semanticClassifier.SetEmbeddingFunc(captureTestEmbedding)
	require.NoError(t, p.storeComplexityAnalyzerConfig(captureTestConfig(&configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1})))
	require.Eventually(t, func() bool { return p.semanticClassifier.Status().State == complexity.SemanticStatusReady },
		2*time.Second, 5*time.Millisecond)
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyRequestID, "config-request")
	_, err := p.semanticClassifier.Classify(ctx, complexity.ComplexityInput{LastUserText: "simple request"})
	require.NoError(t, err)
	assert.Zero(t, p.ComplexityCaptureStats().Queued)
	assert.Empty(t, captureNamespacesOf(t, p))
}

// Deleting a router drops what it captured, and only that.
func TestRemoveModelRouterDropsItsCaptures(t *testing.T) {
	p := newCapturePlugin(t, &configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1})
	serveCaptureRouter(t, p, "other-router", &configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1})
	other, _ := p.modelRouter("other-router")
	classifyAs(t, p, "mine")
	classifyWith(t, other, "theirs")
	require.Eventually(t, func() bool { return p.ComplexityCaptureStats().Written == 2 }, 2*time.Second, 5*time.Millisecond)

	require.NoError(t, p.RemoveModelRouter("other-router"))
	require.Eventually(t, func() bool { return len(captureNamespacesOf(t, p)) == 1 }, 2*time.Second, 5*time.Millisecond)
	key, ok := complexity.CaptureNamespaceRouterKey(captureNamespacesOf(t, p)[0])
	require.True(t, ok)
	assert.Equal(t, complexity.CaptureRouterKey(captureRouterID), key)
}

// The upkeep drops namespaces from before captures were per router, and ages
// out those of routers this node does not serve on the default lookback.
func TestComplexityCaptureUpkeepDropsUnroutedNamespaces(t *testing.T) {
	p := newCapturePlugin(t, &configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1, LookbackDays: 30})
	ctx := context.Background()
	store := p.captureStore()
	legacy := complexity.CaptureNamespacePrefix + "_" + strings.Repeat("ab", 16)
	require.NoError(t, store.CreateNamespace(ctx, legacy, 2, nil))
	unserved := complexity.CaptureNamespace("deleted-elsewhere", "openai", "test-embedding-model", 2, 1)
	classifyAs(t, p, "kept")
	require.Eventually(t, func() bool { return p.ComplexityCaptureStats().Written == 1 }, 2*time.Second, 5*time.Millisecond)
	require.NoError(t, store.CreateNamespace(ctx, unserved, 2, nil))
	require.NoError(t, store.Add(ctx, unserved, "11111111-1111-4111-8111-111111111111", []float32{1, 0}, map[string]any{
		"kind": "request", "timestamp": time.Now().Unix(), "log_id": "old",
	}))

	// Eight days on: past the 7-day default, inside the router's 30 days.
	p.captureUpkeepOnce(ctx, time.Now().Add(8*24*time.Hour))
	namespaces := captureNamespacesOf(t, p)
	assert.NotContains(t, namespaces, legacy, "a namespace from before captures were per router is dropped")
	n, err := complexity.SweepCaptureNamespaces(ctx, store, []string{unserved}, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Zero(t, n, "an unserved router's captures aged out on the default lookback")
	n, err = captureRouter(t, p).semantic.SweepCapturedRequests(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the served router keeps its own 30-day lookback")
}

// routersStore answers the model router list the capture upkeep reads.
type routersStore struct {
	configstore.ConfigStore
	routers []configstore.ModelRouter
	err     error
}

func (s *routersStore) GetModelRouters(context.Context) ([]configstore.ModelRouter, error) {
	return s.routers, s.err
}

// A router this node does not serve may be another node's, or saved but left
// out here: its captures keep the lookback the config store has for it. Only
// a router the store no longer holds ages out on the default, and a store that
// cannot be read sweeps none of them.
func TestComplexityCaptureUpkeepKeepsUnservedStoredRouters(t *testing.T) {
	ctx := context.Background()
	addOld := func(t *testing.T, p *RoutingPlugin, routerID string) string {
		t.Helper()
		ns := complexity.CaptureNamespace(routerID, "openai", "test-embedding-model", 2, 1)
		store := p.captureStore()
		require.NoError(t, store.CreateNamespace(ctx, ns, 2, nil))
		require.NoError(t, store.Add(ctx, ns, "11111111-1111-4111-8111-111111111111", []float32{1, 0}, map[string]any{
			"kind": "request", "timestamp": time.Now().Unix(), "log_id": "old",
		}))
		return ns
	}
	left := func(t *testing.T, p *RoutingPlugin, ns string) int {
		t.Helper()
		n, err := complexity.SweepCaptureNamespaces(ctx, p.captureStore(), []string{ns}, time.Now().Add(time.Hour))
		require.NoError(t, err)
		return n
	}
	elsewhere := configstore.ModelRouter{ID: "served-elsewhere", Name: "Elsewhere"}
	elsewhere.Suggestions = &configstore.ComplexitySuggestionsConfig{LookbackDays: 30}

	t.Run("a stored router keeps its lookback, a deleted one the default", func(t *testing.T) {
		p := newCapturePlugin(t, &configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1, LookbackDays: 30})
		p.configStore = &routersStore{routers: []configstore.ModelRouter{elsewhere}}
		stored, deleted := addOld(t, p, elsewhere.ID), addOld(t, p, "deleted-everywhere")
		// Ten days on: past the 7-day default, inside the stored router's 30.
		p.captureUpkeepOnce(ctx, time.Now().Add(10*24*time.Hour))
		assert.Equal(t, 1, left(t, p, stored), "kept for the stored router's 30-day lookback")
		assert.Zero(t, left(t, p, deleted), "a router no one holds ages out on the default")
	})

	t.Run("an unreadable store sweeps no unserved router", func(t *testing.T) {
		p := newCapturePlugin(t, &configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1, LookbackDays: 30})
		p.configStore = &routersStore{err: errors.New("config store down")}
		ns := addOld(t, p, "deleted-everywhere")
		p.captureUpkeepOnce(ctx, time.Now().Add(40*24*time.Hour))
		assert.Equal(t, 1, left(t, p, ns), "left for a pass that can tell")
	})
}

// recordedDeletes is an in-memory CaptureDeleteRecorder.
type recordedDeletes struct {
	mu   sync.Mutex
	rows []complexity.CaptureDelete
}

func (r *recordedDeletes) AddCaptureDeletes(_ context.Context, rows []complexity.CaptureDelete) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, rows...)
	return nil
}

func (r *recordedDeletes) ListCaptureDeletes(_ context.Context, limit int) ([]complexity.CaptureDelete, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]complexity.CaptureDelete(nil), r.rows[:min(limit, len(r.rows))]...), nil
}

func (r *recordedDeletes) DeleteCaptureDeletes(_ context.Context, done []complexity.CaptureDelete) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.rows[:0]
	for _, row := range r.rows {
		if !slices.Contains(done, row) {
			kept = append(kept, row)
		}
	}
	r.rows = kept
	return nil
}

// A delete recorded for retry - here with no namespace, as one recorded
// before the embedding space was known - is retried by the upkeep pass
// against the serving capture space, and leaves the record once it is done.
func TestComplexityCaptureUpkeepRetriesRecordedDeletes(t *testing.T) {
	p := newCapturePlugin(t, &configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1, LookbackDays: 7})
	recorder := &recordedDeletes{}
	p.SetComplexityCaptureDeleteRecorder(recorder)
	for _, id := range []string{"a", "b"} {
		classifyAs(t, p, id)
	}
	require.Eventually(t, func() bool { return p.ComplexityCaptureStats().Written == 2 }, 2*time.Second, 5*time.Millisecond)
	ctx := context.Background()
	require.NoError(t, recorder.AddCaptureDeletes(ctx, []complexity.CaptureDelete{{LogID: "a"}}))

	p.captureUpkeepOnce(ctx, time.Now())
	rows, err := recorder.ListCaptureDeletes(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, rows, "a retried delete leaves the record")
	n, err := captureRouter(t, p).semantic.SweepCapturedRequests(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only b's vector was left for the sweep")
}
