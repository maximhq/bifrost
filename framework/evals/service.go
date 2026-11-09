package evals

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// Execution wait bounds: how long an execute call holds the request open for a result.
const (
	DefaultExecutionWait  = 30 * time.Second
	MaxExecutionWait      = 2 * time.Minute
	executionPollInterval = 250 * time.Millisecond
)

// InvalidError is a request the caller can fix: a bad config, a missing field, an
// unknown built-in. Handlers report it as a 400.
type InvalidError struct{ Message string }

func (e *InvalidError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &InvalidError{Message: fmt.Sprintf(format, args...)}
}

// IsInvalid reports whether err is a request the caller can fix.
func IsInvalid(err error) bool {
	var target *InvalidError
	return errors.As(err, &target)
}

// ServiceStore is the part of the config store the service uses.
type ServiceStore interface {
	ListEvaluators(ctx context.Context, params configstore.EvaluatorListParams) ([]tables.TableEvaluator, int64, error)
	GetEvaluator(ctx context.Context, id string) (*tables.TableEvaluator, error)
	CreateEvaluator(ctx context.Context, evaluator *tables.TableEvaluator, version *tables.TableEvaluatorVersion) error
	UpdateEvaluator(ctx context.Context, evaluator *tables.TableEvaluator) error
	DeleteEvaluator(ctx context.Context, id string) error
	CreateEvaluatorVersion(ctx context.Context, version *tables.TableEvaluatorVersion) error
	GetEvaluatorVersion(ctx context.Context, evaluatorID string, versionNumber int) (*tables.TableEvaluatorVersion, error)
	ListEvaluatorVersions(ctx context.Context, evaluatorID string) ([]tables.TableEvaluatorVersion, error)
	GetEvaluatorVersionsAt(ctx context.Context, versionByEvaluator map[string]int) (map[string]tables.TableEvaluatorVersion, error)
	CreateEvalExecution(ctx context.Context, execution *tables.TableEvalExecution) error
	GetEvalExecution(ctx context.Context, id string) (*tables.TableEvalExecution, error)
	CompleteEvalExecution(ctx context.Context, id string, resultJSON json.RawMessage) error
}

// Service is the evaluator API: definitions, versions, the built-in catalog and
// execution. The HTTP handlers are a thin layer over it.
type Service struct {
	store  ServiceStore
	engine *Engine
	runner *Runner
}

// NewService returns a service. runner may be nil, in which case evaluators can be
// managed but not executed.
func NewService(store ServiceStore, engine *Engine, runner *Runner) *Service {
	return &Service{store: store, engine: engine, runner: runner}
}

// EvalKeyView is a provider key enabled for evaluations, as the API returns it.
type EvalKeyView struct {
	KeyID       string `json:"key_id"`
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Concurrency int    `json:"concurrency"` // max evaluations in flight on this key
}

// Keys lists the provider keys enabled for evaluations, by provider and then name. It is
// empty when nothing can execute.
func (s *Service) Keys(ctx context.Context) ([]EvalKeyView, error) {
	views := []EvalKeyView{}
	if s.runner == nil {
		return views, nil
	}
	keys, err := s.runner.Keys(ctx)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		views = append(views, EvalKeyView{KeyID: key.ID, Name: key.Name, Provider: key.Provider, Concurrency: key.Concurrency})
	}
	slices.SortFunc(views, func(a, b EvalKeyView) int {
		return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Name, b.Name), cmp.Compare(a.KeyID, b.KeyID))
	})
	return views, nil
}

// EvaluatorView is an evaluator as the API returns it, with the config of one version.
type EvaluatorView struct {
	tables.TableEvaluator
	Version           int             `json:"version"`                      // the version Config belongs to
	Config            json.RawMessage `json:"config"`                       // secrets redacted
	Variables         []string        `json:"variables"`                    // required inputs
	OptionalVariables []string        `json:"optional_variables,omitempty"` // read when present
	Executable        bool            `json:"executable"`                   // false for human evaluators
}

// EvaluatorInput is the body of a create or update.
type EvaluatorInput struct {
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	Type          EvaluatorType   `json:"type"`
	Tags          []string        `json:"tags"`
	Config        json.RawMessage `json:"config"`
	CommitMessage string          `json:"commit_message"`
	CreatedBy     string          `json:"-"`
}

// ----------------------------------------------------------------------------
// Definitions
// ----------------------------------------------------------------------------

// Create stores a new custom evaluator with its first version.
func (s *Service) Create(ctx context.Context, in EvaluatorInput) (*EvaluatorView, error) {
	return s.create(ctx, in, nil)
}

