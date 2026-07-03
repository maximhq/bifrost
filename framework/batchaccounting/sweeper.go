package batchaccounting

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const (
	defaultSweepInterval = time.Minute
	defaultSweepLimit    = 50
	defaultKVLeaseTTL    = 5 * time.Minute
	maxPollAttempts      = 120
)

type SweepStore interface {
	Store
	FindDueBatchJobs(ctx context.Context, provider string, now time.Time, limit int) ([]*logstore.BatchJob, error)
}

type BatchResultFetcher interface {
	RetrieveBatch(ctx context.Context, job *logstore.BatchJob) (*schemas.BifrostBatchRetrieveResponse, error)
	FetchBatchResults(ctx context.Context, job *logstore.BatchJob) (*schemas.BifrostBatchResultsResponse, error)
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
	pricing       PricingManager
	fetcher       BatchResultFetcher
	emitter       AggregateLogEmitter
	usageReporter UsageReporter
	config        SweeperConfig
}

func NewSweeper(store SweepStore, pricing PricingManager, fetcher BatchResultFetcher, emitter AggregateLogEmitter, usageReporter UsageReporter, config SweeperConfig) *Sweeper {
	if config.Interval <= 0 {
		config.Interval = defaultSweepInterval
	}
	if config.Limit <= 0 {
		config.Limit = defaultSweepLimit
	}
	if config.Provider == "" {
		config.Provider = schemas.OpenAI
	}
	if config.ClaimedBy == "" {
		config.ClaimedBy = "batch-sweeper"
	}
	if config.KVLeaseTTL <= 0 {
		config.KVLeaseTTL = defaultKVLeaseTTL
	}
	return &Sweeper{
		store:         store,
		pricing:       pricing,
		fetcher:       fetcher,
		emitter:       emitter,
		usageReporter: usageReporter,
		config:        config,
	}
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
	if s == nil || s.store == nil || s.pricing == nil || s.fetcher == nil {
		return
	}
	now := time.Now().UTC()
	jobs, err := s.store.FindDueBatchJobs(ctx, string(s.config.Provider), now, s.config.Limit)
	if err != nil {
		s.warn("batch accounting sweeper failed to find due jobs: %v", err)
		return
	}
	for _, job := range jobs {
		s.sweepJob(ctx, job, now)
	}
}

