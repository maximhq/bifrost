package traceingest

import (
	"context"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testLogger struct{}

func (testLogger) Debug(string, ...any)                   {}
func (testLogger) Info(string, ...any)                    {}
func (testLogger) Warn(string, ...any)                    {}
func (testLogger) Error(string, ...any)                   {}
func (testLogger) Fatal(string, ...any)                   {}
func (testLogger) SetLevel(schemas.LogLevel)              {}
func (testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// fakeConfig is an in-memory tracing config store.
type fakeConfig struct {
	mu       sync.Mutex
	projects map[string]*tables.TableTraceProject
}

func newFakeConfig() *fakeConfig {
	return &fakeConfig{projects: map[string]*tables.TableTraceProject{}}
}

func (f *fakeConfig) GetTraceProject(_ context.Context, id string) (*tables.TableTraceProject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if project, ok := f.projects[id]; ok {
		return project, nil
	}
	return nil, configstore.ErrNotFound
}

func (f *fakeConfig) GetTraceProjectByName(_ context.Context, name string) (*tables.TableTraceProject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, project := range f.projects {
		if project.Name == name {
			return project, nil
		}
	}
	return nil, configstore.ErrNotFound
}

func (f *fakeConfig) CreateTraceProject(_ context.Context, project *tables.TableTraceProject) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.projects {
		if existing.Name == project.Name {
			return configstore.ErrTraceProjectExists
		}
	}
	if project.ID == "" {
		project.ID = "proj-" + project.Name
	}
	f.projects[project.ID] = project
	return nil
}

type testEnv struct {
	service *Service
	store   logstore.LogStore
	config  *fakeConfig
	project *tables.TableTraceProject
	key     *Principal
}

func newTestEnv(t testing.TB, cfg Config) *testEnv {
	t.Helper()
	ctx := context.Background()
	store, err := logstore.NewLogStore(ctx, &logstore.Config{Type: logstore.LogStoreTypeSQLite, Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")}}, testLogger{})
	require.NoError(t, err)
	config := newFakeConfig()
	project := &tables.TableTraceProject{ID: "p1", Name: "agent"}
	config.projects[project.ID] = project
	if cfg.DefaultProjectName == "" {
		cfg.DefaultProjectName = project.Name
	}
	service := NewService(store, config, cfg, testLogger{})
	t.Cleanup(func() {
		service.Close()
		_ = store.Close(ctx)
	})
	return &testEnv{service: service, store: store, config: config, project: project, key: AdminPrincipal()}
}

func at(ms int) *time.Time {
	return new(time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC).Add(time.Duration(ms) * time.Millisecond))
}

// TestIngestStoresSpansBeforeItReturns pins the ack rule: when Ingest returns nil the parts and the
// folded spans are already in the store, and the trace is listed once its rollup runs.
func TestIngestStoresSpansBeforeItReturns(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	spans := []*SpanPart{
		{TraceID: "t1", SpanID: "root", Name: "agent", Kind: logstore.TraceSpanKindTask, StartTime: at(0), EndTime: at(900), Input: "hi", Output: "bye"},
		{TraceID: "t1", SpanID: "llm", ParentSpanIDs: []string{"root"}, Name: "chat", Kind: logstore.TraceSpanKindLLM, StartTime: at(100), EndTime: at(800), PromptTokens: 10, CompletionTokens: 5, Scores: map[string]float64{"accuracy": 1}, Comments: []string{"looks right"}},
	}
	require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceBraintrust, spans))

	stored, err := env.store.ListTraceSpans(ctx, "p1", "t1")
	require.NoError(t, err)
	require.Len(t, stored, 2, "both spans are stored when the ack is sent")
	parts, err := env.store.ListTraceSpanParts(ctx, "p1", "t1", "llm")
	require.NoError(t, err)
	require.Len(t, parts, 1)
	require.NotNil(t, parts[0].ExpiresAt, "parts are kept for refolds only, not forever")

	require.NoError(t, env.service.FlushRollups(ctx))
	trace, err := env.store.FindTrace(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Equal(t, "agent", trace.Name)
	assert.Equal(t, 15, trace.TotalTokens)
	assert.Equal(t, map[string]float64{"accuracy": 1}, trace.ScoresParsed, "a score sent on the span row becomes a score row")
	comments, err := env.store.ListTraceComments(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Len(t, comments, 1)

	// A retried batch stores nothing twice.
	require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceBraintrust, []*SpanPart{
		{TraceID: "t1", SpanID: "llm", ParentSpanIDs: []string{"root"}, Name: "chat", Kind: logstore.TraceSpanKindLLM, StartTime: at(100), EndTime: at(800), PromptTokens: 10, CompletionTokens: 5, Scores: map[string]float64{"accuracy": 1}, Comments: []string{"looks right"}},
	}))
	parts, err = env.store.ListTraceSpanParts(ctx, "p1", "t1", "llm")
	require.NoError(t, err)
	assert.Len(t, parts, 1)
	scores, err := env.store.ListTraceScores(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Len(t, scores, 1)
}

// TestIngestFoldsPartsAcrossRequestsInAnyOrder pins the cross-request fold: a merge row committed
// before its base still ends up in the one whole span.
func TestIngestFoldsPartsAcrossRequestsInAnyOrder(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	merge := &SpanPart{TraceID: "t1", SpanID: "s", IsMerge: true, Output: "done", EndTime: at(500), Metadata: map[string]any{"b": 2.0}}
	require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceBraintrust, []*SpanPart{merge}))
	spans, err := env.store.ListTraceSpans(ctx, "p1", "t1")
	require.NoError(t, err)
	require.Len(t, spans, 1)
	assert.True(t, spans[0].Partial, "the base has not arrived")

	base := &SpanPart{TraceID: "t1", SpanID: "s", Name: "tool", Kind: logstore.TraceSpanKindTool, StartTime: at(0), Input: "q", Output: "pending", Metadata: map[string]any{"a": 1.0}}
	require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceBraintrust, []*SpanPart{base}))
	spans, err = env.store.ListTraceSpans(ctx, "p1", "t1")
	require.NoError(t, err)
	require.Len(t, spans, 1)
	assert.False(t, spans[0].Partial)
	assert.Equal(t, "tool", spans[0].Name)
	assert.Equal(t, "done", spans[0].Output, "the merge row wins over the base that arrived after it")
	assert.Equal(t, map[string]any{"a": 1.0, "b": 2.0}, spans[0].MetadataParsed)
	assert.Equal(t, *at(0), spans[0].StartTime.UTC())
}

