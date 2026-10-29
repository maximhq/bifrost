package gemini

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixtures under testdata/streamfunctioncallargs are Vertex streamGenerateContent SSE bodies
// for requests sent with streamFunctionCallArguments: true. The vertex-*.sse files were
// captured from Vertex (global, gemini-4-argon and gemini-3.8-flash) on 2026-10-29;
// handcrafted-edge-cases.sse covers shapes those captures do not reach (strings with every
// escape class split across parts, a functionCall in the same chunk as thought and text
// parts, a call ended without the empty functionCall marker, a single-part streamed call,
// a call left open when finishReason arrives, bracket-quoted keys).
var streamFunctionCallArgsFixtures = []string{
	"vertex-leaf-kinds.sse",
	"vertex-parallel-calls.sse",
	"vertex-thoughts-then-calls.sse",
	"handcrafted-edge-cases.sse",
}

// StreamFunctionCallArgsFixtures lists the fixture names; exported for the gemini_test package.
func StreamFunctionCallArgsFixtures() []string { return streamFunctionCallArgsFixtures }

// LoadStreamFunctionCallArgsFixture decodes a fixture into the chunks the stream readers hand
// to the converters, through the same processGeminiStreamChunk the providers use.
func LoadStreamFunctionCallArgsFixture(t testing.TB, name string) []*GenerateContentResponse {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "streamfunctioncallargs", name))
	require.NoError(t, err)
	var chunks []*GenerateContentResponse
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		chunk, err := processGeminiStreamChunk(bytes.TrimPrefix(line, []byte("data: ")))
		require.NoError(t, err)
		chunks = append(chunks, chunk)
	}
	require.NoError(t, scanner.Err())
	require.NotEmpty(t, chunks)
	return chunks
}

// ExpectedStreamedCall is what a fixture's parts say one function call is.
type ExpectedStreamedCall struct {
	Name      string
	ID        string
	Signature []byte
	Args      any // decoded with json.Number, compared against DecodeArgs output
	Streamed  bool
	// FirstLeafChunk and EndChunk are the chunk positions of the first argument leaf and of
	// the part (or finishReason) that ended the call, so tests can prove argument text left
	// the converter before the call finished.
	FirstLeafChunk int
	EndChunk       int
}

// ExpectedStreamedCalls rebuilds every function call in chunks with an oracle that shares no
// code with geminiPartialArgsWriter: it assembles a value tree by JSONPath (concatenating
// string leaves, creating containers for "[]"/"{}" entries) instead of streaming text.
func ExpectedStreamedCalls(t testing.TB, chunks []*GenerateContentResponse) []ExpectedStreamedCall {
	t.Helper()
	var calls []ExpectedStreamedCall
	var root map[string]any
	open := false
	end := func(at int) {
		if open {
			calls[len(calls)-1].Args = root
			calls[len(calls)-1].EndChunk = at
			open = false
		}
	}
	for i, chunk := range chunks {
		if len(chunk.Candidates) == 0 || chunk.Candidates[0].Content == nil {
			continue
		}
		for _, part := range chunk.Candidates[0].Content.Parts {
			fc := part.FunctionCall
			if fc == nil {
				continue
			}
			if fc.Name != "" {
				end(i)
				if fc.PartialArgs == nil && (fc.WillContinue == nil || !*fc.WillContinue) {
					var args any = map[string]any{}
					if len(fc.Args) > 0 {
						args = decodeJSONWithNumbers(t, fc.Args)
					}
					calls = append(calls, ExpectedStreamedCall{Name: fc.Name, ID: fc.ID, Signature: part.ThoughtSignature, Args: args, FirstLeafChunk: -1, EndChunk: i})
					continue
				}
				calls = append(calls, ExpectedStreamedCall{Name: fc.Name, ID: fc.ID, Signature: part.ThoughtSignature, Streamed: true, FirstLeafChunk: -1})
				root = map[string]any{}
				open = true
			}
			if !open {
				continue
			}
			if len(fc.PartialArgs) > 0 && calls[len(calls)-1].FirstLeafChunk < 0 {
				calls[len(calls)-1].FirstLeafChunk = i
			}
			for _, arg := range fc.PartialArgs {
				oracleApply(t, root, arg)
			}
			ended := fc.WillContinue == nil || !*fc.WillContinue
			for _, arg := range fc.PartialArgs {
				if arg.WillContinue != nil && *arg.WillContinue {
					ended = false
				}
			}
			if ended {
				end(i)
			}
		}
		if chunk.Candidates[0].FinishReason != "" {
			end(i)
		}
	}
	require.False(t, open, "fixture ends with an open call and no finishReason")
	return calls
}

