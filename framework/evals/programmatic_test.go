package evals

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newProgrammaticEngine(opts ProgrammaticOptions) *Engine {
	engine := NewEngine(EngineOptions{})
	RegisterProgrammaticEvaluator(engine, opts)
	return engine
}

func programmaticDefinition(t *testing.T, code string, responseType GradingStyle) Definition {
	t.Helper()
	config, err := json.Marshal(map[string]any{"code": code, "response_type": responseType})
	require.NoError(t, err)
	return Definition{ID: "prog-1", Type: EvaluatorTypeProgrammatic, Version: 1, Config: config}
}

func TestProgrammatic_ReturnTypes(t *testing.T) {
	cases := []struct {
		name         string
		code         string
		responseType GradingStyle
		want         Score
		wantPass     bool
	}{
		{"boolean", "def validate(output):\n    return len(output) > 3\n", GradingStyleYesNo, BoolScore(true), true},
		{"integer", "def validate(output):\n    return len(output)\n", GradingStyleNumber, NumberScore(5), true},
		{"float", "def validate(output):\n    return len(output) / 10\n", GradingStyleNumber, NumberScore(0.5), false},
		{"string", "def validate(output):\n    return output.upper()\n", GradingStyleNumber, Score{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := newProgrammaticEngine(ProgrammaticOptions{}).Run(context.Background(),
				programmaticDefinition(t, tc.code, tc.responseType), Input{Variables: map[string]string{"output": "hello"}})
			if tc.want.Status == "" {
				assert.Equal(t, ScoreStatusError, result.Score.Status, "a label where a number was promised is an evaluator fault")
				return
			}
			require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
			assert.Equal(t, tc.want, result.Score)
			require.NotNil(t, result.Pass)
			assert.Equal(t, tc.wantPass, *result.Pass)
		})
	}
}

// A dict return carries reasoning alongside the score, which Maxim's sandboxes cannot.
func TestProgrammatic_DictReturnCarriesReasoning(t *testing.T) {
	code := "def validate(output, expected_output):\n" +
		"    same = output.strip() == expected_output.strip()\n" +
		"    return {\"score\": same, \"reasoning\": \"exact match\" if same else \"differs\"}\n"
	result := newProgrammaticEngine(ProgrammaticOptions{}).Run(context.Background(),
		programmaticDefinition(t, code, GradingStyleYesNo),
		Input{Variables: map[string]string{"output": " 42 ", "expected_output": "42"}})

	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.Equal(t, BoolScore(true), result.Score)
	assert.Equal(t, "exact match", result.Reasoning)
}

// The parameters of validate are the required variables. Parameters with a default are
// optional and take the default when the input has no value.
func TestProgrammatic_ParametersAreVariables(t *testing.T) {
	code := "def validate(output, context = \"none\"):\n    return output + \"|\" + context\n"
	engine := newProgrammaticEngine(ProgrammaticOptions{})

	missing := engine.Run(context.Background(), programmaticDefinition(t, code, GradingStyleNumber), Input{})
	assert.Equal(t, ScoreStatusNA, missing.Score.Status)
	assert.Equal(t, "Missing variables: output", missing.Reasoning)

	params, err := ProgrammaticVariables(code)
	require.NoError(t, err)
	assert.Equal(t, []string{"output"}, params.Required)
	assert.Equal(t, []string{"context"}, params.Optional)
}

func TestProgrammatic_ArgumentsAreStrings(t *testing.T) {
	code := "def validate(output):\n    return type(output) == \"string\" and json.decode(output)[\"n\"] == 3\n"
	result := newProgrammaticEngine(ProgrammaticOptions{}).Run(context.Background(),
		programmaticDefinition(t, code, GradingStyleYesNo), Input{Variables: map[string]string{"output": `{"n": 3}`}})
	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.Equal(t, BoolScore(true), result.Score, "values arrive as text and json is there to parse them")
}

func TestProgrammatic_Modules(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Score
	}{
		{"math", "    return math.floor(2.7)\n", NumberScore(2)},
		{"re.search finds the first match", "    return re.search(\"[0-9]+\", output) == \"42\"\n", BoolScore(true)},
		{"re.search returns None on no match", "    return re.search(\"[A-Z]{9}\", output) == None\n", BoolScore(true)},
		{"re.match tests the whole pattern anywhere", "    return re.match(\"^order [0-9]+ shipped$\", output)\n", BoolScore(true)},
		{"re.findall returns every match", "    return len(re.findall(\"[a-z]+\", output))\n", NumberScore(2)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			style := GradingStyleNumber
			if tc.want.Bool != nil {
				style = GradingStyleYesNo
			}
			result := newProgrammaticEngine(ProgrammaticOptions{}).Run(context.Background(),
				programmaticDefinition(t, "def validate(output):\n"+tc.body, style),
				Input{Variables: map[string]string{"output": "order 42 shipped"}})
			require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
			assert.Equal(t, tc.want, result.Score)
		})
	}
}

