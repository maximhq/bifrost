package jobaccounting

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
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

const backgroundModel = "o4-mini-deep-research"

func responseCost(input, output int) float64 {
	return float64(input)*0.000002 + float64(output)*0.000008
}

type fakeResponsePoller struct {
	resp  *schemas.BifrostResponsesResponse
	err   error
	calls int
}

func (f *fakeResponsePoller) RetrieveResponse(ctx context.Context, job *cstables.TableProviderJob) (*schemas.BifrostResponsesResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// responseJob builds the row a background create leaves behind: the response, the model it named,
// and who created it.
func responseJob(id string) *cstables.TableProviderJob {
	vk := "vk-creator"
	source := "req-create-" + id
	budgets := `["budget-creator"]`
	rateLimits := `["rl-creator"]`
	return &cstables.TableProviderJob{
		ID:               cstables.ProviderJobID(cstables.ProviderJobKindResponse, string(schemas.OpenAI), id),
		Kind:             cstables.ProviderJobKindResponse,
		Provider:         string(schemas.OpenAI),
		JobID:            id,
		Model:            backgroundModel,
		ProviderStatus:   "queued",
		AccountingStatus: cstables.ProviderJobAccountingStatusPending,
		SelectedKeyID:    "key-creator",
		VirtualKeyID:     &vk,
		BudgetIDs:        &budgets,
		RateLimitIDs:     &rateLimits,
		SourceLogID:      &source,
	}
}

// storedResponse is a response as a retrieve returns it. Zero tokens mean the object carries no
// usage, which is what a queued or in-progress response looks like.
func storedResponse(id, status string, input, output int) *schemas.BifrostResponsesResponse {
	resp := &schemas.BifrostResponsesResponse{ID: &id, Status: &status, Model: backgroundModel + "-2025-06-26", Background: new(true)}
	if input > 0 || output > 0 {
		resp.Usage = &schemas.ResponsesResponseUsage{InputTokens: input, OutputTokens: output, TotalTokens: input + output}
	}
	return resp
}

func accountResponseInline(t *testing.T, store *fakeAccountingStore, reporter *fakeUsageReporter, runner string, resp *schemas.BifrostResponsesResponse) *Outcome {
	t.Helper()
	out, err := AccountResponseJob(context.Background(), store, store, fakePricing{}, JobRequest{
		Provider:      schemas.OpenAI,
		ProviderJobID: *resp.ID,
		UsageReporter: reporter,
		ClaimedBy:     runner,
		Payload:       ResponsePayload{Response: resp},
	})
	require.NoError(t, err)
	return out
}

// A background response reports its usage on every retrieve once it has finished. However many
// polls see it finished, and on whichever node each lands, it is charged once: one cost row under
// the create, one usage report against the limits the create was admitted under.
func TestAccountResponseJob_ChargesABackgroundResponseOnceAcrossPollsAndNodes(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	job := responseJob("resp_once")
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))
	finished := storedResponse("resp_once", "completed", 42000, 9000)

	first := accountResponseInline(t, store, reporter, "node-a", finished)
	require.NotNil(t, first)
	assert.True(t, first.Accounted)
	assert.InDelta(t, responseCost(42000, 9000), first.Cost, 1e-12)

	for _, node := range []string{"node-a", "node-b", "node-c"} {
		again := accountResponseInline(t, store, reporter, node, finished)
		assert.True(t, again == nil || !again.Accounted, "a later poll on %s must not charge the response again", node)
	}

	logID := AccountingLogID(ProviderJobKindResponse, schemas.OpenAI, "resp_once")
	require.Len(t, store.logs, 1)
	entry := store.logs[logID]
	require.NotNil(t, entry, "the cost row is keyed by the response namespace")
	require.NotNil(t, entry.Cost)
	assert.InDelta(t, responseCost(42000, 9000), *entry.Cost, 1e-12)
	assert.Equal(t, backgroundModel, entry.Model, "the row is priced at the model the create named")
	assert.Equal(t, string(schemas.ResponsesRequest), entry.Object)
	assert.Equal(t, 42000, entry.PromptTokens)
	assert.Equal(t, 9000, entry.CompletionTokens)
	require.NotNil(t, entry.ParentRequestID)
	assert.Equal(t, "req-create-resp_once", *entry.ParentRequestID, "the cost sits under the create, whoever polled first")
	assert.Equal(t, "key-creator", entry.SelectedKeyID)

	require.Len(t, reporter.reports, 1, "the limits are charged exactly once")
	report := reporter.reports[0]
	assert.Equal(t, logID, report.RequestID)
	assert.Equal(t, int64(51000), report.TokensUsed)
	assert.InDelta(t, responseCost(42000, 9000), report.Cost, 1e-12)
	assert.Equal(t, []string{"budget-creator"}, report.BudgetIDs)
	assert.Equal(t, []string{"rl-creator"}, report.RateLimitIDs)
	assert.Equal(t, "vk-creator", report.VirtualKeyID)
	assert.True(t, report.SkipRequestCount, "the create already counted as the request")
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, store.jobs[job.ID].AccountingStatus)
	assert.Equal(t, "completed", store.jobs[job.ID].ProviderStatus)
}

