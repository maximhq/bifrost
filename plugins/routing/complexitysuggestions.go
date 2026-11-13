package routing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/sidekiq"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggest"
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggestrun"
)

// Scheduled phrase-suggestion runs.
//
// The schedule's state lives in the config store: when the next run is due,
// and a version every write bumps. Every node checks about once a minute,
// reading the row only once its cached next run has come (or the schedule's
// settings changed). A node that finds the run due claims it by moving the
// next run on while the version still matches, so exactly one node takes it,
// and the next run is counted from now, so a gateway that was down for
// several runs catches up with one.
//
// The node that claims a run records it, queued, before handing it to the job
// runner, or skipped with its reason when it cannot start (the router is
// still warming, or a run is still in progress here). Without a config store
// there is no one to coordinate with: the node keeps the next run in memory
// and runs it itself.

// ComplexitySuggestionsJobKind is the sidekiq kind a suggestion run is queued
// as.
const ComplexitySuggestionsJobKind = "model_routing_suggestions"

// complexitySuggestionRunNamespace scopes the deterministic IDs of scheduled
// suggestion runs.
var complexitySuggestionRunNamespace = uuid.MustParse("e0cfefa2-278e-44ef-8e11-27edf6602d00")

// complexitySuggestionSlotRunID is the run and job ID of a scheduled slot: a
// UUID, so it fits the run table's key, and the same on every node, so a run
// recorded or queued twice is still one run.
func complexitySuggestionSlotRunID(slot time.Time) string {
	return uuid.NewSHA1(complexitySuggestionRunNamespace, []byte(fmt.Sprintf("%s\x00%d", ComplexitySuggestionsJobKind, slot.Unix()))).String()
}

// complexitySuggestionScheduleKey names the router a schedule row belongs to.
// Suggestions run for one router, the complexity router.
const complexitySuggestionScheduleKey = "default"

