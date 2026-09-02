package jobaccounting

import (
	"context"
	"errors"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeVideoRetriever struct {
	resp  *schemas.BifrostVideoGenerationResponse
	err   error
	calls int
}

func (f *fakeVideoRetriever) RetrieveVideo(ctx context.Context, job *cstables.TableProviderJob) (*schemas.BifrostVideoGenerationResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// videoJob builds a submitted video job row carrying the dimensions the request
// asked for — the state recordVideoJobLifecycle leaves behind.
func videoJob(t *testing.T, videoID string, dims modelcatalog.VideoPricingDimensions) *cstables.TableProviderJob {
	t.Helper()
	params, err := MarshalVideoDimensions(dims)
	require.NoError(t, err)
	return &cstables.TableProviderJob{
		ID:               cstables.ProviderJobID(cstables.ProviderJobKindVideo, string(schemas.OpenAI), videoID),
		Kind:             cstables.ProviderJobKindVideo,
		Provider:         string(schemas.OpenAI),
		JobID:            videoID,
		Model:            dims.Model,
		Params:           params,
		AccountingStatus: cstables.ProviderJobAccountingStatusPending,
	}
}

func generationDims(model, size string, seconds int) modelcatalog.VideoPricingDimensions {
	return modelcatalog.VideoPricingDimensions{
		Model:       model,
		RequestType: schemas.VideoGenerationRequest,
		Seconds:     &seconds,
		Size:        size,
	}
}

func settleVideo(t *testing.T, job *cstables.TableProviderJob, resp *schemas.BifrostVideoGenerationResponse) *Settlement {
	t.Helper()
	settlement, err := NewVideoSettler(nil).Settle(context.Background(), fakePricing{}, JobRequest{
		Provider:      schemas.OpenAI,
		ProviderJobID: job.JobID,
		FallbackModel: job.Model,
		Job:           job,
		Payload:       VideoPayload{Response: resp},
	})
	require.NoError(t, err)
	require.NotNil(t, settlement)
	return settlement
}

func TestVideoDimensionsRoundTripThroughJobParams(t *testing.T) {
	audio := true
	dims := generationDims("sora-2-pro", "1920x1080", 8)
	dims.Audio = &audio
	dims.Extra = map[string]any{"sample_count": float64(2)}

	got := VideoDimensionsFromJob(videoJob(t, "vid_round", dims))
	assert.Equal(t, "sora-2-pro", got.Model)
	assert.Equal(t, "1920x1080", got.Size)
	require.NotNil(t, got.Seconds)
	assert.Equal(t, 8, *got.Seconds)
	require.NotNil(t, got.Audio)
	assert.True(t, *got.Audio)
	assert.Equal(t, float64(2), got.Extra["sample_count"],
		"the research hatch must survive the round trip even though nothing prices on it yet")
}

func TestVideoDimensionsFromJob_MalformedParamsDoNotBlockSettlement(t *testing.T) {
	// A params blob we cannot read must not strand the job: the response may still
	// carry enough to price it.
	garbage := "{not json"
	job := &cstables.TableProviderJob{Kind: cstables.ProviderJobKindVideo, Params: &garbage}
	assert.Equal(t, modelcatalog.VideoPricingDimensions{}, VideoDimensionsFromJob(job))
}

func TestVideoSettle_PricesFromCapturedParamsWhenResponseIsSilent(t *testing.T) {
	// The common provider: a retrieve that reports a status and nothing else.
	job := videoJob(t, "vid_silent", generationDims("sora-2-pro", "1920x1080", 8))
	settlement := settleVideo(t, job, &schemas.BifrostVideoGenerationResponse{
		Status: schemas.VideoStatusCompleted,
		Videos: []schemas.VideoOutput{{Type: schemas.VideoOutputTypeURL}},
	})

	assert.True(t, settlement.Priced)
	assert.True(t, settlement.Complete)
	assert.InDelta(t, 8*0.70, settlement.Cost, 1e-12,
		"the submitted request is the only place the duration and size still exist")
	assert.Equal(t, schemas.VideoGenerationRequest, settlement.Object)
}

func TestVideoSettle_ResponseOverridesCapturedParams(t *testing.T) {
	// The provider downscaled: the bill follows what was produced, not what was asked.
	job := videoJob(t, "vid_downscaled", generationDims("sora-2-pro", "1920x1080", 8))
	settlement := settleVideo(t, job, &schemas.BifrostVideoGenerationResponse{
		Status:  schemas.VideoStatusCompleted,
		Seconds: bifrost.Ptr("4"),
		Size:    "1280x720",
		Videos:  []schemas.VideoOutput{{Type: schemas.VideoOutputTypeURL}},
	})

	require.True(t, settlement.Priced)
	assert.InDelta(t, 4*0.30, settlement.Cost, 1e-12)
}

func TestVideoSettle_BillsEveryReturnedClip(t *testing.T) {
	job := videoJob(t, "vid_multi", generationDims("sora-2-pro", "1280x720", 8))
	settlement := settleVideo(t, job, &schemas.BifrostVideoGenerationResponse{
		Status: schemas.VideoStatusCompleted,
		Videos: []schemas.VideoOutput{
			{Type: schemas.VideoOutputTypeURL},
			{Type: schemas.VideoOutputTypeURL},
			{Type: schemas.VideoOutputTypeURL},
		},
	})

	require.True(t, settlement.Priced)
	assert.InDelta(t, 3*8*0.30, settlement.Cost, 1e-12)
}

func TestVideoSettle_ProviderReportedCostWins(t *testing.T) {
	job := videoJob(t, "vid_provider_cost", generationDims("sora-2-pro", "1920x1080", 8))
	settlement := settleVideo(t, job, &schemas.BifrostVideoGenerationResponse{
		Status: schemas.VideoStatusCompleted,
		Videos: []schemas.VideoOutput{{Type: schemas.VideoOutputTypeURL}},
		Usage:  &schemas.VideoUsage{Cost: &schemas.BifrostCost{TotalCost: 0.42}},
	})

	require.True(t, settlement.Priced)
	assert.InDelta(t, 0.42, settlement.Cost, 1e-12,
		"a provider that reports its own figure is never overridden by an estimate")
}

// A failed generation is a real price of zero, not an absence of one. Recording it
// as unpriceable would leave free work parked in the sweeper forever.
func TestVideoSettle_FailedJobIsPricedAtZero(t *testing.T) {
	job := videoJob(t, "vid_failed", generationDims("sora-2-pro", "1920x1080", 8))
	settlement := settleVideo(t, job, &schemas.BifrostVideoGenerationResponse{
		Status: schemas.VideoStatusFailed,
		Error:  &schemas.VideoCreateError{Code: "generation_failed"},
	})

	assert.True(t, settlement.Priced, "failure is a final answer about the money")
	assert.True(t, settlement.Complete)
	assert.Zero(t, settlement.Cost)
	assert.Empty(t, settlement.UnpriceableReason)
}

// Content filtering is the one case we refuse to guess: real compute was spent, and
// whether the provider bills for it is undocumented. Parking it keeps the job
// re-drivable instead of over- or under-charging.
func TestVideoSettle_ContentFilteredParksRatherThanGuesses(t *testing.T) {
	job := videoJob(t, "vid_filtered", generationDims("sora-2-pro", "1920x1080", 8))
	settlement := settleVideo(t, job, &schemas.BifrostVideoGenerationResponse{
		Status:        schemas.VideoStatusFailed,
		ContentFilter: &schemas.ContentFilterInfo{FilteredCount: 1, Reasons: []string{"safety"}},
	})

	assert.False(t, settlement.Priced)
	assert.Equal(t, UnpriceableReasonContentFiltered, settlement.UnpriceableReason)
	assert.Zero(t, settlement.Cost)
}

func TestVideoSettle_NoRateRecordsUnpricedRatherThanFree(t *testing.T) {
	job := videoJob(t, "vid_norate", generationDims("some-unknown-model", "1920x1080", 8))
	settlement := settleVideo(t, job, &schemas.BifrostVideoGenerationResponse{
		Status: schemas.VideoStatusCompleted,
		Videos: []schemas.VideoOutput{{Type: schemas.VideoOutputTypeURL}},
	})

	assert.False(t, settlement.Priced, "no rate must never read as free")
	assert.True(t, settlement.RecordUnpriced, "the row must stay visible so backfill can reprice it")
	assert.Equal(t, UnpriceableReasonMissingVideoPricing, settlement.UnpriceableReason)
	assert.Equal(t, "some-unknown-model", settlement.UnpricedModel)
}

func TestVideoPoll_InProgressJobIsNotSettleable(t *testing.T) {
	job := videoJob(t, "vid_running", generationDims("sora-2-pro", "1920x1080", 8))
	retriever := &fakeVideoRetriever{resp: &schemas.BifrostVideoGenerationResponse{
		ID:     "vid_running",
		Status: schemas.VideoStatusInProgress,
	}}

	poll, err := NewVideoSettler(retriever).Poll(context.Background(), job)
	require.NoError(t, err)
	require.NotNil(t, poll)
	assert.False(t, poll.Terminal)
	assert.False(t, poll.Settleable)
	require.NotNil(t, poll.Job)
	assert.Equal(t, string(schemas.VideoStatusInProgress), poll.Job.ProviderStatus)
}

func TestVideoPoll_MismatchedIDIsAnError(t *testing.T) {
	job := videoJob(t, "vid_expected", generationDims("sora-2-pro", "1920x1080", 8))
	retriever := &fakeVideoRetriever{resp: &schemas.BifrostVideoGenerationResponse{
		ID:     "vid_someone_elses",
		Status: schemas.VideoStatusCompleted,
	}}

	_, err := NewVideoSettler(retriever).Poll(context.Background(), job)
	require.Error(t, err, "settling another job's response would bill the wrong request")
}

func TestVideoPoll_RetrieveErrorReschedules(t *testing.T) {
	job := videoJob(t, "vid_err", generationDims("sora-2-pro", "1920x1080", 8))
	retriever := &fakeVideoRetriever{err: errors.New("upstream unavailable")}

	_, err := NewVideoSettler(retriever).Poll(context.Background(), job)
	require.Error(t, err)
}

// Video jobs finish in minutes; the batch ladder would leave a finished one
// unsettled for a quarter of an hour.
func TestVideoBackoffStaysInMinutes(t *testing.T) {
	settler := NewVideoSettler(nil)
	base := time.Minute
	assert.Equal(t, time.Minute, settler.Backoff(1, base))
	assert.Equal(t, 2*time.Minute, settler.Backoff(20, base))
	assert.Equal(t, 5*time.Minute, settler.Backoff(40, base))
	assert.Less(t, settler.Backoff(40, base), NewBatchSettler(nil).Backoff(40, base))
}

func TestVideoSettlerSupportsOnlyRealVideoProviders(t *testing.T) {
	settler := NewVideoSettler(nil)
	for _, provider := range []schemas.ModelProvider{schemas.OpenAI, schemas.Gemini, schemas.Vertex, schemas.Replicate, schemas.Runway, schemas.Runware} {
		assert.True(t, settler.SupportsProvider(provider), string(provider))
	}
	// Every provider implements the VideoGeneration method, so a real allowlist is
	// the only thing keeping the sweeper from burning attempts on providers that
	// merely return "unsupported".
	assert.False(t, settler.SupportsProvider(schemas.Anthropic))
	assert.False(t, settler.SupportsProvider(schemas.Cohere))
}

// The video namespace must never collide with batch: the id is both the aggregate
// write's idempotency key and the governance dedupe key.
func TestVideoAccountingLogIDIsDistinctFromBatch(t *testing.T) {
	video := AccountingLogID(ProviderJobKindVideo, schemas.OpenAI, "same_id")
	batch := AccountingLogID(ProviderJobKindBatch, schemas.OpenAI, "same_id")
	assert.NotEqual(t, video, batch)
	assert.Equal(t, video, AccountingLogID(ProviderJobKindVideo, schemas.OpenAI, "same_id"), "the id must be deterministic")
}

// End to end through the shared engine: the sweeper picks up a due video job,
// settles it exactly once, and reports it exactly once.
func TestVideoSweeper_SettlesCompletedJobOnce(t *testing.T) {
	store := newFakeAccountingStore()
	due := time.Now().UTC().Add(-time.Minute)
	job := videoJob(t, "vid_sweep", generationDims("sora-2-pro", "1920x1080", 8))
	job.NextCheckAt = &due
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))

	// A batch job sitting in the same table must be left entirely alone.
	batchDue := due
	require.NoError(t, store.UpsertProviderJob(context.Background(), &cstables.TableProviderJob{
		ID:               cstables.ProviderJobID(cstables.ProviderJobKindBatch, string(schemas.OpenAI), "batch_untouched"),
		Kind:             cstables.ProviderJobKindBatch,
		Provider:         string(schemas.OpenAI),
		JobID:            "batch_untouched",
		AccountingStatus: cstables.ProviderJobAccountingStatusPending,
		NextCheckAt:      &batchDue,
	}))

	retriever := &fakeVideoRetriever{resp: &schemas.BifrostVideoGenerationResponse{
		ID:     "vid_sweep",
		Status: schemas.VideoStatusCompleted,
		Videos: []schemas.VideoOutput{{Type: schemas.VideoOutputTypeURL}},
	}}
	reporter := &fakeUsageReporter{}
	sweeper := NewVideoSweeper(store, store, fakePricing{}, retriever, nil, reporter, SweeperConfig{
		Provider: schemas.OpenAI,
		Limit:    10,
	})

	sweeper.SweepOnce(context.Background())

	assert.Equal(t, 1, retriever.calls, "the video sweeper must not poll the batch row")
	settled := store.jobs[cstables.ProviderJobID(cstables.ProviderJobKindVideo, string(schemas.OpenAI), "vid_sweep")]
	require.NotNil(t, settled)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, settled.AccountingStatus)

	logID := AccountingLogID(ProviderJobKindVideo, schemas.OpenAI, "vid_sweep")
	entry := store.logs[logID]
	require.NotNil(t, entry, "the aggregate cost row must be keyed by the video namespace")
	require.NotNil(t, entry.Cost)
	assert.InDelta(t, 8*0.70, *entry.Cost, 1e-12)
	assert.Equal(t, string(schemas.VideoGenerationRequest), entry.Object)
	require.Len(t, reporter.reports, 1)
	assert.Equal(t, logID, reporter.reports[0].RequestID)

	untouched := store.jobs[cstables.ProviderJobID(cstables.ProviderJobKindBatch, string(schemas.OpenAI), "batch_untouched")]
	require.NotNil(t, untouched)
	assert.Equal(t, cstables.ProviderJobAccountingStatusPending, untouched.AccountingStatus,
		"a batch row must never be settled by the video sweeper")

	// Settlement is at-least-once by design, so a second sweep must be inert.
	sweeper.SweepOnce(context.Background())
	assert.Len(t, store.logs, 1)
	assert.Len(t, reporter.reports, 1, "governance must be told exactly once")
}

