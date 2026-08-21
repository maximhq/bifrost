package batchaccounting

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"strconv"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	cstables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const (
	defaultSweepInterval = time.Minute
	defaultSweepLimit    = 50
	defaultKVLeaseTTL    = 5 * time.Minute
	maxPollAttempts      = 120
	// defaultProviderPollTimeout bounds a single provider call. The sweep is
	// serial, so without it one hung provider stalls every remaining due job
	// indefinitely — the sweeper's context is long-lived and has no deadline of its
	// own. Sized to the KV poll lease: past that window another worker may take the
	// job anyway, so there is nothing to gain by waiting longer. Generous enough for
	// a large results download.
	defaultProviderPollTimeout = 5 * time.Minute
)

type SweepStore interface {
	BatchJobStore
	ListDueBatchJobs(ctx context.Context, provider string, now time.Time, limit int) ([]*cstables.TableBatchJob, error)
}

type BatchResultFetcher interface {
	RetrieveBatch(ctx context.Context, job *cstables.TableBatchJob) (*schemas.BifrostBatchRetrieveResponse, error)
	FetchBatchResults(ctx context.Context, job *cstables.TableBatchJob) (*schemas.BifrostBatchResultsResponse, error)
}

type SweeperConfig struct {
	Interval   time.Duration
	Limit      int
	ClaimedBy  string
	Provider   schemas.ModelProvider
	Scopes     *modelcatalog.PricingLookupScopes
	KVStore    schemas.KVStore
	KVLeaseTTL time.Duration
	Logger     schemas.Logger
}

type Sweeper struct {
	store         SweepStore
	logStore      AggregateLogStore
	pricing       PricingManager
	fetcher       BatchResultFetcher
	emitter       AggregateLogEmitter
	usageReporter UsageReporter
	config        SweeperConfig
}

func NewSweeper(store SweepStore, logStore AggregateLogStore, pricing PricingManager, fetcher BatchResultFetcher, emitter AggregateLogEmitter, usageReporter UsageReporter, config SweeperConfig) *Sweeper {
	if config.Interval <= 0 {
		config.Interval = defaultSweepInterval
	}
	if config.Limit <= 0 {
		config.Limit = defaultSweepLimit
	}
	if config.ClaimedBy == "" {
		// Never default to a bare constant: ClaimedBy becomes the runner id that
		// ClaimBatchJob and every ownership fence key on, so two sweepers sharing a
		// database and a default would be indistinguishable — each able to advance
		// the other's in-flight job. Callers should pass a stable per-node id; if
		// they don't, a per-instance id at least keeps the fence meaningful.
		config.ClaimedBy = "batch-sweeper:" + newSweeperInstanceID()
	}
	if config.KVLeaseTTL <= 0 {
		config.KVLeaseTTL = defaultKVLeaseTTL
	}
	return &Sweeper{
		store:         store,
		logStore:      logStore,
		pricing:       pricing,
		fetcher:       fetcher,
		emitter:       emitter,
		usageReporter: usageReporter,
		config:        config,
	}
}

// retrieveBatch and fetchBatchResults wrap the provider calls in a bounded context.
// The sweep is serial and the sweeper's own context is long-lived with no deadline,
// so an unbounded provider call would stall every remaining due job. The timeout is
// derived from the parent, so shutdown cancellation still propagates.
func (s *Sweeper) retrieveBatch(ctx context.Context, job *cstables.TableBatchJob) (*schemas.BifrostBatchRetrieveResponse, error) {
	callCtx, cancel := context.WithTimeout(ctx, defaultProviderPollTimeout)
	defer cancel()
	return s.fetcher.RetrieveBatch(callCtx, job)
}

func (s *Sweeper) fetchBatchResults(ctx context.Context, job *cstables.TableBatchJob) (*schemas.BifrostBatchResultsResponse, error) {
	callCtx, cancel := context.WithTimeout(ctx, defaultProviderPollTimeout)
	defer cancel()
	return s.fetcher.FetchBatchResults(callCtx, job)
}

