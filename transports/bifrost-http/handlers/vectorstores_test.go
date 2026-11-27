package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib/libtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// vectorStoresTestConfig has a config store, its live store, and three Chromem stores: the
// primary "chromem" on disk, "archive" on its own, and "alias", which shares the primary's
// connection.
func vectorStoresTestConfig(t *testing.T) *lib.Config {
	t.Helper()
	SetLogger(&mockLogger{})
	cfg := libtest.Config(t, nil, nil)
	disk := t.TempDir()
	chromemAt := func(name, dir string) vectorstore.Config {
		return vectorstore.Config{Name: name, Enabled: true, Type: vectorstore.VectorStoreTypeChromem, Config: vectorstore.ChromemConfig{Path: filepath.Join(disk, dir)}}
	}
	primary := chromemAt("", "primary")
	registry, err := vectorstore.OpenRegistry(context.Background(), &primary, []vectorstore.Config{chromemAt("archive", "archive"), chromemAt("alias", "primary")}, nil, vectorstore.RegistryOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { registry.Close(context.Background()) })
	cfg.VectorStores = registry
	cfg.VectorStore = registry.Primary().Store
	return cfg
}

func vectorStorePut(t *testing.T, h *VectorStoresHandler, name, body string) *fasthttp.RequestCtx {
	t.Helper()
	ctx := putConfigCtx(body)
	ctx.SetUserValue("name", name)
	h.updateVectorStore(ctx)
	return ctx
}

func decodeVectorStoreView(t *testing.T, ctx *fasthttp.RequestCtx) lib.VectorStoreView {
	t.Helper()
	var view lib.VectorStoreView
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &view), string(ctx.Response.Body()))
	return view
}

// GET lists every configured store, the primary first, with its status, where it keeps its
// data, the store it shares a connection with and its settings; it is editable with a config store.
func TestListVectorStores(t *testing.T) {
	cfg := vectorStoresTestConfig(t)
	h := NewVectorStoresHandler(cfg, cfg)
	require.Equal(t, fasthttp.StatusOK, vectorStorePut(t, h, "archive", `{"retention_days": 7, "max_size_mb": 512}`).Response.StatusCode())

	ctx := &fasthttp.RequestCtx{}
	h.listVectorStores(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	var got vectorStoresResponse
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &got))
	assert.True(t, got.Editable)
	require.Len(t, got.VectorStores, 3)
	primary, archive, alias := got.VectorStores[0], got.VectorStores[1], got.VectorStores[2]
	assert.Equal(t, "chromem", primary.Name)
	assert.True(t, primary.Primary)
	assert.Equal(t, "connected", primary.Status)
	assert.Contains(t, primary.Endpoint, "chromem://")
	assert.Equal(t, 7, archive.RetentionDays)
	assert.Equal(t, 512, archive.MaxSizeMB)
	assert.Equal(t, "chromem", alias.SharedWith)
	assert.Zero(t, alias.RetentionDays)

	require.Equal(t, fasthttp.StatusOK, vectorStorePut(t, h, "chromem", `{"retention_days": 21}`).Response.StatusCode())
	ctx = &fasthttp.RequestCtx{}
	h.listVectorStores(ctx)
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &got))
	assert.Equal(t, 21, got.VectorStores[2].RetentionDays, "a store sharing a connection shows the owner's settings, the ones that apply to it")

	cfg.ConfigStore = nil
	ctx = &fasthttp.RequestCtx{}
	h.listVectorStores(ctx)
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &got))
	assert.False(t, got.Editable, "without a config store the settings come from config.json alone")
}

// PUT changes the fields it is sent: a field left out keeps the stored value, null resets it to
// 0. The save is recorded, the live store follows, and the answer is the store as it now is.
func TestUpdateVectorStoreSettings(t *testing.T) {
	cfg := vectorStoresTestConfig(t)
	h := NewVectorStoresHandler(cfg, cfg)
	live := func() (int, int) {
		settings, ok := cfg.LiveStore.Collections.VectorStoreSettings.Get("archive")
		require.True(t, ok)
		return settings.RetentionDays, settings.MaxSizeMB
	}

	ctx := vectorStorePut(t, h, "archive", `{"retention_days": 7, "max_size_mb": 2048}`)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	view := decodeVectorStoreView(t, ctx)
	assert.Equal(t, "archive", view.Name)
	assert.Equal(t, 7, view.RetentionDays)
	assert.Equal(t, 2048, view.MaxSizeMB)
	changes, _ := ctx.UserValue(configstore.AuditChangesUserValueKey).([]configstore.AuditChange)
	require.Len(t, changes, 1)
	assert.Equal(t, "vector_store_settings.updated", changes[0].EventName)
	assert.Equal(t, "archive", changes[0].TargetID)
	assert.JSONEq(t, `{"name": "archive", "retention_days": 7, "max_size_mb": 2048}`, string(changes[0].After), "the change as reloaded")

	ctx = vectorStorePut(t, h, "archive", `{"retention_days": 30}`)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	retention, size := live()
	assert.Equal(t, 30, retention)
	assert.Equal(t, 2048, size, "a field left out keeps the stored value")

	ctx = vectorStorePut(t, h, "archive", `{"max_size_mb": null}`)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	retention, size = live()
	assert.Equal(t, 30, retention)
	assert.Zero(t, size, "null resets it to 0")
}

