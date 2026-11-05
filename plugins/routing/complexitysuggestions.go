package routing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/sidekiq"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggest"
	"github.com/maximhq/bifrost/plugins/routing/complexity/suggestrun"
)

// Scheduled phrase-suggestion runs.
//
// Every node checks once a minute whether the schedule's latest slot has a
// run yet. The slot comes from the calendar, so every node names the same one,
// and the log store's run records say whether it ran. A slot without a run is
// queued as a sidekiq job whose ID is the slot, so nodes that notice it at the
// same time queue one job between them. A node with no job runner (nothing to
// coordinate through) runs the slot itself.

// ComplexitySuggestionsJobKind is the sidekiq kind a suggestion run is queued
// as.
const ComplexitySuggestionsJobKind = "complexity_suggestions"

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

const (
	// suggestionScheduleTick is how often a node checks the schedule; a run
	// starts at most this late.
	suggestionScheduleTick = time.Minute
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

// complexitySuggestions is the plugin's suggestion-run state.
type complexitySuggestions struct {
	deps atomic.Pointer[complexitySuggestionDeps]
	jobs atomic.Pointer[complexitySuggestionJobs]
	// inline guards the run a node without a job runner does itself, per
	// router.
	inlineMu sync.Mutex
	inline   map[string]bool

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
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

// suggestionRouter is one router suggestions are made for: its settings and
// the classifier serving its phrase bank. A gateway has one today, the
// default router, built from the plugin's own config and classifier; several
// routers add entries here and nothing else in this file changes.
type suggestionRouter struct {
	id         string
	config     *complexity.AnalyzerConfig
	classifier *complexity.SemanticClassifier
}

// ErrUnknownComplexityRouter is a suggestion call for a router that does not
// exist.
var ErrUnknownComplexityRouter = errors.New("no such model router")

// ErrComplexitySuggestionWindow is a run window outside what is kept.
var ErrComplexitySuggestionWindow = errors.New("the run window must be positive and no longer than the suggestions lookback")

// suggestionRouters lists the routers suggestions are made for.
func (p *RoutingPlugin) suggestionRouters() []suggestionRouter {
	return []suggestionRouter{{id: complexity.DefaultRouterID, config: p.complexityConfig.Load(), classifier: p.semanticClassifier}}
}

// suggestionRouter finds one router by ID.
func (p *RoutingPlugin) suggestionRouter(id string) (suggestionRouter, bool) {
	for _, r := range p.suggestionRouters() {
		if r.id == id {
			return r, true
		}
	}
	return suggestionRouter{}, false
}

// complexitySuggestionPartition keeps one router's runs in order and apart
// from every other router's.
func complexitySuggestionPartition(routerID string) string {
	return ComplexitySuggestionsJobKind + ":" + routerID
}

// startComplexitySuggestions starts the schedule check. tick <= 0 starts no
// ticker, for tests that drive the check themselves.
func (p *RoutingPlugin) startComplexitySuggestions(ctx context.Context, tick time.Duration) {
	cs := &complexitySuggestions{inline: map[string]bool{}}
	cs.ctx, cs.cancel = context.WithCancel(ctx)
	p.suggestions = cs
	if tick <= 0 {
		return
	}
	cs.wg.Go(func() {
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-cs.ctx.Done():
				return
			case now := <-ticker.C:
				if err := p.scheduleComplexitySuggestions(cs.ctx, now); err != nil {
					p.logger.Warn("[Routing] phrase suggestion schedule check failed: %v", err)
				}
			}
		}
	})
}

