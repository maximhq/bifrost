package evals

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	starlarkjson "github.com/canonical/starlark/lib/json"
	starlarkmath "github.com/canonical/starlark/lib/math"
	"github.com/canonical/starlark/starlark"
	"github.com/canonical/starlark/starlarkstruct"
	"github.com/canonical/starlark/syntax"
)

// programmaticFunction is the function a programmatic evaluator must define.
const programmaticFunction = "validate"

// programmaticFile names the user's source in error messages.
const programmaticFile = "evaluator.star"

// Default sandbox limits for one evaluation.
const (
	defaultProgrammaticMaxSteps    = 10_000_000
	defaultProgrammaticMaxMemory   = 64 << 20
	defaultProgrammaticMaxSource   = 64 << 10
	defaultProgrammaticMaxLogBytes = 64 << 10
	defaultProgrammaticTimeout     = 5 * time.Second
)

// sandboxRegexpCache is the thread-local key of the patterns one evaluation has
// compiled, and maxSandboxRegexps is how many it keeps.
const (
	sandboxRegexpCache = "evals.regexps"
	maxSandboxRegexps  = 32
)

// sandboxSafety is what every builtin must guarantee to run in the sandbox.
const sandboxSafety = starlark.CPUSafe | starlark.MemSafe | starlark.TimeSafe

// programmaticFileOptions are the dialect options, matching MCP code mode so scripts
// behave the same in both places.
var programmaticFileOptions = &syntax.FileOptions{TopLevelControl: true, While: true, Set: true, GlobalReassign: true, Recursion: true}

// ProgrammaticOptions bounds what one programmatic evaluation may consume. Zero values
// take the defaults.
type ProgrammaticOptions struct {
	MaxSteps       int64         // interpreter steps
	MaxMemoryBytes int64         // allocation budget
	MaxSourceBytes int           // size of the evaluator's code
	MaxLogBytes    int           // total size of print output kept
	Timeout        time.Duration // wall clock
}

func (o ProgrammaticOptions) withDefaults() ProgrammaticOptions {
	if o.MaxSteps <= 0 {
		o.MaxSteps = defaultProgrammaticMaxSteps
	}
	if o.MaxMemoryBytes <= 0 {
		o.MaxMemoryBytes = defaultProgrammaticMaxMemory
	}
	if o.MaxSourceBytes <= 0 {
		o.MaxSourceBytes = defaultProgrammaticMaxSource
	}
	if o.MaxLogBytes <= 0 {
		o.MaxLogBytes = defaultProgrammaticMaxLogBytes
	}
	if o.Timeout <= 0 {
		o.Timeout = defaultProgrammaticTimeout
	}
	return o
}

// RegisterProgrammaticEvaluator installs the Starlark evaluator on an engine.
func RegisterProgrammaticEvaluator(engine *Engine, opts ProgrammaticOptions) {
	opts = opts.withDefaults()
	engine.Register(EvaluatorTypeProgrammatic, "", func(_ Definition, cfg Config) (Evaluator, error) {
		programmatic, ok := cfg.(*ProgrammaticConfig)
		if !ok {
			return nil, fmt.Errorf("a programmatic evaluator needs a programmatic config, got %T", cfg)
		}
		if len(programmatic.Code) > opts.MaxSourceBytes {
			return nil, fmt.Errorf("evaluator source exceeds the %d byte limit", opts.MaxSourceBytes)
		}
		params, err := ProgrammaticVariables(programmatic.Code)
		if err != nil {
			return nil, err
		}
		// Compiled once per evaluator version; each run starts its own module from it.
		_, program, err := starlark.SourceProgramOptions(programmaticFileOptions, programmaticFile, programmatic.Code, sandboxModules.Has)
		if err != nil {
			return nil, errors.New(describeStarlarkError(err))
		}
		return &programmaticEvaluator{program: program, params: params, opts: opts}, nil
	})
}

// ProgrammaticParams are the variables a programmatic evaluator reads, taken from the
// parameters of its validate function.
type ProgrammaticParams struct {
	Required []string `json:"required"` // parameters without a default
	Optional []string `json:"optional"` // parameters with a default
}

