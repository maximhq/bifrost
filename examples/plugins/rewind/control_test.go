package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func do(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m, rec.Body.String()
}

func TestControlAPI(t *testing.T) {
	s := newStore(t)
	h := controlHandler(func() *Store { return s })

	if code, m, _ := do(t, h, "GET", "/rewind/health", ""); code != 200 || m["status"] != "ok" {
		t.Fatalf("health = %d %v", code, m)
	}
	code, m, _ := do(t, h, "POST", "/rewind/runs", `{"run_id":"base","mode":"record","meta":{"agent":"a"}}`)
	if code != 201 || m["run_id"] != "base" || m["mode"] != "record" {
		t.Fatalf("create = %d %v", code, m)
	}
	code, m, _ = do(t, h, "POST", "/rewind/runs", `{"run_id":"fk","mode":"fork","source":"base","fork_at":1,"mutation":{"type":"outage","status":503}}`)
	if code != 201 || m["parent"] != "base" || m["fork_at"].(float64) != 1 {
		t.Fatalf("fork = %d %v", code, m)
	}
	for _, bad := range []string{`{`, `{"run_id":"../x"}`, `{"run_id":"r","mode":"replay","source":"missing"}`} {
		if code, _, _ := do(t, h, "POST", "/rewind/runs", bad); code != 400 {
			t.Fatalf("bad spec %s gave %d", bad, code)
		}
	}
	if code, _, _ := do(t, h, "DELETE", "/rewind/runs", ""); code != 405 {
		t.Fatalf("DELETE gave %d", code)
	}

	_, _, raw := do(t, h, "GET", "/rewind/runs", "")
	var list []Summary
	if err := json.Unmarshal([]byte(raw), &list); err != nil || len(list) != 2 {
		t.Fatalf("list = %s", raw)
	}

	if code, m, _ := do(t, h, "GET", "/rewind/runs/base", ""); code != 200 || m["run_id"] != "base" || m["steps"] == nil {
		t.Fatalf("get = %d %v", code, m)
	}
	if code, _, _ := do(t, h, "GET", "/rewind/runs/missing", ""); code != 404 {
		t.Fatalf("missing run gave %d", code)
	}
	if code, m, _ := do(t, h, "POST", "/rewind/runs/base/close", ""); code != 200 || m["run_id"] != "base" {
		t.Fatalf("close = %d %v", code, m)
	}
	if code, _, _ := do(t, h, "POST", "/rewind/runs/base/close", ""); code != 404 {
		t.Fatalf("second close gave %d", code)
	}
	// closed runs are still readable from disk
	if code, m, _ := do(t, h, "GET", "/rewind/runs/base", ""); code != 200 || m["ended_at"] == "" {
		t.Fatalf("closed get = %d %v", code, m)
	}
}

func TestControlUsesCurrentStore(t *testing.T) {
	a, b := newStore(t), newStore(t)
	cur := a
	h := controlHandler(func() *Store { return cur })
	do(t, h, "POST", "/rewind/runs", `{"run_id":"one"}`)
	cur = b
	if code, _, _ := do(t, h, "GET", "/rewind/runs/one", ""); code != 404 {
		t.Fatal("handler should follow the current store")
	}
}