// closeComplexitySuggestions stops the schedule check and any run this node
// is doing itself.
func (p *RoutingPlugin) closeComplexitySuggestions() {
	cs := p.suggestions
	if cs == nil {
		return
	}
	cs.cancel()
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

// scheduleComplexitySuggestions starts, for each router, the run for its
// schedule's latest slot if that slot has none yet.
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
	deps := p.suggestions.deps.Load()
	if deps == nil || !router.config.SuggestionsEnabled() {
		return nil
	}
	// A router still warming (after a restart, or a phrase change) has no
	// space to suggest against yet; the slot waits for it rather than being
	// recorded as failed and skipped until the next one.
	if router.classifier == nil || router.classifier.Status().State != complexity.SemanticStatusReady {
		return nil
	}
	slot, err := router.config.Suggestions.EffectiveSchedule().LastSlot(now)
	if err != nil {
		return err
	}
	latest, err := deps.store.ListComplexitySuggestionRuns(ctx, logstore.ComplexitySuggestionRunFilter{RouterID: router.id}, 1)
	if err != nil {
		return fmt.Errorf("read the latest suggestion run: %w", err)
	}
	if len(latest) > 0 && !latest[0].StartedAt.Before(slot) {
		return nil // this slot has its run
	}
	runID := complexitySuggestionSlotRunID(router.id, slot)
	_, err = p.startComplexitySuggestionRun(ctx, complexitySuggestionJobMeta{RunID: runID, RouterID: router.id, TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule})
	return err
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
	// Manual runs are not deduplicated, so a bare UUID: the router and the
	// trigger are on the run row and the kind on the job.
	runID := uuid.NewString()
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

// checkSuggestionWindow refuses a window longer than what is kept.
func checkSuggestionWindow(router suggestionRouter, window time.Duration) error {
	if window == 0 {
		return nil
	}
	lookback := time.Duration(router.config.Suggestions.LookbackDays) * 24 * time.Hour
	if window < 0 || window > lookback {
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
	return suggestrun.EstimateRun(count, settings), nil
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
	space, err := router.classifier.SuggestionSpace(ctx)
	if err != nil {
		return suggestrun.PhraseTest{}, err
	}
	defer space.Close()
	return suggestrun.TestPhrases(ctx, space, space.MinSimilarity, phrases, examples)
}

// ComplexitySuggestionMessageHistoryCount is how many user turns a router
// embeds, so request text read back from logs matches what was captured.
func (p *RoutingPlugin) ComplexitySuggestionMessageHistoryCount(routerID string) int {
	if router, ok := p.suggestionRouter(routerID); ok && router.config != nil && router.config.Semantic != nil {
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

// startComplexitySuggestionRun queues a router's run, or starts it here when
// there is no job runner, and reports whether it did. A run the router
// already has queued or running is not doubled; other routers' runs are no
// reason to wait.
func (p *RoutingPlugin) startComplexitySuggestionRun(ctx context.Context, meta complexitySuggestionJobMeta) (bool, error) {
	cs := p.suggestions
	if jobs := cs.jobs.Load(); jobs != nil {
		busy, err := p.routerSuggestionBusy(ctx, jobs, meta.RouterID)
		if err != nil || busy {
			return false, err
		}
		encoded, err := sonic.MarshalString(meta)
		if err != nil {
			return false, err
		}
		err = jobs.queue.EnqueuePartitioned(ctx, meta.RunID, ComplexitySuggestionsJobKind, complexitySuggestionPartition(meta.RouterID), encoded, "")
		if sidekiq.IsDuplicateJobError(err) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("queue suggestion run: %w", err)
		}
		return true, nil
	}
	cs.inlineMu.Lock()
	if cs.inline[meta.RouterID] {
		cs.inlineMu.Unlock()
		return false, nil
	}
	cs.inline[meta.RouterID] = true
	cs.inlineMu.Unlock()
	cs.wg.Go(func() {
		defer func() {
			cs.inlineMu.Lock()
			delete(cs.inline, meta.RouterID)
			cs.inlineMu.Unlock()
		}()
		if _, err := p.runComplexitySuggestions(cs.ctx, meta); err != nil {
			p.logger.Warn("[Routing] phrase suggestion run %s failed: %v", meta.RunID, err)
		}
	})
	return true, nil
}

// routerSuggestionBusy reports whether a router has a run queued or running.
// The job store answers per job kind, so a queued job of the kind counts only
// when it is this router's; a run record still running inside the run
// timeout covers a job of this router queued behind another router's.
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
	return len(latest) > 0 && latest[0].Status == logstore.ComplexitySuggestionRunRunning &&
		time.Since(latest[0].StartedAt) < suggestionRunTimeout, nil
}

// RunComplexitySuggestionsJob is the sidekiq handler for a queued run.
func (p *RoutingPlugin) RunComplexitySuggestionsJob(ctx context.Context, job tables.TableSidekiqJob, _ sidekiq.ProgressFunc) (string, error) {
	var meta complexitySuggestionJobMeta
	if err := sonic.UnmarshalString(job.Metadata, &meta); err != nil || meta.RunID == "" {
		meta = complexitySuggestionJobMeta{RunID: job.ID, TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule}
	}
	if meta.RouterID == "" {
		meta.RouterID = complexity.DefaultRouterID
	}
	result, err := p.runComplexitySuggestions(ctx, meta)
	switch {
	case errors.Is(err, errSuggestionsUnavailable):
		// Suggestions were turned off, or this node has nowhere to keep them,
		// between queueing and running: nothing to do is not a failure.
		meta.Message, err = err.Error(), nil
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

// SummarizeComplexitySuggestionsJob reports a run job's outcome for generic
// job views.
func SummarizeComplexitySuggestionsJob(metadata string) sidekiq.JobSummary {
	var meta complexitySuggestionJobMeta
	if sonic.UnmarshalString(metadata, &meta) != nil {
		return sidekiq.JobSummary{}
	}
	return sidekiq.JobSummary{Message: meta.Message}
}

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
		Instructions:       settings.Instructions,
		Seed:               time.Now().UnixNano(),
	}
	if router.config.LLM != nil {
		s.Guidance = router.config.LLM.Prompt
	}
	return s
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
	provider, model := settings.Provider, settings.Model
	if model == "" && router.config.LLM != nil {
		provider, model = router.config.LLM.Provider, router.config.LLM.Model
	}
	var llm suggest.LLM
	if model != "" {
		s.Model = string(provider) + "/" + model
		adapter := &suggestionLLM{p: p, provider: provider, model: model}
		llm, s.Cost = adapter, adapter.totalCost
	}

	space, err := router.classifier.SuggestionSpace(ctx)
	if err != nil {
		// Recorded, so a run that could not start reads as failed rather than
		// never having happened, and the schedule does not retry it every
		// minute.
		run := &logstore.ComplexitySuggestionRun{ID: meta.RunID, RouterID: router.id, TriggeredBy: meta.TriggeredBy,
			Status: logstore.ComplexitySuggestionRunFailed, Model: s.Model, Error: err.Error()}
		finished := time.Now().UTC()
		run.StartedAt, run.FinishedAt = finished, &finished
		if recordErr := deps.store.UpsertComplexitySuggestionRun(ctx, run); recordErr != nil {
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
			meta.RunID, result.Unmatched, result.Assigned)
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
