package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestResolveAnthropicProgrammaticCaller pins the version rule. allowed_callers must
// name the code execution version the request declares: Anthropic auto-injects a
// code_execution tool for the caller, which 400s ("Auto-injecting tools would
// conflict with existing tool names: ['code_execution']") when the versions differ.
func TestResolveAnthropicProgrammaticCaller(t *testing.T) {
	tests := []struct {
		name            string
		declaredVersion string
		hasCodeExec     bool
		wantCaller      string
		wantEmitVersion string
	}{
		{
			name:        "no code execution tool: Anthropic auto-injects the named version",
			hasCodeExec: false,
			wantCaller:  "code_execution_20260120",
		},
		{
			name:            "version-less code_interpreter is raised to reach programmatic tool calling",
			declaredVersion: "",
			hasCodeExec:     true,
			wantCaller:      "code_execution_20260120",
			wantEmitVersion: "code_execution_20260120",
		},
		{
			name:            "explicit 20250825 is matched verbatim",
			declaredVersion: "code_execution_20250825",
			hasCodeExec:     true,
			wantCaller:      "code_execution_20250825",
		},
		{
			name:            "explicit 20260120 is matched verbatim",
			declaredVersion: "code_execution_20260120",
			hasCodeExec:     true,
			wantCaller:      "code_execution_20260120",
		},
		{
			name:            "explicit 20260521 is matched verbatim",
			declaredVersion: "code_execution_20260521",
			hasCodeExec:     true,
			wantCaller:      "code_execution_20260521",
		},
		{
			name:            "unrecognized version is named as declared and left alone",
			declaredVersion: "code_execution_20270101",
			hasCodeExec:     true,
			wantCaller:      "code_execution_20270101",
		},
		{
			name:            "legacy 20250522 has no caller value",
			declaredVersion: "code_execution_20250522",
			hasCodeExec:     true,
			wantCaller:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caller, emitVersion := resolveAnthropicProgrammaticCaller(tt.declaredVersion, tt.hasCodeExec)
			if caller != tt.wantCaller {
				t.Errorf("caller = %q, want %q", caller, tt.wantCaller)
			}
			if emitVersion != tt.wantEmitVersion {
				t.Errorf("emitVersion = %q, want %q", emitVersion, tt.wantEmitVersion)
			}
		})
	}
}

