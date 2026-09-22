package handlers

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/valyala/fasthttp"
)

type loginDecodeConfigStore struct {
	configstore.ConfigStore
}

func TestSessionLoginInvalidPayloadDoesNotExposeDecoderDetails(t *testing.T) {
	h := &SessionHandler{configStore: &loginDecodeConfigStore{}}
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"username":1234,"password":"Suresh"}`)

	h.login(ctx)

	body := string(ctx.Response.Body())
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", ctx.Response.StatusCode(), fasthttp.StatusBadRequest, body)
	}
	if !strings.Contains(body, "Invalid request payload") {
		t.Fatalf("body = %s, want generic invalid payload message", body)
	}
	if strings.Contains(body, "cannot unmarshal") || strings.Contains(body, "username") || strings.Contains(body, "Go struct field") {
		t.Fatalf("body exposes decoder internals: %s", body)
	}
}

func TestPrepareRequestInvalidPayloadDoesNotExposeDecoderDetails(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"model":1234,"prompt":"hello"}`)

	_, _, err := prepareRequest[TextRequest](ctx, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid payload")
	}
	msg := err.Error()
	if msg != "Invalid request payload" {
		t.Fatalf("error = %q, want generic invalid payload message", msg)
	}
	if strings.Contains(msg, "cannot unmarshal") || strings.Contains(msg, "model") || strings.Contains(msg, "Go struct field") {
		t.Fatalf("error exposes decoder internals: %s", msg)
	}
}

// decodeRequest exercises the production prepareRequest path without extracting extras.
func decodeRequest[T baseRequest](t *testing.T, body []byte) *T {
	t.Helper()
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBody(body)
	req, _, err := prepareRequest[T](ctx, nil, nil)
	if err != nil {
		t.Fatalf("prepareRequest: %v", err)
	}
	return req
}

// TestResponsesRequestDecodeAllSections guards the plain-struct decoding of
// ResponsesRequest: Go promotes UnmarshalJSON from embedded types, so
// if ResponsesParameters ever gains a custom UnmarshalJSON, sonic would call
// the promoted method and silently drop Input and BifrostParams. This test
// fails loudly if that happens. (ChatRequest hit exactly this: ChatParameters
// has a custom unmarshaller, hence ChatRequest keeps its explicit one.)
func TestResponsesRequestDecodeAllSections(t *testing.T) {
	body := []byte(`{
		"model": "openai/gpt-4o",
		"stream": true,
		"fallbacks": ["anthropic/claude-sonnet-5"],
		"input": [{"role": "user", "content": "hello"}],
		"temperature": 0.5,
		"max_output_tokens": 128
	}`)
	req := decodeRequest[ResponsesRequest](t, body)

	if req.Model != "openai/gpt-4o" {
		t.Errorf("BifrostParams.Model lost: got %q", req.Model)
	}
	if req.Stream == nil || !*req.Stream {
		t.Errorf("BifrostParams.Stream lost")
	}
	if len(req.Fallbacks) != 1 {
		t.Errorf("BifrostParams.Fallbacks lost: got %v", req.Fallbacks)
	}
	if len(req.Input.ResponsesRequestInputArray) != 1 {
		t.Fatalf("Input lost: got %d messages", len(req.Input.ResponsesRequestInputArray))
	}
	if req.Temperature == nil || *req.Temperature != 0.5 {
		t.Errorf("ResponsesParameters.Temperature lost")
	}
	if req.MaxOutputTokens == nil || *req.MaxOutputTokens != 128 {
		t.Errorf("ResponsesParameters.MaxOutputTokens lost")
	}
}