var oraclePathToken = regexp.MustCompile(`^(?:\.([^.\[\]{}]+)|\[(\d+)\]|\['((?:[^'\\]|\\.)*)'\])`)

func oracleApply(t testing.TB, root map[string]any, arg *PartialArg) {
	t.Helper()
	path := strings.TrimPrefix(arg.JSONPath, "$")
	var container any
	switch {
	case strings.HasSuffix(path, "[]"):
		path, container = strings.TrimSuffix(path, "[]"), []any{}
	case strings.HasSuffix(path, "{}"):
		path, container = strings.TrimSuffix(path, "{}"), map[string]any{}
	}
	var keys []any
	for path != "" {
		m := oraclePathToken.FindStringSubmatch(path)
		require.NotNil(t, m, "oracle cannot parse %q", arg.JSONPath)
		switch {
		case m[1] != "":
			keys = append(keys, m[1])
		case m[2] != "":
			n, _ := strconv.Atoi(m[2])
			keys = append(keys, n)
		default:
			keys = append(keys, strings.ReplaceAll(m[3], `\'`, `'`))
		}
		path = path[len(m[0]):]
	}
	var leaf any
	switch {
	case container != nil:
		leaf = container
	case arg.StringValue != nil:
		leaf = *arg.StringValue
	case len(arg.NumberValue) > 0:
		leaf = json.Number(arg.NumberValue)
	case arg.BoolValue != nil:
		leaf = *arg.BoolValue
	case len(arg.NullValue) > 0:
		leaf = nil
	default:
		return
	}
	var cur any = root
	for i, key := range keys {
		last := i == len(keys)-1
		next := func() any {
			if last {
				return leaf
			}
			if _, isIndex := keys[i+1].(int); isIndex {
				return []any{}
			}
			return map[string]any{}
		}
		switch c := cur.(type) {
		case map[string]any:
			k := key.(string)
			existing, ok := c[k]
			switch {
			case last && ok && container != nil:
				// container marker after its children: nothing to add
			case last && ok:
				if s, isString := existing.(string); isString && arg.StringValue != nil {
					c[k] = s + *arg.StringValue
				}
			case last || !ok:
				c[k] = next()
			}
			cur = c[k]
		case []any:
			idx := key.(int)
			parentSet := setterForSliceParent(root, keys[:i])
			for len(c) <= idx {
				c = append(c, nil)
			}
			if c[idx] == nil || (last && container == nil) {
				if last && arg.StringValue != nil {
					if s, isString := c[idx].(string); isString {
						c[idx] = s + *arg.StringValue
					} else {
						c[idx] = leaf
					}
				} else if c[idx] == nil {
					c[idx] = next()
				}
			}
			parentSet(c)
			cur = c[idx]
		}
	}
}

// setterForSliceParent returns a function that stores a (possibly re-allocated) slice back
// at keys inside root.
func setterForSliceParent(root map[string]any, keys []any) func([]any) {
	return func(s []any) {
		var cur any = root
		for i, key := range keys {
			last := i == len(keys)-1
			switch c := cur.(type) {
			case map[string]any:
				if last {
					c[key.(string)] = s
					return
				}
				cur = c[key.(string)]
			case []any:
				if last {
					c[key.(int)] = s
					return
				}
				cur = c[key.(int)]
			}
		}
	}
}