// complexitySuggestionScheduleHash identifies the schedule settings a next
// run was computed from, so a change to them moves it.
func complexitySuggestionScheduleHash(schedule configstore.ComplexitySuggestionsSchedule) string {
	sum := sha256.Sum256([]byte(schedule.Interval().String() + "\x00" + schedule.Start().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}

const (
	// suggestionScheduleTick is how often a node checks the schedule, plus up
	// to suggestionScheduleJitter so nodes do not all ask at once; a run
	// starts at most that late.
	suggestionScheduleTick   = time.Minute
	suggestionScheduleJitter = 10 * time.Second
	// suggestionRunTimeout bounds one run.
	suggestionRunTimeout = time.Hour
	// suggestionLLMTimeout bounds one suggestion model call; a cluster takes
	// at most two.
	suggestionLLMTimeout = 90 * time.Second
	// suggestionLLMMaxTokens bounds one answer: a tier, a reason and two
	// short phrases, with room for a reasoning model's thinking.
	suggestionLLMMaxTokens = 4000
)

// ComplexitySuggestionTexts returns the text the semantic router embedded for
// each of the given logs, rebuilt with messageHistoryCount user turns. A log
// that is gone or hides its content is left out.
type ComplexitySuggestionTexts func(ctx context.Context, logIDs []string, messageHistoryCount int) (map[string]string, error)

// ComplexitySuggestionJobQueue queues suggestion runs. *sidekiq.Runner is one.
type ComplexitySuggestionJobQueue interface {
	EnqueuePartitioned(ctx context.Context, id, kind, partitioningKey, metadata, createdBy string) error
}

// ComplexitySuggestionJobStore reports queued and running jobs. The config
// store is one.
type ComplexitySuggestionJobStore interface {
	GetInFlightSidekiqJobByKind(ctx context.Context, kind string) (*tables.TableSidekiqJob, error)
}

// ComplexitySuggestionScheduleStore keeps schedule state and lets nodes
// claim a due run. The config store is one.
type ComplexitySuggestionScheduleStore interface {
	GetComplexitySuggestionSchedule(ctx context.Context, routerID string) (*tables.TableComplexitySuggestionSchedule, error)
	SetComplexitySuggestionSchedule(ctx context.Context, routerID, scheduleHash string, nextRunAt time.Time, readVersion *int64) (bool, error)
	ClaimComplexitySuggestionRun(ctx context.Context, routerID string, version int64, dueAt, nextRunAt time.Time, runID string) (bool, error)
}

// complexitySuggestions is the plugin's suggestion-run state.
type complexitySuggestions struct {
	deps      atomic.Pointer[complexitySuggestionDeps]
	jobs      atomic.Pointer[complexitySuggestionJobs]
	schedules atomic.Pointer[ComplexitySuggestionScheduleStore]
	// inline guards the run a node without a job runner does itself.
	inline atomic.Bool

	// next is this node's view of the schedule: the next run of the settings
	// hash names. With a schedule store it only saves reading the row before
	// the run is due; without one it is the schedule.
	nextMu sync.Mutex
	next   complexitySuggestionNext

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// mu orders run registration (beginRun) against close; see beginRun.
	mu     sync.Mutex
	closed bool
}

type complexitySuggestionNext struct {
	hash string
	at   time.Time
}

func (cs *complexitySuggestions) cachedNext() complexitySuggestionNext {
	cs.nextMu.Lock()
	defer cs.nextMu.Unlock()
	return cs.next
}

func (cs *complexitySuggestions) rememberNext(hash string, at time.Time) {
	cs.nextMu.Lock()
	cs.next = complexitySuggestionNext{hash: hash, at: at}
	cs.nextMu.Unlock()
}

// errSuggestionsClosing is a run that reached a plugin being cleaned up.
var errSuggestionsClosing = errors.New("the routing plugin is shutting down")

// beginRun registers a run, whether this node starts it or a job runner hands
// it over, with the plugin's lifetime. The returned context ends when parent
// does or when the plugin is cleaned up, and done must be called when the run
// ends. Cleanup closes the classifier whose serving space a run reads, so it
// cancels and waits for every registered run first. Registration and close
// share mu, so a run is either registered before close starts waiting or
// refused: never added to the wait group while close is already in Wait.
func (cs *complexitySuggestions) beginRun(parent context.Context) (context.Context, func(), error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.closed {
		return nil, nil, errSuggestionsClosing
	}
	cs.wg.Add(1)
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(cs.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
		cs.wg.Done()
	}, nil
}

type complexitySuggestionDeps struct {
	store suggestrun.Store
	texts ComplexitySuggestionTexts
}

type complexitySuggestionJobs struct {
	queue ComplexitySuggestionJobQueue
	store ComplexitySuggestionJobStore
}

// complexitySuggestionJobMeta is a run job's metadata.
type complexitySuggestionJobMeta struct {
	RunID       string `json:"run_id"`
	TriggeredBy string `json:"triggered_by"`
	Message     string `json:"message,omitempty"`
}

// startComplexitySuggestions starts the schedule check. tick <= 0 starts no
// ticker, for tests that drive the check themselves.
func (p *RoutingPlugin) startComplexitySuggestions(ctx context.Context, tick time.Duration) {
	cs := &complexitySuggestions{}
	cs.ctx, cs.cancel = context.WithCancel(ctx)
	p.suggestions = cs
	if tick <= 0 {
		return
	}
	cs.wg.Go(func() {
		timer := time.NewTimer(tick + rand.N(suggestionScheduleJitter))
		defer timer.Stop()
		for {
			select {
			case <-cs.ctx.Done():
				return
			case now := <-timer.C:
				if err := p.scheduleComplexitySuggestions(cs.ctx, now); err != nil {
					p.logger.Warn("[Routing] phrase suggestion schedule check failed: %v", err)
				}
				timer.Reset(tick + rand.N(suggestionScheduleJitter))
			}
		}
	})
}

// closeComplexitySuggestions stops the schedule check and every run in
// progress here, whether this node started it or a job runner handed it over,
// and waits for them; runs that arrive afterwards are refused.
func (p *RoutingPlugin) closeComplexitySuggestions() {
	cs := p.suggestions
	if cs == nil {
		return
	}
	cs.mu.Lock()
	cs.closed = true
	cs.cancel()
	cs.mu.Unlock()
	cs.wg.Wait()
}

// SetComplexitySuggestionStore supplies where suggestions and runs are kept
// and how request text is read back from the logs. Without it no run starts.
func (p *RoutingPlugin) SetComplexitySuggestionStore(store suggestrun.Store, texts ComplexitySuggestionTexts) {
	if p.suggestions == nil {
		return
	}
	if store == nil || texts == nil {
		p.suggestions.deps.Store(nil)
		return
	}
	p.suggestions.deps.Store(&complexitySuggestionDeps{store: store, texts: texts})
}

// SetComplexitySuggestionJobs supplies the job runner runs are queued on and
// the store that says which jobs are in flight. Without them a node runs its
// slots itself.
func (p *RoutingPlugin) SetComplexitySuggestionJobs(queue ComplexitySuggestionJobQueue, store ComplexitySuggestionJobStore) {
	if p.suggestions == nil {
		return
	}
	if queue == nil || store == nil {
		p.suggestions.jobs.Store(nil)
		return
	}
	p.suggestions.jobs.Store(&complexitySuggestionJobs{queue: queue, store: store})
}

// SetComplexitySuggestionSchedules supplies where schedule state is kept and
// due runs are claimed. Without it a node keeps the schedule in memory.
func (p *RoutingPlugin) SetComplexitySuggestionSchedules(store ComplexitySuggestionScheduleStore) {
	if p.suggestions == nil {
		return
	}
	if store == nil {
		p.suggestions.schedules.Store(nil)
		return
	}
	p.suggestions.schedules.Store(&store)
}

func (cs *complexitySuggestions) scheduleStore() ComplexitySuggestionScheduleStore {
	if store := cs.schedules.Load(); store != nil {
		return *store
	}
	return nil
}

// scheduleComplexitySuggestions claims the schedule's run if it is due, and
// starts or records it.
func (p *RoutingPlugin) scheduleComplexitySuggestions(ctx context.Context, now time.Time) error {
	cs := p.suggestions
	config := p.complexityConfig.Load()
	if cs == nil || !config.SuggestionsEnabled() {
		return nil
	}
	deps := cs.deps.Load()
	if deps == nil {
		return nil
	}
	// Nothing is captured without a capture store, and that does not pass
	// the way warming does, so the run is neither claimed nor recorded as
	// skipped every interval; the status says why instead.
	if p.semanticClassifier != nil && !p.semanticClassifier.Status().CaptureAvailable {
		return nil
	}
	schedule := config.Suggestions.EffectiveSchedule()
	hash := complexitySuggestionScheduleHash(schedule)
	due, version, isDue, err := cs.dueRun(ctx, schedule, hash, now)
	if err != nil || !isDue {
		return err
	}
	next := schedule.NextSlot(now)
	runID := complexitySuggestionSlotRunID(due)
	if store := cs.scheduleStore(); store != nil {
		claimed, err := store.ClaimComplexitySuggestionRun(ctx, complexitySuggestionScheduleKey, version, due, next, runID)
		if err != nil {
			return err
		}
		cs.rememberNext(hash, next)
		if !claimed {
			return nil // another node took it
		}
	} else {
		cs.rememberNext(hash, next)
	}
	// A router still warming (after a restart, or a phrase change) has no
	// space to suggest against: the run is recorded as skipped, so the
	// schedule never claims to have run when it did not.
	if p.semanticClassifier == nil || p.semanticClassifier.Status().State != complexity.SemanticStatusReady {
		return recordSkippedSuggestionRun(ctx, deps.store, runID, logstore.ComplexitySuggestionTriggerSchedule, "the complexity router was still warming up")
	}
	return p.startComplexitySuggestionRun(ctx, runID, logstore.ComplexitySuggestionTriggerSchedule)
}

// dueRun reports the run due now, with the schedule row version to claim it
// at. The row is read only once the run this node last saw is due, or the
// schedule's settings changed; a row missing or computed from other settings
// is set to the next run after now, so a new or changed schedule waits for its
// next run rather than running at once.
func (cs *complexitySuggestions) dueRun(ctx context.Context, schedule configstore.ComplexitySuggestionsSchedule, hash string, now time.Time) (time.Time, int64, bool, error) {
	cached := cs.cachedNext()
	if cached.hash == hash && now.Before(cached.at) {
		return time.Time{}, 0, false, nil
	}
	store := cs.scheduleStore()
	if store == nil {
		if cached.hash != hash {
			cs.rememberNext(hash, schedule.NextSlot(now))
			return time.Time{}, 0, false, nil
		}
		return cached.at, 0, true, nil
	}
	row, err := store.GetComplexitySuggestionSchedule(ctx, complexitySuggestionScheduleKey)
	if err != nil {
		return time.Time{}, 0, false, err
	}
	if row == nil || row.ScheduleHash != hash {
		next := schedule.NextSlot(now)
		var readVersion *int64
		if row != nil {
			readVersion = &row.Version
		}
		set, err := store.SetComplexitySuggestionSchedule(ctx, complexitySuggestionScheduleKey, hash, next, readVersion)
		if err != nil {
			return time.Time{}, 0, false, err
		}
		if set {
			cs.rememberNext(hash, next)
			return time.Time{}, 0, false, nil
		}
		// Another node wrote the row since it was read, and may already have
		// claimed a slot: read what it wrote rather than overwrite it.
		if row, err = store.GetComplexitySuggestionSchedule(ctx, complexitySuggestionScheduleKey); err != nil {
			return time.Time{}, 0, false, err
		}
		if row == nil || row.ScheduleHash != hash {
			return time.Time{}, 0, false, nil // the next check sets it again
		}
	}
	cs.rememberNext(hash, row.NextRunAt)
	if now.Before(row.NextRunAt) {
		return time.Time{}, 0, false, nil
	}
	return row.NextRunAt, row.Version, true, nil
}

// recordSkippedSuggestionRun records a run that was due but could not start.
func recordSkippedSuggestionRun(ctx context.Context, store suggestrun.Store, runID, triggeredBy, reason string) error {
	now := time.Now().UTC()
	run := &logstore.ComplexitySuggestionRun{ID: runID, TriggeredBy: triggeredBy, Status: logstore.ComplexitySuggestionRunSkipped,
		Error: reason, StartedAt: now, WindowStart: now, FinishedAt: &now}
	if err := store.UpsertComplexitySuggestionRun(ctx, run); err != nil {
		return fmt.Errorf("record skipped suggestion run: %w", err)
	}
	return nil
}

// recordQueuedSuggestionRun records a run handed to the job runner or this
// node, before either starts it, so a run is visible from the moment it is
// taken; the run itself then records its progress under the same ID.
func recordQueuedSuggestionRun(ctx context.Context, store suggestrun.Store, runID, triggeredBy string) error {
	now := time.Now().UTC()
	run := &logstore.ComplexitySuggestionRun{ID: runID, TriggeredBy: triggeredBy, Status: logstore.ComplexitySuggestionRunQueued,
		StartedAt: now, WindowStart: now}
	if err := store.UpsertComplexitySuggestionRun(ctx, run); err != nil {
		return fmt.Errorf("record queued suggestion run: %w", err)
	}
	return nil
}

// suggestionRecordTimeout bounds writing a run's outcome.
const suggestionRecordTimeout = 30 * time.Second

// suggestionRecordContext is the context a run's outcome is written on: it
// outlives the run's own, whose ending may be why the run is being recorded,
// as suggestrun.Run does for the outcomes it writes.
func suggestionRecordContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), suggestionRecordTimeout)
}

