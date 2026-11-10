package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode"
)

// BuiltinToolCallAccuracy is the catalog key of the tool call accuracy metric.
const BuiltinToolCallAccuracy = "tool_call_accuracy"

// Variables the tool call accuracy metric reads.
const (
	VariableExpectedToolCalls = "expected_tool_calls" // what should have been called, in the grammar below
	VariableToolCalls         = "tool_calls"          // what the model called
	VariableTools             = "tools"               // optional: the tool definitions, with their JSON schemas
)

// Keywords of the expected calls grammar. They keep Maxim's spelling, so a dataset
// exported from Maxim scores the same here.
const (
	toolCallsInAnyOrder = "inAnyOrder"
	toolCallsAnyOne     = "anyOne"
)

// toolCall is one call: a function name and its arguments.
type toolCall struct {
	name string
	args map[string]any
}

// toolCallElement is one element of the expected calls: a call, an inAnyOrder block
// or an anyOne block. Blocks nest.
type toolCallElement struct {
	call       *toolCall
	inAnyOrder []toolCallElement
	anyOne     [][]toolCallElement
	block      string // "", toolCallsInAnyOrder or toolCallsAnyOne
}

// toolCallAccuracyEvaluator is a port of Maxim's ToolsCallingAccuracyEvaluator, which
// follows the Berkeley Function Calling Leaderboard rules. The score is 1 when the
// calls the model made satisfy the whole expected sequence with nothing left over,
// and 0 otherwise.
type toolCallAccuracyEvaluator struct{}

func (toolCallAccuracyEvaluator) RequiredVariables() []string {
	return []string{VariableExpectedToolCalls, VariableToolCalls}
}

// emptyToolCalls reports whether a variable holds no calls at all.
func emptyToolCalls(raw string) bool {
	switch strings.TrimSpace(raw) {
	case "", "[]", "{}", "null":
		return true
	}
	return false
}

func (toolCallAccuracyEvaluator) Evaluate(_ context.Context, in Input) (Result, error) {
	expectedRaw := in.Variables[VariableExpectedToolCalls]
	actualRaw := in.Variables[VariableToolCalls]
	expectedEmpty, actualEmpty := emptyToolCalls(expectedRaw), emptyToolCalls(actualRaw)
	if expectedEmpty && actualEmpty {
		return Result{Score: NumberScore(1), Reasoning: "The model completed the task without making any tool calls, as expected."}, nil
	}

	// Calls that cannot be read count as no calls, as in Maxim.
	var actualJSON any
	if !actualEmpty {
		_ = json.Unmarshal([]byte(actualRaw), &actualJSON)
	}
	var actual []toolCall
	if elements, err := parseToolCallElements(actualJSON); err == nil {
		for _, element := range elements {
			if element.call == nil {
				return Result{}, errors.New("tool_calls must be a list of calls; inAnyOrder and anyOne belong in expected_tool_calls")
			}
			actual = append(actual, *element.call)
		}
	} else if actualJSON != nil {
		return Result{}, fmt.Errorf("tool_calls: %w", err)
	}
	// No calls against a real expectation still goes through the matcher below: an
	// expectation can allow making no call, with an empty block or an empty alternative.
	if expectedEmpty {
		return Result{Score: NumberScore(0), Reasoning: fmt.Sprintf("No tool calls were expected, but the model made %d.", len(actual))}, nil
	}

	// The expected calls may refer to other variables of the row, as in Maxim.
	expectedText := compileTemplate(expectedRaw).renderString(in, nil)
	var expectedJSON any
	if err := json.Unmarshal([]byte(expectedText), &expectedJSON); err != nil {
		return Result{}, fmt.Errorf("expected_tool_calls is not valid JSON after variables were filled in: %w", err)
	}
	expected, err := parseToolCallElements(expectedJSON)
	if err != nil {
		return Result{}, fmt.Errorf("expected_tool_calls: %w", err)
	}
	schemas, err := parseToolSchemas(in.Variables[VariableTools])
	if err != nil {
		return Result{}, err
	}

	matcher := toolCallMatcher{schemas: schemas}
	if matcher.sequence(actual, expected) {
		return Result{Score: NumberScore(1), Reasoning: matcher.reasoning("All expected tool calls matched.")}, nil
	}
	return Result{Score: NumberScore(0), Reasoning: matcher.reasoning("The tool calls did not match what was expected.")}, nil
}