// A retrieve of a response that is still queued or running carries no final usage.
func TestAccountResponseJob_LeavesAnUnfinishedResponseAlone(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	job := responseJob("resp_running")
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))

	for _, status := range []string{"queued", "in_progress"} {
		assert.Nil(t, accountResponseInline(t, store, reporter, "node-a", storedResponse("resp_running", status, 0, 0)), status)
	}
	assert.Empty(t, store.logs)
	assert.Empty(t, reporter.reports)
	assert.Equal(t, cstables.ProviderJobAccountingStatusPending, store.jobs[job.ID].AccountingStatus)
}

// A retrieve never starts tracking: a response with no row was either charged from its own create
// or created somewhere else, and neither is grounds to charge whoever read it.
func TestAccountResponseJob_IgnoresAResponseWithNoJobRow(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	assert.Nil(t, accountResponseInline(t, store, reporter, "node-a", storedResponse("resp_unknown", "completed", 10, 5)))
	assert.Empty(t, store.jobs)
	assert.Empty(t, store.logs)
	assert.Empty(t, reporter.reports)
}

// A response that failed or was cancelled settles whatever its final object reports, which may be
// nothing, and stops there.
func TestAccountResponseJob_SettlesAFailedOrCancelledResponseAndStops(t *testing.T) {
	cases := []struct {
		id, status    string
		input, output int
	}{
		{"resp_cancelled", "cancelled", 0, 0},
		{"resp_failed", "failed", 0, 0},
		{"resp_failed_late", "failed", 3000, 100},
		{"resp_incomplete", "incomplete", 3000, 400},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			store := newFakeAccountingStore()
			reporter := &fakeUsageReporter{}
			job := responseJob(tc.id)
			due := time.Now().UTC().Add(time.Minute)
			job.NextCheckAt = &due
			require.NoError(t, store.UpsertProviderJob(context.Background(), job))
			final := storedResponse(tc.id, tc.status, tc.input, tc.output)

			out := accountResponseInline(t, store, reporter, "node-a", final)
			require.NotNil(t, out)
			assert.True(t, out.Accounted)
			assert.InDelta(t, responseCost(tc.input, tc.output), out.Cost, 1e-12)
			assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, store.jobs[job.ID].AccountingStatus)
			require.Len(t, reporter.reports, 1)
			assert.Equal(t, int64(tc.input+tc.output), reporter.reports[0].TokensUsed)

			again := accountResponseInline(t, store, reporter, "node-b", final)
			assert.True(t, again == nil || !again.Accounted)
			assert.Len(t, reporter.reports, 1)
			assert.Len(t, store.logs, 1)

			jobs, err := store.ListDueProviderJobs(context.Background(), cstables.ProviderJobKindResponse, "", time.Now().UTC().Add(time.Hour), 10)
			require.NoError(t, err)
			assert.Empty(t, jobs, "a settled response is never swept again")
		})
	}
}

