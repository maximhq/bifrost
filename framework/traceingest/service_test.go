package traceingest

import (
	"context"
	"path/filepath"
	"sync"
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
	keys     map[string]*tables.TableTracingKey
	lookups  int
}

func newFakeConfig() *fakeConfig {
	return &fakeConfig{projects: map[string]*tables.TableTraceProject{}, keys: map[string]*tables.TableTracingKey{}}
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

func (f *fakeConfig) GetTracingKey(_ context.Context, id string) (*tables.TableTracingKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, key := range f.keys {
		if key.ID == id {
			return key, nil
		}
	}
	return nil, configstore.ErrNotFound
}

func (f *fakeConfig) GetTracingKeyByHash(_ context.Context, hash string) (*tables.TableTracingKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	if key, ok := f.keys[hash]; ok {
		return key, nil
	}
	return nil, configstore.ErrNotFound
}

func (f *fakeConfig) UpdateTracingKey(_ context.Context, key *tables.TableTracingKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[key.KeyHash] = key
	return nil
}

func (f *fakeConfig) TouchTracingKey(context.Context, string, time.Time) error { return nil }

func (f *fakeConfig) addKey(value string, key *tables.TableTracingKey) {
	key.SetValue(value)
	if key.ID == "" {
		key.ID = "key-" + key.Name
	}
	f.keys[key.KeyHash] = key
}

type testEnv struct {
	service *Service
	store   logstore.LogStore
	config  *fakeConfig
	project *tables.TableTraceProject
	key     *Principal
}

func newTestEnv(t *testing.T, cfg Config) *testEnv {
	t.Helper()
	ctx := context.Background()
	store, err := logstore.NewLogStore(ctx, &logstore.Config{Type: logstore.LogStoreTypeSQLite, Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")}}, testLogger{})
	require.NoError(t, err)
	config := newFakeConfig()
	project := &tables.TableTraceProject{ID: "p1", Name: "agent"}
	config.projects[project.ID] = project
	config.addKey("bf-tr-test", &tables.TableTracingKey{Name: "ci", ProjectIDs: []string{"p1"}, DefaultProjectID: "p1", LogContent: true, Enabled: true})
	service := NewService(store, config, cfg, testLogger{})
	t.Cleanup(func() {
		service.Close()
		_ = store.Close(ctx)
	})
	principal, err := service.Authenticate(ctx, "bf-tr-test")
	require.NoError(t, err)
	return &testEnv{service: service, store: store, config: config, project: project, key: principal}
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
	assert.Equal(t, "key-ci", stored[0].TracingKeyID)
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

// TestKeysAreBoundToProjects pins isolation: no key is rejected, a disabled key is rejected, a key
// cannot write outside its projects, and only a key allowed to create projects makes a new one.
func TestKeysAreBoundToProjects(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, Config{})
	_, err := env.service.Authenticate(ctx, "")
	assert.ErrorIs(t, err, ErrUnauthorized)
	_, err = env.service.Authenticate(ctx, "bf-tr-unknown")
	assert.ErrorIs(t, err, ErrUnauthorized)
	env.config.addKey("bf-tr-off", &tables.TableTracingKey{Name: "off", ProjectIDs: []string{"p1"}, Enabled: false})
	_, err = env.service.Authenticate(ctx, "bf-tr-off")
	assert.ErrorIs(t, err, ErrUnauthorized)

	lookups := env.config.lookups
	_, err = env.service.Authenticate(ctx, "bf-tr-test")
	require.NoError(t, err)
	assert.Equal(t, lookups, env.config.lookups, "a verified key is served from the cache")

	other := &tables.TableTraceProject{ID: "p2", Name: "billing"}
	env.config.projects["p2"] = other
	_, err = env.service.ResolveProject(ctx, env.key, "billing")
	assert.ErrorIs(t, err, ErrForbidden)
	assert.ErrorIs(t, env.service.Ingest(ctx, env.key, other, logstore.TraceSourceOTel, []*SpanPart{{TraceID: "t", SpanID: "s"}}), ErrForbidden)
	_, err = env.service.ResolveProject(ctx, env.key, "brand-new")
	assert.ErrorIs(t, err, ErrForbidden, "this key may not create projects")

	project, err := env.service.ResolveProject(ctx, env.key, "")
	require.NoError(t, err)
	assert.Equal(t, "p1", project.ID, "no project named means the key's default")
	byName, err := env.service.ResolveProject(ctx, env.key, "agent")
	require.NoError(t, err)
	assert.Equal(t, "p1", byName.ID)

	env.config.addKey("bf-tr-maker", &tables.TableTracingKey{Name: "maker", CanCreateProjects: true, LogContent: true, Enabled: true})
	maker, err := env.service.Authenticate(ctx, "bf-tr-maker")
	require.NoError(t, err)
	created, err := env.service.ResolveProject(ctx, maker, "brand-new")
	require.NoError(t, err)
	assert.Equal(t, "brand-new", created.Name)
	require.NoError(t, env.service.Ingest(ctx, maker, created, logstore.TraceSourceBraintrust, []*SpanPart{{TraceID: "t", SpanID: "s", StartTime: at(0)}}))
	again, err := env.service.Authenticate(ctx, "bf-tr-maker")
	require.NoError(t, err)
	assert.Contains(t, again.ProjectIDs, created.ID, "the key is bound to the project it created")
}

// TestRetentionAndRateLimit pins per-project retention and the per-key span cap.
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
