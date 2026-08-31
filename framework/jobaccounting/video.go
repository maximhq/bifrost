package jobaccounting

import (
	"context"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const ProviderJobKindVideo ProviderJobKind = cstables.ProviderJobKindVideo

const (
	UnpriceableReasonMissingVideoPricing = "missing_video_pricing"
	// UnpriceableReasonVideoGone is a job the provider no longer knows about.
	// Generated assets expire, so this is an ordinary end state, not an error.
	UnpriceableReasonVideoGone = "video_gone"
	// UnpriceableReasonContentFiltered is deliberately not a price. A safety-filtered
	// job consumed real compute, but whether the provider bills for it is a
	// per-provider policy none of them document consistently. Guessing free
	// under-bills and guessing billable over-bills; parking it costs nothing, keeps
	// the job re-drivable, and the existing missing-cost backfill can settle it in
	// bulk once the policy is actually known.
	UnpriceableReasonContentFiltered = "content_filtered_policy_unknown"
)

// videoLogNamespace keys the aggregate cost row for a settled video. It is its own
// namespace so a video can never collide with a batch that happens to share a
// provider-side id, and it is fixed forever: it is both the aggregate write's
// idempotency key and the governance dedupe key.
var videoLogNamespace = uuid.MustParse("b1e0f7c4-2a63-4d18-9f5b-6c0a3d8e41d7")

func init() { RegisterLogNamespace(ProviderJobKindVideo, videoLogNamespace) }

// videoProviders is the set Bifrost can actually settle video for. Every provider
// implements the VideoGeneration method, so method presence proves nothing — this
// is the list with a real implementation behind it.
var videoProviders = map[schemas.ModelProvider]struct{}{
	schemas.OpenAI:    {},
	schemas.Gemini:    {},
	schemas.Vertex:    {},
	schemas.Replicate: {},
	schemas.Runway:    {},
	schemas.Runware:   {},
}

// VideoRetriever is the provider call the video settler needs: given a job row,
// report where that job now stands.
type VideoRetriever interface {
	RetrieveVideo(ctx context.Context, job *cstables.TableProviderJob) (*schemas.BifrostVideoGenerationResponse, error)
}

// VideoSettler prices provider video jobs. It is the Settler for
// ProviderJobKindVideo.
//
// retriever may be nil on the inline settlement path, where the caller already
// holds a terminal response and never polls.
type VideoSettler struct {
	retriever VideoRetriever
}

func NewVideoSettler(retriever VideoRetriever) *VideoSettler {
	return &VideoSettler{retriever: retriever}
}

// VideoPayload is the video kind's settlement input, carried opaquely by the engine
// from Poll (or an inline caller) into Settle.
type VideoPayload struct {
	Response *schemas.BifrostVideoGenerationResponse
}

func (s *VideoSettler) Kind() ProviderJobKind { return ProviderJobKindVideo }

func (s *VideoSettler) SupportsProvider(provider schemas.ModelProvider) bool {
	_, ok := videoProviders[provider]
	return ok
}

// Backoff is minutes where batch's is hours. A video job typically finishes in one
// to five minutes, so the batch ladder would leave a finished job unsettled — and
// its assets unreadable — for a quarter of an hour after it was ready.
func (s *VideoSettler) Backoff(attempts int, interval time.Duration) time.Duration {
	delay := interval
	switch {
	case attempts >= 40:
		delay = maxDuration(delay, 5*time.Minute)
	case attempts >= 20:
		delay = maxDuration(delay, 2*time.Minute)
	}
	return delay
}

func (s *VideoSettler) Poll(ctx context.Context, job *cstables.TableProviderJob) (*PollResult, error) {
	if s.retriever == nil {
		return nil, fmt.Errorf("video retriever is nil")
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultProviderPollTimeout)
	defer cancel()

	resp, err := s.retriever.RetrieveVideo(callCtx, job)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("video retrieve returned nil response for job %s", job.ID)
	}
	if resp.ID != "" && resp.ID != job.JobID {
		return nil, fmt.Errorf("video retrieve returned mismatched video id for job %s", job.ID)
	}

	latest := videoJobFromResponse(job, resp)
	if !isTerminalVideoStatus(resp.Status) {
		return &PollResult{Job: latest}, nil
	}
	return &PollResult{
		Job:        latest,
		Terminal:   true,
		Settleable: true,
		Payload:    VideoPayload{Response: resp},
	}, nil
}

// Settle prices one video job from its merged dimensions: what was ordered, as
// recorded on the job row at submission, overlaid with what the terminal response
// says actually happened.
//
// The merge is the whole design. Most providers' retrieve reports little beyond a
// status and a URL, so the request captured at submission is usually the only place
// the duration and resolution exist by the time this runs.
func (s *VideoSettler) Settle(ctx context.Context, pricing PricingManager, jobReq JobRequest) (*Settlement, error) {
	load, _ := jobReq.Payload.(VideoPayload)
	resp := load.Response

	captured := VideoDimensionsFromJob(jobReq.Job)
	dims := captured.MergedWith(modelcatalog.VideoDimensionsFromResponse(resp))
	if dims.Model == "" && jobReq.FallbackModel != "" {
		dims.Model = jobReq.FallbackModel
	}

	settlement := &Settlement{
		Model:  dims.Model,
		Object: videoObjectFor(dims.RequestType),
	}

	if resp != nil && resp.ContentFilter != nil && resp.ContentFilter.FilteredCount > 0 {
		settlement.UnpriceableReason = UnpriceableReasonContentFiltered
		return settlement, nil
	}

	// A failed generation is priced, at zero. Providers are explicit that an
	// unsuccessful video is not billed, and that is a final answer about the money —
	// not an absence of one. Recording it as unpriceable instead would leave free
	// work parked in the sweeper forever.
	if resp != nil && resp.Status == schemas.VideoStatusFailed {
		settlement.Priced = true
		settlement.Complete = true
		return settlement, nil
	}

	details := pricing.CalculateVideoCostDetails(dims, jobReq.Provider, jobReq.Scopes)
	if !details.Priced {
		settlement.UnpriceableReason = UnpriceableReasonMissingVideoPricing
		// Keep the row so the usage stays visible and the backfill can reprice it
		// once the rate lands, rather than dropping the job's cost entirely.
		settlement.RecordUnpriced = true
		settlement.UnpricedModel = dims.Model
		return settlement, nil
	}

	settlement.Priced = true
	settlement.Complete = true
	settlement.Cost = details.Cost
	return settlement, nil
}

