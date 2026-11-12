package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/heimdall"
	"github.com/valyala/fasthttp"
)

// fakeHeimdall records what the handler asked of the service and answers from its fields.
type fakeHeimdall struct {
	allowUnauth bool
	err         error

	startReq      heimdall.CreateRunRequest
	sessionsQuery heimdall.SessionQuery
	runsQuery     heimdall.RunQuery
	runSessionsQ  heimdall.RunSessionQuery
	ids           []string // the ids the last call got, in order
	calls         []string
	startedRun    *heimdall.RunDetail
}

func (f *fakeHeimdall) record(name string, ids ...string) {
	f.calls = append(f.calls, name)
	f.ids = ids
}

func (f *fakeHeimdall) AllowUnauthenticatedMutations() bool { return f.allowUnauth }

func (f *fakeHeimdall) Status(context.Context) (*heimdall.Status, error) {
	f.record("Status")
	id := "run_1"
	return &heimdall.Status{Enabled: true, ActiveRunID: &id, Requirements: heimdall.Requirements{LogsStore: true, ConfigStore: true, ContentLogging: true}}, f.err
}

func (f *fakeHeimdall) ListSessions(_ context.Context, q heimdall.SessionQuery) (*heimdall.SessionPage, error) {
	f.record("ListSessions")
	f.sessionsQuery = q
	return &heimdall.SessionPage{Sessions: []heimdall.SessionSummary{{SessionID: "s1", Provider: "openai", Model: "m", Turns: 4}}, Count: 1, Limit: 50}, f.err
}

func (f *fakeHeimdall) GetSession(_ context.Context, id string) (*heimdall.SessionDetail, error) {
	f.record("GetSession", id)
	return &heimdall.SessionDetail{SessionID: id, Eligibility: heimdall.Eligibility{Code: heimdall.CodeEligible}}, f.err
}

func (f *fakeHeimdall) StartRun(_ context.Context, req heimdall.CreateRunRequest) (*heimdall.RunDetail, error) {
	f.record("StartRun")
	f.startReq = req
	if f.err != nil {
		return nil, f.err
	}
	if f.startedRun != nil {
		return f.startedRun, nil
	}
	return &heimdall.RunDetail{ID: "run_new", Name: req.Name, Status: heimdall.StatusQueued, Baseline: req.Baseline, Candidate: req.Candidate, Warnings: []string{"w"}}, nil
}

func (f *fakeHeimdall) ListRuns(_ context.Context, q heimdall.RunQuery) (*heimdall.RunPage, error) {
	f.record("ListRuns")
	f.runsQuery = q
	return &heimdall.RunPage{Runs: []heimdall.RunDetail{{ID: "run_1"}}, Count: 1, Total: 1, Limit: 25}, f.err
}

func (f *fakeHeimdall) GetRun(_ context.Context, id string) (*heimdall.RunDetail, error) {
	f.record("GetRun", id)
	if f.err != nil {
		return nil, f.err
	}
	return &heimdall.RunDetail{ID: id, Status: heimdall.StatusComplete}, nil
}

func (f *fakeHeimdall) CancelRun(_ context.Context, id string) (*heimdall.RunDetail, error) {
	f.record("CancelRun", id)
	if f.err != nil {
		return nil, f.err
	}
	return &heimdall.RunDetail{ID: id, Status: heimdall.StatusCancelled}, nil
}

func (f *fakeHeimdall) ListRunSessions(_ context.Context, id string, q heimdall.RunSessionQuery) (*heimdall.RunSessionPage, error) {
	f.record("ListRunSessions", id)
	f.runSessionsQ = q
	return &heimdall.RunSessionPage{Sessions: []heimdall.RunSessionRow{{SessionID: "s1", CriticalCount: 2}}, Count: 1, Total: 1, Limit: 25}, f.err
}

func (f *fakeHeimdall) GetRunSession(_ context.Context, id, sessionID string) (*heimdall.RunSessionDetail, error) {
	f.record("GetRunSession", id, sessionID)
	return &heimdall.RunSessionDetail{SessionID: sessionID, Eligibility: heimdall.CodeEligible}, f.err
}