// ProgrammaticVariables parses evaluator code without running it and returns the
// parameters of validate. It is also the save-time syntax check.
func ProgrammaticVariables(code string) (ProgrammaticParams, error) {
	var params ProgrammaticParams
	file, err := programmaticFileOptions.Parse(programmaticFile, code, 0)
	if err != nil {
		return params, fmt.Errorf("syntax error: %w", err)
	}
	var def *syntax.DefStmt
	for _, stmt := range file.Stmts {
		if candidate, ok := stmt.(*syntax.DefStmt); ok && candidate.Name.Name == programmaticFunction {
			def = candidate
		}
	}
	if def == nil {
		return params, fmt.Errorf("the code must define a function named %s", programmaticFunction)
	}
	for _, param := range def.Params {
		switch p := param.(type) {
		case *syntax.Ident:
			params.Required = append(params.Required, p.Name)
		case *syntax.BinaryExpr:
			name, ok := p.X.(*syntax.Ident)
			if !ok {
				return params, fmt.Errorf("%s must take only named parameters", programmaticFunction)
			}
			params.Optional = append(params.Optional, name.Name)
		default:
			// *args and **kwargs cannot be mapped to variables by name.
			return params, fmt.Errorf("%s must take only named parameters", programmaticFunction)
		}
	}
	return params, nil
}

// programmaticEvaluator runs a user's validate function in a Starlark sandbox. The code
// has no file, network or clock access, and each run starts from a fresh module.
type programmaticEvaluator struct {
	program *starlark.Program // immutable compiled code, shared by every run
	params  ProgrammaticParams
	opts    ProgrammaticOptions
}

// RequiredVariables lists the validate parameters that have no default.
func (e *programmaticEvaluator) RequiredVariables() []string { return e.params.Required }

// Evaluate runs validate with the input variables as string arguments.
func (e *programmaticEvaluator) Evaluate(ctx context.Context, in Input) (Result, error) {
	runCtx, cancel := context.WithTimeout(ctx, e.opts.Timeout)
	defer cancel()

	thread := &starlark.Thread{Name: "evaluator"}
	thread.SetParentContext(runCtx)
	thread.SetMaxSteps(e.opts.MaxSteps)
	thread.SetMaxAllocs(e.opts.MaxMemoryBytes)
	thread.RequireSafety(sandboxSafety)
	defer thread.Cancel("evaluation finished")

	var logs []string
	logBytes := 0
	thread.Print = func(_ *starlark.Thread, line string) {
		if logBytes+len(line) > e.opts.MaxLogBytes {
			return
		}
		logBytes += len(line) + 1
		logs = append(logs, line)
	}

	// The interpreter only notices cancellation between steps it accounts for, so the
	// deadline also cancels the thread directly. Nothing runs for this unless the
	// deadline passes.
	stopWatching := context.AfterFunc(runCtx, func() { thread.Cancel("evaluator timed out") })
	defer stopWatching()

	value, err := e.call(thread, in)
	if err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return Result{Logs: logs}, fmt.Errorf("evaluator timed out after %s", e.opts.Timeout)
		}
		if errors.Is(err, starlark.ErrSafety) {
			return Result{Logs: logs}, fmt.Errorf("sandbox resource limit exceeded: %w", err)
		}
		return Result{Logs: logs}, errors.New(describeStarlarkError(err))
	}
	result, err := resultFromStarlark(value)
	if err != nil {
		return Result{Logs: logs}, err
	}
	result.Logs = logs
	return result, nil
}

// call loads the module and invokes validate. Arguments are passed by name so a
// parameter with a default keeps it when the input has no value.
func (e *programmaticEvaluator) call(thread *starlark.Thread, in Input) (starlark.Value, error) {
	globals, err := e.program.Init(thread, sandboxModules)
	if err != nil {
		return nil, err
	}
	fn, ok := globals[programmaticFunction].(starlark.Callable)
	if !ok {
		return nil, fmt.Errorf("the code must define a function named %s", programmaticFunction)
	}
	kwargs := make([]starlark.Tuple, 0, len(e.params.Required)+len(e.params.Optional))
	for _, names := range [][]string{e.params.Required, e.params.Optional} {
		for _, name := range names {
			value, present := in.Variables[name]
			if !present {
				continue
			}
			if err := thread.AddAllocs(starlark.SafeInt(len(value) + 32)); err != nil {
				return nil, err
			}
			kwargs = append(kwargs, starlark.Tuple{starlark.String(name), starlark.String(value)})
		}
	}
	return starlark.Call(thread, fn, nil, kwargs)
}

// resultFromStarlark converts validate's return value. A bare scalar is the score; a
// dict may carry "score" and "reasoning".
func resultFromStarlark(value starlark.Value) (Result, error) {
	if dict, ok := value.(*starlark.Dict); ok {
		rawScore, found, err := dict.Get(starlark.String("score"))
		if err != nil {
			return Result{}, err
		}
		if !found {
			return Result{}, fmt.Errorf(`%s returned a dict without a "score" key`, programmaticFunction)
		}
		score, err := scoreFromStarlark(rawScore)
		if err != nil {
			return Result{}, err
		}
		result := Result{Score: score}
		if reasoning, found, _ := dict.Get(starlark.String("reasoning")); found {
			if text, ok := starlark.AsString(reasoning); ok {
				result.Reasoning = text
			} else if reasoning != starlark.None {
				result.Reasoning = reasoning.String()
			}
		}
		return result, nil
	}
	score, err := scoreFromStarlark(value)
	if err != nil {
		return Result{}, err
	}
	return Result{Score: score}, nil
}