// newSweeperInstanceID returns a random id distinguishing this sweeper from any
// other sharing the same database. Random rather than PID-based: sweepers on
// different hosts can share a PID, and this value is what keeps their claims apart.
func newSweeperInstanceID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

func (s *Sweeper) Run(ctx context.Context) {
	if s == nil {
		return
	}
	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()
	for {
		s.SweepOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Sweeper) SweepOnce(ctx context.Context) {
	if s == nil || s.store == nil || s.logStore == nil || s.pricing == nil || s.fetcher == nil {
		return
	}
	now := time.Now().UTC()
	jobs, err := s.store.ListDueBatchJobs(ctx, string(s.config.Provider), now, s.config.Limit)
	if err != nil {
		s.warn("batch accounting sweeper failed to find due jobs: %v", err)
		return
	}
	for _, job := range jobs {
		// Stop promptly on shutdown rather than working through the remaining
		// jobs firing provider calls that are already doomed to fail.
		if ctx.Err() != nil {
			return
		}
		s.sweepJob(ctx, job, now)
	}
}

func (s *Sweeper) sweepJob(ctx context.Context, job *cstables.TableBatchJob, now time.Time) {
	if job == nil || !IsProviderSupported(schemas.ModelProvider(job.Provider)) {
		return
	}
	locked, err := s.acquireProviderPollLease(job)
	if err != nil {
		s.warn("batch accounting sweeper failed to acquire poll lease provider=%s batch_id=%s job_id=%s: %v", job.Provider, job.BatchID, job.ID, err)
		return
	}
	if !locked {
		return
	}
	defer s.deletePollLease(job)
	retrieved, err := s.retrieveBatch(ctx, job)
	if err != nil || retrieved == nil {
		if err != nil {
			s.warn("batch accounting sweeper retrieve failed provider=%s batch_id=%s job_id=%s: %v", job.Provider, job.BatchID, job.ID, err)
		} else {
			s.warn("batch accounting sweeper retrieve returned nil response provider=%s batch_id=%s job_id=%s", job.Provider, job.BatchID, job.ID)
		}
		s.reschedule(ctx, job, now)
		return
	}
	if retrieved.ID != "" && retrieved.ID != job.BatchID {
		s.warn("batch accounting sweeper retrieve returned mismatched batch id job_id=%s", job.ID)
		s.reschedule(ctx, job, now)
		return
	}
	latest := batchJobFromRetrieve(job, retrieved)
	if err := s.store.UpsertBatchJob(ctx, latest); err != nil {
		s.warn("batch accounting sweeper failed to upsert retrieved batch provider=%s batch_id=%s job_id=%s: %v", latest.Provider, latest.BatchID, latest.ID, err)
		return
	}
	if retrieved.Status != schemas.BatchStatusCompleted && retrieved.Status != schemas.BatchStatusEnded {
		if isTerminalStatus(retrieved.Status) {
			// An expired or cancelled batch is not an empty one: the provider bills the
			// requests that did finish before the window closed, and their rows are in
			// the output file. Treating the status alone as "no results" silently threw
			// that usage away. Try the fetch, settle whatever came back, and only fall
			// through to the give-up path when there is genuinely nothing to price.
			if terminalMayHaveResults(retrieved.Status, latest) {
				if results, ok := s.fetchResultsForSettlement(ctx, latest); ok && len(results.Results) > 0 {
					s.settle(ctx, latest, retrieved, results, now)
					return
				}
			}
			s.markTerminalWithoutResults(ctx, latest)
			return
		}
		s.reschedule(ctx, latest, now)
		return
	}

	results, ok := s.fetchResultsForSettlement(ctx, latest)
	if !ok {
		s.reschedule(ctx, latest, now)
		return
	}
	s.settle(ctx, latest, retrieved, results, now)
}

// terminalMayHaveResults reports whether a terminal provider status can still have
// completed rows worth fetching. Expired and cancelled batches keep the rows that
// finished; a failed batch only has an output file when the provider says so; a
// deleted batch has nothing left to read.
func terminalMayHaveResults(status schemas.BatchStatus, job *cstables.TableBatchJob) bool {
	switch status {
	case schemas.BatchStatusExpired, schemas.BatchStatusCancelled:
		return true
	case schemas.BatchStatusFailed:
		return (job.OutputFileID != nil && *job.OutputFileID != "") || (job.ResultsURL != nil && *job.ResultsURL != "")
	default:
		return false
	}
}

// fetchResultsForSettlement returns the batch's results, or ok=false when they
// could not be obtained. What that means is the caller's to decide: a live batch
// retries later, a terminal one gives up.
func (s *Sweeper) fetchResultsForSettlement(ctx context.Context, job *cstables.TableBatchJob) (*schemas.BifrostBatchResultsResponse, bool) {
	results, err := s.fetchBatchResults(ctx, job)
	if err != nil || results == nil {
		if err != nil {
			s.warn("batch accounting sweeper results fetch failed provider=%s batch_id=%s job_id=%s: %v", job.Provider, job.BatchID, job.ID, err)
		} else {
			s.warn("batch accounting sweeper results fetch returned no response provider=%s batch_id=%s job_id=%s", job.Provider, job.BatchID, job.ID)
		}
		return nil, false
	}
	if results.BatchID != "" && results.BatchID != job.BatchID {
		s.warn("batch accounting sweeper results returned mismatched batch id job_id=%s", job.ID)
		return nil, false
	}
	return results, true
}

func (s *Sweeper) settle(ctx context.Context, latest *cstables.TableBatchJob, retrieved *schemas.BifrostBatchRetrieveResponse, results *schemas.BifrostBatchResultsResponse, now time.Time) {
	endpoint := schemas.BatchEndpoint(latest.Endpoint)
	if results.Endpoint != "" {
		endpoint = results.Endpoint
	}
	if _, err := AccountBatchResults(ctx, s.store, s.logStore, s.pricing, Request{
		Provider:      schemas.ModelProvider(latest.Provider),
		BatchID:       latest.BatchID,
		FallbackModel: latest.Model,
		Endpoint:      endpoint,
		Results:       results.Results,
		ParseErrors:   results.ExtraFields.ParseErrors,
		RequestCounts: &retrieved.RequestCounts,
		BatchJob:      latest,
		Emitter:       s.emitter,
		UsageReporter: s.usageReporter,
		ClaimedBy:     s.config.ClaimedBy,
		Scopes:        s.config.Scopes,
		Now:           now,
	}); err != nil {
		s.warn("batch accounting sweeper accounting failed provider=%s batch_id=%s job_id=%s: %v", latest.Provider, latest.BatchID, latest.ID, err)
		// Settlement failures were the one retry path with no budget: next_check_at
		// stayed in the past and poll_attempts never moved, so every sweep re-downloaded
		// the entire results payload and re-failed, forever. Reschedule like any other
		// retry so backoff, jitter, and the attempt cap apply here too.
		s.reschedule(ctx, latest, now)
	}
}

func (s *Sweeper) reschedule(ctx context.Context, job *cstables.TableBatchJob, now time.Time) {
	job.PollAttempts++
	if job.PollAttempts >= maxPollAttempts {
		s.markTerminalAsUnpriceable(ctx, job, UnpriceableReasonMaxPollAttempts)
		return
	}
	next := s.nextCheckAt(job, now)
	job.NextCheckAt = &next
	if err := s.store.UpsertBatchJob(ctx, job); err != nil {
		s.warn("batch accounting sweeper failed to reschedule provider=%s batch_id=%s job_id=%s: %v", job.Provider, job.BatchID, job.ID, err)
		return
	}
}

func (s *Sweeper) markTerminalAsUnpriceable(ctx context.Context, job *cstables.TableBatchJob, reason string) {
	runnerID := s.config.ClaimedBy
	// allowUnpriceable=false: this path is giving up on a job, not settling one, so
	// it has no business re-opening one that already reached that state.
	claimed, err := s.store.ClaimBatchJob(ctx, job.ID, runnerID, time.Now().UTC().Add(-defaultClaimTTL), false)
	if err != nil || !claimed {
		if err != nil {
			s.warn("batch accounting sweeper failed to claim for unpriceable provider=%s batch_id=%s job_id=%s reason=%s: %v", job.Provider, job.BatchID, job.ID, reason, err)
		}
		return
	}
	if err := s.store.MarkBatchJobUnpriceable(ctx, job.ID, runnerID, reason, nil); err != nil {
		s.warn("batch accounting sweeper failed to mark unpriceable batch provider=%s batch_id=%s job_id=%s reason=%s: %v", job.Provider, job.BatchID, job.ID, reason, err)
	}
}

func (s *Sweeper) markTerminalWithoutResults(ctx context.Context, job *cstables.TableBatchJob) {
	s.markTerminalAsUnpriceable(ctx, job, "terminal_without_results")
}

func batchJobFromRetrieve(existing *cstables.TableBatchJob, retrieved *schemas.BifrostBatchRetrieveResponse) *cstables.TableBatchJob {
	job := *existing
	job.ProviderStatus = string(retrieved.Status)
	if retrieved.Endpoint != "" {
		job.Endpoint = retrieved.Endpoint
	}
	job.InputFileID = retrieved.InputFileID
	job.OutputFileID = retrieved.OutputFileID
	job.ErrorFileID = retrieved.ErrorFileID
	job.ResultsURL = retrieved.ResultsURL
	return &job
}

func isTerminalStatus(status schemas.BatchStatus) bool {
	switch status {
	case schemas.BatchStatusCompleted, schemas.BatchStatusFailed, schemas.BatchStatusExpired, schemas.BatchStatusCancelled, schemas.BatchStatusEnded, schemas.BatchStatusDeleted:
		return true
	default:
		return false
	}
}

func (s *Sweeper) acquireProviderPollLease(job *cstables.TableBatchJob) (bool, error) {
	if s.config.KVStore == nil {
		return true, nil
	}
	key := fmt.Sprintf("batch-accounting:poll:%s:%s", job.Provider, job.BatchID)
	value := map[string]string{
		"claimed_by": s.config.ClaimedBy,
		"job_id":     job.ID,
	}
	return s.config.KVStore.SetNXWithTTL(key, value, s.config.KVLeaseTTL)
}

func (s *Sweeper) deletePollLease(job *cstables.TableBatchJob) {
	if s.config.KVStore == nil {
		return
	}
	key := fmt.Sprintf("batch-accounting:poll:%s:%s", job.Provider, job.BatchID)
	if _, err := s.config.KVStore.Delete(key); err != nil {
		s.warn("batch accounting sweeper failed to release poll lease provider=%s batch_id=%s job_id=%s: %v", job.Provider, job.BatchID, job.ID, err)
	}
}

func (s *Sweeper) nextCheckAt(job *cstables.TableBatchJob, now time.Time) time.Time {
	delay := s.config.Interval
	switch {
	case job.PollAttempts >= 30:
		delay = maxDuration(delay, 15*time.Minute)
	case job.PollAttempts >= 10:
		delay = maxDuration(delay, 5*time.Minute)
	case job.PollAttempts >= 5:
		delay = maxDuration(delay, 2*time.Minute)
	}
	return now.Add(delay + deterministicJitter(job.ID, job.PollAttempts, delay))
}

func deterministicJitter(jobID string, attempts int, delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	jitterCap := min(time.Minute, delay/5)
	if jitterCap <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%s:%d", jobID, attempts)
	return time.Duration(h.Sum32()%uint32(jitterCap/time.Second+1)) * time.Second
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func (s *Sweeper) warn(msg string, args ...any) {
	if s.config.Logger != nil {
		s.config.Logger.Warn(msg, args...)
	}
}
