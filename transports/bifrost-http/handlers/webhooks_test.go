package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/webhooks"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// configWebhookManager adapts a *lib.Config to WebhookEndpointManager,
// mirroring the base server's per-endpoint reload/remove so handler tests
// observe the same in-memory refresh the real wiring produces.
type configWebhookManager struct{ config *lib.Config }

func (m configWebhookManager) ReloadWebhookEndpoint(ctx context.Context, id string) error {
	endpoint, err := m.config.ConfigStore.GetWebhookEndpointByID(ctx, id)
	if err != nil {
		return err
	}
	m.config.SetWebhookEndpoint(endpoint)
	return nil
}

func (m configWebhookManager) RemoveWebhookEndpoint(ctx context.Context, id string) error {
	m.config.RemoveWebhookEndpoint(id)
	return nil
}

func newWebhookTestHandler(t *testing.T) (*WebhookHandler, *lib.Config) {
	t.Helper()
	ctx := context.Background()

	store, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { store.Close(context.Background()) })

	logsStore, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeSQLite,
		Config:  &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")},
	}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { logsStore.Close(context.Background()) })

	config := &lib.Config{ConfigStore: store, LogsStore: logsStore}
	dispatcher := webhooks.NewDispatcher(ctx, "", 30*24*time.Hour, store, logsStore, config, testLogger{})
	t.Cleanup(dispatcher.Stop)
	return NewWebhookHandler(configWebhookManager{config}, config, dispatcher), config
}

func newWebhookRequestCtx(body string, pathParams map[string]string) *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.SetBodyString(body)
	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)
	for key, value := range pathParams {
		ctx.SetUserValue(key, value)
	}
	return ctx
}

func decodeJSONResponse(t *testing.T, ctx *fasthttp.RequestCtx) map[string]any {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &payload), "body: %s", ctx.Response.Body())
	return payload
}

