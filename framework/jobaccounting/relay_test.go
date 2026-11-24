package jobaccounting

import (
	"context"
	"net/http"
	"strings"
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
	err    *schemas.BifrostError
	calls  int
	onPoll func()
}

func (p *fakeEndpointPoller) PollJob(_ *schemas.BifrostContext, _ string, _ relay.JobPollRequest) (*relay.JobPollResponse, *schemas.BifrostError) {
	p.calls++
	if p.onPoll != nil {
		p.onPoll()
	}
	if p.err != nil {
		return nil, p.err
	}
	return &relay.JobPollResponse{StatusCode: p.status, ContentType: "application/json", Body: []byte(p.body)}, nil
}

type fakeEndpointLogs struct {
	rows map[string]*logstore.RelayLog
}

func newFakeEndpointLogs() *fakeEndpointLogs {
	return &fakeEndpointLogs{rows: map[string]*logstore.RelayLog{}}
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
	reports    []RelayUsageReport
	fail       bool
	failAlways bool
}

func (r *fakeEndpointReporter) ReportRelayUsage(_ context.Context, report RelayUsageReport) error {
	if r.failAlways {
		return assert.AnError
	}
	if r.fail {
		r.fail = false
		return assert.AnError
	}
	r.reports = append(r.reports, report)
	return nil
}

// failMarkOnceStore fails the first "row written" marker, after the row itself was stored.
type failMarkOnceStore struct {
	*fakeAccountingStore
	failed bool
}

func (s *failMarkOnceStore) MarkProviderJobAggregateLogWritten(ctx context.Context, id, runnerID string) error {
	if !s.failed {
		s.failed = true
		return assert.AnError
	}
	return s.fakeAccountingStore.MarkProviderJobAggregateLogWritten(ctx, id, runnerID)
}

func seedEndpointJob(t *testing.T, store *fakeAccountingStore) *cstables.TableProviderJob {
	t.Helper()
	return seedEndpointJobWithInterval(t, store, 0)
}

