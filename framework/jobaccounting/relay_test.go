package jobaccounting

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/relay"
	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeEndpointPoller struct {
	status int
	body   string
	calls  int
}

func (p *fakeEndpointPoller) PollJob(_ *schemas.BifrostContext, _ string, _ relay.JobPollRequest) (*relay.JobPollResponse, *schemas.BifrostError) {
	p.calls++
	return &relay.JobPollResponse{StatusCode: p.status, ContentType: "application/json", Body: []byte(p.body)}, nil
}

type fakeEndpointLogs struct {
	rows map[string]*logstore.RelayLog
}

func (l *fakeEndpointLogs) BatchCreateRelayLogsIfNotExists(_ context.Context, entries []*logstore.RelayLog) ([]string, error) {
	var inserted []string
	for _, e := range entries {
		if _, ok := l.rows[e.ID]; ok {
			continue
		}
		copied := *e
		l.rows[e.ID] = &copied
		inserted = append(inserted, e.ID)
	}
	return inserted, nil
}

func (l *fakeEndpointLogs) FindRelayLog(_ context.Context, id string) (*logstore.RelayLog, error) {
	row, ok := l.rows[id]
	if !ok {
		return nil, logstore.ErrNotFound
	}
	return row, nil
}

type fakeEndpointReporter struct {
	reports []RelayUsageReport
	fail    bool
}

func (r *fakeEndpointReporter) ReportRelayUsage(_ context.Context, report RelayUsageReport) error {
	if r.fail {
		r.fail = false
		return assert.AnError
	}
	r.reports = append(r.reports, report)
	return nil
}

func seedEndpointJob(t *testing.T, store *fakeAccountingStore) *cstables.TableProviderJob {
	t.Helper()
	snap := relay.JobSnapshot{
		Job: schemas.RelayJobConfig{
			IDPath: "JobId", StatusPath: "status", Succeeded: []string{"SUCCEEDED"}, Failed: []string{"FAILED"},
			Poll: schemas.RelayJobPoll{Method: "GET", Path: "/jobs/{{job.id}}"},
		},
		Usage: &schemas.RelayUsageConfig{UnitsPath: "pages", CostPerUnit: schemas.Ptr(0.05)},
	}
	params, err := sonic.MarshalString(snap)
	require.NoError(t, err)
	budgets := `["budget-1"]`
	past := time.Now().UTC().Add(-time.Second)
	job := &cstables.TableProviderJob{
		ID:   cstables.ProviderJobID(cstables.ProviderJobKindRelay, "textract", "req-1:j-1"),
		Kind: cstables.ProviderJobKindRelay, Provider: "textract", JobID: schemas.RelayScopedJobID("req-1", "j-1"),
		Params: &params, NextCheckAt: &past, VirtualKeyID: schemas.Ptr("vk-1"), BudgetIDs: &budgets,
		SourceLogID: schemas.Ptr("req-1"), CreatedAt: time.Now().UTC(),
	}
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))
	return store.jobs[job.ID]
}

func makeDue(job *cstables.TableProviderJob) {
	past := time.Now().UTC().Add(-time.Second)
	job.NextCheckAt = &past
}

func TestEndpointJobSweeperSettlesOnce(t *testing.T) {
	ctx := context.Background()
	store := newFakeAccountingStore()
	job := seedEndpointJob(t, store)
	poller := &fakeEndpointPoller{status: 200, body: `{"status":"IN_PROGRESS"}`}
	logs := &fakeEndpointLogs{rows: map[string]*logstore.RelayLog{}}
	reporter := &fakeEndpointReporter{}
	sweeper := NewRelayJobSweeper(store, poller, logs, reporter, RelayJobSweeperConfig{ClaimedBy: "node-1"})

	require.NoError(t, sweeper.SweepOnce(ctx))
	assert.Empty(t, logs.rows, "a running job writes nothing")
	assert.Equal(t, 1, job.PollAttempts)
	assert.Equal(t, "IN_PROGRESS", job.ProviderStatus)
	assert.True(t, job.NextCheckAt.After(time.Now()), "and is polled again later")

	// The governance report fails once: the row is written, the charge retried.
	poller.body = `{"status":"SUCCEEDED","pages":3}`
	reporter.fail = true
	makeDue(job)
	require.Error(t, sweeper.SweepOnce(ctx))
	require.Len(t, logs.rows, 1)
	assert.NotNil(t, job.AggregateLogWrittenAt)
	assert.Empty(t, reporter.reports)

	pollsBefore := poller.calls
	makeDue(job)
	require.NoError(t, sweeper.SweepOnce(ctx))
	assert.Equal(t, pollsBefore, poller.calls, "a settlement resumed after its row was written does not poll again")
	require.Len(t, reporter.reports, 1)
	report := reporter.reports[0]
	assert.InDelta(t, 3*0.05, report.Cost, 1e-12)
	assert.Equal(t, []string{"budget-1"}, report.BudgetIDs)
	assert.Equal(t, "vk-1", report.VirtualKeyID)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, job.AccountingStatus)

	var row *logstore.RelayLog
	for _, r := range logs.rows {
		row = r
	}
	assert.Equal(t, logstore.RelayLogKindJob, row.Kind)
	assert.Equal(t, "j-1", *row.JobID)
	assert.Equal(t, "req-1", *row.ParentLogID)
	assert.Equal(t, report.RequestID, row.ID, "the charge is keyed by the settlement row, so governance dedupes retries")

	makeDue(job)
	require.NoError(t, sweeper.SweepOnce(ctx))
	assert.Len(t, reporter.reports, 1, "an accounted job is never charged twice")
}