// TestIngestGroupsConcurrentRequestsIntoFewCommits pins the group commit: many single-span requests
// in flight at once all succeed and all land.
func TestIngestGroupsConcurrentRequestsIntoFewCommits(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{CommitWindow: 20 * time.Millisecond})
	const requests = 60
	var wg sync.WaitGroup
	errs := make(chan error, requests)
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, []*SpanPart{
				{TraceID: "t1", SpanID: "span-" + string(rune('A'+i%26)) + string(rune('a'+i/26)), Name: "work", StartTime: at(i)},
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	spans, err := env.store.ListTraceSpans(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Len(t, spans, requests)
}

// TestIngestAppliesContentSwitchAndMasking pins content control: a key with content off stores no
// input or output, in the span and in the kept part alike, and masking rules rewrite stored text.
func TestIngestAppliesContentSwitchAndMasking(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	env.project.MaskingRules = []tables.TraceMaskingRule{{Name: "email", Pattern: `[\w.]+@[\w.]+`, Replacement: "[email]"}}
	env.project.UpdatedAt = time.Now()
	require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, []*SpanPart{
		{TraceID: "t1", SpanID: "s", Name: "x", StartTime: at(0), Input: map[string]any{"messages": []any{"mail a@b.co now"}}, Output: "sent to a@b.co", Metadata: map[string]any{"owner": "a@b.co"}},
	}))
	spans, err := env.store.ListTraceSpans(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Equal(t, `{"messages":["mail [email] now"]}`, spans[0].Input)
	assert.Equal(t, "sent to [email]", spans[0].Output)
	assert.Equal(t, "[email]", spans[0].MetadataParsed["owner"])
	parts, err := env.store.ListTraceSpanParts(ctx, "p1", "t1", "s")
	require.NoError(t, err)
	assert.NotContains(t, parts[0].Payload, "a@b.co", "the kept part is masked too")

	quiet := *env.key
	quiet.LogContent = false
	require.NoError(t, env.service.Ingest(ctx, &quiet, env.project, logstore.TraceSourceOTel, []*SpanPart{
		{TraceID: "t2", SpanID: "s", Name: "x", StartTime: at(0), Input: "secret", Output: "secret", PromptTokens: 3},
	}))
	spans, err = env.store.ListTraceSpans(ctx, "p1", "t2")
	require.NoError(t, err)
	assert.Empty(t, spans[0].Input)
	assert.Empty(t, spans[0].Output)
	assert.True(t, spans[0].ContentHidden)
	assert.Equal(t, 3, spans[0].PromptTokens, "usage is kept when content is not")
	parts, err = env.store.ListTraceSpanParts(ctx, "p1", "t2", "s")
	require.NoError(t, err)
	assert.NotContains(t, parts[0].Payload, "secret")
}