// --- Inline settlement on the retrieve path ---

func accountVideoInline(t *testing.T, store *fakeAccountingStore, reporter *fakeUsageReporter, videoID string, resp *schemas.BifrostVideoGenerationResponse) *Outcome {
	t.Helper()
	out, err := AccountVideoJob(context.Background(), store, store, fakePricing{}, JobRequest{
		Provider:      schemas.OpenAI,
		ProviderJobID: videoID,
		FallbackModel: "sora-2-pro",
		UsageReporter: reporter,
		ClaimedBy:     "logging-node",
		Payload:       VideoPayload{Response: resp},
	})
	require.NoError(t, err)
	return out
}

// A user polling GET /v1/videos/{id} has already fetched the terminal response.
// Settling from it saves a provider call and a full sweep interval of delay.
func TestAccountVideoJob_SettlesFromTheCallersTerminalResponse(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	job := videoJob(t, "vid_inline", generationDims("sora-2-pro", "1920x1080", 8))
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))

	out := accountVideoInline(t, store, reporter, "vid_inline", &schemas.BifrostVideoGenerationResponse{
		ID:     "vid_inline",
		Status: schemas.VideoStatusCompleted,
		Videos: []schemas.VideoOutput{{Type: schemas.VideoOutputTypeURL}},
	})

	require.NotNil(t, out)
	assert.True(t, out.Accounted)
	assert.InDelta(t, 8*0.70, out.Cost, 1e-12)

	settled := store.jobs[cstables.ProviderJobID(cstables.ProviderJobKindVideo, string(schemas.OpenAI), "vid_inline")]
	require.NotNil(t, settled)
	assert.Equal(t, cstables.ProviderJobAccountingStatusAccounted, settled.AccountingStatus)
	assert.Len(t, store.logs, 1)
	require.Len(t, reporter.reports, 1)
}