// heimdallServer is a router with the Heimdall routes over a fake service.
type heimdallServer struct {
	r   *router.Router
	svc *fakeHeimdall
}

func newHeimdallServer(svc *fakeHeimdall) *heimdallServer {
	r := router.New()
	NewHeimdallHandler(func() HeimdallService { return svc }).RegisterRoutes(r)
	return &heimdallServer{r: r, svc: svc}
}

type heimdallResponse struct {
	status int
	body   []byte
}

// message is the error message of an error response.
func (r heimdallResponse) message(t *testing.T) string {
	t.Helper()
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.body, &e); err != nil {
		t.Fatalf("not an error body: %s", r.body)
	}
	return e.Error.Message
}

func (r heimdallResponse) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("not JSON: %s", r.body)
	}
	return m
}

func (s *heimdallServer) do(method, uri, body string, prepare ...func(*fasthttp.RequestCtx)) heimdallResponse {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(method)
	ctx.Request.SetRequestURI(uri)
	if body != "" {
		ctx.Request.SetBodyString(body)
		ctx.Request.Header.SetContentType("application/json")
	}
	for _, p := range prepare {
		p(ctx)
	}
	s.r.Handler(ctx)
	return heimdallResponse{status: ctx.Response.StatusCode(), body: append([]byte(nil), ctx.Response.Body()...)}
}

func bypassAuth(ctx *fasthttp.RequestCtx) {
	ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
}

const validRunBody = `{
  "name": "Try demo-fast for refunds",
  "baseline": {"provider": "openai", "model": "demo-baseline"},
  "candidate": {"provider": "openai", "model": "demo-fast"},
  "selection": {"session_ids": ["a", "b"], "limit": 5},
  "params": {"margin": 0.1, "min_sessions": 30},
  "tool_rules": [{"tool": "shop-issue_refund", "protected": true, "requires_before": ["shop-check_refund_eligibility"]}],
  "forbidden_tools": ["shop-delete_customer"],
  "max_usd": 5.0,
  "max_calls": null,
  "data_egress_ack": true
}`

func TestHeimdall_EveryRouteIsRegistered(t *testing.T) {
	s := newHeimdallServer(&fakeHeimdall{})
	cases := []struct {
		method, uri, body string
		status            int
		call              string
		ids               []string
	}{
		{"GET", "/api/heimdall/status", "", 200, "Status", nil},
		{"GET", "/api/heimdall/sessions", "", 200, "ListSessions", nil},
		{"GET", "/api/heimdall/sessions/demo-001", "", 200, "GetSession", []string{"demo-001"}},
		{"POST", "/api/heimdall/runs", validRunBody, 202, "StartRun", nil},
		{"GET", "/api/heimdall/runs", "", 200, "ListRuns", nil},
		{"GET", "/api/heimdall/runs/run_1", "", 200, "GetRun", []string{"run_1"}},
		{"POST", "/api/heimdall/runs/run_1/cancel", "", 200, "CancelRun", []string{"run_1"}},
		{"GET", "/api/heimdall/runs/run_1/sessions", "", 200, "ListRunSessions", []string{"run_1"}},
		{"GET", "/api/heimdall/runs/run_1/sessions/s1", "", 200, "GetRunSession", []string{"run_1", "s1"}},
	}
	for _, tc := range cases {
		s.svc.calls = nil
		resp := s.do(tc.method, tc.uri, tc.body)
		if resp.status != tc.status {
			t.Errorf("%s %s = %d (%s), want %d", tc.method, tc.uri, resp.status, resp.body, tc.status)
			continue
		}
		if len(s.svc.calls) != 1 || s.svc.calls[0] != tc.call {
			t.Errorf("%s %s called %v, want [%s]", tc.method, tc.uri, s.svc.calls, tc.call)
		}
		if len(tc.ids) > 0 && strings.Join(s.svc.ids, ",") != strings.Join(tc.ids, ",") {
			t.Errorf("%s %s passed ids %v, want %v", tc.method, tc.uri, s.svc.ids, tc.ids)
		}
	}
	// Wrong methods and unknown paths reach nothing.
	s.svc.calls = nil
	for _, c := range [][2]string{{"DELETE", "/api/heimdall/runs/run_1"}, {"GET", "/api/heimdall/runs/run_1/cancel"}, {"POST", "/api/heimdall/status"}, {"GET", "/api/heimdall/nothing"}} {
		if resp := s.do(c[0], c[1], ""); resp.status != 404 && resp.status != 405 {
			t.Errorf("%s %s = %d, want 404 or 405", c[0], c[1], resp.status)
		}
	}
	if len(s.svc.calls) != 0 {
		t.Errorf("unexpected calls: %v", s.svc.calls)
	}
}