func TestProgrammatic_PrintGoesToLogs(t *testing.T) {
	code := "def validate(output):\n    print(\"checking\", output)\n    print(\"done\")\n    return True\n"
	result := newProgrammaticEngine(ProgrammaticOptions{}).Run(context.Background(),
		programmaticDefinition(t, code, GradingStyleYesNo), Input{Variables: map[string]string{"output": "x"}})
	require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
	assert.Equal(t, []string{"checking x", "done"}, result.Logs)
}

func TestProgrammatic_SandboxLimits(t *testing.T) {
	cases := []struct {
		name    string
		opts    ProgrammaticOptions
		code    string
		wantErr string
	}{
		{"runaway loop hits the step limit", ProgrammaticOptions{MaxSteps: 10_000},
			"def validate(output):\n    n = 0\n    while True:\n        n += 1\n    return n\n", "limit"},
		{"runaway allocation hits the memory limit", ProgrammaticOptions{MaxMemoryBytes: 1 << 20},
			"def validate(output):\n    data = \"x\"\n    for _ in range(40):\n        data = data + data\n    return len(data)\n", "limit"},
		{"load is not available", ProgrammaticOptions{},
			"load(\"os.star\", \"os\")\ndef validate(output):\n    return True\n", "load"},
		{"no file access", ProgrammaticOptions{},
			"def validate(output):\n    return open(\"/etc/passwd\")\n", "open"},
		{"oversized source", ProgrammaticOptions{MaxSourceBytes: 64},
			"def validate(output):\n    return True\n" + strings.Repeat("# padding\n", 20), "source"},
		{"runtime error names the line", ProgrammaticOptions{},
			"def validate(output):\n    return 1 // 0\n", "division"},
		{"None is not a score", ProgrammaticOptions{},
			"def validate(output):\n    pass\n", "None"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := newProgrammaticEngine(tc.opts).Run(context.Background(),
				programmaticDefinition(t, tc.code, GradingStyleNumber), Input{Variables: map[string]string{"output": "x"}})
			assert.Equal(t, ScoreStatusError, result.Score.Status)
			assert.Contains(t, strings.ToLower(result.Reasoning), strings.ToLower(tc.wantErr))
		})
	}
}

func TestProgrammatic_Timeout(t *testing.T) {
	code := "def validate(output):\n    n = 0\n    while True:\n        n += 1\n    return n\n"
	start := time.Now()
	result := newProgrammaticEngine(ProgrammaticOptions{Timeout: 50 * time.Millisecond, MaxSteps: 1 << 40}).Run(context.Background(),
		programmaticDefinition(t, code, GradingStyleNumber), Input{Variables: map[string]string{"output": "x"}})

	assert.Equal(t, ScoreStatusError, result.Score.Status)
	assert.Contains(t, result.Reasoning, "timed out")
	assert.Less(t, time.Since(start), 5*time.Second, "the script is stopped, not waited out")
}

func TestProgrammaticVariables_RejectsBadCode(t *testing.T) {
	cases := []struct {
		name    string
		code    string
		wantErr string
	}{
		{"syntax error", "def validate(output)\n    return True\n", "syntax"},
		{"no validate function", "def check(output):\n    return True\n", "validate"},
		{"validate is not a function", "validate = 3\n", "validate"},
		{"variadic parameters", "def validate(*args):\n    return True\n", "named parameters"},
		{"keyword catch-all", "def validate(output, **kwargs):\n    return True\n", "named parameters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ProgrammaticVariables(tc.code)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// Running one script many times at once must not share state between runs.
func TestProgrammatic_ConcurrentRunsAreIsolated(t *testing.T) {
	code := "seen = []\ndef validate(output):\n    seen.append(output)\n    return len(seen)\n"
	engine := newProgrammaticEngine(ProgrammaticOptions{})
	defs := make([]Definition, 20)
	for i := range defs {
		defs[i] = programmaticDefinition(t, code, GradingStyleNumber)
	}
	for _, result := range engine.RunMany(context.Background(), defs, Input{Variables: map[string]string{"output": "x"}}) {
		require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
		assert.Equal(t, NumberScore(1), result.Score, "each run starts from a fresh module")
	}
}