// modelProvider is the provider a config's model calls go to, or "" when the evaluator
// calls no model. It is stored beside each version for the key status filter.
func (s *Service) modelProvider(evaluatorType EvaluatorType, builtinKey string, config json.RawMessage) string {
	provider, _, needsModel := s.engine.modelOf(Definition{Type: evaluatorType, BuiltinKey: builtinKey, Config: config})
	if !needsModel {
		return ""
	}
	return provider
}

func (s *Service) create(ctx context.Context, in EvaluatorInput, builtinKey *string) (*EvaluatorView, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, invalid("name is required")
	}
	if !IsValidEvaluatorType(in.Type) {
		return nil, invalid("type must be one of ai, programmatic, statistical, api, human")
	}
	if in.Type == EvaluatorTypeStatistical && builtinKey == nil {
		return nil, invalid("a statistical evaluator is created from the catalog: install one of the built-in metrics")
	}
	config, err := s.checkedConfig(in.Type, deref(builtinKey), in.Config)
	if err != nil {
		return nil, err
	}
	evaluator := &tables.TableEvaluator{
		Name:        name,
		Description: in.Description,
		Type:        string(in.Type),
		BuiltinKey:  builtinKey,
		Tags:        in.Tags,
		CreatedBy:   in.CreatedBy,
	}
	version := &tables.TableEvaluatorVersion{
		Config: config, CommitMessage: in.CommitMessage, CreatedBy: in.CreatedBy,
		ModelProvider: s.modelProvider(in.Type, deref(builtinKey), config),
	}
	if err := s.store.CreateEvaluator(ctx, evaluator, version); err != nil {
		return nil, err
	}
	return s.view(evaluator, version), nil
}

// checkedConfig validates a config and returns its stored form. It also builds the
// evaluator, so a problem only the evaluator can see (code that does not compile, an
// unknown built-in) is reported at save time rather than at the first run.
func (s *Service) checkedConfig(evaluatorType EvaluatorType, builtinKey string, raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	config, err := NormalizeConfig(evaluatorType, raw)
	if err != nil {
		return nil, &InvalidError{Message: err.Error()}
	}
	if evaluatorType == EvaluatorTypeHuman {
		return config, nil
	}
	if _, err := s.engine.Build(Definition{Type: evaluatorType, BuiltinKey: builtinKey, Config: config}); err != nil {
		return nil, &InvalidError{Message: err.Error()}
	}
	return config, nil
}

// Get returns an evaluator with its current config.
func (s *Service) Get(ctx context.Context, id string) (*EvaluatorView, error) {
	evaluator, err := s.store.GetEvaluator(ctx, id)
	if err != nil {
		return nil, err
	}
	version, err := s.store.GetEvaluatorVersion(ctx, id, evaluator.CurrentVersion)
	if err != nil {
		return nil, err
	}
	return s.view(evaluator, version), nil
}

// EvaluatorSummary is an evaluator as a listing returns it: no config, but the few
// facts a list shows about it.
type EvaluatorSummary struct {
	tables.TableEvaluator
	Variables     []string   `json:"variables"`                // required inputs
	PassCriterion *Criterion `json:"pass_criterion,omitempty"` // the entry-level rule
	LowerIsBetter bool       `json:"lower_is_better"`
	Provider      string     `json:"provider,omitempty"` // judge or embedding provider, when it calls a model
	Model         string     `json:"model,omitempty"`
	Endpoint      string     `json:"endpoint,omitempty"` // method and host, for an API evaluator
}

// List returns a page of evaluators with a summary of each one's current config, and
// the total before paging. The configs are read in one query for the whole page.
func (s *Service) List(ctx context.Context, params configstore.EvaluatorListParams) ([]EvaluatorSummary, int64, error) {
	if params.KeyStatus != "" {
		// Which providers have an eval key is live state the store cannot see.
		keys, err := s.Keys(ctx)
		if err != nil {
			return nil, 0, err
		}
		params.KeyProviders = params.KeyProviders[:0]
		for _, key := range keys {
			if !slices.Contains(params.KeyProviders, key.Provider) {
				params.KeyProviders = append(params.KeyProviders, key.Provider)
			}
		}
	}
	evaluators, total, err := s.store.ListEvaluators(ctx, params)
	if err != nil {
		return nil, 0, err
	}
	current := make(map[string]int, len(evaluators))
	for _, evaluator := range evaluators {
		current[evaluator.ID] = evaluator.CurrentVersion
	}
	versions, err := s.store.GetEvaluatorVersionsAt(ctx, current)
	if err != nil {
		return nil, 0, err
	}
	summaries := make([]EvaluatorSummary, len(evaluators))
	for i, evaluator := range evaluators {
		summaries[i] = s.summarize(evaluator, versions[evaluator.ID])
	}
	return summaries, total, nil
}