func settleResponse(t *testing.T, job *cstables.TableProviderJob, resp *schemas.BifrostResponsesResponse) *Settlement {
	t.Helper()
	settlement, err := NewResponseSettler(nil).Settle(context.Background(), fakePricing{}, JobRequest{
		Provider:      schemas.OpenAI,
		ProviderJobID: job.JobID,
		FallbackModel: job.Model,
		Job:           job,
		Payload:       ResponsePayload{Response: resp},
	})
	require.NoError(t, err)
	require.NotNil(t, settlement)
	return settlement
}

func TestResponseSettle(t *testing.T) {
	t.Run("a completed response prices its usage at the model the create named", func(t *testing.T) {
		resp := storedResponse("resp_settle", "completed", 1000, 50)
		s := settleResponse(t, responseJob("resp_settle"), resp)
		assert.True(t, s.Priced)
		assert.True(t, s.Complete)
		assert.InDelta(t, responseCost(1000, 50), s.Cost, 1e-12)
		assert.Equal(t, backgroundModel, s.Model)
		assert.Equal(t, 1050, s.Usage.TotalTokens)
		assert.True(t, s.SkipRequestCount)
		assert.Equal(t, schemas.RequestType(""), resp.ExtraFields.RequestType, "pricing must not rewrite the response the caller is about to receive")
	})
	t.Run("a row with no model falls back to the model the response reports", func(t *testing.T) {
		job := responseJob("resp_settle")
		job.Model = ""
		resp := storedResponse("resp_settle", "completed", 1000, 50)
		resp.Model = backgroundModel
		s := settleResponse(t, job, resp)
		assert.True(t, s.Priced)
		assert.Equal(t, backgroundModel, s.Model)
	})
	t.Run("the served tier rides on the row so a reprice charges the same tier", func(t *testing.T) {
		resp := storedResponse("resp_settle", "completed", 1000, 50)
		tier := schemas.BifrostServiceTier("flex")
		resp.ServiceTier = &tier
		s := settleResponse(t, responseJob("resp_settle"), resp)
		require.NotNil(t, s.ApplyDebug)
		entry := buildAggregateLog(JobRequest{Provider: schemas.OpenAI}, s, &Outcome{}, time.Now(), "")
		require.NotNil(t, entry.ServiceTier)
		assert.Equal(t, "flex", *entry.ServiceTier)
	})
	t.Run("a completed response that reports no usage is unpriceable, not free", func(t *testing.T) {
		s := settleResponse(t, responseJob("resp_settle"), storedResponse("resp_settle", "completed", 0, 0))
		assert.False(t, s.Priced)
		assert.Equal(t, UnpriceableReasonResponseNoUsage, s.UnpriceableReason)
	})
	t.Run("a model with no rate keeps the row with its usage for the backfill", func(t *testing.T) {
		job := responseJob("resp_settle")
		job.Model = "unpriced-model"
		s := settleResponse(t, job, storedResponse("resp_settle", "completed", 1000, 50))
		assert.False(t, s.Priced)
		assert.True(t, s.RecordUnpriced)
		assert.Equal(t, UnpriceableReasonMissingResponsePricing, s.UnpriceableReason)
		assert.Equal(t, "unpriced-model", s.UnpricedModel)
		assert.Equal(t, 1050, s.Usage.TotalTokens)
	})
}