func createTestWebhookEndpoint(t *testing.T, handler *WebhookHandler, name string) (id, secret string) {
	t.Helper()
	body := fmt.Sprintf(`{"name":%q,"url":"https://93.184.216.34/hook","events":["async_job.completed","async_job.failed"]}`, name)
	ctx := newWebhookRequestCtx(body, nil)
	handler.createWebhookEndpoint(ctx)
	require.Equal(t, fasthttp.StatusCreated, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
	payload := decodeJSONResponse(t, ctx)
	endpoint := payload["endpoint"].(map[string]any)
	return endpoint["id"].(string), payload["secret"].(string)
}

func TestWebhookHandlerRouteRegistration(t *testing.T) {
	handler, _ := newWebhookTestHandler(t)
	r := router.New()
	// Panics on route conflicts, which is the failure this guards against.
	handler.RegisterRoutes(r)
}

func TestWebhookHandlerCreateAndGet(t *testing.T) {
	handler, config := newWebhookTestHandler(t)

	id, secret := createTestWebhookEndpoint(t, handler, "create-test")
	assert.Contains(t, secret, "whsec_")

	// The secret is shown exactly once: reads never include it.
	getCtx := newWebhookRequestCtx("", map[string]string{"id": id})
	handler.getWebhookEndpoint(getCtx)
	require.Equal(t, fasthttp.StatusOK, getCtx.Response.StatusCode())
	assert.NotContains(t, string(getCtx.Response.Body()), "whsec_")
	assert.NotContains(t, string(getCtx.Response.Body()), secret)

	// The in-memory store serves the endpoint, with its secret, immediately.
	inMemory, ok := config.WebhookEndpointByID(id)
	require.True(t, ok)
	assert.Equal(t, "create-test", inMemory.Name)
	assert.Equal(t, secret, inMemory.Secret.GetValue())
	byName, ok := config.WebhookEndpointByName("create-test")
	require.True(t, ok)
	assert.Equal(t, id, byName.ID)

	listCtx := newWebhookRequestCtx("", nil)
	handler.listWebhookEndpoints(listCtx)
	require.Equal(t, fasthttp.StatusOK, listCtx.Response.StatusCode())
	assert.Equal(t, float64(1), decodeJSONResponse(t, listCtx)["count"])

	missingCtx := newWebhookRequestCtx("", map[string]string{"id": "does-not-exist"})
	handler.getWebhookEndpoint(missingCtx)
	assert.Equal(t, fasthttp.StatusNotFound, missingCtx.Response.StatusCode())
}

func TestWebhookHandlerListFilters(t *testing.T) {
	handler, _ := newWebhookTestHandler(t)

	create := func(body string) {
		t.Helper()
		ctx := newWebhookRequestCtx(body, nil)
		handler.createWebhookEndpoint(ctx)
		require.Equal(t, fasthttp.StatusCreated, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
	}
	create(`{"name":"billing","url":"https://93.184.216.34/billing","events":["async_job.completed"]}`)
	create(`{"name":"alerts","url":"https://93.184.216.34/alerts","events":["async_job.failed"]}`)
	create(`{"name":"paused","url":"https://93.184.216.34/paused","events":["async_job.completed"],"disabled":true}`)

	list := func(query map[string]string) map[string]any {
		t.Helper()
		ctx := newWebhookRequestCtx("", nil)
		for key, value := range query {
			ctx.QueryArgs().Set(key, value)
		}
		handler.listWebhookEndpoints(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "body: %s", ctx.Response.Body())
		return decodeJSONResponse(t, ctx)
	}

	// Unfiltered: full count plus the pagination envelope.
	page := list(nil)
	assert.Equal(t, float64(3), page["total_count"])
	assert.Equal(t, float64(3), page["count"])
	assert.Equal(t, float64(25), page["limit"])

	// Paging keeps the full match count.
	page = list(map[string]string{"limit": "1", "offset": "1"})
	assert.Equal(t, float64(3), page["total_count"])
	assert.Equal(t, float64(1), page["count"])

	page = list(map[string]string{"search": "ALERTS"})
	assert.Equal(t, float64(1), page["total_count"])

	page = list(map[string]string{"disabled": "true"})
	assert.Equal(t, float64(1), page["total_count"])

	page = list(map[string]string{"event": "async_job.completed"})
	assert.Equal(t, float64(2), page["total_count"])

	// Invalid parameters are rejected.
	for name, query := range map[string]map[string]string{
		"bad disabled": {"disabled": "maybe"},
		"bad event":    {"event": "bogus.event"},
		"bad limit":    {"limit": "not-a-number"},
		"bad offset":   {"offset": "-1"},
	} {
		ctx := newWebhookRequestCtx("", nil)
		for key, value := range query {
			ctx.QueryArgs().Set(key, value)
		}
		handler.listWebhookEndpoints(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), name)
	}
}

func TestWebhookHandlerCreateValidation(t *testing.T) {
	handler, _ := newWebhookTestHandler(t)

	cases := map[string]string{
		"missing name":     `{"url":"https://93.184.216.34/hook","events":["async_job.completed"]}`,
		"http without opt": `{"name":"a","url":"http://93.184.216.34/hook","events":["async_job.completed"]}`,
		"no events":        `{"name":"a","url":"https://93.184.216.34/hook","events":[]}`,
		"unknown event":    `{"name":"a","url":"https://93.184.216.34/hook","events":["bogus.event"]}`,
		"invalid body":     `{not json`,
	}
	for name, body := range cases {
		ctx := newWebhookRequestCtx(body, nil)
		handler.createWebhookEndpoint(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), name)
	}
}

func TestWebhookHandlerDuplicateName(t *testing.T) {
	handler, _ := newWebhookTestHandler(t)
	createTestWebhookEndpoint(t, handler, "dup-name")

	ctx := newWebhookRequestCtx(`{"name":"dup-name","url":"https://93.184.216.34/hook","events":["async_job.completed"]}`, nil)
	handler.createWebhookEndpoint(ctx)
	assert.Equal(t, fasthttp.StatusConflict, ctx.Response.StatusCode())
}

func TestWebhookHandlerUpdate(t *testing.T) {
	handler, config := newWebhookTestHandler(t)
	id, _ := createTestWebhookEndpoint(t, handler, "update-test")

	updateCtx := newWebhookRequestCtx(`{"name":"renamed","url":"https://93.184.216.34/hook2","events":["async_job.completed"],"disabled":true}`, map[string]string{"id": id})
	handler.updateWebhookEndpoint(updateCtx)
	require.Equal(t, fasthttp.StatusOK, updateCtx.Response.StatusCode(), "body: %s", updateCtx.Response.Body())

	// Memory follows the database, including the name index.
	_, ok := config.WebhookEndpointByName("update-test")
	assert.False(t, ok, "old name must leave the in-memory index")
	renamed, ok := config.WebhookEndpointByName("renamed")
	require.True(t, ok)
	assert.Equal(t, "https://93.184.216.34/hook2", renamed.URL)
	assert.True(t, renamed.Disabled)

	missingCtx := newWebhookRequestCtx(`{"name":"x","url":"https://93.184.216.34/hook","events":["async_job.completed"]}`, map[string]string{"id": "does-not-exist"})
	handler.updateWebhookEndpoint(missingCtx)
	assert.Equal(t, fasthttp.StatusNotFound, missingCtx.Response.StatusCode())
}