// summarize reads the listing facts out of an evaluator's config. A config that no
// longer parses still lists, with the facts left empty.
func (s *Service) summarize(evaluator tables.TableEvaluator, version tables.TableEvaluatorVersion) EvaluatorSummary {
	summary := EvaluatorSummary{TableEvaluator: evaluator, Variables: []string{}}
	evaluatorType := EvaluatorType(evaluator.Type)
	cfg, err := ParseConfig(evaluatorType, version.Config)
	if err != nil {
		return summary
	}
	common := cfg.Common()
	summary.PassCriterion = &common.PassCriteria.EntryLevel
	summary.LowerIsBetter = common.Reversed
	summary.Variables = s.describe(Definition{
		ID: evaluator.ID, Type: evaluatorType, BuiltinKey: deref(evaluator.BuiltinKey), Version: version.Version, Config: version.Config,
	}).Variables
	switch c := cfg.(type) {
	case *AIConfig:
		summary.Provider, summary.Model = c.Provider, c.Model
	case *StatisticalConfig:
		summary.Provider, summary.Model = c.EmbeddingProvider, c.EmbeddingModel
	case *APIConfig:
		// Only the method and host: a path or query may carry identifiers.
		if parsed, err := url.Parse(c.URL); err == nil {
			summary.Endpoint = c.Method + " " + parsed.Host
		}
	}
	return summary
}

// Update changes an evaluator. Name, description and tags change in place. A config
// that differs from the current one becomes a new version; an unchanged config does not.
func (s *Service) Update(ctx context.Context, id string, in EvaluatorInput) (*EvaluatorView, error) {
	evaluator, err := s.store.GetEvaluator(ctx, id)
	if err != nil {
		return nil, err
	}
	current, err := s.store.GetEvaluatorVersion(ctx, id, evaluator.CurrentVersion)
	if err != nil {
		return nil, err
	}
	if in.Type != "" && string(in.Type) != evaluator.Type {
		return nil, invalid("the type of an evaluator cannot be changed")
	}
	if name := strings.TrimSpace(in.Name); name != "" {
		evaluator.Name = name
	}
	evaluator.Description = in.Description
	evaluator.Tags = in.Tags

	// The config is checked before anything is written: a save that is refused must
	// not leave the new name, description and tags behind.
	var next *tables.TableEvaluatorVersion
	if len(in.Config) > 0 {
		evaluatorType := EvaluatorType(evaluator.Type)
		// A client saves the redacted config it was shown; stored secrets are put back first.
		incoming, err := RestoreRedactedSecrets(evaluatorType, in.Config, current.Config)
		if err != nil {
			return nil, &InvalidError{Message: err.Error()}
		}
		config, err := s.checkedConfig(evaluatorType, deref(evaluator.BuiltinKey), incoming)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(config, current.Config) {
			next = &tables.TableEvaluatorVersion{
				EvaluatorID: id, Config: config, CommitMessage: in.CommitMessage, CreatedBy: in.CreatedBy,
				ModelProvider: s.modelProvider(evaluatorType, deref(evaluator.BuiltinKey), config),
			}
		}
	}
	if err := s.store.UpdateEvaluator(ctx, evaluator); err != nil {
		return nil, err
	}
	if next != nil {
		if err := s.store.CreateEvaluatorVersion(ctx, next); err != nil {
			return nil, err
		}
		evaluator.CurrentVersion = next.Version
		current = next
	}
	return s.view(evaluator, current), nil
}

// Delete soft-deletes an evaluator.
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.store.DeleteEvaluator(ctx, id)
}

// Duplicate copies an evaluator's current config under a new name.
func (s *Service) Duplicate(ctx context.Context, id, name, createdBy string) (*EvaluatorView, error) {
	evaluator, err := s.store.GetEvaluator(ctx, id)
	if err != nil {
		return nil, err
	}
	version, err := s.store.GetEvaluatorVersion(ctx, id, evaluator.CurrentVersion)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		name = evaluator.Name + " (copy)"
	}
	// The stored config is copied as it is, sealed secrets included.
	return s.create(ctx, EvaluatorInput{
		Name:          name,
		Description:   evaluator.Description,
		Type:          EvaluatorType(evaluator.Type),
		Tags:          evaluator.Tags,
		Config:        version.Config,
		CommitMessage: "Copied from " + evaluator.Name,
		CreatedBy:     createdBy,
	}, evaluator.BuiltinKey)
}