func seedEndpointJobWithInterval(t *testing.T, store *fakeAccountingStore, interval int) *cstables.TableProviderJob {
	t.Helper()
	snap := relay.JobSnapshot{
		Job: schemas.RelayJobConfig{
			IDPath: "JobId", StatusPath: "status", Succeeded: []string{"SUCCEEDED"}, Failed: []string{"FAILED"},
			Poll: schemas.RelayJobPoll{Method: "GET", Path: "/jobs/{{job.id}}"}, IntervalSeconds: interval,
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

func relaySweeper(store SweepStore, poller RelayJobPoller, logs RelayJobLogStore, reporter RelayUsageReporter) *Sweeper {
	return NewRelayJobSweeper(store, poller, logs, reporter, SweeperConfig{ClaimedBy: "node-1"})
}

func TestEndpointJobSweeperSettlesOnce(t *testing.T) {
	ctx := context.Background()
	store := newFakeAccountingStore()
	job := seedEndpointJob(t, store)
	poller := &fakeEndpointPoller{status: 200, body: `{"status":"IN_PROGRESS"}`}
	logs := newFakeEndpointLogs()
	// The start call's row, whose model, route and names the job row copies.
	logs.rows["req-1"] = &logstore.RelayLog{ID: "req-1", Model: "textract-v2", RoutePath: "/jobs", VirtualKeyName: schemas.Ptr("prod"),
		TeamName: schemas.Ptr("Team One"), ProjectID: schemas.Ptr("p-1"), App: schemas.Ptr("claude-code"), TeamIDsParsed: []string{"t1"}}
	reporter := &fakeEndpointReporter{}
	sweeper := relaySweeper(store, poller, logs, reporter)

	sweeper.SweepOnce(ctx)
	assert.Len(t, logs.rows, 1, "a running job writes nothing")
	assert.Equal(t, 1, job.PollAttempts)
	assert.Equal(t, "IN_PROGRESS", job.ProviderStatus)
	assert.True(t, job.NextCheckAt.After(time.Now()), "and is polled again later")

	// The governance report fails once: the row is written, the charge retried.
	poller.body = `{"status":"SUCCEEDED","pages":3}`
	reporter.fail = true
	makeDue(job)
	sweeper.SweepOnce(ctx)
	require.Len(t, logs.rows, 2)
	assert.NotNil(t, job.AggregateLogWrittenAt)
	assert.Empty(t, reporter.reports)

	pollsBefore := poller.calls
	makeDue(job)
	sweeper.SweepOnce(ctx)
	assert.Equal(t, pollsBefore, poller.calls, "a settlement resumed after its row was written does not poll again")
	require.Len(t, reporter.reports, 1)
	report := reporter.reports[0]
	assert.InDelta(t, 3*0.05, report.Cost, 1e-12)
	assert.Equal(t, []string{"budget-1"}, report.BudgetIDs)
	assert.Equal(t, "vk-1", report.VirtualKeyID)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, job.AccountingStatus)

	row := logs.rows[relayRowID(job)]
	require.NotNil(t, row)
	assert.Equal(t, logstore.RelayLogKindJob, row.Kind)
	assert.Equal(t, "j-1", *row.JobID)
	assert.Equal(t, "req-1", *row.ParentLogID)
	assert.Equal(t, report.RequestID, row.ID, "the charge is keyed by the settlement row, so governance dedupes retries")
	require.NotNil(t, row.JobDebugParsed)
	assert.Equal(t, 2, row.JobDebugParsed.Polls, "one poll found it running, one found it done")
	assert.Equal(t, "SUCCEEDED", row.JobDebugParsed.LastStatus)
	assert.Equal(t, 200, row.JobDebugParsed.LastHTTPStatus)
	assert.Empty(t, row.JobDebugParsed.LastError)
	assert.Equal(t, "textract-v2", row.Model)
	assert.Equal(t, "/jobs", row.RoutePath)
	assert.Equal(t, "prod", *row.VirtualKeyName)
	assert.Equal(t, "Team One", *row.TeamName)
	assert.Equal(t, "p-1", *row.ProjectID)
	assert.Equal(t, "claude-code", *row.App)
	assert.Equal(t, []string{"t1"}, row.TeamIDsParsed)

	makeDue(job)
	sweeper.SweepOnce(ctx)
	assert.Len(t, reporter.reports, 1, "an accounted job is never charged twice")
}

func TestEndpointJobSweeperChargesAWrittenRowOverALaterReply(t *testing.T) {
	store := newFakeAccountingStore()
	job := seedEndpointJob(t, store)
	job.CreatedAt = time.Now().UTC().Add(-2 * time.Hour) // past the default 3600s timeout
	logs := newFakeEndpointLogs()
	reporter := &fakeEndpointReporter{}
	poller := &fakeEndpointPoller{status: 200, body: `{"status":"SUCCEEDED","pages":9}`}

	// A worker wrote the row and stopped before recording it on the job.
	id := relayRowID(job)
	logs.rows[id] = &logstore.RelayLog{ID: id, Cost: schemas.Ptr(0.15)}

	relaySweeper(store, poller, logs, reporter).SweepOnce(context.Background())
	require.Len(t, reporter.reports, 1)
	assert.InDelta(t, 0.15, reporter.reports[0].Cost, 1e-12, "the written row is what is charged")
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, job.AccountingStatus)
}

func TestEndpointJobSweeperRereadsMarkersBeforePolling(t *testing.T) {
	store := newFakeAccountingStore()
	job := seedEndpointJob(t, store)
	logs := newFakeEndpointLogs()
	logs.rows[relayRowID(job)] = &logstore.RelayLog{ID: relayRowID(job), Cost: schemas.Ptr(0.15)}
	reporter := &fakeEndpointReporter{}
	poller := &fakeEndpointPoller{status: 200, body: `{"status":"SUCCEEDED","pages":3}`}
	sweeper := relaySweeper(store, poller, logs, reporter)

	// Another node settled and charged the job after this node listed it, then stopped before
	// completing it. The stale listed copy has neither marker.
	stale := *job
	now := time.Now().UTC()
	job.AggregateLogWrittenAt, job.GovernanceReportedAt = &now, &now
	sweeper.sweepJob(context.Background(), &stale, now)
	assert.Empty(t, reporter.reports, "the markers read after listing stop a second charge")
	assert.Zero(t, poller.calls)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, job.AccountingStatus)
}