// TestResponsesRequestParamsNonNilWithoutParamFields pins the core request's
// non-nil Params invariant even when the body carries no parameter fields.
func TestResponsesRequestParamsNonNilWithoutParamFields(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"model":"openai/gpt-4o","input":"hi"}`)
	req, coreReq, err := prepareResponsesRequest(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if coreReq.Params == nil {
		t.Fatal("Params is nil for a body without parameter fields")
	}
	if req.Input.ResponsesRequestInputStr == nil || *req.Input.ResponsesRequestInputStr != "hi" {
		t.Errorf("string-form input lost")
	}
}

func TestPrepareResponsesRequestNullClearsParameters(t *testing.T) {
	for _, tt := range []struct {
		field string
		value string
	}{
		{field: "temperature", value: `0.7`},
		{field: "max_output_tokens", value: `128`},
		{field: "instructions", value: `"previous instructions"`},
		{field: "metadata", value: `{"tag":"previous"}`},
		{field: "include", value: `["file_search_call.results"]`},
		{field: "tools", value: `[{"type":"function","name":"test_fn","parameters":{"type":"object","properties":{}}}]`},
		{field: "tool_choice", value: `"auto"`},
		{field: "reasoning", value: `{"effort":"low"}`},
		{field: "store", value: `true`},
	} {
		for _, key := range []string{tt.field, strings.ToUpper(tt.field)} {
			t.Run(key, func(t *testing.T) {
				ctx := &fasthttp.RequestCtx{}
				ctx.Request.SetBodyString(`{"model":"openai/gpt-4o","input":"hi","` + tt.field + `":` + tt.value + `,"` + key + `":null}`)
				_, req, err := prepareResponsesRequest(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				// Check both the internal parameters and the provider's typed
				// request, before ExtraParams can mask a stale parameter.
				for _, value := range []any{req.Params, openai.ToOpenAIResponsesRequest(nil, req)} {
					body, err := providerUtils.MarshalSorted(value)
					if err != nil {
						t.Fatal(err)
					}
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(body, &fields); err != nil {
						t.Fatal(err)
					}
					if _, ok := fields[tt.field]; ok {
						t.Errorf("null must clear %s in %T: %s", tt.field, value, body)
					}
				}
			})
		}
	}
}

func TestPrepareResponsesRequestPreservesRawNull(t *testing.T) {
	for _, fields := range []string{
		`"context_management":null`,
		`"context_management":[],"context_management":null`,
		`"context_management":[],"CONTEXT_MANAGEMENT":null`,
		`"c\u006fntext_management":null`,
	} {
		t.Run(fields, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetBodyString(`{"model":"openai/gpt-4o","input":"hi",` + fields + `}`)
			_, req, err := prepareResponsesRequest(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(req.Params.ContextManagement); got != "null" {
				t.Errorf("context_management = %q, want raw null", got)
			}
		})
	}
}

// TestChatRequestDecodeAllSections documents why ChatRequest keeps its custom
// UnmarshalJSON: ChatParameters has one, and dropping ChatRequest's would let
// the promoted method swallow Messages and BifrostParams.
func TestChatRequestDecodeAllSections(t *testing.T) {
	body := []byte(`{
		"model": "openai/gpt-4o",
		"stream": true,
		"messages": [{"role": "user", "content": "hi"}],
		"temperature": 0.5
	}`)
	req := decodeRequest[ChatRequest](t, body)

	if req.Model != "openai/gpt-4o" {
		t.Errorf("BifrostParams.Model lost: got %q", req.Model)
	}
	if len(req.Messages) != 1 {
		t.Errorf("Messages lost: got %d", len(req.Messages))
	}
	if req.ChatParameters == nil || req.Temperature == nil || *req.Temperature != 0.5 {
		t.Errorf("ChatParameters.Temperature lost")
	}
}

func TestResponsesRequestInputUnmarshalJSONBoundaries(t *testing.T) {
	tests := []struct {
		name string
		data string
		want ResponsesRequestInput
	}{
		{name: "empty string", data: `""`, want: ResponsesRequestInput{ResponsesRequestInputStr: schemas.Ptr("")}},
		{name: "string with whitespace", data: " \t\r\n\"hello\" \t\r\n", want: ResponsesRequestInput{ResponsesRequestInputStr: schemas.Ptr("hello")}},
		{name: "escaped string", data: `"line\n\"quote\"\u4e16\u754c"`, want: ResponsesRequestInput{ResponsesRequestInputStr: schemas.Ptr("line\n\"quote\"\u4e16\u754c")}},
		{name: "null", data: " \t\r\nnull \t\r\n", want: ResponsesRequestInput{ResponsesRequestInputStr: schemas.Ptr("")}},
		{name: "empty array", data: " \t\r\n[] \t\r\n", want: ResponsesRequestInput{ResponsesRequestInputArray: []schemas.ResponsesMessage{}}},
		{
			name: "message array",
			data: `[{"role":"user","content":"hello"}]`,
			want: ResponsesRequestInput{ResponsesRequestInputArray: []schemas.ResponsesMessage{
				{Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser), Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")}},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Successful input decoding replaces both union fields on reuse.
			got := ResponsesRequestInput{
				ResponsesRequestInputStr:   schemas.Ptr("previous"),
				ResponsesRequestInputArray: []schemas.ResponsesMessage{{ID: schemas.Ptr("old")}},
			}
			if err := got.UnmarshalJSON([]byte(tt.data)); err != nil {
				t.Fatalf("UnmarshalJSON: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("input = %#v, want %#v", got, tt.want)
			}
		})
	}

	invalid := []struct {
		name string
		data string
	}{
		{name: "empty", data: ""},
		{name: "whitespace only", data: " \t\r\n"},
		{name: "object", data: `{}`},
		{name: "number", data: `123`},
		{name: "boolean", data: `true`},
		{name: "non JSON whitespace", data: "\vnull"},
		{name: "truncated null", data: `nul`},
		{name: "truncated string", data: `"unterminated`},
		{name: "invalid escape", data: `"\q"`},
		{name: "truncated array", data: `[`},
		{name: "invalid array item", data: `[{"role":"user","content":"new"},42]`},
		{name: "invalid content", data: `[{"role":"user","content":42}]`},
		{name: "trailing comma", data: `[{},]`},
		{name: "trailing value", data: `"text" false`},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			before := ResponsesRequestInput{ResponsesRequestInputStr: schemas.Ptr("previous")}
			got := before
			err := got.UnmarshalJSON([]byte(tt.data))
			const wantError = "invalid responses request input"
			if err == nil || err.Error() != wantError {
				t.Fatalf("error = %v, want %q", err, wantError)
			}
			if !reflect.DeepEqual(got, before) {
				t.Fatalf("failed decode changed receiver: %#v", got)
			}
		})
	}
}

