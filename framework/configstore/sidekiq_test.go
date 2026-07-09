package configstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupSidekiqTestStore extends the base test store with the sidekiq table.
func setupSidekiqTestStore(t *testing.T) *RDBConfigStore {
	store := setupRDBTestStore(t)
	require.NoError(t, store.DB().AutoMigrate(&tables.TableSidekiqJob{}), "migrate sidekiq table")
	return store
}

// setUpdatedAt forces a job's updated_at to a fixed time so staleness can be
// exercised deterministically without sleeping.
func setUpdatedAt(t *testing.T, store *RDBConfigStore, id string, ts time.Time) {
	t.Helper()
	require.NoError(t, store.DB().Model(&tables.TableSidekiqJob{}).
		Where("id = ?", id).Update("updated_at", ts).Error)
}

func getJob(t *testing.T, store *RDBConfigStore, id string) *tables.TableSidekiqJob {
	t.Helper()
	job, err := store.GetSidekiqJob(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, job, "job %s should exist", id)
	return job
}

func TestCreateSidekiqJobValidation(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()

	assert.Error(t, store.CreateSidekiqJob(ctx, nil), "nil job")
	assert.Error(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{Kind: "k"}), "empty id")
	assert.Error(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "x"}), "empty kind")
}

func TestCreateSidekiqJobDefaults(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()

	job := &tables.TableSidekiqJob{ID: "j1", Kind: "sync"}
	require.NoError(t, store.CreateSidekiqJob(ctx, job))

	got := getJob(t, store, "j1")
	assert.Equal(t, tables.SidekiqStatusPending, got.Status, "status defaults to pending")
	assert.Equal(t, "{}", got.Metadata, "metadata defaults to {}")
	assert.Equal(t, 0, got.Attempts)
	assert.False(t, got.CreatedAt.IsZero(), "created_at stamped")
	assert.False(t, got.UpdatedAt.IsZero(), "updated_at stamped")
	assert.Nil(t, got.StartedAt, "started_at nil until claimed")
}

func TestCreateSidekiqJobHonoursExplicitFields(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()

	job := &tables.TableSidekiqJob{ID: "j1", Kind: "sync", Status: tables.SidekiqStatusRunning, Metadata: `{"cursor":5}`}
	require.NoError(t, store.CreateSidekiqJob(ctx, job))

	got := getJob(t, store, "j1")
	assert.Equal(t, tables.SidekiqStatusRunning, got.Status)
	assert.Equal(t, `{"cursor":5}`, got.Metadata)
}

func TestGetSidekiqJobMissingReturnsNil(t *testing.T) {
	store := setupSidekiqTestStore(t)
	job, err := store.GetSidekiqJob(context.Background(), "nope")
	require.NoError(t, err)
	assert.Nil(t, job)
}

func TestClaimSidekiqJobPending(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j1", Kind: "k"}))

	ok, err := store.ClaimSidekiqJob(ctx, "j1", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)
	assert.True(t, ok, "pending job is claimable")

	got := getJob(t, store, "j1")
	assert.Equal(t, tables.SidekiqStatusRunning, got.Status)
	assert.Equal(t, "owner-A", got.RunnerID)
	assert.Equal(t, 1, got.Attempts, "claim increments attempts")
	require.NotNil(t, got.StartedAt, "started_at set on first claim")
}

func TestClaimSidekiqJobFreshRunningRejected(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j1", Kind: "k"}))

	ok, err := store.ClaimSidekiqJob(ctx, "j1", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)
	require.True(t, ok)

	// A second owner cannot claim while the heartbeat is fresh.
	ok2, err := store.ClaimSidekiqJob(ctx, "j1", "owner-B", time.Now().Add(-time.Minute))
	require.NoError(t, err)
	assert.False(t, ok2, "fresh running job is not re-claimable")
	assert.Equal(t, "owner-A", getJob(t, store, "j1").RunnerID, "owner unchanged")
}