// A value out of range, a field that is not a whole number or one that cannot be changed is a
// 400; a name config.json does not configure is a 404; a store that shares another's connection
// is a 409 naming that store. None of them writes anything.
func TestUpdateVectorStoreSettingsRefusals(t *testing.T) {
	cfg := vectorStoresTestConfig(t)
	h := NewVectorStoresHandler(cfg, cfg)
	for name, tc := range map[string]struct {
		store, body string
		status      int
		message     string
	}{
		"negative retention":    {"archive", `{"retention_days": -1}`, fasthttp.StatusBadRequest, "retention_days -1 must be between 0 and"},
		"retention too long":    {"archive", fmt.Sprintf(`{"retention_days": %d}`, configstore.MaxVectorRetentionDays+1), fasthttp.StatusBadRequest, "retention_days"},
		"size too large":        {"archive", fmt.Sprintf(`{"max_size_mb": %d}`, configstore.MaxVectorMaxSizeMB+1), fasthttp.StatusBadRequest, "max_size_mb"},
		"a fraction":            {"archive", `{"max_size_mb": 1.5}`, fasthttp.StatusBadRequest, "max_size_mb must be a whole number"},
		"a field it cannot set": {"archive", `{"type": "redis"}`, fasthttp.StatusBadRequest, `unknown field "type"`},
		"not JSON":              {"archive", `nope`, fasthttp.StatusBadRequest, "invalid request format"},
		"an unknown store":      {"nobody", `{"retention_days": 1}`, fasthttp.StatusNotFound, `vector store "nobody" is not configured`},
		"a shared store":        {"alias", `{"retention_days": 1}`, fasthttp.StatusConflict, `vector store "alias" shares its connection with "chromem"; edit "chromem" instead`},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := vectorStorePut(t, h, tc.store, tc.body)
			assert.Equal(t, tc.status, ctx.Response.StatusCode(), string(ctx.Response.Body()))
			var body struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(ctx.Response.Body(), &body))
			assert.Contains(t, body.Error.Message, tc.message)
			assert.Nil(t, ctx.UserValue(configstore.AuditChangesUserValueKey), "nothing is recorded")
			_, err := cfg.ConfigStore.GetVectorStoreSettings(context.Background(), tc.store)
			assert.ErrorIs(t, err, configstore.ErrNotFound, "nothing is written")
		})
	}
}

// upsertThenFailReadsStore saves vector store settings, then fails every read after it.
type upsertThenFailReadsStore struct {
	configstore.ConfigStore
	readsFail *atomic.Bool
}

func (s upsertThenFailReadsStore) UpdateVectorStoreSettings(ctx context.Context, name string, retentionDays, maxSizeMB *int) (before, after *configtables.TableVectorStoreSettings, err error) {
	before, after, err = s.ConfigStore.UpdateVectorStoreSettings(ctx, name, retentionDays, maxSizeMB)
	if err == nil {
		s.readsFail.Store(true)
	}
	return before, after, err
}

// A save whose re-read fails, after the retries, answers 500 saying it was saved but not
// reloaded; the change is still recorded, since it is stored, and memory keeps the previous settings.
func TestUpdateVectorStoreSettingsRereadFails(t *testing.T) {
	cfg := vectorStoresTestConfig(t)
	h := NewVectorStoresHandler(cfg, cfg)
	require.Equal(t, fasthttp.StatusOK, vectorStorePut(t, h, "archive", `{"retention_days": 7}`).Response.StatusCode())

	readsFail := &atomic.Bool{}
	failReadsWhile(t, cfg.ConfigStore.DB(), readsFail)
	cfg.ConfigStore = upsertThenFailReadsStore{ConfigStore: cfg.ConfigStore, readsFail: readsFail}
	ctx := vectorStorePut(t, h, "archive", `{"retention_days": 30}`)
	readsFail.Store(false)

	assert.Equal(t, fasthttp.StatusInternalServerError, ctx.Response.StatusCode())
	assert.Contains(t, string(ctx.Response.Body()), "Vector store settings updated in database but failed to reload in-memory state")
	changes, _ := ctx.UserValue(configstore.AuditChangesUserValueKey).([]configstore.AuditChange)
	require.Len(t, changes, 1, "the stored change is recorded")
	settings, ok := cfg.LiveStore.Collections.VectorStoreSettings.Get("archive")
	require.True(t, ok)
	assert.Equal(t, 7, settings.RetentionDays, "memory keeps the previous settings")
	stored, err := cfg.ConfigStore.GetVectorStoreSettings(context.Background(), "archive")
	require.NoError(t, err)
	assert.Equal(t, 30, stored.RetentionDays, "the save itself is kept")
}