// The sweeper settles the background responses nobody polled to the end, reading each with the key
// that created it.
func TestResponseSweeper_SettlesAResponseNobodyPolledToTheEnd(t *testing.T) {
	store := newFakeAccountingStore()
	due := time.Now().UTC().Add(-time.Minute)
	job := responseJob("resp_sweep")
	job.NextCheckAt = &due
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))

	poller := &fakeResponsePoller{resp: storedResponse("resp_sweep", "in_progress", 0, 0)}
	reporter := &fakeUsageReporter{}
	sweeper := NewResponseSweeper(store, store, fakePricing{}, poller, nil, reporter, SweeperConfig{Limit: 10})

	sweeper.SweepOnce(context.Background())
	assert.Equal(t, 1, poller.calls)
	assert.Empty(t, store.logs, "a running response is not settled")
	assert.Empty(t, reporter.reports)
	rescheduled := store.jobs[job.ID]
	assert.Equal(t, "in_progress", rescheduled.ProviderStatus)
	assert.Equal(t, 1, rescheduled.PollAttempts)
	require.NotNil(t, rescheduled.NextCheckAt)
	assert.True(t, rescheduled.NextCheckAt.After(time.Now().UTC()), "the running response is read again later")

	rescheduled.NextCheckAt = &due
	poller.resp = storedResponse("resp_sweep", "completed", 1000, 50)
	sweeper.SweepOnce(context.Background())
	require.Len(t, store.logs, 1)
	entry := store.logs[AccountingLogID(ProviderJobKindResponse, schemas.OpenAI, "resp_sweep")]
	require.NotNil(t, entry)
	require.NotNil(t, entry.Cost)
	assert.InDelta(t, responseCost(1000, 50), *entry.Cost, 1e-12)
	require.Len(t, reporter.reports, 1)
	assert.Equal(t, []string{"budget-creator"}, reporter.reports[0].BudgetIDs)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, store.jobs[job.ID].AccountingStatus)

	sweeper.SweepOnce(context.Background())
	assert.Equal(t, 2, poller.calls, "a settled response is not read again")
	assert.Len(t, store.logs, 1)
	assert.Len(t, reporter.reports, 1)
}

// A response nobody polls and that never finishes is given up on after the same number of reads as
// every other kind, so its row does not stay due forever.
func TestResponseSweeper_GivesUpAfterTheSharedAttemptCap(t *testing.T) {
	store := newFakeAccountingStore()
	due := time.Now().UTC().Add(-time.Minute)
	job := responseJob("resp_stuck")
	job.NextCheckAt = &due
	job.PollAttempts = MaxPollAttempts - 1
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))

	poller := &fakeResponsePoller{resp: storedResponse("resp_stuck", "in_progress", 0, 0)}
	NewResponseSweeper(store, store, fakePricing{}, poller, nil, nil, SweeperConfig{Limit: 10}).SweepOnce(context.Background())

	parked := store.jobs[job.ID]
	assert.Equal(t, cstables.ProviderJobAccountingStatusUnpriceable, parked.AccountingStatus)
	require.NotNil(t, parked.UnpriceableReason)
	assert.Equal(t, UnpriceableReasonMaxPollAttempts, *parked.UnpriceableReason)
}

// A response the provider no longer knows (deleted before it was read again) parks instead of
// using up its reads, and a key that was removed is told apart from it.
func TestResponseSettlerPoll_ClassifiesUnreachableResponses(t *testing.T) {
	cases := map[int]string{
		404: UnpriceableReasonResponseGone,
		401: UnpriceableReasonResponseAccessDenied,
		405: UnpriceableReasonResponseRejected,
	}
	for status, reason := range cases {
		poller := &fakeResponsePoller{err: &ProviderCallError{StatusCode: status, Message: "upstream"}}
		poll, err := NewResponseSettler(poller).Poll(context.Background(), responseJob("resp_gone"))
		require.NoError(t, err, status)
		require.NotNil(t, poll, status)
		assert.True(t, poll.Terminal, status)
		assert.False(t, poll.Settleable, status)
		assert.Equal(t, reason, poll.UnpriceableReason, status)
	}

	poller := &fakeResponsePoller{err: &ProviderCallError{StatusCode: 503, Message: "unavailable"}}
	_, err := NewResponseSettler(poller).Poll(context.Background(), responseJob("resp_retry"))
	assert.Error(t, err, "a 5xx is retried, not parked")

	poller = &fakeResponsePoller{resp: storedResponse("resp_other", "completed", 1, 1)}
	_, err = NewResponseSettler(poller).Poll(context.Background(), responseJob("resp_mine"))
	assert.Error(t, err, "a read that returns another response settles nothing")
}

