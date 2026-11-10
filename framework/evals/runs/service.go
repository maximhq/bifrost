package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/evals"
	"github.com/maximhq/bifrost/framework/evals/datasets"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/tidwall/sjson"
)

// Limits of the run API.
const (
	DefaultEntryPageSize = 100
	MaxEntryPageSize     = 200
	MinComparedRuns      = 2
	MaxComparedRuns      = 5
	// maxResultAttempts is how many times an evaluator that calls out is tried before
	// its result is marked failed.
	maxResultAttempts = 3
	// planPageSize is how many dataset rows are read and stored at a time when a run
	// is planned. The whole dataset is never in memory.
	planPageSize = 200
	// writeBatch is how many IDs one bulk update carries.
	writeBatch = 500
)

// ErrNoRunner is returned when a run cannot be started because the evaluation queue
// is not running.
var ErrNoRunner = errors.New("test runs cannot start: the evaluation queue is not running")

func invalid(format string, args ...any) error {
	return &evals.InvalidError{Message: fmt.Sprintf(format, args...)}
}

// Store is the part of the config store the run service uses.
type Store interface {
	CreateEvalRun(ctx context.Context, run *tables.TableEvalRun) error
	GetEvalRun(ctx context.Context, id string) (*tables.TableEvalRun, error)
	GetEvalRunState(ctx context.Context, id string) (*configstore.EvalRunState, error)
	ListEvalRuns(ctx context.Context, params configstore.EvalRunListParams) ([]tables.TableEvalRun, int64, error)
	TransitionEvalRun(ctx context.Context, id string, from []string, fields map[string]any) (bool, error)
	TransitionEvalRunAtGeneration(ctx context.Context, id string, from []string, generation int64, fields map[string]any) (bool, error)
	TransitionEvalRunEntriesAtGeneration(ctx context.Context, runID string, generation int64, runFields map[string]any, from []string, entryFields map[string]any) (int64, error)
	TransitionStoppedEvalRunEntries(ctx context.Context, runID string, generation int64, from []string, idleEvaluating bool, entryFields map[string]any) (int64, error)
	TransitionEvalRunAtReview(ctx context.Context, id string, from []string, generation, reviewSeq int64, fields map[string]any) (bool, error)
	CountEvalRunReview(ctx context.Context, id string) error
	ClaimEvalRunWithEntries(ctx context.Context, id string, runFrom []string, generation int64, runFields map[string]any, entries *configstore.EvalRunEntryUpdate) (bool, error)
	DeleteEvalRun(ctx context.Context, id string) error
	CreateEvalRunEntries(ctx context.Context, entries []tables.TableEvalRunEntry) error
	ListEvalRunEntries(ctx context.Context, params configstore.EvalRunEntryListParams) ([]tables.TableEvalRunEntry, int64, error)
	ListEvalRunEntryRefs(ctx context.Context, params configstore.EvalRunEntryListParams) ([]configstore.EvalRunEntryRef, error)
	HasOpenEvalRunEntries(ctx context.Context, runID string) (bool, error)
	GetEvalRunEntry(ctx context.Context, id string) (*tables.TableEvalRunEntry, error)
	TransitionEvalRunEntry(ctx context.Context, id string, from []string, fields map[string]any) (bool, error)
	TransitionEvalRunEntries(ctx context.Context, runID string, from, ids []string, fields map[string]any) (int64, error)
	RouteEvalRunEntry(ctx context.Context, id, fromKey, toKey string, routedAt time.Time) (bool, error)
	RouteEvalRunResult(ctx context.Context, id, fromKey, toKey string, routedAt time.Time) (bool, error)
	ClaimEvalRunEntry(ctx context.Context, id, keyID, claimID string, attempt int, startedBefore, recoverStartedAt time.Time) (bool, error)
	ClaimEvalRunResult(ctx context.Context, id, keyID, claimID string, attempt int, startedBefore, recoverStartedAt time.Time) (bool, error)
	ClaimQueuedEvalRunEntry(ctx context.Context, id, keyID, claimID string, attempt int) (bool, error)
	ClaimQueuedEvalRunResult(ctx context.Context, id, keyID, claimID string, attempt int) (bool, error)
	ReleaseQueuedEvalRunClaims(ctx context.Context, claimID string, startedBefore time.Time) (int64, error)
	TransitionEvalRunEntryAttempt(ctx context.Context, id string, attempt int, from []string, fields map[string]any) (bool, error)
	TransitionEvalRunEntryAtRun(ctx context.Context, runID, status string, generation int64, id string, from []string, fields map[string]any) (bool, error)
	TransitionEvalRunEntriesAtRun(ctx context.Context, runID, status string, generation int64, from []string, fields map[string]any) (int64, error)
	TransitionEvalRunEntryClaimed(ctx context.Context, id string, attempt int, claimID string, from []string, fields map[string]any) (bool, error)
	TransitionEvalRunEntryAtReview(ctx context.Context, runID string, reviewSeq, generation int64, id string, attempt int, from []string, fields map[string]any) (bool, error)
	TransitionEvalRunResultAttempt(ctx context.Context, id string, attempt int, from []string, fields map[string]any) (bool, error)
	TransitionEvalRunResultUnstarted(ctx context.Context, id string, attempt int, fields map[string]any) (bool, error)
	TransitionEvalRunResultRetry(ctx context.Context, id string, attempt int, fields map[string]any) (bool, error)
	ResetEvalRunEntries(ctx context.Context, runID string, generation int64, ids []string, fields map[string]any) (int64, error)
	GetEvalRunLastSeq(ctx context.Context, runID string) (int64, error)
	ListEvalRunRowIDs(ctx context.Context, runIDs []string, offset, limit int) ([]string, int64, error)
	CountEvalRunResultsByEvaluator(ctx context.Context, runID string) ([]configstore.EvalRunEvaluatorCount, error)
	CountEvalRunEntries(ctx context.Context, runID string) (map[string]int64, error)
	CountEvalRunEntriesPassed(ctx context.Context, runID string) (int64, error)
	ListEvalRunTargetLatencies(ctx context.Context, runID string) ([]int64, error)
	GetEvalRunTotals(ctx context.Context, runID string) (configstore.EvalRunTotals, error)
	CreateEvalRunResults(ctx context.Context, results []tables.TableEvalRunResult, entryAttempt int) error
	GetEvalRunResult(ctx context.Context, id string) (*tables.TableEvalRunResult, error)
	ListEvalRunResults(ctx context.Context, params configstore.EvalRunResultListParams) ([]tables.TableEvalRunResult, error)
	ListEvalRunResultRefs(ctx context.Context, params configstore.EvalRunResultListParams) ([]configstore.EvalRunResultRef, error)
	TransitionEvalRunResult(ctx context.Context, id string, from []string, fields map[string]any) (bool, error)
	CountEvalRunScoreChanges(ctx context.Context, baseRunID, runID string) ([]configstore.EvalRunScoreChange, error)
	DeleteEvalRunResults(ctx context.Context, runID string, entryIDs []string, evaluatorID string) (int64, error)
	GetLatestPromptVersion(ctx context.Context, promptID string) (*tables.TablePromptVersion, error)
	GetPromptVersions(ctx context.Context, promptID string) ([]tables.TablePromptVersion, error)
}

