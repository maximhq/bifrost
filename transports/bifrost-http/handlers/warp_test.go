package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

type recordingWarpStore struct {
	row      *tables.TableWarpConfig
	upserted []tables.TableWarpConfig
}

func (s *recordingWarpStore) GetWarpConfig(context.Context) (*tables.TableWarpConfig, error) {
	return s.row, nil
}

func (s *recordingWarpStore) UpsertWarpConfig(_ context.Context, config *tables.TableWarpConfig) error {
	s.upserted = append(s.upserted, *config)
	s.row = config
	return nil
}

func newTestWarpHandler(store *recordingWarpStore) *WarpHandler {
	return &WarpHandler{service: warp.NewService(nil, warp.WithConfigStore(store))}
}

// adminCtx builds a request context that passes the local-admin gate.
func adminCtx(body string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.IsLocalAdminContextKey, true)
	ctx.Request.SetBodyString(body)
	return ctx
}

func TestWarpConfigPutRequiresLocalAdmin(t *testing.T) {
	handler := newTestWarpHandler(&recordingWarpStore{})
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"enabled":true,"provider":"openai","model":"gpt-4o"}`)
	handler.putConfig(ctx)

	require.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
}

func TestWarpConfigPutRejectsMalformedBody(t *testing.T) {
	store := &recordingWarpStore{}
	ctx := adminCtx(`{not json`)
	newTestWarpHandler(store).putConfig(ctx)

	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	require.Empty(t, store.upserted)
}

// Validation failures are the service's ErrInvalidConfig family; the handler
// maps them all to 400 and passes the reason through.
func TestWarpConfigPutMapsValidationTo400(t *testing.T) {
	store := &recordingWarpStore{}
	ctx := adminCtx(`{"enabled":true,"provider":"","model":"gpt-4o"}`)
	newTestWarpHandler(store).putConfig(ctx)

	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	require.Contains(t, string(ctx.Response.Body()), "provider is required")
	require.Empty(t, store.upserted)
}

// A store with no Warp support is a supported deployment: 503, not 500.
func TestWarpConfigWithoutStoreIs503(t *testing.T) {
	handler := &WarpHandler{service: warp.NewService(nil)}
	ctx := &fasthttp.RequestCtx{}
	handler.getConfig(ctx)
	require.Equal(t, fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode())

	ctx = adminCtx(`{"enabled":false}`)
	handler.putConfig(ctx)
	require.Equal(t, fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode())
}

// The wire shape the settings page depends on: a key reference is a plain
// field, and defaults are resolved rather than sent as zero.
func TestWarpConfigGetBodyShape(t *testing.T) {
	handler := newTestWarpHandler(&recordingWarpStore{row: &tables.TableWarpConfig{
		ID: tables.WarpConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o", APIKeyID: "key-abc",
	}})
	ctx := &fasthttp.RequestCtx{}
	handler.getConfig(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	var body map[string]any
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &body))
	require.Equal(t, true, body["configured"])
	require.Equal(t, "key-abc", body["api_key_id"])
	require.Equal(t, float64(schemas.WarpDefaultMaxIterations), body["max_iterations"])
	require.NotContains(t, body, "api_key")
}

// The agent runs after the handler returns and fasthttp has recycled the
// request. A snapshot that drops the query scope silently widens every tool to
// the whole deployment, so the copy is asserted rather than assumed.
func TestWarpSnapshotCarriesQueryScope(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	applied := false
	scope := queryscope.QueryScope(func(db *gorm.DB) *gorm.DB { applied = true; return db })
	ctx.SetUserValue(schemas.BifrostContextKeyQueryScope, scope)
	ctx.SetUserValue(schemas.BifrostContextKeyUserID, "u-1")

	snapshot, cancel, err := snapshotWarpContext(ctx, time.Second)
	require.NoError(t, err)
	defer cancel()
	carried := queryscope.FromContext(snapshot)
	require.NotNil(t, carried)
	carried(nil)
	require.True(t, applied, "the snapshot must carry the request's own scope, not a fresh one")
	require.Equal(t, "u-1", snapshot.Value(schemas.BifrostContextKeyUserID))
}

// A scope that was set on the request but cannot be carried over is the
// dangerous case: queryscope.FromContext reads a missing scope as "no
// restriction", so the agent would run unscoped over every tenant's prompts and
// costs with no error and no log line. That must fail closed.
//
// An absent scope is different and must keep working: the key is set by the
// enterprise wrapper, so an OSS deployment legitimately has none, and failing
// closed there would disable Warp entirely.
func TestWarpSnapshotFailsClosedOnUnusableScope(t *testing.T) {
	t.Run("absent scope is allowed", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		snapshot, cancel, err := snapshotWarpContext(ctx, time.Second)
		require.NoError(t, err, "an OSS deployment has no scope wrapper and must still work")
		defer cancel()
		require.Nil(t, queryscope.FromContext(snapshot))
	})

	t.Run("wrong type fails closed", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue(schemas.BifrostContextKeyQueryScope, "not-a-scope")
		_, cancel, err := snapshotWarpContext(ctx, time.Second)
		if cancel != nil {
			defer cancel()
		}
		require.Error(t, err, "a scope that cannot be carried must not degrade to unrestricted")
	})

	t.Run("nil scope of the right type fails closed", func(t *testing.T) {
		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue(schemas.BifrostContextKeyQueryScope, queryscope.QueryScope(nil))
		_, cancel, err := snapshotWarpContext(ctx, time.Second)
		if cancel != nil {
			defer cancel()
		}
		require.Error(t, err)
	})
}

// The chat route must exist whether or not Warp can currently answer.
//
// RegisterRoutes runs once, at startup. Gating the route on CanChat() meant a
// deployment that enabled logging afterwards - through /api/plugins, which
// reloads plugins without re-registering routes - kept returning 405 for the
// rest of the process's life, with nothing to indicate the feature had become
// available. A registered route that answers 503 carries a machine-readable
// reason the dashboard already branches on, so "present but unusable" and
// "absent" stay distinguishable without making the state permanent.
func TestWarpChatRouteIsRegisteredWithoutALogReader(t *testing.T) {
	handler := newTestWarpHandler(&recordingWarpStore{})
	require.False(t, handler.service.CanChat(), "precondition: no log reader, so Warp cannot answer")

	router := router.New()
	handler.RegisterRoutes(router)

	ctx := adminCtx(`{"messages":[{"role":"user","content":"how much did we spend?"}]}`)
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetRequestURI("/api/warp/chat")
	router.Handler(ctx)

	require.Equal(t, fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode(),
		"the route must answer 503, not 405")

	var body schemas.WarpUnavailableResponse
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &body))
	require.Equal(t, schemas.WarpUnavailableNoLogStore, body.Reason,
		"the dashboard hides the launcher on this reason, so it must be reported accurately")
}

// A heartbeat has to be a complete SSE block, not a bare comment line.
//
// SendEvent terminates every Warp frame with "\n\n", and the browser splitter
// looks for exactly that boundary. A heartbeat ending in a single "\n" has no
// boundary, so the client holds it in its carry buffer until the next real
// event arrives - on an idle turn the keep-alive that exists to prove the
// connection is alive is the one thing the reader cannot see.
func TestWarpHeartbeatIsADelimitedSSEBlock(t *testing.T) {
	reader := lib.NewSSEStreamReader()
	go func() {
		warpHeartbeat(reader)()
		reader.Done()
	}()

	buf := make([]byte, 4096)
	n, err := reader.Read(buf)
	require.NoError(t, err)
	require.Equal(t, ": heartbeat\n\n", string(buf[:n]),
		"the heartbeat must carry its own frame boundary")
}