// startComplexitySuggestionRun records a run as queued and hands it to the
// job runner, whose partition runs one suggestion run at a time, or starts it
// here when there is no job runner. A node already running one itself records
// this one as skipped.
func (p *RoutingPlugin) startComplexitySuggestionRun(ctx context.Context, runID, triggeredBy string) error {
	cs := p.suggestions
	deps := cs.deps.Load()
	if deps == nil {
		return nil
	}
	if jobs := cs.jobs.Load(); jobs != nil {
		meta, err := sonic.MarshalString(complexitySuggestionJobMeta{RunID: runID, TriggeredBy: triggeredBy})
		if err != nil {
			return err
		}
		if err := recordQueuedSuggestionRun(ctx, deps.store, runID, triggeredBy); err != nil {
			return err
		}
		err = jobs.queue.EnqueuePartitioned(ctx, runID, ComplexitySuggestionsJobKind, ComplexitySuggestionsJobKind, meta, "")
		if err != nil && !sidekiq.IsDuplicateJobError(err) {
			// Recorded as queued above, the run would otherwise read as
			// waiting for a job that does not exist.
			err = fmt.Errorf("queue suggestion run: %w", err)
			recordCtx, cancel := suggestionRecordContext(ctx)
			defer cancel()
			if recordErr := recordSkippedSuggestionRun(recordCtx, deps.store, runID, triggeredBy, "the run could not be queued: "+err.Error()); recordErr != nil {
				err = errors.Join(err, recordErr)
			}
			return err
		}
		return nil
	}
	if !cs.inline.CompareAndSwap(false, true) {
		return recordSkippedSuggestionRun(ctx, deps.store, runID, triggeredBy, "a suggestion run was still in progress")
	}
	runCtx, done, err := cs.beginRun(cs.ctx)
	if err != nil {
		cs.inline.Store(false)
		return nil // closing
	}
	if err := recordQueuedSuggestionRun(ctx, deps.store, runID, triggeredBy); err != nil {
		done()
		cs.inline.Store(false)
		return err
	}
	go func() {
		defer done()
		defer cs.inline.Store(false)
		_, err := p.runComplexitySuggestions(runCtx, runID, triggeredBy)
		if errors.Is(err, errSuggestionsUnavailable) {
			// Turned off between queueing and starting, as a job can find them:
			// the run recorded as queued above is closed as skipped.
			recordCtx, cancel := suggestionRecordContext(runCtx)
			defer cancel()
			err = recordSkippedSuggestionRun(recordCtx, deps.store, runID, triggeredBy, suggestionsTurnedOffReason)
		}
		if err != nil {
			p.logger.Warn("[Routing] phrase suggestion run %s failed: %v", runID, err)
		}
	}()
	return nil
}