func decodeJSONWithNumbers(t testing.TB, raw []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	require.NoError(t, dec.Decode(&v), "not valid JSON: %s", raw)
	require.False(t, dec.More(), "trailing data after JSON value: %s", raw)
	return v
}

// DecodeArgs parses concatenated argument text the way a client would, failing the test if
// it is not one valid JSON value.
func DecodeArgs(t testing.TB, args string) any {
	t.Helper()
	return decodeJSONWithNumbers(t, []byte(args))
}

func TestStreamFunctionCallArgsOracleMatchesKnownFixture(t *testing.T) {
	// Guards the oracle itself against the hand-written expectation, so a bug shared by the
	// oracle and the writer cannot pass the converter tests.
	calls := ExpectedStreamedCalls(t, LoadStreamFunctionCallArgsFixture(t, "handcrafted-edge-cases.sse"))
	require.Len(t, calls, 4)
	want := DecodeArgs(t, `{"body":"He said \"hi\" and left C:\\tmp\\x\n\ttab 中文 🚀 \u0001 </script>","n":0.1,"big":12345678901234567890,"neg":-3e-7,"ok":false,"nothing":null,"rows":[{"cells":["a",2]},{"cells":[]},{}],"cfg":{},"odd.key":"dotted","z":true}`)
	assert.Equal(t, want, calls[0].Args)
	assert.Equal(t, DecodeArgs(t, `{"x":1}`), calls[1].Args)
	assert.Equal(t, DecodeArgs(t, `{}`), calls[2].Args)
	assert.Equal(t, DecodeArgs(t, `{"msg":"cut "}`), calls[3].Args)

	leafKinds := ExpectedStreamedCalls(t, LoadStreamFunctionCallArgsFixture(t, "vertex-leaf-kinds.sse"))
	require.Len(t, leafKinds, 1)
	args := leafKinds[0].Args.(map[string]any)
	assert.Equal(t, []any{}, args["empty"], "Vertex sends $.empty[] as the only entry of an empty array")
	assert.Equal(t, map[string]any{}, args["meta"].(map[string]any)["extra"], "Vertex sends $.meta.extra{} as the only entry of an empty object")
}

// runPartialArgsWriter feeds one call's partialArgs groups through the writer and returns
// the fragments it produced, the last one being the closing text.
func runPartialArgsWriter(groups ...[]*PartialArg) []string {
	var w geminiPartialArgsWriter
	var fragments []string
	for _, group := range groups {
		fragments = append(fragments, string(w.appendPartialArgs(nil, group)))
	}
	return append(fragments, string(w.appendClose(nil)))
}

func strArg(path, value string, willContinue bool) *PartialArg {
	arg := &PartialArg{JSONPath: path, StringValue: &value}
	if willContinue {
		arg.WillContinue = &willContinue
	}
	return arg
}

func numArg(path, literal string) *PartialArg {
	return &PartialArg{JSONPath: path, NumberValue: json.RawMessage(literal)}
}