func TestResponseStatusClassification(t *testing.T) {
	for _, status := range []string{"completed", "failed", "incomplete", "cancelled"} {
		assert.True(t, IsTerminalResponseStatus(status), status)
		assert.False(t, IsPendingResponseStatus(status), status)
	}
	for _, status := range []string{"queued", "in_progress"} {
		assert.False(t, IsTerminalResponseStatus(status), status)
		assert.True(t, IsPendingResponseStatus(status), status)
	}
	assert.False(t, IsTerminalResponseStatus(""))
	assert.False(t, IsPendingResponseStatus(""), "a response that reports no status is not tracked")
}

// recordingResponsePricing prices a background response like fakePricing, split into input and
// output, and keeps what it was asked to price with.
type recordingResponsePricing struct {
	fakePricing
	scopes    *modelcatalog.PricingLookupScopes
	toolUsage *schemas.ToolUsage
	routing   schemas.RoutingInfo
}

func (r *recordingResponsePricing) CalculateCostBreakdown(result *schemas.BifrostResponse, scopes *modelcatalog.PricingLookupScopes) *schemas.BifrostCost {
	if result == nil || result.ResponsesResponse == nil || result.ResponsesResponse.Usage == nil {
		return nil
	}
	if scopes != nil {
		copied := *scopes
		r.scopes = &copied
	}
	usage := result.ResponsesResponse.Usage
	r.toolUsage = usage.ToolUsage
	r.routing = result.ResponsesResponse.ExtraFields.RoutingInfo
	in, out := float64(usage.InputTokens)*0.000002, float64(usage.OutputTokens)*0.000008
	return &schemas.BifrostCost{InputCost: in, OutputCost: out, TotalCost: in + out}
}

func accountResponseWith(t *testing.T, store *fakeAccountingStore, pricing PricingManager, reporter *fakeUsageReporter, resp *schemas.BifrostResponsesResponse) *Outcome {
	t.Helper()
	out, err := AccountResponseJob(context.Background(), store, store, pricing, JobRequest{
		Provider:      schemas.OpenAI,
		ProviderJobID: *resp.ID,
		UsageReporter: reporter,
		ClaimedBy:     "node-a",
		Payload:       ResponsePayload{Response: resp},
	})
	require.NoError(t, err)
	return out
}

// A background response is priced the way its create would have been: under the user, virtual
// key and provider key that created it, whoever reads it when it finishes. A deployment with
// prices set for one user charges that user's rate.
func TestAccountResponseJob_PricesUnderTheCreatorsUser(t *testing.T) {
	store := newFakeAccountingStore()
	job := responseJob("resp_user")
	user := "user-creator"
	job.UserID = &user
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))
	pricing := &recordingResponsePricing{}

	out := accountResponseWith(t, store, pricing, &fakeUsageReporter{}, storedResponse("resp_user", "completed", 100, 50))
	require.NotNil(t, out)
	require.True(t, out.Accounted)
	require.NotNil(t, pricing.scopes)
	assert.Equal(t, "user-creator", pricing.scopes.UserID)
	assert.Equal(t, "vk-creator", pricing.scopes.VirtualKeyID)
	assert.Equal(t, "key-creator", pricing.scopes.SelectedKeyID)
}

