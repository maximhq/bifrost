package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func fp(t *testing.T, path, body string) string {
	t.Helper()
	h, _ := Fingerprint(path, []byte(body))
	return h
}

func TestFingerprintNormalisesVolatileValues(t *testing.T) {
	const path = "/v1/chat/completions"
	cases := []struct {
		name string
		a, b string
	}{
		{"iso timestamp", `{"m":"now is 2026-11-15T10:20:30.123Z ok"}`, `{"m":"now is 2025-01-01T00:00:00Z ok"}`},
		{"timestamp with offset", `{"m":"2026-11-15T10:20:30+05:30"}`, `{"m":"2020-02-02T02:02:02-0700"}`},
		{"timestamp with space", `{"m":"at 2026-11-15 10:20"}`, `{"m":"at 1999-12-31 23:59"}`},
		{"uuid", `{"m":"run 123e4567-e89b-12d3-a456-426614174000"}`, `{"m":"run 9F1C2D3E-AAAA-4BBB-8CCC-DDDDEEEEFFFF"}`},
		{"call id", `{"m":"see call_abc123XYZ"}`, `{"m":"see call_Q9"}`},
		{"toolu id", `{"m":"toolu_01ABC"}`, `{"m":"toolu_zz"}`},
		{"chatcmpl id", `{"m":"chatcmpl_aaa"}`, `{"m":"chatcmpl_bbb"}`},
		{"long number", `{"m":"epoch 1731661234567"}`, `{"m":"epoch 1731669999999"}`},
		{"ten digits", `{"m":"1234567890"}`, `{"m":"9876543210"}`},
		{"dropped keys", `{"model":"x","stream":true,"user":"u1","metadata":{"a":1},"request_id":"r1","stream_options":{"include_usage":true}}`, `{"model":"x"}`},
		{"nested dropped keys", `{"a":{"user":"u1","b":1}}`, `{"a":{"b":1}}`},
		{"key order", `{"model":"x","messages":[{"role":"user","content":"hi"}]}`, `{"messages":[{"content":"hi","role":"user"}],"model":"x"}`},
		{"id fields", `{"messages":[{"id":"msg_1","tool_call_id":"abc"}]}`, `{"messages":[{"id":"msg_2","tool_call_id":"def"}]}`},
		{"whitespace", `{"a": 1,   "b": [1, 2]}`, `{"a":1,"b":[1,2]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if fp(t, path, c.a) != fp(t, path, c.b) {
				t.Fatalf("expected equal fingerprints for %s vs %s", c.a, c.b)
			}
		})
	}
}

func TestFingerprintKeepsMeaningfulDifferences(t *testing.T) {
	const path = "/v1/chat/completions"
	cases := []struct {
		name string
		a, b string
	}{
		{"content", `{"m":"hello"}`, `{"m":"goodbye"}`},
		{"model", `{"model":"a"}`, `{"model":"b"}`},
		{"short number", `{"m":"123456789"}`, `{"m":"123456788"}`},
		{"array order", `{"a":[1,2]}`, `{"a":[2,1]}`},
		{"extra key", `{"a":1}`, `{"a":1,"temperature":0.2}`},
		{"type", `{"a":1}`, `{"a":"1"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if fp(t, path, c.a) == fp(t, path, c.b) {
				t.Fatalf("expected different fingerprints for %s vs %s", c.a, c.b)
			}
		})
	}
	if fp(t, "/v1/chat/completions", `{}`) == fp(t, "/relay/tavily/search", `{}`) {
		t.Fatal("path must be part of the fingerprint")
	}
}

func TestFingerprintNormalisedValue(t *testing.T) {
	h, norm := Fingerprint("/x", []byte(`{"id":"a","user":"u","msg":"call_1 at 2026-01-01T00:00:00Z id 123e4567-e89b-12d3-a456-426614174000 n=12345678901"}`))
	if len(h) != 64 {
		t.Fatalf("want sha256 hex, got %q", h)
	}
	m := norm.(map[string]any)
	if m["id"] != "<id>" {
		t.Fatalf("id not masked: %v", m["id"])
	}
	if _, ok := m["user"]; ok {
		t.Fatal("user not dropped")
	}
	if got, want := m["msg"], "<call> at <ts> id <uuid> n=<n>"; got != want {
		t.Fatalf("msg = %q, want %q", got, want)
	}
}

func TestFingerprintNonJSONBody(t *testing.T) {
	a, na := Fingerprint("/relay/x", []byte("q=at 2026-01-01T00:00:00Z"))
	b, _ := Fingerprint("/relay/x", []byte("q=at 2027-05-05T05:05:05Z"))
	if a != b {
		t.Fatal("non-JSON body should still be normalised")
	}
	if na != "q=at <ts>" {
		t.Fatalf("norm = %v", na)
	}
	// empty body is stable
	if fp(t, "/x", "") != fp(t, "/x", "") {
		t.Fatal("empty body not stable")
	}
}

