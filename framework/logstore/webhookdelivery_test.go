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

	page, err := store.SearchWebhookDeliveries(ctx, &WebhookDeliverySearchFilters{EndpointIDs: []string{"ep1"}}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(3), page.Pagination.TotalCount)
	require.Len(t, page.Deliveries, 3)
	// Newest first.
	assert.Equal(t, "d3", page.Deliveries[0].ID)
	assert.Equal(t, "d2", page.Deliveries[1].ID)
	assert.Equal(t, "d1", page.Deliveries[2].ID)

	page, err = store.SearchWebhookDeliveries(ctx, &WebhookDeliverySearchFilters{EndpointIDs: []string{"ep1"}}, PaginationOptions{Limit: 1, Offset: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(3), page.Pagination.TotalCount)
	require.Len(t, page.Deliveries, 1)
	assert.Equal(t, "d2", page.Deliveries[0].ID)

	page, err = store.SearchWebhookDeliveries(ctx, &WebhookDeliverySearchFilters{EndpointIDs: []string{"unknown-endpoint"}}, PaginationOptions{Limit: 10})
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
	first, err := store.SearchWebhookDeliveries(ctx, &WebhookDeliverySearchFilters{EndpointIDs: []string{"ep1"}}, PaginationOptions{Limit: 1, Offset: 0})
	require.NoError(t, err)
	assert.Equal(t, int64(3), first.Pagination.TotalCount)
	require.Len(t, first.Deliveries, 1)
	assert.Equal(t, "c1", first.Deliveries[0].ID)

	// A one-row page still returns whB's entire run — both attempts, newest
	// first — rather than a boundary-split partial run.
	second, err := store.SearchWebhookDeliveries(ctx, &WebhookDeliverySearchFilters{EndpointIDs: []string{"ep1"}}, PaginationOptions{Limit: 1, Offset: 1})
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

// TestWebhookDeliverySearchFilters covers each filter dimension of the
// dedicated deliveries page, plus the cross-endpoint (no EndpointIDs) view.
func TestWebhookDeliverySearchFilters(t *testing.T) {
	store := setupWebhookDeliveryTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	// ep1: a delivered 200 and a permanently failed 404.
	ok := testWebhookDelivery("ok", "ep1", base)
	ok.RequestID = "req-ok"
	require.NoError(t, store.CreateWebhookDelivery(ctx, ok))

	failed := testWebhookDelivery("failed", "ep1", base.Add(time.Second))
	failed.Outcome = WebhookDeliveryOutcomePermanentFailure
	failed.StatusCode = 404
	failed.Event = tables.WebhookEventAsyncJobFailed
	failed.RequestID = "req-failed"
	require.NoError(t, store.CreateWebhookDelivery(ctx, failed))

	// ep2: a network error, so no response ever arrived.
	noResponse := testWebhookDelivery("noresp", "ep2", base.Add(2*time.Second))
	noResponse.Outcome = WebhookDeliveryOutcomeExhausted
	noResponse.StatusCode = 0
	require.NoError(t, store.CreateWebhookDelivery(ctx, noResponse))

	ids := func(t *testing.T, filters *WebhookDeliverySearchFilters) []string {
		t.Helper()
		page, err := store.SearchWebhookDeliveries(ctx, filters, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		out := make([]string, 0, len(page.Deliveries))
		for _, d := range page.Deliveries {
			out = append(out, d.ID)
		}
		return out
	}

	// No filters at all is the cross-endpoint view: everything, newest first.
	assert.Equal(t, []string{"noresp", "failed", "ok"}, ids(t, &WebhookDeliverySearchFilters{}))
	assert.Equal(t, []string{"noresp", "failed", "ok"}, ids(t, nil))

	assert.Equal(t, []string{"failed"}, ids(t, &WebhookDeliverySearchFilters{
		Outcomes: []string{string(WebhookDeliveryOutcomePermanentFailure)},
	}))
	assert.Equal(t, []string{"failed"}, ids(t, &WebhookDeliverySearchFilters{
		Events: []string{string(tables.WebhookEventAsyncJobFailed)},
	}))
	assert.Equal(t, []string{"ok"}, ids(t, &WebhookDeliverySearchFilters{
		StatusClass: []string{WebhookDeliveryStatusClass2xx},
	}))
	assert.Equal(t, []string{"noresp"}, ids(t, &WebhookDeliverySearchFilters{
		StatusClass: []string{WebhookDeliveryStatusClassNone},
	}))
	// Classes are alternatives, so they OR rather than AND.
	assert.Equal(t, []string{"failed", "ok"}, ids(t, &WebhookDeliverySearchFilters{
		StatusClass: []string{WebhookDeliveryStatusClass2xx, WebhookDeliveryStatusClass4xx},
	}))
	assert.Equal(t, []string{"failed"}, ids(t, &WebhookDeliverySearchFilters{RequestID: "req-failed"}))
	assert.Equal(t, []string{"failed"}, ids(t, &WebhookDeliverySearchFilters{DeliveryID: "wh-failed"}))
	assert.Equal(t, []string{"noresp", "failed"}, ids(t, &WebhookDeliverySearchFilters{
		EndpointIDs: []string{"ep1", "ep2"},
		StartTime:   timePtr(base.Add(time.Second)),
	}))
	assert.Equal(t, []string{"ok"}, ids(t, &WebhookDeliverySearchFilters{EndTime: timePtr(base)}))
	// Dimensions AND against each other.
	assert.Empty(t, ids(t, &WebhookDeliverySearchFilters{
		EndpointIDs: []string{"ep2"},
		Outcomes:    []string{string(WebhookDeliveryOutcomeDelivered)},
	}))
}

// TestWebhookDeliverySearchFilterKeepsWholeRun pins the rule that filters
// select delivery groups, not attempts: a run matched by one of its attempts is
// returned with every attempt intact, so the UI can still render the full
// status-code sequence.
func TestWebhookDeliverySearchFilterKeepsWholeRun(t *testing.T) {
	store := setupWebhookDeliveryTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	// One delivery run: two retryable 503s, then a final 200.
	for i, spec := range []struct {
		id      string
		outcome WebhookDeliveryOutcome
		code    int
	}{
		{"a1", WebhookDeliveryOutcomeRetryableFailure, 503},
		{"a2", WebhookDeliveryOutcomeRetryableFailure, 503},
		{"a3", WebhookDeliveryOutcomeDelivered, 200},
	} {
		attempt := testWebhookDelivery(spec.id, "ep1", base.Add(time.Duration(i)*time.Second))
		attempt.WebhookID = "wh-run"
		attempt.AttemptNo = i + 1
		attempt.Outcome = spec.outcome
		attempt.StatusCode = spec.code
		require.NoError(t, store.CreateWebhookDelivery(ctx, attempt))
	}

	page, err := store.SearchWebhookDeliveries(ctx, &WebhookDeliverySearchFilters{
		Outcomes: []string{string(WebhookDeliveryOutcomeDelivered)},
	}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	// One group matched...
	assert.Equal(t, int64(1), page.Pagination.TotalCount)
	// ...and all three of its attempts came back, not just the delivered one.
	require.Len(t, page.Deliveries, 3)
	assert.Equal(t, []string{"a3", "a2", "a1"}, []string{
		page.Deliveries[0].ID, page.Deliveries[1].ID, page.Deliveries[2].ID,
	})
}

// TestWebhookDeliveryStatusClassBounds pins each status class to its own
// hundred. 2xx and 4xx are bounded ranges, and 5xx has to be one too: a status
// code outside 100-599 is not in any HTTP class, so an open-ended `>= 500` would
// quietly fold it into the server-error bucket.
func TestWebhookDeliveryStatusClassBounds(t *testing.T) {
	store := setupWebhookDeliveryTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second)

	for i, tc := range []struct {
		id     string
		status int
	}{
		{"c200", 200},
		{"c404", 404},
		{"c503", 503},
		{"c600", 600},
	} {
		d := testWebhookDelivery(tc.id, "ep1", base.Add(time.Duration(i)*time.Second))
		d.StatusCode = tc.status
		require.NoError(t, store.CreateWebhookDelivery(ctx, d))
	}

	ids := func(t *testing.T, class string) []string {
		t.Helper()
		page, err := store.SearchWebhookDeliveries(ctx, &WebhookDeliverySearchFilters{
			StatusClass: []string{class},
		}, PaginationOptions{Limit: 50})
		require.NoError(t, err)
		out := make([]string, 0, len(page.Deliveries))
		for _, d := range page.Deliveries {
			out = append(out, d.ID)
		}
		return out
	}

	assert.Equal(t, []string{"c200"}, ids(t, WebhookDeliveryStatusClass2xx))
	assert.Equal(t, []string{"c404"}, ids(t, WebhookDeliveryStatusClass4xx))
	// 600 is not a 5xx code and must not come back with the 503.
	assert.Equal(t, []string{"c503"}, ids(t, WebhookDeliveryStatusClass5xx))
}
