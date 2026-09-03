package jobaccounting

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
	MaxPollAttempts      = 120

	UnpriceableReasonMaxPollAttempts = "max_poll_attempts"
)

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
	settler       Settler
	emitter       AggregateLogEmitter
	usageReporter UsageReporter
	config        SweeperConfig
}

func NewSweeper(store SweepStore, logStore AggregateLogStore, pricing PricingManager, settler Settler, emitter AggregateLogEmitter, usageReporter UsageReporter, config SweeperConfig) *Sweeper {
	if config.Interval <= 0 {
		config.Interval = defaultSweepInterval
	}
	if config.Limit <= 0 {
		config.Limit = defaultSweepLimit
	}
	if config.ClaimedBy == "" {
		// Never default to a bare constant: ClaimedBy becomes the runner id that
		// ClaimProviderJob and every ownership fence key on, so two sweepers sharing a
		// database and a default would be indistinguishable — each able to advance
		// the other's in-flight job. Callers should pass a stable per-node id; if
		// they don't, a per-instance id at least keeps the fence meaningful.
		config.ClaimedBy = "job-sweeper:" + newInstanceID()
	}
	if config.KVLeaseTTL <= 0 {
		config.KVLeaseTTL = defaultKVLeaseTTL
	}
	return &Sweeper{
		store:         store,
		logStore:      logStore,
		pricing:       pricing,
		settler:       settler,
		emitter:       emitter,
		usageReporter: usageReporter,
		config:        config,
	}
}