// The settled row keeps the input and output split of the cost, like a row written by an
// ordinary create, so the two do not report the same work differently.
func TestAccountResponseJob_KeepsTheInputAndOutputSplit(t *testing.T) {
	store := newFakeAccountingStore()
	job := responseJob("resp_split")
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))
	finished := storedResponse("resp_split", "completed", 42000, 9000)
	// A cost the provider put on the usage is not the one recorded.
	finished.Usage.Cost = &schemas.BifrostCost{TotalCost: 99}

	out := accountResponseWith(t, store, &recordingResponsePricing{}, &fakeUsageReporter{}, finished)
	require.NotNil(t, out)
	require.True(t, out.Accounted)
	entry := store.logs[AccountingLogID(ProviderJobKindResponse, schemas.OpenAI, "resp_split")]
	require.NotNil(t, entry)
	require.NotNil(t, entry.Cost)
	assert.InDelta(t, responseCost(42000, 9000), *entry.Cost, 1e-12)
	require.NotNil(t, entry.TokenUsageParsed)
	require.NotNil(t, entry.TokenUsageParsed.Cost, "the row carries the split, not only the total")
	assert.InDelta(t, 42000*0.000002, entry.TokenUsageParsed.Cost.InputCost, 1e-12)
	assert.InDelta(t, 9000*0.000008, entry.TokenUsageParsed.Cost.OutputCost, 1e-12)
	assert.InDelta(t, responseCost(42000, 9000), entry.TokenUsageParsed.Cost.TotalCost, 1e-12)
	assert.InDelta(t, 99, finished.Usage.Cost.TotalCost, 1e-12, "the caller's response is not changed")
}

// The sweeper stops reading a response after its poll attempts run out and parks the job. A
// client that reads the response later, once it has finished, still settles it: that read holds
// the final usage, and nothing else would ever charge it. A job parked for any other reason
// stays parked.
func TestAccountResponseJob_SettlesAResponseParkedForMaxPollAttempts(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	job := responseJob("resp_parked")
	job.AccountingStatus = cstables.ProviderJobAccountingStatusUnpriceable
	parked := UnpriceableReasonMaxPollAttempts
	job.UnpriceableReason = &parked
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))
	finished := storedResponse("resp_parked", "completed", 42000, 9000)

	first := accountResponseInline(t, store, reporter, "node-a", finished)
	require.NotNil(t, first)
	assert.True(t, first.Accounted)
	again := accountResponseInline(t, store, reporter, "node-b", finished)
	assert.True(t, again == nil || !again.Accounted, "a second read does not charge it again")
	require.Len(t, store.logs, 1)
	require.Len(t, reporter.reports, 1)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, store.jobs[job.ID].AccountingStatus)

	gone := responseJob("resp_gone")
	gone.AccountingStatus = cstables.ProviderJobAccountingStatusUnpriceable
	reason := UnpriceableReasonResponseGone
	gone.UnpriceableReason = &reason
	require.NoError(t, store.UpsertProviderJob(context.Background(), gone))
	out := accountResponseInline(t, store, reporter, "node-a", storedResponse("resp_gone", "completed", 10, 10))
	assert.True(t, out == nil || !out.Accounted, "a job parked for another reason stays parked")
	require.Len(t, reporter.reports, 1)
}