// Versions lists an evaluator's versions, newest first, with secrets redacted.
func (s *Service) Versions(ctx context.Context, id string) ([]tables.TableEvaluatorVersion, error) {
	evaluator, err := s.store.GetEvaluator(ctx, id)
	if err != nil {
		return nil, err
	}
	versions, err := s.store.ListEvaluatorVersions(ctx, id)
	if err != nil {
		return nil, err
	}
	for i := range versions {
		versions[i].Config = RedactConfig(EvaluatorType(evaluator.Type), versions[i].Config)
	}
	return versions, nil
}

// Version returns an evaluator as of one version.
func (s *Service) Version(ctx context.Context, id string, versionNumber int) (*EvaluatorView, error) {
	evaluator, err := s.store.GetEvaluator(ctx, id)
	if err != nil {
		return nil, err
	}
	version, err := s.store.GetEvaluatorVersion(ctx, id, versionNumber)
	if err != nil {
		return nil, err
	}
	return s.view(evaluator, version), nil
}

// view assembles the API form of an evaluator at a version.
func (s *Service) view(evaluator *tables.TableEvaluator, version *tables.TableEvaluatorVersion) *EvaluatorView {
	evaluatorType := EvaluatorType(evaluator.Type)
	view := &EvaluatorView{
		TableEvaluator: *evaluator,
		Version:        version.Version,
		Config:         RedactConfig(evaluatorType, version.Config),
		Executable:     evaluatorType != EvaluatorTypeHuman,
	}
	described := s.describe(Definition{
		ID: evaluator.ID, Type: evaluatorType, BuiltinKey: deref(evaluator.BuiltinKey), Version: version.Version, Config: version.Config,
	})
	view.Variables, view.OptionalVariables = described.Variables, described.OptionalVariables
	return view
}

// ----------------------------------------------------------------------------
// Validation
// ----------------------------------------------------------------------------

// Validation is the outcome of checking a config without saving or running it.
type Validation struct {
	Valid             bool            `json:"valid"`
	Error             string          `json:"error,omitempty"`
	Variables         []string        `json:"variables"`
	OptionalVariables []string        `json:"optional_variables,omitempty"`
	Config            json.RawMessage `json:"config,omitempty"` // the config with defaults filled in
}

// Validate checks a config as a save would, and reports the variables it reads. An
// editor calls it as the user types.
func (s *Service) Validate(evaluatorType EvaluatorType, builtinKey string, raw json.RawMessage) Validation {
	if !IsValidEvaluatorType(evaluatorType) {
		return Validation{Error: "type must be one of ai, programmatic, statistical, api, human", Variables: []string{}}
	}
	config, err := s.checkedConfig(evaluatorType, builtinKey, raw)
	if err != nil {
		return Validation{Error: err.Error(), Variables: []string{}}
	}
	out := s.describe(Definition{Type: evaluatorType, BuiltinKey: builtinKey, Config: config})
	out.Valid = true
	out.Config = RedactConfig(evaluatorType, config)
	return out
}

// describe reports the variables a definition reads.
func (s *Service) describe(def Definition) Validation {
	out := Validation{Variables: []string{}}
	cfg, err := ParseConfig(def.Type, def.Config)
	if err != nil {
		return out
	}
	out.OptionalVariables = sortedKeys(cfg.Common().OptionalVariables)
	if def.Type == EvaluatorTypeHuman {
		out.Variables = sortedKeys(cfg.Common().Variables)
		return out
	}
	if b, err := s.engine.build(def); err == nil {
		out.Variables = append(out.Variables, b.required...)
	}
	if programmatic, ok := cfg.(*ProgrammaticConfig); ok {
		if params, err := ProgrammaticVariables(programmatic.Code); err == nil {
			out.OptionalVariables = append(out.OptionalVariables, params.Optional...)
		}
	}
	if entry, ok := CatalogEntryFor(def.BuiltinKey); ok && def.BuiltinKey != "" {
		out.OptionalVariables = append(out.OptionalVariables, entry.OptionalVariables...)
	}
	return out
}

// ----------------------------------------------------------------------------
// Catalog
// ----------------------------------------------------------------------------

