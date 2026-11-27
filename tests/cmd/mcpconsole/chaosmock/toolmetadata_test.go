package chaosmock_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

// TestRealClientReadsToolMetadata: a tool's title, icons and output schema reach a real client as
// the scenario set them, and a tool that sets none arrives without them.
func TestRealClientReadsToolMetadata(t *testing.T) {
	fleet, _ := newFleetWithTools(t,
		chaosmock.Tool{
			Name:         "echo",
			Title:        "Echo",
			Icons:        json.RawMessage(`[{"src":"https://icons.example.invalid/echo.png","mimeType":"image/png","sizes":["48x48"]}]`),
			OutputSchema: json.RawMessage(`{"type":"object","properties":{"echoed":{"type":"string"}},"required":["echoed"]}`),
		},
		chaosmock.Tool{Name: "plain"},
	)
	c, ctx := connectAt(t, fleet.ServerURL("alpha"), "")
	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil || len(res.Tools) != 2 {
		t.Fatalf("tools/list = %+v, %v", res, err)
	}
	echo, plain := res.Tools[0], res.Tools[1]
	if echo.Title != "Echo" {
		t.Errorf("title = %q, want Echo", echo.Title)
	}
	if len(echo.Icons) != 1 || echo.Icons[0].Src != "https://icons.example.invalid/echo.png" || echo.Icons[0].MIMEType != "image/png" || strings.Join(echo.Icons[0].Sizes, ",") != "48x48" {
		t.Errorf("icons = %+v", echo.Icons)
	}
	if echo.OutputSchema.Type != "object" || echo.OutputSchema.Properties["echoed"] == nil || strings.Join(echo.OutputSchema.Required, ",") != "echoed" {
		t.Errorf("outputSchema = %+v", echo.OutputSchema)
	}
	if plain.Title != "" || plain.Icons != nil || plain.OutputSchema.Type != "" {
		t.Errorf("a tool without metadata must arrive without it, got %+v", plain)
	}
}

// TestToolMetadataIsValidated: icons that are not a list, or an output schema that is not an
// object, are refused rather than served.
func TestToolMetadataIsValidated(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	for _, body := range []string{
		`{"tools":[{"name":"bad","icons":{"src":"x"}}]}`,
		`{"tools":[{"name":"bad","outputSchema":["string"]}]}`,
	} {
		resp, err := http.Post(fleet.URL()+"/control/alpha/tools/set", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, resp.StatusCode)
		}
	}
}

// TestToolReportsProgress: a tool with progress steps reports them, in order and under the caller's
// token, ahead of its result on every transport, and sends none to a call that sent no token.
func TestToolReportsProgress(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "work", Progress: []chaosmock.ProgressStep{
		{Progress: 1, Total: 2, Message: "step 1 of 2"},
		{Progress: 2, Total: 2},
	}})
	if err := srv.SetProtocolVersions(allVersions); err != nil {
		t.Fatal(err)
	}
	transports := []struct {
		name    string
		connect func() (*client.Client, context.Context)
	}{
		{chaosmock.ProtocolVersion20260728, func() (*client.Client, context.Context) { return connectAt(t, fleet.ServerURL("alpha"), "") }},
		{chaosmock.ProtocolVersion20251125, func() (*client.Client, context.Context) {
			return connectAt(t, fleet.ServerURL("alpha"), chaosmock.ProtocolVersion20251125)
		}},
		{"sse", func() (*client.Client, context.Context) { return connectSSE(t, fleet.SSEURL("alpha")) }},
		{"stdio", func() (*client.Client, context.Context) { return bridgeToClient(t, fleet, "alpha") }},
	}
	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			c, ctx := tr.connect()
			if strings.HasPrefix(tr.name, "20") && c.ProtocolVersion() != tr.name {
				t.Fatalf("negotiated %q, want %q", c.ProtocolVersion(), tr.name)
			}
			var mu sync.Mutex
			var progress []map[string]any
			c.OnNotification(func(n mcp.JSONRPCNotification) {
				if n.Method == string(mcp.MethodNotificationProgress) {
					mu.Lock()
					progress = append(progress, n.Params.AdditionalFields)
					mu.Unlock()
				}
			})

			var call mcp.CallToolRequest
			call.Params.Name = "work"
			call.Params.Meta = &mcp.Meta{ProgressToken: "tok"}
			if _, err := c.CallTool(ctx, call); err != nil {
				t.Fatalf("tools/call: %v", err)
			}
			call.Params.Meta = nil
			if _, err := c.CallTool(ctx, call); err != nil {
				t.Fatalf("tools/call without a token: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			want := []map[string]any{
				{"progressToken": "tok", "progress": float64(1), "total": float64(2), "message": "step 1 of 2"},
				{"progressToken": "tok", "progress": float64(2), "total": float64(2)},
			}
			if !reflect.DeepEqual(progress, want) {
				t.Fatalf("progress = %v, want %v", progress, want)
			}
		})
	}
	if got := srv.Counters().Snapshot().ProgressCalls["work"]; got != int64(len(transports)) {
		t.Fatalf("progress_calls[work] = %d, want %d: only the calls that sent a token", got, len(transports))
	}
}