// What a tool costs depends on the request as well as the response: which image model the
// image tool was given, and whether a code container was started for this response. The create
// saves those settings on the job, and settlement restores them before pricing, because the
// read that finds the response finished carries no request.
func TestAccountResponseJob_RestoresToolPricingDetailsSavedAtTheCreate(t *testing.T) {
	store := newFakeAccountingStore()
	job := responseJob("resp_tools")
	params := `{"tools":[{"type":"image_generation","model":"gpt-image-1-mini"},{"type":"code_interpreter","container":{"type":"auto"}}]}`
	job.Params = &params
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))

	finished := storedResponse("resp_tools", "completed", 1000, 500)
	finished.Usage.ToolUsage = &schemas.ToolUsage{ImageGen: &schemas.ImageGenToolUsage{InputTokens: 10, OutputTokens: 200, TotalTokens: 210}}
	callType := schemas.ResponsesMessageTypeCodeInterpreterCall
	finished.Output = []schemas.ResponsesMessage{{Type: &callType, ResponsesToolMessage: &schemas.ResponsesToolMessage{
		ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{ContainerID: "cntr_new"},
	}}}
	pricing := &recordingResponsePricing{}

	out := accountResponseWith(t, store, pricing, &fakeUsageReporter{}, finished)
	require.NotNil(t, out)
	require.True(t, out.Accounted)
	require.NotNil(t, pricing.toolUsage)
	assert.Equal(t, "gpt-image-1-mini", pricing.toolUsage.ImageGenModel, "the image tool is priced at the model the request gave it")
	assert.Equal(t, map[string]int{schemas.ContainerTierDefault: 1}, pricing.toolUsage.ContainerSessions, "the container this response started is charged")
	assert.Empty(t, finished.Usage.ToolUsage.ImageGenModel, "the caller's response is not changed")
	assert.Nil(t, finished.Usage.ToolUsage.ContainerSessions, "the caller's response is not changed")

	// The cost row keeps them too. Recalculating costs later prices the row from its own usage,
	// and without them the container fee and the chosen image model would be lost then.
	entry := store.logs[AccountingLogID(ProviderJobKindResponse, schemas.OpenAI, "resp_tools")]
	require.NotNil(t, entry)
	require.NotNil(t, entry.TokenUsageParsed)
	require.NotNil(t, entry.TokenUsageParsed.ToolUsage, "the saved usage carries the tool details")
	assert.Equal(t, "gpt-image-1-mini", entry.TokenUsageParsed.ToolUsage.ImageGenModel)
	assert.Equal(t, map[string]int{schemas.ContainerTierDefault: 1}, entry.TokenUsageParsed.ToolUsage.ContainerSessions)
}

// unpricedResponsePricing has no rate for anything, as a catalog missing the model or the
// container price would.
type unpricedResponsePricing struct{ fakePricing }

func (unpricedResponsePricing) CalculateCostBreakdown(*schemas.BifrostResponse, *modelcatalog.PricingLookupScopes) *schemas.BifrostCost {
	return nil
}

// A response that counts no tokens can still owe money: a code container session is a flat fee,
// whatever the model consumed. When that cannot be priced the job is parked as unpriced, with its
// row written, so it stays visible and the missing rate can be filled in later. Only a response
// with no tokens and no billable tool usage is settled as free.
func TestAccountResponseJob_ZeroTokensWithBillableToolUsageIsNotFree(t *testing.T) {
	callType := schemas.ResponsesMessageTypeCodeInterpreterCall
	zeroTokens := func(id string, withContainer bool) *schemas.BifrostResponsesResponse {
		status := "completed"
		resp := &schemas.BifrostResponsesResponse{ID: &id, Status: &status, Model: backgroundModel, Background: new(true), Usage: &schemas.ResponsesResponseUsage{}}
		if withContainer {
			resp.Output = []schemas.ResponsesMessage{{Type: &callType, ResponsesToolMessage: &schemas.ResponsesToolMessage{
				ResponsesCodeInterpreterToolCall: &schemas.ResponsesCodeInterpreterToolCall{ContainerID: "cntr_new"},
			}}}
		}
		return resp
	}

	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	job := responseJob("resp_container_only")
	params := `{"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`
	job.Params = &params
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))

	out := accountResponseWith(t, store, unpricedResponsePricing{}, reporter, zeroTokens("resp_container_only", true))
	assert.True(t, out == nil || !out.Accounted, "a container session with no rate is not a free response")
	assert.Equal(t, cstables.ProviderJobAccountingStatusUnpriceable, store.jobs[job.ID].AccountingStatus)
	require.NotNil(t, store.jobs[job.ID].UnpriceableReason)
	assert.Equal(t, UnpriceableReasonMissingResponsePricing, *store.jobs[job.ID].UnpriceableReason)
	assert.Empty(t, reporter.reports)

	plain := responseJob("resp_nothing_used")
	require.NoError(t, store.UpsertProviderJob(context.Background(), plain))
	out = accountResponseWith(t, store, unpricedResponsePricing{}, reporter, zeroTokens("resp_nothing_used", false))
	require.NotNil(t, out)
	assert.True(t, out.Accounted, "no tokens and no tool usage owes nothing")
	assert.Zero(t, out.Cost)
}

