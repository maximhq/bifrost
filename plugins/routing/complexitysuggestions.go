package routing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
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
// Each router's schedule state lives in the config store, one row per router:
// when its next run is due, and a version every write bumps. Every node checks
// about once a minute, reading a router's row only once its cached next run
// has come (or the router's schedule settings changed). A node that finds a
// run due claims it by moving the next run on while the version still matches,
// so exactly one node takes it, and the next run is counted from now, so a
// gateway that was down for several runs catches up with one.
//
// The node that claims a run records it, queued, before handing it to the job
// runner, or skipped with its reason when it cannot start (the router is
// still warming, or one of its runs is still in progress). Without a config
// store there is no one to coordinate with: the node keeps each router's next
// run in memory and runs it itself.

// ComplexitySuggestionsJobKind is the sidekiq kind a suggestion run is queued
// as.
const ComplexitySuggestionsJobKind = "model_routing_suggestions"

// complexitySuggestionRunNamespace scopes the deterministic IDs of scheduled
// suggestion runs.
var complexitySuggestionRunNamespace = uuid.MustParse("e0cfefa2-278e-44ef-8e11-27edf6602d00")

// complexitySuggestionSlotRunID is the run and job ID of one router's
// scheduled slot. It is the same on every node, which is what keeps a slot
// from being queued twice, differs between routers due at the same slot, and
// is a UUID, so it fits the run table's key.
func complexitySuggestionSlotRunID(routerID string, slot time.Time) string {
	return uuid.NewSHA1(complexitySuggestionRunNamespace, []byte(fmt.Sprintf("%s\x00%s\x00%d", ComplexitySuggestionsJobKind, routerID, slot.Unix()))).String()
}

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
	// Cancel stops a queued or running job, on whichever node runs it, and
	// reports whether this call did.
	Cancel(ctx context.Context, id string) (bool, error)
}

// ComplexitySuggestionJobStore reports queued and running jobs, and the
// latest job of a kind. The config store is one.
type ComplexitySuggestionJobStore interface {
	GetInFlightSidekiqJobByKind(ctx context.Context, kind string) (*tables.TableSidekiqJob, error)
	GetLatestSidekiqJobByKind(ctx context.Context, kind string) (*tables.TableSidekiqJob, error)
	// GetSidekiqJob reads one job, so a run that stopped early can tell a
	// user's cancel from a shutdown.
	GetSidekiqJob(ctx context.Context, id string) (*tables.TableSidekiqJob, error)
}

// ComplexitySuggestionScheduleStore keeps each router's schedule state and
// lets nodes claim a due run. The config store is one.
type ComplexitySuggestionScheduleStore interface {
	GetComplexitySuggestionSchedule(ctx context.Context, routerID string) (*tables.TableComplexitySuggestionSchedule, error)
	SetComplexitySuggestionSchedule(ctx context.Context, routerID, scheduleHash string, nextRunAt time.Time, readVersion *int64) (bool, error)
	ClaimComplexitySuggestionRun(ctx context.Context, routerID string, version int64, dueAt, nextRunAt time.Time, runID string) (bool, error)
}