func norm(t *testing.T, s string) any {
	t.Helper()
	_, n := Fingerprint("/p", []byte(s))
	return n
}

func TestDiffJSONPaths(t *testing.T) {
	a := norm(t, `{"model":"m1","messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}],"temperature":0}`)
	b := norm(t, `{"model":"m2","messages":[{"role":"user","content":"hello"}],"tools":[1]}`)
	d := DiffJSON(a, b)
	got := map[string]FieldDiff{}
	var order []string
	for _, f := range d {
		got[f.Path] = f
		order = append(order, f.Path)
	}
	want := []string{"messages[0].content", "messages[1]", "model", "temperature", "tools"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("paths = %v, want %v", order, want)
	}
	if got["model"].Expected != `"m1"` || got["model"].Got != `"m2"` {
		t.Fatalf("model diff = %+v", got["model"])
	}
	if got["messages[1]"].Got != "null" {
		t.Fatalf("missing element should be null, got %+v", got["messages[1]"])
	}
	if got["tools"].Expected != "null" || got["tools"].Got != "[1]" {
		t.Fatalf("tools diff = %+v", got["tools"])
	}
}

func TestDiffJSONEqualAndTopLevel(t *testing.T) {
	a := norm(t, `{"a":[1,{"b":2}]}`)
	if d := DiffJSON(a, a); len(d) != 0 {
		t.Fatalf("equal values should not diff: %v", d)
	}
	d := DiffJSON("x", "y")
	if len(d) != 1 || d[0].Path != "" {
		t.Fatalf("top-level diff = %+v", d)
	}
	d = DiffJSON(map[string]any{"a": 1.0}, []any{1.0})
	if len(d) != 1 {
		t.Fatalf("type mismatch should give one diff, got %+v", d)
	}
}

func TestDiffJSONCapAndClip(t *testing.T) {
	a := map[string]any{}
	b := map[string]any{}
	for i := 0; i < 60; i++ {
		k := string(rune('A'+i%26)) + string(rune('a'+i/26))
		a[k] = i
		b[k] = i + 1
	}
	if d := DiffJSON(a, b); len(d) != 25 {
		t.Fatalf("diff should be capped at 25, got %d", len(d))
	}
	long := strings.Repeat("x", 1000)
	d := DiffJSON(map[string]any{"s": long}, map[string]any{"s": "y"})
	if len(d) != 1 || !strings.HasSuffix(d[0].Expected, "…") || len(d[0].Expected) > 410 {
		t.Fatalf("long value should be clipped: %d", len(d[0].Expected))
	}
}

func TestRawJSON(t *testing.T) {
	if string(rawJSON(nil)) != "null" {
		t.Fatal("empty should be null")
	}
	src := []byte(`{"a":1}`)
	r := rawJSON(src)
	src[2] = 'b'
	if string(r) != `{"a":1}` {
		t.Fatalf("rawJSON must copy, got %s", r)
	}
	r = rawJSON([]byte("not json"))
	var s string
	if err := json.Unmarshal(r, &s); err != nil || s != "not json" {
		t.Fatalf("invalid JSON should be wrapped as string, got %s", r)
	}
}

func TestModelOfAndSetModel(t *testing.T) {
	if modelOf([]byte(`{"model":"gemini/x"}`)) != "gemini/x" {
		t.Fatal("modelOf")
	}
	if modelOf([]byte(`junk`)) != "" {
		t.Fatal("modelOf on junk should be empty")
	}
	out, err := setModel([]byte(`{"model":"a","messages":[1]}`), "groq/b")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["model"] != "groq/b" || m["messages"] == nil {
		t.Fatalf("setModel = %s", out)
	}
	if _, err := setModel([]byte(`junk`), "x"); err == nil {
		t.Fatal("setModel should fail on invalid JSON")
	}
}

func TestPoisonSearch(t *testing.T) {
	resp := json.RawMessage(`{"query":"q","results":[{"title":"t","content":"orig"},{"content":"second"}]}`)
	out, err := poisonSearch(resp, "IGNORE PREVIOUS")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Query   string `json:"query"`
		Results []struct {
			Title   string `json:"title"`
			Content string `json:"content"`
		} `json:"results"`
	}
	_ = json.Unmarshal(out, &m)
	if m.Results[0].Content != "orig\n\nIGNORE PREVIOUS" || m.Results[0].Title != "t" {
		t.Fatalf("first result = %+v", m.Results[0])
	}
	if m.Results[1].Content != "second" || m.Query != "q" {
		t.Fatal("other fields must be untouched")
	}
	if out, err := poisonSearch(json.RawMessage(`{"results":[{}]}`), "x"); err != nil || !strings.Contains(string(out), `"content":"\n\nx"`) {
		t.Fatalf("missing content: %s %v", out, err)
	}
	for _, bad := range []string{`{"results":[]}`, `{}`, `{"results":["s"]}`, `junk`} {
		if _, err := poisonSearch(json.RawMessage(bad), "x"); err == nil {
			t.Fatalf("expected error for %s", bad)
		}
	}
}