// TestProjectsResolveByNameAndScopedPrincipals pins project resolution: a request that names no
// project goes to the default one, a name is created on first use, and a scoped principal (an
// enterprise API key) writes only to its own projects.
func TestProjectsResolveByNameAndScopedPrincipals(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	admin := AdminPrincipal()

	project, err := env.service.ResolveProject(ctx, admin, "")
	require.NoError(t, err)
	assert.Equal(t, "p1", project.ID, "no project named means the default project")
	byName, err := env.service.ResolveProject(ctx, admin, "agent")
	require.NoError(t, err)
	assert.Equal(t, "p1", byName.ID)
	created, err := env.service.ResolveProject(ctx, admin, "brand-new")
	require.NoError(t, err)
	assert.Equal(t, "brand-new", created.Name, "a project an SDK names is created on first use")
	again, err := env.service.ResolveProject(ctx, admin, "brand-new")
	require.NoError(t, err)
	assert.Equal(t, created.ID, again.ID)

	fresh := newTestEnv(t, Config{DefaultProjectName: DefaultProjectName})
	defaulted, err := fresh.service.ResolveProject(ctx, admin, "")
	require.NoError(t, err)
	assert.Equal(t, DefaultProjectName, defaulted.Name, "the default project is created on first use")

	scoped := &Principal{ProjectIDs: []string{"p1"}, LogContent: true}
	_, err = env.service.ResolveProject(ctx, scoped, "brand-new")
	assert.ErrorIs(t, err, ErrForbidden, "a scoped principal cannot write to another project")
	_, err = env.service.ResolveProject(ctx, scoped, "never-made")
	assert.ErrorIs(t, err, ErrForbidden, "a scoped principal cannot create projects")
	assert.ErrorIs(t, env.service.Ingest(ctx, scoped, created, logstore.TraceSourceOTel, []*SpanPart{{TraceID: "t", SpanID: "s"}}), ErrForbidden)
	own, err := env.service.ResolveProject(ctx, scoped, "")
	require.NoError(t, err)
	assert.Equal(t, "p1", own.ID, "a principal scoped to one project defaults to it")
}

// TestRetentionAndRateLimit pins per-project retention and the span rate cap.
func TestRetentionAndRateLimit(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{MaxSpansPerSecond: 2, RetentionDays: func() int { return 90 }})
	require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, []*SpanPart{{TraceID: "t1", SpanID: "a", StartTime: at(0)}, {TraceID: "t1", SpanID: "b", StartTime: at(0)}}))
	assert.ErrorIs(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, []*SpanPart{{TraceID: "t1", SpanID: "c", StartTime: at(0)}}), ErrRateLimited)
	spans, err := env.store.ListTraceSpans(ctx, "p1", "t1")
	require.NoError(t, err)
	require.Len(t, spans, 2)
	require.NotNil(t, spans[0].ExpiresAt)
	assert.WithinDuration(t, time.Now().AddDate(0, 0, 90), *spans[0].ExpiresAt, time.Minute, "a project with no retention of its own takes the global one")

	short := newTestEnv(t, Config{RetentionDays: func() int { return 90 }})
	short.project.RetentionDays = new(7)
	require.NoError(t, short.service.Ingest(ctx, short.key, short.project, logstore.TraceSourceOTel, []*SpanPart{{TraceID: "t1", SpanID: "a", StartTime: at(0)}}))
	spans, err = short.store.ListTraceSpans(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().AddDate(0, 0, 7), *spans[0].ExpiresAt, time.Minute)
}