// ---------------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------------

// parseToolCallElements reads a list of elements, or one element on its own.
func parseToolCallElements(v any) ([]toolCallElement, error) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		list = []any{v}
	}
	elements := make([]toolCallElement, 0, len(list))
	for _, item := range list {
		element, err := parseToolCallElement(item)
		if err != nil {
			return nil, err
		}
		elements = append(elements, element)
	}
	return elements, nil
}

func parseToolCallElement(v any) (toolCallElement, error) {
	object, ok := v.(map[string]any)
	if !ok {
		return toolCallElement{}, fmt.Errorf("an element must be an object, got %s", describeJSON(v))
	}
	if raw, ok := object[toolCallsInAnyOrder]; ok {
		list, ok := raw.([]any)
		if !ok {
			return toolCallElement{}, fmt.Errorf("%s must hold a list", toolCallsInAnyOrder)
		}
		elements, err := parseToolCallElements(list)
		if err != nil {
			return toolCallElement{}, err
		}
		return toolCallElement{block: toolCallsInAnyOrder, inAnyOrder: elements}, nil
	}
	if raw, ok := object[toolCallsAnyOne]; ok {
		list, ok := raw.([]any)
		if !ok {
			return toolCallElement{}, fmt.Errorf("%s must hold a list", toolCallsAnyOne)
		}
		element := toolCallElement{block: toolCallsAnyOne, anyOne: make([][]toolCallElement, 0, len(list))}
		// A list of lists gives each alternative as a sequence. A plain list makes each
		// item an alternative of one element.
		nested := len(list) > 0
		if nested {
			_, nested = list[0].([]any)
		}
		for _, item := range list {
			var branch []toolCallElement
			var err error
			if nested {
				sequence, ok := item.([]any)
				if !ok {
					return toolCallElement{}, fmt.Errorf("every alternative of %s must be a list when the first one is", toolCallsAnyOne)
				}
				branch, err = parseToolCallElements(sequence)
			} else {
				var single toolCallElement
				single, err = parseToolCallElement(item)
				branch = []toolCallElement{single}
			}
			if err != nil {
				return toolCallElement{}, err
			}
			element.anyOne = append(element.anyOne, branch)
		}
		return element, nil
	}
	call, err := parseToolCall(object)
	if err != nil {
		return toolCallElement{}, err
	}
	return toolCallElement{call: call}, nil
}

// parseToolCall reads a call in the plain, OpenAI, Anthropic or Bedrock shape.
func parseToolCall(object map[string]any) (*toolCall, error) {
	name, _ := object["name"].(string)
	arguments, hasArguments := object["arguments"]
	if function, ok := object["function"].(map[string]any); ok { // OpenAI
		name, _ = function["name"].(string)
		arguments, hasArguments = function["arguments"]
	} else if use, ok := object["toolUse"].(map[string]any); ok { // Bedrock
		name, _ = use["name"].(string)
		arguments, hasArguments = use["input"]
	} else if input, ok := object["input"]; ok && !hasArguments { // Anthropic
		arguments, hasArguments = input, true
	}
	if name == "" {
		return nil, errors.New(`a tool call needs a name: use {"name": "function_name", "arguments": {}}, or the OpenAI, Anthropic or Bedrock tool call shape`)
	}
	call := &toolCall{name: name, args: map[string]any{}}
	if !hasArguments || arguments == nil {
		return call, nil
	}
	if text, ok := arguments.(string); ok {
		var decoded any
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			return nil, fmt.Errorf("the arguments of %q are not valid JSON", name)
		}
		arguments = decoded
	}
	args, ok := arguments.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the arguments of %q must be an object", name)
	}
	call.args = args
	return call, nil
}