// A video submitted before this build was charged at submission. Settling it now,
// off a row we never wrote, would bill it a second time.
func TestAccountVideoJob_RefusesAJobWeNeverSawSubmitted(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}

	out := accountVideoInline(t, store, reporter, "vid_pre_upgrade", &schemas.BifrostVideoGenerationResponse{
		ID:     "vid_pre_upgrade",
		Status: schemas.VideoStatusCompleted,
		Videos: []schemas.VideoOutput{{Type: schemas.VideoOutputTypeURL}},
	})

	assert.Nil(t, out, "no coordination row means we never saw the submission")
	assert.Empty(t, store.logs, "nothing may be billed for a job we did not record")
	assert.Empty(t, reporter.reports)
}

// Clients poll a running job repeatedly; only a terminal response is settlement
// input.
func TestAccountVideoJob_IgnoresAStillRunningJob(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	job := videoJob(t, "vid_running_inline", generationDims("sora-2-pro", "1920x1080", 8))
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))

	for _, status := range []schemas.VideoStatus{schemas.VideoStatusQueued, schemas.VideoStatusInProgress} {
		out := accountVideoInline(t, store, reporter, "vid_running_inline", &schemas.BifrostVideoGenerationResponse{
			ID:     "vid_running_inline",
			Status: status,
		})
		assert.Nil(t, out, string(status))
	}
	assert.Empty(t, store.logs)
	assert.Empty(t, reporter.reports)
}

