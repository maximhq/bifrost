package evals

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToolCallAccuracy_MaximCases runs the cases ported from Maxim's
// toolCallAccuracy.test.ts and expects the score Maxim asserts for each.
func TestToolCallAccuracy_MaximCases(t *testing.T) {
	data, err := os.ReadFile("testdata/toolcallaccuracy.json")
	require.NoError(t, err)
	var fixture struct {
		ToolSets []json.RawMessage `json:"tool_sets"`
		Cases    []struct {
			Name     string          `json:"name"`
			ToolSet  int             `json:"tool_set"`
			Actual   json.RawMessage `json:"actual"`
			Expected json.RawMessage `json:"expected"`
			Score    float64         `json:"score"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.Len(t, fixture.Cases, 45)

	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			result, err := toolCallAccuracyEvaluator{}.Evaluate(context.Background(), Input{Variables: map[string]string{
				VariableExpectedToolCalls: string(c.Expected),
				VariableToolCalls:         string(c.Actual),
				VariableTools:             string(fixture.ToolSets[c.ToolSet]),
			}})
			require.NoError(t, err)
			require.NotNil(t, result.Score.Number)
			assert.Equal(t, c.Score, *result.Score.Number, "expected %s\nactual %s\nreasoning:\n%s", c.Expected, c.Actual, result.Reasoning)
		})
	}
}

func runToolCallAccuracy(t *testing.T, expected, actual, tools string) (float64, string) {
	t.Helper()
	variables := map[string]string{VariableExpectedToolCalls: expected, VariableToolCalls: actual}
	if tools != "" {
		variables[VariableTools] = tools
	}
	result, err := toolCallAccuracyEvaluator{}.Evaluate(context.Background(), Input{Variables: variables})
	require.NoError(t, err)
	require.NotNil(t, result.Score.Number)
	return *result.Score.Number, result.Reasoning
}

func TestToolCallAccuracy_EmptyLists(t *testing.T) {
	score, reasoning := runToolCallAccuracy(t, "[]", " [] ", "")
	assert.Equal(t, 1.0, score, "nothing expected and nothing called")
	assert.Contains(t, reasoning, "without making any tool calls")

	score, _ = runToolCallAccuracy(t, `[{"name":"a","arguments":{}}]`, "null", "")
	assert.Equal(t, 0.0, score, "calls were expected and none were made")

	score, reasoning = runToolCallAccuracy(t, "[]", `[{"name":"a","arguments":{}}]`, "")
	assert.Equal(t, 0.0, score)
	assert.Contains(t, reasoning, "No tool calls were expected")

	score, _ = runToolCallAccuracy(t, `[{"name":"a","arguments":{}}]`, "not json", "")
	assert.Equal(t, 0.0, score, "unreadable actual calls count as no calls, as in Maxim")
}

func TestToolCallAccuracy_ProviderFormats(t *testing.T) {
	expected := `[{"name":"get_weather","arguments":{"city":"Paris"}}]`
	for name, actual := range map[string]string{
		"openai":           `[{"id":"1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]`,
		"anthropic":        `[{"type":"tool_use","id":"1","name":"get_weather","input":{"city":"Paris"}}]`,
		"bedrock":          `[{"toolUse":{"toolUseId":"1","name":"get_weather","input":{"city":"Paris"}}}]`,
		"arguments string": `[{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}]`,
		"single object":    `{"name":"get_weather","arguments":{"city":"Paris"}}`,
	} {
		score, reasoning := runToolCallAccuracy(t, expected, actual, "")
		assert.Equal(t, 1.0, score, "%s: %s", name, reasoning)
	}
}

// Without tool definitions Maxim compares the two lists as plain JSON, so the
// grammar is never read. Bifrost reads the grammar either way and compares each
// call's arguments by equality.
func TestToolCallAccuracy_GrammarWorksWithoutSchemas(t *testing.T) {
	expected := `[{"inAnyOrder":[{"name":"a","arguments":{"x":1}},{"name":"b","arguments":{}}]},{"anyOne":[{"name":"c","arguments":{}},{"name":"d","arguments":{}}]}]`
	score, reasoning := runToolCallAccuracy(t, expected, `[{"name":"b","arguments":{}},{"name":"a","arguments":{"x":1}},{"name":"d","arguments":{}}]`, "")
	assert.Equal(t, 1.0, score, reasoning)

	score, reasoning = runToolCallAccuracy(t, expected, `[{"name":"b","arguments":{}},{"name":"a","arguments":{"x":2}},{"name":"d","arguments":{}}]`, "")
	assert.Equal(t, 0.0, score, "without a schema, arguments must be equal")
	assert.Contains(t, reasoning, `"a"`)
}

func TestToolCallAccuracy_ToolDefinitionFormats(t *testing.T) {
	expected := `[{"name":"search","arguments":{"query":"Paris Hotels"}}]`
	actual := `[{"name":"search","arguments":{"query":"paris-hotels"}}]`
	schema := `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`
	for name, tools := range map[string]string{
		"openai":             `[{"type":"function","function":{"name":"search","parameters":` + schema + `}}]`,
		"flat":               `[{"name":"search","parameters":` + schema + `}]`,
		"anthropic":          `[{"name":"search","input_schema":` + schema + `}]`,
		"schema as a string": `[{"name":"search","parameters":` + string(mustJSON(t, schema)) + `}]`,
	} {
		score, reasoning := runToolCallAccuracy(t, expected, actual, tools)
		assert.Equal(t, 1.0, score, "%s: a string argument is compared after normalizing\n%s", name, reasoning)
	}
	score, _ := runToolCallAccuracy(t, expected, actual, "")
	assert.Equal(t, 0.0, score, "without the schema the two strings differ")
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

func TestToolCallAccuracy_ExpectedCallsCanUseVariables(t *testing.T) {
	result, err := toolCallAccuracyEvaluator{}.Evaluate(context.Background(), Input{Variables: map[string]string{
		VariableExpectedToolCalls: `[{"name":"get_weather","arguments":{"city":"{{city}}"}}]`,
		VariableToolCalls:         `[{"name":"get_weather","arguments":{"city":"Paris"}}]`,
		"city":                    "Paris",
	}})
	require.NoError(t, err)
	assert.Equal(t, 1.0, *result.Score.Number)
}

func TestToolCallAccuracy_BadInputIsAnError(t *testing.T) {
	_, err := toolCallAccuracyEvaluator{}.Evaluate(context.Background(), Input{Variables: map[string]string{
		VariableExpectedToolCalls: `[{"name":`,
		VariableToolCalls:         `[{"name":"a","arguments":{}}]`,
	}})
	require.Error(t, err, "expected calls that are not JSON cannot be scored")

	_, err = toolCallAccuracyEvaluator{}.Evaluate(context.Background(), Input{Variables: map[string]string{
		VariableExpectedToolCalls: `[{"tool":"a"}]`,
		VariableToolCalls:         `[{"name":"a","arguments":{}}]`,
	}})
	require.Error(t, err, "an element that is neither a call nor a block is rejected")
}

func TestToolCallAccuracy_InCatalog(t *testing.T) {
	entry, ok := CatalogEntryFor(BuiltinToolCallAccuracy)
	require.True(t, ok)
	assert.Equal(t, EvaluatorTypeStatistical, entry.Type)
	assert.Equal(t, CategoryAgent, entry.Category)
	assert.Equal(t, []string{VariableExpectedToolCalls, VariableToolCalls}, entry.Variables)
	assert.Equal(t, []string{VariableTools}, entry.OptionalVariables)
	assert.False(t, entry.NeedsEmbedding)
}