// parseToolSchemas reads the tool definitions into a map of function name to its
// parameter schema. It accepts the OpenAI, plain, Anthropic and Bedrock shapes.
func parseToolSchemas(raw string) (map[string]map[string]any, error) {
	if emptyToolCalls(raw) {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("tools is not valid JSON: %w", err)
	}
	list, ok := decoded.([]any)
	if !ok {
		list = []any{decoded}
	}
	schemas := make(map[string]map[string]any, len(list))
	for _, item := range list {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if function, ok := object["function"].(map[string]any); ok {
			object = function
		} else if spec, ok := object["toolSpec"].(map[string]any); ok {
			object = spec
		}
		name, _ := object["name"].(string)
		if name == "" {
			continue
		}
		var schema any
		for _, key := range []string{"parameters", "input_schema", "inputSchema"} {
			if value, ok := object[key]; ok {
				schema = value
				break
			}
		}
		if wrapped, ok := schema.(map[string]any); ok {
			if inner, ok := wrapped["json"].(map[string]any); ok && wrapped["type"] == nil { // Bedrock
				schema = inner
			}
		}
		if text, ok := schema.(string); ok {
			var parsed any
			if err := json.Unmarshal([]byte(text), &parsed); err != nil {
				continue // an unreadable schema means the function is compared without one
			}
			schema = parsed
		}
		if parameters, ok := schema.(map[string]any); ok {
			schemas[name] = parameters
		}
	}
	return schemas, nil
}

// ---------------------------------------------------------------------------
// Matching
// ---------------------------------------------------------------------------

// toolCallMatcher walks the expected elements against the actual calls and records
// why each one matched or did not.
type toolCallMatcher struct {
	schemas map[string]map[string]any
	notes   []string
}

type toolCallMatch struct {
	ok       bool
	consumed int // actual calls used
}

func (m *toolCallMatcher) note(ok bool, format string, args ...any) {
	prefix := "FAIL: "
	if ok {
		prefix = "ok: "
	}
	m.notes = append(m.notes, prefix+fmt.Sprintf(format, args...))
}

func (m *toolCallMatcher) reasoning(summary string) string {
	var b strings.Builder
	b.WriteString(summary)
	for _, note := range m.notes {
		b.WriteByte('\n')
		b.WriteString(note)
	}
	return b.String()
}

// sequence matches the top-level expected elements in order. Every actual call must
// be used: an extra call fails the match.
func (m *toolCallMatcher) sequence(actual []toolCall, expected []toolCallElement) bool {
	next := 0
	for i, element := range expected {
		if element.call != nil && next >= len(actual) {
			m.note(false, "element %d: expected a call to %q, but the model made no more tool calls", i+1, element.call.name)
			return false
		}
		match := m.element(actual, element, next)
		if !match.ok {
			return false
		}
		next += match.consumed
	}
	if next < len(actual) {
		names := make([]string, 0, len(actual)-next)
		for _, call := range actual[next:] {
			names = append(names, strconv.Quote(call.name))
		}
		m.note(false, "the model made %d tool call(s) that were not expected: %s", len(actual)-next, strings.Join(names, ", "))
		return false
	}
	return true
}

func (m *toolCallMatcher) element(actual []toolCall, element toolCallElement, start int) toolCallMatch {
	switch element.block {
	case toolCallsInAnyOrder:
		return m.inAnyOrder(actual, element.inAnyOrder, start)
	case toolCallsAnyOne:
		return m.anyOne(actual, element.anyOne, start)
	}
	if start >= len(actual) {
		m.note(false, "expected a call to %q, but the model made no more tool calls", element.call.name)
		return toolCallMatch{}
	}
	return toolCallMatch{ok: m.call(actual[start], *element.call), consumed: 1}
}