func TestHeimdall_DisabledGives503(t *testing.T) {
	handlers := map[string]*HeimdallHandler{
		"nil resolve":       NewHeimdallHandler(nil),
		"plugin not loaded": NewHeimdallHandler(func() HeimdallService { return nil }),
	}
	for name, h := range handlers {
		r := router.New()
		h.RegisterRoutes(r)
		s := &heimdallServer{r: r}
		for _, c := range [][2]string{
			{"GET", "/api/heimdall/status"}, {"GET", "/api/heimdall/sessions"}, {"GET", "/api/heimdall/sessions/x"},
			{"POST", "/api/heimdall/runs"}, {"GET", "/api/heimdall/runs"}, {"GET", "/api/heimdall/runs/x"},
			{"POST", "/api/heimdall/runs/x/cancel"}, {"GET", "/api/heimdall/runs/x/sessions"}, {"GET", "/api/heimdall/runs/x/sessions/y"},
		} {
			body := ""
			if c[0] == "POST" {
				body = validRunBody
			}
			resp := s.do(c[0], c[1], body)
			if resp.status != 503 || !strings.HasPrefix(resp.message(t), "heimdall_disabled") {
				t.Errorf("%s: %s %s = %d %s; want 503 heimdall_disabled", name, c[0], c[1], resp.status, resp.body)
			}
		}
	}
	var nilHandler *HeimdallHandler
	ctx := &fasthttp.RequestCtx{}
	nilHandler.status(ctx)
	if ctx.Response.StatusCode() != 503 {
		t.Errorf("a nil handler = %d", ctx.Response.StatusCode())
	}
}

func TestHeimdall_MutationsNeedAuthUnlessAllowed(t *testing.T) {
	svc := &fakeHeimdall{}
	s := newHeimdallServer(svc)

	for _, c := range [][3]string{{"POST", "/api/heimdall/runs", validRunBody}, {"POST", "/api/heimdall/runs/run_1/cancel", ""}} {
		svc.calls = nil
		resp := s.do(c[0], c[1], c[2], bypassAuth)
		if resp.status != 403 || !strings.HasPrefix(resp.message(t), "auth_required") {
			t.Errorf("%s %s with auth off = %d %s; want 403 auth_required", c[0], c[1], resp.status, resp.body)
		}
		if len(svc.calls) != 0 {
			t.Errorf("%s %s reached the service with auth off: %v", c[0], c[1], svc.calls)
		}
	}
	// Reading stays open.
	if resp := s.do("GET", "/api/heimdall/runs", "", bypassAuth); resp.status != 200 {
		t.Errorf("a read with auth off = %d", resp.status)
	}
	// The demo setting lets the mutation through.
	svc.allowUnauth = true
	if resp := s.do("POST", "/api/heimdall/runs", validRunBody, bypassAuth); resp.status != 202 {
		t.Errorf("with allow_unauthenticated_mutations = %d %s", resp.status, resp.body)
	}
	if resp := s.do("POST", "/api/heimdall/runs/run_1/cancel", "", bypassAuth); resp.status != 200 {
		t.Errorf("cancel with allow_unauthenticated_mutations = %d", resp.status)
	}
	// With auth on, the setting does not matter.
	svc.allowUnauth = false
	if resp := s.do("POST", "/api/heimdall/runs", validRunBody); resp.status != 202 {
		t.Errorf("with auth on = %d %s", resp.status, resp.body)
	}
}

