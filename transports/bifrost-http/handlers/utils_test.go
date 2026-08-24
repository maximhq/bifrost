package handlers

import (
	"strings"
	"testing"

	"github.com/valyala/fasthttp"
)

// TestSendJSON_StandardBytes pins the exact json.NewEncoder byte contract that the
// switch to sonic must not regress: HTML escaping, sorted map keys (deterministic
// output), and a trailing newline. The input has out-of-order keys and HTML
// metacharacters so all three properties show up in one exact-bytes comparison.
// sonic.ConfigDefault would break every one of them.
func TestSendJSON_StandardBytes(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	SendJSON(ctx, map[string]string{"z": "1", "a": "<b>&</b>"})

	got := string(ctx.Response.Body())
	want := `{"a":"\u003cb\u003e\u0026\u003c/b\u003e","z":"1"}` + "\n"
	if got != want {
		t.Errorf("SendJSON bytes mismatch:\n got  %q\n want %q", got, want)
	}
}

// TestSendJSON_Deterministic pins that a many-key map serializes byte-identically
// every time. Without key sorting (ConfigDefault) Go's randomized map iteration
// would make this flaky — the regression we must never reintroduce.
func TestSendJSON_Deterministic(t *testing.T) {
	m := map[string]int{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6, "g": 7, "h": 8}
	first := &fasthttp.RequestCtx{}
	SendJSON(first, m)
	want := string(first.Response.Body())
	for i := 0; i < 50; i++ {
		ctx := &fasthttp.RequestCtx{}
		SendJSON(ctx, m)
		if got := string(ctx.Response.Body()); got != want {
			t.Fatalf("non-deterministic output on iter %d:\n want %s\n got  %s", i, want, got)
		}
	}
}

// TestSendJSONWithStatus_StandardBytes mirrors SendJSON's exact-bytes contract and
// pins the custom status code.
func TestSendJSONWithStatus_StandardBytes(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	SendJSONWithStatus(ctx, map[string]string{"z": "1", "a": "<x>"}, fasthttp.StatusCreated)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusCreated {
		t.Errorf("status = %d, want %d", got, fasthttp.StatusCreated)
	}
	got := string(ctx.Response.Body())
	want := `{"a":"\u003cx\u003e","z":"1"}` + "\n"
	if got != want {
		t.Errorf("SendJSONWithStatus bytes mismatch:\n got  %q\n want %q", got, want)
	}
}

// TestSendJSON_MarshalError pins the marshal-failure early-return: an unsupported
// value (a channel) must yield HTTP 500 and the SendError body, never a partial or
// panicking write. Guards the err != nil branch that the happy-path tests skip.
func TestSendJSON_MarshalError(t *testing.T) {
	SetLogger(&mockLogger{}) // error path logs a warning; shared no-op logger
	ctx := &fasthttp.RequestCtx{}
	SendJSON(ctx, make(chan int)) // channels are unmarshalable

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Errorf("status = %d, want %d", got, fasthttp.StatusInternalServerError)
	}
	if body := string(ctx.Response.Body()); !strings.Contains(body, "Failed to encode response") {
		t.Errorf("expected SendError body, got %q", body)
	}
}

// TestSendJSONWithStatus_MarshalError mirrors the marshal-failure early-return for
// the custom-status helper: the 500 from SendError must win over the requested
// status code.
func TestSendJSONWithStatus_MarshalError(t *testing.T) {
	SetLogger(&mockLogger{})
	ctx := &fasthttp.RequestCtx{}
	SendJSONWithStatus(ctx, make(chan int), fasthttp.StatusCreated)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusInternalServerError {
		t.Errorf("status = %d, want %d (SendError must override the requested status)", got, fasthttp.StatusInternalServerError)
	}
	if body := string(ctx.Response.Body()); !strings.Contains(body, "Failed to encode response") {
		t.Errorf("expected SendError body, got %q", body)
	}
}