func TestGeminiPartialArgsWriter(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name   string
		groups [][]*PartialArg
		want   []string
	}{
		{
			name: "string split across parts with every escape class",
			groups: [][]*PartialArg{
				{strArg("$.s", `quote " backslash \ `, true)},
				{strArg("$.s", "newline\n tab\t ctrl\x01 ", true)},
				{strArg("$.s", "中文 🚀 \u2028", true)},
				{strArg("$.s", "", false)},
			},
			want: []string{`{"s":"quote \" backslash \\ `, `newline\n tab\t ctrl\u0001 `, "中文 🚀 \u2028", `"`, `}`},
		},
		{
			name:   "single-part string closes immediately",
			groups: [][]*PartialArg{{strArg("$.a", "x", false), strArg("$.b", "y", false)}},
			want:   []string{`{"a":"x","b":"y"`, `}`},
		},
		{
			name:   "repeated terminator after a closed string is not re-emitted",
			groups: [][]*PartialArg{{strArg("$.a", "x", false)}, {strArg("$.a", "", false)}},
			want:   []string{`{"a":"x"`, ``, `}`},
		},
		{
			name: "scalar leaves keep their literals",
			groups: [][]*PartialArg{{
				numArg("$.n", "42.5"), numArg("$.big", "12345678901234567890"),
				{JSONPath: "$.t", BoolValue: &yes}, {JSONPath: "$.f", BoolValue: &no},
				{JSONPath: "$.z", NullValue: json.RawMessage("null")},
			}},
			want: []string{`{"n":42.5,"big":12345678901234567890,"t":true,"f":false,"z":null`, `}`},
		},
		{
			name: "nested objects and arrays of objects without close markers",
			groups: [][]*PartialArg{
				{strArg("$.items[0].k", "x", true)}, {strArg("$.items[0].k", "", false)},
				{numArg("$.items[0].v", "1")}, {strArg("$.items[1].k", "y", false)},
				{numArg("$.meta.author.age", "7")}, {strArg("$.meta.author.name", "Ann", false)},
			},
			want: []string{`{"items":[{"k":"x`, `"`, `,"v":1`, `},{"k":"y"`, `}],"meta":{"author":{"age":7`, `,"name":"Ann"`, `}}}`},
		},
		{
			name: "close markers after children emit nothing; empty containers are written",
			groups: [][]*PartialArg{
				{strArg("$.tags[0]", "a", false), strArg("$.tags[1]", "b", false), {JSONPath: "$.tags[]"}},
				{{JSONPath: "$.meta.author{}"}, {JSONPath: "$.meta.extra{}"}, {JSONPath: "$.empty[]"}},
				{{JSONPath: "$.rows[0]{}"}, {JSONPath: "$.rows[1].cells[]"}},
			},
			want: []string{`{"tags":["a","b"`, `],"meta":{"author":{},"extra":{}},"empty":[`, `],"rows":[{},{"cells":[`, `]}]}`},
		},
		{
			name:   "bracket-quoted keys",
			groups: [][]*PartialArg{{strArg(`$['a.b']`, "1", false), strArg(`$["c\"d"]`, "2", false), strArg(`$['it\'s']`, "3", false)}},
			want:   []string{`{"a.b":"1","c\"d":"2","it's":"3"`, `}`},
		},
		{
			name:   "call without leaves closes to an empty object",
			groups: nil,
			want:   []string{`{}`},
		},
		{
			name:   "unparseable and value-less paths are skipped",
			groups: [][]*PartialArg{{{JSONPath: "$.a"}, {JSONPath: "a.b"}, {JSONPath: "$.x[oops]"}, numArg("$.ok", "1")}},
			want:   []string{`{"ok":1`, `}`},
		},
		{
			name: "late close markers for containers the document moved past are not re-emitted",
			groups: [][]*PartialArg{
				{numArg("$.a.b", "1"), numArg("$.c", "2"), {JSONPath: "$.a{}"}},
				{numArg("$.arr[0]", "1"), numArg("$.d", "3"), {JSONPath: "$.arr[]"}},
			},
			want: []string{`{"a":{"b":1},"c":2`, `,"arr":[1],"d":3`, `}`},
		},
		{
			name:   "a leaf re-entering a closed container is dropped rather than duplicating its key",
			groups: [][]*PartialArg{{numArg("$.x.a", "1"), numArg("$.y", "2"), numArg("$.x.b", "3"), numArg("$.arr[0]", "1"), numArg("$.z", "1"), numArg("$.arr[1]", "2")}},
			want:   []string{`{"x":{"a":1},"y":2,"arr":[1],"z":1`, `}`},
		},
		{
			name:   "a non-string leaf for the open string path closes the string",
			groups: [][]*PartialArg{{strArg("$.s", "ab", true)}, {numArg("$.s", "1"), numArg("$.n", "2")}},
			want:   []string{`{"s":"ab`, `","n":2`, `}`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runPartialArgsWriter(tt.groups...)
			assert.Equal(t, tt.want, got)
			joined := strings.Join(got, "")
			assert.True(t, json.Valid([]byte(joined)), "concatenation is not valid JSON: %s", joined)
		})
	}
}

