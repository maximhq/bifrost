package handlers

import (
	"context"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/warp"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
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