// RunComplexitySuggestionsJob is the sidekiq handler for a queued run.
func (p *RoutingPlugin) RunComplexitySuggestionsJob(ctx context.Context, job tables.TableSidekiqJob, _ sidekiq.ProgressFunc) (string, error) {
	meta := decodeComplexitySuggestionJobMeta(job)
	var result suggestrun.Result
	var err error
	if cs := p.suggestions; cs != nil {
		// Tracked like a run this node starts, so cleanup stops it before the
		// classifier it reads closes.
		runCtx, done, beginErr := cs.beginRun(ctx)
		if beginErr != nil {
			// The plugin is closing: the run recorded as queued is closed as
			// skipped, on the job's own context since there is no run context.
			err = beginErr
			if deps := cs.deps.Load(); deps != nil {
				recordCtx, cancel := suggestionRecordContext(ctx)
				if recordErr := recordSkippedSuggestionRun(recordCtx, deps.store, meta.RunID, meta.TriggeredBy, "the routing plugin was shutting down"); recordErr != nil {
					err = errors.Join(err, recordErr)
				}
				cancel()
			}
		} else {
			ctx = runCtx
			defer done()
		}
	}
	if err == nil {
		result, err = p.runComplexitySuggestions(ctx, meta.RunID, meta.TriggeredBy)
	}
	switch {
	case errors.Is(err, errSuggestionsUnavailable):
		// Suggestions were turned off, or this node has nowhere to keep them,
		// between queueing and running: nothing to do is not a failure. The
		// run recorded as queued is closed as skipped, where it can be, on a
		// context of its own. A skip that cannot be recorded is a failure: the
		// run would read queued, and keep the next ones waiting behind it.
		meta.Message, err = err.Error(), nil
		if cs := p.suggestions; cs != nil {
			if deps := cs.deps.Load(); deps != nil {
				recordCtx, cancel := suggestionRecordContext(ctx)
				err = recordSkippedSuggestionRun(recordCtx, deps.store, meta.RunID, meta.TriggeredBy, suggestionsTurnedOffReason)
				cancel()
			}
		}
	case err != nil:
		meta.Message = "Suggestion run failed: " + err.Error()
	default:
		meta.Message = fmt.Sprintf("Read %d requests into %d clusters: %d new suggestions, %d refreshed, %d superseded.",
			result.Run.RequestsRead, result.Run.Clusters, result.Run.Created, result.Run.Refreshed, result.Run.Superseded)
	}
	encoded, encodeErr := sonic.MarshalString(meta)
	if encodeErr != nil {
		encoded = job.Metadata
	}
	return encoded, err
}

