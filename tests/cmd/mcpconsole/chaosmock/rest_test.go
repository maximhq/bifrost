package chaosmock_test

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/tests/cmd/mcpconsole/chaosmock"
)

// The REST surface serves whatever a scenario sets, so these tests pin the
// two things a client test leans on: that a body arrives exactly as set, and
// that the log shows exactly what the client sent.

// restDo sends one request to alpha's REST surface and returns the status,
// content type and body.
func restDo(t *testing.T, fleet *chaosmock.Fleet, method, path, body string, header map[string]string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequest(method, fleet.URL()+"/s/alpha/rest"+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Type"), string(out)
}

func TestRESTRoutesServeTheirBodiesByteForByte(t *testing.T) {
	fleet, srv := newFleetWithTools(t)
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	post(t, fleet.URL()+"/control/alpha/routes/set", map[string]any{"routes": []any{
		map[string]any{"method": "GET", "path": "/openapi.json", "json": map[string]any{"openapi": "3.0.0"}},
		map[string]any{"path": "/broken", "body": "{"},
		map[string]any{"path": "/logo", "body_base64": base64.StdEncoding.EncodeToString(png), "content_type": "image/png"},
		map[string]any{"path": "/missing", "status": 404, "json": map[string]any{"e": 1}, "headers": map[string]string{"X-Trace": "t1"}},
	}})

	if status, ct, body := restDo(t, fleet, "GET", "/openapi.json", "", nil); status != 200 || ct != "application/json" || body != `{"openapi":"3.0.0"}` {
		t.Errorf("json route: got %d %q %q", status, ct, body)
	}
	if status, ct, body := restDo(t, fleet, "GET", "/broken", "", nil); status != 200 || ct != "text/plain; charset=utf-8" || body != "{" {
		t.Errorf("body route: got %d %q %q", status, ct, body)
	}
	if status, ct, body := restDo(t, fleet, "GET", "/logo", "", nil); status != 200 || ct != "image/png" || body != string(png) {
		t.Errorf("binary route: got %d %q %q", status, ct, body)
	}
	if status, _, body := restDo(t, fleet, "GET", "/missing", "", nil); status != 404 || body != `{"e":1}` {
		t.Errorf("status route: got %d %q", status, body)
	}
	if calls := srv.State().REST.Calls; len(calls) != 4 || calls[3].Route != "/missing" || calls[3].Status != 404 {
		t.Errorf("expected four logged calls ending with the 404 route, got %+v", calls)
	}
}

func TestRESTMatchesInOrderBySegmentAndMethod(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	base := fleet.URL() + "/control/alpha"
	post(t, base+"/routes/set", map[string]any{"routes": []any{
		map[string]any{"method": "GET", "path": "/pets/{petId}", "body": "any pet"},
		map[string]any{"method": "GET", "path": "/pets/7", "body": "pet seven"},
		map[string]any{"path": "/pets", "body": "every method"},
	}})

	// The first matching route wins, the query is not matched, and a method
	// that no route names falls through to the echo.
	if _, _, body := restDo(t, fleet, "GET", "/pets/7?verbose=1", "", nil); body != "any pet" {
		t.Errorf("expected the first route to win, got %q", body)
	}
	if _, _, body := restDo(t, fleet, "DELETE", "/pets", "", nil); body != "every method" {
		t.Errorf("expected a route without a method to match any, got %q", body)
	}
	if _, _, body := restDo(t, fleet, "POST", "/pets/7", "", nil); !strings.HasPrefix(body, `{"body"`) {
		t.Errorf("expected the echo for an unrouted method, got %q", body)
	}

	// Adding the same method and path replaces it in place, keeping its order.
	post(t, base+"/routes/add", map[string]any{"route": map[string]any{"method": "get", "path": "/pets/{petId}", "body": "replaced"}})
	if _, _, body := restDo(t, fleet, "GET", "/pets/7", "", nil); body != "replaced" {
		t.Errorf("expected the replaced route, got %q", body)
	}
	post(t, base+"/routes/remove", map[string]any{"path": "/pets/{petId}"})
	if _, _, body := restDo(t, fleet, "GET", "/pets/7", "", nil); body != "pet seven" {
		t.Errorf("expected the next route once the first is removed, got %q", body)
	}
	if resp, _ := post(t, base+"/routes/remove", map[string]any{"path": "/nothing"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 removing a route that does not exist, got %d", resp.StatusCode)
	}
}

func TestRESTEchoAndLogShowWhatTheClientSent(t *testing.T) {
	fleet, srv := newFleetWithTools(t)
	status, _, body := restDo(t, fleet, "POST", "/pets", `{"name":"x"}`, map[string]string{
		"Authorization": "Bearer T1", "X-Static": "s",
	})
	if status != 200 || body != `{"body":"{\"name\":\"x\"}","method":"POST","path":"/pets","query":""}` {
		t.Errorf("unexpected echo %d %s", status, body)
	}
	restDo(t, fleet, "GET", "/pets", "", nil)
	// Raw query, as sent.
	restDo(t, fleet, "GET", "/pets?limit=2&b=1", "", nil)

	calls := srv.State().REST.Calls
	if len(calls) != 3 {
		t.Fatalf("expected three logged calls, got %+v", calls)
	}
	first := calls[0]
	if first.Method != "POST" || first.Path != "/pets" || first.Body != `{"name":"x"}` || first.Status != 200 || first.Route != "" {
		t.Errorf("unexpected log entry %+v", first)
	}
	// Credentials are kept, under canonical names: checking them is the point.
	if first.Headers["Authorization"] != "Bearer T1" || first.Headers["X-Static"] != "s" {
		t.Errorf("expected the request headers in the log, got %v", first.Headers)
	}
	if calls[2].Query != "limit=2&b=1" {
		t.Errorf("expected the raw query, got %q", calls[2].Query)
	}

	_, state := post(t, fleet.URL()+"/control/alpha/counters/reset", nil)
	if state.REST != nil {
		t.Errorf("expected counters/reset to clear the log, got %+v", state.REST)
	}
}

func TestRESTFollowsUnscopedServerFaults(t *testing.T) {
	fleet, srv := newFleetWithTools(t)

	srv.SetFaults(chaosmock.Faults{Status: 500, Message: "catalog down"})
	if status, _, body := restDo(t, fleet, "GET", "/catalog.json", "", nil); status != 500 || body != "catalog down" {
		t.Errorf("expected the server's 500, got %d %q", status, body)
	}
	// A fault scoped to JSON-RPC methods does not reach REST, which has none.
	srv.SetFaults(chaosmock.Faults{Status: 500, Methods: []string{"tools/call"}})
	if status, _, _ := restDo(t, fleet, "GET", "/catalog.json", "", nil); status != 200 {
		t.Errorf("a scoped fault must not reach REST, got %d", status)
	}
	srv.SetFaults(chaosmock.Faults{Refuse: true})
	if _, err := http.Get(fleet.URL() + "/s/alpha/rest/catalog.json"); err == nil {
		t.Errorf("expected a refused connection")
	}

	// Go's client retries an idempotent request once when the connection
	// closes before any response, so the refusal may be logged twice.
	calls := srv.State().REST.Calls
	if len(calls) < 3 || calls[0].Status != 500 || calls[1].Status != 200 {
		t.Fatalf("expected statuses 500 and 200 first in the log, got %+v", calls)
	}
	for _, c := range calls[2:] {
		if c.Status != 0 {
			t.Errorf("expected a refused call to log status 0, got %+v", c)
		}
	}
}

func TestControlRejectsRoutesThatCannotBeServed(t *testing.T) {
	fleet, _ := newFleetWithTools(t)
	for name, route := range map[string]map[string]any{
		"relative path":   {"path": "pets"},
		"two bodies":      {"path": "/x", "body": "a", "json": map[string]any{}},
		"bad base64":      {"path": "/x", "body_base64": "%%%"},
		"not a status":    {"path": "/x", "status": 42},
		"misspelled body": {"path": "/x", "bdy": "a"},
	} {
		if resp, _ := post(t, fleet.URL()+"/control/alpha/routes/add", map[string]any{"route": route}); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", name, resp.StatusCode)
		}
	}
}
