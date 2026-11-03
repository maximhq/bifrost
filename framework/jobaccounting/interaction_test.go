package jobaccounting

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const deepResearch = "deep-research-preview-04-2026"

type fakeInteractionPoller struct {
	resp  *schemas.BifrostPassthroughResponse
	err   error
	calls int
}

func (f *fakeInteractionPoller) PollInteraction(ctx context.Context, job *cstables.TableProviderJob) (*schemas.BifrostPassthroughResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// interactionJob builds the row a create leaves behind: the run, the agent it named, where to poll
// it, and who created it.
func interactionJob(t *testing.T, id string) *cstables.TableProviderJob {
	t.Helper()
	params, err := MarshalInteractionParams(InteractionJobParams{APIVersion: "v1beta"})
	require.NoError(t, err)
	vk := "vk-creator"
	source := "req-create-" + id
	return &cstables.TableProviderJob{
		ID:               cstables.ProviderJobID(cstables.ProviderJobKindInteraction, string(schemas.Gemini), id),
		Kind:             cstables.ProviderJobKindInteraction,
		Provider:         string(schemas.Gemini),
		JobID:            id,
		Model:            deepResearch,
		Endpoint:         "/interactions/" + id,
		Params:           params,
		AccountingStatus: cstables.ProviderJobAccountingStatusPending,
		SelectedKeyID:    "key-creator",
		VirtualKeyID:     &vk,
		SourceLogID:      &source,
	}
}

func finishedRun(id, status string, prompt, completion int) *schemas.BifrostPassthroughJob {
	run := &schemas.BifrostPassthroughJob{ID: id, Status: status, Terminal: true, PollPath: "/interactions/" + id}
	if prompt > 0 || completion > 0 {
		run.Usage = &schemas.BifrostPassthroughUsage{LLMUsage: &schemas.BifrostLLMUsage{
			PromptTokens:     prompt,
			CompletionTokens: completion,
			TotalTokens:      prompt + completion,
		}}
	}
	return run
}

func runCost(prompt, completion int) float64 {
	return float64(prompt)*0.000002 + float64(completion)*0.000012
}

func accountInteractionInline(t *testing.T, store *fakeAccountingStore, reporter *fakeUsageReporter, runner string, run *schemas.BifrostPassthroughJob) *Outcome {
	t.Helper()
	out, err := AccountInteractionJob(context.Background(), store, store, fakePricing{}, JobRequest{
		Provider:      schemas.Gemini,
		ProviderJobID: run.ID,
		FallbackModel: deepResearch,
		UsageReporter: reporter,
		ClaimedBy:     runner,
		Payload:       InteractionPayload{Run: run, Model: deepResearch},
	})
	require.NoError(t, err)
	return out
}

// Every read of a finished interaction repeats the run's full usage. However many reads see it
// finished, and on whichever node each lands, the run is billed exactly once: one aggregate row,
// one governance report. This is what the per-process set it replaces could not promise across
// nodes or restarts.
func TestAccountInteractionJob_BillsARunOnceAcrossReadsAndNodes(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	job := interactionJob(t, "int_once")
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))
	run := finishedRun("int_once", "completed", 361700, 33400)

	first := accountInteractionInline(t, store, reporter, "node-a", run)
	require.NotNil(t, first)
	assert.True(t, first.Accounted)
	assert.InDelta(t, runCost(361700, 33400), first.Cost, 1e-12)

	for _, node := range []string{"node-a", "node-b", "node-c"} {
		again := accountInteractionInline(t, store, reporter, node, run)
		require.NotNil(t, again)
		assert.False(t, again.Accounted, "a re-read on %s must not settle the run again", node)
	}

	logID := AccountingLogID(ProviderJobKindInteraction, schemas.Gemini, "int_once")
	require.Len(t, store.logs, 1)
	entry := store.logs[logID]
	require.NotNil(t, entry, "the aggregate row is keyed by the interaction namespace")
	require.NotNil(t, entry.Cost)
	assert.InDelta(t, runCost(361700, 33400), *entry.Cost, 1e-12)
	assert.Equal(t, deepResearch, entry.Model)
	assert.Equal(t, string(schemas.ResponsesRequest), entry.Object, "the generic recalculation reprices it as the Responses row it is")
	assert.Equal(t, 361700, entry.PromptTokens)
	require.NotNil(t, entry.ParentRequestID)
	assert.Equal(t, "req-create-int_once", *entry.ParentRequestID, "the bill sits under the create, whoever read the run first")
	assert.Equal(t, "key-creator", entry.SelectedKeyID)

	require.Len(t, reporter.reports, 1, "governance is told exactly once")
	assert.Equal(t, logID, reporter.reports[0].RequestID)
	assert.Equal(t, int64(361700+33400), reporter.reports[0].TokensUsed)
	assert.True(t, reporter.reports[0].SkipRequestCount, "the create already counted as the run's request")
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, store.jobs[job.ID].AccountingStatus)
}