// decodeComplexitySuggestionJobMeta reads a run job's metadata, falling back
// to the job's own ID for a job queued without it.
func decodeComplexitySuggestionJobMeta(job tables.TableSidekiqJob) complexitySuggestionJobMeta {
	var meta complexitySuggestionJobMeta
	if err := sonic.UnmarshalString(job.Metadata, &meta); err != nil || meta.RunID == "" {
		meta = complexitySuggestionJobMeta{RunID: job.ID, TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule}
	}
	return meta
}

// SkipComplexitySuggestionsJob closes the run a queued job was recorded as,
// as skipped with reason, when there is nothing to run it: the routing plugin
// was removed or disabled after the run was queued. Nothing to run is not a
// job failure, so it returns the job's metadata with the reason as its
// message; the error is only a failure to record the run.
func SkipComplexitySuggestionsJob(ctx context.Context, store suggestrun.Store, job tables.TableSidekiqJob, reason string) (string, error) {
	meta := decodeComplexitySuggestionJobMeta(job)
	meta.Message = reason
	encoded, err := sonic.MarshalString(meta)
	if err != nil {
		encoded = job.Metadata
	}
	if store == nil {
		return encoded, nil
	}
	recordCtx, cancel := suggestionRecordContext(ctx)
	defer cancel()
	return encoded, recordSkippedSuggestionRun(recordCtx, store, meta.RunID, meta.TriggeredBy, reason)
}

