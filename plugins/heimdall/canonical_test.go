package heimdall

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestCanonicalArgs_KeyOrder(t *testing.T) {
	a := CanonicalArgs(`{"b": 1, "a": {"y": true, "x": [1, 2]}}`, nil)
	b := CanonicalArgs("{\"a\":{\"x\":[1,2],\"y\":true},\n\"b\":1}", nil)
	if a != b {
		t.Errorf("key order or whitespace changed the result:\n%s\n%s", a, b)
	}
	if want := `{"a":{"x":[1,2],"y":true},"b":1}`; a != want {
		t.Errorf("canonical form = %s, want %s", a, want)
	}
	if got := CanonicalArgs(`{"q":"a<b&c"}`, nil); got != `{"q":"a<b&c"}` {
		t.Errorf("text was escaped: %s", got)
	}
	if CanonicalArgs(`[1,2]`, nil) == CanonicalArgs(`[2,1]`, nil) {
		t.Error("array order must matter")
	}
}

func TestCanonicalArgs_Numbers(t *testing.T) {
	if CanonicalArgs(`{"n":1}`, nil) == CanonicalArgs(`{"n":1.0}`, nil) {
		t.Error("1 and 1.0 must stay distinct")
	}
	big := `{"id":12345678901234567890123}`
	if got := CanonicalArgs(big, nil); got != big {
		t.Errorf("a large number was rounded: %s", got)
	}
	if got := CanonicalArgs(`{"n":1e3}`, nil); got != `{"n":1e3}` {
		t.Errorf("number text changed: %s", got)
	}
}

func TestCanonicalArgs_InvalidJSON(t *testing.T) {
	cases := map[string]string{
		"  not json \n":        "not json",
		`{"a":1} trailing`:     `{"a":1} trailing`,
		`{"a":1}{"b":2}`:       `{"a":1}{"b":2}`,
		`{"a":`:                `{"a":`,
		"":                     "",
		"   ":                  "",
		"\x00\xff{broken":      "\x00\xff{broken",
		strings.Repeat("[", 5): "[[[[[",
	}
	for in, want := range cases {
		if got := CanonicalArgs(in, []string{"a"}); got != want {
			t.Errorf("CanonicalArgs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalArgs_IgnoreFields(t *testing.T) {
	in := `{"a":1,"trace_id":"x","n":{"trace_id":"y","b":[{"trace_id":"z","c":2},3]}}`
	want := `{"a":1,"n":{"b":[{"c":2},3]}}`
	if got := CanonicalArgs(in, []string{"trace_id"}); got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	if got := CanonicalArgs(in, nil); !strings.Contains(got, "trace_id") {
		t.Error("nothing should be removed without an ignore list")
	}
	if got := CanonicalArgs(`{"a":1}`, []string{"a"}); got != `{}` {
		t.Errorf("removing the only key = %s, want {}", got)
	}
}

func TestMessageText(t *testing.T) {
	text := "hello"
	if got, non := messageText(nil); got != "" || non {
		t.Errorf("nil content = %q, %v", got, non)
	}
	if got, non := messageText(&schemas.ChatMessageContent{ContentStr: &text}); got != "hello" || non {
		t.Errorf("string content = %q, %v", got, non)
	}
	refusal := "no"
	blocks := &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
		{Type: schemas.ChatContentBlockTypeText, Text: &text},
		{Type: schemas.ChatContentBlockTypeRefusal, Refusal: &refusal},
	}}
	if got, non := messageText(blocks); got != "hellono" || non {
		t.Errorf("text blocks = %q, %v", got, non)
	}
	blocks.ContentBlocks = append(blocks.ContentBlocks, schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeImage})
	if _, non := messageText(blocks); !non {
		t.Error("an image block must be reported as non-text")
	}
}

func TestMessageHash_IgnoresWhatClientsRewrite(t *testing.T) {
	reasoning := "thinking"
	a := assistantCalls(call("c1", "check", `{"b":1,"a":2}`))
	b := assistantCalls(call("c1", "check", `{"a": 2, "b": 1}`))
	b.ChatAssistantMessage.Reasoning = &reasoning
	b.ToolCalls[0].Index = 7
	if messageHash(a) != messageHash(b) {
		t.Error("argument layout, reasoning and call index must not change the hash")
	}
	if messageHash(a) == messageHash(assistantCalls(call("c2", "check", `{"a":2,"b":1}`))) {
		t.Error("a different tool call id must change the hash")
	}
	if messageHash(user("a")) == messageHash(user("b")) {
		t.Error("different text must change the hash")
	}
	if messageHash(assistantText("a")) == messageHash(user("a")) {
		t.Error("a different role must change the hash")
	}
	if messageHash(toolResult("c1", "x")) == messageHash(toolResult("c2", "x")) {
		t.Error("a different tool_call_id must change the hash")
	}
}