// InstallInput is the body of a catalog install.
type InstallInput struct {
	Name         string         `json:"name"` // defaults to the built-in's name
	Description  string         `json:"description"`
	Tags         []string       `json:"tags"`
	Provider     string         `json:"provider"`
	Model        string         `json:"model"`
	ModelParams  map[string]any `json:"model_params"`
	PassCriteria *PassCriteria  `json:"pass_criteria"`
	CreatedBy    string         `json:"-"`
}

// Install creates an evaluator from a built-in.
func (s *Service) Install(ctx context.Context, key string, in InstallInput) (*EvaluatorView, error) {
	entry, ok := CatalogEntryFor(key)
	if !ok {
		return nil, configstore.ErrNotFound
	}
	config, err := entry.InstallConfig(InstallOptions{Provider: in.Provider, Model: in.Model, ModelParams: in.ModelParams, PassCriteria: in.PassCriteria})
	if err != nil {
		return nil, &InvalidError{Message: err.Error()}
	}
	name, description := in.Name, in.Description
	if strings.TrimSpace(name) == "" {
		name = entry.Name
	}
	if description == "" {
		description = entry.Description
	}
	return s.create(ctx, EvaluatorInput{
		Name: name, Description: description, Type: entry.Type, Tags: in.Tags, Config: config,
		CommitMessage: "Installed from the catalog", CreatedBy: in.CreatedBy,
	}, &key)
}

// ----------------------------------------------------------------------------
// Execution
// ----------------------------------------------------------------------------

// ExecuteInput asks for one evaluator run. Either EvaluatorID names a saved evaluator,
// or Draft carries an unsaved one.
type ExecuteInput struct {
	EvaluatorID string            `json:"-"`
	Version     int               `json:"version"` // 0 means the current version
	Draft       *DraftEvaluator   `json:"draft"`
	Variables   map[string]string `json:"variables"`
	WaitMs      *int              `json:"wait_ms"` // how long to hold the call for a result
	CreatedBy   string            `json:"-"`
}

// DraftEvaluator is an evaluator that has not been saved, as an editor's test panel
// sends it.
type DraftEvaluator struct {
	Type       EvaluatorType   `json:"type"`
	BuiltinKey string          `json:"builtin_key"`
	Config     json.RawMessage `json:"config"`
	// EvaluatorID is the evaluator being edited, if any. It lets a draft that still
	// carries redacted secrets run with the stored ones.
	EvaluatorID string `json:"evaluator_id"`
}