// SummarizeComplexitySuggestionsJob reports a run job's outcome for generic
// job views.
func SummarizeComplexitySuggestionsJob(metadata string) sidekiq.JobSummary {
	var meta complexitySuggestionJobMeta
	if sonic.UnmarshalString(metadata, &meta) != nil {
		return sidekiq.JobSummary{}
	}
	return sidekiq.JobSummary{Message: meta.Message}
}

// suggestionsTurnedOffReason closes a queued run that found suggestions
// turned off when it started.
const suggestionsTurnedOffReason = "phrase suggestions were turned off before the run started"

// errSuggestionsUnavailable is a run that had nothing to run against.
var errSuggestionsUnavailable = errors.New("phrase suggestions are off or have nowhere to be kept")

// runComplexitySuggestions runs one suggestion run against the serving
// embedding space with the current settings.
func (p *RoutingPlugin) runComplexitySuggestions(ctx context.Context, runID, triggeredBy string) (suggestrun.Result, error) {
	config := p.complexityConfig.Load()
	var deps *complexitySuggestionDeps
	if p.suggestions != nil {
		deps = p.suggestions.deps.Load()
	}
	if !config.SuggestionsEnabled() || deps == nil || p.semanticClassifier == nil {
		return suggestrun.Result{}, errSuggestionsUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, suggestionRunTimeout)
	defer cancel()
	settings := config.Suggestions
	s := suggestrun.Settings{
		RunID: runID, TriggeredBy: triggeredBy,
		Lookback:           time.Duration(settings.LookbackDays) * 24 * time.Hour,
		MaxClustered:       settings.MaxClusteredRequests,
		MinClusterRequests: settings.MinClusterRequests,
		Instructions:       settings.Instructions,
		Seed:               time.Now().UnixNano(),
	}
	if config.LLM != nil {
		s.Guidance = config.LLM.Prompt
	}
	provider, model := settings.Provider, settings.Model
	if model == "" && config.LLM != nil {
		provider, model = config.LLM.Provider, config.LLM.Model
	}
	var llm suggest.LLM
	if model != "" {
		s.Model = string(provider) + "/" + model
		adapter := &suggestionLLM{p: p, provider: provider, model: model}
		llm, s.Cost = adapter, adapter.totalCost
	}

	space, err := p.semanticClassifier.SuggestionSpace(ctx)
	if err != nil {
		// Recorded, so a run that could not start reads as failed rather than
		// never having happened, and the schedule does not retry it every
		// minute.
		run := &logstore.ComplexitySuggestionRun{ID: runID, TriggeredBy: triggeredBy, Status: logstore.ComplexitySuggestionRunFailed, Model: s.Model, Error: err.Error()}
		finished := time.Now().UTC()
		run.StartedAt, run.FinishedAt = finished, &finished
		recordCtx, cancelRecord := suggestionRecordContext(ctx)
		defer cancelRecord()
		if recordErr := deps.store.UpsertComplexitySuggestionRun(recordCtx, run); recordErr != nil {
			err = errors.Join(err, recordErr)
		}
		return suggestrun.Result{Run: *run}, err
	}
	defer space.Close()
	texts := func(ctx context.Context, ids []string) (map[string]string, error) {
		return deps.texts(ctx, ids, space.MessageHistoryCount)
	}
	result, err := suggestrun.Run(ctx, space, deps.store, texts, llm, s)
	if err == nil && result.Unmatched > 0 {
		p.logger.Info("[Routing] phrase suggestion run %s: %d of %d assigned requests matched no cluster; raise max_clustered_requests to cluster more of the window",
			runID, result.Unmatched, result.Assigned)
	}
	return result, err
}

