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

// setCreatedAt forces a job's created_at so FIFO ordering can be exercised
// deterministically without relying on insert-time clock resolution.
func setCreatedAt(t *testing.T, store *RDBConfigStore, id string, ts time.Time) {
	t.Helper()
	require.NoError(t, store.DB().Model(&tables.TableSidekiqJob{}).
		Where("id = ?", id).Update("created_at", ts).Error)
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

func TestCancelSidekiqJob(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()

	t.Run("pending job is cancelled before it ever runs", func(t *testing.T) {
		require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "p1", Kind: "k"}))

		cancelled, err := store.CancelSidekiqJob(ctx, "p1")
		require.NoError(t, err)
		assert.True(t, cancelled)

		got := getJob(t, store, "p1")
		assert.Equal(t, tables.SidekiqStatusCancelled, got.Status)
		require.NotNil(t, got.CompletedAt)
	})

	t.Run("running job is cancelled regardless of owner", func(t *testing.T) {
		require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "r1", Kind: "k"}))
		_, err := store.ClaimSidekiqJob(ctx, "r1", "owner-A", time.Now().Add(-time.Minute))
		require.NoError(t, err)

		// The cancel arrives on a node that does not own the job — it must still apply.
		cancelled, err := store.CancelSidekiqJob(ctx, "r1")
		require.NoError(t, err)
		assert.True(t, cancelled)
		assert.Equal(t, tables.SidekiqStatusCancelled, getJob(t, store, "r1").Status)

		// The owner learns of it through its heartbeat, which is fenced on running.
		alive, err := store.HeartbeatSidekiqJob(ctx, "r1", "owner-A")
		require.NoError(t, err)
		assert.False(t, alive, "heartbeat must report lost ownership so the owner stops")
	})

	t.Run("cancelled job is neither claimable nor reapable", func(t *testing.T) {
		require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "c1", Kind: "k"}))
		_, err := store.ClaimSidekiqJob(ctx, "c1", "owner-A", time.Now().Add(-time.Minute))
		require.NoError(t, err)
		_, err = store.CancelSidekiqJob(ctx, "c1")
		require.NoError(t, err)
		setUpdatedAt(t, store, "c1", time.Now().Add(-30*time.Minute))

		claimable, err := store.ListClaimableSidekiqJobs(ctx, time.Now().Add(-15*time.Minute))
		require.NoError(t, err)
		for _, j := range claimable {
			assert.NotEqual(t, "c1", j.ID, "a cancelled job must not be re-claimed")
		}

		reaped, err := store.MarkStaleSidekiqJobsFailed(ctx, time.Now().Add(-15*time.Minute))
		require.NoError(t, err)
		assert.Zero(t, reaped, "a cancelled job must not be reaped as stale")
		assert.Equal(t, tables.SidekiqStatusCancelled, getJob(t, store, "c1").Status)

		inFlight, err := store.GetInFlightSidekiqJobByKind(ctx, "k")
		require.NoError(t, err)
		if inFlight != nil {
			assert.NotEqual(t, "c1", inFlight.ID, "a cancelled job must not count as in-flight")
		}
	})

	t.Run("terminal and unknown jobs are no-ops", func(t *testing.T) {
		require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "d1", Kind: "k"}))
		_, err := store.ClaimSidekiqJob(ctx, "d1", "owner-A", time.Now().Add(-time.Minute))
		require.NoError(t, err)
		require.NoError(t, store.CompleteSidekiqJob(ctx, "d1", "owner-A", `{"done":true}`))

		cancelled, err := store.CancelSidekiqJob(ctx, "d1")
		require.NoError(t, err)
		assert.False(t, cancelled, "completing wins a race against a cancel click")
		assert.Equal(t, tables.SidekiqStatusCompleted, getJob(t, store, "d1").Status)

		cancelled, err = store.CancelSidekiqJob(ctx, "does-not-exist")
		require.NoError(t, err)
		assert.False(t, cancelled)
	})
}

func TestFinalizeCancelledSidekiqJob(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()

	newCancelledJob := func(t *testing.T, id string) {
		t.Helper()
		require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: id, Kind: "k", Metadata: `{"processed":3}`}))
		_, err := store.ClaimSidekiqJob(ctx, id, "owner-A", time.Now().Add(-time.Minute))
		require.NoError(t, err)
		_, err = store.CancelSidekiqJob(ctx, id)
		require.NoError(t, err)
	}

	t.Run("stores the partial progress without disturbing the status", func(t *testing.T) {
		newCancelledJob(t, "f1")
		require.NoError(t, store.FinalizeCancelledSidekiqJob(ctx, "f1", "owner-A", `{"processed":9}`))

		got := getJob(t, store, "f1")
		assert.Equal(t, `{"processed":9}`, got.Metadata)
		assert.Equal(t, tables.SidekiqStatusCancelled, got.Status, "finalizing must not change the status")
	})

	t.Run("empty metadata leaves the last checkpoint alone", func(t *testing.T) {
		newCancelledJob(t, "f2")
		require.NoError(t, store.FinalizeCancelledSidekiqJob(ctx, "f2", "owner-A", ""))
		assert.Equal(t, `{"processed":3}`, getJob(t, store, "f2").Metadata)
	})

	t.Run("a non-owner cannot write counters", func(t *testing.T) {
		newCancelledJob(t, "f3")
		require.NoError(t, store.FinalizeCancelledSidekiqJob(ctx, "f3", "owner-B", `{"processed":99}`))
		assert.Equal(t, `{"processed":3}`, getJob(t, store, "f3").Metadata, "fenced on runner_id")
	})
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

