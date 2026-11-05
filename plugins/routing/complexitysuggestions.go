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

// complexitySuggestionSlotRunID is the run and job ID of a scheduled slot. It
// is the same on every node, which is what keeps a slot from being queued
// twice, and a UUID, so it fits the run table's key.
func complexitySuggestionSlotRunID(slot time.Time) string {
	return uuid.NewSHA1(complexitySuggestionRunNamespace, []byte(fmt.Sprintf("%s\x00%d", ComplexitySuggestionsJobKind, slot.Unix()))).String()
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
	// inline guards the run a node without a job runner does itself.
	inline atomic.Bool

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

// scheduleComplexitySuggestions starts the run for the schedule's latest slot
// if it has none yet.
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
	// A router still warming (after a restart, or a phrase change) has no
	// space to suggest against yet; the slot waits for it rather than being
	// recorded as failed and skipped until the next one.
	if p.semanticClassifier == nil || p.semanticClassifier.Status().State != complexity.SemanticStatusReady {
		return nil
	}
	slot, err := config.Suggestions.EffectiveSchedule().LastSlot(now)
	if err != nil {
		return err
	}
	latest, err := deps.store.ListComplexitySuggestionRuns(ctx, nil, 1)
	if err != nil {
		return fmt.Errorf("read the latest suggestion run: %w", err)
	}
	if len(latest) > 0 && !latest[0].StartedAt.Before(slot) {
		return nil // this slot has its run
	}
	runID := complexitySuggestionSlotRunID(slot)
	_, err = p.startComplexitySuggestionRun(ctx, runID, logstore.ComplexitySuggestionTriggerSchedule)
	return err
}

// ErrComplexitySuggestionsOff is a run asked for while suggestions are off or
// have nowhere to be kept.
var ErrComplexitySuggestionsOff = errors.New("phrase suggestions are off: enable them in the complexity router settings")

// ErrComplexitySuggestionRunInFlight is a run asked for while one is already
// queued or running.
var ErrComplexitySuggestionRunInFlight = errors.New("a phrase suggestion run is already queued or running")

// StartComplexitySuggestionRunNow starts a run outside the schedule and
// returns its ID; the run itself happens in the background.
func (p *RoutingPlugin) StartComplexitySuggestionRunNow(ctx context.Context) (string, error) {
	if p.suggestions == nil || p.suggestions.deps.Load() == nil || !p.complexityConfig.Load().SuggestionsEnabled() {
		return "", ErrComplexitySuggestionsOff
	}
	// Manual runs are not deduplicated, so a bare UUID: the trigger is on
	// the run row and the kind on the job.
	runID := uuid.NewString()
	started, err := p.startComplexitySuggestionRun(ctx, runID, logstore.ComplexitySuggestionTriggerManual)
	if err != nil {
		return "", err
	}
	if !started {
		return "", ErrComplexitySuggestionRunInFlight
	}
	return runID, nil
}

// TestComplexitySuggestionPhrases routes examples with and without phrases
// against the serving phrase bank, without saving anything.
func (p *RoutingPlugin) TestComplexitySuggestionPhrases(ctx context.Context, phrases []complexity.SuggestionPhrase, examples []suggestrun.TestExample) (suggestrun.PhraseTest, error) {
	if p.semanticClassifier == nil {
		return suggestrun.PhraseTest{}, complexity.ErrNoServingGeneration
	}
	space, err := p.semanticClassifier.SuggestionSpace(ctx)
	if err != nil {
		return suggestrun.PhraseTest{}, err
	}
	defer space.Close()
	return suggestrun.TestPhrases(ctx, space, space.MinSimilarity, phrases, examples)
}

// ComplexitySuggestionMessageHistoryCount is how many user turns the router
// embeds, so request text read back from logs matches what was captured.
func (p *RoutingPlugin) ComplexitySuggestionMessageHistoryCount() int {
	if config := p.complexityConfig.Load(); config != nil && config.Semantic != nil {
		return config.Semantic.MessageHistoryCount
	}
	return 1
}

// startComplexitySuggestionRun queues a run, or starts it here when there is
// no job runner, and reports whether it did. A run already queued or running
// is not doubled.
func (p *RoutingPlugin) startComplexitySuggestionRun(ctx context.Context, runID, triggeredBy string) (bool, error) {
	cs := p.suggestions
	if jobs := cs.jobs.Load(); jobs != nil {
		inFlight, err := jobs.store.GetInFlightSidekiqJobByKind(ctx, ComplexitySuggestionsJobKind)
		if err != nil {
			return false, fmt.Errorf("check for a running suggestion run: %w", err)
		}
		if inFlight != nil {
			return false, nil
		}
		meta, err := sonic.MarshalString(complexitySuggestionJobMeta{RunID: runID, TriggeredBy: triggeredBy})
		if err != nil {
			return false, err
		}
		err = jobs.queue.EnqueuePartitioned(ctx, runID, ComplexitySuggestionsJobKind, ComplexitySuggestionsJobKind, meta, "")
		if sidekiq.IsDuplicateJobError(err) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("queue suggestion run: %w", err)
		}
		return true, nil
	}
	if !cs.inline.CompareAndSwap(false, true) {
		return false, nil
	}
	cs.wg.Go(func() {
		defer cs.inline.Store(false)
		if _, err := p.runComplexitySuggestions(cs.ctx, runID, triggeredBy); err != nil {
			p.logger.Warn("[Routing] phrase suggestion run %s failed: %v", runID, err)
		}
	})
	return true, nil
}

// RunComplexitySuggestionsJob is the sidekiq handler for a queued run.
func (p *RoutingPlugin) RunComplexitySuggestionsJob(ctx context.Context, job tables.TableSidekiqJob, _ sidekiq.ProgressFunc) (string, error) {
	var meta complexitySuggestionJobMeta
	if err := sonic.UnmarshalString(job.Metadata, &meta); err != nil || meta.RunID == "" {
		meta = complexitySuggestionJobMeta{RunID: job.ID, TriggeredBy: logstore.ComplexitySuggestionTriggerSchedule}
	}
	result, err := p.runComplexitySuggestions(ctx, meta.RunID, meta.TriggeredBy)
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