// call compares one actual call with one expected call.
func (m *toolCallMatcher) call(actual, expected toolCall) bool {
	if actual.name != expected.name {
		m.note(false, "function name mismatch: expected %q, got %q", expected.name, actual.name)
		return false
	}
	schema, known := m.schemas[expected.name]
	if !known {
		if !reflect.DeepEqual(actual.args, expected.args) {
			m.note(false, "%q: arguments differ (compared by equality, no schema for this function)", expected.name)
			return false
		}
		m.note(true, "%q matched (compared by equality, no schema for this function)", expected.name)
		return true
	}
	before := len(m.notes)
	ok := m.parameters(actual.args, expected.args, schema, expected.name)
	if ok {
		m.notes = m.notes[:before]
		m.note(true, "%q matched", expected.name)
	}
	return ok
}

// inAnyOrder matches a block whose elements may come in any order. It looks at
// exactly as many actual calls as the block's elements account for, and gives each
// expected element the first unused call that satisfies it.
func (m *toolCallMatcher) inAnyOrder(actual []toolCall, expected []toolCallElement, start int) toolCallMatch {
	if len(expected) == 0 {
		return toolCallMatch{ok: true}
	}
	counts := make([]int, len(expected))
	total := 0
	for i, element := range expected {
		counts[i] = 1 // a call, or an anyOne block
		if element.block == toolCallsInAnyOrder {
			counts[i] = len(element.inAnyOrder)
		}
		total += counts[i]
	}
	if len(actual)-start < total {
		m.note(false, "inAnyOrder block needs %d tool call(s), but only %d were left", total, max(len(actual)-start, 0))
		return toolCallMatch{}
	}
	window := actual[start : start+total]
	used := make([]bool, total)
	for i, element := range expected {
		matched := false
		before := len(m.notes)
		for at := range window {
			if used[at] {
				continue
			}
			m.notes = m.notes[:before] // keep the notes of the last candidate only
			var match toolCallMatch
			if counts[i] == 1 {
				match = m.element(window[at:at+1], element, 0)
			} else {
				if at+counts[i] > total {
					continue
				}
				free := true
				for k := range counts[i] {
					free = free && !used[at+k]
				}
				if !free {
					continue
				}
				match = m.element(window, element, at)
			}
			if match.ok {
				for k := range match.consumed {
					used[at+k] = true
				}
				matched = true
				break
			}
		}
		if !matched {
			m.note(false, "inAnyOrder block: no tool call satisfied %s", describeToolCallElement(element))
			return toolCallMatch{}
		}
	}
	m.note(true, "inAnyOrder block matched %d tool call(s)", total)
	return toolCallMatch{ok: true, consumed: total}
}

// anyOne matches a block of alternatives. Exactly one must match: two matching
// alternatives make the expectation ambiguous, which fails.
func (m *toolCallMatcher) anyOne(actual []toolCall, branches [][]toolCallElement, start int) toolCallMatch {
	allEmpty := true
	for _, branch := range branches {
		allEmpty = allEmpty && len(branch) == 0
	}
	if allEmpty {
		return toolCallMatch{ok: true}
	}
	before := len(m.notes)
	for i, branch := range branches {
		match := m.branch(actual, branch, start)
		if !match.ok {
			continue
		}
		for j := i + 1; j < len(branches); j++ {
			if m.branch(actual, branches[j], start).ok {
				m.notes = m.notes[:before]
				m.note(false, "anyOne block is ambiguous: alternatives %d and %d both match", i+1, j+1)
				return toolCallMatch{}
			}
		}
		m.notes = m.notes[:before]
		m.note(true, "anyOne block matched alternative %d", i+1)
		return match
	}
	m.note(false, "anyOne block: none of the %d alternatives matched", len(branches))
	return toolCallMatch{}
}

// branch matches one alternative of an anyOne block: its elements, in order.
func (m *toolCallMatcher) branch(actual []toolCall, branch []toolCallElement, start int) toolCallMatch {
	consumed := 0
	for _, element := range branch {
		match := m.element(actual, element, start+consumed)
		if !match.ok {
			return toolCallMatch{}
		}
		consumed += match.consumed
	}
	return toolCallMatch{ok: true, consumed: consumed}
}