func TestEndpointJobSettlementTimestampIsStable(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	job := &cstables.TableProviderJob{CreatedAt: created}
	assert.Equal(t, created, settlementTimestamp(job), "a retried settlement reuses the same ClickHouse key")
}

func TestEndpointJobSweeperFailedJobCostsNothing(t *testing.T) {
	store := newFakeAccountingStore()
	job := seedEndpointJob(t, store)
	logs := newFakeEndpointLogs()
	reporter := &fakeEndpointReporter{}
	relaySweeper(store, &fakeEndpointPoller{status: 200, body: `{"status":"FAILED","pages":3}`}, logs, reporter).SweepOnce(context.Background())
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, job.AccountingStatus)
	row := logs.rows[relayRowID(job)]
	require.NotNil(t, row)
	assert.Zero(t, *row.Cost)
	assert.Equal(t, "FAILED", *row.JobStatus)
	assert.Empty(t, reporter.reports, "a failed job is recorded at no charge")
}

// One 4xx poll does not forgive a job: eventually consistent APIs answer 404 for a new job and a key
// rotation answers 403, so the poll is retried until the timeout and only then ends the job unpriced.
func TestEndpointJobSweeperRetriesClientErrorsUntilTimeout(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		status int
		body   string
		reason string
	}{
		{"not found", http.StatusNotFound, `{}`, UnpriceableReasonRelayJobGone},
		{"access denied", http.StatusForbidden, `{"__type":"AccessDeniedException"}`, UnpriceableReasonRelayJobDenied},
		{"refused id", http.StatusBadRequest, `{"__type":"InvalidJobIdException"}`, UnpriceableReasonRelayJobRefused},
		{"conflict", http.StatusConflict, `{}`, UnpriceableReasonRelayJobRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeAccountingStore()
			job := seedEndpointJob(t, store)
			poller := &fakeEndpointPoller{status: tc.status, body: tc.body}
			sweeper := relaySweeper(store, poller, newFakeEndpointLogs(), &fakeEndpointReporter{})
			sweeper.SweepOnce(ctx)
			assert.NotEqual(t, cstables.ProviderJobAccountingStatusUnpriceable, job.AccountingStatus, "retried before the timeout")
			assert.True(t, job.NextCheckAt.After(time.Now()))

			job.CreatedAt = time.Now().UTC().Add(-2 * time.Hour)
			makeDue(job)
			sweeper.SweepOnce(ctx)
			assert.Equal(t, 2, poller.calls, "polled once more past the timeout")
			assert.Equal(t, cstables.ProviderJobAccountingStatusUnpriceable, job.AccountingStatus)
			require.NotNil(t, job.UnpriceableReason)
			assert.Equal(t, tc.reason, *job.UnpriceableReason)
		})
	}

	// A failed poll before the timeout comes back as an error, so the sweeper logs it like other kinds.
	{
		store := newFakeAccountingStore()
		job := seedEndpointJob(t, store)
		sweeper := relaySweeper(store, &fakeEndpointPoller{status: http.StatusBadGateway, body: `{}`}, newFakeEndpointLogs(), &fakeEndpointReporter{})
		poll, err := sweeper.settler.Poll(ctx, job)
		assert.Nil(t, poll)
		assert.ErrorContains(t, err, "job poll answered 502")
	}

	// The same 4xx answering a later poll before the timeout no longer matters once the job succeeds.
	store := newFakeAccountingStore()
	job := seedEndpointJob(t, store)
	poller := &fakeEndpointPoller{status: http.StatusNotFound, body: `{}`}
	reporter := &fakeEndpointReporter{}
	sweeper := relaySweeper(store, poller, newFakeEndpointLogs(), reporter)
	sweeper.SweepOnce(ctx)
	poller.status, poller.body = 200, `{"status":"SUCCEEDED","pages":2}`
	makeDue(job)
	sweeper.SweepOnce(ctx)
	require.Len(t, reporter.reports, 1, "a job that answered 404 once is still charged when it succeeds")
	assert.InDelta(t, 2*0.05, reporter.reports[0].Cost, 1e-12)
}