// A key can map an alias to a wire model that the catalog does not know by that name, and tag
// the alias with the canonical model to price it at. An ordinary create is priced through that
// alias. The create saves it on the job, and settlement prices through it too, so a background
// response is charged at the same rate and is not left unpriced.
func TestAccountResponseJob_PricesThroughTheAliasTheCreateResolved(t *testing.T) {
	store := newFakeAccountingStore()
	job := responseJob("resp_alias")
	job.Model = "deployment-7f3a"
	params := `{"routing":{"model":"team-default","resolved_key_alias":{"model_id":"deployment-7f3a","model_name":"gpt-4o-mini"}}}`
	job.Params = &params
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))
	pricing := &recordingResponsePricing{}

	out := accountResponseWith(t, store, pricing, &fakeUsageReporter{}, storedResponse("resp_alias", "completed", 100, 50))
	require.NotNil(t, out)
	require.True(t, out.Accounted)
	assert.Equal(t, "team-default", pricing.routing.Model, "the model the caller sent")
	require.NotNil(t, pricing.routing.ResolvedKeyAlias, "the alias the create resolved is restored")
	assert.Equal(t, "deployment-7f3a", pricing.routing.ResolvedKeyAlias.ModelID)
	require.NotNil(t, pricing.routing.ResolvedKeyAlias.ModelName)
	assert.Equal(t, "gpt-4o-mini", *pricing.routing.ResolvedKeyAlias.ModelName, "the canonical name pricing looks up first")

	// The cost row carries the alias too, from the job itself: this job's create log is not in the
	// store, and a later reprice of the row rebuilds the alias from these columns alone.
	entry := store.logs[AccountingLogID(ProviderJobKindResponse, schemas.OpenAI, "resp_alias")]
	require.NotNil(t, entry)
	require.NotNil(t, entry.Alias, "the row records the name the caller used")
	assert.Equal(t, "team-default", *entry.Alias)
	require.NotNil(t, entry.CanonicalModelName, "the row records the canonical name it was priced at")
	assert.Equal(t, "gpt-4o-mini", *entry.CanonicalModelName)

	// A job with no saved alias is priced at the model on the row, as before.
	plain := responseJob("resp_no_alias")
	require.NoError(t, store.UpsertProviderJob(context.Background(), plain))
	accountResponseWith(t, store, pricing, &fakeUsageReporter{}, storedResponse("resp_no_alias", "completed", 100, 50))
	assert.Equal(t, backgroundModel, pricing.routing.Model)
	assert.Nil(t, pricing.routing.ResolvedKeyAlias)
}

// A model with peak and off-peak rates is billed at the rate in force when the request started.
// A background response is settled later, by a poll or the sweeper, and must not be billed at
// the rate in force then. The job row's creation time is the create's, so pricing is told that.
func TestAccountResponseJob_BillsAtTheTimeOfTheCreate(t *testing.T) {
	store := newFakeAccountingStore()
	job := responseJob("resp_peak")
	created := time.Date(2026, 11, 20, 3, 15, 0, 0, time.UTC)
	job.CreatedAt = created
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))
	pricing := &recordingResponsePricing{}

	out := accountResponseWith(t, store, pricing, &fakeUsageReporter{}, storedResponse("resp_peak", "completed", 100, 50))
	require.NotNil(t, out)
	require.True(t, out.Accounted)
	require.NotNil(t, pricing.scopes)
	assert.True(t, pricing.scopes.BilledAt.Equal(created), "billed at %s, want the create's time %s", pricing.scopes.BilledAt, created)
}