func scoreFromStarlark(value starlark.Value) (Score, error) {
	switch v := value.(type) {
	case starlark.Bool:
		return BoolScore(bool(v)), nil
	case starlark.String:
		return StringScore(string(v)), nil
	case starlark.Int, starlark.Float:
		if f, ok := starlark.AsFloat(v); ok {
			return NumberScore(f), nil
		}
	}
	return Score{}, fmt.Errorf("%s returned %s (%s); a score must be a boolean, a number or a string", programmaticFunction, value.String(), value.Type())
}

// describeStarlarkError renders a script failure with the line it happened on.
func describeStarlarkError(err error) string {
	var evalErr *starlark.EvalError
	if errors.As(err, &evalErr) {
		for i := len(evalErr.CallStack) - 1; i >= 0; i-- {
			frame := evalErr.CallStack[i]
			if frame.Pos.Filename() == programmaticFile {
				return fmt.Sprintf("%s (line %d, in %s)", evalErr.Error(), frame.Pos.Line, frame.Name)
			}
		}
	}
	return err.Error()
}

// sandboxModules are predeclared in every evaluation: json to parse values that arrive
// as text, math for arithmetic, and re because most code evaluators are pattern checks.
var sandboxModules = starlark.StringDict{
	"json": starlarkjson.Module,
	"math": starlarkmath.Module,
	"re": &starlarkstruct.Module{Name: "re", Members: starlark.StringDict{
		"match":   starlark.NewBuiltinWithSafety("re.match", sandboxSafety, reMatch),
		"search":  starlark.NewBuiltinWithSafety("re.search", sandboxSafety, reSearch),
		"findall": starlark.NewBuiltinWithSafety("re.findall", sandboxSafety, reFindAll),
	}},
}

// compileSandboxRegexp reads the (pattern, text) arguments and compiles the pattern. Go's
// regexp runs in time linear in the input, so the cost is charged as steps up front.
func compileSandboxRegexp(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (*regexp.Regexp, string, error) {
	var pattern, text string
	if err := starlark.UnpackPositionalArgs(b.Name(), args, kwargs, 2, &pattern, &text); err != nil {
		return nil, "", err
	}
	if err := thread.AddSteps(starlark.SafeInt(len(pattern) + len(text))); err != nil {
		return nil, "", err
	}
	// Code that checks many values in a loop uses the same few patterns, so one
	// evaluation compiles each pattern once.
	cache, _ := thread.Local(sandboxRegexpCache).(map[string]*regexp.Regexp)
	if compiled, ok := cache[pattern]; ok {
		return compiled, text, nil
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, "", fmt.Errorf("%s: invalid pattern: %s", b.Name(), strings.TrimPrefix(err.Error(), "error parsing regexp: "))
	}
	if cache == nil {
		cache = make(map[string]*regexp.Regexp, 4)
		thread.SetLocal(sandboxRegexpCache, cache)
	}
	if len(cache) < maxSandboxRegexps {
		cache[pattern] = compiled
	}
	return compiled, text, nil
}

// reMatch reports whether the pattern matches anywhere in the text.
func reMatch(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	compiled, text, err := compileSandboxRegexp(thread, b, args, kwargs)
	if err != nil {
		return nil, err
	}
	return starlark.Bool(compiled.MatchString(text)), nil
}

// reSearch returns the first match, or None.
func reSearch(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	compiled, text, err := compileSandboxRegexp(thread, b, args, kwargs)
	if err != nil {
		return nil, err
	}
	loc := compiled.FindStringIndex(text)
	if loc == nil {
		return starlark.None, nil
	}
	if err := thread.AddAllocs(starlark.SafeInt(loc[1] - loc[0] + 16)); err != nil {
		return nil, err
	}
	return starlark.String(text[loc[0]:loc[1]]), nil
}

// reFindAll returns every non-overlapping match as a list.
func reFindAll(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	compiled, text, err := compileSandboxRegexp(thread, b, args, kwargs)
	if err != nil {
		return nil, err
	}
	matches := compiled.FindAllString(text, -1)
	values := make([]starlark.Value, len(matches))
	for i, match := range matches {
		if err := thread.AddAllocs(starlark.SafeInt(len(match) + 32)); err != nil {
			return nil, err
		}
		values[i] = starlark.String(match)
	}
	return starlark.NewList(values), nil
}