func TestGeminiPartialArgsWriterResetReusesWriter(t *testing.T) {
	var w geminiPartialArgsWriter
	first := string(w.appendPartialArgs(nil, []*PartialArg{strArg("$.a.b", "x", true)})) + string(w.appendClose(nil))
	second := string(w.appendPartialArgs(nil, []*PartialArg{numArg("$.c", "1")})) + string(w.appendClose(nil))
	assert.Equal(t, `{"a":{"b":"x"}}`, first)
	assert.Equal(t, `{"c":1}`, second, "appendClose must leave the writer ready for the next call")
}

func TestFunctionCallStreamClassification(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name                    string
		fc                      FunctionCall
		start, continues, ended bool
	}{
		{"complete call", FunctionCall{Name: "f", Args: json.RawMessage(`{"a":1}`)}, false, false, true},
		{"complete call without args", FunctionCall{Name: "f"}, false, false, true},
		{"streamed start without leaves", FunctionCall{Name: "f", ID: "1", WillContinue: &yes}, true, false, false},
		{"streamed start with leaves", FunctionCall{Name: "f", PartialArgs: []*PartialArg{numArg("$.a", "1")}, WillContinue: &yes}, true, false, false},
		{"single-part streamed call", FunctionCall{Name: "f", PartialArgs: []*PartialArg{numArg("$.a", "1")}}, true, false, true},
		{"continuation", FunctionCall{PartialArgs: []*PartialArg{numArg("$.a", "1")}, WillContinue: &yes}, false, true, false},
		{"continuation with an open string ends nothing", FunctionCall{PartialArgs: []*PartialArg{strArg("$.a", "x", true)}}, false, true, false},
		{"last continuation", FunctionCall{PartialArgs: []*PartialArg{numArg("$.a", "1")}, WillContinue: &no}, false, true, true},
		{"empty end marker", FunctionCall{}, false, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.start, tt.fc.isStreamStart(), "isStreamStart")
			assert.Equal(t, tt.continues, tt.fc.isStreamContinuation(), "isStreamContinuation")
			assert.Equal(t, tt.ended, tt.fc.streamEnds(), "streamEnds")
		})
	}
}

func TestFunctionCallPartialArgsDecode(t *testing.T) {
	var part Part
	require.NoError(t, json.Unmarshal([]byte(`{"functionCall":{"partialArgs":[{"jsonPath":"$.n","numberValue":1.50},{"jsonPath":"$.z","nullValue":null},{"jsonPath":"$.s","stringValue":"a","willContinue":true}],"willContinue":true}}`), &part))
	require.NotNil(t, part.FunctionCall)
	require.Len(t, part.FunctionCall.PartialArgs, 3)
	assert.Equal(t, "1.50", string(part.FunctionCall.PartialArgs[0].NumberValue), "number literal kept verbatim")
	assert.Equal(t, "null", string(part.FunctionCall.PartialArgs[1].NullValue), "explicit null distinguishable from absent")
	assert.Empty(t, part.FunctionCall.PartialArgs[2].NullValue)
	require.NotNil(t, part.FunctionCall.WillContinue)
	assert.True(t, *part.FunctionCall.WillContinue)
}