func TestHeimdall_StartRunParsesTheBody(t *testing.T) {
	svc := &fakeHeimdall{}
	s := newHeimdallServer(svc)
	resp := s.do("POST", "/api/heimdall/runs", validRunBody)
	if resp.status != 202 {
		t.Fatalf("status = %d %s", resp.status, resp.body)
	}
	run := resp.json(t)
	if run["id"] != "run_new" || run["status"] != "queued" || run["warnings"] == nil {
		t.Errorf("response = %v; want the queued run with its warnings", run)
	}

	req := svc.startReq
	if req.Name != "Try demo-fast for refunds" || req.Baseline.Model != "demo-baseline" || req.Candidate.Model != "demo-fast" || req.Candidate.Provider != "openai" {
		t.Errorf("routes = %+v", req)
	}
	if len(req.Selection.SessionIDs) != 2 || req.Selection.Limit != 5 || req.Params.Margin != 0.1 || req.Params.MinSessions != 30 {
		t.Errorf("selection %+v, params %+v", req.Selection, req.Params)
	}
	if len(req.ToolRules) != 1 || !req.ToolRules[0].Protected || req.ToolRules[0].RequiresBefore[0] != "shop-check_refund_eligibility" || req.ForbiddenTools[0] != "shop-delete_customer" {
		t.Errorf("rules = %+v %v", req.ToolRules, req.ForbiddenTools)
	}
	if req.MaxUSD != 5 || req.MaxCalls != nil || !req.DataEgressAck {
		t.Errorf("budget %v, calls %v, ack %v", req.MaxUSD, req.MaxCalls, req.DataEgressAck)
	}

	// A time window parses as RFC 3339.
	window := `{"baseline":{"provider":"openai","model":"a"},"candidate":{"provider":"openai","model":"b"},"max_usd":1,"max_calls":120,
	  "selection":{"start_time":"2026-11-12T00:00:00Z","end_time":"2026-11-12T23:59:59Z"}}`
	if resp := s.do("POST", "/api/heimdall/runs", window); resp.status != 202 {
		t.Fatalf("window = %d %s", resp.status, resp.body)
	}
	if svc.startReq.Selection.StartTime == nil || svc.startReq.Selection.StartTime.Hour() != 0 || svc.startReq.Selection.EndTime == nil || *svc.startReq.MaxCalls != 120 {
		t.Errorf("request = %+v", svc.startReq)
	}
}

func TestHeimdall_StartRunRejectsABadBody(t *testing.T) {
	svc := &fakeHeimdall{}
	s := newHeimdallServer(svc)
	for name, body := range map[string]string{
		"not json":             `{"name": `,
		"a misspelt field":     `{"baseline":{"provider":"openai","model":"a"},"candidate":{"provider":"openai","model":"b"},"max_dollars":5}`,
		"a misspelt rule key":  `{"tool_rules":[{"tool":"x","protectd":true}]}`,
		"a wrong type":         `{"max_usd":"five"}`,
		"a bad time":           `{"selection":{"start_time":"yesterday"}}`,
		"two values":           validRunBody + validRunBody,
		"an array":             `[]`,
		"a string":             `"hello"`,
		"an empty body":        ``,
		"a negative call cap":  `{"max_calls":"x"}`,
		"a non-string session": `{"selection":{"session_ids":[1,2]}}`,
	} {
		svc.calls = nil
		resp := s.do("POST", "/api/heimdall/runs", body)
		if resp.status != 400 || !strings.HasPrefix(resp.message(t), "invalid_request") {
			t.Errorf("%s: %d %s; want 400 invalid_request", name, resp.status, resp.body)
		}
		if len(svc.calls) != 0 {
			t.Errorf("%s: a bad body reached the service", name)
		}
	}
}

