package anthropic

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
)

func TestAnthropicMessageRequestUnmarshalJSONExtraParams(t *testing.T) {
	var req AnthropicMessageRequest
	err := sonic.Unmarshal([]byte(`{
		"model":"claude-sonnet-4-20250514",
		"max_tokens":1024,
		"messages":[{"role":"user","content":"hello"}],
		"temperature":0.2,
		"fallback_credit_token":"credit_opaque",
		"output_format": { "type": "json_schema", "schema": { "z": 1, "a": 2 } },
		"output_config": { "format": { "type": "json_schema", "schema": { "z": 1, "a": 2 } } },
		"client_scalar":true,
		"client_object": { "z": 1, "a": [ 2, { "b": "c" } ] },
		"client_array":["a",2],
		"client_null":null,
		"include":["web_search_call.action.sources"],
		"reasoning_summary":"concise",
		"extra_params":{"must_not":"be preserved"}
	}`), &req)
	if err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	if req.Temperature == nil || *req.Temperature != 0.2 {
		t.Fatalf("temperature = %v, want 0.2", req.Temperature)
	}
	if req.FallbackCreditToken == nil || *req.FallbackCreditToken != "credit_opaque" {
		t.Fatalf("fallback credit token = %v, want credit_opaque", req.FallbackCreditToken)
	}

	assertRawExtraParam(t, req.ExtraParams, "client_scalar", `true`)
	assertRawExtraParam(t, req.ExtraParams, "client_object", `{"z":1,"a":[2,{"b":"c"}]}`)
	assertRawExtraParam(t, req.ExtraParams, "client_array", `["a",2]`)
	assertRawExtraParam(t, req.ExtraParams, "client_null", `null`)
	assertRawExtraParam(t, req.ExtraParams, "include", `["web_search_call.action.sources"]`)
	assertRawExtraParam(t, req.ExtraParams, "reasoning_summary", `"concise"`)

	for _, key := range []string{"model", "max_tokens", "messages", "temperature", "fallback_credit_token", "output_format", "output_config", "extra_params"} {
		if _, ok := req.ExtraParams[key]; ok {
			t.Errorf("known field %q must not be copied to ExtraParams", key)
		}
	}

	if got, want := string(req.OutputFormat), `{"type":"json_schema","schema":{"z":1,"a":2}}`; got != want {
		t.Errorf("output_format = %s, want %s", got, want)
	}
	if req.OutputConfig == nil {
		t.Fatal("output_config is nil")
	}
	if got, want := string(req.OutputConfig.Format), `{"type":"json_schema","schema":{"z":1,"a":2}}`; got != want {
		t.Errorf("output_config.format = %s, want %s", got, want)
	}
}

func TestAnthropicMessageRequestUnmarshalJSONDuplicateAndReuseSemantics(t *testing.T) {
	req := AnthropicMessageRequest{ExtraParams: map[string]interface{}{"existing": "preserved"}}
	err := sonic.Unmarshal([]byte(`{
		"model":"claude-sonnet-4-20250514",
		"max_tokens":1024,
		"messages":[{"role":"user","content":"hello"}],
		"client_extension":"first",
		"client_extension":{"last":true}
	}`), &req)
	if err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	if got := req.ExtraParams["existing"]; got != "preserved" {
		t.Errorf("existing extra param = %#v, want preserved", got)
	}
	assertRawExtraParam(t, req.ExtraParams, "client_extension", `{"last":true}`)
}

func TestAnthropicMessageRequestUnmarshalJSONMalformedInput(t *testing.T) {
	var req AnthropicMessageRequest
	if err := sonic.Unmarshal([]byte(`{"model":"claude-sonnet-4-20250514"`), &req); err == nil {
		t.Fatal("expected malformed JSON to return an error")
	}
}

func assertRawExtraParam(t *testing.T, params map[string]interface{}, key, want string) {
	t.Helper()
	raw, ok := params[key].(json.RawMessage)
	if !ok {
		t.Fatalf("ExtraParams[%q] = %#v (%T), want json.RawMessage", key, params[key], params[key])
	}
	if got := string(raw); got != want {
		t.Errorf("ExtraParams[%q] = %s, want %s", key, got, want)
	}
}

var (
	benchmarkAnthropicMinimalRequest      = []byte(`{"model":"claude-sonnet-4-20250514","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`)
	benchmarkAnthropicLargeKnownRequest   = makeAnthropicBenchmarkRequest(false)
	benchmarkAnthropicLargeUnknownRequest = makeAnthropicBenchmarkRequest(true)
)

func makeAnthropicBenchmarkRequest(includeUnknown bool) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"claude-sonnet-4-20250514","max_tokens":4096,"stream":true,"messages":[`)
	content := strings.Repeat("x", 16*1024)
	for i := 0; i < 12; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"role":"user","content":"`)
		b.WriteString(content)
		b.WriteString(`"}`)
	}
	b.WriteString(`],"tools":[`)
	for tool := 0; tool < 8; tool++ {
		if tool > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"name":"tool","description":"benchmark tool","input_schema":{"type":"object","properties":{`)
		for property := 0; property < 80; property++ {
			if property > 0 {
				b.WriteByte(',')
			}
			b.WriteString(`"property":{"type":"string","description":"`)
			b.WriteString(strings.Repeat("schema", 20))
			b.WriteString(`"}`)
		}
		b.WriteString(`},"required":["property"]}}`)
	}
	b.WriteString(`]`)
	if includeUnknown {
		b.WriteString(`,"client_extensions":{"nested":{"payload":"`)
		b.WriteString(strings.Repeat("u", 128*1024))
		b.WriteString(`"}}`)
	}
	b.WriteByte('}')
	return []byte(b.String())
}

func BenchmarkAnthropicMessageRequestUnmarshal(b *testing.B) {
	cases := []struct {
		name string
		body []byte
	}{
		{name: "minimal", body: benchmarkAnthropicMinimalRequest},
		{name: "large_known_fields_only", body: benchmarkAnthropicLargeKnownRequest},
		{name: "large_with_unknown_field", body: benchmarkAnthropicLargeUnknownRequest},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.body)))
			for b.Loop() {
				var req AnthropicMessageRequest
				if err := sonic.Unmarshal(tc.body, &req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkAnthropicMessageRequestRawFieldExtraction(b *testing.B) {
	cases := []struct {
		name string
		body []byte
	}{
		{name: "large_known_fields_only", body: benchmarkAnthropicLargeKnownRequest},
		{name: "large_with_unknown_field", body: benchmarkAnthropicLargeUnknownRequest},
	}

	for _, tc := range cases {
		b.Run("current_map/"+tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.body)))
			for b.Loop() {
				var fields map[string]json.RawMessage
				if err := sonic.Unmarshal(tc.body, &fields); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("proposed_scan/"+tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.body)))
			for b.Loop() {
				var req AnthropicMessageRequest
				extractAnthropicMessageRequestUnknownFields(tc.body, &req)
			}
		})
	}
}

func TestAnthropicMessageRequestBenchmarkPayloadsAreValid(t *testing.T) {
	for _, body := range [][]byte{benchmarkAnthropicMinimalRequest, benchmarkAnthropicLargeKnownRequest, benchmarkAnthropicLargeUnknownRequest} {
		if !json.Valid(body) {
			t.Fatal("benchmark payload is invalid JSON")
		}
		if bytes.Contains(body, []byte("\n")) {
			t.Fatal("benchmark payload unexpectedly contains a newline")
		}
	}
}