// fakeEvaluator scores every trace 0.9 and counts its calls.
type fakeEvaluator struct {
	mu    sync.Mutex
	calls []map[string]string
}

func (f *fakeEvaluator) EvaluateTrace(_ context.Context, _ string, variables map[string]string) (*TraceEvaluation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, variables)
	return &TraceEvaluation{Value: new(0.9), Reasoning: "grounded"}, nil
}

func (f *fakeEvaluator) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// TestEvaluatorRulesScoreACompletedTraceOnce pins online evaluation: a rule scores a trace when it
// completes, never while it is still arriving, never twice, never outside its sample, and never
// past its daily cap. The result is a score on the trace, written by the evaluator.
func TestEvaluatorRulesScoreACompletedTraceOnce(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	evaluator := &fakeEvaluator{}
	env.service.SetTraceEvaluator(evaluator)
	env.project.Evaluators = []tables.TraceEvaluatorRule{
		{EvaluatorID: "ev-faithful", Name: "faithfulness", SampleRate: 1, DailyCap: 2, Enabled: true},
		{EvaluatorID: "ev-off", SampleRate: 1, Enabled: false},
		{EvaluatorID: "ev-never", SampleRate: 0, Enabled: true},
	}
	ingest := func(traceID string, end *time.Time) {
		require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, []*SpanPart{{TraceID: traceID, SpanID: "root", Name: "run", Kind: logstore.TraceSpanKindTask, StartTime: at(0), EndTime: end, Input: "question", Output: "answer", Expected: "answer"}}))
		require.NoError(t, env.service.FlushRollups(ctx))
	}

	ingest("running", nil)
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, evaluator.count(), "a trace that is still arriving is not scored")

	ingest("t1", at(500))
	require.Eventually(t, func() bool {
		scores, _ := env.store.ListTraceScores(ctx, "p1", "t1")
		return len(scores) == 1
	}, 3*time.Second, 10*time.Millisecond)
	scores, err := env.store.ListTraceScores(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Equal(t, "faithfulness", scores[0].Name)
	assert.Equal(t, logstore.TraceAuthorEvaluator, scores[0].Source)
	assert.Equal(t, 0.9, *scores[0].Value)
	assert.Equal(t, "grounded", scores[0].Comment)
	evaluator.mu.Lock()
	assert.Equal(t, map[string]string{"input": "question", "output": "answer", "expected_output": "answer"}, evaluator.calls[0])
	evaluator.mu.Unlock()

	// The score marks the trace dirty; the rollup that follows must not score it again.
	require.NoError(t, env.service.FlushRollups(ctx))
	require.NoError(t, env.service.FlushRollups(ctx))
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, evaluator.count(), "a trace is scored once per rule")
	trace, err := env.store.FindTrace(ctx, "p1", "t1")
	require.NoError(t, err)
	assert.Equal(t, 0.9, trace.ScoresParsed["faithfulness"])

	ingest("t2", at(500))
	require.Eventually(t, func() bool { return evaluator.count() == 2 }, 3*time.Second, 10*time.Millisecond)
	ingest("t3", at(500))
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 2, evaluator.count(), "the daily cap stops the third trace")

	assert.True(t, sampled("any", "ev", 1))
	assert.False(t, sampled("any", "ev", 0))
	assert.Equal(t, sampled("trace-a", "ev", 0.5), sampled("trace-a", "ev", 0.5), "a trace is in the sample or out of it for good")
	in := 0
	for i := range 2000 {
		if sampled(string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676)), "ev", 0.25) {
			in++
		}
	}
	assert.InDelta(t, 500, in, 120, "the sample is close to its rate")
}