// Datasets is the part of the dataset service a run reads.
type Datasets interface {
	Get(ctx context.Context, id string) (*datasets.DatasetView, error)
	SaveVersion(ctx context.Context, id, name, description, actor string) (*tables.TableEvalDatasetVersion, error)
	Version(ctx context.Context, id string, number int) (*tables.TableEvalDatasetVersion, error)
	Rows(ctx context.Context, id string, query datasets.RowQuery) (*datasets.RowsPage, error)
}

// Evaluators is the part of the evaluator service a run reads.
type Evaluators interface {
	Definition(ctx context.Context, evaluatorID string, version int) (evals.Definition, error)
	Describe(def evals.Definition) evals.Validation
}

// Engine runs an evaluator. The HTTP target is called through it, as an API evaluator.
type Engine interface {
	Run(ctx context.Context, def evals.Definition, in evals.Input) evals.Result
}

// Runner queues the jobs of a run. *evals.Runner implements it.
type Runner interface {
	Submit(ctx context.Context, job *evals.Job) error
	// Resume puts back a job a forgetting queue lost, from the key the store has it on.
	Resume(ctx context.Context, job *evals.Job) error
	Keys(ctx context.Context) ([]evals.EvalKey, error)
	// Run runs one evaluator on one input in place (see evals.Runner.Run).
	Run(ctx context.Context, def evals.Definition, input evals.Input) evals.Result
}

// Options are the dependencies of the run service.
type Options struct {
	Store      Store
	Datasets   Datasets
	Evaluators Evaluators
	Engine     Engine
	// Model calls the model under test for prompt and model targets.
	Model evals.ModelCaller
	// LiveStore holds the decoded run cache; nil decodes a run on every job.
	LiveStore *livestore.Store
}

// Service is the test run API. The HTTP handlers are a thin layer over it, and the
// job store in this package drives a run's work through it.
type Service struct {
	store      Store
	datasets   Datasets
	evaluators Evaluators
	engine     Engine
	model      evals.ModelCaller
	runner     Runner
	// specs holds the decoded target and evaluators of the runs with work in flight.
	specs *livestore.Cache[*runSpec] // nil when there is no live store
}

// NewService returns a run service. Call SetRunner before starting a run: the runner
// is built on the job store, which needs the service first.
func NewService(opts Options) *Service {
	var specs *livestore.Cache[*runSpec]
	if opts.LiveStore != nil {
		// A failure (a cache of this name already exists) leaves runs uncached, which is
		// slower but correct.
		specs, _ = opts.LiveStore.NewCache[*runSpec](context.Background(), specCacheName,
			livestore.WithSharedFills[*runSpec](),
			livestore.WithCacheTTL(func(*runSpec) time.Duration { return specCacheTTL }))
	}
	return &Service{
		store: opts.Store, datasets: opts.Datasets, evaluators: opts.Evaluators, engine: opts.Engine, model: opts.Model,
		specs: specs,
	}
}

// SetRunner gives the service the runner its jobs are queued on.
func (s *Service) SetRunner(runner Runner) { s.runner = runner }

// ============================================================================
// Starting a run
// ============================================================================

// resolved is a run request with everything looked up and filled in.
type resolved struct {
	dataset     *datasets.DatasetView
	target      Target
	definitions []evals.Definition
	variables   []string // what the run provides to its evaluators, sorted
}