func TestPrepareResponsesRequestInvalidPayload(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "invalid input type", body: `{"model":"openai/gpt-4o","input":42}`},
		{name: "invalid content type", body: `{"model":"openai/gpt-4o","input":[{"role":"user","content":{}}]}`},
		{name: "malformed input", body: `{"model":"openai/gpt-4o","input":[}`},
		{name: "invalid parameter", body: `{"model":"openai/gpt-4o","input":"hello","temperature":"invalid"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetBodyString(tt.body)
			_, _, err := prepareResponsesRequest(ctx, nil)
			if err == nil || err.Error() != "Invalid request payload" {
				t.Fatalf("error = %v, want generic invalid payload error", err)
			}
		})
	}
}

// TestResponsesRequestNullInputBehavior pins that "input": null keeps decoding
// to an empty string rather than erroring. The peek-first union decoder has no
// case for 'n', so null must be handled explicitly or a client sending it gets
// a 400 where it previously got a synthetic empty user message.
func TestResponsesRequestNullInputBehavior(t *testing.T) {
	req := decodeRequest[ResponsesRequest](t, []byte(`{"model":"openai/gpt-4o","input":null}`))

	if req.Input.ResponsesRequestInputStr == nil {
		t.Fatal(`"input": null must decode to an empty string, not leave both union fields nil`)
	}
	if *req.Input.ResponsesRequestInputStr != "" {
		t.Errorf(`"input": null decoded to %q, want ""`, *req.Input.ResponsesRequestInputStr)
	}
}

// TestResponsesMessageContentNullBehavior pins that "content": null decodes to
// an empty string. OpenAI emits null content on assistant turns that carry only
// tool calls, so erroring here would drop valid provider responses.
func TestResponsesMessageContentNullBehavior(t *testing.T) {
	var c schemas.ResponsesMessageContent
	if err := sonic.Unmarshal([]byte(`null`), &c); err != nil {
		t.Fatalf(`"content": null must not error: %v`, err)
	}
	if c.ContentStr == nil {
		t.Fatal(`"content": null must decode to an empty string, not leave both union fields nil`)
	}
	if *c.ContentStr != "" {
		t.Errorf(`"content": null decoded to %q, want ""`, *c.ContentStr)
	}
}

func TestExtractExtraParamsOwnsReturnedData(t *testing.T) {
	tests := []struct {
		name string
		body string
		want map[string]any
	}{
		{
			name: "known fields only",
			body: `{"input":[{"content":"hello"}]}`,
			want: map[string]any{},
		},
		{
			name: "surrounding JSON whitespace",
			body: " \t\r\n" + `{"input":[],"extra":"value"}` + " \t\r\n",
			want: map[string]any{"extra": "value"},
		},
		{
			name: "nested values and escaped keys",
			body: `{"input":[],"plain":"value","escaped\u005fkey":"line\nbreak","nested":{"items":["hello",{"text":"world"}]},"nullable":null,"number":1.5,"enabled":true}`,
			want: map[string]any{
				"plain":       "value",
				"escaped_key": "line\nbreak",
				"nested":      map[string]any{"items": []any{"hello", map[string]any{"text": "world"}}},
				"nullable":    nil,
				"number":      float64(1.5),
				"enabled":     true,
			},
		},
		{
			name: "last duplicate key wins",
			body: `{"input":[],"extra":"first","ex\u0074ra":{"value":"last"}}`,
			want: map[string]any{"extra": map[string]any{"value": "last"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			extras, err := extractExtraParams(body, map[string]bool{"input": true})
			if err != nil {
				t.Fatalf("extractExtraParams: %v", err)
			}
			if string(body) != tt.body {
				t.Fatal("extractExtraParams modified the request body")
			}
			// FastHTTP reuses its body buffer after the handler returns. Every
			// returned key and nested string must survive that reuse.
			for i := range body {
				body[i] = 'X'
			}
			if !reflect.DeepEqual(extras, tt.want) {
				t.Fatalf("extras after body reuse = %#v, want %#v", extras, tt.want)
			}
		})
	}
}

func TestExtractExtraParamsRejectsMalformedJSON(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "empty body", body: ""},
		{name: "truncated object", body: `{"input":"hello"`},
		{name: "invalid known value", body: `{"input":[}`},
		{name: "invalid extra", body: `{"input":"hello","extra":tru}`},
		{name: "trailing comma", body: `{"input":"hello",}`},
		{name: "trailing value", body: `{"input":"hello"} {}`},
		{name: "trailing non JSON whitespace", body: "{\"input\":\"hello\"}\v"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := extractExtraParams([]byte(tt.body), responsesParamsKnownFields); err == nil {
				t.Fatal("expected malformed JSON to fail")
			}
		})
	}
}

func TestExtractExtraParamsInvalidValuesMatchDecoder(t *testing.T) {
	invalidValues := []struct {
		name  string
		value string
	}{
		{name: "invalid escape", value: `"\q"`},
		{name: "invalid unicode escape", value: `"\u12xz"`},
		{name: "short unicode escape", value: `"\u123"`},
		{name: "nested invalid escape", value: `{"items":["\q"]}`},
		{name: "nested invalid key", value: `{"bad\q":true}`},
		{name: "positive overflow", value: `1e1000`},
		{name: "negative overflow", value: `-1e1000`},
		{name: "float64 boundary overflow", value: `1.7976931348623159e308`},
		{name: "integer overflow", value: strings.Repeat("9", 309)},
		{name: "nested overflow", value: `{"items":[1e1000]}`},
		{name: "overwritten nested overflow", value: `{"value":1e1000,"value":1}`},
	}
	for _, tt := range invalidValues {
		for _, field := range []string{"input", "extra"} {
			for _, overwrite := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/overwrite=%t", tt.name, field, overwrite), func(t *testing.T) {
					body := `{"valid_extra":"keep","` + field + `":` + tt.value
					if overwrite {
						body += `,"` + field + `":null`
					}
					body += `}`
					// Sonic's raw-value validation differs between decoder backends.
					// Preserve that behavior instead of adding a second, stricter parse.
					var raw map[string]json.RawMessage
					decodeErr := sonic.Unmarshal([]byte(body), &raw)
					extras, err := extractExtraParams([]byte(body), map[string]bool{"input": true})
					if (err == nil) != (decodeErr == nil) {
						t.Fatalf("error = %v, decoder error = %v", err, decodeErr)
					}
					if err != nil {
						if extras != nil {
							t.Fatalf("returned partial extras on failure: %#v", extras)
						}
						return
					}
					want := map[string]any{"valid_extra": "keep"}
					if field == "extra" {
						var value any
						if sonic.Unmarshal(raw[field], &value) == nil {
							want[field] = value
						}
					}
					if !reflect.DeepEqual(extras, want) {
						t.Fatalf("extras = %#v, want %#v", extras, want)
					}
				})
			}
		}
	}
	for _, tt := range []struct{ name, body string }{
		{name: "invalid top-level key", body: `{"valid_extra":1,"bad\q":2}`},
		{name: "array root", body: `[]`},
		{name: "string root", body: `"hello"`},
		{name: "number root", body: `123`},
		{name: "boolean root", body: `true`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if extras, err := extractExtraParams([]byte(tt.body), nil); err == nil || extras != nil {
				t.Fatalf("extras = %#v, error = %v; want nil extras and an error", extras, err)
			}
		})
	}
}

func TestExtractExtraParamsValidScalarBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value string
		want  any
	}{
		{name: "all escapes", value: `"\"\\\/\b\f\n\r\t\u4e16\u754c"`, want: "\"\\/\b\f\n\r\t世界"},
		{name: "surrogate pair", value: `"\ud83d\ude00"`, want: "😀"},
		{name: "unpaired surrogate", value: `"\ud800"`, want: "\ufffd"},
		{name: "numeric string", value: `"1e1000"`, want: "1e1000"},
		{name: "escaped backslash", value: `"\\q"`, want: `\q`},
		{name: "float64 max", value: `1.7976931348623157e308`, want: float64(1.7976931348623157e308)},
		{name: "negative float64 max", value: `-1.7976931348623157e308`, want: float64(-1.7976931348623157e308)},
		{name: "above int64 max", value: `9223372036854775808`, want: float64(9223372036854775808)},
		{name: "underflow", value: `1e-1000`, want: float64(0)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"input":` + tt.value + `,"extra":` + tt.value + `}`)
			extras, err := extractExtraParams(body, map[string]bool{"input": true})
			if err != nil {
				t.Fatal(err)
			}
			if want := map[string]any{"extra": tt.want}; !reflect.DeepEqual(extras, want) {
				t.Fatalf("extras = %#v, want %#v", extras, want)
			}
		})
	}
	for _, body := range []string{`{}`, `null`} {
		t.Run(body, func(t *testing.T) {
			extras, err := extractExtraParams([]byte(body), nil)
			if err != nil || len(extras) != 0 {
				t.Fatalf("extras = %#v, error = %v; want no extras and no error", extras, err)
			}
		})
	}
}