// benchTrace builds one OTLP-shaped trace of n spans under a fresh trace id.
func benchTrace(traceID string, n int) []*SpanPart {
	start := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	spans := make([]*SpanPart, n)
	for i := range spans {
		spans[i] = &SpanPart{
			TraceID: traceID, SpanID: "s" + strconv.Itoa(i), Name: "chat", Kind: logstore.TraceSpanKindLLM,
			StartTime: &start, EndTime: new(start.Add(time.Second)), Model: "gpt-4o", PromptTokens: 18, CompletionTokens: 4,
			Input:      []any{map[string]any{"role": "user", "content": "Who wrote Dune?"}},
			Output:     "Frank Herbert.",
			Attributes: map[string]any{"llm.provider": "openai", "otel.scope.name": "bench", "resource": map[string]any{"service.name": "bench"}},
		}
		if i > 0 {
			spans[i].ParentSpanIDs = []string{"s0"}
		}
	}
	return spans
}

// BenchmarkIngest measures the whole ingest path on SQLite, acknowledged write included: encode the
// parts, group commit, fold, store the spans and mark the trace dirty.
func BenchmarkIngest(b *testing.B) {
	for _, size := range []int{1, 10} {
		b.Run(strconv.Itoa(size)+"_spans", func(b *testing.B) {
			env := newTestEnv(b, Config{CommitWindow: time.Microsecond, RollupInterval: time.Hour})
			ctx := context.Background()
			b.ReportAllocs()
			i := 0
			for b.Loop() {
				i++
				if err := env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, benchTrace("trace-"+strconv.Itoa(i), size)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestStoredSpanEqualsARefoldFromItsStoredParts pins that the write path, which folds the parts it
// holds in memory, stores the same span a fold of the parts read back from the store gives. The two
// must agree for every value shape an SDK sends: integers, nested maps, lists, plain text.
func TestStoredSpanEqualsARefoldFromItsStoredParts(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	cost := 0.0012
	parts := []*SpanPart{
		{TraceID: "t1", SpanID: "s1", Name: "chat", Kind: logstore.TraceSpanKindLLM, StartTime: at(0), EndTime: at(900),
			Input:    []any{map[string]any{"role": "user", "content": "hi", "n": int64(3)}},
			Output:   map[string]any{"text": "hello", "choices": []any{int64(1), 2.5, "x"}},
			Metadata: map[string]any{"count": int64(7), "nested": map[string]any{"ratio": 0.5}}, Tags: []string{"b", "a"},
			Attributes: map[string]any{"http.response.status_code": int64(200), "flags": []any{true, "y"}},
			Model:      "gpt-4o", PromptTokens: 18, CompletionTokens: 4, Cost: &cost, Scores: map[string]float64{"ok": 1}},
		{TraceID: "t1", SpanID: "s2", ParentSpanIDs: []string{"s1"}, Name: "tool", Kind: logstore.TraceSpanKindTool, StartTime: at(100), EndTime: at(200), Output: "done"},
	}
	require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, parts))
	// A merge row in a later request is laid over the stored span.
	require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, []*SpanPart{
		{TraceID: "t1", SpanID: "s1", IsMerge: true, Metadata: map[string]any{"late": int64(1)}, Tags: []string{"c"}},
	}))

	stored, err := env.store.ListTraceSpans(ctx, env.project.ID, "t1")
	require.NoError(t, err)
	require.Len(t, stored, 2)
	for _, span := range stored {
		rows, err := env.store.ListTraceSpanParts(ctx, env.project.ID, "t1", span.SpanID)
		require.NoError(t, err)
		decoded := make([]*SpanPart, len(rows))
		for i, row := range rows {
			decoded[i], err = partFromStored(row)
			require.NoError(t, err)
		}
		refolded := Fold(nil, decoded).Span(env.project.ID, logstore.TraceSourceOTel, nil)
		require.NoError(t, refolded.SerializeFields())
		assert.Equal(t, refolded.Input, span.Input, span.SpanID)
		assert.Equal(t, refolded.Output, span.Output, span.SpanID)
		assert.Equal(t, refolded.Metadata, span.Metadata, span.SpanID)
		assert.Equal(t, refolded.Attributes, span.Attributes, span.SpanID)
		assert.Equal(t, refolded.Tags, span.Tags, span.SpanID)
		assert.Equal(t, refolded.ParentSpanID, span.ParentSpanID, span.SpanID)
		assert.Equal(t, refolded.TotalTokens, span.TotalTokens, span.SpanID)
		require.NotNil(t, span.DurationMs)
		assert.InDelta(t, *refolded.DurationMs, *span.DurationMs, 1e-9, span.SpanID)
	}
	assert.Equal(t, `["a","b","c"]`, *stored[0].Tags)
	assert.Equal(t, `{"count":7,"late":1,"nested":{"ratio":0.5}}`, *stored[0].Metadata)
}

// countingStore counts the part writes of a slow store, to show how requests share commits.
type countingStore struct {
	logstore.LogStore
	commits atomic.Int64
	delay   time.Duration
}

func (c *countingStore) BatchCreateTraceSpanParts(ctx context.Context, parts []*logstore.TraceSpanPart) (int, error) {
	c.commits.Add(1)
	time.Sleep(c.delay)
	return c.LogStore.BatchCreateTraceSpanParts(ctx, parts)
}

// TestCommitWritesALoneRequestAtOnceAndBatchesUnderLoad pins the group commit policy. A request that
// arrives alone is written at once: SDKs that export inline (Phoenix by default) wait for the ack on
// the application's own thread, so a fixed wait per request is latency in the user's app. Requests
// that arrive while a write is in flight queue behind it and share the next write.
func TestCommitWritesALoneRequestAtOnceAndBatchesUnderLoad(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	one := func(i int) []*SpanPart {
		return []*SpanPart{{TraceID: "lone", SpanID: "s" + strconv.Itoa(i), Name: "work", StartTime: at(i)}}
	}
	const sequential = 50
	began := time.Now()
	for i := range sequential {
		require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, one(i)))
	}
	// A 5ms wait per request would take 250ms here on any machine.
	assert.Less(t, time.Since(began), 200*time.Millisecond, "a lone request must not wait for a commit window")

	slow := &countingStore{LogStore: env.store, delay: 5 * time.Millisecond}
	service := NewService(slow, env.config, Config{DefaultProjectName: env.project.Name}, testLogger{})
	defer service.Close()
	const concurrent = 60
	var wg sync.WaitGroup
	errs := make(chan error, concurrent)
	for i := range concurrent {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, []*SpanPart{
				{TraceID: "busy", SpanID: "s" + strconv.Itoa(i), Name: "work", StartTime: at(i)},
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	spans, err := env.store.ListTraceSpans(ctx, "p1", "busy")
	require.NoError(t, err)
	assert.Len(t, spans, concurrent)
	assert.LessOrEqual(t, slow.commits.Load(), int64(concurrent/3), "requests in flight during a write share the next one")
}

// TestIngest_AllocationScaling pins how much memory storing a span costs relative to what it carries.
// A span with an inline image is megabytes. The write path encodes it for the part and for the span
// row and hands both to the driver; it must not also read the part back and decode it again, which
// is what it once did.
func TestIngest_AllocationScaling(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{RollupInterval: time.Hour})
	const size = 1 << 20
	image := strings.Repeat("A", size)
	i := 0
	ingest := func() {
		i++
		require.NoError(t, env.service.Ingest(ctx, env.key, env.project, logstore.TraceSourceOTel, []*SpanPart{{
			TraceID: "big-" + strconv.Itoa(i), SpanID: "s1", Name: "vision", Kind: logstore.TraceSpanKindLLM, StartTime: at(0), EndTime: at(10),
			Input: []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + image}}}}},
		}}))
	}
	ingest()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	const runs = 4
	for range runs {
		ingest()
	}
	runtime.ReadMemStats(&after)
	perSpan := (after.TotalAlloc - before.TotalAlloc) / runs
	t.Logf("storing a %d byte span allocated %d bytes (%.1fx)", size, perSpan, float64(perSpan)/size)
	// Two JSON encodes with buffer growth, two driver binds and the test's own input come to about 8x.
	assert.Less(t, perSpan, uint64(10*size), "storing a span allocated %.1f times its size", float64(perSpan)/size)
}