// TestToolProgressIsValidated: progress that does not increase, or a total below its progress, is refused.
func TestToolProgressIsValidated(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	for _, body := range []string{
		`{"tools":[{"name":"bad","progress":[{"progress":0}]}]}`,
		`{"tools":[{"name":"bad","progress":[{"progress":2},{"progress":1}]}]}`,
		`{"tools":[{"name":"bad","progress":[{"progress":3,"total":2}]}]}`,
	} {
		resp, err := http.Post(fleet.URL()+"/control/alpha/tools/set", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, resp.StatusCode)
		}
	}
}

// TestToolProgressOutrunsSSEBuffer: over the legacy SSE transport, more progress than the stream
// buffers at once still reaches the caller in full, followed by the result.
func TestToolProgressOutrunsSSEBuffer(t *testing.T) {
	steps := make([]chaosmock.ProgressStep, 50)
	for i := range steps {
		steps[i] = chaosmock.ProgressStep{Progress: float64(i + 1)}
	}
	fleet, _ := newFleetWithTools(t, chaosmock.Tool{Name: "work", Progress: steps})
	c, ctx := connectSSE(t, fleet.SSEURL("alpha"))
	var mu sync.Mutex
	var got int
	c.OnNotification(func(n mcp.JSONRPCNotification) {
		if n.Method == string(mcp.MethodNotificationProgress) {
			mu.Lock()
			got++
			mu.Unlock()
		}
	})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var call mcp.CallToolRequest
	call.Params.Name = "work"
	call.Params.Meta = &mcp.Meta{ProgressToken: "tok"}
	if _, err := c.CallTool(ctx, call); err != nil {
		t.Fatalf("tools/call: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got != len(steps) {
		t.Fatalf("got %d progress notifications, want %d", got, len(steps))
	}
}

// TestToolProgressNotCountedOnReset: a call cut off before any progress is written is not counted as
// sent progress.
func TestToolProgressNotCountedOnReset(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "work", Progress: []chaosmock.ProgressStep{{Progress: 1}}})
	c, ctx := connectAt(t, fleet.ServerURL("alpha"), chaosmock.ProtocolVersion20251125)
	srv.SetFaults(chaosmock.Faults{Reset: true})
	var call mcp.CallToolRequest
	call.Params.Name = "work"
	call.Params.Meta = &mcp.Meta{ProgressToken: "tok"}
	if _, err := c.CallTool(ctx, call); err == nil {
		t.Fatal("tools/call succeeded through a reset")
	}
	if got := srv.Counters().Snapshot().ProgressCalls["work"]; got != 0 {
		t.Fatalf("progress_calls[work] = %d, want 0", got)
	}
}