// interleavingStore lets another request's save land just before a save writes the row, as two
// overlapping requests can, and runs afterWrite once the write has committed.
type interleavingStore struct {
	configstore.ConfigStore
	between    func()
	afterWrite func()
}

func (s *interleavingStore) UpdateVectorStoreSettings(ctx context.Context, name string, retentionDays, maxSizeMB *int) (before, after *configtables.TableVectorStoreSettings, err error) {
	if between := s.between; between != nil {
		s.between = nil
		between()
	}
	before, after, err = s.ConfigStore.UpdateVectorStoreSettings(ctx, name, retentionDays, maxSizeMB)
	if err == nil && s.afterWrite != nil {
		s.afterWrite()
	}
	return before, after, err
}

// Two overlapping saves of different settings both stay: a save writes only the
// setting it was sent, never the other one from its earlier read of the row.
func TestUpdateVectorStoreSettingsOverlappingSavesKeepBoth(t *testing.T) {
	cfg := vectorStoresTestConfig(t)
	h := NewVectorStoresHandler(cfg, cfg)
	require.Equal(t, fasthttp.StatusOK, vectorStorePut(t, h, "archive", `{"retention_days": 7, "max_size_mb": 100}`).Response.StatusCode())

	inner := cfg.ConfigStore
	cfg.ConfigStore = &interleavingStore{ConfigStore: inner, between: func() {
		retention := 30
		_, _, err := inner.UpdateVectorStoreSettings(context.Background(), "archive", &retention, nil)
		require.NoError(t, err)
	}}
	ctx := vectorStorePut(t, h, "archive", `{"max_size_mb": 200}`)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	stored, err := inner.GetVectorStoreSettings(context.Background(), "archive")
	require.NoError(t, err)
	assert.Equal(t, 30, stored.RetentionDays, "the other request's retention stays")
	assert.Equal(t, 200, stored.MaxSizeMB)
	assert.Equal(t, 30, decodeVectorStoreView(t, ctx).RetentionDays, "and the answer shows it")
}

// The change is recorded as the row the write replaced and the row as the write left it, even
// when the reload after it fails: an overlapping save of the other setting that landed first
// shows in both, as it does in the database, rather than this request's earlier picture of it.
func TestUpdateVectorStoreSettingsRecordsTheRowAsWritten(t *testing.T) {
	cfg := vectorStoresTestConfig(t)
	h := NewVectorStoresHandler(cfg, cfg)
	require.Equal(t, fasthttp.StatusOK, vectorStorePut(t, h, "archive", `{"retention_days": 7, "max_size_mb": 100}`).Response.StatusCode())

	inner := cfg.ConfigStore
	readsFail := &atomic.Bool{}
	failReadsWhile(t, inner.DB(), readsFail)
	cfg.ConfigStore = &interleavingStore{
		ConfigStore: inner,
		between: func() {
			retention := 30
			_, _, err := inner.UpdateVectorStoreSettings(context.Background(), "archive", &retention, nil)
			require.NoError(t, err)
		},
		afterWrite: func() { readsFail.Store(true) },
	}
	ctx := vectorStorePut(t, h, "archive", `{"max_size_mb": 200}`)
	readsFail.Store(false)

	require.Equal(t, fasthttp.StatusInternalServerError, ctx.Response.StatusCode(), "the reload fails")
	changes, _ := ctx.UserValue(configstore.AuditChangesUserValueKey).([]configstore.AuditChange)
	require.Len(t, changes, 1)
	assert.JSONEq(t, `{"name": "archive", "retention_days": 30, "max_size_mb": 100}`, string(changes[0].Before), "the row the write replaced")
	assert.JSONEq(t, `{"name": "archive", "retention_days": 30, "max_size_mb": 200}`, string(changes[0].After), "the row as stored")
}

// recordingReloader is the server's reload as a server that embeds it sees it: each name it is
// asked to reload, before it reloads it.
type recordingReloader struct {
	*lib.Config
	names []string
}

func (r *recordingReloader) ReloadVectorStoreSettings(ctx context.Context, name string) error {
	r.names = append(r.names, name)
	return r.Config.ReloadVectorStoreSettings(ctx, name)
}

// A save reloads through the server's callback, which a server that embeds it overrides to have
// its other nodes reload too; every node sweeps its stores under these settings.
func TestUpdateVectorStoreSettingsReloadsThroughTheServer(t *testing.T) {
	cfg := vectorStoresTestConfig(t)
	reloader := &recordingReloader{Config: cfg}
	h := NewVectorStoresHandler(cfg, reloader)
	ctx := vectorStorePut(t, h, "archive", `{"retention_days": 7}`)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.Equal(t, []string{"archive"}, reloader.names)

	refused := vectorStorePut(t, h, "archive", `{"retention_days": -1}`)
	require.Equal(t, fasthttp.StatusBadRequest, refused.Response.StatusCode())
	assert.Equal(t, []string{"archive"}, reloader.names, "a refused save reloads nothing")
}