func TestWebhookHandlerDelete(t *testing.T) {
	handler, config := newWebhookTestHandler(t)
	id, _ := createTestWebhookEndpoint(t, handler, "delete-test")

	deleteCtx := newWebhookRequestCtx("", map[string]string{"id": id})
	handler.deleteWebhookEndpoint(deleteCtx)
	require.Equal(t, fasthttp.StatusOK, deleteCtx.Response.StatusCode())

	_, ok := config.WebhookEndpointByID(id)
	assert.False(t, ok, "deleted endpoints must leave the in-memory store")

	againCtx := newWebhookRequestCtx("", map[string]string{"id": id})
	handler.deleteWebhookEndpoint(againCtx)
	assert.Equal(t, fasthttp.StatusNotFound, againCtx.Response.StatusCode())
}

func TestWebhookHandlerRotateSecret(t *testing.T) {
	handler, config := newWebhookTestHandler(t)
	id, originalSecret := createTestWebhookEndpoint(t, handler, "rotate-test")

	rotateCtx := newWebhookRequestCtx("", map[string]string{"id": id})
	handler.rotateWebhookEndpointSecret(rotateCtx)
	require.Equal(t, fasthttp.StatusOK, rotateCtx.Response.StatusCode())
	newSecret := decodeJSONResponse(t, rotateCtx)["secret"].(string)
	assert.Contains(t, newSecret, "whsec_")
	assert.NotEqual(t, originalSecret, newSecret)

	// Deliveries sign with the new secret from this moment on.
	inMemory, ok := config.WebhookEndpointByID(id)
	require.True(t, ok)
	assert.Equal(t, newSecret, inMemory.Secret.GetValue())

	missingCtx := newWebhookRequestCtx("", map[string]string{"id": "does-not-exist"})
	handler.rotateWebhookEndpointSecret(missingCtx)
	assert.Equal(t, fasthttp.StatusNotFound, missingCtx.Response.StatusCode())
}

func seedWebhookDelivery(t *testing.T, config *lib.Config, id, webhookID, endpointID string, createdAt time.Time) {
	t.Helper()
	require.NoError(t, config.LogsStore.CreateWebhookDelivery(context.Background(), &logstore.WebhookDelivery{
		ID: id, WebhookID: webhookID, EndpointID: endpointID, AsyncJobID: "job-1",
		Event: configstoreTables.WebhookEventAsyncJobCompleted, AttemptNo: 1,
		Outcome: logstore.WebhookDeliveryOutcomeExhausted, StatusCode: 503,
		CreatedAt: createdAt,
	}))
}

func TestWebhookHandlerListDeliveries(t *testing.T) {
	handler, config := newWebhookTestHandler(t)
	id, _ := createTestWebhookEndpoint(t, handler, "deliveries-test")

	now := time.Now().UTC().Truncate(time.Second)
	seedWebhookDelivery(t, config, "d1", "wh1", id, now.Add(-time.Minute))
	seedWebhookDelivery(t, config, "d2", "wh2", id, now)

	listCtx := newWebhookRequestCtx("", map[string]string{"id": id})
	listCtx.QueryArgs().Set("limit", "1")
	handler.listWebhookDeliveries(listCtx)
	require.Equal(t, fasthttp.StatusOK, listCtx.Response.StatusCode())
	payload := decodeJSONResponse(t, listCtx)
	deliveries := payload["deliveries"].([]any)
	require.Len(t, deliveries, 1)
	assert.Equal(t, "d2", deliveries[0].(map[string]any)["id"], "newest first")
	assert.Equal(t, float64(2), payload["pagination"].(map[string]any)["total_count"])

	badCtx := newWebhookRequestCtx("", map[string]string{"id": id})
	badCtx.QueryArgs().Set("limit", "not-a-number")
	handler.listWebhookDeliveries(badCtx)
	assert.Equal(t, fasthttp.StatusBadRequest, badCtx.Response.StatusCode())
}