// A job past its timeout is polled one last time, so one that finished just before the deadline,
// or while the sweeper was behind, is charged; one still running is written off.
func TestEndpointJobSweeperPollsOnceMorePastTimeout(t *testing.T) {
	ctx := context.Background()
	store := newFakeAccountingStore()
	job := seedEndpointJob(t, store)
	job.CreatedAt = time.Now().UTC().Add(-2 * time.Hour)
	poller := &fakeEndpointPoller{status: 200, body: `{"status":"SUCCEEDED","pages":3}`}
	reporter := &fakeEndpointReporter{}
	relaySweeper(store, poller, newFakeEndpointLogs(), reporter).SweepOnce(ctx)
	assert.Equal(t, 1, poller.calls)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, job.AccountingStatus)
	require.Len(t, reporter.reports, 1)
	assert.InDelta(t, 3*0.05, reporter.reports[0].Cost, 1e-12)

	store = newFakeAccountingStore()
	job = seedEndpointJob(t, store)
	job.CreatedAt = time.Now().UTC().Add(-2 * time.Hour)
	logs := newFakeEndpointLogs()
	relaySweeper(store, &fakeEndpointPoller{status: 200, body: `{"status":"IN_PROGRESS"}`}, logs, &fakeEndpointReporter{}).SweepOnce(ctx)
	assert.Equal(t, cstables.ProviderJobAccountingStatusUnpriceable, job.AccountingStatus)
	require.NotNil(t, job.UnpriceableReason)
	assert.Equal(t, UnpriceableReasonRelayJobTimeout, *job.UnpriceableReason)
	// The job row says why: the status it was stuck at and the timeout.
	row := logs.rows[relayRowID(job)]
	require.NotNil(t, row)
	require.NotNil(t, row.JobDebugParsed)
	assert.Equal(t, 1, row.JobDebugParsed.Polls)
	assert.Equal(t, "IN_PROGRESS", row.JobDebugParsed.LastStatus)
	assert.Contains(t, row.JobDebugParsed.LastError, "did not finish")
	assert.Empty(t, row.Model, "no start row to copy from leaves the job row as it was")
}

// A poll the upstream throttles, or one sent while the relay is missing from the live set (a reload,
// a config.json prune), is retried later instead of writing the job off for free.
func TestEndpointJobSweeperRetriesThrottledAndMissingRelayPolls(t *testing.T) {
	for name, poller := range map[string]*fakeEndpointPoller{
		"AWS throttling as 400": {status: http.StatusBadRequest, body: `{"__type":"ThrottlingException","message":"Rate exceeded"}`},
		"relay not loaded":      {err: relay.RelayNotFoundError("textract")},
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeAccountingStore()
			job := seedEndpointJob(t, store)
			relaySweeper(store, poller, newFakeEndpointLogs(), &fakeEndpointReporter{}).SweepOnce(context.Background())
			assert.NotEqual(t, cstables.ProviderJobAccountingStatusUnpriceable, job.AccountingStatus)
			assert.Equal(t, 1, job.PollAttempts)
			assert.True(t, job.NextCheckAt.After(time.Now()), "polled again after the backoff")
		})
	}
}

// A succeeded job whose reply carries no units cannot be priced, so it is not recorded as a free success.
func TestEndpointJobSweeperSucceededWithoutUnitsIsUnpriceable(t *testing.T) {
	store := newFakeAccountingStore()
	job := seedEndpointJob(t, store)
	logs := newFakeEndpointLogs()
	relaySweeper(store, &fakeEndpointPoller{status: 200, body: `{"status":"SUCCEEDED","DocumentMetadata":{"Pages":3}}`}, logs, &fakeEndpointReporter{}).SweepOnce(context.Background())
	assert.Equal(t, cstables.ProviderJobAccountingStatusUnpriceable, job.AccountingStatus)
	require.NotNil(t, job.UnpriceableReason)
	assert.Equal(t, UnpriceableReasonRelayJobNoUsage, *job.UnpriceableReason)
	row := logs.rows[relayRowID(job)]
	require.NotNil(t, row, "every job that ends gets a row")
	assert.Nil(t, row.Cost, "an unknown cost is not $0")
	assert.Equal(t, "SUCCEEDED", *row.JobStatus)
}

