package bifrost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestPassthroughRouteAllowed(t *testing.T) {
	post := func(path string) *schemas.BifrostPassthroughRequest {
		return &schemas.BifrostPassthroughRequest{Method: http.MethodPost, Path: path}
	}
	chatPlusFiles := &schemas.PassthroughPolicy{AllowedRoutes: []schemas.PassthroughRouteRule{
		{Methods: []string{http.MethodPost}, Path: "/v1/chat/completions"},
		{Methods: []string{http.MethodGet}, Path: "/v1/files/*"},
	}}
	genAIVersioned := &schemas.PassthroughPolicy{AllowedRoutes: []schemas.PassthroughRouteRule{{Path: "/v1beta/models/*:generateContent"}}}

	for _, tc := range []struct {
		name   string
		policy *schemas.PassthroughPolicy
		req    *schemas.BifrostPassthroughRequest
		want   bool
	}{
		{"nil policy forwards every route", nil, &schemas.BifrostPassthroughRequest{Method: http.MethodDelete, Path: "/v1/files/f1"}, true},
		{"empty policy forwards nothing", &schemas.PassthroughPolicy{}, post("/v1/chat/completions"), false},
		{"first rule allows its route", chatPlusFiles, post("/v1/chat/completions"), true},
		{"later rule allows its route", chatPlusFiles, &schemas.BifrostPassthroughRequest{Method: http.MethodGet, Path: "/v1/files/f1"}, true},
		{"unlisted route refused", chatPlusFiles, post("/v1/files"), false},
		{"rule keeps its method limit", chatPlusFiles, &schemas.BifrostPassthroughRequest{Method: http.MethodDelete, Path: "/v1/files/f1"}, false},
		{"genai rule includes the version segment", genAIVersioned, &schemas.BifrostPassthroughRequest{Method: http.MethodPost, APIVersion: "v1beta", Path: "/models/m:generateContent"}, true},
		{"genai rule refuses another version", genAIVersioned, &schemas.BifrostPassthroughRequest{Method: http.MethodPost, APIVersion: "v1", Path: "/models/m:generateContent"}, false},
		{"host-backed routes are not governed", &schemas.PassthroughPolicy{}, &schemas.BifrostPassthroughRequest{Method: http.MethodPost, Path: "/backend-api/codex/responses", UpstreamURL: "https://chatgpt.com"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := passthroughRouteAllowed(tc.policy, tc.req); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// A refused route is answered by the worker before key acquisition, so the upstream never sees it,
// while an allowed route and a provider without a policy are forwarded unchanged.
func TestPassthroughPolicyRefusesBeforeUpstream(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	takeHits := func() []string {
		mu.Lock()
		defer mu.Unlock()
		got := hits
		hits = nil
		return got
	}

	const restricted schemas.ModelProvider = "sidecar-restricted"
	const open schemas.ModelProvider = "sidecar-open"
	account := NewMockAccount()
	for _, provider := range []schemas.ModelProvider{restricted, open} {
		account.AddProviderWithBaseURL(provider, 2, 16, upstream.URL)
		account.SetCustomProviderConfig(provider, &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI})
		account.SetKeysForProvider(provider, []schemas.Key{scenarioKey(string(provider), 1)})
	}
	account.mu.Lock()
	account.configs[restricted].NetworkConfig.MaxRetries = 0
	account.configs[open].NetworkConfig.MaxRetries = 0
	account.configs[restricted].Passthrough = &schemas.PassthroughPolicy{AllowedRoutes: []schemas.PassthroughRouteRule{
		{Methods: []string{http.MethodPost}, Path: "/v1/models/*/predict"},
	}}
	account.mu.Unlock()
	client := scenarioClient(t, account, nil, nil)

	send := func(provider schemas.ModelProvider, method, path string) (*schemas.BifrostPassthroughResponse, *schemas.BifrostError) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		return client.Passthrough(ctx, provider, &schemas.BifrostPassthroughRequest{Method: method, Path: path, Body: []byte(`{}`), SafeHeaders: map[string]string{}})
	}

	resp, err := send(restricted, http.MethodPost, "/v1/models/m1/predict")
	if err != nil {
		t.Fatalf("allowed route refused: %s", err.GetErrorString())
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("allowed route status %d, want 200", resp.StatusCode)
	}
	if got := takeHits(); len(got) != 1 || got[0] != "POST /v1/models/m1/predict" {
		t.Fatalf("upstream hits %v, want the allowed route once", got)
	}

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/models/m1/predict"},
		{http.MethodPost, "/v1/files"},
		{http.MethodPost, "/v1/models/m1/predict/extra"},
	} {
		_, err := send(restricted, tc.method, tc.path)
		if err == nil {
			t.Fatalf("%s %s: want refused", tc.method, tc.path)
		}
		if err.StatusCode == nil || *err.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s: status %v, want 403", tc.method, tc.path, err.StatusCode)
		}
		if err.Error == nil || err.Error.Code == nil || *err.Error.Code != "passthrough_route_not_allowed" {
			t.Fatalf("%s %s: error %+v, want code passthrough_route_not_allowed", tc.method, tc.path, err.Error)
		}
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, err := client.PassthroughStream(ctx, restricted, &schemas.BifrostPassthroughRequest{Method: http.MethodPost, Path: "/v1/files", Body: []byte(`{}`), SafeHeaders: map[string]string{}}); err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusForbidden {
		t.Fatalf("stream to a refused route: got %+v, want 403", err)
	}
	if got := takeHits(); len(got) != 0 {
		t.Fatalf("refused routes reached the upstream: %v", got)
	}

	if _, err := send(open, http.MethodDelete, "/v1/files/f1"); err != nil {
		t.Fatalf("provider without a policy refused a route: %s", err.GetErrorString())
	}
	if got := takeHits(); len(got) != 1 || got[0] != "DELETE /v1/files/f1" {
		t.Fatalf("upstream hits %v, want the unrestricted route once", got)
	}
}

// The policy runs once per passthrough attempt. Only the provider's few rules are walked;
// it must not allocate.
func BenchmarkPassthroughRouteAllowed(b *testing.B) {
	policy := &schemas.PassthroughPolicy{AllowedRoutes: []schemas.PassthroughRouteRule{
		{Methods: []string{http.MethodPost}, Path: "/v1/chat/completions"},
		{Methods: []string{http.MethodGet, http.MethodDelete}, Path: "/v1/files/*"},
		{Path: "/v1/batches/**"},
	}}
	cases := map[string]*schemas.BifrostPassthroughRequest{
		"exact":    {Method: http.MethodPost, Path: "/v1/chat/completions"},
		"path":     {Method: http.MethodPost, Path: "/v1/batches/b1/cancel"},
		"refused":  {Method: http.MethodPost, Path: "/v1/fine_tuning/jobs"},
		"genai":    {Method: http.MethodPost, APIVersion: "v1beta", Path: "/models/gemini-2.5-flash:generateContent"},
		"nil-rule": {Method: http.MethodPost, Path: "/v1/chat/completions"},
	}
	for name, req := range cases {
		p := policy
		if name == "nil-rule" {
			p = nil
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				passthroughRouteAllowed(p, req)
			}
		})
	}
}