func TestClaimSidekiqJobStaleReclaimPreservesStartedAt(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j1", Kind: "k"}))

	ok, err := store.ClaimSidekiqJob(ctx, "j1", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)
	require.True(t, ok)
	firstStart := *getJob(t, store, "j1").StartedAt

	// Age the heartbeat past the stale window.
	setUpdatedAt(t, store, "j1", time.Now().Add(-30*time.Minute))

	ok2, err := store.ClaimSidekiqJob(ctx, "j1", "owner-B", time.Now().Add(-15*time.Minute))
	require.NoError(t, err)
	assert.True(t, ok2, "stale running job is re-claimable")

	got := getJob(t, store, "j1")
	assert.Equal(t, "owner-B", got.RunnerID, "ownership transferred")
	assert.Equal(t, 2, got.Attempts, "re-claim increments attempts")
	require.NotNil(t, got.StartedAt)
	assert.WithinDuration(t, firstStart, *got.StartedAt, time.Millisecond, "started_at preserved across resume")
}

func TestClaimSidekiqJobMissing(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ok, err := store.ClaimSidekiqJob(context.Background(), "ghost", "owner-A", time.Now())
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestHeartbeatSidekiqJob(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j1", Kind: "k"}))
	_, err := store.ClaimSidekiqJob(ctx, "j1", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)

	setUpdatedAt(t, store, "j1", time.Now().Add(-5*time.Minute))
	before := getJob(t, store, "j1").UpdatedAt

	ok, err := store.HeartbeatSidekiqJob(ctx, "j1", "owner-A")
	require.NoError(t, err)
	assert.True(t, ok, "owner heartbeat succeeds")
	assert.True(t, getJob(t, store, "j1").UpdatedAt.After(before), "heartbeat bumps updated_at")

	// Wrong owner cannot heartbeat.
	ok, err = store.HeartbeatSidekiqJob(ctx, "j1", "owner-B")
	require.NoError(t, err)
	assert.False(t, ok, "non-owner heartbeat rejected")
}

func TestHeartbeatSidekiqJobRejectedWhenNotRunning(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j1", Kind: "k"}))
	_, err := store.ClaimSidekiqJob(ctx, "j1", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)
	require.NoError(t, store.CompleteSidekiqJob(ctx, "j1", "owner-A", "{}"))

	ok, err := store.HeartbeatSidekiqJob(ctx, "j1", "owner-A")
	require.NoError(t, err)
	assert.False(t, ok, "heartbeat on a completed job is rejected")
}

func TestUpdateSidekiqJobProgress(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j1", Kind: "k"}))
	_, err := store.ClaimSidekiqJob(ctx, "j1", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)

	require.NoError(t, store.UpdateSidekiqJobProgress(ctx, "j1", "owner-A", `{"cursor":42}`))
	assert.Equal(t, `{"cursor":42}`, getJob(t, store, "j1").Metadata)

	// A stale/non-owner cannot advance progress.
	assert.Error(t, store.UpdateSidekiqJobProgress(ctx, "j1", "owner-B", `{"cursor":99}`))
	assert.Equal(t, `{"cursor":42}`, getJob(t, store, "j1").Metadata, "metadata unchanged by non-owner")
}

func TestCompleteSidekiqJob(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j1", Kind: "k"}))
	_, err := store.ClaimSidekiqJob(ctx, "j1", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)

	require.NoError(t, store.CompleteSidekiqJob(ctx, "j1", "owner-A", `{"done":true}`))
	got := getJob(t, store, "j1")
	assert.Equal(t, tables.SidekiqStatusCompleted, got.Status)
	assert.Equal(t, `{"done":true}`, got.Metadata)
	require.NotNil(t, got.CompletedAt)
}

func TestCompleteSidekiqJobRejectsNonOwner(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j1", Kind: "k"}))
	_, err := store.ClaimSidekiqJob(ctx, "j1", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)

	assert.Error(t, store.CompleteSidekiqJob(ctx, "j1", "owner-B", "{}"), "non-owner cannot complete")
	assert.Equal(t, tables.SidekiqStatusRunning, getJob(t, store, "j1").Status)
}

// TestCompleteSidekiqJobRejectsReapedJob covers the status guard: once the reaper
// has flipped a running job to failed, its former owner must not resurrect it.
func TestCompleteSidekiqJobRejectsReapedJob(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j1", Kind: "k"}))
	_, err := store.ClaimSidekiqJob(ctx, "j1", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)

	// Reaper fails the job (owner_id left intact) while owner-A is still running.
	setUpdatedAt(t, store, "j1", time.Now().Add(-30*time.Minute))
	n, err := store.MarkStaleSidekiqJobsFailed(ctx, time.Now().Add(-15*time.Minute))
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	assert.Error(t, store.CompleteSidekiqJob(ctx, "j1", "owner-A", "{}"),
		"complete must fail once the job is no longer running")
	got := getJob(t, store, "j1")
	assert.Equal(t, tables.SidekiqStatusFailed, got.Status, "reaped failure must not be resurrected")
}

func TestFailSidekiqJob(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j1", Kind: "k"}))
	_, err := store.ClaimSidekiqJob(ctx, "j1", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)

	require.NoError(t, store.FailSidekiqJob(ctx, "j1", "owner-A", `{"cursor":7}`, "boom"))
	got := getJob(t, store, "j1")
	assert.Equal(t, tables.SidekiqStatusFailed, got.Status)
	assert.Equal(t, "boom", got.LastError)
	assert.Equal(t, `{"cursor":7}`, got.Metadata, "checkpoint metadata preserved for resume")
	require.NotNil(t, got.CompletedAt)
}

func TestFailSidekiqJobEmptyMetadataPreservesExisting(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j1", Kind: "k", Metadata: `{"cursor":3}`}))
	_, err := store.ClaimSidekiqJob(ctx, "j1", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)

	require.NoError(t, store.FailSidekiqJob(ctx, "j1", "owner-A", "", "panic"))
	got := getJob(t, store, "j1")
	assert.Equal(t, "panic", got.LastError)
	assert.Equal(t, `{"cursor":3}`, got.Metadata, "empty metadata does not clobber last checkpoint")
}

// TestFailSidekiqJobRejectsReapedJob covers the status guard on the fail path: the
// panic/execute path must not overwrite a last_error the reaper already wrote.
func TestFailSidekiqJobRejectsReapedJob(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j1", Kind: "k"}))
	_, err := store.ClaimSidekiqJob(ctx, "j1", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)

	setUpdatedAt(t, store, "j1", time.Now().Add(-30*time.Minute))
	_, err = store.MarkStaleSidekiqJobsFailed(ctx, time.Now().Add(-15*time.Minute))
	require.NoError(t, err)
	reapedErr := getJob(t, store, "j1").LastError

	assert.Error(t, store.FailSidekiqJob(ctx, "j1", "owner-A", "", "late handler error"))
	assert.Equal(t, reapedErr, getJob(t, store, "j1").LastError, "reaper's last_error preserved")
}

func TestListClaimableSidekiqJobs(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()

	// pending → claimable
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "pending", Kind: "k"}))

	// running + fresh → not claimable
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "fresh", Kind: "k"}))
	_, err := store.ClaimSidekiqJob(ctx, "fresh", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)

	// running + stale → claimable
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "stale", Kind: "k"}))
	_, err = store.ClaimSidekiqJob(ctx, "stale", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)
	setUpdatedAt(t, store, "stale", time.Now().Add(-30*time.Minute))

	// completed → not claimable
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "done", Kind: "k"}))
	_, err = store.ClaimSidekiqJob(ctx, "done", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)
	require.NoError(t, store.CompleteSidekiqJob(ctx, "done", "owner-A", "{}"))

	jobs, err := store.ListClaimableSidekiqJobs(ctx, time.Now().Add(-15*time.Minute))
	require.NoError(t, err)

	ids := map[string]bool{}
	for _, j := range jobs {
		ids[j.ID] = true
	}
	assert.True(t, ids["pending"], "pending is claimable")
	assert.True(t, ids["stale"], "stale running is claimable")
	assert.False(t, ids["fresh"], "fresh running is not claimable")
	assert.False(t, ids["done"], "completed is not claimable")
}