// Every ending writes a job row with a blank cost and its reason, and charges nothing.
func TestEndpointJobEndingUnpricedGetsARow(t *testing.T) {
	for name, tc := range map[string]struct {
		poller    *fakeEndpointPoller
		reason    string
		jobStatus string
	}{
		"timed out":     {&fakeEndpointPoller{status: 200, body: `{"status":"IN_PROGRESS"}`}, UnpriceableReasonRelayJobTimeout, "IN_PROGRESS"},
		"gone":          {&fakeEndpointPoller{status: http.StatusNotFound, body: `{}`}, UnpriceableReasonRelayJobGone, UnpriceableReasonRelayJobGone},
		"access denied": {&fakeEndpointPoller{status: http.StatusForbidden, body: `{}`}, UnpriceableReasonRelayJobDenied, UnpriceableReasonRelayJobDenied},
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeAccountingStore()
			job := seedEndpointJob(t, store)
			job.CreatedAt = time.Now().UTC().Add(-2 * time.Hour)
			logs := newFakeEndpointLogs()
			reporter := &fakeEndpointReporter{}
			relaySweeper(store, tc.poller, logs, reporter).SweepOnce(context.Background())

			assert.Equal(t, cstables.ProviderJobAccountingStatusUnpriceable, job.AccountingStatus)
			assert.Equal(t, tc.reason, *job.UnpriceableReason)
			row := logs.rows[relayRowID(job)]
			require.NotNil(t, row)
			assert.Nil(t, row.Cost)
			assert.Equal(t, tc.jobStatus, *row.JobStatus)
			assert.Equal(t, tc.reason, *row.ErrorDetailsParsed.Error.Type)
			assert.Empty(t, reporter.reports)
		})
	}
}

// A retry after the row's marker failed keeps the unpriced ending instead of counting it as a $0 job.
func TestEndpointJobUnpricedRowSurvivesAFailedMarker(t *testing.T) {
	inner := newFakeAccountingStore()
	job := seedEndpointJob(t, inner)
	store := &failMarkOnceStore{fakeAccountingStore: inner}
	logs := newFakeEndpointLogs()
	reporter := &fakeEndpointReporter{}
	poller := &fakeEndpointPoller{status: 200, body: `{"status":"SUCCEEDED"}`}
	sweeper := relaySweeper(store, poller, logs, reporter)
	sweeper.SweepOnce(context.Background())
	require.Len(t, logs.rows, 1)

	poller.body = `{"status":"SUCCEEDED","pages":3}`
	makeDue(job)
	sweeper.SweepOnce(context.Background())
	assert.Equal(t, cstables.ProviderJobAccountingStatusUnpriceable, job.AccountingStatus)
	assert.Equal(t, UnpriceableReasonRelayJobNoUsage, *job.UnpriceableReason)
	assert.Nil(t, logs.rows[relayRowID(job)].Cost)
	assert.Empty(t, reporter.reports)
}

// A settlement that keeps failing backs off like a poll instead of retrying on every sweep.
func TestEndpointJobSweeperBacksOffFailedSettlement(t *testing.T) {
	store := newFakeAccountingStore()
	job := seedEndpointJob(t, store)
	reporter := &fakeEndpointReporter{failAlways: true}
	sweeper := relaySweeper(store, &fakeEndpointPoller{status: 200, body: `{"status":"SUCCEEDED","pages":3}`}, newFakeEndpointLogs(), reporter)
	sweeper.SweepOnce(context.Background())
	assert.Equal(t, 1, job.PollAttempts)
	assert.True(t, job.NextCheckAt.After(time.Now()))
	sweeper.SweepOnce(context.Background())
	assert.Equal(t, 1, job.PollAttempts, "not due again yet, so the second sweep leaves it alone")
}

// A final poll failing while another node writes the row must not write the job off.
func TestEndpointJobFinalPollFailureRacingAWrittenRowStillCharges(t *testing.T) {
	store := newFakeAccountingStore()
	job := seedEndpointJob(t, store)
	job.CreatedAt = time.Now().UTC().Add(-2 * time.Hour)
	logs := newFakeEndpointLogs()
	reporter := &fakeEndpointReporter{}
	poller := &fakeEndpointPoller{status: http.StatusNotFound, body: `{}`, onPoll: func() {
		now := time.Now().UTC()
		logs.rows[relayRowID(job)] = &logstore.RelayLog{ID: relayRowID(job), Cost: schemas.Ptr(0.15)}
		job.AggregateLogWrittenAt = &now
		job.AccountingStatus = cstables.ProviderJobAccountingStatusError
	}}
	relaySweeper(store, poller, logs, reporter).SweepOnce(context.Background())
	require.Len(t, reporter.reports, 1)
	assert.InDelta(t, 0.15, reporter.reports[0].Cost, 1e-12)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, job.AccountingStatus)
}