func TestHeimdall_ServiceErrorsKeepTheirStatusAndCode(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		prefix string
	}{
		{&heimdall.Error{Status: 409, Code: "run_active", Message: "run r1 is still going"}, 409, "run_active: run r1"},
		{&heimdall.Error{Status: 409, Code: "estimate_exceeds_budget", Message: "about $6.66"}, 409, "estimate_exceeds_budget:"},
		{&heimdall.Error{Status: 400, Code: "egress_not_acknowledged", Message: "x"}, 400, "egress_not_acknowledged:"},
		{&heimdall.Error{Status: 400, Code: "unsupported_candidate", Message: "x"}, 400, "unsupported_candidate:"},
		{&heimdall.Error{Status: 404, Code: "not_found", Message: "no run"}, 404, "not_found: no run"},
		{&heimdall.Error{Status: 409, Code: "run_not_active", Message: "x"}, 409, "run_not_active:"},
		{errors.New("the database is on fire"), 500, "internal: the database is on fire"},
		{&heimdall.Error{Status: 500, Code: "internal", Message: "x"}, 500, "internal: x"},
	} {
		svc := &fakeHeimdall{err: tc.err}
		s := newHeimdallServer(svc)
		for _, c := range [][3]string{
			{"GET", "/api/heimdall/status", ""}, {"GET", "/api/heimdall/sessions", ""}, {"GET", "/api/heimdall/sessions/x", ""},
			{"POST", "/api/heimdall/runs", validRunBody}, {"GET", "/api/heimdall/runs", ""}, {"GET", "/api/heimdall/runs/x", ""},
			{"POST", "/api/heimdall/runs/x/cancel", ""}, {"GET", "/api/heimdall/runs/x/sessions", ""}, {"GET", "/api/heimdall/runs/x/sessions/y", ""},
		} {
			resp := s.do(c[0], c[1], c[2])
			if resp.status != tc.status || !strings.HasPrefix(resp.message(t), tc.prefix) {
				t.Errorf("%v on %s %s = %d %q; want %d %q", tc.err, c[0], c[1], resp.status, resp.body, tc.status, tc.prefix)
			}
		}
	}
}

func TestHeimdall_QueryArguments(t *testing.T) {
	svc := &fakeHeimdall{}
	s := newHeimdallServer(svc)

	q := url.Values{"limit": {"10"}, "offset": {"20"}, "provider": {"openai"}, "model": {"demo-baseline"}, "start_time": {"2026-11-12T10:00:00Z"}, "end_time": {"2026-11-12T11:30:00+05:30"}}
	if resp := s.do("GET", "/api/heimdall/sessions?"+q.Encode(), ""); resp.status != 200 {
		t.Fatalf("sessions = %d %s", resp.status, resp.body)
	}
	sq := svc.sessionsQuery
	if sq.Limit != 10 || sq.Offset != 20 || sq.Provider != "openai" || sq.Model != "demo-baseline" ||
		sq.StartTime == nil || !sq.StartTime.Equal(time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)) || sq.EndTime == nil || sq.EndTime.UTC().Hour() != 6 {
		t.Errorf("sessions query = %+v", sq)
	}

	s.do("GET", "/api/heimdall/runs?status=complete&verdict=FAIL&limit=5&offset=5", "")
	if svc.runsQuery != (heimdall.RunQuery{Status: "complete", Verdict: "FAIL", Limit: 5, Offset: 5}) {
		t.Errorf("runs query = %+v", svc.runsQuery)
	}
	s.do("GET", "/api/heimdall/runs/r1/sessions?filter=critical&limit=3&offset=6", "")
	if svc.runSessionsQ != (heimdall.RunSessionQuery{Filter: "critical", Limit: 3, Offset: 6}) {
		t.Errorf("run sessions query = %+v", svc.runSessionsQ)
	}
	// Nothing given: the service's own defaults apply.
	s.do("GET", "/api/heimdall/runs", "")
	if svc.runsQuery != (heimdall.RunQuery{}) {
		t.Errorf("empty runs query = %+v", svc.runsQuery)
	}

	for _, uri := range []string{
		"/api/heimdall/sessions?limit=abc", "/api/heimdall/sessions?limit=-1", "/api/heimdall/sessions?offset=1.5",
		"/api/heimdall/sessions?start_time=yesterday", "/api/heimdall/sessions?end_time=2026-11-12",
		"/api/heimdall/runs?limit=ten", "/api/heimdall/runs?offset=-3", "/api/heimdall/runs/r1/sessions?limit=x",
	} {
		svc.calls = nil
		resp := s.do("GET", uri, "")
		if resp.status != 400 || !strings.HasPrefix(resp.message(t), "invalid_request") {
			t.Errorf("%s = %d %s; want 400 invalid_request", uri, resp.status, resp.body)
		}
		if len(svc.calls) != 0 {
			t.Errorf("%s reached the service", uri)
		}
	}
}

