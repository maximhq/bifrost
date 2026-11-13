package handlers

import (
	"context"
	"encoding/base64"
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

// Dashboard credentials every handler test authenticates with.
const (
	traceTestUser     = "admin"
	traceTestPassword = "secret"
	// traceTestKey is base64("admin:secret"): the dashboard credentials in the form an SDK's API key field takes.
	traceTestKey = "YWRtaW46c2VjcmV0"
)

// fakeDashboardAuth is a dashboard with auth on and one admin account.
type fakeDashboardAuth struct{ active bool }

func (f *fakeDashboardAuth) IsDashboardAuthActive() bool { return f.active }
func (f *fakeDashboardAuth) VerifyAdminCredentials(username, password string) bool {
	return username == traceTestUser && password == traceTestPassword
}
func (f *fakeDashboardAuth) VerifySessionToken(token string) bool { return token == "session-token" }

type traceIngestTestEnv struct {
	handler *TraceIngestHandler
	router  *router.Router
	store   logstore.LogStore
	service *traceingest.Service
	config  *traceTestConfig
}

// newTraceIngestTestEnv builds the ingest handler over a real SQLite trace store, with one project
// "agent" (id p1) as the default, and dashboard auth on.
func newTraceIngestTestEnv(t *testing.T) *traceIngestTestEnv {
	t.Helper()
	ctx := context.Background()
	store, err := logstore.NewLogStore(ctx, &logstore.Config{Type: logstore.LogStoreTypeSQLite, Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")}}, logger)
	require.NoError(t, err)
	config := &traceTestConfig{projects: map[string]*tables.TableTraceProject{"p1": {ID: "p1", Name: "agent"}}}
	service := traceingest.NewService(store, config, traceingest.Config{DefaultProjectName: "agent"}, logger)
	t.Cleanup(func() {
		service.Close()
		_ = store.Close(ctx)
	})
	handler := NewTraceIngestHandler(service, nil, logger)
	handler.SetCredentials(&fakeDashboardAuth{active: true})
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

// TestIngestAcceptsTheDashboardCredentialsInEveryForm pins ingest auth: SDKs send the dashboard's
// own credentials in whichever form their API key field takes, and with dashboard auth off nothing
// is needed.
func TestIngestAcceptsTheDashboardCredentialsInEveryForm(t *testing.T) {
	env := newTraceIngestTestEnv(t)
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:secret"))
	for name, credential := range map[string]string{
		"basic, as Langfuse sends public and secret key": basic,
		"bearer base64, as the dashboard API takes it":   "Bearer " + traceTestKey,
		"bare base64":              traceTestKey,
		"bare username:password":   "admin:secret",
		"bearer username:password": "Bearer admin:secret",
		"unpadded base64":          "YWRtaW46c2VjcmV0",
		"dashboard session":        "Bearer session-token",
	} {
		principal, err := env.handler.authorize([]string{credential})
		require.NoError(t, err, name)
		assert.True(t, principal.AllProjects, name)
	}
	for name, credential := range map[string]string{
		"wrong password": "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:nope")),
		"wrong user":     "someone:secret",
		"garbage":        "Bearer not-a-credential",
		"unknown scheme": "Digest admin:secret",
		"empty":          "",
	} {
		_, err := env.handler.authorize([]string{credential})
		assert.ErrorIs(t, err, traceingest.ErrUnauthorized, name)
	}
	_, err := env.handler.authorize(nil)
	assert.ErrorIs(t, err, traceingest.ErrUnauthorized)
	_, err = env.handler.authorize([]string{"junk", traceTestKey})
	assert.NoError(t, err, "any one valid credential is enough")

	env.handler.SetCredentials(&fakeDashboardAuth{active: false})
	_, err = env.handler.authorize(nil)
	assert.NoError(t, err, "with dashboard auth off, no credential is needed")
	env.handler.SetCredentials(nil)
	_, err = env.handler.authorize(nil)
	assert.NoError(t, err)
}