// variableNames lists the evaluator variables a set of dataset columns produces.
func variableNames(columns []datasets.Column) []string {
	cells := make(map[string]string, len(columns))
	for _, column := range columns {
		cells[column.ID] = "x"
	}
	produced := datasets.RowVariables(columns, cells)
	names := make([]string, 0, len(produced))
	for name := range produced {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// resolve looks up everything a run request names and lists what would stop it from
// running. An error is returned only for a failure to look something up.
func (s *Service) resolve(ctx context.Context, in RunInput) (*resolved, []string, error) {
	var problems []string
	problem := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if err := datasets.ValidateName("run", in.Name); err != nil {
		problem("%s", err.Error())
	}
	if err := evals.ValidateTags(in.Tags); err != nil {
		problem("%s", err.Error())
	}

	dataset, err := s.datasets.Get(ctx, in.DatasetID)
	if errors.Is(err, configstore.ErrNotFound) {
		problem("the dataset does not exist")
		return &resolved{}, problems, nil
	}
	if err != nil {
		return nil, nil, err
	}
	out := &resolved{dataset: dataset}
	var columns []datasets.Column
	rowCount := dataset.RowCount
	if in.DatasetVersion < 0 {
		problem("dataset_version must be 0 or more")
		return out, problems, nil
	}
	if in.DatasetVersion > 0 {
		version, err := s.datasets.Version(ctx, dataset.ID, in.DatasetVersion)
		if errors.Is(err, configstore.ErrNotFound) {
			problem("the dataset has no version %d", in.DatasetVersion)
			return out, problems, nil
		}
		if err != nil {
			return nil, nil, err
		}
		columns, rowCount = datasets.VersionColumns(version), version.RowCount
	} else {
		columns = make([]datasets.Column, len(dataset.Columns))
		for i, column := range dataset.Columns {
			columns[i] = datasets.Column{ID: column.ID, Name: column.Name, Type: datasets.ColumnType(column.Type)}
		}
	}
	if rowCount == 0 {
		problem("the dataset has no rows")
	}
	if in.SplitID != "" && !slices.ContainsFunc(dataset.Splits, func(split datasets.SplitView) bool { return split.ID == in.SplitID }) {
		problem("the dataset has no such split")
	} else if in.SplitID != "" && len(problems) == 0 {
		// The filter planning uses, read once: a split with no rows at this version would
		// otherwise fail the run only after it was stored.
		page, err := s.datasets.Rows(ctx, dataset.ID, datasets.RowQuery{Version: in.DatasetVersion, SplitID: in.SplitID, Limit: 1, SkipTotal: true})
		if err != nil {
			return nil, nil, err
		}
		if len(page.Rows) == 0 {
			problem("the split has no rows in this version of the dataset")
		}
	}
	provided := variableNames(columns)

	// Models whose eval key the run needs, with what needs them, for the message.
	needsKey := map[modelNeed]string{}
	out.target, err = s.resolveTarget(ctx, in, provided, problem)
	if err != nil {
		return nil, nil, err
	}
	if out.target.Type == TargetPrompt || out.target.Type == TargetModel {
		if out.target.Provider != "" {
			needsKey[modelNeed{out.target.Provider, out.target.Model}] = "the target"
		}
	}
	for _, name := range []string{VariableOutput, VariableToolCalls} {
		if !slices.Contains(provided, name) {
			provided = append(provided, name)
		}
	}
	slices.Sort(provided)
	out.variables = provided

	if len(in.Evaluators) == 0 {
		problem("at least one evaluator is required")
	}
	seen := map[string]bool{}
	for _, ref := range in.Evaluators {
		if seen[ref.ID] {
			continue
		}
		seen[ref.ID] = true
		definition, missing, err := s.resolveEvaluator(ctx, ref, provided)
		if errors.Is(err, configstore.ErrNotFound) {
			problem("evaluator %q does not exist, or has no version %d", ref.ID, ref.Version)
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if len(missing) > 0 {
			problem("evaluator %q needs %s, which this run does not provide", definition.Name, strings.Join(missing, ", "))
		}
		if provider, model := evals.ModelOf(definition); provider != "" {
			if _, ok := needsKey[modelNeed{provider, model}]; !ok {
				needsKey[modelNeed{provider, model}] = fmt.Sprintf("evaluator %q", definition.Name)
			}
		}
		out.definitions = append(out.definitions, definition)
	}

	if len(needsKey) > 0 && s.runner != nil {
		keys, err := s.runner.Keys(ctx)
		if err != nil {
			return nil, nil, err
		}
		needs := make([]modelNeed, 0, len(needsKey))
		for need := range needsKey {
			needs = append(needs, need)
		}
		slices.SortFunc(needs, func(a, b modelNeed) int { return strings.Compare(a.provider+"/"+a.model, b.provider+"/"+b.model) })
		for _, need := range needs {
			if message := keyProblem(keys, need, needsKey[need]); message != "" {
				problem("%s", message)
			}
		}
	}
	return out, problems, nil
}

// modelNeed is a model a run calls through an eval key.
type modelNeed struct{ provider, model string }

// keyProblem says why no eval key serves a model, or "" when one does. A key serves a
// model when it is on the model's provider and supports the model, the rule the runner
// places jobs by (Runner.keyServes), so a run is refused up front instead of failing on
// every entry.
func keyProblem(keys []evals.EvalKey, need modelNeed, who string) string {
	onProvider := false
	for _, key := range keys {
		if key.Provider != need.provider {
			continue
		}
		onProvider = true
		if key.Supports == nil || need.model == "" || key.Supports(need.model) {
			return ""
		}
	}
	if !onProvider {
		return fmt.Sprintf("%s calls provider %q, which has no key marked \"use for evaluations\"", who, need.provider)
	}
	return fmt.Sprintf("%s calls model %q, which no key of provider %q marked \"use for evaluations\" supports", who, need.model, need.provider)
}

// resolveEvaluator loads an evaluator's definition and lists the variables it needs
// that the run does not provide.
func (s *Service) resolveEvaluator(ctx context.Context, ref EvaluatorRef, provided []string) (evals.Definition, []string, error) {
	if ref.Version < 0 {
		return evals.Definition{}, nil, invalid("version must be 0 or more")
	}
	definition, err := s.evaluators.Definition(ctx, ref.ID, ref.Version)
	if err != nil {
		return evals.Definition{}, nil, err
	}
	var missing []string
	for _, name := range s.evaluators.Describe(definition).Variables {
		if !slices.Contains(provided, name) {
			missing = append(missing, name)
		}
	}
	return definition, missing, nil
}

// resolveTarget checks a target and fills in what the run stores: a prompt target
// gets a copy of its version, and an HTTP target gets its header values encrypted.
func (s *Service) resolveTarget(ctx context.Context, in RunInput, provided []string, problem func(string, ...any)) (Target, error) {
	target := in.Target
	switch target.Type {
	case TargetDataset:
		if !slices.Contains(provided, VariableOutput) {
			problem("the dataset has no output column, so there is nothing to score; add one, or pick a target that produces the output")
		}
		return Target{Type: TargetDataset}, nil
	case TargetModel:
		if target.Provider == "" || target.Model == "" {
			problem("a model target needs a provider and a model")
		}
		// provided holds only the dataset's variables here: output and tool_calls are
		// added after the target is resolved.
		if target.System == "" && !slices.Contains(provided, VariableInput) && !slices.Contains(provided, VariableHistory) {
			problem("the model target has no system message and the dataset has no input or conversation history column, so there is nothing to send")
		}
		return Target{Type: TargetModel, Provider: target.Provider, Model: target.Model, System: target.System, Params: target.Params}, nil
	case TargetPrompt:
		if target.PromptID == "" {
			problem("a prompt target needs a prompt")
			return Target{Type: TargetPrompt}, nil
		}
		if target.PromptVersion < 0 {
			problem("prompt_version must be 0 or more")
			return Target{Type: TargetPrompt}, nil
		}
		version, err := s.promptVersion(ctx, target.PromptID, target.PromptVersion)
		if errors.Is(err, configstore.ErrNotFound) {
			problem("the prompt does not exist, or has no such version")
			return Target{Type: TargetPrompt, PromptID: target.PromptID}, nil
		}
		if err != nil {
			return Target{}, err
		}
		if version.Provider == "" || version.Model == "" {
			problem("the prompt version has no model set")
		}
		out := Target{
			Type: TargetPrompt, PromptID: target.PromptID, PromptVersion: version.VersionNumber,
			Provider: version.Provider, Model: version.Model, Params: version.ModelParams,
		}
		for _, message := range version.Messages {
			out.Messages = append(out.Messages, json.RawMessage(message.Message))
		}
		if len(out.Messages) == 0 {
			problem("the prompt version has no messages")
		}
		return out, nil
	case TargetHTTP:
		raw := []byte(target.HTTP)
		if len(raw) == 0 {
			raw = []byte(`{}`)
		}
		if in.TargetConcurrency > 0 {
			withLimit, err := sjson.SetBytes(raw, "concurrency", in.TargetConcurrency)
			if err != nil {
				problem("the HTTP target is not a JSON object")
				return Target{Type: TargetHTTP}, nil
			}
			raw = withLimit
		}
		config, err := evals.NormalizeConfig(evals.EvaluatorTypeAPI, raw)
		if err != nil {
			problem("the HTTP target is not valid: %s", err.Error())
			return Target{Type: TargetHTTP}, nil
		}
		return Target{Type: TargetHTTP, HTTP: config}, nil
	}
	problem("target.type must be one of dataset, prompt, model, http")
	return Target{Type: target.Type}, nil
}

// promptVersion returns a prompt's version by number, or its latest when number is 0.
func (s *Service) promptVersion(ctx context.Context, promptID string, number int) (*tables.TablePromptVersion, error) {
	if number == 0 {
		return s.store.GetLatestPromptVersion(ctx, promptID)
	}
	versions, err := s.store.GetPromptVersions(ctx, promptID)
	if err != nil {
		return nil, err
	}
	for i := range versions {
		if versions[i].VersionNumber == number {
			return &versions[i], nil
		}
	}
	return nil, configstore.ErrNotFound
}

// Validate checks a run's settings without starting it.
func (s *Service) Validate(ctx context.Context, in RunInput) (*Check, error) {
	looked, problems, err := s.resolve(ctx, in)
	if err != nil {
		return nil, err
	}
	check := &Check{Valid: len(problems) == 0, Problems: problems, Variables: looked.variables}
	if check.Problems == nil {
		check.Problems = []string{}
	}
	if check.Variables == nil {
		check.Variables = []string{}
	}
	return check, nil
}

// Create starts a run. It pins the dataset version the run reads, copies the target
// and the evaluators onto the run, and queues the planning job. The entries are
// created by that job, so the call returns at once whatever the dataset's size.
func (s *Service) Create(ctx context.Context, in RunInput) (*RunView, error) {
	looked, problems, err := s.resolve(ctx, in)
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, &evals.InvalidError{Message: strings.Join(problems, "; ")}
	}
	if s.runner == nil {
		return nil, ErrNoRunner
	}

	version := in.DatasetVersion
	if version == 0 {
		// The run reads a saved version, so the draft is saved first when it has edits.
		version = looked.dataset.HeadVersion
		if looked.dataset.DraftChanges > 0 || version == 0 {
			saved, err := s.datasets.SaveVersion(ctx, looked.dataset.ID, "", fmt.Sprintf("Saved automatically before test run %q", strings.TrimSpace(in.Name)), in.CreatedBy)
			switch {
			case err == nil:
				version = saved.Number
			case errors.Is(err, configstore.ErrEvalDatasetNoChanges):
				// Another request saved the same draft first, so the saved head is the draft
				// that was validated: the head read before that save is older.
				current, getErr := s.datasets.Get(ctx, looked.dataset.ID)
				if getErr != nil {
					return nil, getErr
				}
				if current.HeadVersion == 0 {
					return nil, err
				}
				version = current.HeadVersion
			default:
				return nil, err
			}
		}
	}

	targetJSON, err := providerUtils.MarshalSorted(looked.target)
	if err != nil {
		return nil, err
	}
	evaluatorsJSON, err := providerUtils.MarshalSorted(looked.definitions)
	if err != nil {
		return nil, err
	}
	run := &tables.TableEvalRun{
		Name: strings.TrimSpace(in.Name), Tags: in.Tags, Status: tables.EvalRunQueued,
		TargetType: string(looked.target.Type), Target: targetJSON,
		DatasetID: looked.dataset.ID, DatasetVersion: version, Evaluators: evaluatorsJSON,
		TargetConcurrency: in.TargetConcurrency, CreatedBy: in.CreatedBy, Generation: 1,
	}
	if in.SplitID != "" {
		run.SplitID = new(in.SplitID)
	}
	if err := s.store.CreateEvalRun(ctx, run); err != nil {
		return nil, err
	}
	if err := s.runner.Submit(ctx, &evals.Job{ID: planJobID(run.ID)}); err != nil {
		_ = s.failRun(ctx, run.ID, run.Generation, err) // the queueing error is what the caller gets
		return nil, err
	}
	return s.Get(ctx, run.ID)
}

// ============================================================================
// Reading runs
// ============================================================================

// definitions decodes the evaluators copied onto a run.
func definitionsOf(run *tables.TableEvalRun) ([]evals.Definition, error) {
	var definitions []evals.Definition
	if len(run.Evaluators) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(run.Evaluators, &definitions); err != nil {
		return nil, fmt.Errorf("run %s has unreadable evaluators: %w", run.ID, err)
	}
	return definitions, nil
}

// targetOf decodes the target copied onto a run.
func targetOf(run *tables.TableEvalRun) (Target, error) {
	var target Target
	if err := json.Unmarshal(run.Target, &target); err != nil {
		return Target{}, fmt.Errorf("run %s has an unreadable target: %w", run.ID, err)
	}
	return target, nil
}

// view builds the API form of a run: live counts, the summary, and secrets taken out
// of the copied target and evaluators.
func (s *Service) view(ctx context.Context, run *tables.TableEvalRun, withSummary bool) (*RunView, error) {
	out := &RunView{TableEvalRun: *run}
	counts, err := s.store.CountEvalRunEntries(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	out.Counts = counts
	if out.Passed, err = s.store.CountEvalRunEntriesPassed(ctx, run.ID); err != nil {
		return nil, err
	}
	definitions, err := definitionsOf(run)
	if err != nil {
		return nil, err
	}
	switch {
	case len(run.Summary) > 0:
		var summary Summary
		if err := json.Unmarshal(run.Summary, &summary); err == nil {
			out.Summary = &summary
		}
	case withSummary:
		if out.Summary, err = s.liveSummary(ctx, run.ID, definitions); err != nil {
			return nil, err
		}
	}

	redacted := make([]evals.Definition, len(definitions))
	for i, definition := range definitions {
		redacted[i] = definition
		redacted[i].Config = evals.RedactConfig(definition.Type, definition.Config)
	}
	if out.Evaluators, err = providerUtils.MarshalSorted(redacted); err != nil {
		return nil, err
	}
	if target, err := targetOf(run); err == nil && target.Type == TargetHTTP {
		target.HTTP = evals.RedactConfig(evals.EvaluatorTypeAPI, target.HTTP)
		if out.Target, err = providerUtils.MarshalSorted(target); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Get returns one run with its counts and summary.
func (s *Service) Get(ctx context.Context, id string) (*RunView, error) {
	run, err := s.store.GetEvalRun(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.view(ctx, run, true)
}

// List returns runs with their counts. A run that finished its entries carries its
// summary. A run that failed before its rows were planned has none; its error says why.
func (s *Service) List(ctx context.Context, params configstore.EvalRunListParams) ([]RunView, int64, error) {
	runs, total, err := s.store.ListEvalRuns(ctx, params)
	if err != nil {
		return nil, 0, err
	}
	views := make([]RunView, len(runs))
	for i := range runs {
		view, err := s.view(ctx, &runs[i], false)
		if err != nil {
			return nil, 0, err
		}
		views[i] = *view
	}
	return views, total, nil
}

// Update renames a run and replaces its tags.
func (s *Service) Update(ctx context.Context, id string, in UpdateInput) (*RunView, error) {
	if err := datasets.ValidateName("run", in.Name); err != nil {
		return nil, err
	}
	if err := evals.ValidateTags(in.Tags); err != nil {
		return nil, err
	}
	var tagsJSON *string
	if in.Tags != nil {
		data, err := json.Marshal(in.Tags)
		if err != nil {
			return nil, err
		}
		tagsJSON = new(string(data))
	}
	// The run is read first: it carries the read scope, which the write does not.
	if _, err := s.store.GetEvalRun(ctx, id); err != nil {
		return nil, err
	}
	ok, err := s.store.TransitionEvalRun(ctx, id, nil, map[string]any{"name": strings.TrimSpace(in.Name), "tags_json": tagsJSON, "updated_at": time.Now()})
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, configstore.ErrNotFound
	}
	return s.Get(ctx, id)
}

// isActive reports whether a run still has work to do.
// finishedRunStatuses are the statuses a run can be rerun or rescored from.
var finishedRunStatuses = []string{tables.EvalRunCompleted, tables.EvalRunFailed, tables.EvalRunStopped}

func isActive(status string) bool {
	return status == tables.EvalRunQueued || status == tables.EvalRunRunning
}

// Delete removes a run that is not active, with its entries and results.
func (s *Service) Delete(ctx context.Context, id string) error {
	run, err := s.store.GetEvalRun(ctx, id)
	if err != nil {
		return err
	}
	if isActive(run.Status) {
		return invalid("the run is still active; stop it before deleting it")
	}
	// The check above is the fast path. The store repeats it in the delete itself, so a
	// run another request has just claimed is kept.
	if err := s.store.DeleteEvalRun(ctx, id); errors.Is(err, configstore.ErrEvalRunActive) {
		return invalid("the run is still active; stop it before deleting it")
	} else if err != nil {
		return err
	}
	return nil
}

// pageSize bounds a requested page.
func pageSize(limit int) int {
	if limit <= 0 {
		return DefaultEntryPageSize
	}
	return min(limit, MaxEntryPageSize)
}

// withResults attaches each entry's results.
func (s *Service) withResults(ctx context.Context, runID string, entries []tables.TableEvalRunEntry) ([]EntryView, error) {
	views := make([]EntryView, len(entries))
	if len(entries) == 0 {
		return views, nil
	}
	ids := make([]string, len(entries))
	at := make(map[string]int, len(entries))
	for i := range entries {
		views[i] = EntryView{TableEvalRunEntry: entries[i], Results: []tables.TableEvalRunResult{}}
		ids[i] = entries[i].ID
		at[entries[i].ID] = i
	}
	results, err := s.store.ListEvalRunResults(ctx, configstore.EvalRunResultListParams{RunID: runID, EntryIDs: ids})
	if err != nil {
		return nil, err
	}
	for _, result := range results {
		index := at[result.EntryID]
		views[index].Results = append(views[index].Results, result)
	}
	return views, nil
}

// Entries returns a page of a run's entries with their results, by row position.
func (s *Service) Entries(ctx context.Context, id string, query EntryQuery) (*EntriesPage, error) {
	run, err := s.store.GetEvalRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if query.Offset < 0 {
		return nil, invalid("offset must not be negative")
	}
	if err := s.checkBaseline(ctx, run, query); err != nil {
		return nil, err
	}
	entries, total, err := s.store.ListEvalRunEntries(ctx, configstore.EvalRunEntryListParams{
		RunID: id, Statuses: query.Statuses, Passed: query.Passed, Search: query.Search, Offset: query.Offset, Limit: pageSize(query.Limit),
		BaselineRunID: query.Baseline, Change: query.Change,
	})
	if err != nil {
		return nil, err
	}
	views, err := s.withResults(ctx, id, entries)
	if err != nil {
		return nil, err
	}
	if query.Baseline != "" {
		if err := s.attachBaseline(ctx, query.Baseline, views); err != nil {
			return nil, err
		}
	}
	return &EntriesPage{Entries: views, Total: total}, nil
}

// checkBaseline checks the baseline and the change a page of entries is read with. A
// baseline is another run on the same dataset: rows of the two are matched on the
// dataset row they came from, so a run on another dataset has none in common.
func (s *Service) checkBaseline(ctx context.Context, run *tables.TableEvalRun, query EntryQuery) error {
	switch query.Change {
	case "":
	case configstore.EvalRunChangeWorse, configstore.EvalRunChangeBetter:
		if query.Baseline == "" {
			return invalid("change needs a baseline run to measure against")
		}
	default:
		return invalid("change must be %q or %q", configstore.EvalRunChangeWorse, configstore.EvalRunChangeBetter)
	}
	if query.Baseline == "" {
		return nil
	}
	if query.Baseline == run.ID {
		return invalid("a run cannot be its own baseline")
	}
	baseline, err := s.store.GetEvalRun(ctx, query.Baseline)
	if errors.Is(err, configstore.ErrNotFound) {
		return invalid("the baseline run does not exist")
	}
	if err != nil {
		return err
	}
	if baseline.DatasetID != run.DatasetID {
		return invalid("the baseline run reads another dataset; runs are compared row by row on the same dataset")
	}
	return nil
}

// attachBaseline gives each entry the baseline run's entry for the same dataset row.
// It reads the baseline's entries for this page's rows only.
func (s *Service) attachBaseline(ctx context.Context, baselineID string, views []EntryView) error {
	if len(views) == 0 {
		return nil
	}
	rowIDs := make([]string, len(views))
	for i := range views {
		rowIDs[i] = views[i].DatasetRowID
	}
	entries, _, err := s.store.ListEvalRunEntries(ctx, configstore.EvalRunEntryListParams{RunID: baselineID, DatasetRowIDs: rowIDs})
	if err != nil {
		return err
	}
	baseline, err := s.withResults(ctx, baselineID, entries)
	if err != nil {
		return err
	}
	byRow := make(map[string]*EntryView, len(baseline))
	for i := range baseline {
		byRow[baseline[i].DatasetRowID] = &baseline[i]
	}
	for i := range views {
		views[i].Baseline = byRow[views[i].DatasetRowID]
	}
	return nil
}

// Entry returns one entry of a run with its results.
func (s *Service) Entry(ctx context.Context, runID, entryID string) (*EntryView, error) {
	// The run is read first: it carries the read scope, which the entry read does not.
	if _, err := s.store.GetEvalRun(ctx, runID); err != nil {
		return nil, err
	}
	entry, err := s.store.GetEvalRunEntry(ctx, entryID)
	if err != nil {
		return nil, err
	}
	if entry.RunID != runID {
		return nil, configstore.ErrNotFound
	}
	views, err := s.withResults(ctx, runID, []tables.TableEvalRunEntry{*entry})
	if err != nil {
		return nil, err
	}
	return &views[0], nil
}

// ============================================================================
// Stop, rerun, more evaluators, review
// ============================================================================

// Stop ends an active run. Entries that are waiting stop at once. An entry whose
// target is being called finishes that call and then stops.
//
// The stop is written at the generation read, and moves it: the stopped generation fences
// the cleanup that stops the waiting entries. A stopped run that is not finalized is not
// active, so a rerun or added evaluators can claim it before the cleanup runs; the cleanup
// then writes nothing, and their entries are left to them.
func (s *Service) Stop(ctx context.Context, id string) (*RunView, error) {
	run, err := s.store.GetEvalRun(ctx, id)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	status, generation := run.Status, run.Generation
	stoppedAt := int64(-1) // the generation the stop left the run at
	for range finalizeTries {
		if !isActive(status) {
			break
		}
		stopped, err := s.store.TransitionEvalRunAtGeneration(ctx, id, []string{tables.EvalRunQueued, tables.EvalRunRunning}, generation,
			map[string]any{"status": tables.EvalRunStopped, "generation": configstore.EvalRunNextGeneration, "updated_at": now})
		if err != nil {
			return nil, err
		}
		if stopped {
			stoppedAt = generation + 1
			break
		}
		// Another operation moved the run since it was read: read it again.
		state, err := s.store.GetEvalRunState(ctx, id)
		if err != nil {
			return nil, err
		}
		status, generation = state.Status, state.Generation
	}
	if stoppedAt < 0 && isActive(status) {
		return nil, fmt.Errorf("run %s changed during each of %d attempts to stop it", id, finalizeTries)
	}
	if stoppedAt < 0 {
		// A stop whose run write committed and whose cleanup below then failed leaves the
		// run stopped but not finalized. A repeated stop finishes that cleanup at the
		// generation the stop left; a rerun since then moved it, so this cannot touch it.
		state, err := s.store.GetEvalRunState(ctx, id)
		if err != nil {
			return nil, err
		}
		if state.Status != tables.EvalRunStopped || state.EndedAt != nil {
			return nil, invalid("the run is not active")
		}
		stoppedAt = state.Generation
	}
	if err := s.finishStop(ctx, id, stoppedAt); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// finishStop is the cleanup of a stop that left the run stopped at generation: the
// waiting entries stop, and so do entries left evaluating with nothing queued or running
// (added evaluators reopened them and their score job had not run), which nothing would
// move on under a stopped run. Then the run is finalized. The cleanup holds the run while
// it is stopped at generation; when a rerun or added evaluators claimed it since, it does
// nothing, since the entries are theirs. It is safe to repeat.
func (s *Service) finishStop(ctx context.Context, runID string, generation int64) error {
	if _, err := s.store.TransitionStoppedEvalRunEntries(ctx, runID, generation, []string{tables.EvalRunEntryQueued}, true,
		map[string]any{"status": tables.EvalRunEntryStopped, "ended_at": time.Now()}); errors.Is(err, configstore.ErrEvalRunChanged) {
		return nil
	} else if err != nil {
		return err
	}
	return s.maybeFinalize(ctx, runID)
}

// resetEntry is what a rerun writes to an entry: back to waiting, with nothing of the
// last attempt left on it.
func resetEntry(now time.Time) map[string]any {
	return map[string]any{
		"status": tables.EvalRunEntryQueued, "output": "", "tool_calls_json": nil, "context": nil, "passed": nil, "error": "", "key_id": "",
		"routed_at": nil, "claim_id": "",
		"target_latency_ms": 0, "target_prompt_tokens": 0, "target_completion_tokens": 0, "target_total_tokens": 0, "target_cost": nil,
		"queued_at": now, "started_at": nil, "ended_at": nil,
	}
}

// Rerun runs entries of a finished run again: the failed ones, all of them, or the
// listed ones. The run keeps its dataset version, its target and its evaluators, so
// the rerun measures the same thing.
func (s *Service) Rerun(ctx context.Context, id string, in RerunInput) (*RunView, error) {
	run, err := s.store.GetEvalRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.runner == nil {
		return nil, ErrNoRunner
	}
	if isActive(run.Status) {
		return nil, invalid("the run is still active; wait for it to finish, or stop it")
	}
	if err := unplanned(run); err != nil {
		return nil, err
	}
	params := configstore.EvalRunEntryListParams{RunID: id, Limit: writeBatch}
	switch {
	case len(in.EntryIDs) > 0:
		params.IDs = in.EntryIDs
	case in.Scope == RerunFailed:
		params.Statuses = []string{tables.EvalRunEntryFailed, tables.EvalRunEntryStopped}
	case in.Scope == RerunAll:
	default:
		return nil, invalid("scope must be %q or %q, or entry_ids must list the entries to rerun", RerunFailed, RerunAll)
	}
	// The entries are read a page at a time, and only their IDs: a rerun of a large
	// run never holds its variables and outputs. Paging is by position, so an entry
	// that was just put back to waiting is behind the page and is not read again.
	page, err := s.store.ListEvalRunEntryRefs(ctx, params)
	if err != nil {
		return nil, err
	}
	if len(page) == 0 {
		return nil, invalid("there is nothing to rerun")
	}

	now := time.Now()
	// An empty page must never reach the writes below: to them an empty ID list means
	// every entry of the run. The first page is not empty (checked above), and a later one
	// is empty when the selection is an exact multiple of writeBatch.
	ids := make([]string, 0, writeBatch)
	pageIDs := func() []string {
		ids = ids[:0]
		for i := range page {
			ids = append(ids, page[i].ID)
		}
		return ids
	}
	// The idle check above read the run a moment ago; this write only goes ahead if the
	// run is still finished and at the generation read, so of two requests that both saw
	// it idle, one wins. The claim's generation fences every later write of the rerun.
	//
	// The first page is reset in the claim's own transaction: a finalizer must never see
	// the run running with none of its entries open, which it would end at the claim.
	generation, claimed, err := s.claimFinishedRun(ctx, id, run.Generation, map[string]any{
		"status": tables.EvalRunRunning, "summary_json": nil, "ended_at": nil, "error": "", "updated_at": now,
	}, &configstore.EvalRunEntryUpdate{IDs: pageIDs(), Fields: resetEntry(now), DeleteResults: true}, nil)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, invalid("the run is still active; wait for it to finish, or stop it")
	}
	for len(page) == writeBatch {
		params.AfterSeq = page[len(page)-1].Seq
		if page, err = s.store.ListEvalRunEntryRefs(ctx, params); err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		// The page's results are deleted and its entries reset in one transaction, so a
		// failure leaves each entry either untouched or fully reset. The run is then
		// active; a stop ends the reset (waiting) entries as stopped, and a rerun of the
		// failed entries picks them up. Each page holds the run at the claim's generation:
		// a stop between two pages ends the rerun, and the pages after it reset nothing.
		if _, err := s.store.ResetEvalRunEntries(ctx, id, generation, pageIDs(), resetEntry(now)); errors.Is(err, configstore.ErrEvalRunChanged) {
			return nil, s.rerunInterrupted(ctx, id)
		} else if err != nil {
			return nil, err
		}
	}
	// Queueing the entries is a job of its own: the request does not wait for it, and
	// a gateway that dies while queueing leaves a message that finishes the work. The walk
	// only speeds things up: the entries are reset and their results deleted, which cannot
	// be undone, and the sweep queues reset entries without it, after StrandedAfter for a
	// model target and after the lost-message cutoff for a local one. So a failed Submit
	// still reports the rerun as started. Every step of the walk is safe to repeat, so a
	// walk message that did land does no harm.
	_ = s.runner.Submit(ctx, &evals.Job{ID: runJobID(queueJobPrefix, id, generation), Exec: noop})
	return s.Get(ctx, id)
}

// unplanned refuses a rerun or an addition on a run whose rows were never all planned
// (no start time). A run that failed while planning holds a partial row set under a total
// of 0, and finalize never ends a running run without a start time, so the claim would
// leave it running for good. A run stopped while planning gets its start time when its
// plan ends (endStoppedPlan, also from a redelivered plan job), and is rerunnable then.
func unplanned(run *tables.TableEvalRun) error {
	if run.StartedAt != nil {
		return nil
	}
	if run.Status == tables.EvalRunFailed {
		return invalid("the run failed before its rows were planned; start a new run")
	}
	return invalid("the run's planning has not ended yet; try again in a moment")
}

// stopping refuses an addition on a stopped run whose stop has not finished (no end
// time). finalizeOnce sets ended_at on a stopped run only once nothing is open, so until
// then the target work from before the stop can still be queued, and an addition would
// run it under an operation that does not call the target. Rerun takes such a run over
// on purpose and does not check this.
func stopping(run *tables.TableEvalRun) error {
	if run.Status == tables.EvalRunStopped && run.EndedAt == nil {
		return invalid("the run is still stopping; wait for it to finish, or stop it again")
	}
	return nil
}

// rerunInterrupted is the error of a rerun whose run moved on between two pages of its
// reset. It names a stop only when the run is stopped.
func (s *Service) rerunInterrupted(ctx context.Context, id string) error {
	if state, err := s.store.GetEvalRunState(ctx, id); err == nil && state.Status == tables.EvalRunStopped {
		return invalid("the run was stopped while the rerun was starting")
	}
	return invalid("the run changed while the rerun was starting; reload it and try again")
}

// claimFinishedRun moves a finished run to running with fields, and returns the claim's
// generation, which fences every later write of the operation. The claim is fenced on the
// generation read: of two requests that both saw the run finished, one wins. A review
// that rebuilt the summary of a still finished run since the read also moved it, so the
// claim reads the run again and retries while it stays finished, up to finalizeTries
// times; a run that went active reports false.
//
// entries, when set, is applied in the claim's transaction: an operation that reopens the
// run's entries must not leave a moment where the run is running with nothing open, which
// a finalizer would end at the claim's generation.
//
// refresh, when set, rebuilds fields and entries before each retry: an operation whose
// fields depend on the run it read (AddEvaluators) must not write a stale copy over what
// the operation that moved the generation stored.
func (s *Service) claimFinishedRun(ctx context.Context, id string, generation int64, fields map[string]any, entries *configstore.EvalRunEntryUpdate, refresh func(ctx context.Context) (map[string]any, *configstore.EvalRunEntryUpdate, error)) (int64, bool, error) {
	for try := range finalizeTries {
		if try > 0 && refresh != nil {
			var err error
			if fields, entries, err = refresh(ctx); err != nil {
				return 0, false, err
			}
		}
		fields["generation"] = configstore.EvalRunNextGeneration
		claimed, err := s.store.ClaimEvalRunWithEntries(ctx, id, finishedRunStatuses, generation, fields, entries)
		if err != nil || claimed {
			return generation + 1, claimed, err
		}
		state, err := s.store.GetEvalRunState(ctx, id)
		if err != nil {
			return 0, false, err
		}
		if !slices.Contains(finishedRunStatuses, state.Status) || state.Generation == generation {
			return 0, false, nil
		}
		generation = state.Generation
	}
	return 0, false, nil
}

// AddEvaluators scores a finished run's stored outputs with more evaluators. The
// target is not called again.
func (s *Service) AddEvaluators(ctx context.Context, id string, refs []EvaluatorRef) (*RunView, error) {
	run, err := s.store.GetEvalRun(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.runner == nil {
		return nil, ErrNoRunner
	}
	if isActive(run.Status) {
		return nil, invalid("the run is still active; wait for it to finish, or stop it")
	}
	if err := unplanned(run); err != nil {
		return nil, err
	}
	if err := stopping(run); err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, invalid("at least one evaluator is required")
	}
	evaluatorsJSON, err := s.addedEvaluators(ctx, run, refs)
	if err != nil {
		return nil, err
	}
	claimFields := func(evaluatorsJSON []byte) map[string]any {
		return map[string]any{"status": tables.EvalRunRunning, "evaluators_json": string(evaluatorsJSON), "summary_json": nil, "ended_at": nil, "updated_at": time.Now()}
	}
	reopen := &configstore.EvalRunEntryUpdate{From: []string{tables.EvalRunEntryCompleted}, Fields: map[string]any{"status": tables.EvalRunEntryEvaluating, "ended_at": nil}}
	// A lost claim is retried with the run read again: an AddEvaluators that claimed and
	// finished in between stored its evaluators, which this write must keep.
	prior := run
	refresh := func(ctx context.Context) (map[string]any, *configstore.EvalRunEntryUpdate, error) {
		fresh, err := s.store.GetEvalRun(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		if err := stopping(fresh); err != nil {
			return nil, nil, err
		}
		evaluatorsJSON, err := s.addedEvaluators(ctx, fresh, refs)
		if err != nil {
			return nil, nil, err
		}
		prior = fresh
		return claimFields(evaluatorsJSON), reopen, nil
	}
	// The claim's generation is its token: the undo below writes only while the run is
	// still running at it. A rename does not move it.
	//
	// Only entries that have an output can be scored. They all go back to being scored in
	// the claim's own write, before any of the new results exists: the run cannot be seen
	// as finished while some of its entries have not been reached yet, and a finalizer
	// never sees it running with nothing open. This holds for human evaluators too: the
	// status is the durable mark that an entry's results are still to be created, which a
	// resume after a lost scoring message reads (resumeRun). An entry whose new results are
	// all awaiting a reviewer completes as soon as they exist, since those hold nothing up.
	generation, claimed, err := s.claimFinishedRun(ctx, id, run.Generation, claimFields(evaluatorsJSON), reopen, refresh)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, invalid("the run is still active; wait for it to finish, or stop it")
	}
	// Creating and queueing the results is a job of its own. See Rerun.
	if err := s.runner.Submit(ctx, &evals.Job{ID: runJobID(scoreJobPrefix, id, generation), Exec: noop}); err != nil {
		if undoErr := s.undoAddEvaluators(ctx, prior, generation); undoErr != nil {
			return nil, errors.Join(err, fmt.Errorf("the run could not be put back as it was; stop it to end it: %w", undoErr))
		}
		return nil, err
	}
	return s.Get(ctx, id)
}

// addedEvaluators checks refs against a finished run, as Create checks a new run's
// evaluators, and returns the run's evaluators with refs added. An evaluator the run
// already has is refused.
func (s *Service) addedEvaluators(ctx context.Context, run *tables.TableEvalRun, refs []EvaluatorRef) ([]byte, error) {
	definitions, err := definitionsOf(run)
	if err != nil {
		return nil, err
	}
	// The checks Create runs: what the run provides, and an eval key for every model an
	// added evaluator calls. Otherwise a bad evaluator would fail on every entry.
	var columns []datasets.Column
	if version, err := s.datasets.Version(ctx, run.DatasetID, run.DatasetVersion); err == nil {
		columns = datasets.VersionColumns(version)
	} else if !errors.Is(err, configstore.ErrNotFound) {
		return nil, err
	}
	provided := append(variableNames(columns), VariableOutput, VariableToolCalls)
	var added []evals.Definition
	for _, ref := range refs {
		taken := func(definition evals.Definition) bool { return definition.ID == ref.ID }
		if slices.ContainsFunc(definitions, taken) || slices.ContainsFunc(added, taken) {
			return nil, invalid("the run already has evaluator %q", ref.ID)
		}
		definition, missing, err := s.resolveEvaluator(ctx, ref, provided)
		if errors.Is(err, configstore.ErrNotFound) {
			return nil, invalid("evaluator %q does not exist, or has no version %d", ref.ID, ref.Version)
		}
		if err != nil {
			return nil, err
		}
		if len(missing) > 0 {
			return nil, invalid("evaluator %q needs %s, which this run does not provide", definition.Name, strings.Join(missing, ", "))
		}
		if provider, model := evals.ModelOf(definition); provider != "" {
			keys, err := s.runner.Keys(ctx)
			if err != nil {
				return nil, err
			}
			if message := keyProblem(keys, modelNeed{provider, model}, fmt.Sprintf("evaluator %q", definition.Name)); message != "" {
				return nil, invalid("%s", message)
			}
		}
		added = append(added, definition)
	}
	return providerUtils.MarshalSorted(append(definitions, added...))
}

// undoAddEvaluators puts a run back as it was before AddEvaluators claimed it, when the
// scoring could not be queued. Left as it is, the run would be active with no job, and a
// stop would then end its entries as stopped, so a rerun would call the target again for
// outputs that are stored. The run and its entries move back in one write that holds the
// run while it is running at generation, the claim's: a stop, a rerun or another addition
// since the claim each moved the generation, so newer work is never undone, while a
// rename does not and keeps its name. The writes use a context the request's cancellation
// does not reach. The write is fenced on the claim's generation, so it is retried up to
// finalizeTries times; an error that remains is returned, and a stop is then the way out.
func (s *Service) undoAddEvaluators(ctx context.Context, prior *tables.TableEvalRun, generation int64) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), failWriteTimeout)
	defer cancel()
	var err error
	for range finalizeTries {
		now := time.Now()
		if _, err = s.store.TransitionEvalRunEntriesAtGeneration(ctx, prior.ID, generation, map[string]any{
			"status": prior.Status, "evaluators_json": prior.EvaluatorsJSON, "summary_json": prior.SummaryJSON, "ended_at": prior.EndedAt,
			"generation": configstore.EvalRunNextGeneration, "updated_at": now,
		}, []string{tables.EvalRunEntryEvaluating}, map[string]any{"status": tables.EvalRunEntryCompleted, "ended_at": now}); err == nil || ctx.Err() != nil {
			break
		}
	}
	return err
}

// reviewScore turns a reviewer's value into a score.
func reviewScore(value any) (evals.Score, bool) {
	switch v := value.(type) {
	case bool:
		return evals.BoolScore(v), true
	case float64:
		return evals.NumberScore(v), true
	case int:
		return evals.NumberScore(float64(v)), true
	case json.Number:
		if number, err := v.Float64(); err == nil {
			return evals.NumberScore(number), true
		}
	case string:
		return evals.StringScore(v), true
	}
	return evals.Score{}, false
}

// Review records a reviewer's score for a human evaluator on one entry.
func (s *Service) Review(ctx context.Context, runID, entryID, evaluatorID string, in ReviewInput, reviewer string) (*tables.TableEvalRunResult, error) {
	run, err := s.store.GetEvalRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	entry, err := s.store.GetEvalRunEntry(ctx, entryID)
	if err != nil {
		return nil, err
	}
	if entry.RunID != runID {
		return nil, configstore.ErrNotFound
	}
	definitions, err := definitionsOf(run)
	if err != nil {
		return nil, err
	}
	at := slices.IndexFunc(definitions, func(definition evals.Definition) bool { return definition.ID == evaluatorID })
	if at < 0 {
		return nil, configstore.ErrNotFound
	}
	if definitions[at].Type != evals.EvaluatorTypeHuman {
		return nil, invalid("only a human evaluator's result is scored by a reviewer")
	}
	cfg, err := evals.ParseConfig(definitions[at].Type, definitions[at].Config)
	if err != nil {
		return nil, err
	}
	score, ok := reviewScore(in.Score)
	if !ok {
		return nil, invalid("score must be true or false, a number, or one of the evaluator's choices")
	}
	if err := evals.CheckScore(cfg, score); err != nil {
		return nil, &evals.InvalidError{Message: err.Error()}
	}
	score = evals.ApplyNormalizer(cfg.Common().ScoreNormalizer, score)
	// A normalizer can push a valid score out of the finite range, which cannot be stored.
	if err := evals.FiniteScore(score); err != nil {
		return nil, &evals.InvalidError{Message: err.Error()}
	}
	var pass *bool
	if passed, ok := evals.JudgeEntry(cfg.Common(), score); ok {
		pass = &passed
	}
	scoreJSON, err := providerUtils.MarshalSorted(score)
	if err != nil {
		return nil, err
	}
	results, err := s.store.ListEvalRunResults(ctx, configstore.EvalRunResultListParams{RunID: runID, EntryIDs: []string{entryID}, EvaluatorID: evaluatorID})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, configstore.ErrNotFound
	}
	now := time.Now()
	wrote, err := s.store.TransitionEvalRunResult(ctx, results[0].ID, nil, map[string]any{
		"status": tables.EvalRunResultCompleted, "score_json": string(scoreJSON), "score_number": scoreNumber(score), "pass": pass, "reasoning": in.Reasoning,
		"reviewed_by": reviewer, "error": "", "completed_at": now,
	})
	if err != nil {
		return nil, err
	}
	if !wrote {
		// A rerun deleted the result since it was listed: there is nothing to count.
		return nil, configstore.ErrNotFound
	}
	// The review is counted on the run, and the run is read after that: either the
	// finalizer's summary write committed before the count, and the run reads as finished
	// here, or the count moved first and fences that write out, so the finalizer computes
	// the summary again with this score (see finalizeOnce).
	if err := s.store.CountEvalRunReview(ctx, runID); err != nil {
		return nil, err
	}
	// The entry is read after the count, and its verdict is computed again when it is
	// completed. A worker that completed it after the count was fenced on the count and
	// computed with this score (see entryProgress). When the verdict is written, the review
	// is counted again, so a finalizer that read the entries before that write computes the
	// summary again.
	rejudged, err := s.reviewVerdict(ctx, runID, entryID)
	if err != nil {
		return nil, err
	}
	if rejudged {
		if err := s.store.CountEvalRunReview(ctx, runID); err != nil {
			return nil, err
		}
	}
	if run, err = s.store.GetEvalRun(ctx, runID); err != nil {
		return nil, err
	}
	// A finished run's stored summary is rebuilt, so it follows the review. The review
	// itself is stored either way.
	if !isActive(run.Status) && len(run.Summary) > 0 {
		if err := s.rebuildSummary(ctx, run); err != nil {
			return nil, err
		}
	}
	return s.store.GetEvalRunResult(ctx, results[0].ID)
}

// rebuildSummary writes a finished run's summary again after a review. The write is
// fenced on the generation read with the run and moves it, so of two reviews that both
// computed a summary, the one that writes second is fenced out: it reads the run again
// and recomputes, now with the other review's score, up to finalizeTries times while the
// run is still finished. A rerun that started since the read is running and builds its
// own summary when it ends, so the loop stops there.
func (s *Service) rebuildSummary(ctx context.Context, run *tables.TableEvalRun) error {
	for range finalizeTries {
		definitions, err := definitionsOf(run)
		if err != nil {
			return err
		}
		wrote, err := s.writeSummary(ctx, run.ID, definitions, finishedRunStatuses, run.Generation, nil, nil)
		if err != nil || wrote {
			return err
		}
		if run, err = s.store.GetEvalRun(ctx, run.ID); errors.Is(err, configstore.ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if isActive(run.Status) || len(run.Summary) == 0 {
			return nil
		}
	}
	return fmt.Errorf("run %s changed during each of %d attempts to rebuild its summary", run.ID, finalizeTries)
}
