package chaosmock_test

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
)

// Bedrock streams are read with the decoder Bedrock's Go clients use, which
// checks both CRCs of every frame, so a framing slip fails here rather than as
// a dropped stream in the client under test.

// readEventStream decodes an AWS event stream into its messages, failing the
// test on a frame the decoder rejects.
func readEventStream(t *testing.T, body string) []eventstream.Message {
	t.Helper()
	dec := eventstream.NewDecoder()
	r := strings.NewReader(body)
	var out []eventstream.Message
	for {
		m, err := dec.Decode(r, nil)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("frame %d does not decode: %v", len(out), err)
		}
		out = append(out, m)
	}
}

// header reads one header's string value, empty when absent.
func header(m eventstream.Message, name string) string {
	if v := m.Headers.Get(name); v != nil {
		return v.String()
	}
	return ""
}

func TestLLMConverseRepliesAsBedrockConverse(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	llmScript(t, fleet, map[string]any{"scenario": "conv", "then": turnWithCall})
	req := `{"messages":[{"role":"user","content":[{"text":"[scenario:conv]"}]}],"toolConfig":{"tools":[{"toolSpec":{"name":"alpha-echo","inputSchema":{"json":{}}}},{"cachePoint":{"type":"default"}}]}}`

	status, _, body := llmDo(t, fleet, "POST", "/bedrock/model/m1:0/converse", req)
	b := decode(t, body)
	if status != 200 || at(b, "output", "message", "content", 0, "text") != "Hello there" || at(b, "output", "message", "content", 1, "toolUse", "toolUseId") != "t1" ||
		at(b, "output", "message", "content", 1, "toolUse", "input", "text") != "hi" || at(b, "stopReason") != "tool_use" || at(b, "usage", "totalTokens") != 10.0 {
		t.Fatalf("unexpected reply %d %s", status, body)
	}

	_, h, body := llmDo(t, fleet, "POST", "/bedrock/model/m1:0/converse-stream", req)
	if h.Get("Content-Type") != "application/vnd.amazon.eventstream" {
		t.Errorf("expected an event stream, got %q", h.Get("Content-Type"))
	}
	var events []string
	var text, input string
	for _, m := range readEventStream(t, body) {
		if header(m, ":message-type") != "event" || header(m, ":content-type") != "application/json" {
			t.Fatalf("every frame must be a JSON event, got %v", m.Headers)
		}
		event := header(m, ":event-type")
		events = append(events, event)
		d := decode(t, string(m.Payload))
		switch event {
		case "contentBlockDelta":
			if s, ok := at(d, "delta", "text").(string); ok {
				text += s
			}
			if s, ok := at(d, "delta", "toolUse", "input").(string); ok {
				input += s
			}
		case "contentBlockStart":
			if at(d, "contentBlockIndex") != 1.0 || at(d, "start", "toolUse", "name") != "alpha-echo" {
				t.Errorf("unexpected contentBlockStart %s", m.Payload)
			}
		case "messageStop":
			if at(d, "stopReason") != "tool_use" {
				t.Errorf("unexpected messageStop %s", m.Payload)
			}
		case "metadata":
			if at(d, "usage", "outputTokens") != 3.0 {
				t.Errorf("unexpected metadata %s", m.Payload)
			}
		}
	}
	want := []string{"messageStart", "contentBlockDelta", "contentBlockDelta", "contentBlockStop", "contentBlockStart", "contentBlockDelta", "contentBlockDelta", "contentBlockStop", "messageStop", "metadata"}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("event order\n got %v\nwant %v", events, want)
	}
	if text != "Hello there" || input != `{"text":"hi"}` {
		t.Errorf("joined stream gave %q, %q", text, input)
	}

	calls := llmRequests(t, fleet, "conv")
	if len(calls) != 2 || calls[0].Format != "bedrock.converse" || calls[0].Model != "m1:0" || calls[0].Stream || !calls[1].Stream ||
		!reflect.DeepEqual(calls[0].Tools, []string{"alpha-echo", "type:cachePoint"}) {
		t.Errorf("unexpected log %+v", calls)
	}
}

func TestServeTLSAnswersAsOneServersBedrockSurface(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	base, stop, err := fleet.ServeTLS("127.0.0.1:0", "alpha")
	if err != nil {
		t.Fatalf("ServeTLS: %v", err)
	}
	t.Cleanup(func() { _ = stop(context.Background()) })
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	do := func(method, path, body string) (int, http.Header, string) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s failed: %v", method, path, err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header, string(out)
	}

	if !strings.HasPrefix(base, "https://127.0.0.1:") {
		t.Errorf("expected an https loopback URL, got %s", base)
	}
	if _, _, body := do("GET", "/foundation-models?byOutputModality=TEXT", ""); at(decode(t, body), "modelSummaries", 0, "modelId") != "mock-model" {
		t.Errorf("expected the default model in Bedrock's listing, got %s", body)
	}
	if _, _, body := do("GET", "/v1/models", ""); at(decode(t, body), "data", 0, "id") != "mock-model" {
		t.Errorf("expected the default model in the OpenAI-compatible listing, got %s", body)
	}

	// Errors are Bedrock's: the type in a header and only a message in the body.
	status, h, body := do("POST", "/model/mock-model/converse", `{"messages":[]}`)
	if status != 400 || h.Get("x-amzn-ErrorType") != "chaosmock_unscripted" || !strings.Contains(at(decode(t, body), "message").(string), "no [scenario:<key>] marker") {
		t.Errorf("expected an unscripted 400 in Bedrock's shape, got %d %v %s", status, h, body)
	}
	if status, h, _ := do("POST", "/model/mock-model/invoke", `{}`); status != 404 || h.Get("x-amzn-ErrorType") != "not_found_error" {
		t.Errorf("expected a 404 in Bedrock's shape, got %d", status)
	}

	llmScript(t, fleet, map[string]any{"scenario": "tls", "then": map[string]any{"text": "over tls"}})
	if status, _, body := do("POST", "/model/mock-model/converse", `{"messages":[{"role":"user","content":[{"text":"[scenario:tls]"}]}]}`); status != 200 ||
		at(decode(t, body), "output", "message", "content", 0, "text") != "over tls" {
		t.Errorf("expected the scripted reply, got %d %s", status, body)
	}
	if calls := llmRequests(t, fleet, "tls"); len(calls) != 1 || calls[0].Path != "/bedrock/model/mock-model/converse" {
		t.Errorf("expected the request logged under the bedrock prefix, got %+v", calls)
	}

	// The server is looked up per request, so one removed later is a 404.
	fleet.Remove("alpha")
	if status, _, body := do("GET", "/foundation-models", ""); status != 404 || !strings.Contains(body, "no such server: alpha") {
		t.Errorf("expected a 404 for a removed server, got %d %s", status, body)
	}
}