// newInstanceID returns a random id distinguishing one sweeper from any other
// sharing the same database. Random rather than PID-based: sweepers on different
// hosts can share a PID, and this value is what keeps their claims apart. Exported
// so a kind can build its own runner-id prefix.
func newInstanceID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// Config returns the sweeper's resolved configuration, with defaults applied.
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
	if s == nil || s.store == nil || s.logStore == nil || s.pricing == nil || s.settler == nil {
		return
	}
	now := time.Now().UTC()
	jobs, err := s.store.ListDueProviderJobs(ctx, string(s.settler.Kind()), string(s.config.Provider), now, s.config.Limit)
	if err != nil {
		s.warn("%s accounting sweeper failed to find due jobs: %v", s.settler.Kind(), err)
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

// sweepJob polls and settles a single due job.
func (s *Sweeper) sweepJob(ctx context.Context, job *cstables.TableProviderJob, now time.Time) {
	if job == nil || !s.settler.SupportsProvider(schemas.ModelProvider(job.Provider)) {
		return
	}
	locked, err := s.acquireProviderPollLease(job)
	if err != nil {
		s.warn("%s accounting sweeper failed to acquire poll lease provider=%s job=%s job_id=%s: %v", s.settler.Kind(), job.Provider, job.JobID, job.ID, err)
		return
	}
	if !locked {
		return
	}
	defer s.deletePollLease(job)

	poll, err := s.settler.Poll(ctx, job)
	if err != nil || poll == nil {
		if err != nil {
			s.warn("%s accounting sweeper poll failed provider=%s job=%s job_id=%s: %v", s.settler.Kind(), job.Provider, job.JobID, job.ID, err)
		} else {
			s.warn("%s accounting sweeper poll returned nil result provider=%s job=%s job_id=%s", s.settler.Kind(), job.Provider, job.JobID, job.ID)
		}
		s.reschedule(ctx, job, now)
		return
	}

	// Persist whatever the poll learned before deciding anything, so a poll that
	// advanced the provider status records it even when settlement cannot proceed.
	latest := job
	if poll.Job != nil {
		if err := s.store.UpsertProviderJob(ctx, poll.Job); err != nil {
			s.warn("%s accounting sweeper failed to upsert polled job provider=%s job=%s job_id=%s: %v", s.settler.Kind(), poll.Job.Provider, poll.Job.JobID, poll.Job.ID, err)
			return
		}
		latest = poll.Job
	}

	switch {
	case poll.Retry || !poll.Terminal:
		s.reschedule(ctx, latest, now)
	case !poll.Settleable:
		s.markTerminalAsUnpriceable(ctx, latest, poll.UnpriceableReason)
	default:
		s.settle(ctx, latest, poll, now)
	}
}

func (s *Sweeper) settle(ctx context.Context, job *cstables.TableProviderJob, poll *PollResult, now time.Time) {
	if _, err := AccountJob(ctx, s.store, s.logStore, s.pricing, s.settler, JobRequest{
		Provider:      schemas.ModelProvider(job.Provider),
		ProviderJobID: job.JobID,
		FallbackModel: job.Model,
		Job:           job,
		Payload:       poll.Payload,
		Emitter:       s.emitter,
		UsageReporter: s.usageReporter,
		ClaimedBy:     s.config.ClaimedBy,
		Scopes:        s.config.Scopes,
		Now:           now,
	}); err != nil {
		s.warn("%s accounting sweeper accounting failed provider=%s job=%s job_id=%s: %v", s.settler.Kind(), job.Provider, job.JobID, job.ID, err)
		// Settlement failures were the one retry path with no budget: next_check_at
		// stayed in the past and poll_attempts never moved, so every sweep re-did the
		// entire settlement and re-failed, forever. Reschedule like any other retry so
		// backoff, jitter, and the attempt cap apply here too.
		s.reschedule(ctx, job, now)
	}
}

func (s *Sweeper) reschedule(ctx context.Context, job *cstables.TableProviderJob, now time.Time) {
	job.PollAttempts++
	if job.PollAttempts >= MaxPollAttempts {
		s.markTerminalAsUnpriceable(ctx, job, UnpriceableReasonMaxPollAttempts)
		return
	}
	next := s.nextCheckAt(job, now)
	job.NextCheckAt = &next
	if err := s.store.UpsertProviderJob(ctx, job); err != nil {
		s.warn("%s accounting sweeper failed to reschedule provider=%s job=%s job_id=%s: %v", s.settler.Kind(), job.Provider, job.JobID, job.ID, err)
		return
	}
}

func (s *Sweeper) markTerminalAsUnpriceable(ctx context.Context, job *cstables.TableProviderJob, reason string) {
	runnerID := s.config.ClaimedBy
	// allowUnpriceable=false: this path is giving up on a job, not settling one, so
	// it has no business re-opening one that already reached that state.
	claimed, err := s.store.ClaimProviderJob(ctx, job.ID, runnerID, time.Now().UTC().Add(-defaultClaimTTL), false)
	if err != nil || !claimed {
		if err != nil {
			s.warn("%s accounting sweeper failed to claim for unpriceable provider=%s job=%s job_id=%s reason=%s: %v", s.settler.Kind(), job.Provider, job.JobID, job.ID, reason, err)
		}
		return
	}
	if err := s.store.MarkProviderJobUnpriceable(ctx, job.ID, runnerID, reason, nil); err != nil {
		s.warn("%s accounting sweeper failed to mark unpriceable provider=%s job=%s job_id=%s reason=%s: %v", s.settler.Kind(), job.Provider, job.JobID, job.ID, reason, err)
	}
}

func (s *Sweeper) acquireProviderPollLease(job *cstables.TableProviderJob) (bool, error) {
	if s.config.KVStore == nil {
		return true, nil
	}
	value := map[string]string{
		"claimed_by": s.config.ClaimedBy,
		"job_id":     job.ID,
	}
	return s.config.KVStore.SetNXWithTTL(s.pollLeaseKey(job), value, s.config.KVLeaseTTL)
}

func (s *Sweeper) deletePollLease(job *cstables.TableProviderJob) {
	if s.config.KVStore == nil {
		return
	}
	if _, err := s.config.KVStore.Delete(s.pollLeaseKey(job)); err != nil {
		s.warn("%s accounting sweeper failed to release poll lease provider=%s job=%s job_id=%s: %v", s.settler.Kind(), job.Provider, job.JobID, job.ID, err)
	}
}

// pollLeaseKey keeps the batch kind's historical key prefix so a rolling deploy
// does not let an old and a new node poll the same batch concurrently.
func (s *Sweeper) pollLeaseKey(job *cstables.TableProviderJob) string {
	prefix := string(s.settler.Kind())
	if s.settler.Kind() == ProviderJobKindBatch {
		prefix = "batch-accounting"
	}
	return fmt.Sprintf("%s:poll:%s:%s", prefix, job.Provider, job.JobID)
}

func (s *Sweeper) nextCheckAt(job *cstables.TableProviderJob, now time.Time) time.Time {
	delay := s.settler.Backoff(job.PollAttempts, s.config.Interval)
	if delay < s.config.Interval {
		delay = s.config.Interval
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

func (s *Sweeper) warn(msg string, args ...any) {
	if s.config.Logger != nil {
		s.config.Logger.Warn(msg, args...)
	}
}