func TestListClaimableSidekiqJobsOrderedOldestFirst(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "a", Kind: "k"}))
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "b", Kind: "k"}))
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "c", Kind: "k"}))
	// Force a deterministic created_at ordering: c, a, b.
	require.NoError(t, store.DB().Model(&tables.TableSidekiqJob{}).Where("id = ?", "c").Update("created_at", time.Now().Add(-3*time.Hour)).Error)
	require.NoError(t, store.DB().Model(&tables.TableSidekiqJob{}).Where("id = ?", "a").Update("created_at", time.Now().Add(-2*time.Hour)).Error)
	require.NoError(t, store.DB().Model(&tables.TableSidekiqJob{}).Where("id = ?", "b").Update("created_at", time.Now().Add(-1*time.Hour)).Error)

	jobs, err := store.ListClaimableSidekiqJobs(ctx, time.Now().Add(-15*time.Minute))
	require.NoError(t, err)
	require.Len(t, jobs, 3)
	assert.Equal(t, []string{"c", "a", "b"}, []string{jobs[0].ID, jobs[1].ID, jobs[2].ID})
}

func TestGetInFlightSidekiqJobByKindNoMatch(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()

	// No jobs at all.
	job, err := store.GetInFlightSidekiqJobByKind(ctx, "sync")
	require.NoError(t, err)
	assert.Nil(t, job, "no jobs of any kind → nil")

	// A job of a different kind must not match.
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "other", Kind: "reindex"}))
	job, err = store.GetInFlightSidekiqJobByKind(ctx, "sync")
	require.NoError(t, err)
	assert.Nil(t, job, "only a different-kind job exists → nil")
}