func TestEndpointJobSweeperRecoversWrittenSettlementPastTimeout(t *testing.T) {
	ctx := context.Background()
	store := newFakeAccountingStore()
	job := seedEndpointJob(t, store)
	job.CreatedAt = time.Now().UTC().Add(-2 * time.Hour) // past the default 3600s timeout
	logs := &fakeEndpointLogs{rows: map[string]*logstore.RelayLog{}}
	reporter := &fakeEndpointReporter{}
	poller := &fakeEndpointPoller{status: 200, body: `{"status":"SUCCEEDED","pages":3}`}
	sweeper := NewRelayJobSweeper(store, poller, logs, reporter, RelayJobSweeperConfig{ClaimedBy: "node-1"})

	// A worker wrote the row and stopped before recording it on the job.
	id := sweeper.settlementRowID(job)
	logs.rows[id] = &logstore.RelayLog{ID: id, Cost: schemas.Ptr(0.15)}

	require.NoError(t, sweeper.SweepOnce(ctx))
	assert.Zero(t, poller.calls, "the written settlement is finished without polling")
	require.Len(t, reporter.reports, 1, "and its cost is charged instead of the job being dropped as timed out")
	assert.InDelta(t, 0.15, reporter.reports[0].Cost, 1e-12)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, job.AccountingStatus)
}

func TestEndpointJobSweeperRereadsMarkersAfterClaim(t *testing.T) {
	ctx := context.Background()
	store := newFakeAccountingStore()
	job := seedEndpointJob(t, store)
	logs := &fakeEndpointLogs{rows: map[string]*logstore.RelayLog{}}
	reporter := &fakeEndpointReporter{}
	poller := &fakeEndpointPoller{status: 200, body: `{"status":"SUCCEEDED","pages":3}`}
	sweeper := NewRelayJobSweeper(store, poller, logs, reporter, RelayJobSweeperConfig{ClaimedBy: "node-1"})

	// Another node settled and charged the job after this node listed it, then stopped before
	// completing it. The stale listed copy has neither marker.
	stale := *job
	now := time.Now().UTC()
	job.AggregateLogWrittenAt, job.GovernanceReportedAt = &now, &now
	require.NoError(t, sweeper.handle(ctx, &stale))
	assert.Empty(t, reporter.reports, "the markers read after the claim stop a second charge")
	assert.Zero(t, poller.calls)
}

func TestEndpointJobSettlementTimestampIsStable(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	job := &cstables.TableProviderJob{CreatedAt: created}
	assert.Equal(t, created, settlementTimestamp(job), "a retried settlement reuses the same ClickHouse key")
}

func TestEndpointJobSweeperFailedAndGoneJobs(t *testing.T) {
	ctx := context.Background()
	store := newFakeAccountingStore()
	job := seedEndpointJob(t, store)
	logs := &fakeEndpointLogs{rows: map[string]*logstore.RelayLog{}}
	reporter := &fakeEndpointReporter{}
	sweeper := NewRelayJobSweeper(store, &fakeEndpointPoller{status: 200, body: `{"status":"FAILED","pages":3}`}, logs, reporter, RelayJobSweeperConfig{ClaimedBy: "node-1"})
	require.NoError(t, sweeper.SweepOnce(ctx))
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, job.AccountingStatus)
	require.Len(t, logs.rows, 1)
	assert.Empty(t, reporter.reports, "a failed job is recorded at no charge")

	store = newFakeAccountingStore()
	job = seedEndpointJob(t, store)
	sweeper = NewRelayJobSweeper(store, &fakeEndpointPoller{status: http.StatusNotFound, body: `{}`}, logs, reporter, RelayJobSweeperConfig{ClaimedBy: "node-1"})
	require.NoError(t, sweeper.SweepOnce(ctx))
	assert.Equal(t, cstables.ProviderJobAccountingStatusUnpriceable, job.AccountingStatus, "a job the upstream forgot stops being polled")
	require.NotNil(t, job.UnpriceableReason)
	assert.Equal(t, UnpriceableReasonRelayJobGone, *job.UnpriceableReason)
}