// A poll of a run still in progress reports partial usage that the final read reports in full.
func TestAccountInteractionJob_LeavesARunningRunAlone(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	require.NoError(t, store.UpsertProviderJob(context.Background(), interactionJob(t, "int_running")))

	run := finishedRun("int_running", "in_progress", 100, 1)
	run.Terminal = false
	assert.Nil(t, accountInteractionInline(t, store, reporter, "node-a", run))
	assert.Empty(t, store.logs)
	assert.Empty(t, reporter.reports)
}

// A run Bifrost never saw created could belong to anyone holding the provider key; polling it is
// not grounds to bill the caller.
func TestAccountInteractionJob_RefusesARunWeNeverSawCreated(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	assert.Nil(t, accountInteractionInline(t, store, reporter, "node-a", finishedRun("int_foreign", "completed", 10, 5)))
	assert.Empty(t, store.logs)
	assert.Empty(t, reporter.reports)
}

func settleInteraction(t *testing.T, payload InteractionPayload) *Settlement {
	t.Helper()
	job := interactionJob(t, "int_settle")
	settlement, err := NewInteractionSettler(nil).Settle(context.Background(), fakePricing{}, JobRequest{
		Provider:      schemas.Gemini,
		ProviderJobID: job.JobID,
		FallbackModel: job.Model,
		Job:           job,
		Payload:       payload,
	})
	require.NoError(t, err)
	require.NotNil(t, settlement)
	return settlement
}

func TestInteractionSettle(t *testing.T) {
	t.Run("a completed run prices its usage at the agent, on the standard rate", func(t *testing.T) {
		s := settleInteraction(t, InteractionPayload{Run: finishedRun("int_settle", "completed", 1000, 50)})
		assert.True(t, s.Priced)
		assert.True(t, s.Complete)
		assert.InDelta(t, runCost(1000, 50), s.Cost, 1e-12, "the job model names the agent when the read named none")
		assert.Equal(t, deepResearch, s.Model)
		assert.Equal(t, 1050, s.Usage.TotalTokens)
	})
	t.Run("the served tier rides on the row so a reprice charges the same tier", func(t *testing.T) {
		run := finishedRun("int_settle", "completed", 1000, 50)
		tier := schemas.BifrostServiceTier("priority")
		run.Usage.ServiceTier = &tier
		s := settleInteraction(t, InteractionPayload{Run: run, Model: deepResearch})
		require.NotNil(t, s.ApplyDebug)
		entry := buildAggregateLog(JobRequest{Provider: schemas.Gemini}, s, &Outcome{}, time.Now(), "")
		require.NotNil(t, entry.ServiceTier)
		assert.Equal(t, "priority", *entry.ServiceTier)
	})
	t.Run("a run that failed or was cancelled before doing work owes nothing", func(t *testing.T) {
		for _, status := range []string{"failed", "cancelled"} {
			s := settleInteraction(t, InteractionPayload{Run: finishedRun("int_settle", status, 0, 0)})
			assert.True(t, s.Priced, status)
			assert.True(t, s.Complete, status)
			assert.Zero(t, s.Cost, status)
		}
	})
	t.Run("a completed run that reports no usage is unpriceable, not free", func(t *testing.T) {
		s := settleInteraction(t, InteractionPayload{Run: finishedRun("int_settle", "completed", 0, 0)})
		assert.False(t, s.Priced)
		assert.Equal(t, UnpriceableReasonInteractionNoUsage, s.UnpriceableReason)
	})
	t.Run("an agent with no rate keeps the row with its usage for the backfill", func(t *testing.T) {
		s := settleInteraction(t, InteractionPayload{Run: finishedRun("int_settle", "completed", 1000, 50), Model: "unpriced-agent"})
		assert.False(t, s.Priced)
		assert.True(t, s.RecordUnpriced)
		assert.Equal(t, UnpriceableReasonMissingInteractionPricing, s.UnpriceableReason)
		assert.Equal(t, "unpriced-agent", s.UnpricedModel)
		assert.Equal(t, 1050, s.Usage.TotalTokens)
	})
}