func TestGetInFlightSidekiqJobByKindReturnsPendingOrRunning(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()

	// Pending job of the kind is in flight.
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "pending", Kind: "sync"}))
	job, err := store.GetInFlightSidekiqJobByKind(ctx, "sync")
	require.NoError(t, err)
	require.NotNil(t, job, "pending job is in flight")
	assert.Equal(t, "pending", job.ID)

	// Claiming it flips it to running — still in flight.
	_, err = store.ClaimSidekiqJob(ctx, "pending", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)
	job, err = store.GetInFlightSidekiqJobByKind(ctx, "sync")
	require.NoError(t, err)
	require.NotNil(t, job, "running job is in flight")
	assert.Equal(t, "pending", job.ID)
	assert.Equal(t, tables.SidekiqStatusRunning, job.Status)
}

func TestGetInFlightSidekiqJobByKindExcludesTerminal(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()

	// completed → not in flight
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "done", Kind: "sync"}))
	_, err := store.ClaimSidekiqJob(ctx, "done", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)
	require.NoError(t, store.CompleteSidekiqJob(ctx, "done", "owner-A", "{}"))

	// failed → not in flight
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "failed", Kind: "sync"}))
	_, err = store.ClaimSidekiqJob(ctx, "failed", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)
	require.NoError(t, store.FailSidekiqJob(ctx, "failed", "owner-A", "{}", "boom"))

	job, err := store.GetInFlightSidekiqJobByKind(ctx, "sync")
	require.NoError(t, err)
	assert.Nil(t, job, "completed and failed jobs are not in flight")
}

func TestGetInFlightSidekiqJobByKindReturnsMostRecent(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "old", Kind: "sync"}))
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "mid", Kind: "sync"}))
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "new", Kind: "sync"}))
	// Force a deterministic created_at ordering: old < mid < new.
	require.NoError(t, store.DB().Model(&tables.TableSidekiqJob{}).Where("id = ?", "old").Update("created_at", time.Now().Add(-3*time.Hour)).Error)
	require.NoError(t, store.DB().Model(&tables.TableSidekiqJob{}).Where("id = ?", "mid").Update("created_at", time.Now().Add(-2*time.Hour)).Error)
	require.NoError(t, store.DB().Model(&tables.TableSidekiqJob{}).Where("id = ?", "new").Update("created_at", time.Now().Add(-1*time.Hour)).Error)

	// A newer job of a different kind must not win.
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "newest-other", Kind: "reindex"}))

	job, err := store.GetInFlightSidekiqJobByKind(ctx, "sync")
	require.NoError(t, err)
	require.NotNil(t, job)
	assert.Equal(t, "new", job.ID, "most-recently-created in-flight job of the kind wins")
}

func TestMarkStaleSidekiqJobsFailed(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()

	// stale running → reaped
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "stale", Kind: "k"}))
	_, err := store.ClaimSidekiqJob(ctx, "stale", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)
	setUpdatedAt(t, store, "stale", time.Now().Add(-30*time.Minute))

	// fresh running → left alone
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "fresh", Kind: "k"}))
	_, err = store.ClaimSidekiqJob(ctx, "fresh", "owner-A", time.Now().Add(-time.Minute))
	require.NoError(t, err)

	// pending → left alone (not running)
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "pending", Kind: "k"}))

	n, err := store.MarkStaleSidekiqJobsFailed(ctx, time.Now().Add(-15*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "only the stale running job is reaped")

	assert.Equal(t, tables.SidekiqStatusFailed, getJob(t, store, "stale").Status)
	assert.NotEmpty(t, getJob(t, store, "stale").LastError)
	assert.Equal(t, tables.SidekiqStatusRunning, getJob(t, store, "fresh").Status)
	assert.Equal(t, tables.SidekiqStatusPending, getJob(t, store, "pending").Status)
}