// HydrateFromLog is a no-op: video carries no kind-specific detail on the aggregate
// row yet, and the engine reads cost and usage off the row itself.
func (s *VideoSettler) HydrateFromLog(entry *logstore.Log, out *Outcome) {}

// VideoDimensionsFromJob reads the pricing dimensions captured on the job row at
// submission. An unreadable blob yields empty dimensions rather than an error: the
// response may still carry enough to price the job, and refusing to settle over a
// malformed params column would strand real money.
func VideoDimensionsFromJob(job *cstables.TableProviderJob) modelcatalog.VideoPricingDimensions {
	if job == nil || job.Params == nil || *job.Params == "" {
		return modelcatalog.VideoPricingDimensions{}
	}
	var dims modelcatalog.VideoPricingDimensions
	if err := sonic.Unmarshal([]byte(*job.Params), &dims); err != nil {
		return modelcatalog.VideoPricingDimensions{}
	}
	return dims
}

// MarshalVideoDimensions renders dimensions for the job row's params column.
func MarshalVideoDimensions(dims modelcatalog.VideoPricingDimensions) (*string, error) {
	data, err := sonic.Marshal(dims)
	if err != nil {
		return nil, err
	}
	encoded := string(data)
	return &encoded, nil
}

// AccountVideoJob settles a video from a terminal response the caller already holds.
// It is the retrieve path's inline settlement — the counterpart to
// AccountBatchResults on /v1/batches/{id}/results.
//
// It exists to avoid throwing away work: a user polling GET /v1/videos/{id} has
// already fetched the terminal response, cost included for providers that report
// one, and waiting for the sweeper to fetch it again costs both a provider call and
// up to a full sweep interval of delay.
//
// Two things separate it from the batch entry point:
//
// It never creates the coordination row. A video submitted before this build was
// charged at submission, so settling one whose row we never wrote would bill it a
// second time. No row means we did not see the submission — leave it alone.
//
// It does not force the claim. A caller holding batch results has something the
// parked job lacked and may re-drive it; a video retrieve carries no more than the
// settlement that parked the job already saw, and clients poll it repeatedly, so
// re-driving on every poll would be churn. An already-settled or parked job takes
// the engine's mirror path and reports the price it already has.
func AccountVideoJob(ctx context.Context, stateStore JobStore, logStore AggregateLogStore, pricing PricingManager, req JobRequest) (*Outcome, error) {
	if stateStore == nil {
		return nil, fmt.Errorf("video accounting state store is nil")
	}
	if req.Provider == "" || req.ProviderJobID == "" {
		return nil, nil
	}

	load, _ := req.Payload.(VideoPayload)
	if load.Response == nil || !isTerminalVideoStatus(load.Response.Status) {
		// Still running: nothing to settle, and the sweeper is already scheduled.
		return nil, nil
	}

	job, err := stateStore.GetProviderJob(ctx, cstables.ProviderJobID(cstables.ProviderJobKindVideo, string(req.Provider), req.ProviderJobID))
	if err != nil || job == nil {
		return nil, nil
	}
	req.Job = job
	return AccountJob(ctx, stateStore, logStore, pricing, NewVideoSettler(nil), req)
}

// NewVideoSweeper binds the generic sweeper to the video settler.
func NewVideoSweeper(store SweepStore, logStore AggregateLogStore, pricing PricingManager, retriever VideoRetriever, emitter AggregateLogEmitter, usageReporter UsageReporter, config SweeperConfig) *Sweeper {
	if config.ClaimedBy == "" {
		config.ClaimedBy = "video-sweeper:" + newInstanceID()
	}
	return NewSweeper(store, logStore, pricing, NewVideoSettler(retriever), emitter, usageReporter, config)
}

func videoJobFromResponse(existing *cstables.TableProviderJob, resp *schemas.BifrostVideoGenerationResponse) *cstables.TableProviderJob {
	job := *existing
	job.ProviderStatus = string(resp.Status)
	if resp.Model != "" {
		job.Model = resp.Model
	}
	return &job
}

func isTerminalVideoStatus(status schemas.VideoStatus) bool {
	switch status {
	case schemas.VideoStatusCompleted, schemas.VideoStatusFailed:
		return true
	default:
		return false
	}
}

// videoObjectFor labels the aggregate row with the request type that created the
// job, so a later repricing pass reaches the same rates this settlement used.
func videoObjectFor(requestType schemas.RequestType) schemas.RequestType {
	switch requestType {
	case schemas.VideoRemixRequest, schemas.VideoEditRequest:
		return requestType
	default:
		return schemas.VideoGenerationRequest
	}
}