// BenchmarkGeminiPartialArgsWriter measures the per-part cost of turning one streamed string
// leaf into argument text: the shape of a long Write/reply argument, which is where the
// feature matters (one ~80 byte stringValue per part).
func BenchmarkGeminiPartialArgsWriter(b *testing.B) {
	piece := strings.Repeat("lorem ipsum \"dolor\" sit amet, 中文 ", 2)
	cont := []*PartialArg{strArg("$.content", piece, true)}
	var w geminiPartialArgsWriter
	buf := make([]byte, 0, 256)
	w.appendPartialArgs(buf[:0], []*PartialArg{strArg("$.file_path", "/tmp/x", false)})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = w.appendPartialArgs(buf[:0], cont)
	}
}

func TestDefaultStreamFunctionCallArguments(t *testing.T) {
	tests := []struct {
		provider schemas.ModelProvider
		model    string
		want     bool
	}{
		{schemas.Vertex, "gemini-3.8-flash", true},
		{schemas.Vertex, "gemini-4-argon", true},
		{schemas.Vertex, "gemini-3-pro-preview", true},
		{schemas.Vertex, "gemini-3.5-flash-lite", true},
		{schemas.Vertex, "google/gemini-3.8-flash", true},
		{schemas.Vertex, "gemini-3.1-flash-lite", false},
		{schemas.Vertex, "gemini-3.1-flash-lite-preview", false},
		{schemas.Vertex, "gemini-2.5-pro", false},
		{schemas.Vertex, "gemini-2.0-flash", false},
		{schemas.Vertex, "claude-sonnet-4-6", false},
		{schemas.Vertex, "gemma-3-27b-it", false},
		{schemas.Gemini, "gemini-3.8-flash", false},
		{schemas.OpenRouter, "google/gemini-3.8-flash", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, DefaultStreamFunctionCallArguments(tt.provider, tt.model), "%s/%s", tt.provider, tt.model)
	}
}

func TestApplyStreamFunctionCallArguments(t *testing.T) {
	declarations := []Tool{{FunctionDeclarations: []*FunctionDeclaration{{Name: "write"}}}}
	yes, no := true, false
	tests := []struct {
		name     string
		req      *GeminiGenerationRequest
		provider schemas.ModelProvider
		model    string
		want     *bool
		wantNil  bool // no functionCallingConfig synthesized
	}{
		{"default on", &GeminiGenerationRequest{Tools: declarations}, schemas.Vertex, "gemini-3.8-flash", &yes, false},
		{"keeps mode", &GeminiGenerationRequest{Tools: declarations, ToolConfig: &ToolConfig{FunctionCallingConfig: &FunctionCallingConfig{Mode: FunctionCallingConfigModeAny}}}, schemas.Vertex, "gemini-4-argon", &yes, false},
		{"explicit false wins over the default", &GeminiGenerationRequest{Tools: declarations, streamFunctionCallArguments: &no}, schemas.Vertex, "gemini-3.8-flash", &no, false},
		{"explicit true outside the default", &GeminiGenerationRequest{Tools: declarations, streamFunctionCallArguments: &yes}, schemas.Gemini, "gemini-3.8-flash", &yes, false},
		{"explicit false outside the default leaves the request alone", &GeminiGenerationRequest{Tools: declarations, streamFunctionCallArguments: &no}, schemas.Vertex, "gemini-2.5-flash", nil, true},
		{"older model", &GeminiGenerationRequest{Tools: declarations}, schemas.Vertex, "gemini-2.5-flash", nil, true},
		{"gemini api", &GeminiGenerationRequest{Tools: declarations}, schemas.Gemini, "gemini-3.8-flash", nil, true},
		{"no tools", &GeminiGenerationRequest{}, schemas.Vertex, "gemini-3.8-flash", nil, true},
		{"tools without declarations", &GeminiGenerationRequest{Tools: []Tool{{GoogleSearch: &GoogleSearch{}}}}, schemas.Vertex, "gemini-3.8-flash", nil, true},
		{"mode none", &GeminiGenerationRequest{Tools: declarations, ToolConfig: &ToolConfig{FunctionCallingConfig: &FunctionCallingConfig{Mode: FunctionCallingConfigModeNone}}}, schemas.Vertex, "gemini-3.8-flash", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ApplyStreamFunctionCallArguments(tt.req, tt.provider, tt.model)
			if tt.wantNil {
				assert.Nil(t, tt.req.ToolConfig, "toolConfig synthesized for a request the flag does not apply to")
				return
			}
			require.NotNil(t, tt.req.ToolConfig)
			require.NotNil(t, tt.req.ToolConfig.FunctionCallingConfig)
			assert.Equal(t, tt.want, tt.req.ToolConfig.FunctionCallingConfig.StreamFunctionCallArguments)
		})
	}
	assert.NotPanics(t, func() { ApplyStreamFunctionCallArguments(nil, schemas.Vertex, "gemini-3.8-flash") })
}

