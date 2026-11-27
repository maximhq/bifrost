package handlers

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/traceingest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// traceTestConfig is an in-memory tracing config store for handler tests.
type traceTestConfig struct {
	mu       sync.Mutex
	projects map[string]*tables.TableTraceProject
	keys     map[string]*tables.TableTracingKey
}

func (f *traceTestConfig) GetTraceProject(_ context.Context, id string) (*tables.TableTraceProject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if project, ok := f.projects[id]; ok {
		return project, nil
	}
	return nil, configstore.ErrNotFound
}

func (f *traceTestConfig) GetTraceProjectByName(_ context.Context, name string) (*tables.TableTraceProject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, project := range f.projects {
		if project.Name == name {
			return project, nil
		}
	}
	return nil, configstore.ErrNotFound
}

func (f *traceTestConfig) CreateTraceProject(_ context.Context, project *tables.TableTraceProject) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if project.ID == "" {
		project.ID = "proj-" + project.Name
	}
	f.projects[project.ID] = project
	return nil
}

func (f *traceTestConfig) GetTracingKey(_ context.Context, id string) (*tables.TableTracingKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, key := range f.keys {
		if key.ID == id {
			return key, nil
		}
	}
	return nil, configstore.ErrNotFound
}

func (f *traceTestConfig) GetTracingKeyByHash(_ context.Context, hash string) (*tables.TableTracingKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if key, ok := f.keys[hash]; ok {
		return key, nil
	}
	return nil, configstore.ErrNotFound
}

func (f *traceTestConfig) UpdateTracingKey(_ context.Context, key *tables.TableTracingKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[key.KeyHash] = key
	return nil
}

func (f *traceTestConfig) TouchTracingKey(context.Context, string, time.Time) error { return nil }

// traceTestKey is the tracing key every handler test authenticates with.
const traceTestKey = "bf-tr-handler-test"

type traceIngestTestEnv struct {
	handler *TraceIngestHandler
	router  *router.Router
	store   logstore.LogStore
	service *traceingest.Service
	config  *traceTestConfig
}

// newTraceIngestTestEnv builds the ingest handler over a real SQLite trace store, with one project
// "agent" (id p1) and one key bound to it that may also create projects.
func newTraceIngestTestEnv(t *testing.T) *traceIngestTestEnv {
	t.Helper()
	ctx := context.Background()
	store, err := logstore.NewLogStore(ctx, &logstore.Config{Type: logstore.LogStoreTypeSQLite, Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")}}, logger)
	require.NoError(t, err)
	config := &traceTestConfig{projects: map[string]*tables.TableTraceProject{"p1": {ID: "p1", Name: "agent"}}, keys: map[string]*tables.TableTracingKey{}}
	key := &tables.TableTracingKey{ID: "k1", Name: "test", ProjectIDs: []string{"p1"}, DefaultProjectID: "p1", LogContent: true, CanCreateProjects: true, Enabled: true}
	key.SetValue(traceTestKey)
	config.keys[key.KeyHash] = key
	service := traceingest.NewService(store, config, traceingest.Config{}, logger)
	t.Cleanup(func() {
		service.Close()
		_ = store.Close(ctx)
	})
	handler := NewTraceIngestHandler(service, nil, logger)
	rt := router.New()
	handler.RegisterRoutes(rt)
	return &traceIngestTestEnv{handler: handler, router: rt, store: store, service: service, config: config}
}

// do sends one request through the router and returns the response context.
func (e *traceIngestTestEnv) do(method, uri string, headers map[string]string, body []byte) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(method)
	ctx.Request.SetRequestURI(uri)
	ctx.Request.Header.SetHost("bifrost.test")
	for name, value := range headers {
		ctx.Request.Header.Set(name, value)
	}
	ctx.Request.SetBody(body)
	e.router.Handler(ctx)
	return ctx
}

// TestTraceUploadURLWorksOnceWithinItsWindow pins uploads: the URL handed to an SDK accepts the
// file with no auth header, then closes, and an unknown or stale id is refused.
func TestTraceUploadURLWorksOnceWithinItsWindow(t *testing.T) {
	env := newTraceIngestTestEnv(t)
	reqCtx := &fasthttp.RequestCtx{}
	reqCtx.Request.Header.SetHost("bifrost.test")
	reqCtx.Request.Header.Set("X-Forwarded-Proto", "https")
	upload, url, err := env.handler.newUpload(reqCtx, env.config.projects["p1"], logstore.TraceSourceBraintrust, "", "image/png", "shot.png")
	require.NoError(t, err)
	assert.Equal(t, "https://bifrost.test/tracing/uploads/"+upload.ID, url)

	resp := env.do(fasthttp.MethodPut, "/tracing/uploads/"+upload.ID, nil, []byte("png-bytes"))
	require.Equal(t, fasthttp.StatusOK, resp.Response.StatusCode(), string(resp.Response.Body()))
	stored, data, err := env.store.ReadTraceUploadBytes(context.Background(), upload.ID)
	require.NoError(t, err)
	assert.Equal(t, []byte("png-bytes"), data)
	assert.Equal(t, logstore.TraceUploadStatusDone, stored.Status)

	again := env.do(fasthttp.MethodPut, "/tracing/uploads/"+upload.ID, nil, []byte("overwrite"))
	assert.Equal(t, fasthttp.StatusNotFound, again.Response.StatusCode(), "a settled upload cannot be rewritten")
	unknown := env.do(fasthttp.MethodPut, "/tracing/uploads/nope", nil, []byte("x"))
	assert.Equal(t, fasthttp.StatusNotFound, unknown.Response.StatusCode())

	stale := &logstore.TraceUpload{ID: "stale", ProjectID: "p1", Source: logstore.TraceSourceBraintrust, CreatedAt: time.Now().Add(-time.Hour)}
	require.NoError(t, env.store.CreateTraceUpload(context.Background(), stale))
	late := env.do(fasthttp.MethodPut, "/tracing/uploads/stale", nil, []byte("x"))
	assert.Equal(t, fasthttp.StatusNotFound, late.Response.StatusCode(), "the upload window has closed")
}

// TestBearerCredentialReadsEveryAuthForm pins the credential forms SDKs send.
func TestBearerCredentialReadsEveryAuthForm(t *testing.T) {
	assert.Equal(t, "bf-tr-1", bearerCredential("Bearer bf-tr-1"))
	assert.Equal(t, "bf-tr-1", bearerCredential("bearer  bf-tr-1"))
	assert.Equal(t, "bf-tr-1", bearerCredential("bf-tr-1"), "Arize sends the bare key")
	assert.Equal(t, "bf-tr-1", bearerCredential("Basic cGstbGYtYW55OmJmLXRyLTE="), "Langfuse sends public:secret, the secret is the key")
	assert.Empty(t, bearerCredential("Basic not-base64!"))
	assert.Empty(t, bearerCredential(""))
}