// Polling is not billing. A client that keeps calling retrieve after the job
// finished must not be charged again on every call.
func TestAccountVideoJob_RepeatedRetrievesBillOnce(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	job := videoJob(t, "vid_repoll", generationDims("sora-2-pro", "1280x720", 8))
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))

	resp := &schemas.BifrostVideoGenerationResponse{
		ID:     "vid_repoll",
		Status: schemas.VideoStatusCompleted,
		Videos: []schemas.VideoOutput{{Type: schemas.VideoOutputTypeURL}},
	}
	first := accountVideoInline(t, store, reporter, "vid_repoll", resp)
	require.NotNil(t, first)
	assert.True(t, first.Accounted)

	for i := 0; i < 3; i++ {
		again := accountVideoInline(t, store, reporter, "vid_repoll", resp)
		require.NotNil(t, again)
		assert.False(t, again.Claimed, "a settled job must not be re-claimed")
		// The caller still wants a price to display, mirrored off the written row.
		assert.InDelta(t, first.Cost, again.Cost, 1e-12)
	}
	assert.Len(t, store.logs, 1)
	assert.Len(t, reporter.reports, 1, "governance must be told exactly once")
}

// The Runware case: the retrieve the caller already holds carries the provider's
// own figure, so inline settlement prices exactly without any datasheet rate.
func TestAccountVideoJob_UsesProviderReportedCostFromTheRetrieve(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	// No duration and an unknown model: nothing here is priceable from the catalog.
	job := videoJob(t, "vid_runware", modelcatalog.VideoPricingDimensions{
		Model:       "runware:tripo-v3.1",
		RequestType: schemas.VideoGenerationRequest,
	})
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))

	out := accountVideoInline(t, store, reporter, "vid_runware", &schemas.BifrostVideoGenerationResponse{
		ID:     "vid_runware",
		Status: schemas.VideoStatusCompleted,
		Videos: []schemas.VideoOutput{{Type: schemas.VideoOutputTypeURL}},
		Usage:  &schemas.VideoUsage{Cost: &schemas.BifrostCost{TotalCost: 0.4}},
	})

	require.NotNil(t, out)
	assert.True(t, out.Accounted)
	assert.InDelta(t, 0.4, out.Cost, 1e-12,
		"a provider-reported cost settles a job the catalog cannot price at all")
}

