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
	"github.com/maximhq/bifrost/framework/lrucache"
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
	DeleteEvalRun(ctx context.Context, id string) error
	CreateEvalRunEntries(ctx context.Context, entries []tables.TableEvalRunEntry) error
	ListEvalRunEntries(ctx context.Context, params configstore.EvalRunEntryListParams) ([]tables.TableEvalRunEntry, int64, error)
	ListEvalRunEntryRefs(ctx context.Context, params configstore.EvalRunEntryListParams) ([]configstore.EvalRunEntryRef, error)
	HasOpenEvalRunEntries(ctx context.Context, runID string) (bool, error)
	GetEvalRunEntry(ctx context.Context, id string) (*tables.TableEvalRunEntry, error)
	TransitionEvalRunEntry(ctx context.Context, id string, from []string, fields map[string]any) (bool, error)
	TransitionEvalRunEntries(ctx context.Context, runID string, from, ids []string, fields map[string]any) (int64, error)
	RouteEvalRunEntry(ctx context.Context, id, fromKey, toKey string) (bool, error)
	RouteEvalRunResult(ctx context.Context, id, fromKey, toKey string) (bool, error)
	GetEvalRunLastSeq(ctx context.Context, runID string) (int64, error)
	ListEvalRunRowIDs(ctx context.Context, runIDs []string, offset, limit int) ([]string, int64, error)
	CountEvalRunResultsByEvaluator(ctx context.Context, runID string) ([]configstore.EvalRunEvaluatorCount, error)
	CountEvalRunEntries(ctx context.Context, runID string) (map[string]int64, error)
	CountEvalRunEntriesPassed(ctx context.Context, runID string) (int64, error)
	ListEvalRunTargetLatencies(ctx context.Context, runID string) ([]int64, error)
	GetEvalRunTotals(ctx context.Context, runID string) (configstore.EvalRunTotals, error)
	CreateEvalRunResults(ctx context.Context, results []tables.TableEvalRunResult) error
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
	Keys(ctx context.Context) ([]evals.EvalKey, error)
}

// Options are the dependencies of the run service.
type Options struct {
	Store      Store
	Datasets   Datasets
	Evaluators Evaluators
	Engine     Engine
	// Model calls the model under test for prompt and model targets.
	Model evals.ModelCaller
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
	specs *lrucache.Cache[*runSpec]
}

