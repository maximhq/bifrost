package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/framework/lrucache"
)

// ErrNotExecutable is returned when an evaluator type cannot be run by the engine, such
// as a human evaluator that a reviewer scores by hand.
var ErrNotExecutable = errors.New("this evaluator type is not executed automatically")

// Default engine limits.
const (
	defaultEvaluatorTimeout = 2 * time.Minute
	// defaultAIEvaluatorTimeout bounds one run of an AI evaluator. Every model call in
	// it has its own limit of 90 seconds, and a built-in can make seven or more in a
	// row, so the run as a whole needs far longer than an evaluator that calls nothing.
	defaultAIEvaluatorTimeout      = 15 * time.Minute
	defaultProgrammaticConcurrency = 10
	defaultStatisticalConcurrency  = 10
	defaultAPIPoolConcurrency      = 5
	// defaultEvaluatorCacheSize bounds how many built evaluator versions stay in memory.
	defaultEvaluatorCacheSize = 2048
)

// Input is what an evaluator scores. Every value is a string, as in Maxim.
type Input struct {
	Variables map[string]string `json:"variables"`
}

// Evaluator scores one input.
type Evaluator interface {
	Evaluate(ctx context.Context, in Input) (Result, error)
}

// VariableRequirer is implemented by evaluators that know which variables they need
// beyond the ones their config lists, such as a built-in or a parsed function.
type VariableRequirer interface {
	RequiredVariables() []string
}