func TestWebhookHandlerRedeliver(t *testing.T) {
	handler, config := newWebhookTestHandler(t)
	id, _ := createTestWebhookEndpoint(t, handler, "redeliver-test")
	seedWebhookDelivery(t, config, "d1", "wh1", id, time.Now().UTC())

	redeliverCtx := newWebhookRequestCtx("", map[string]string{"id": "d1"})
	handler.redeliverWebhook(redeliverCtx)
	require.Equal(t, fasthttp.StatusAccepted, redeliverCtx.Response.StatusCode(), "body: %s", redeliverCtx.Response.Body())
	assert.Equal(t, "wh1", decodeJSONResponse(t, redeliverCtx)["webhook_id"])

	// The replay reuses the original webhook id, so the queue row exists
	// under it and a second redelivery conflicts while it is in flight.
	due, err := config.ConfigStore.ListDueWebhookJobs(context.Background(), 0)
	require.NoError(t, err)
	require.Len(t, due, 1)
	assert.Equal(t, "wh1", due[0].ID)

	conflictCtx := newWebhookRequestCtx("", map[string]string{"id": "d1"})
	handler.redeliverWebhook(conflictCtx)
	assert.Equal(t, fasthttp.StatusConflict, conflictCtx.Response.StatusCode())

	missingCtx := newWebhookRequestCtx("", map[string]string{"id": "unknown-delivery"})
	handler.redeliverWebhook(missingCtx)
	assert.Equal(t, fasthttp.StatusNotFound, missingCtx.Response.StatusCode())

	// History whose endpoint has since been deleted cannot be redelivered.
	seedWebhookDelivery(t, config, "d-orphan", "wh-orphan", "ep-gone", time.Now().UTC())
	orphanCtx := newWebhookRequestCtx("", map[string]string{"id": "d-orphan"})
	handler.redeliverWebhook(orphanCtx)
	assert.Equal(t, fasthttp.StatusBadRequest, orphanCtx.Response.StatusCode())
}

func TestWebhookHandlerTestDelivery(t *testing.T) {
	handler, config := newWebhookTestHandler(t)

	var received http.Header
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()

	// Loopback receivers require the private-network opt-in.
	body := fmt.Sprintf(`{"name":"test-fire","url":%q,"events":["async_job.completed"],"allow_private_network":true}`, receiver.URL)
	createCtx := newWebhookRequestCtx(body, nil)
	handler.createWebhookEndpoint(createCtx)
	require.Equal(t, fasthttp.StatusCreated, createCtx.Response.StatusCode(), "body: %s", createCtx.Response.Body())
	id := decodeJSONResponse(t, createCtx)["endpoint"].(map[string]any)["id"].(string)

	testCtx := newWebhookRequestCtx("", map[string]string{"id": id})
	handler.testWebhookEndpoint(testCtx)
	require.Equal(t, fasthttp.StatusOK, testCtx.Response.StatusCode())
	payload := decodeJSONResponse(t, testCtx)
	assert.Equal(t, true, payload["delivered"])
	assert.Equal(t, float64(http.StatusNoContent), payload["receiver_status_code"])
	assert.NotEmpty(t, received.Get("webhook-signature"), "test deliveries go through the signing path")

	// Test fires are not rate limited, so an immediate second fire is delivered
	// just like the first.
	repeatCtx := newWebhookRequestCtx("", map[string]string{"id": id})
	handler.testWebhookEndpoint(repeatCtx)
	require.Equal(t, fasthttp.StatusOK, repeatCtx.Response.StatusCode(), "body: %s", repeatCtx.Response.Body())
	assert.Equal(t, true, decodeJSONResponse(t, repeatCtx)["delivered"])

	missingCtx := newWebhookRequestCtx("", map[string]string{"id": "does-not-exist"})
	handler.testWebhookEndpoint(missingCtx)
	assert.Equal(t, fasthttp.StatusNotFound, missingCtx.Response.StatusCode())

	// A disabled endpoint cannot be test-fired.
	disabledBody := fmt.Sprintf(`{"name":"test-fire-disabled","url":%q,"events":["async_job.completed"],"allow_private_network":true,"disabled":true}`, receiver.URL)
	disabledCreateCtx := newWebhookRequestCtx(disabledBody, nil)
	handler.createWebhookEndpoint(disabledCreateCtx)
	require.Equal(t, fasthttp.StatusCreated, disabledCreateCtx.Response.StatusCode())
	disabledID := decodeJSONResponse(t, disabledCreateCtx)["endpoint"].(map[string]any)["id"].(string)
	disabledCtx := newWebhookRequestCtx("", map[string]string{"id": disabledID})
	handler.testWebhookEndpoint(disabledCtx)
	assert.Equal(t, fasthttp.StatusBadRequest, disabledCtx.Response.StatusCode())

	_ = config
}