func describeToolCallElement(element toolCallElement) string {
	switch element.block {
	case toolCallsInAnyOrder:
		return fmt.Sprintf("a nested inAnyOrder block of %d element(s)", len(element.inAnyOrder))
	case toolCallsAnyOne:
		return fmt.Sprintf("an anyOne block of %d alternative(s)", len(element.anyOne))
	}
	return fmt.Sprintf("the call to %q", element.call.name)
}

// ---------------------------------------------------------------------------
// Argument comparison
// ---------------------------------------------------------------------------

func schemaProperties(schema map[string]any) (map[string]any, bool) {
	properties, ok := schema["properties"].(map[string]any)
	return properties, ok
}

func schemaRequired(schema map[string]any) []string {
	list, _ := schema["required"].([]any)
	required := make([]string, 0, len(list))
	for _, item := range list {
		if name, ok := item.(string); ok {
			required = append(required, name)
		}
	}
	return required
}

// parameters compares a call's arguments against the expected ones, with the
// function's schema. Only arguments named in the expected call are compared, but
// every required parameter must be present and no argument may be outside the schema.
func (m *toolCallMatcher) parameters(actual, expected map[string]any, schema map[string]any, function string) bool {
	properties, _ := schemaProperties(schema)
	ok := true
	for _, name := range schemaRequired(schema) {
		value, present := actual[name]
		switch {
		case !present:
			m.note(false, "%q: required parameter %q is missing", function, name)
			ok = false
		case value == nil:
			m.note(false, "%q: required parameter %q is null", function, name)
			ok = false
		}
	}
	for name := range actual {
		if _, defined := properties[name]; !defined {
			m.note(false, "%q: parameter %q is not in the function's schema", function, name)
			ok = false
		}
	}
	for name, want := range expected {
		paramSchema, defined := properties[name]
		if !defined {
			m.note(false, "%q: expected parameter %q is not in the function's schema", function, name)
			ok = false
			continue
		}
		got, present := actual[name]
		if !present || got == nil {
			m.note(false, "%q: parameter %q is missing", function, name)
			ok = false
			continue
		}
		typed, _ := paramSchema.(map[string]any)
		if reason := compareToolArgument(got, want, typed, name); reason != "" {
			m.note(false, "%q: %s", function, reason)
			ok = false
		}
	}
	return ok
}

// toolStringNoise are the characters a string argument is compared without.
const toolStringNoise = ",./-_*^"

func normalizeToolString(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsSpace(r) || strings.ContainsRune(toolStringNoise, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// toolNumber reads an expected value as a number the way JavaScript's Number() does
// for the cases that occur in JSON.
func toolNumber(v any) float64 {
	switch value := v.(type) {
	case float64:
		return value
	case string:
		text := strings.TrimSpace(value)
		if text == "" {
			return 0
		}
		number, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return math.NaN()
		}
		return number
	case bool:
		if value {
			return 1
		}
		return 0
	case nil:
		return 0
	}
	return math.NaN()
}

func describeJSON(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case float64:
		return "a number"
	case bool:
		return "a boolean"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	}
	return fmt.Sprintf("%T", v)
}