// suggestionLLM is the suggestion model, called the way the llm fallback
// classifier is: an internal chat completion, retried on the Responses API
// when the model needs it. It adds up what its calls cost.
type suggestionLLM struct {
	p        *RoutingPlugin
	provider schemas.ModelProvider
	model    string

	mu   sync.Mutex
	cost float64
}

func (l *suggestionLLM) totalCost() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cost
}

// CompleteJSON implements suggest.LLM.
func (l *suggestionLLM) CompleteJSON(ctx context.Context, messages []suggest.Message) (string, error) {
	executor := l.p.chatExecutor()
	if executor == nil {
		return "", ErrChatRequestExecutorNotConfigured
	}
	chatCtx := schemas.NewBifrostContext(ctx, time.Now().Add(suggestionLLMTimeout))
	defer chatCtx.Cancel()
	bifrost.PrepareContextForInternalRequest(chatCtx)

	input := make([]schemas.ChatMessage, 0, len(messages))
	for _, m := range messages {
		content := m.Content
		role := schemas.ChatMessageRoleUser
		switch m.Role {
		case "system":
			role = schemas.ChatMessageRoleSystem
		case "assistant":
			role = schemas.ChatMessageRoleAssistant
		}
		input = append(input, schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: &content}})
	}
	maxTokens := suggestionLLMMaxTokens
	req := &schemas.BifrostChatRequest{
		Provider: l.provider, Model: l.model, Input: input,
		Params: &schemas.ChatParameters{MaxCompletionTokens: &maxTokens},
	}
	response, bifrostErr := l.p.runClassifierCompletion(chatCtx, executor, req)
	if bifrostErr != nil {
		return "", fmt.Errorf("suggestion model call failed: %v", bifrostErr.GetErrorString())
	}
	if response == nil {
		return "", errors.New("suggestion model returned no response")
	}
	cost := chatCtx.CalculateCost(&schemas.BifrostResponse{ChatResponse: response})
	l.mu.Lock()
	l.cost += cost
	l.mu.Unlock()
	text := chatResponseText(response)
	if strings.TrimSpace(text) == "" {
		return "", errors.New("suggestion model response contained no text")
	}
	return text, nil
}