// TestRejectedCallReportsNoProgress: on 2026-07-28 a call the server rejects is answered with the
// rejection's own status and no progress, while an error from work already under way is streamed after it.
func TestRejectedCallReportsNoProgress(t *testing.T) {
	steps := []chaosmock.ProgressStep{{Progress: 1, Total: 1}}
	fleet, srv := newFleetWithTools(t,
		chaosmock.Tool{Name: "rejected", Progress: steps, ReplyError: json.RawMessage(`{"code":-32602,"message":"bad arguments"}`)},
		chaosmock.Tool{Name: "failing", Progress: steps, ReplyError: json.RawMessage(`{"code":-32000,"message":"failed mid-work"}`)},
	)
	if err := srv.SetProtocolVersions(allVersions); err != nil {
		t.Fatal(err)
	}
	callTool := func(name string) (int, string, string) {
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","progressToken":"tok"}}}`
		req, err := http.NewRequest(http.MethodPost, fleet.ServerURL("alpha"), strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set(chaosmock.HeaderProtocolVersion, chaosmock.ProtocolVersion20260728)
		req.Header.Set(chaosmock.HeaderMcpMethod, "tools/call")
		req.Header.Set(chaosmock.HeaderMcpName, name)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(raw)
	}

	status, contentType, body := callTool("rejected")
	if status != http.StatusBadRequest || !strings.HasPrefix(contentType, "application/json") || strings.Contains(body, "notifications/progress") || !strings.Contains(body, "-32602") {
		t.Fatalf("rejected call: status %d, %s, body %s; want 400 JSON carrying -32602 and no progress", status, contentType, body)
	}
	status, contentType, body = callTool("failing")
	if status != http.StatusOK || !strings.HasPrefix(contentType, "text/event-stream") ||
		strings.Index(body, "notifications/progress") < 0 || strings.Index(body, "notifications/progress") > strings.Index(body, "-32000") {
		t.Fatalf("failing call: status %d, %s, body %s; want 200 SSE with progress before the -32000 error", status, contentType, body)
	}
	if got := srv.Counters().Snapshot().ProgressCalls; !reflect.DeepEqual(got, map[string]int64{"failing": 1}) {
		t.Fatalf("progress_calls = %v, want only the failing call", got)
	}
}

// failingWriter is a client gone before anything reached it: every write fails.
type failingWriter struct{ header http.Header }

func (w *failingWriter) Header() http.Header       { return w.header }
func (w *failingWriter) WriteHeader(int)           {}
func (w *failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// TestProgressCountedOnlyOnceSent: a call whose progress could not be written to the caller is
// counted as a call, never as one that was sent progress.
func TestProgressCountedOnlyOnceSent(t *testing.T) {
	fleet, srv := newFleetWithTools(t, chaosmock.Tool{Name: "work", Progress: []chaosmock.ProgressStep{{Progress: 1}}})
	if err := srv.SetProtocolVersions(allVersions); err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(fleet.ServerURL("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"work","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","progressToken":"tok"}}}`
	req := httptest.NewRequest(http.MethodPost, target.Path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set(chaosmock.HeaderProtocolVersion, chaosmock.ProtocolVersion20260728)
	req.Header.Set(chaosmock.HeaderMcpMethod, "tools/call")
	req.Header.Set(chaosmock.HeaderMcpName, "work")
	fleet.Handler().ServeHTTP(&failingWriter{header: http.Header{}}, req)

	counters := srv.Counters().Snapshot()
	if counters.Calls["work"] != 1 {
		t.Fatalf("calls[work] = %d, want 1: the call must have run", counters.Calls["work"])
	}
	if len(counters.ProgressCalls) != 0 {
		t.Fatalf("progress_calls = %v, want none: nothing reached the caller", counters.ProgressCalls)
	}
}