// NewService returns a run service. Call SetRunner before starting a run: the runner
// is built on the job store, which needs the service first.
func NewService(opts Options) *Service {
	return &Service{
		store: opts.Store, datasets: opts.Datasets, evaluators: opts.Evaluators, engine: opts.Engine, model: opts.Model,
		specs: lrucache.New[*runSpec](specCacheSize),
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
	}
	provided := variableNames(columns)

	// Providers whose eval key the run needs, with what needs it, for the message.
	needsKey := map[string]string{}
	out.target, err = s.resolveTarget(ctx, in, provided, problem)
	if err != nil {
		return nil, nil, err
	}
	if out.target.Type == TargetPrompt || out.target.Type == TargetModel {
		if out.target.Provider != "" {
			needsKey[out.target.Provider] = "the target"
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
		if provider := judgeProvider(definition); provider != "" {
			if _, ok := needsKey[provider]; !ok {
				needsKey[provider] = fmt.Sprintf("evaluator %q", definition.Name)
			}
		}
		out.definitions = append(out.definitions, definition)
	}

	if len(needsKey) > 0 && s.runner != nil {
		keys, err := s.runner.Keys(ctx)
		if err != nil {
			return nil, nil, err
		}
		providers := make([]string, 0, len(needsKey))
		for provider := range needsKey {
			providers = append(providers, provider)
		}
		slices.Sort(providers)
		for _, provider := range providers {
			if !slices.ContainsFunc(keys, func(key evals.EvalKey) bool { return key.Provider == provider }) {
				problem("%s calls provider %q, which has no key marked \"use for evaluations\"", needsKey[provider], provider)
			}
		}
	}
	return out, problems, nil
}

// resolveEvaluator loads an evaluator's definition and lists the variables it needs
// that the run does not provide.
func (s *Service) resolveEvaluator(ctx context.Context, ref EvaluatorRef, provided []string) (evals.Definition, []string, error) {
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

// judgeProvider is the provider an AI evaluator's judge model is on, or "".
func judgeProvider(definition evals.Definition) string {
	cfg, err := evals.ParseConfig(definition.Type, definition.Config)
	if err != nil {
		return ""
	}
	if ai, ok := cfg.(*evals.AIConfig); ok {
		return ai.Provider
	}
	return ""
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
		return Target{Type: TargetModel, Provider: target.Provider, Model: target.Model, System: target.System, Params: target.Params}, nil
	case TargetPrompt:
		if target.PromptID == "" {
			problem("a prompt target needs a prompt")
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
	if number <= 0 {
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
			case errors.Is(err, configstore.ErrEvalDatasetNoChanges) && version > 0:
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
		TargetConcurrency: in.TargetConcurrency, CreatedBy: in.CreatedBy,
	}
	if in.SplitID != "" {
		run.SplitID = new(in.SplitID)
	}
	if err := s.store.CreateEvalRun(ctx, run); err != nil {
		return nil, err
	}
	if err := s.runner.Submit(ctx, &evals.Job{ID: planJobID(run.ID)}); err != nil {
		s.failRun(ctx, run.ID, err)
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

// List returns runs with their counts. A finished run carries its summary.
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
	var tagsJSON *string
	if in.Tags != nil {
		data, err := json.Marshal(in.Tags)
		if err != nil {
			return nil, err
		}
		tagsJSON = new(string(data))
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
	return s.store.DeleteEvalRun(ctx, id)
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
func (s *Service) Stop(ctx context.Context, id string) (*RunView, error) {
	if _, err := s.store.GetEvalRun(ctx, id); err != nil {
		return nil, err
	}
	now := time.Now()
	stopped, err := s.store.TransitionEvalRun(ctx, id, []string{tables.EvalRunQueued, tables.EvalRunRunning}, map[string]any{"status": tables.EvalRunStopped, "updated_at": now})
	if err != nil {
		return nil, err
	}
	if !stopped {
		return nil, invalid("the run is not active")
	}
	if _, err := s.store.TransitionEvalRunEntries(ctx, id, []string{tables.EvalRunEntryQueued}, nil, map[string]any{"status": tables.EvalRunEntryStopped, "ended_at": now}); err != nil {
		return nil, err
	}
	if err := s.maybeFinalize(ctx, id); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// resetEntry is what a rerun writes to an entry: back to waiting, with nothing of the
// last attempt left on it.
func resetEntry(now time.Time) map[string]any {
	return map[string]any{
		"status": tables.EvalRunEntryQueued, "output": "", "tool_calls_json": nil, "context": nil, "passed": nil, "error": "", "key_id": "",
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
	if _, err := s.store.TransitionEvalRun(ctx, id, nil, map[string]any{
		"status": tables.EvalRunRunning, "summary_json": nil, "ended_at": nil, "error": "", "updated_at": now,
	}); err != nil {
		return nil, err
	}
	ids := make([]string, 0, writeBatch)
	for {
		ids = ids[:0]
		for i := range page {
			ids = append(ids, page[i].ID)
		}
		if _, err := s.store.DeleteEvalRunResults(ctx, id, ids, ""); err != nil {
			return nil, err
		}
		if _, err := s.store.TransitionEvalRunEntries(ctx, id, nil, ids, resetEntry(now)); err != nil {
			return nil, err
		}
		if len(page) < writeBatch {
			break
		}
		params.AfterSeq = page[len(page)-1].Seq
		if page, err = s.store.ListEvalRunEntryRefs(ctx, params); err != nil {
			return nil, err
		}
	}
	// Queueing the entries is a job of its own: the request does not wait for it, and
	// a gateway that dies while queueing leaves a message that finishes the work.
	if err := s.runner.Submit(ctx, &evals.Job{ID: runJobID(queueJobPrefix, id), Exec: noop}); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
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
	if len(refs) == 0 {
		return nil, invalid("at least one evaluator is required")
	}
	definitions, err := definitionsOf(run)
	if err != nil {
		return nil, err
	}
	var added []evals.Definition
	for _, ref := range refs {
		taken := func(definition evals.Definition) bool { return definition.ID == ref.ID }
		if slices.ContainsFunc(definitions, taken) || slices.ContainsFunc(added, taken) {
			return nil, invalid("the run already has evaluator %q", ref.ID)
		}
		definition, err := s.evaluators.Definition(ctx, ref.ID, ref.Version)
		if errors.Is(err, configstore.ErrNotFound) {
			return nil, invalid("evaluator %q does not exist, or has no version %d", ref.ID, ref.Version)
		}
		if err != nil {
			return nil, err
		}
		added = append(added, definition)
	}
	evaluatorsJSON, err := providerUtils.MarshalSorted(append(definitions, added...))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if _, err := s.store.TransitionEvalRun(ctx, id, nil, map[string]any{
		"status": tables.EvalRunRunning, "evaluators_json": string(evaluatorsJSON), "summary_json": nil, "ended_at": nil, "updated_at": now,
	}); err != nil {
		return nil, err
	}

	// Only entries that have an output can be scored. They all go back to being
	// scored in one write, before any of the new results exists: the run cannot be
	// seen as finished while some of its entries have not been reached yet.
	if slices.ContainsFunc(added, func(definition evals.Definition) bool { return definition.Type != evals.EvaluatorTypeHuman }) {
		if _, err := s.store.TransitionEvalRunEntries(ctx, id, []string{tables.EvalRunEntryCompleted}, nil,
			map[string]any{"status": tables.EvalRunEntryEvaluating, "ended_at": nil}); err != nil {
			return nil, err
		}
	}
	// Creating and queueing the results is a job of its own. See Rerun.
	if err := s.runner.Submit(ctx, &evals.Job{ID: runJobID(scoreJobPrefix, id), Exec: noop}); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
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
	var pass *bool
	if passed, ok := evals.EvaluateEntry(cfg.Common().PassCriteria.EntryLevel, score); ok {
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
	if _, err := s.store.TransitionEvalRunResult(ctx, results[0].ID, nil, map[string]any{
		"status": tables.EvalRunResultCompleted, "score_json": string(scoreJSON), "score_number": scoreNumber(score), "pass": pass, "reasoning": in.Reasoning,
		"reviewed_by": reviewer, "error": "", "completed_at": now,
	}); err != nil {
		return nil, err
	}
	if entry.Status == tables.EvalRunEntryCompleted {
		passed, err := s.entryPassed(ctx, runID, entryID)
		if err != nil {
			return nil, err
		}
		if _, err := s.store.TransitionEvalRunEntry(ctx, entryID, nil, map[string]any{"passed": passed}); err != nil {
			return nil, err
		}
	}
	// A finished run's stored summary is rebuilt, so it follows the review.
	if !isActive(run.Status) && len(run.Summary) > 0 {
		if err := s.writeSummary(ctx, run.ID, definitions, nil, nil); err != nil {
			return nil, err
		}
	}
	return s.store.GetEvalRunResult(ctx, results[0].ID)
}