func benchmarkRequestBody(b *testing.B, fields map[string]any, extraCount int) string {
	b.Helper()
	fields = maps.Clone(fields)
	for i := range extraCount {
		fields[fmt.Sprintf("custom_%d", i)] = map[string]any{
			"enabled": true,
			"values":  []any{1, "two", nil},
		}
	}
	body, err := schemas.MarshalSorted(fields)
	if err != nil {
		b.Fatal(err)
	}
	return string(body)
}

// BenchmarkPrepareSmallRequest exercises the shared prepareRequest path with
// ordinary text payloads and varying numbers of provider-specific parameters.
func BenchmarkPrepareSmallRequest(b *testing.B) {
	for _, extraCount := range []int{0, 1, 32} {
		b.Run(fmt.Sprintf("Responses/extras=%d", extraCount), func(b *testing.B) {
			body := benchmarkRequestBody(b, map[string]any{
				"model": "openai/gpt-4o", "input": "hello", "temperature": 0.7,
			}, extraCount)
			benchmarkPrepareRequest[ResponsesRequest](b, body, responsesParamsKnownFields, extraCount)
		})
		b.Run(fmt.Sprintf("ResponsesArray/extras=%d", extraCount), func(b *testing.B) {
			body := benchmarkRequestBody(b, map[string]any{
				"model": "openai/gpt-4o",
				"input": []map[string]any{{"role": "user", "content": []map[string]any{
					{"type": "input_text", "text": "hello"},
				}}},
				"temperature": 0.7,
			}, extraCount)
			benchmarkPrepareRequest[ResponsesRequest](b, body, responsesParamsKnownFields, extraCount)
		})
		b.Run(fmt.Sprintf("Chat/extras=%d", extraCount), func(b *testing.B) {
			body := benchmarkRequestBody(b, map[string]any{
				"model":       "openai/gpt-4o",
				"messages":    []map[string]any{{"role": "user", "content": "hello"}},
				"temperature": 0.7,
			}, extraCount)
			benchmarkPrepareRequest[ChatRequest](b, body, chatParamsKnownFields, extraCount)
		})
		b.Run(fmt.Sprintf("Embedding/extras=%d", extraCount), func(b *testing.B) {
			body := benchmarkRequestBody(b, map[string]any{
				"model": "openai/text-embedding-3-small", "input": "hello", "dimensions": 256,
			}, extraCount)
			benchmarkPrepareRequest[EmbeddingRequest](b, body, embeddingParamsKnownFields, extraCount)
		})
	}
}