func (s *Sweeper) sweepJob(ctx context.Context, job *logstore.BatchJob, now time.Time) {
	if job == nil || schemas.ModelProvider(job.Provider) != schemas.OpenAI {
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
	retrieved, err := s.fetcher.RetrieveBatch(ctx, job)
	if err != nil || retrieved == nil {
		if err != nil {
			s.warn("batch accounting sweeper retrieve failed provider=%s batch_id=%s job_id=%s: %v", job.Provider, job.BatchID, job.ID, err)
		} else {
			s.warn("batch accounting sweeper retrieve returned nil response provider=%s batch_id=%s job_id=%s", job.Provider, job.BatchID, job.ID)
		}
		s.reschedule(ctx, job, now)
		return
	}
	latest := batchJobFromRetrieve(job, retrieved, now)
	if err := s.store.UpsertBatchJob(ctx, latest); err != nil {
		s.warn("batch accounting sweeper failed to upsert retrieved batch provider=%s batch_id=%s job_id=%s: %v", latest.Provider, latest.BatchID, latest.ID, err)
		return
	}
	if retrieved.Status != schemas.BatchStatusCompleted {
		if isTerminalStatus(retrieved.Status) {
			s.markTerminalWithoutResults(ctx, latest)
			return
		}
		s.reschedule(ctx, latest, now)
		return
	}

	results, err := s.fetcher.FetchBatchResults(ctx, latest)
	if err != nil || results == nil {
		if err != nil {
			s.warn("batch accounting sweeper results fetch failed provider=%s batch_id=%s job_id=%s: %v", latest.Provider, latest.BatchID, latest.ID, err)
		} else {
			s.warn("batch accounting sweeper results fetch returned no response provider=%s batch_id=%s job_id=%s", latest.Provider, latest.BatchID, latest.ID)
		}
		s.reschedule(ctx, latest, now)
		return
	}
	if _, err := AccountBatchResults(ctx, s.store, s.pricing, Request{
		Provider:      schemas.ModelProvider(latest.Provider),
		BatchID:       latest.BatchID,
		FallbackModel: latest.Model,
		Results:       results.Results,
		RequestCounts: &retrieved.RequestCounts,
		BatchJob:      latest,
		Emitter:       s.emitter,
		UsageReporter: s.usageReporter,
		ClaimedBy:     s.config.ClaimedBy,
		Scopes:        s.config.Scopes,
		Now:           now,
	}); err != nil {
		s.warn("batch accounting sweeper accounting failed provider=%s batch_id=%s job_id=%s: %v", latest.Provider, latest.BatchID, latest.ID, err)
	}
}

func (s *Sweeper) reschedule(ctx context.Context, job *logstore.BatchJob, now time.Time) {
	job.PollAttempts++
	if job.PollAttempts >= maxPollAttempts {
		s.markTerminalAsUnpriceable(ctx, job, UnpriceableReasonMaxPollAttempts)
		return
	}
	next := s.nextCheckAt(job, now)
	job.NextCheckAt = &next
	if err := s.store.UpsertBatchJob(ctx, job); err != nil {
		s.warn("batch accounting sweeper failed to reschedule provider=%s batch_id=%s job_id=%s: %v", job.Provider, job.BatchID, job.ID, err)
	}
}

func (s *Sweeper) markTerminalAsUnpriceable(ctx context.Context, job *logstore.BatchJob, reason string) {
	token, claimed, err := s.store.ClaimBatchJobAccounting(ctx, job.ID, s.config.ClaimedBy, defaultClaimTTL)
	if err != nil || !claimed {
		if err != nil {
			s.warn("batch accounting sweeper failed to claim for unpriceable provider=%s batch_id=%s job_id=%s reason=%s: %v", job.Provider, job.BatchID, job.ID, reason, err)
		}
		return
	}
	if err := s.store.MarkBatchJobUnpriceable(ctx, job.ID, token, reason, nil); err != nil {
		s.warn("batch accounting sweeper failed to mark unpriceable batch provider=%s batch_id=%s job_id=%s reason=%s: %v", job.Provider, job.BatchID, job.ID, reason, err)
	}
}

func (s *Sweeper) markTerminalWithoutResults(ctx context.Context, job *logstore.BatchJob) {
	s.markTerminalAsUnpriceable(ctx, job, "terminal_without_results")
}

func batchJobFromRetrieve(existing *logstore.BatchJob, retrieved *schemas.BifrostBatchRetrieveResponse, now time.Time) *logstore.BatchJob {
	job := *existing
	job.BatchID = retrieved.ID
	job.ProviderStatus = string(retrieved.Status)
	job.InputFileID = retrieved.InputFileID
	job.OutputFileID = retrieved.OutputFileID
	job.ErrorFileID = retrieved.ErrorFileID
	job.ResultsURL = retrieved.ResultsURL
	return &job
}

func marshalString(value any) string {
	out, err := sonic.MarshalString(value)
	if err != nil || out == "{}" {
		return ""
	}
	return out
}

func isTerminalStatus(status schemas.BatchStatus) bool {
	switch status {
	case schemas.BatchStatusCompleted, schemas.BatchStatusFailed, schemas.BatchStatusExpired, schemas.BatchStatusCancelled, schemas.BatchStatusEnded, schemas.BatchStatusDeleted:
		return true
	default:
		return false
	}
}

func (s *Sweeper) acquireProviderPollLease(job *logstore.BatchJob) (bool, error) {
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

func (s *Sweeper) nextCheckAt(job *logstore.BatchJob, now time.Time) time.Time {
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
	jitterCap := delay / 5
	if jitterCap > time.Minute {
		jitterCap = time.Minute
	}
	if jitterCap <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(fmt.Sprintf("%s:%d", jobID, attempts)))
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

func BatchFetcherError(provider schemas.ModelProvider, batchID string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("batch fetch failed for provider=%s batch_id=%s: %w", provider, batchID, err)
}