// BenchmarkApplyStreamFunctionCallArguments records the request-side cost: the common case
// (no tools, or a provider/model outside the default) must stay allocation free.
func BenchmarkApplyStreamFunctionCallArguments(b *testing.B) {
	declarations := []Tool{{FunctionDeclarations: []*FunctionDeclaration{{Name: "write"}}}}
	for _, bc := range []struct {
		name     string
		tools    []Tool
		provider schemas.ModelProvider
		model    string
	}{
		{"no tools", nil, schemas.Vertex, "gemini-3.8-flash"},
		{"gemini api", declarations, schemas.Gemini, "gemini-3.8-flash"},
		{"vertex gemini 2.5", declarations, schemas.Vertex, "gemini-2.5-flash"},
		{"vertex gemini 3 applies", declarations, schemas.Vertex, "gemini-3.8-flash"},
	} {
		b.Run(bc.name, func(b *testing.B) {
			config := &FunctionCallingConfig{}
			req := &GeminiGenerationRequest{Tools: bc.tools, ToolConfig: &ToolConfig{FunctionCallingConfig: config}}
			b.ReportAllocs()
			for b.Loop() {
				config.StreamFunctionCallArguments = nil
				ApplyStreamFunctionCallArguments(req, bc.provider, bc.model)
			}
		})
	}
}

// BenchmarkGeminiChatStreamFunctionCallChunk compares the per-chunk conversion cost of a
// streamed-call continuation part with a text part of the same size, both through the
// public chat stream converter.
func BenchmarkGeminiChatStreamFunctionCallChunk(b *testing.B) {
	piece := strings.Repeat("lorem ipsum \"dolor\" sit amet, 中文 ", 2)
	yes := true
	text := &GenerateContentResponse{Candidates: []*Candidate{{Content: &Content{Role: "model", Parts: []*Part{{Text: piece}}}}}}
	cont := &GenerateContentResponse{Candidates: []*Candidate{{Content: &Content{Role: "model", Parts: []*Part{{FunctionCall: &FunctionCall{
		PartialArgs: []*PartialArg{strArg("$.content", piece, true)}, WillContinue: &yes,
	}}}}}}}
	start := &GenerateContentResponse{Candidates: []*Candidate{{Content: &Content{Role: "model", Parts: []*Part{{FunctionCall: &FunctionCall{
		Name: "write", ID: "c1", WillContinue: &yes,
	}}}}}}}
	b.Run("text part", func(b *testing.B) {
		state := NewGeminiStreamState()
		b.ReportAllocs()
		for b.Loop() {
			_, _, _ = text.ToBifrostChatCompletionStream(state)
		}
	})
	b.Run("streamed call continuation", func(b *testing.B) {
		state := NewGeminiStreamState()
		_, _, _ = start.ToBifrostChatCompletionStream(state)
		b.ReportAllocs()
		for b.Loop() {
			_, _, _ = cont.ToBifrostChatCompletionStream(state)
		}
	})
}