// The sweeper settles the runs no client read to the end: a background Deep Research run the caller
// stopped polling, or a stream that closed before the run finished.
func TestInteractionSweeper_SettlesARunNobodyPolledToTheEnd(t *testing.T) {
	store := newFakeAccountingStore()
	due := time.Now().UTC().Add(-time.Minute)
	job := interactionJob(t, "int_sweep")
	job.NextCheckAt = &due
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))

	running := &schemas.BifrostPassthroughJob{ID: "int_sweep", Status: "in_progress"}
	poller := &fakeInteractionPoller{resp: &schemas.BifrostPassthroughResponse{StatusCode: 200, ProviderJob: running, ServedModel: deepResearch}}
	reporter := &fakeUsageReporter{}
	sweeper := NewInteractionSweeper(store, store, fakePricing{}, poller, nil, reporter, SweeperConfig{Provider: schemas.Gemini, Limit: 10})

	sweeper.SweepOnce(context.Background())
	assert.Equal(t, 1, poller.calls)
	assert.Empty(t, store.logs, "a running run is not settled")
	rescheduled := store.jobs[job.ID]
	require.NotNil(t, rescheduled.NextCheckAt)
	assert.True(t, rescheduled.NextCheckAt.After(time.Now().UTC()), "the running run is polled again later")

	// Make it due again and let the run finish.
	rescheduled.NextCheckAt = &due
	poller.resp = &schemas.BifrostPassthroughResponse{StatusCode: 200, ProviderJob: finishedRun("int_sweep", "completed", 1000, 50), ServedModel: deepResearch}
	sweeper.SweepOnce(context.Background())
	require.Len(t, store.logs, 1)
	entry := store.logs[AccountingLogID(ProviderJobKindInteraction, schemas.Gemini, "int_sweep")]
	require.NotNil(t, entry)
	require.NotNil(t, entry.Cost)
	assert.InDelta(t, runCost(1000, 50), *entry.Cost, 1e-12)
	require.Len(t, reporter.reports, 1)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, store.jobs[job.ID].AccountingStatus)

	// Settlement is at-least-once by design, so another sweep must be inert.
	sweeper.SweepOnce(context.Background())
	assert.Len(t, store.logs, 1)
	assert.Len(t, reporter.reports, 1)
}

// A run the provider no longer knows (deleted, or stored only briefly) parks instead of burning its
// poll attempts, and a key that was revoked is told apart from it.
func TestInteractionSettlerPoll_ClassifiesUnreachableRuns(t *testing.T) {
	cases := map[int]string{
		404: UnpriceableReasonInteractionGone,
		403: UnpriceableReasonInteractionAccessDenied,
		400: UnpriceableReasonInteractionRejected,
	}
	for status, reason := range cases {
		poller := &fakeInteractionPoller{err: &ProviderCallError{StatusCode: status, Message: "upstream"}}
		poll, err := NewInteractionSettler(poller).Poll(context.Background(), interactionJob(t, "int_gone"))
		require.NoError(t, err, status)
		require.NotNil(t, poll, status)
		assert.True(t, poll.Terminal, status)
		assert.False(t, poll.Settleable, status)
		assert.Equal(t, reason, poll.UnpriceableReason, status)
	}

	poller := &fakeInteractionPoller{err: &ProviderCallError{StatusCode: 503, Message: "unavailable"}}
	_, err := NewInteractionSettler(poller).Poll(context.Background(), interactionJob(t, "int_retry"))
	assert.Error(t, err, "a 5xx is retried, not parked")
}

func TestInteractionSettlerPoll_RejectsAMismatchedRun(t *testing.T) {
	poller := &fakeInteractionPoller{resp: &schemas.BifrostPassthroughResponse{ProviderJob: finishedRun("int_other", "completed", 1, 1)}}
	_, err := NewInteractionSettler(poller).Poll(context.Background(), interactionJob(t, "int_mine"))
	assert.Error(t, err)
}

func TestInteractionParamsRoundTrip(t *testing.T) {
	assert.Equal(t, "v1beta", InteractionParamsFromJob(interactionJob(t, "int_params")).APIVersion)
	garbage := "{not json"
	assert.Equal(t, InteractionJobParams{}, InteractionParamsFromJob(&cstables.TableProviderJob{Params: &garbage}),
		"an unreadable blob polls at the default version rather than never settling")
}

func TestInteractionSettlerBackoffStretchesWithTheRun(t *testing.T) {
	s := NewInteractionSettler(nil)
	assert.Equal(t, time.Minute, s.Backoff(0, time.Minute))
	assert.Equal(t, 2*time.Minute, s.Backoff(10, time.Minute))
	assert.Equal(t, 5*time.Minute, s.Backoff(30, time.Minute))
	assert.Equal(t, 10*time.Minute, s.Backoff(60, time.Minute))
}
