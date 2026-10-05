//go:build !tinygo && !wasm

package starlark

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/canonical/starlark/starlark"
	"github.com/canonical/starlark/starlarkstruct"
	"github.com/canonical/starlark/syntax"
	"github.com/maximhq/bifrost/core/schemas"
)

// sandboxLimitsKey holds an execution's resolved limits as a thread local, so
// conversions and tool-result checks deep in a call use the same budget.
const sandboxLimitsKey = "bifrost.codemode.limits"

// sandboxLimits returns the limits of the execution running on thread, or the
// defaults for a thread that was not started by runSandbox.
func sandboxLimits(thread *starlark.Thread) schemas.MCPCodeModeLimits {
	if thread != nil {
		if limits, ok := thread.Local(sandboxLimitsKey).(schemas.MCPCodeModeLimits); ok {
			return limits
		}
	}
	return schemas.MCPCodeModeLimits{}.WithDefaults()
}

// contextSandboxLimits returns the limits of the execution that owns ctx.
func contextSandboxLimits(ctx context.Context) schemas.MCPCodeModeLimits {
	return sandboxLimits(starlark.ContextThread(ctx))
}

func sandboxFailure(message string) ExecutionResult {
	return ExecutionResult{Logs: []string{}, Errors: &ExecutionError{Kind: ExecutionErrorTypeRuntime, Message: message}}
}

type sandboxToolCaller func(context.Context, string, string, map[string]interface{}, func(string)) (interface{}, error)

func newSandboxThread(ctx context.Context, limits schemas.MCPCodeModeLimits) *starlark.Thread {
	thread := &starlark.Thread{Name: "codemode"}
	thread.SetParentContext(ctx)
	thread.SetLocal(sandboxLimitsKey, limits)
	thread.SetMaxSteps(int64(limits.MaxSteps))
	thread.SetMaxAllocs(int64(limits.MaxMemoryBytes))
	thread.RequireSafety(starlark.CPUSafe | starlark.MemSafe | starlark.TimeSafe)
	return thread
}

// runSandbox evaluates in-process under limits, which must already have defaults applied. Interpreter operations and Go conversions at
// tool boundaries share a budget. This is cooperative accounting, not an
// OS-enforced process memory limit.
func runSandbox(ctx context.Context, limits schemas.MCPCodeModeLimits, code string, bindings map[string][]string, call sandboxToolCaller) ExecutionResult {
	if len(code) > limits.MaxSourceBytes {
		return sandboxFailure("code exceeds source limit")
	}
	if err := ctx.Err(); err != nil {
		return sandboxFailure("code mode execution cancelled: " + err.Error())
	}
	thread := newSandboxThread(ctx, limits)
	defer thread.Cancel("execution completed")
	logs := []string{}
	logBytes := 0
	appendLog := func(line string) {
		// Charge newlines too: empty print calls must consume the budget.
		if len(line) >= limits.MaxLogBytes-logBytes {
			thread.Cancel("code mode log limit exceeded")
			return
		}
		if err := thread.AddAllocs(starlark.SafeInt(len(line) + 32)); err != nil {
			return
		}
		logBytes += len(line) + 1
		logs = append(logs, line)
	}
	thread.Print = func(_ *starlark.Thread, line string) { appendLog(line) }
	// Tool diagnostics are best effort: a line that no longer fits is dropped,
	// once noted, instead of cancelling an execution whose tools already ran.
	toolLogOmitted := false
	appendToolLog := func(line string) {
		if len(line) < limits.MaxLogBytes-logBytes {
			appendLog(line)
			return
		}
		const marker = "[TOOL] further tool logs omitted"
		if !toolLogOmitted && len(marker) < limits.MaxLogBytes-logBytes {
			toolLogOmitted = true
			appendLog(marker)
		}
	}
	predeclared := starlark.StringDict{}
	servers := make([]string, 0, len(bindings))
	calls := 0
	for client, tools := range bindings {
		members := starlark.StringDict{}
		for _, tool := range tools {
			if err := thread.AddAllocs(starlark.SafeInt(512 + len(client) + 3*len(tool))); err != nil {
				return sandboxFailure(err.Error())
			}
			name := getCanonicalToolName(client, tool)
			fn := starlark.NewBuiltinWithSafety(name, starlark.CPUSafe|starlark.MemSafe|starlark.TimeSafe,
				func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
					calls++
					if calls > limits.MaxToolCalls {
						return nil, fmt.Errorf("code mode tool call limit exceeded")
					}
					converter := newValueConversion(thread)
					values := map[string]interface{}{}
					for _, kw := range kwargs {
						key := string(kw[0].(starlark.String))
						if err := converter.stringSize(key); err != nil {
							return nil, err
						}
						value, err := converter.toGo(kw[1], 0)
						if err != nil {
							return nil, err
						}
						values[key] = value
					}
					if len(args) == 1 && len(kwargs) == 0 {
						if dict, ok := args[0].(*starlark.Dict); ok {
							value, err := converter.toGo(dict, 0)
							if err != nil {
								return nil, err
							}
							values = value.(map[string]interface{})
						}
					}
					if err := thread.AddSteps(starlark.SafeInt(1)); err != nil {
						return nil, err
					}
					result, err := call(thread.Context(), client, tool, values, appendToolLog)
					if err != nil {
						return nil, fmt.Errorf("tool call failed: %w", err)
					}
					return newValueConversion(thread).fromGo(result, 0)
				})
			members[name] = fn
			alias := getCompatibilityToolAlias(client, tool)
			if alias != name && isValidStarlarkIdentifier(alias) {
				if _, exists := members[alias]; !exists {
					members[alias] = fn
				}
			}
		}
		predeclared[client] = starlarkstruct.FromStringDict(starlark.String(client), members)
		servers = append(servers, client)
	}
	sort.Strings(servers)
	opts := &syntax.FileOptions{TopLevelControl: true, While: true, Set: true, GlobalReassign: true, Recursion: true}
	// Globals never leave this invocation. Avoid ExecFile's unconditional,
	// unmetered Freeze traversal of a potentially deeply nested object graph.
	_, program, err := starlark.SourceProgramOptions(opts, "code.star", code, predeclared.Has)
	var value interface{}
	if err == nil {
		var globals starlark.StringDict
		globals, err = program.Init(thread, predeclared)
		if err == nil {
			if result, ok := globals["result"]; ok && result != starlark.None {
				value, err = newValueConversion(thread).toGo(result, 0)
			}
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = thread.AddSteps(starlark.SafeInt(0))
	}
	result := ExecutionResult{Result: value, Logs: logs, Environment: ExecutionEnvironment{ServerKeys: servers}}
	if err != nil {
		if errors.Is(err, starlark.ErrSafety) {
			err = fmt.Errorf("code mode resource limit: %w", err)
		}
		kind := ExecutionErrorTypeRuntime
		if strings.Contains(err.Error(), "syntax error") {
			kind = ExecutionErrorTypeSyntax
		}
		result.Result = nil
		hints := generatePythonErrorHints(err.Error(), servers)
		if unsupportedStatement(code, err) != "" {
			hints = append(exceptionHandlingHints(), hints...)
		}
		result.Errors = &ExecutionError{Kind: kind, Message: err.Error(), Hints: hints}
	}
	return result
}