// Settling inline and settling from the sweeper must land on the same row, or the
// two paths would each write their own cost record for one video.
func TestAccountVideoJob_SharesTheSweepersAggregateRow(t *testing.T) {
	store := newFakeAccountingStore()
	reporter := &fakeUsageReporter{}
	due := time.Now().UTC().Add(-time.Minute)
	job := videoJob(t, "vid_both", generationDims("sora-2-pro", "1280x720", 8))
	job.NextCheckAt = &due
	require.NoError(t, store.UpsertProviderJob(context.Background(), job))

	resp := &schemas.BifrostVideoGenerationResponse{
		ID:     "vid_both",
		Status: schemas.VideoStatusCompleted,
		Videos: []schemas.VideoOutput{{Type: schemas.VideoOutputTypeURL}},
	}
	require.NotNil(t, accountVideoInline(t, store, reporter, "vid_both", resp))

	retriever := &fakeVideoRetriever{resp: resp}
	NewVideoSweeper(store, store, fakePricing{}, retriever, nil, reporter, SweeperConfig{
		Provider: schemas.OpenAI,
		Limit:    10,
	}).SweepOnce(context.Background())

	assert.Len(t, store.logs, 1, "one video, one cost row, whichever path got there first")
	assert.Len(t, reporter.reports, 1)
	assert.Equal(t, 0, retriever.calls, "an accounted job is no longer due, so the sweeper skips it")
}