// Definition is everything the engine needs to run a stored evaluator.
type Definition struct {
	ID         string          `json:"id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Type       EvaluatorType   `json:"type"`
	BuiltinKey string          `json:"builtin_key,omitempty"`
	Version    int             `json:"version,omitempty"`
	Config     json.RawMessage `json:"config"`
}

// Factory builds an evaluator from a definition and its parsed config.
type Factory func(def Definition, cfg Config) (Evaluator, error)

// EngineOptions tunes the engine. Zero values take the defaults. There is no pool for
// AI evaluators: their concurrency is a property of the provider key they run on, and
// the runner enforces it through the queue.
type EngineOptions struct {
	Timeout                 time.Duration // per evaluator run
	AITimeout               time.Duration // per run of an AI evaluator, which may make many model calls
	ProgrammaticConcurrency int
	StatisticalConcurrency  int
	APIConcurrency          int
}

type registryKey struct {
	evaluatorType EvaluatorType
	builtinKey    string
}

// built is an evaluator ready to run, with everything that depends only on its
// definition already worked out.
type built struct {
	evaluator Evaluator
	cfg       Config
	required  []string // variables that must be present, sorted
}

// Engine builds and runs evaluators. It is safe for concurrent use.
type Engine struct {
	timeout   time.Duration
	aiTimeout time.Duration
	pools     map[EvaluatorType]chan struct{}
	// cache holds built evaluators by ID and version. A version is immutable, so an
	// entry never goes stale; a test run reuses one build for all of its entries.
	cache *lrucache.Cache[*built]

	mu       sync.RWMutex
	registry map[registryKey]Factory
}

// NewEngine returns an engine with no evaluators registered.
func NewEngine(opts EngineOptions) *Engine {
	pool := func(size, fallback int) chan struct{} {
		if size <= 0 {
			size = fallback
		}
		return make(chan struct{}, size)
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultEvaluatorTimeout
	}
	aiTimeout := opts.AITimeout
	if aiTimeout <= 0 {
		aiTimeout = defaultAIEvaluatorTimeout
	}
	return &Engine{
		timeout:   timeout,
		aiTimeout: aiTimeout,
		pools: map[EvaluatorType]chan struct{}{
			EvaluatorTypeProgrammatic: pool(opts.ProgrammaticConcurrency, defaultProgrammaticConcurrency),
			EvaluatorTypeStatistical:  pool(opts.StatisticalConcurrency, defaultStatisticalConcurrency),
			EvaluatorTypeAPI:          pool(opts.APIConcurrency, defaultAPIPoolConcurrency),
		},
		registry: make(map[registryKey]Factory),
		cache:    lrucache.New[*built](defaultEvaluatorCacheSize),
	}
}

// Register installs the factory for an evaluator type. builtinKey is empty for the
// custom evaluator of that type and names a catalog entry otherwise.
func (e *Engine) Register(evaluatorType EvaluatorType, builtinKey string, factory Factory) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.registry[registryKey{evaluatorType, builtinKey}] = factory
}

// Build validates a definition and returns its evaluator.
func (e *Engine) Build(def Definition) (Evaluator, error) {
	b, err := e.build(def)
	if err != nil {
		return nil, err
	}
	return b.evaluator, nil
}

// build returns the built evaluator for a definition, from the cache when the
// definition names a saved version. An unsaved draft has no stable identity and is
// built every time.
func (e *Engine) build(def Definition) (*built, error) {
	if def.ID == "" || def.Version <= 0 {
		return e.buildUncached(def)
	}
	key := def.ID + "@" + strconv.Itoa(def.Version)
	if cached, ok := e.cache.Get(key); ok {
		return cached, nil
	}
	return e.cache.Fill(context.Background(), key, func() (*built, string, error) {
		b, err := e.buildUncached(def)
		return b, "", err
	})
}

func (e *Engine) buildUncached(def Definition) (*built, error) {
	cfg, err := ParseConfig(def.Type, def.Config)
	if err != nil {
		return nil, err
	}
	if def.Type == EvaluatorTypeHuman {
		return nil, ErrNotExecutable
	}
	e.mu.RLock()
	factory, ok := e.registry[registryKey{def.Type, def.BuiltinKey}]
	e.mu.RUnlock()
	if !ok {
		if def.BuiltinKey != "" {
			return nil, fmt.Errorf("no %s evaluator is registered for built-in %q", def.Type, def.BuiltinKey)
		}
		return nil, fmt.Errorf("no evaluator is registered for type %q", def.Type)
	}
	evaluator, err := factory(def, cfg)
	if err != nil {
		return nil, err
	}
	return &built{evaluator: evaluator, cfg: cfg, required: requiredVariables(cfg, evaluator)}, nil
}

// modelOf reports the provider and model an evaluator calls, if it calls one. It uses
// the build cache, so placing a job on a key costs no config parse for a saved version.
func (e *Engine) modelOf(def Definition) (provider, model string, needsModel bool) {
	var cfg Config
	if b, err := e.build(def); err == nil {
		cfg = b.cfg
	} else if cfg, err = ParseConfig(def.Type, def.Config); err != nil {
		// A config that does not parse needs no key: it runs locally and reports its error.
		return "", "", false
	}
	switch c := cfg.(type) {
	case *AIConfig:
		return c.Provider, c.Model, true
	case *StatisticalConfig:
		// Only the embedding-based metrics call a model, whatever the config carries.
		if isEmbeddingMetric(def.BuiltinKey) && c.EmbeddingProvider != "" {
			return c.EmbeddingProvider, c.EmbeddingModel, true
		}
	}
	return "", "", false
}

// Run executes one evaluator. It never fails: a broken evaluator yields a result with
// an error score, so one bad evaluator cannot sink the entries around it.
func (e *Engine) Run(ctx context.Context, def Definition, in Input) Result {
	start := time.Now()
	result := e.run(ctx, def, in)
	result.EvaluatorID = def.ID
	result.EvaluatorVersion = def.Version
	if result.LatencyMs == 0 {
		result.LatencyMs = time.Since(start).Milliseconds()
	}
	return result
}

// RunMany executes several evaluators on the same input and returns their results in
// the order the definitions were given. Evaluator types that make no model calls each
// have a bounded pool.
func (e *Engine) RunMany(ctx context.Context, defs []Definition, in Input) []Result {
	results := make([]Result, len(defs))
	var wg sync.WaitGroup
	for i, def := range defs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pool := e.pools[def.Type]
			if pool != nil {
				select {
				case pool <- struct{}{}:
					defer func() { <-pool }()
				case <-ctx.Done():
					results[i] = errorResult(ctx.Err())
					results[i].EvaluatorID, results[i].EvaluatorVersion = def.ID, def.Version
					return
				}
			}
			results[i] = e.Run(ctx, def, in)
		}()
	}
	wg.Wait()
	return results
}

func (e *Engine) run(ctx context.Context, def Definition, in Input) (result Result) {
	b, err := e.build(def)
	if err != nil {
		return errorResult(err)
	}
	evaluator, cfg := b.evaluator, b.cfg
	if missing := missingVariables(b.required, in); len(missing) > 0 {
		return Result{Score: NAScore(), Reasoning: "Missing variables: " + strings.Join(missing, ", ")}
	}

	timeout := e.timeout
	if def.Type == EvaluatorTypeAI {
		timeout = e.aiTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			result = errorResult(fmt.Errorf("evaluator panicked: %v", recovered))
		}
	}()

	result, err = evaluator.Evaluate(runCtx, in)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return errorResult(fmt.Errorf("evaluator timed out after %s", timeout))
		}
		return errorResult(err)
	}
	switch result.Score.Status {
	case ScoreStatusOK:
	case ScoreStatusNA, ScoreStatusError:
		result.Pass = nil
		return result
	default:
		return errorResult(errors.New("evaluator returned no score"))
	}
	if err := checkScore(cfg, result.Score); err != nil {
		failed := errorResult(err)
		failed.Usage, failed.Cost, failed.Logs = result.Usage, result.Cost, result.Logs
		return failed
	}
	common := cfg.Common()
	result.Score = ApplyNormalizer(common.ScoreNormalizer, result.Score)
	if pass, ok := EvaluateEntry(common.PassCriteria.EntryLevel, result.Score); ok {
		result.Pass = &pass
	}
	return result
}

// errorResult wraps a failure as a result.
func errorResult(err error) Result {
	return Result{Score: ErrorScore(), Reasoning: err.Error()}
}

// requiredVariables lists the variables an evaluator needs: the ones its config
// declares plus the ones the evaluator itself reports. Sorted, without repeats.
func requiredVariables(cfg Config, evaluator Evaluator) []string {
	var required []string
	for name := range cfg.Common().Variables {
		required = append(required, name)
	}
	if requirer, ok := evaluator.(VariableRequirer); ok {
		required = append(required, requirer.RequiredVariables()...)
	}
	slices.Sort(required)
	return slices.Compact(required)
}

// missingVariables lists the required variables the input does not carry. A variable
// set to the empty string is present. The common case, nothing missing, allocates nothing.
func missingVariables(required []string, in Input) []string {
	var missing []string
	for _, name := range required {
		if _, present := in.Variables[name]; !present {
			missing = append(missing, name)
		}
	}
	return missing
}

// CheckScore reports whether a score has the shape an evaluator's config allows: a
// boolean for yes or no, a number within the scale, one of the choices. A reviewer's
// score for a human evaluator is checked with it.
func CheckScore(cfg Config, score Score) error {
	return checkScore(cfg, score)
}

// checkScore verifies that a score has the shape its grading style promises. Types
// without a declared style (statistical, API) are not checked.
func checkScore(cfg Config, score Score) error {
	var style GradingStyle
	var scale *Scale
	switch c := cfg.(type) {
	case *AIConfig:
		style, scale = c.GradingStyle, c.Scale
	case *ProgrammaticConfig:
		style = c.ResponseType
	case *HumanConfig:
		style, scale = c.EvaluationType, c.Scale
	default:
		return nil
	}
	switch style {
	case GradingStyleYesNo:
		if score.Bool == nil {
			return errors.New("expected a boolean score for grading style yes_no")
		}
	case GradingStyleNumber:
		if score.Number == nil {
			return errors.New("expected a numeric score for grading style number")
		}
	case GradingStyleScale:
		if score.Number == nil {
			return errors.New("expected a numeric score for grading style scale")
		}
		if scale != nil && (*score.Number < scale.Min || *score.Number > scale.Max) {
			return fmt.Errorf("score %v is outside the scale %v to %v", *score.Number, scale.Min, scale.Max)
		}
	case GradingStyleMultiselect:
		if score.String == nil {
			return errors.New("expected a label score for grading style multiselect")
		}
		options := cfg.Common().PassCriteria.EntryLevel.Options
		if !slices.Contains(options, *score.String) {
			return fmt.Errorf("score %q is not one of the options %v", *score.String, options)
		}
	}
	return nil
}

// variablePattern matches a {{name}} placeholder. It is the pattern Maxim uses.
var variablePattern = regexp.MustCompile(`\{\{([a-zA-Z0-9_ \-$.]+)\}\}`)

// ExtractVariables returns the placeholder names used in text, in order of first use.
func ExtractVariables(text string) []string {
	var names []string
	seen := make(map[string]struct{})
	for _, match := range variablePattern.FindAllStringSubmatch(text, -1) {
		name := strings.TrimSpace(match[1])
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}