// complexitySuggestions is the plugin's suggestion-run state.
type complexitySuggestions struct {
	backfills sync.Map // router id -> chan struct{}: one backfill per router at a time
	deps      atomic.Pointer[complexitySuggestionDeps]
	jobs      atomic.Pointer[complexitySuggestionJobs]
	schedules atomic.Pointer[ComplexitySuggestionScheduleStore]
	// inline guards the run a node without a job runner does itself, per
	// router.
	inlineMu sync.Mutex
	inline   map[string]bool
	// inlineCancels stops an inline run by its run ID, and cancelled marks
	// the ones a user stopped, so the run records itself as cancelled.
	inlineCancels map[string]context.CancelFunc
	cancelled     map[string]bool

	// next is this node's view of each router's schedule: the next run of the
	// settings hash names. With a schedule store it only saves reading the
	// row before the run is due; without one it is the schedule.
	nextMu sync.Mutex
	next   map[string]complexitySuggestionNext

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

func (cs *complexitySuggestions) cachedNext(routerID string) complexitySuggestionNext {
	cs.nextMu.Lock()
	defer cs.nextMu.Unlock()
	return cs.next[routerID]
}

func (cs *complexitySuggestions) rememberNext(routerID, hash string, at time.Time) {
	cs.nextMu.Lock()
	cs.next[routerID] = complexitySuggestionNext{hash: hash, at: at}
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
	RunID         string `json:"run_id"`
	RouterID      string `json:"router_id"`
	TriggeredBy   string `json:"triggered_by"`
	WindowSeconds int64  `json:"window_seconds,omitempty"` // 0 reads the whole lookback
	Message       string `json:"message,omitempty"`
}

// suggestionRouter is one model router suggestions are made for: its settings
// and the classifier serving its phrase bank, which captures its requests.
type suggestionRouter struct {
	id         string
	config     *complexity.AnalyzerConfig
	classifier *complexity.SemanticClassifier
	capture    *complexity.RequestCapture // where a backfill from logs writes
}

// ErrUnknownComplexityRouter is a suggestion call for a router that does not
// exist.
var ErrUnknownComplexityRouter = errors.New("no such model router")

// ErrComplexitySuggestionWindow is a run window outside what is kept.
var ErrComplexitySuggestionWindow = errors.New("the run window must be at least one second and no longer than the suggestions lookback")

// suggestionRouters lists the routers suggestions are made for: every model
// router this node serves, by ID. Whether one has suggestions on, and is
// semantic, is checked where it matters (a run, the schedule), so a router
// with them off still lists what it had. The Complexity Router page's config
// is not one: it captures nothing.
func (p *RoutingPlugin) suggestionRouters() []suggestionRouter {
	routers := p.modelRouters()
	out := make([]suggestionRouter, 0, len(routers))
	for _, rt := range routers {
		out = append(out, suggestionRouterOf(rt))
	}
	slices.SortFunc(out, func(a, b suggestionRouter) int { return strings.Compare(a.id, b.id) })
	return out
}

// suggestionRouter finds one router by ID.
func (p *RoutingPlugin) suggestionRouter(id string) (suggestionRouter, bool) {
	rt, ok := p.modelRouter(id)
	if !ok {
		return suggestionRouter{}, false
	}
	return suggestionRouterOf(rt), true
}

func suggestionRouterOf(rt *modelRouterRuntime) suggestionRouter {
	return suggestionRouter{id: rt.id, config: rt.config.Load(), classifier: rt.semantic, capture: rt.capture}
}

// complexitySuggestionPartition keeps one router's runs in order and apart
// from every other router's.
func complexitySuggestionPartition(routerID string) string {
	return ComplexitySuggestionsJobKind + ":" + routerID
}

// startComplexitySuggestions starts the schedule check. tick <= 0 starts no
// ticker, for tests that drive the check themselves.
func (p *RoutingPlugin) startComplexitySuggestions(ctx context.Context, tick time.Duration) {
	cs := &complexitySuggestions{inline: map[string]bool{}, inlineCancels: map[string]context.CancelFunc{}, cancelled: map[string]bool{},
		next: map[string]complexitySuggestionNext{}}
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

// scheduleComplexitySuggestions claims, for each router, its schedule's run if
// it is due, and starts or records it.
func (p *RoutingPlugin) scheduleComplexitySuggestions(ctx context.Context, now time.Time) error {
	if p.suggestions == nil {
		return nil
	}
	var errs []error
	for _, router := range p.suggestionRouters() {
		if err := p.scheduleRouterSuggestions(ctx, router, now); err != nil {
			errs = append(errs, fmt.Errorf("router %s: %w", router.id, err))
		}
	}
	return errors.Join(errs...)
}

func (p *RoutingPlugin) scheduleRouterSuggestions(ctx context.Context, router suggestionRouter, now time.Time) error {
	cs := p.suggestions
	deps := cs.deps.Load()
	if deps == nil || !router.config.SuggestionsEnabled() {
		return nil
	}
	// Nothing is captured without a capture store, and that does not pass
	// the way warming does, so the run is neither claimed nor recorded as
	// skipped every interval; the status says why instead.
	if router.classifier != nil && !router.classifier.Status().CaptureAvailable {
		return nil
	}
	schedule := router.config.Suggestions.EffectiveSchedule()
	hash := complexitySuggestionScheduleHash(schedule)
	due, version, isDue, err := cs.dueRun(ctx, router.id, schedule, hash, now)
	if err != nil || !isDue {
		return err
	}
	next := schedule.NextSlot(now)
	runID := complexitySuggestionSlotRunID(router.id, due)
	if store := cs.scheduleStore(); store != nil {
		claimed, err := store.ClaimComplexitySuggestionRun(ctx, router.id, version, due, next, runID)
		if err != nil {
			return err
		}
		cs.rememberNext(router.id, hash, next)
		if !claimed {
			return nil // another node took it
		}
	} else {
		cs.rememberNext(router.id, hash, next)
	}
	// The slot is taken from here on, so its run is queued or recorded on a
	// context that outlives the scheduler's: a shutdown ending that one is one
	// of the outcomes to record, and a slot claimed with no record of its run
	// would read as a run that never happened.
	recordCtx, cancel := suggestionRecordContext(ctx)
	defer cancel()
	trigger := logstore.ComplexitySuggestionTriggerSchedule
	// A router still warming (after a restart, or a phrase change) has no
	// space to suggest against: the run is recorded as skipped, so the
	// schedule never claims to have run when it did not.
	if router.classifier == nil || router.classifier.Status().State != complexity.SemanticStatusReady {
		return recordSkippedSuggestionRun(recordCtx, deps.store, runID, router.id, trigger, "the complexity router was still warming up")
	}
	started, err := p.startComplexitySuggestionRun(recordCtx, complexitySuggestionJobMeta{RunID: runID, RouterID: router.id, TriggeredBy: trigger})
	switch {
	case errors.Is(err, errSuggestionsClosing):
		return recordSkippedSuggestionRun(recordCtx, deps.store, runID, router.id, trigger, "the gateway was shutting down")
	case err != nil:
		return err
	case !started:
		return recordSkippedSuggestionRun(recordCtx, deps.store, runID, router.id, trigger, "a suggestion run was still in progress")
	}
	return nil
}

// dueRun reports a router's run due now, with the schedule row version to
// claim it at. The row is read only once the run this node last saw is due,
// or the schedule's settings changed; a row missing or computed from other
// settings is set to the next run after now, so a new or changed schedule
// waits for its next run rather than running at once.
func (cs *complexitySuggestions) dueRun(ctx context.Context, routerID string, schedule configstore.ComplexitySuggestionsSchedule, hash string, now time.Time) (time.Time, int64, bool, error) {
	cached := cs.cachedNext(routerID)
	if cached.hash == hash && now.Before(cached.at) {
		return time.Time{}, 0, false, nil
	}
	store := cs.scheduleStore()
	if store == nil {
		if cached.hash != hash {
			cs.rememberNext(routerID, hash, schedule.NextSlot(now))
			return time.Time{}, 0, false, nil
		}
		return cached.at, 0, true, nil
	}
	row, err := store.GetComplexitySuggestionSchedule(ctx, routerID)
	if err != nil {
		return time.Time{}, 0, false, err
	}
	if row == nil || row.ScheduleHash != hash {
		next := schedule.NextSlot(now)
		var readVersion *int64
		if row != nil {
			readVersion = &row.Version
		}
		set, err := store.SetComplexitySuggestionSchedule(ctx, routerID, hash, next, readVersion)
		if err != nil {
			return time.Time{}, 0, false, err
		}
		if set {
			cs.rememberNext(routerID, hash, next)
			return time.Time{}, 0, false, nil
		}
		// Another node wrote the row since it was read, and may already have
		// claimed a slot: read what it wrote rather than overwrite it.
		if row, err = store.GetComplexitySuggestionSchedule(ctx, routerID); err != nil {
			return time.Time{}, 0, false, err
		}
		if row == nil || row.ScheduleHash != hash {
			return time.Time{}, 0, false, nil // the next check sets it again
		}
	}
	cs.rememberNext(routerID, hash, row.NextRunAt)
	if now.Before(row.NextRunAt) {
		return time.Time{}, 0, false, nil
	}
	return row.NextRunAt, row.Version, true, nil
}

// NextComplexitySuggestionRun is when a router's schedule runs next: the next
// run its state has once a node has set it for these settings, else the next
// one the settings give; a run already due is due now.
func NextComplexitySuggestionRun(ctx context.Context, store ComplexitySuggestionScheduleStore, routerID string, schedule configstore.ComplexitySuggestionsSchedule, now time.Time) time.Time {
	next := schedule.NextSlot(now)
	if store != nil {
		if row, err := store.GetComplexitySuggestionSchedule(ctx, routerID); err == nil && row != nil && row.ScheduleHash == complexitySuggestionScheduleHash(schedule) {
			next = row.NextRunAt
		}
	}
	if next.Before(now) {
		next = now
	}
	return next.UTC()
}

// recordSkippedSuggestionRun records a run that was due but could not start.
func recordSkippedSuggestionRun(ctx context.Context, store suggestrun.Store, runID, routerID, triggeredBy, reason string) error {
	now := time.Now().UTC()
	run := &logstore.ComplexitySuggestionRun{ID: runID, RouterID: routerID, TriggeredBy: triggeredBy, Status: logstore.ComplexitySuggestionRunSkipped,
		Error: reason, StartedAt: now, WindowStart: now, FinishedAt: &now}
	if err := store.UpsertComplexitySuggestionRun(ctx, run); err != nil {
		return fmt.Errorf("record skipped suggestion run: %w", err)
	}
	return nil
}

// recordQueuedSuggestionRun records a run handed to the job runner or this
// node, before either starts it, so a run is visible from the moment it is
// taken; the run itself then records its progress under the same ID. Only a
// run not recorded yet is written: two starts that raced to the same ID must
// not have the later one put a run that already ended back to queued.
func recordQueuedSuggestionRun(ctx context.Context, store suggestrun.Store, meta complexitySuggestionJobMeta) error {
	now := time.Now().UTC()
	run := &logstore.ComplexitySuggestionRun{ID: meta.RunID, RouterID: meta.RouterID, TriggeredBy: meta.TriggeredBy, Status: logstore.ComplexitySuggestionRunQueued,
		StartedAt: now, WindowStart: now}
	if _, err := store.InsertComplexitySuggestionRun(ctx, run); err != nil {
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

// ErrComplexitySuggestionsOff is a run asked for while suggestions are off or
// have nowhere to be kept.
var ErrComplexitySuggestionsOff = errors.New("phrase suggestions are off: enable them in the complexity router settings")

// ErrComplexitySuggestionRunInFlight is a run asked for while one is already
// queued or running.
var ErrComplexitySuggestionRunInFlight = errors.New("a phrase suggestion run is already queued or running")

// StartComplexitySuggestionRunNow starts a run for one router outside the
// schedule and returns its ID; the run itself happens in the background.
// window bounds how much of the lookback it reads; zero reads all of it.
func (p *RoutingPlugin) StartComplexitySuggestionRunNow(ctx context.Context, routerID string, window time.Duration) (string, error) {
	router, ok := p.suggestionRouter(routerID)
	if !ok {
		return "", ErrUnknownComplexityRouter
	}
	if p.suggestions == nil || p.suggestions.deps.Load() == nil || !router.config.SuggestionsEnabled() {
		return "", ErrComplexitySuggestionsOff
	}
	if err := checkSuggestionWindow(router, window); err != nil {
		return "", err
	}
	// Refused up front rather than accepted and recorded as a failed run. A
	// previous phrase bank still serving while a new one warms is enough.
	if router.classifier == nil || !router.classifier.Serving() {
		return "", complexity.ErrNoServingGeneration
	}
	if !router.classifier.Status().CaptureAvailable {
		return "", complexity.ErrNoCaptureStore
	}
	runID, err := p.manualSuggestionRunID(ctx, router.id)
	if err != nil {
		return "", err
	}
	started, err := p.startComplexitySuggestionRun(ctx, complexitySuggestionJobMeta{
		RunID: runID, RouterID: router.id, TriggeredBy: logstore.ComplexitySuggestionTriggerManual, WindowSeconds: int64(window / time.Second),
	})
	if err != nil {
		return "", err
	}
	if !started {
		return "", ErrComplexitySuggestionRunInFlight
	}
	return runID, nil
}

// manualSuggestionRunID names a manual run. With a job runner it is derived
// from the latest suggestion job, so starts that race past the in-flight
// check (on this node or another) name the same job: the job table's key
// takes the first, the others get the duplicate, and every caller is handed
// the one run. Once that job exists it is the latest, so the next start names
// a new one. Without a job runner the node's own guard serializes runs, and a
// fresh UUID will do. The router is part of the name, so starts for
// different routers never share a run.
func (p *RoutingPlugin) manualSuggestionRunID(ctx context.Context, routerID string) (string, error) {
	jobs := p.suggestions.jobs.Load()
	if jobs == nil {
		return uuid.NewString(), nil
	}
	latest, err := jobs.store.GetLatestSidekiqJobByKind(ctx, ComplexitySuggestionsJobKind)
	if err != nil {
		return "", fmt.Errorf("read the latest suggestion run: %w", err)
	}
	previous := ""
	if latest != nil {
		previous = latest.ID
	}
	return uuid.NewSHA1(complexitySuggestionRunNamespace, []byte(ComplexitySuggestionsJobKind+"\x00manual\x00"+routerID+"\x00"+previous)).String(), nil
}

// checkSuggestionWindow refuses a window longer than what is kept, and one
// shorter than a second: a run carries its window in whole seconds, so a
// shorter one would arrive as 0, the whole lookback.
func checkSuggestionWindow(router suggestionRouter, window time.Duration) error {
	if window == 0 {
		return nil
	}
	lookback := time.Duration(router.config.Suggestions.LookbackDays) * 24 * time.Hour
	if window < time.Second || window > lookback {
		return ErrComplexitySuggestionWindow
	}
	return nil
}

// EstimateComplexitySuggestionRun sizes a run of one router over window
// without running it: how many requests it would read, cluster and count in,
// and at most how many suggestion-model calls it would make.
func (p *RoutingPlugin) EstimateComplexitySuggestionRun(ctx context.Context, routerID string, window time.Duration) (suggestrun.Estimate, error) {
	router, ok := p.suggestionRouter(routerID)
	if !ok {
		return suggestrun.Estimate{}, ErrUnknownComplexityRouter
	}
	if !router.config.SuggestionsEnabled() {
		return suggestrun.Estimate{}, ErrComplexitySuggestionsOff
	}
	if err := checkSuggestionWindow(router, window); err != nil {
		return suggestrun.Estimate{}, err
	}
	if router.classifier == nil {
		return suggestrun.Estimate{}, complexity.ErrNoServingGeneration
	}
	space, err := router.classifier.SuggestionSpace(ctx)
	if err != nil {
		return suggestrun.Estimate{}, err
	}
	defer space.Close()
	settings := runSettings(router, "", "", window)
	now := time.Now()
	window = settings.Window
	if window <= 0 || window > settings.Lookback {
		window = settings.Lookback
	}
	count, err := space.CountCaptured(ctx, now.Add(-window), now)
	if err != nil {
		return suggestrun.Estimate{}, err
	}
	// What a run would first embed from logs, as it would plan it. A plan
	// that fails leaves the backfill unknown rather than failing the
	// estimate: the run plans again, and the dialog starts a run only on an
	// estimate.
	var backfill int
	backfillUnknown := false
	if deps := p.suggestionDeps(); deps != nil && router.capture != nil {
		logs, _ := deps.store.(ComplexitySuggestionRouterLogs)
		plan, err := p.planBackfill(ctx, router, space, logs, now.Add(-window), now, backfillLimit(router))
		if err != nil {
			p.logger.Warn("[Routing] phrase suggestion estimate for router %s: backfill unknown: %v", routerID, err)
			backfillUnknown = true
		} else {
			backfill = min(plan.missing, backfillLimit(router))
		}
	}
	estimate := suggestrun.EstimateRun(count+backfill, settings)
	estimate.Backfill = backfill
	estimate.BackfillUnknown = backfillUnknown
	return estimate, nil
}

// TestComplexitySuggestionPhrases routes examples with and without phrases
// against one router's serving phrase bank, without saving anything.
func (p *RoutingPlugin) TestComplexitySuggestionPhrases(ctx context.Context, routerID string, phrases []complexity.SuggestionPhrase, examples []suggestrun.TestExample) (suggestrun.PhraseTest, error) {
	router, ok := p.suggestionRouter(routerID)
	if !ok {
		return suggestrun.PhraseTest{}, ErrUnknownComplexityRouter
	}
	if router.classifier == nil {
		return suggestrun.PhraseTest{}, complexity.ErrNoServingGeneration
	}
	// Only the serving bank is read, so no capture store is needed.
	space, err := router.classifier.ServingSpace(ctx)
	if err != nil {
		return suggestrun.PhraseTest{}, err
	}
	defer space.Close()
	return suggestrun.TestPhrases(ctx, space, space.MinSimilarity, phrases, examples)
}

// ComplexitySuggestionMessageHistoryCount is how many user turns a router
// embeds, so request text read back from logs matches what was captured. It
// is the serving generation's count: a config change that has not warmed, or
// failed to, leaves the serving bank classifying with the old one.
func (p *RoutingPlugin) ComplexitySuggestionMessageHistoryCount(routerID string) int {
	router, ok := p.suggestionRouter(routerID)
	if !ok {
		return 1
	}
	if router.classifier != nil {
		if count, serving := router.classifier.ServingMessageHistoryCount(); serving {
			return count
		}
	}
	if router.config != nil && router.config.Semantic != nil {
		return router.config.Semantic.MessageHistoryCount
	}
	return 1
}

// ComplexitySuggestionRouterVersion names the phrase bank a router serves, so
// suggestions made against an earlier one can be shown as such. Empty while
// nothing is serving or the router does not exist.
func (p *RoutingPlugin) ComplexitySuggestionRouterVersion(routerID string) string {
	if router, ok := p.suggestionRouter(routerID); ok && router.classifier != nil {
		return router.classifier.ServingVersion()
	}
	return ""
}

// ComplexitySuggestionRouterExists reports whether suggestions can be kept
// for a router.
func (p *RoutingPlugin) ComplexitySuggestionRouterExists(routerID string) bool {
	_, ok := p.suggestionRouter(routerID)
	return ok
}

// startComplexitySuggestionRun records a router's run as queued and hands it
// to the job runner, or starts it here when there is no job runner, and
// reports whether it did. A run the router already has queued or running is
// not doubled: it reports false, which a scheduled caller records as a
// skipped run and a manual one refuses. Other routers' runs are no reason to
// wait.
func (p *RoutingPlugin) startComplexitySuggestionRun(ctx context.Context, meta complexitySuggestionJobMeta) (bool, error) {
	cs := p.suggestions
	deps := cs.deps.Load()
	if deps == nil {
		return false, errSuggestionsUnavailable
	}
	if jobs := cs.jobs.Load(); jobs != nil {
		busy, err := p.routerSuggestionBusy(ctx, jobs, meta.RouterID)
		if err != nil || busy {
			return false, err
		}
		encoded, err := sonic.MarshalString(meta)
		if err != nil {
			return false, err
		}
		if err := recordQueuedSuggestionRun(ctx, deps.store, meta); err != nil {
			return false, err
		}
		err = jobs.queue.EnqueuePartitioned(ctx, meta.RunID, ComplexitySuggestionsJobKind, complexitySuggestionPartition(meta.RouterID), encoded, "")
		if sidekiq.IsDuplicateJobError(err) {
			return true, nil // this run is already queued
		}
		if err != nil {
			err = fmt.Errorf("queue suggestion run: %w", err)
			recordCtx, cancel := suggestionRecordContext(ctx)
			defer cancel()
			// An enqueue error does not prove no job was written (a commit
			// can land after its acknowledgement is lost), so the job store
			// is asked for this run's own job before the run recorded as
			// queued is closed. Only "no such job" proves it was not created.
			job, lookupErr := jobs.store.GetSidekiqJob(recordCtx, meta.RunID)
			switch {
			case lookupErr != nil:
				return false, errors.Join(err, fmt.Errorf("could not confirm whether the run was queued: %w", lookupErr))
			case job != nil:
				return true, nil // written after all; the dispatcher runs it
			}
			if recordErr := recordSkippedSuggestionRun(recordCtx, deps.store, meta.RunID, meta.RouterID, meta.TriggeredBy, "the run could not be queued: "+err.Error()); recordErr != nil {
				err = errors.Join(err, recordErr)
			}
			return false, err
		}
		return true, nil
	}
	cs.inlineMu.Lock()
	if cs.inline[meta.RouterID] {
		cs.inlineMu.Unlock()
		return false, nil
	}
	runCtx, done, err := cs.beginRun(cs.ctx)
	if err != nil {
		cs.inlineMu.Unlock()
		// Closing: not "already running", so a manual run is told why.
		return false, err
	}
	runCtx, stop := context.WithCancel(runCtx)
	cs.inline[meta.RouterID] = true
	cs.inlineCancels[meta.RunID] = stop
	cs.inlineMu.Unlock()
	forget := func() {
		stop()
		cs.inlineMu.Lock()
		delete(cs.inline, meta.RouterID)
		delete(cs.inlineCancels, meta.RunID)
		delete(cs.cancelled, meta.RunID)
		cs.inlineMu.Unlock()
	}
	if err := recordQueuedSuggestionRun(ctx, deps.store, meta); err != nil {
		forget()
		done()
		return false, err
	}
	go func() {
		defer done()
		defer forget()
		_, err := p.runComplexitySuggestions(runCtx, meta)
		if errors.Is(err, errSuggestionsUnavailable) {
			// Turned off between queueing and starting, as a job can find them:
			// the run recorded as queued above is closed as skipped.
			recordCtx, cancel := suggestionRecordContext(runCtx)
			defer cancel()
			err = recordSkippedSuggestionRun(recordCtx, deps.store, meta.RunID, meta.RouterID, meta.TriggeredBy, suggestionsTurnedOffReason)
		}
		if err != nil {
			p.logger.Warn("[Routing] phrase suggestion run %s failed: %v", meta.RunID, err)
		}
	}()
	return true, nil
}

// suggestionCancelSettleWait bounds how long a cancel waits for a running
// run's handler to record that it stopped, polling every
// suggestionCancelSettlePoll; past it, the record is returned as it stands.
var (
	suggestionCancelSettleWait = 3 * time.Second
	suggestionCancelSettlePoll = 25 * time.Millisecond
)

// ErrUnknownComplexitySuggestionRun is a cancel for a run the router does not
// have.
var ErrUnknownComplexitySuggestionRun = errors.New("no such phrase suggestion run")

// ErrComplexitySuggestionRunEnded is a cancel for a run that already ended.
var ErrComplexitySuggestionRunEnded = errors.New("the phrase suggestion run has already ended")

// CancelComplexitySuggestionRun stops one router's queued or running run and
// returns its record as it now stands. A running run stops at its next check
// and records itself as cancelled, keeping what it had written; a run that
// had not started, or whose job is already gone, is recorded as cancelled
// here.
func (p *RoutingPlugin) CancelComplexitySuggestionRun(ctx context.Context, routerID, runID string) (logstore.ComplexitySuggestionRun, error) {
	if _, ok := p.suggestionRouter(routerID); !ok {
		return logstore.ComplexitySuggestionRun{}, ErrUnknownComplexityRouter
	}
	cs := p.suggestions
	if cs == nil || cs.deps.Load() == nil {
		return logstore.ComplexitySuggestionRun{}, ErrComplexitySuggestionsOff
	}
	store := cs.deps.Load().store
	runs, err := store.ListComplexitySuggestionRuns(ctx, logstore.ComplexitySuggestionRunFilter{RouterID: routerID, IDs: []string{runID}}, 1)
	if err != nil {
		return logstore.ComplexitySuggestionRun{}, fmt.Errorf("read the suggestion run: %w", err)
	}
	if len(runs) == 0 {
		return logstore.ComplexitySuggestionRun{}, ErrUnknownComplexitySuggestionRun
	}
	run := runs[0]
	if run.Status != logstore.ComplexitySuggestionRunQueued && run.Status != logstore.ComplexitySuggestionRunRunning {
		return run, ErrComplexitySuggestionRunEnded
	}
	// Whether a handler is still there to record the outcome itself. An
	// inline run on this node is stopped directly, whether or not the job
	// runner is wired now: one started before it was (at start-up, say) has
	// no job to cancel.
	handlerRecords := false
	cs.inlineMu.Lock()
	if stop, ok := cs.inlineCancels[runID]; ok {
		cs.cancelled[runID] = true
		stop()
		handlerRecords = true
	}
	cs.inlineMu.Unlock()
	if jobs := cs.jobs.Load(); jobs != nil && !handlerRecords {
		stopped, err := jobs.queue.Cancel(ctx, runID)
		if err != nil {
			return run, fmt.Errorf("cancel the suggestion run: %w", err)
		}
		handlerRecords = stopped && run.Status == logstore.ComplexitySuggestionRunRunning
	}
	if handlerRecords {
		return awaitSuggestionRunEnd(ctx, store, routerID, run), nil
	}
	// Written on a context of its own: the job is already cancelled, so a
	// caller that went away must not leave the run reading queued. The write
	// is conditional on the run still being queued or running, so an outcome
	// it wrote first (it ended while the job was being cancelled) is kept, not
	// overwritten with the snapshot read above.
	recordCtx, cancel := suggestionRecordContext(ctx)
	defer cancel()
	finished := time.Now().UTC()
	moved, err := store.UpdateComplexitySuggestionRunStatus(recordCtx, runID,
		[]string{logstore.ComplexitySuggestionRunQueued, logstore.ComplexitySuggestionRunRunning}, logstore.ComplexitySuggestionRunCancelled, &finished)
	if err != nil {
		return run, fmt.Errorf("record the cancelled suggestion run: %w", err)
	}
	// The record as it now stands: cancelled, or the outcome that won. A
	// cancel that landed is not reported as failed for want of this read.
	current, readErr := store.ListComplexitySuggestionRuns(recordCtx, logstore.ComplexitySuggestionRunFilter{RouterID: routerID, IDs: []string{runID}}, 1)
	if readErr == nil && len(current) == 1 {
		run = current[0]
	} else if moved {
		run.Status, run.Stage, run.FinishedAt = logstore.ComplexitySuggestionRunCancelled, "", &finished
	}
	if !moved {
		if readErr != nil {
			return run, fmt.Errorf("read the suggestion run: %w", readErr)
		}
		return run, ErrComplexitySuggestionRunEnded
	}
	return run, nil
}

// awaitSuggestionRunEnd waits, up to suggestionCancelSettleWait, for a
// cancelled run's handler to record that it stopped, and returns the record:
// the handler's outcome once written, or the latest progress read when the
// wait or the caller's context runs out first.
func awaitSuggestionRunEnd(ctx context.Context, store suggestrun.Store, routerID string, run logstore.ComplexitySuggestionRun) logstore.ComplexitySuggestionRun {
	// The reads share the bound, so a stalled one cannot hold the cancel past
	// it when the caller set no deadline.
	ctx, cancel := context.WithTimeout(ctx, suggestionCancelSettleWait)
	defer cancel()
	poll := time.NewTicker(suggestionCancelSettlePoll)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return run
		case <-poll.C:
		}
		runs, err := store.ListComplexitySuggestionRuns(ctx, logstore.ComplexitySuggestionRunFilter{RouterID: routerID, IDs: []string{run.ID}}, 1)
		if err != nil || len(runs) == 0 {
			continue
		}
		run = runs[0]
		if run.Status != logstore.ComplexitySuggestionRunQueued && run.Status != logstore.ComplexitySuggestionRunRunning {
			return run
		}
	}
}

// suggestionRunCancelled reports whether a user cancelled a run: through
// CancelComplexitySuggestionRun on this node for an inline run, or on any
// node for a queued job, whose row then reads cancelled.
func (p *RoutingPlugin) suggestionRunCancelled(ctx context.Context, runID string) bool {
	cs := p.suggestions
	if cs == nil {
		return false
	}
	cs.inlineMu.Lock()
	cancelled := cs.cancelled[runID]
	cs.inlineMu.Unlock()
	if cancelled {
		return true
	}
	jobs := cs.jobs.Load()
	if jobs == nil {
		return false
	}
	job, err := jobs.store.GetSidekiqJob(ctx, runID)
	return err == nil && job != nil && job.Status == tables.SidekiqStatusCancelled
}

// ComplexitySuggestionImpact replays one router's captured requests from the
// last window against a draft phrase bank and the live one, without saving
// anything: what would change tier, and which new phrases take requests the
// live bank placed confidently elsewhere. It returns the window it read: a
// zero window reads the whole lookback. draftMinSimilarity is the floor the
// draft would save, when it changes it; nil replays the draft at the serving
// floor.
func (p *RoutingPlugin) ComplexitySuggestionImpact(ctx context.Context, routerID string, window time.Duration, draft []complexity.SuggestionPhrase, draftMinSimilarity *float64) (suggestrun.Impact, time.Duration, error) {
	router, ok := p.suggestionRouter(routerID)
	if !ok {
		return suggestrun.Impact{}, 0, ErrUnknownComplexityRouter
	}
	if !router.config.SuggestionsEnabled() {
		return suggestrun.Impact{}, 0, ErrComplexitySuggestionsOff
	}
	if err := checkSuggestionWindow(router, window); err != nil {
		return suggestrun.Impact{}, 0, err
	}
	if router.classifier == nil {
		return suggestrun.Impact{}, 0, complexity.ErrNoServingGeneration
	}
	space, err := router.classifier.SuggestionSpace(ctx)
	if err != nil {
		return suggestrun.Impact{}, 0, err
	}
	defer space.Close()
	settings := runSettings(router, "", "", window)
	if window <= 0 || window > settings.Lookback {
		window = settings.Lookback
	}
	now := time.Now()
	// The replay reads the window as a run would: filled from logs first.
	if _, err := p.backfillWindow(ctx, router, space, p.suggestionDeps(), now.Add(-window), now); err != nil {
		return suggestrun.Impact{}, 0, err
	}
	impact, err := suggestrun.ReplayImpact(ctx, space, suggestrun.ImpactSettings{
		From: now.Add(-window), To: now, MinSimilarity: space.MinSimilarity, DraftMinSimilarity: draftMinSimilarity, Draft: draft, Seed: now.UnixNano(),
	})
	return impact, window, err
}

// routerSuggestionBusy reports whether a router has a run queued or running.
// The job store answers per job kind, so a queued job of the kind counts only
// when it is this router's; the router's latest run record, queued or running
// inside the run timeout, covers a job of this router queued behind another
// router's.
func (p *RoutingPlugin) routerSuggestionBusy(ctx context.Context, jobs *complexitySuggestionJobs, routerID string) (bool, error) {
	inFlight, err := jobs.store.GetInFlightSidekiqJobByKind(ctx, ComplexitySuggestionsJobKind)
	if err != nil {
		return false, fmt.Errorf("check for a running suggestion run: %w", err)
	}
	if inFlight != nil {
		var meta complexitySuggestionJobMeta
		if sonic.UnmarshalString(inFlight.Metadata, &meta) != nil || meta.RouterID == "" || meta.RouterID == routerID {
			return true, nil
		}
	}
	deps := p.suggestions.deps.Load()
	if deps == nil {
		return false, nil
	}
	latest, err := deps.store.ListComplexitySuggestionRuns(ctx, logstore.ComplexitySuggestionRunFilter{RouterID: routerID}, 1)
	if err != nil {
		return false, fmt.Errorf("read the latest suggestion run: %w", err)
	}
	if len(latest) == 0 {
		return false, nil
	}
	status := latest[0].Status
	return (status == logstore.ComplexitySuggestionRunQueued || status == logstore.ComplexitySuggestionRunRunning) &&
		time.Since(latest[0].StartedAt) < suggestionRunTimeout, nil
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
				if recordErr := recordSkippedSuggestionRun(recordCtx, deps.store, meta.RunID, meta.RouterID, meta.TriggeredBy, "the routing plugin was shutting down"); recordErr != nil {
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
		result, err = p.runComplexitySuggestions(ctx, meta)
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
				err = recordSkippedSuggestionRun(recordCtx, deps.store, meta.RunID, meta.RouterID, meta.TriggeredBy, suggestionsTurnedOffReason)
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
	if meta.RouterID == "" {
		meta.RouterID = complexity.DefaultRouterID
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
	return encoded, recordSkippedSuggestionRun(recordCtx, store, meta.RunID, meta.RouterID, meta.TriggeredBy, reason)
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

// runSettings builds a router's run settings from its config.
func runSettings(router suggestionRouter, runID, triggeredBy string, window time.Duration) suggestrun.Settings {
	settings := router.config.Suggestions
	s := suggestrun.Settings{
		RunID: runID, TriggeredBy: triggeredBy, RouterID: router.id,
		Lookback:           time.Duration(settings.LookbackDays) * 24 * time.Hour,
		Window:             window,
		MaxClustered:       settings.MaxClusteredRequests,
		MinClusterRequests: settings.MinClusterRequests,
		MaxPhrases:         configstore.MaxComplexitySemanticPhrases,
		Instructions:       settings.Instructions,
		Seed:               time.Now().UnixNano(),
	}
	return s
}

// suggestionTier returns a router's tier settings, normalized; a block saved
// before tiers were configurable decides them with the phrase model.
func suggestionTier(settings *complexity.SuggestionsConfig) configstore.ComplexitySuggestionsTierConfig {
	tier := configstore.ComplexitySuggestionsTierConfig{Classifier: configstore.ComplexitySuggestionsTierLLM}
	if settings.Tier != nil {
		tier = *settings.Tier
	}
	if tier.Classifier == "" {
		tier.Classifier = configstore.ComplexitySuggestionsTierLLM
	}
	if tier.Model == "" && tier.Classifier == configstore.ComplexitySuggestionsTierLLM {
		tier.Provider, tier.Model = settings.Provider, settings.Model
	}
	return tier
}

// SuggestionTierDefaults is what a router's tier step sends when nothing is
// edited, rendered from the router as configured: the llm prompt body, and
// the decision model's per-tier criteria. Both carry, as examples, the phrases
// the router's administrator configured. Configuration surfaces show these in
// the editors, and an unedited run sends exactly them.
func SuggestionTierDefaults(cfg *complexity.AnalyzerConfig) (string, map[string]configstore.ComplexityDecisionTierDefaults) {
	if cfg == nil {
		cfg = &complexity.AnalyzerConfig{}
	}
	examples := suggest.BankTierExamples(cfg.Keywords)
	guidance := ""
	if cfg.LLM != nil {
		guidance = cfg.LLM.Prompt
	}
	criteria := cfg.Decision.ResolvedCriteria()
	for tier, c := range criteria {
		c.Examples = append([]string{}, examples[tier]...)
		criteria[tier] = c
	}
	return suggest.TierPrompt(guidance, examples), criteria
}

// suggestionTierCriteria is what a decision tier step sends: each tier's
// edited criteria as written, the rest generated.
func suggestionTierCriteria(cfg *complexity.AnalyzerConfig, edited map[string]configstore.ComplexityDecisionTierCriteria) map[string]configstore.ComplexityDecisionTierDefaults {
	_, criteria := SuggestionTierDefaults(cfg)
	for tier, c := range edited {
		criteria[tier] = configstore.ComplexityDecisionTierDefaults{Definition: c.Definition, Signals: c.Signals, Examples: c.Examples}
	}
	return criteria
}

// newSuggestionTierDecider builds the tier step a router's settings name, and
// what identifies the text it sends, for the run's prompt hash.
// The criteria are a map, serialized in key order so the key, and the run's
// prompt hash, are the same for the same settings.
func (p *RoutingPlugin) newSuggestionTierDecider(router suggestionRouter) (suggest.TierDecider, string, func() float64, error) {
	tier := suggestionTier(router.config.Suggestions)
	if tier.Model == "" {
		return nil, "", nil, nil
	}
	if tier.Classifier == configstore.ComplexitySuggestionsTierDecision {
		criteria := suggestionTierCriteria(router.config, tier.Criteria)
		encoded, err := providerUtils.MarshalSorted(criteria)
		if err != nil {
			return nil, "", nil, fmt.Errorf("encode the tier criteria: %w", err)
		}
		key := strings.Join([]string{tier.Classifier, string(tier.Provider), tier.Model, suggest.TierInstructions(tier.Instructions), string(encoded)}, "\x00")
		decider := &decisionTierDecider{p: p, provider: tier.Provider, model: tier.Model, instructions: tier.Instructions, criteria: criteria}
		return decider, key, decider.totalCost, nil
	}
	prompt := tier.Prompt
	if prompt == "" {
		prompt, _ = SuggestionTierDefaults(router.config)
	}
	key := strings.Join([]string{tier.Classifier, string(tier.Provider), tier.Model, prompt}, "\x00")
	adapter := &suggestionLLM{p: p, provider: tier.Provider, model: tier.Model}
	return &suggest.LLMTierDecider{LLM: adapter, Prompt: prompt}, key, adapter.totalCost, nil
}

// suggestionDeps is what runs read and write through, nil until set.
func (p *RoutingPlugin) suggestionDeps() *complexitySuggestionDeps {
	if p.suggestions == nil {
		return nil
	}
	return p.suggestions.deps.Load()
}

// runComplexitySuggestions runs one router's suggestion run against its
// serving embedding space with its current settings.
func (p *RoutingPlugin) runComplexitySuggestions(ctx context.Context, meta complexitySuggestionJobMeta) (suggestrun.Result, error) {
	router, ok := p.suggestionRouter(meta.RouterID)
	var deps *complexitySuggestionDeps
	if p.suggestions != nil {
		deps = p.suggestions.deps.Load()
	}
	if !ok || !router.config.SuggestionsEnabled() || deps == nil || router.classifier == nil {
		return suggestrun.Result{}, errSuggestionsUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, suggestionRunTimeout)
	defer cancel()
	s := runSettings(router, meta.RunID, meta.TriggeredBy, time.Duration(meta.WindowSeconds)*time.Second)
	settings := router.config.Suggestions
	var llm suggest.LLM
	var phraseCost func() float64
	if settings.Model != "" {
		s.Model = string(settings.Provider) + "/" + settings.Model
		adapter := &suggestionLLM{p: p, provider: settings.Provider, model: settings.Model}
		llm, phraseCost = adapter, adapter.totalCost
	}

	// A cancel that arrived before the run started ends it here, recorded as
	// cancelled, rather than as a failure to read a space or write a record.
	if ctx.Err() != nil {
		if recordErr := p.recordUnstartedSuggestionRun(ctx, deps.store, meta, router.id, s, ctx.Err()); recordErr != nil {
			return suggestrun.Result{}, recordErr
		}
		if p.suggestionRunCancelled(context.WithoutCancel(ctx), meta.RunID) {
			return suggestrun.Result{}, nil
		}
		return suggestrun.Result{}, ctx.Err()
	}
	space, err := router.classifier.SuggestionSpace(ctx)
	if err != nil {
		return suggestrun.Result{}, errors.Join(err, p.recordUnstartedSuggestionRun(ctx, deps.store, meta, router.id, s, err))
	}
	defer space.Close()
	tier, tierPrompt, tierCost, err := p.newSuggestionTierDecider(router)
	if err != nil {
		return suggestrun.Result{}, errors.Join(err, p.recordUnstartedSuggestionRun(ctx, deps.store, meta, router.id, s, err))
	}
	s.Tier, s.TierPrompt = tier, tierPrompt
	s.Cost = func() float64 {
		var total float64
		for _, cost := range []func() float64{phraseCost, tierCost} {
			if cost != nil {
				total += cost()
			}
		}
		return total
	}
	texts := func(ctx context.Context, ids []string) (map[string]string, error) {
		return deps.texts(ctx, ids, space.MessageHistoryCount)
	}
	s.Cancelled = func(ctx context.Context) bool { return p.suggestionRunCancelled(ctx, meta.RunID) }
	s.Backfill = func(ctx context.Context, from, to time.Time) (int, error) {
		return p.backfillWindow(ctx, router, space, deps, from, to)
	}
	result, err := suggestrun.Run(ctx, space, deps.store, texts, llm, s)
	if err == nil && result.Backfilled > 0 {
		p.logger.Info("[Routing] phrase suggestion run %s: embedded %d of the window's requests from their logs first", meta.RunID, result.Backfilled)
	}
	if err == nil && result.Unmatched > 0 {
		p.logger.Info("[Routing] phrase suggestion run %s: %d of %d assigned requests matched no cluster; raise max_clustered_requests to cluster more of the window",
			meta.RunID, result.Unmatched, result.Assigned)
	}
	return result, err
}

// recordUnstartedSuggestionRun records a run that ended before suggestrun.Run
// took it over, so it reads as ended rather than never having happened, and
// the schedule does not retry it every minute: cancelled when a user stopped
// it, failed with cause otherwise. It writes on a context of its own, since
// the run's may be why it ended.
func (p *RoutingPlugin) recordUnstartedSuggestionRun(ctx context.Context, store suggestrun.Store, meta complexitySuggestionJobMeta, routerID string, s suggestrun.Settings, cause error) error {
	recordCtx, cancel := suggestionRecordContext(ctx)
	defer cancel()
	run := &logstore.ComplexitySuggestionRun{ID: meta.RunID, RouterID: routerID, TriggeredBy: meta.TriggeredBy,
		Status: logstore.ComplexitySuggestionRunFailed, Model: s.Model, Error: cause.Error()}
	if ctx.Err() != nil && p.suggestionRunCancelled(recordCtx, meta.RunID) {
		run.Status, run.Error = logstore.ComplexitySuggestionRunCancelled, ""
	}
	finished := time.Now().UTC()
	run.StartedAt, run.FinishedAt = finished, &finished
	// The window it would have read, resolved as suggestrun.Run does: the
	// one asked for, else the whole lookback; with neither, the end time.
	window := s.Window
	if window <= 0 || window > s.Lookback {
		window = s.Lookback
	}
	run.WindowStart = finished.Add(-window)
	if err := store.UpsertComplexitySuggestionRun(recordCtx, run); err != nil {
		return fmt.Errorf("record the suggestion run: %w", err)
	}
	return nil
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

// decisionTierDecider picks a suggestion's tier with a decision model: one
// choice question with the tier step's instructions, whose choices are the
// tier step's per-tier criteria.
type decisionTierDecider struct {
	p            *RoutingPlugin
	provider     schemas.ModelProvider
	model        string
	instructions string
	criteria     map[string]configstore.ComplexityDecisionTierDefaults

	mu   sync.Mutex
	cost float64
}

func (d *decisionTierDecider) totalCost() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cost
}

// DecideTier implements suggest.TierDecider. An answer without a valid tier
// is no usable tier, not a failed run.
func (d *decisionTierDecider) DecideTier(ctx context.Context, in suggest.TierInput) (suggest.TierVerdict, error) {
	executor := d.p.decisionExecutor()
	if executor == nil {
		return suggest.TierVerdict{}, errors.New("decision-model executor is not configured")
	}
	decisionCtx := schemas.NewBifrostContext(ctx, time.Now().Add(suggestionLLMTimeout))
	defer decisionCtx.Cancel()
	bifrost.PrepareContextForInternalRequest(decisionCtx)
	text := suggest.TierRequestText(in)
	response, bifrostErr := executor(decisionCtx, &schemas.BifrostDecisionRequest{
		Provider: d.provider,
		Model:    d.model,
		Input:    schemas.DecisionInput{Messages: []schemas.DecisionInputMessage{{Role: "user", Content: schemas.DecisionInputContent{Text: &text}}}},
		Questions: []schemas.DecisionQuestion{{
			Type:         schemas.DecisionTypeChoice,
			Name:         schemas.Ptr(decisionComplexityQuestion),
			Instructions: schemas.NewDecisionText(suggest.TierInstructions(d.instructions)),
			Choices:      decisionTierChoices(d.criteria, d.model),
		}},
	})
	if bifrostErr != nil {
		return suggest.TierVerdict{}, fmt.Errorf("tier decision model call failed: %v", bifrostErr.GetErrorString())
	}
	if response == nil {
		return suggest.TierVerdict{}, errors.New("tier decision model returned no response")
	}
	cost := decisionCtx.CalculateCost(&schemas.BifrostResponse{DecisionResponse: response})
	d.mu.Lock()
	d.cost += cost
	d.mu.Unlock()
	answer, ok := decisionAnswerNamed(response.Answers, decisionComplexityQuestion)
	if !ok || answer.Type != schemas.DecisionTypeChoice || answer.Choice == nil {
		return suggest.TierVerdict{}, nil
	}
	tier, ok := complexityTierFromDecisionChoice(*answer.Choice)
	if !ok {
		return suggest.TierVerdict{}, nil
	}
	verdict := suggest.TierVerdict{Tier: tier}
	if answer.Confidence != nil {
		verdict.Confidence = decisionConfidenceLabel(*answer.Confidence)
	}
	return verdict, nil
}

// decisionConfidenceLabel maps a decision model's 0-1 confidence onto the
// high/medium/low labels suggestions carry.
func decisionConfidenceLabel(confidence float64) string {
	switch {
	case math.IsNaN(confidence) || confidence < 0 || confidence > 1:
		return ""
	case confidence >= 0.75:
		return "high"
	case confidence >= 0.4:
		return "medium"
	}
	return "low"
}