func TestEndpointJobRowWrittenMarkerFailedReplyWithoutUsageCharges(t *testing.T) {
	inner := newFakeAccountingStore()
	job := seedEndpointJob(t, inner)
	store := &failMarkOnceStore{fakeAccountingStore: inner}
	logs := newFakeEndpointLogs()
	reporter := &fakeEndpointReporter{}
	poller := &fakeEndpointPoller{status: 200, body: `{"status":"SUCCEEDED","pages":3}`}
	sweeper := relaySweeper(store, poller, logs, reporter)

	sweeper.SweepOnce(context.Background())
	require.Len(t, logs.rows, 1)
	require.Nil(t, job.AggregateLogWrittenAt)
	assert.Empty(t, reporter.reports)

	poller.body = `{"status":"SUCCEEDED"}`
	makeDue(job)
	sweeper.SweepOnce(context.Background())
	require.Len(t, reporter.reports, 1, "the written row is charged, not written off")
	assert.InDelta(t, 3*0.05, reporter.reports[0].Cost, 1e-12)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, job.AccountingStatus)
}

func TestEndpointJobMarkedWrittenWithRowMissing(t *testing.T) {
	ctx := context.Background()
	marked := func(t *testing.T, store *fakeAccountingStore) *cstables.TableProviderJob {
		job := seedEndpointJob(t, store)
		now := time.Now().UTC()
		job.AggregateLogWrittenAt = &now
		return job
	}

	t.Run("already reported", func(t *testing.T) {
		store := newFakeAccountingStore()
		job := marked(t, store)
		now := time.Now().UTC()
		job.GovernanceReportedAt = &now
		reporter := &fakeEndpointReporter{}
		relaySweeper(store, &fakeEndpointPoller{}, newFakeEndpointLogs(), reporter).SweepOnce(ctx)
		assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, job.AccountingStatus)
		assert.Empty(t, reporter.reports)
	})
	t.Run("no reporter", func(t *testing.T) {
		store := newFakeAccountingStore()
		job := marked(t, store)
		relaySweeper(store, &fakeEndpointPoller{}, newFakeEndpointLogs(), nil).SweepOnce(ctx)
		assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, job.AccountingStatus)
	})
	t.Run("unreported before the deadline retries", func(t *testing.T) {
		store := newFakeAccountingStore()
		job := marked(t, store)
		relaySweeper(store, &fakeEndpointPoller{}, newFakeEndpointLogs(), &fakeEndpointReporter{}).SweepOnce(ctx)
		assert.Equal(t, cstables.ProviderJobAccountingStatusError, job.AccountingStatus)
		assert.True(t, job.NextCheckAt.After(time.Now()))
	})
	t.Run("unreported past the deadline parks", func(t *testing.T) {
		store := newFakeAccountingStore()
		job := marked(t, store)
		job.CreatedAt = time.Now().UTC().Add(-2 * time.Hour)
		relaySweeper(store, &fakeEndpointPoller{}, newFakeEndpointLogs(), &fakeEndpointReporter{}).SweepOnce(ctx)
		assert.Equal(t, cstables.ProviderJobAccountingStatusUnpriceable, job.AccountingStatus)
		require.NotNil(t, job.UnpriceableReason)
		assert.Equal(t, UnpriceableReasonRelayJobRowMissing, *job.UnpriceableReason)
	})
}

func TestEndpointJobIsNotCappedAtMaxPollAttempts(t *testing.T) {
	store := newFakeAccountingStore()
	job := seedEndpointJob(t, store)
	job.PollAttempts = MaxPollAttempts
	relaySweeper(store, &fakeEndpointPoller{status: 200, body: `{"status":"IN_PROGRESS"}`}, newFakeEndpointLogs(), &fakeEndpointReporter{}).SweepOnce(context.Background())
	assert.NotEqual(t, cstables.ProviderJobAccountingStatusUnpriceable, job.AccountingStatus)
	assert.Equal(t, MaxPollAttempts+1, job.PollAttempts)
}

