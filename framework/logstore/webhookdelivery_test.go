package logstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupWebhookDeliveryTestStore builds a SQLite-backed store; migrations run
// inside newSqliteLogStore, so this also exercises the webhook_deliveries
// table migration.
func setupWebhookDeliveryTestStore(t *testing.T) LogStore {
	t.Helper()
	store, err := newSqliteLogStore(context.Background(), &SQLiteConfig{
		Path: filepath.Join(t.TempDir(), "webhookdeliveries.db"),
	}, testLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(context.Background()) })
	return store
}

func testWebhookDelivery(id, endpointID string, createdAt time.Time) *WebhookDelivery {
	return &WebhookDelivery{
		ID:         id,
		WebhookID:  "wh-" + id,
		EndpointID: endpointID,
		AsyncJobID: "job-1",
		Event:      tables.WebhookEventAsyncJobCompleted,
		AttemptNo:  1,
		Outcome:    WebhookDeliveryOutcomeDelivered,
		StatusCode: 200,
		CreatedAt:  createdAt,
	}
}

func TestWebhookDeliveryCreateAndFind(t *testing.T) {
	store := setupWebhookDeliveryTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	delivery := testWebhookDelivery("d1", "ep1", now)
	delivery.Outcome = WebhookDeliveryOutcomeRetryableFailure
	delivery.StatusCode = 503
	delivery.Error = "upstream unavailable"
	require.NoError(t, store.CreateWebhookDelivery(ctx, delivery))

	fetched, err := store.FindWebhookDeliveryByID(ctx, "d1")
	require.NoError(t, err)
	assert.Equal(t, "wh-d1", fetched.WebhookID)
	assert.Equal(t, "ep1", fetched.EndpointID)
	assert.Equal(t, "job-1", fetched.AsyncJobID)
	assert.Equal(t, tables.WebhookEventAsyncJobCompleted, fetched.Event)
	assert.Equal(t, 1, fetched.AttemptNo)
	assert.Equal(t, WebhookDeliveryOutcomeRetryableFailure, fetched.Outcome)
	assert.Equal(t, 503, fetched.StatusCode)
	assert.Equal(t, "upstream unavailable", fetched.Error)
	assert.Nil(t, fetched.ExpiresAt)

	_, err = store.FindWebhookDeliveryByID(ctx, "missing")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestWebhookDeliverySearchPagination(t *testing.T) {
	store := setupWebhookDeliveryTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	// Three attempts on ep1 at distinct times plus one row on another
	// endpoint that must never appear in ep1 pages.
	for i, id := range []string{"d1", "d2", "d3"} {
		require.NoError(t, store.CreateWebhookDelivery(ctx,
			testWebhookDelivery(id, "ep1", base.Add(time.Duration(i)*time.Second))))
	}
	require.NoError(t, store.CreateWebhookDelivery(ctx, testWebhookDelivery("other", "ep2", base)))

	page, err := store.SearchWebhookDeliveries(ctx, "ep1", PaginationOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(3), page.Pagination.TotalCount)
	require.Len(t, page.Deliveries, 3)
	// Newest first.
	assert.Equal(t, "d3", page.Deliveries[0].ID)
	assert.Equal(t, "d2", page.Deliveries[1].ID)
	assert.Equal(t, "d1", page.Deliveries[2].ID)

	page, err = store.SearchWebhookDeliveries(ctx, "ep1", PaginationOptions{Limit: 1, Offset: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(3), page.Pagination.TotalCount)
	require.Len(t, page.Deliveries, 1)
	assert.Equal(t, "d2", page.Deliveries[0].ID)

	page, err = store.SearchWebhookDeliveries(ctx, "unknown-endpoint", PaginationOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(0), page.Pagination.TotalCount)
	assert.Empty(t, page.Deliveries)
}

// TestWebhookDeliverySearchKeepsRunsWholeAcrossPages verifies that pagination is
// by delivery group (webhook_id), so a multi-attempt run is returned whole on a
// single page and never split across a page boundary.
func TestWebhookDeliverySearchKeepsRunsWholeAcrossPages(t *testing.T) {
	store := setupWebhookDeliveryTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	mk := func(id, webhookID string, attemptNo int, offset time.Duration) *WebhookDelivery {
		return &WebhookDelivery{
			ID: id, WebhookID: webhookID, EndpointID: "ep1", AsyncJobID: "job-1",
			Event: tables.WebhookEventAsyncJobCompleted, AttemptNo: attemptNo,
			Outcome: WebhookDeliveryOutcomeDelivered, StatusCode: 200,
			CreatedAt: base.Add(offset),
		}
	}
	// Three groups; whB has two attempts that must page together.
	require.NoError(t, store.CreateWebhookDelivery(ctx, mk("a1", "whA", 1, 0)))
	require.NoError(t, store.CreateWebhookDelivery(ctx, mk("a2", "whA", 2, time.Second)))
	require.NoError(t, store.CreateWebhookDelivery(ctx, mk("b1", "whB", 1, 2*time.Second)))
	require.NoError(t, store.CreateWebhookDelivery(ctx, mk("b2", "whB", 2, 3*time.Second)))
	require.NoError(t, store.CreateWebhookDelivery(ctx, mk("c1", "whC", 1, 4*time.Second)))

	// Total counts groups (3), not raw attempts (5); the first group is the
	// most recently active.
	first, err := store.SearchWebhookDeliveries(ctx, "ep1", PaginationOptions{Limit: 1, Offset: 0})
	require.NoError(t, err)
	assert.Equal(t, int64(3), first.Pagination.TotalCount)
	require.Len(t, first.Deliveries, 1)
	assert.Equal(t, "c1", first.Deliveries[0].ID)

	// A one-row page still returns whB's entire run — both attempts, newest
	// first — rather than a boundary-split partial run.
	second, err := store.SearchWebhookDeliveries(ctx, "ep1", PaginationOptions{Limit: 1, Offset: 1})
	require.NoError(t, err)
	require.Len(t, second.Deliveries, 2)
	assert.Equal(t, "b2", second.Deliveries[0].ID)
	assert.Equal(t, "b1", second.Deliveries[1].ID)
	for _, d := range second.Deliveries {
		assert.Equal(t, "whB", d.WebhookID)
	}
}

func TestWebhookDeliveryDeleteExpired(t *testing.T) {
	store := setupWebhookDeliveryTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	expired := testWebhookDelivery("expired", "ep1", now.Add(-2*time.Hour))
	expiredAt := now.Add(-time.Hour)
	expired.ExpiresAt = &expiredAt
	require.NoError(t, store.CreateWebhookDelivery(ctx, expired))

	future := testWebhookDelivery("future", "ep1", now)
	futureAt := now.Add(time.Hour)
	future.ExpiresAt = &futureAt
	require.NoError(t, store.CreateWebhookDelivery(ctx, future))

	// A row with no expiry set is never reaped.
	require.NoError(t, store.CreateWebhookDelivery(ctx, testWebhookDelivery("no-expiry", "ep1", now)))

	deleted, err := store.DeleteExpiredWebhookDeliveries(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	_, err = store.FindWebhookDeliveryByID(ctx, "expired")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = store.FindWebhookDeliveryByID(ctx, "future")
	require.NoError(t, err)
	_, err = store.FindWebhookDeliveryByID(ctx, "no-expiry")
	require.NoError(t, err)
}