// TestClaimPartitionedSidekiqJobFIFOAndExclusion verifies that jobs sharing a
// partitioning key run one-at-a-time in FIFO order, while a distinct key runs in
// parallel: the newer job cannot claim before the older pending one; while the
// older runs the newer stays blocked; only once the older completes does the
// newer claim.
func TestClaimPartitionedSidekiqJobFIFOAndExclusion(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()
	const runner = "node-1"
	stale := time.Now().Add(-30 * time.Second) // fresh running jobs block

	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j1", Kind: "sync", PartitioningKey: "g"}))
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j2", Kind: "sync", PartitioningKey: "g"}))
	base := time.Now().Add(-time.Hour)
	setCreatedAt(t, store, "j1", base)
	setCreatedAt(t, store, "j2", base.Add(time.Minute)) // j2 strictly newer

	j1, j2 := getJob(t, store, "j1"), getJob(t, store, "j2")

	// Newer job must not jump the queue while the older is still pending (FIFO).
	claimed, err := store.ClaimPartitionedSidekiqJob(ctx, "j2", runner, stale, "g", j2.CreatedAt)
	require.NoError(t, err)
	assert.False(t, claimed, "newer job claimed before older pending job")

	// Oldest pending job claims.
	claimed, err = store.ClaimPartitionedSidekiqJob(ctx, "j1", runner, stale, "g", j1.CreatedAt)
	require.NoError(t, err)
	assert.True(t, claimed, "oldest pending job should claim")

	// Newer job stays blocked while the older one runs (mutual exclusion).
	claimed, err = store.ClaimPartitionedSidekiqJob(ctx, "j2", runner, stale, "g", j2.CreatedAt)
	require.NoError(t, err)
	assert.False(t, claimed, "job claimed while same-key job running")

	// A distinct key runs in parallel.
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "k1", Kind: "sync", PartitioningKey: "h"}))
	k1 := getJob(t, store, "k1")
	claimed, err = store.ClaimPartitionedSidekiqJob(ctx, "k1", runner, stale, "h", k1.CreatedAt)
	require.NoError(t, err)
	assert.True(t, claimed, "distinct partitioning key should run in parallel")

	// Once the predecessor completes, the next in line claims.
	require.NoError(t, store.CompleteSidekiqJob(ctx, "j1", runner, "{}"))
	claimed, err = store.ClaimPartitionedSidekiqJob(ctx, "j2", runner, stale, "g", j2.CreatedAt)
	require.NoError(t, err)
	assert.True(t, claimed, "next-in-line should claim after predecessor completes")
}

// TestClaimPartitionedSidekiqJobSelfNotBlockedByPrecisionSkew: a claim timestamp
// newer than the persisted created_at (the eager-spawn nanosecond-vs-truncated
// skew) must not make the sole pending job block itself in the FIFO subquery.
func TestClaimPartitionedSidekiqJobSelfNotBlockedByPrecisionSkew(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()
	const runner = "node-1"
	stale := time.Now().Add(-30 * time.Second)

	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j1", Kind: "sync", PartitioningKey: "g"}))
	persisted := time.Now().Add(-time.Hour)
	setCreatedAt(t, store, "j1", persisted)

	// Claim with a timestamp strictly newer than the persisted value, as the
	// truncation-losing eager spawn would; the only pending job must still claim.
	claimed, err := store.ClaimPartitionedSidekiqJob(ctx, "j1", runner, stale, "g", persisted.Add(time.Microsecond))
	require.NoError(t, err)
	assert.True(t, claimed, "sole pending job must not block itself on a newer claim timestamp")
}

// TestClaimPartitionedSidekiqJobStaleRunnerDoesNotBlock verifies a dead owner's
// stale running job does not deadlock its key: a newer job becomes claimable once
// the running one is past the stale horizon.
func TestClaimPartitionedSidekiqJobStaleRunnerDoesNotBlock(t *testing.T) {
	store := setupSidekiqTestStore(t)
	ctx := context.Background()
	const runner = "node-1"

	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j1", Kind: "sync", PartitioningKey: "g"}))
	require.NoError(t, store.CreateSidekiqJob(ctx, &tables.TableSidekiqJob{ID: "j2", Kind: "sync", PartitioningKey: "g"}))
	base := time.Now().Add(-time.Hour)
	setCreatedAt(t, store, "j1", base)
	setCreatedAt(t, store, "j2", base.Add(time.Minute))

	stale := time.Now().Add(-30 * time.Second)
	claimed, err := store.ClaimPartitionedSidekiqJob(ctx, "j1", runner, stale, "g", getJob(t, store, "j1").CreatedAt)
	require.NoError(t, err)
	require.True(t, claimed)

	// Simulate a dead owner: push j1's heartbeat before the stale horizon.
	setUpdatedAt(t, store, "j1", time.Now().Add(-2*time.Minute))

	claimed, err = store.ClaimPartitionedSidekiqJob(ctx, "j2", runner, stale, "g", getJob(t, store, "j2").CreatedAt)
	require.NoError(t, err)
	assert.True(t, claimed, "stale (dead-owner) running job must not block its key")
}