// ExecutionView is an execution as the API returns it.
type ExecutionView struct {
	ID               string     `json:"id"`
	Status           string     `json:"status"`
	EvaluatorID      *string    `json:"evaluator_id,omitempty"`
	EvaluatorVersion int        `json:"evaluator_version,omitempty"`
	KeyID            string     `json:"key_id,omitempty"`
	Result           *Result    `json:"result,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
}

// Execute queues one evaluator run and waits for its result. Every run goes through
// the queue, so a model-calling evaluator always runs on an eval key within that key's
// concurrency. If the result is not ready when the wait ends, the execution is
// returned as it stands and the caller polls Execution for the rest.
func (s *Service) Execute(ctx context.Context, in ExecuteInput) (*ExecutionView, error) {
	if s.runner == nil {
		return nil, errors.New("evaluations are not available: no queue is configured")
	}
	def, err := s.definitionFor(ctx, in)
	if err != nil {
		return nil, err
	}
	// Built here so a broken evaluator is refused at once instead of failing in the queue.
	if _, err := s.engine.Build(def); err != nil {
		if errors.Is(err, ErrNotExecutable) {
			return nil, invalid("a human evaluator is scored by reviewers and cannot be executed")
		}
		return nil, &InvalidError{Message: err.Error()}
	}

	snapshot, err := json.Marshal(def)
	if err != nil {
		return nil, err
	}
	variables := in.Variables
	if variables == nil {
		variables = map[string]string{}
	}
	variablesJSON, err := json.Marshal(variables)
	if err != nil {
		return nil, err
	}
	execution := &tables.TableEvalExecution{
		EvaluatorVersion: def.Version,
		Definition:       snapshot,
		Variables:        variablesJSON,
		CreatedBy:        in.CreatedBy,
	}
	if def.ID != "" {
		execution.EvaluatorID = &def.ID
	}
	if err := s.store.CreateEvalExecution(ctx, execution); err != nil {
		return nil, err
	}

	done, cancel := s.runner.Watch(execution.ID)
	defer cancel()
	if err := s.runner.Submit(ctx, &Job{ID: execution.ID, Definition: def, Input: Input{Variables: variables}}); err != nil {
		// The row must not sit queued forever for a job that was never placed.
		if failed, marshalErr := json.Marshal(errorResult(err)); marshalErr == nil {
			_ = s.store.CompleteEvalExecution(context.WithoutCancel(ctx), execution.ID, failed)
		}
		return nil, err
	}
	return s.await(ctx, execution.ID, done, waitFor(in.WaitMs))
}

// definitionFor resolves what an execute call should run.
func (s *Service) definitionFor(ctx context.Context, in ExecuteInput) (Definition, error) {
	if in.Draft != nil {
		draft := in.Draft
		if !IsValidEvaluatorType(draft.Type) {
			return Definition{}, invalid("draft.type must be one of ai, programmatic, statistical, api, human")
		}
		raw := draft.Config
		if len(raw) == 0 {
			raw = json.RawMessage(`{}`)
		}
		if draft.EvaluatorID != "" {
			saved, err := s.store.GetEvaluator(ctx, draft.EvaluatorID)
			if err != nil {
				return Definition{}, err
			}
			current, err := s.store.GetEvaluatorVersion(ctx, saved.ID, saved.CurrentVersion)
			if err != nil {
				return Definition{}, err
			}
			restored, err := RestoreRedactedSecrets(draft.Type, raw, current.Config)
			if err != nil {
				return Definition{}, &InvalidError{Message: err.Error()}
			}
			raw = restored
		}
		config, err := NormalizeConfig(draft.Type, raw)
		if err != nil {
			return Definition{}, &InvalidError{Message: err.Error()}
		}
		// No ID or version: a draft has no stable identity, so it is never cached.
		return Definition{Type: draft.Type, BuiltinKey: draft.BuiltinKey, Config: config}, nil
	}
	if in.EvaluatorID == "" {
		return Definition{}, invalid("an evaluator id or a draft is required")
	}
	evaluator, err := s.store.GetEvaluator(ctx, in.EvaluatorID)
	if err != nil {
		return Definition{}, err
	}
	versionNumber := in.Version
	if versionNumber <= 0 {
		versionNumber = evaluator.CurrentVersion
	}
	version, err := s.store.GetEvaluatorVersion(ctx, evaluator.ID, versionNumber)
	if err != nil {
		return Definition{}, err
	}
	return Definition{
		ID: evaluator.ID, Name: evaluator.Name, Type: EvaluatorType(evaluator.Type),
		BuiltinKey: deref(evaluator.BuiltinKey), Version: version.Version, Config: version.Config,
	}, nil
}

// waitFor bounds the requested wait.
func waitFor(waitMs *int) time.Duration {
	if waitMs == nil {
		return DefaultExecutionWait
	}
	wait := time.Duration(*waitMs) * time.Millisecond
	if wait < 0 {
		return 0
	}
	return min(wait, MaxExecutionWait)
}

// await waits for an execution to complete. This instance is told directly when it
// runs the job itself; a job run by another instance is seen by polling the store.
func (s *Service) await(ctx context.Context, id string, done <-chan struct{}, wait time.Duration) (*ExecutionView, error) {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	poll := time.NewTicker(executionPollInterval)
	defer poll.Stop()
	for {
		view, err := s.Execution(ctx, id)
		if err != nil {
			return nil, err
		}
		if view.Status == tables.EvalExecutionCompleted || wait <= 0 {
			return view, nil
		}
		select {
		case <-done:
			done = nil // a closed channel would spin the loop; the next read sees the result
		case <-poll.C:
		case <-deadline.C:
			return s.Execution(ctx, id)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Execution returns an execution and, once it has completed, its result.
func (s *Service) Execution(ctx context.Context, id string) (*ExecutionView, error) {
	execution, err := s.store.GetEvalExecution(ctx, id)
	if err != nil {
		return nil, err
	}
	view := &ExecutionView{
		ID:               execution.ID,
		Status:           execution.Status,
		EvaluatorID:      execution.EvaluatorID,
		EvaluatorVersion: execution.EvaluatorVersion,
		KeyID:            execution.KeyID,
		CreatedAt:        execution.CreatedAt,
		StartedAt:        execution.StartedAt,
		CompletedAt:      execution.CompletedAt,
	}
	if len(execution.Result) > 0 {
		result, err := DecodeResult(execution.Result)
		if err != nil {
			return nil, err
		}
		view.Result = &result
	}
	return view, nil
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