func TestEndpointJobFreshClaimIsNotPolled(t *testing.T) {
	store := newFakeAccountingStore()
	job := seedEndpointJob(t, store)
	now := time.Now().UTC()
	job.AccountingStatus = cstables.ProviderJobAccountingStatusProcessing
	job.ClaimedAt = &now
	poller := &fakeEndpointPoller{status: 200, body: `{"status":"SUCCEEDED","pages":3}`}
	relaySweeper(store, poller, newFakeEndpointLogs(), &fakeEndpointReporter{}).SweepOnce(context.Background())
	assert.Zero(t, poller.calls)
	assert.True(t, job.NextCheckAt.After(now))
}

func TestEndpointJobKeepsItsIntervalAfterFailures(t *testing.T) {
	const interval = 300 * time.Second
	for name, setup := range map[string]struct {
		poller   *fakeEndpointPoller
		reporter *fakeEndpointReporter
	}{
		"poll failure":       {&fakeEndpointPoller{status: http.StatusBadGateway, body: `{}`}, &fakeEndpointReporter{}},
		"settlement failure": {&fakeEndpointPoller{status: 200, body: `{"status":"SUCCEEDED","pages":3}`}, &fakeEndpointReporter{failAlways: true}},
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeAccountingStore()
			job := seedEndpointJobWithInterval(t, store, int(interval/time.Second))
			before := time.Now().UTC()
			relaySweeper(store, setup.poller, newFakeEndpointLogs(), setup.reporter).SweepOnce(context.Background())
			after := time.Now().UTC()
			require.NotNil(t, job.NextCheckAt)
			assert.False(t, job.NextCheckAt.Before(before.Add(interval)))
			assert.False(t, job.NextCheckAt.After(after.Add(interval)), "no jitter on a relay job's own interval")
		})
	}
}

func TestRelayRowIDDiffersFromBatch(t *testing.T) {
	assert.NotEqual(t,
		AccountingLogID(ProviderJobKindBatch, schemas.OpenAI, "job-1"),
		AccountingLogID(ProviderJobKindRelay, schemas.OpenAI, "job-1"))
}

// Relay claims are named like the other kinds': the kind, then the node, or this process when unclustered.
func TestRelayRunnerIDAndBackoffMatchTheOtherKinds(t *testing.T) {
	assert.Equal(t, "relay-sweeper:node-1", RunnerID("relay-sweeper", "node-1"))
	assert.Equal(t, RunnerID("relay-sweeper", ""), RunnerID("relay-sweeper", ""), "stable across reloads within a process")
	assert.True(t, strings.HasPrefix(RunnerID("relay-sweeper", ""), "relay-sweeper:"))

	relay, video := &RelaySettler{}, NewVideoSettler(nil)
	for _, attempts := range []int{0, 20, 40} {
		assert.Equal(t, video.Backoff(attempts, 30*time.Second), relay.Backoff(attempts, 30*time.Second), "attempts %d", attempts)
	}
	assert.Equal(t, 5*time.Minute, relay.Backoff(40, 30*time.Second))
}

// The job details record the status the upstream answered, and count a final poll that got no answer.
func TestRelayJobDebugRecordsTheAnsweredStatusAndEverySentPoll(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		poller     *fakeEndpointPoller
		wantStatus int
	}{
		{"throttled 400 past the deadline", &fakeEndpointPoller{status: http.StatusBadRequest, body: `{"__type":"ThrottlingException"}`}, http.StatusBadRequest},
		{"no answer past the deadline", &fakeEndpointPoller{err: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "connection reset"}}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeAccountingStore()
			job := seedEndpointJob(t, store)
			job.PollAttempts = 2
			job.CreatedAt = time.Now().UTC().Add(-2 * time.Hour)
			logs := newFakeEndpointLogs()
			relaySweeper(store, tc.poller, logs, &fakeEndpointReporter{}).SweepOnce(ctx)
			row := logs.rows[relayRowID(job)]
			require.NotNil(t, row)
			require.NotNil(t, row.JobDebugParsed)
			assert.Equal(t, tc.wantStatus, row.JobDebugParsed.LastHTTPStatus)
			assert.Equal(t, 3, row.JobDebugParsed.Polls, "two earlier polls and the one that ended the job")
		})
	}
}