// TestAnthropicAllowedCallers covers the value rewrite itself.
func TestAnthropicAllowedCallers(t *testing.T) {
	tests := []struct {
		name               string
		in                 []string
		programmaticCaller string
		want               []string
	}{
		{name: "direct is shared", in: []string{"direct"}, programmaticCaller: "code_execution_20260120", want: []string{"direct"}},
		{name: "programmatic is renamed", in: []string{"programmatic"}, programmaticCaller: "code_execution_20260120", want: []string{"code_execution_20260120"}},
		{name: "renamed to the declared version", in: []string{"programmatic"}, programmaticCaller: "code_execution_20250825", want: []string{"code_execution_20250825"}},
		{name: "mixed list", in: []string{"direct", "programmatic"}, programmaticCaller: "code_execution_20260521", want: []string{"direct", "code_execution_20260521"}},
		{name: "anthropic values pass through", in: []string{"code_execution_20260120"}, programmaticCaller: "code_execution_20260120", want: []string{"code_execution_20260120"}},
		{name: "collapses duplicates after rename", in: []string{"programmatic", "code_execution_20260120"}, programmaticCaller: "code_execution_20260120", want: []string{"code_execution_20260120"}},
		{name: "unexpressible caller is dropped", in: []string{"programmatic"}, programmaticCaller: "", want: nil},
		{name: "unexpressible caller leaves direct alone", in: []string{"direct", "programmatic"}, programmaticCaller: "", want: []string{"direct"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := anthropicAllowedCallers(tt.in, tt.programmaticCaller)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestConvertBifrostToolsToAnthropicTranslatesProgrammaticCaller runs the rewrite
// through the request converter, including the code_interpreter version it has to
// raise so programmatic tool calling exists at all.
func TestConvertBifrostToolsToAnthropicTranslatesProgrammaticCaller(t *testing.T) {
	caps := schemas.ModelCaps{}
	queryTool := schemas.ResponsesTool{
		Type:                  schemas.ResponsesToolTypeFunction,
		Name:                  schemas.Ptr("query_database"),
		AllowedCallers:        []string{"programmatic"},
		ResponsesToolFunction: &schemas.ResponsesToolFunction{},
	}

	t.Run("version-less code_interpreter is raised to 20260120", func(t *testing.T) {
		tools, _, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeCodeInterpreter, ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{}},
			queryTool,
		}, schemas.Anthropic)
		if err != nil {
			t.Fatalf("convert failed: %v", err)
		}
		assertToolCallers(t, tools, "query_database", []string{"code_execution_20260120"})
		assertCodeExecutionVersion(t, tools, "code_execution_20260120")
	})

	t.Run("explicit version is matched, not raised", func(t *testing.T) {
		tools, _, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{
			{
				Type:                         schemas.ResponsesToolTypeCodeInterpreter,
				ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{Version: schemas.Ptr("code_execution_20250825")},
			},
			queryTool,
		}, schemas.Anthropic)
		if err != nil {
			t.Fatalf("convert failed: %v", err)
		}
		assertToolCallers(t, tools, "query_database", []string{"code_execution_20250825"})
		assertCodeExecutionVersion(t, tools, "code_execution_20250825")
	})

	t.Run("no code execution tool falls back to the auto-injected version", func(t *testing.T) {
		tools, _, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{queryTool}, schemas.Anthropic)
		if err != nil {
			t.Fatalf("convert failed: %v", err)
		}
		assertToolCallers(t, tools, "query_database", []string{"code_execution_20260120"})
	})

	t.Run("legacy 20250522 drops the caller instead of 400ing", func(t *testing.T) {
		tools, _, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{
			{
				Type:                         schemas.ResponsesToolTypeCodeInterpreter,
				ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{Version: schemas.Ptr("code_execution_20250522")},
			},
			queryTool,
		}, schemas.Anthropic)
		if err != nil {
			t.Fatalf("convert failed: %v", err)
		}
		assertToolCallers(t, tools, "query_database", nil)
		assertCodeExecutionVersion(t, tools, "code_execution_20250522")
	})

	t.Run("a request without programmatic callers is untouched", func(t *testing.T) {
		tools, _, err := convertBifrostToolsToAnthropic(caps, []schemas.ResponsesTool{
			{Type: schemas.ResponsesToolTypeCodeInterpreter, ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{}},
			{
				Type:                  schemas.ResponsesToolTypeFunction,
				Name:                  schemas.Ptr("query_database"),
				AllowedCallers:        []string{"direct"},
				ResponsesToolFunction: &schemas.ResponsesToolFunction{},
			},
		}, schemas.Anthropic)
		if err != nil {
			t.Fatalf("convert failed: %v", err)
		}
		assertToolCallers(t, tools, "query_database", []string{"direct"})
		// The default version stands when nothing asks for programmatic tool calling.
		assertCodeExecutionVersion(t, tools, "code_execution_20250825")
	})
}

func assertToolCallers(t *testing.T, tools []AnthropicTool, name string, want []string) {
	t.Helper()
	for _, tool := range tools {
		if tool.Name != name {
			continue
		}
		if len(tool.AllowedCallers) != len(want) {
			t.Fatalf("%s allowed_callers = %v, want %v", name, tool.AllowedCallers, want)
		}
		for i := range want {
			if tool.AllowedCallers[i] != want[i] {
				t.Fatalf("%s allowed_callers = %v, want %v", name, tool.AllowedCallers, want)
			}
		}
		return
	}
	t.Fatalf("tool %s missing from %+v", name, tools)
}

func assertCodeExecutionVersion(t *testing.T, tools []AnthropicTool, want string) {
	t.Helper()
	for _, tool := range tools {
		if tool.Name != string(AnthropicToolNameCodeExecution) {
			continue
		}
		if tool.Type == nil || string(*tool.Type) != want {
			t.Fatalf("code_execution type = %v, want %s", tool.Type, want)
		}
		return
	}
	t.Fatalf("code_execution tool missing from %+v", tools)
}