// compareToolArgument compares one argument by its schema type and returns why it
// does not match, or "" when it does. A nil schema compares by equality.
func compareToolArgument(actual, expected any, schema map[string]any, path string) string {
	differs := func() string {
		if reflect.DeepEqual(actual, expected) {
			return ""
		}
		return fmt.Sprintf("parameter %q differs from the expected value", path)
	}
	if schema == nil {
		return differs()
	}
	kind, _ := schema["type"].(string)
	switch kind {
	case "string":
		want, ok := expected.(string)
		if !ok {
			return fmt.Sprintf("parameter %q: the expected value is %s, but the schema says string", path, describeJSON(expected))
		}
		got, ok := actual.(string)
		if !ok {
			return fmt.Sprintf("parameter %q must be a string, got %s", path, describeJSON(actual))
		}
		if normalizeToolString(got) != normalizeToolString(want) {
			return fmt.Sprintf("parameter %q: expected %q, got %q", path, want, got)
		}
		return ""
	case "number", "integer":
		want := toolNumber(expected)
		if math.IsNaN(want) {
			return fmt.Sprintf("parameter %q: the expected value is not a number", path)
		}
		if kind == "integer" && want != math.Trunc(want) {
			return fmt.Sprintf("parameter %q: the expected value is not an integer", path)
		}
		got, ok := actual.(float64)
		if !ok {
			return fmt.Sprintf("parameter %q must be a %s, got %s", path, kind, describeJSON(actual))
		}
		if kind == "integer" {
			if got != math.Trunc(got) {
				return fmt.Sprintf("parameter %q must be an integer, got %v", path, got)
			}
			if got != want {
				return fmt.Sprintf("parameter %q: expected %v, got %v", path, want, got)
			}
			return ""
		}
		if math.Abs(got-want) >= 1e-9 {
			return fmt.Sprintf("parameter %q: expected %v, got %v", path, want, got)
		}
		return ""
	case "boolean":
		want, ok := expected.(bool)
		if !ok {
			return fmt.Sprintf("parameter %q: the expected value is %s, but the schema says boolean", path, describeJSON(expected))
		}
		got, ok := actual.(bool)
		if !ok {
			return fmt.Sprintf("parameter %q must be a boolean, got %s", path, describeJSON(actual))
		}
		if got != want {
			return fmt.Sprintf("parameter %q: expected %v, got %v", path, want, got)
		}
		return ""
	case "array":
		want, ok := expected.([]any)
		if !ok {
			return fmt.Sprintf("parameter %q: the expected value is %s, but the schema says array", path, describeJSON(expected))
		}
		got, ok := actual.([]any)
		if !ok {
			return fmt.Sprintf("parameter %q must be an array, got %s", path, describeJSON(actual))
		}
		if len(got) != len(want) {
			return fmt.Sprintf("parameter %q: expected %d element(s), got %d", path, len(want), len(got))
		}
		items, _ := schema["items"].(map[string]any)
		for i := range got {
			if reason := compareToolArgument(got[i], want[i], items, fmt.Sprintf("%s[%d]", path, i)); reason != "" {
				return reason
			}
		}
		return ""
	case "object":
		return compareToolObject(actual, expected, schema, path)
	}
	return differs()
}

func compareToolObject(actual, expected any, schema map[string]any, path string) string {
	want, ok := expected.(map[string]any)
	if !ok {
		return fmt.Sprintf("parameter %q: the expected value is %s, but the schema says object", path, describeJSON(expected))
	}
	got, ok := actual.(map[string]any)
	if !ok {
		return fmt.Sprintf("parameter %q must be an object, got %s", path, describeJSON(actual))
	}
	properties, hasProperties := schemaProperties(schema)
	if !hasProperties || len(properties) == 0 {
		for key, value := range want {
			other, present := got[key]
			if !present {
				return fmt.Sprintf("parameter %q is missing", path+"."+key)
			}
			if !reflect.DeepEqual(other, value) {
				return fmt.Sprintf("parameter %q differs from the expected value", path+"."+key)
			}
		}
		return ""
	}
	for _, key := range schemaRequired(schema) {
		if _, present := got[key]; !present {
			return fmt.Sprintf("required parameter %q is missing", path+"."+key)
		}
	}
	for key := range got {
		if _, defined := properties[key]; !defined {
			return fmt.Sprintf("parameter %q is not in the function's schema", path+"."+key)
		}
	}
	for key, value := range want {
		keySchema, defined := properties[key]
		if !defined {
			return fmt.Sprintf("expected parameter %q is not in the function's schema", path+"."+key)
		}
		other, present := got[key]
		if !present {
			return fmt.Sprintf("parameter %q is missing", path+"."+key)
		}
		typed, _ := keySchema.(map[string]any)
		if reason := compareToolArgument(other, value, typed, path+"."+key); reason != "" {
			return reason
		}
	}
	return ""
}