func benchmarkPrepareRequest[T baseRequest](b *testing.B, body string, known map[string]bool, extraCount int) {
	b.Helper()
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(body)
	// Warm the decoder and verify the fixture before timing.
	_, base, err := prepareRequest[T](ctx, nil, known)
	if err != nil {
		b.Fatal(err)
	}
	if base.Provider != schemas.OpenAI || len(base.ExtraParams) != extraCount {
		b.Fatalf("unexpected decoded request: %#v", base)
	}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := prepareRequest[T](ctx, nil, known); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkExtractExtraParams isolates the shared extractor from typed decoding.
func BenchmarkExtractExtraParams(b *testing.B) {
	for _, tt := range []struct {
		name       string
		fields     map[string]any
		extraCount int
	}{
		{name: "knownOnly", fields: map[string]any{"model": "openai/gpt-4o", "input": "hello", "temperature": 0.7}},
		{name: "oneExtra", fields: map[string]any{"input": "hello"}, extraCount: 1},
		{name: "manyExtras", fields: map[string]any{"input": "hello"}, extraCount: 32},
		{name: "largeKnown", fields: map[string]any{"input": strings.Repeat("A", 1<<20)}, extraCount: 1},
		{name: "largeExtra", fields: map[string]any{"input": "hello", "large": strings.Repeat("A", 1<<20)}},
	} {
		b.Run(tt.name, func(b *testing.B) {
			body := []byte(benchmarkRequestBody(b, tt.fields, tt.extraCount))
			wantCount := tt.extraCount
			if _, ok := tt.fields["large"]; ok {
				wantCount++
			}
			extras, err := extractExtraParams(body, responsesParamsKnownFields)
			if err != nil {
				b.Fatal(err)
			}
			if len(extras) != wantCount {
				b.Fatalf("extra count = %d, want %d", len(extras), wantCount)
			}
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := extractExtraParams(body, responsesParamsKnownFields); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkExtractExtraParamsNestedKnown covers small conversations where
// materializing known message objects can outweigh the cost of raw decoding.
func BenchmarkExtractExtraParamsNestedKnown(b *testing.B) {
	for _, count := range []int{1, 8, 32} {
		b.Run(fmt.Sprintf("messages=%d", count), func(b *testing.B) {
			message := `{"role":"user","content":[{"type":"input_text","text":"hello"}]}`
			body := []byte(`{"input":[` + strings.TrimSuffix(strings.Repeat(message+",", count), ",") + `]}`)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				extras, err := extractExtraParams(body, responsesParamsKnownFields)
				if err != nil {
					b.Fatal(err)
				}
				if len(extras) != 0 {
					b.Fatalf("unexpected extras: %#v", extras)
				}
			}
		})
	}
}

// buildResponsesBody returns a Responses API JSON body of roughly targetMiB
// mebibytes, shaped like a real multimodal conversation: an input array whose
// bulk is base64-ish image data — the common trigger for large request bodies.
func buildResponsesBody(targetMiB int) string {
	const chunkSize = 64 * 1024
	blob := strings.Repeat("A", chunkSize)
	messages := targetMiB * 1024 * 1024 / chunkSize

	var b strings.Builder
	b.WriteString(`{"model":"openai/gpt-4o","stream":false,"temperature":0.7,"input":[`)
	for i := 0; i < messages; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b,
			`{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,%s"}]}`,
			blob,
		)
	}
	b.WriteString(`],"unknown_passthrough_field":{"nested":"value"}}`)
	return b.String()
}

// BenchmarkResponsesRequestDecode measures per-request allocation for the
// full prepareRequest path, which includes unmarshal + model resolution +
// extra-param extraction. B/op measures cumulative allocations, not retained
// heap or peak memory.
func BenchmarkResponsesRequestDecode(b *testing.B) {
	for _, sizeMiB := range []int{1, 8, 32} {
		body := buildResponsesBody(sizeMiB)

		// Build the request once outside the timed loop: SetBodyString copies the
		// whole body, which would otherwise dominate the reported B/op.
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetBodyString(body)

		b.Run(fmt.Sprintf("body=%dMiB", sizeMiB), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				_, _, err := prepareRequest[ResponsesRequest](ctx, nil, responsesParamsKnownFields)
				if err != nil {
					b.Fatalf("prepareRequest: %v", err)
				}
			}
		})
	}
}

// BenchmarkSingleFullBodyParse measures decoding the same bodies into a raw
// field map. It omits typed and nested decoding, model resolution, and extra
// parameter extraction, so allocation ratios do not indicate parse counts.
func BenchmarkSingleFullBodyParse(b *testing.B) {
	for _, sizeMiB := range []int{1, 8, 32} {
		// Convert once outside the timed loop: []byte(body) copies the whole
		// body, which would otherwise be measured on every iteration.
		bodyBytes := []byte(buildResponsesBody(sizeMiB))

		b.Run(fmt.Sprintf("body=%dMiB", sizeMiB), func(b *testing.B) {
			b.SetBytes(int64(len(bodyBytes)))
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				var raw map[string]sonic.NoCopyRawMessage
				if err := sonic.Unmarshal(bodyBytes, &raw); err != nil {
					b.Fatalf("unmarshal: %v", err)
				}
			}
		})
	}
}