func TestHeimdall_ResponsesFollowTheContract(t *testing.T) {
	s := newHeimdallServer(&fakeHeimdall{})

	st := s.do("GET", "/api/heimdall/status", "").json(t)
	req, _ := st["requirements"].(map[string]any)
	for _, key := range []string{"logs_store", "content_logging", "config_store", "object_storage_hidden_content"} {
		if _, ok := req[key]; !ok {
			t.Errorf("status.requirements lacks %q: %v", key, st)
		}
	}
	if st["enabled"] != true || st["active_run_id"] != "run_1" {
		t.Errorf("status = %v", st)
	}

	sessions := s.do("GET", "/api/heimdall/sessions", "").json(t)
	list, _ := sessions["sessions"].([]any)
	first, _ := list[0].(map[string]any)
	for _, key := range []string{"session_id", "provider", "model", "started_at", "turns", "total_cost"} {
		if _, ok := first[key]; !ok {
			t.Errorf("session lacks %q: %v", key, first)
		}
	}
	if _, ok := sessions["count"]; !ok || sessions["limit"] == nil {
		t.Errorf("sessions page = %v", sessions)
	}

	run := s.do("GET", "/api/heimdall/runs/r9", "").json(t)
	for _, key := range []string{"id", "status", "stage", "baseline", "candidate", "progress", "selection", "spend_usd", "max_usd", "decision", "created_at", "finished_at"} {
		if _, ok := run[key]; !ok {
			t.Errorf("run lacks %q: %v", key, run)
		}
	}
	rs := s.do("GET", "/api/heimdall/runs/r9/sessions", "").json(t)
	row := rs["sessions"].([]any)[0].(map[string]any)
	for _, key := range []string{"session_id", "eligibility", "baseline_divergence", "candidate_divergence", "delta", "critical_count", "first_divergence"} {
		if _, ok := row[key]; !ok {
			t.Errorf("run session lacks %q: %v", key, row)
		}
	}
	if s.do("GET", "/api/heimdall/runs/r9/sessions/s1", "").json(t)["session_id"] != "s1" {
		t.Error("run session detail")
	}
}

func TestHeimdall_BlankPathValues(t *testing.T) {
	svc := &fakeHeimdall{}
	h := NewHeimdallHandler(func() HeimdallService { return svc })
	for name, call := range map[string]func(*fasthttp.RequestCtx){
		"getSession": h.getSession, "getRun": h.getRun, "cancelRun": h.cancelRun, "listRunSessions": h.listRunSessions, "getRunSession": h.getRunSession,
	} {
		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue("id", "   ")
		call(ctx)
		if ctx.Response.StatusCode() != 400 {
			t.Errorf("%s with a blank id = %d, want 400", name, ctx.Response.StatusCode())
		}
	}
	if len(svc.calls) != 0 {
		t.Errorf("a blank id reached the service: %v", svc.calls)
	}
}