func TestWebhookHandlerStoreUnavailable(t *testing.T) {
	cfg := &lib.Config{}
	handler := NewWebhookHandler(configWebhookManager{cfg}, cfg, nil)
	ctx := newWebhookRequestCtx("", nil)
	handler.listWebhookEndpoints(ctx)
	assert.Equal(t, fasthttp.StatusServiceUnavailable, ctx.Response.StatusCode())
}

func TestWebhookHandlerPerEndpointTuning(t *testing.T) {
	handler, config := newWebhookTestHandler(t)

	body := `{"name":"tuned","url":"https://93.184.216.34/hook","events":["async_job.completed"],"max_retries":2,"attempt_timeout_seconds":5,"max_concurrent_deliveries":3}`
	createCtx := newWebhookRequestCtx(body, nil)
	handler.createWebhookEndpoint(createCtx)
	require.Equal(t, fasthttp.StatusCreated, createCtx.Response.StatusCode(), "body: %s", createCtx.Response.Body())
	endpoint := decodeJSONResponse(t, createCtx)["endpoint"].(map[string]any)
	assert.Equal(t, float64(2), endpoint["max_retries"])
	assert.Equal(t, float64(5), endpoint["attempt_timeout_seconds"])

	// The in-memory store carries the knobs for the delivery worker.
	inMemory, ok := config.WebhookEndpointByName("tuned")
	require.True(t, ok)
	assert.Equal(t, 2, inMemory.MaxRetries)
	assert.Equal(t, 5, inMemory.AttemptTimeoutSeconds)
	assert.Equal(t, 3, inMemory.MaxConcurrentDeliveries)

	// Negative knobs are rejected.
	badCtx := newWebhookRequestCtx(`{"name":"bad","url":"https://93.184.216.34/hook","events":["async_job.completed"],"max_retries":-1}`, nil)
	handler.createWebhookEndpoint(badCtx)
	assert.Equal(t, fasthttp.StatusBadRequest, badCtx.Response.StatusCode())
}

func TestWebhookHandlerHeaders(t *testing.T) {
	handler, config := newWebhookTestHandler(t)

	body := `{"name":"headers-test","url":"https://93.184.216.34/hook","events":["async_job.completed"],"headers":{"Authorization":"Bearer receiver-token"}}`
	createCtx := newWebhookRequestCtx(body, nil)
	handler.createWebhookEndpoint(createCtx)
	require.Equal(t, fasthttp.StatusCreated, createCtx.Response.StatusCode(), "body: %s", createCtx.Response.Body())
	id := decodeJSONResponse(t, createCtx)["endpoint"].(map[string]any)["id"].(string)
	assert.NotContains(t, string(createCtx.Response.Body()), "receiver-token", "header values are never echoed")

	// Reads mask header values but keep the names visible.
	getCtx := newWebhookRequestCtx("", map[string]string{"id": id})
	handler.getWebhookEndpoint(getCtx)
	require.Equal(t, fasthttp.StatusOK, getCtx.Response.StatusCode())
	assert.Contains(t, string(getCtx.Response.Body()), "Authorization")
	assert.NotContains(t, string(getCtx.Response.Body()), "receiver-token")

	// The in-memory store (what the delivery worker reads) keeps real values.
	inMemory, ok := config.WebhookEndpointByID(id)
	require.True(t, ok)
	authHeader := inMemory.Headers["Authorization"]
	assert.Equal(t, "Bearer receiver-token", authHeader.GetValue())

	// A masked value round-tripped through an update must not clobber the
	// stored value.
	updateBody := `{"name":"headers-test","url":"https://93.184.216.34/hook","events":["async_job.completed"],"headers":{"Authorization":"<REDACTED>"}}`
	updateCtx := newWebhookRequestCtx(updateBody, map[string]string{"id": id})
	handler.updateWebhookEndpoint(updateCtx)
	require.Equal(t, fasthttp.StatusOK, updateCtx.Response.StatusCode(), "body: %s", updateCtx.Response.Body())
	inMemory, ok = config.WebhookEndpointByID(id)
	require.True(t, ok)
	authHeader = inMemory.Headers["Authorization"]
	assert.Equal(t, "Bearer receiver-token", authHeader.GetValue(), "masked placeholders must restore the stored value")

	// Reserved header names are rejected.
	reservedCtx := newWebhookRequestCtx(`{"name":"h2","url":"https://93.184.216.34/hook","events":["async_job.completed"],"headers":{"webhook-signature":"x"}}`, nil)
	handler.createWebhookEndpoint(reservedCtx)
	assert.Equal(t, fasthttp.StatusBadRequest, reservedCtx.Response.StatusCode())
}
