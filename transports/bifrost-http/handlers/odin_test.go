package handlers

import (
	"context"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type recordingOdinStore struct {
	row      *tables.TableOdinConfig
	upserted []tables.TableOdinConfig
}

func (s *recordingOdinStore) GetOdinConfig(context.Context) (*tables.TableOdinConfig, error) {
	return s.row, nil
}

func (s *recordingOdinStore) UpsertOdinConfig(_ context.Context, config *tables.TableOdinConfig) error {
	s.upserted = append(s.upserted, *config)
	s.row = config
	return nil
}

// adminCtx builds a request context that passes the local-admin gate.
func adminCtx(body string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.IsLocalAdminContextKey, true)
	ctx.Request.SetBodyString(body)
	return ctx
}

func TestOdinConfigPutRequiresLocalAdmin(t *testing.T) {
	service := &OdinService{store: &recordingOdinStore{}}
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"enabled":true,"provider":"openai","model":"gpt-4o"}`)
	service.putConfig(ctx)

	require.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
}

// A key reference round-trips like any other field: there is no secret here, so
// no redaction step and no presence flag.
func TestOdinConfigGetReturnsKeyReference(t *testing.T) {
	service := &OdinService{store: &recordingOdinStore{row: &tables.TableOdinConfig{
		ID: tables.OdinConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o", APIKeyID: "key-abc",
	}}}
	ctx := &fasthttp.RequestCtx{}
	service.getConfig(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	var response odinConfigResponse
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &response))
	require.Equal(t, "key-abc", response.APIKeyID)
	require.True(t, response.Configured)
	require.Equal(t, "gpt-4o", response.Model)
}

// An unconfigured deployment must render its empty settings form, so this is a
// 200 with configured:false rather than a 404.
func TestOdinConfigGetUnconfiguredReturnsDefaults(t *testing.T) {
	service := &OdinService{store: &recordingOdinStore{}}
	ctx := &fasthttp.RequestCtx{}
	service.getConfig(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	var response odinConfigResponse
	require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &response))
	require.False(t, response.Configured)
	require.Empty(t, response.APIKeyID)
	require.Equal(t, schemas.OdinDefaultMaxIterations, response.MaxIterations)
}

// The reference is a plain field, so clearing it is just sending an empty
// value - none of the omitted-versus-empty ambiguity a write-only secret forces.
func TestOdinConfigPutRoundTripsKeyReference(t *testing.T) {
	store := &recordingOdinStore{row: &tables.TableOdinConfig{
		ID: tables.OdinConfigRowID, Enabled: true, Provider: "openai", Model: "gpt-4o", APIKeyID: "key-abc",
	}}
	service := &OdinService{store: store}
	ctx := adminCtx(`{"enabled":true,"provider":"openai","model":"gpt-4o-mini","api_key_id":"key-xyz"}`)
	service.putConfig(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.Len(t, store.upserted, 1)
	require.Equal(t, "key-xyz", store.upserted[0].APIKeyID)
	require.Equal(t, "gpt-4o-mini", store.upserted[0].Model)
}

// A provider on a trusted network, or one using ambient credentials, needs no
// key at all - so an empty reference must be accepted, not rejected.
func TestOdinConfigPutAcceptsEmptyKeyReference(t *testing.T) {
	store := &recordingOdinStore{}
	service := &OdinService{store: store}
	ctx := adminCtx(`{"enabled":true,"provider":"openai","model":"gpt-4o"}`)
	service.putConfig(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.Len(t, store.upserted, 1)
	require.Empty(t, store.upserted[0].APIKeyID)
}

// A half-filled draft with the toggle off is legitimate: an operator must be
// able to fill the form in over more than one sitting.
func TestOdinConfigPutAllowsIncompleteDraftWhenDisabled(t *testing.T) {
	store := &recordingOdinStore{}
	service := &OdinService{store: store}
	ctx := adminCtx(`{"enabled":false,"provider":"","model":""}`)
	service.putConfig(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.Len(t, store.upserted, 1)
}

func TestOdinConfigPutRejectsIncompleteWhenEnabled(t *testing.T) {
	for _, body := range []string{
		`{"enabled":true,"provider":"","model":"gpt-4o"}`,
		`{"enabled":true,"provider":"openai","model":""}`,
	} {
		store := &recordingOdinStore{}
		service := &OdinService{store: store}
		ctx := adminCtx(body)
		service.putConfig(ctx)

		require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), body)
		require.Empty(t, store.upserted, body)
	}
}

func TestOdinConfigPutRejectsIterationsAboveCeiling(t *testing.T) {
	store := &recordingOdinStore{}
	service := &OdinService{store: store}
	ctx := adminCtx(`{"enabled":true,"provider":"openai","model":"gpt-4o","max_iterations":50}`)
	service.putConfig(ctx)

	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	require.Empty(t, store.upserted)
}

// GetConfig is what the chat path calls. It must refuse a disabled or
// incomplete config rather than handing back something half-usable.
func TestOdinGetConfigRejectsUnusableConfigs(t *testing.T) {
	for name, row := range map[string]*tables.TableOdinConfig{
		"missing":  nil,
		"disabled": {Enabled: false, Provider: "openai", Model: "gpt-4o"},
		"no model": {Enabled: true, Provider: "openai"},
	} {
		service := &OdinService{store: &recordingOdinStore{row: row}}
		_, err := service.GetConfig(context.Background())
		require.ErrorIs(t, err, ErrOdinUnavailable, name)
	}
}
