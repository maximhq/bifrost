package traceingest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func forwardProject(vendor, baseURL string) *tables.TableTraceProject {
	return &tables.TableTraceProject{ID: "bf-project", Name: "agent", Forward: &tables.TraceForwardTarget{Vendor: vendor, BaseURL: baseURL, Headers: map[string]string{"Authorization": "Bearer upstream-key"}, Enabled: true}}
}

// TestForwarderReplaysAfterAnUpstreamOutage pins forwarding: the request is replayed as the SDK
// sent it, with the upstream's own credential, and an upstream that is down is retried.
func TestForwarderReplaysAfterAnUpstreamOutage(t *testing.T) {
	var calls atomic.Int32
	var mu sync.Mutex
	var got struct {
		path, auth, contentType, parent string
		body                            []byte
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		mu.Lock()
		got.path, got.auth, got.contentType, got.parent = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Header.Get("x-custom")
		got.body, _ = io.ReadAll(r.Body)
		mu.Unlock()
	}))
	defer upstream.Close()
	forwarder := NewForwarder(nil, testLogger{})
	defer forwarder.Close()

	forwarder.Enqueue(forwardProject(logstore.TraceSourceLangfuse, upstream.URL+"/"), ForwardRequest{
		Source: logstore.TraceSourceLangfuse, Path: "/api/public/otel/v1/traces", ContentType: "application/x-protobuf",
		Headers: map[string]string{"x-custom": "kept", "Authorization": "Basic the-bifrost-key"}, Body: []byte{1, 2, 3},
	})
	// The first attempt fails with a 503. Run the retry now instead of waiting out its delay.
	require.Eventually(t, func() bool { return calls.Load() == 1 && forwarder.Pending() == 1 }, 3*time.Second, 10*time.Millisecond)
	forwarder.drain(time.Now().Add(time.Hour))
	require.Equal(t, int32(2), calls.Load())
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "/api/public/otel/v1/traces", got.path)
	assert.Equal(t, "Bearer upstream-key", got.auth, "the upstream sees its own credential, never the Bifrost tracing key")
	assert.Equal(t, "application/x-protobuf", got.contentType)
	assert.Equal(t, "kept", got.parent)
	assert.Equal(t, []byte{1, 2, 3}, got.body)
	assert.Zero(t, forwarder.Pending())
}

// TestForwarderSkipsWhatItShouldNotSend pins the guards: a project with no target, a disabled
// target, or a target of another vendor forwards nothing, and a request the upstream rejects for
// good is not retried.
func TestForwarderSkipsWhatItShouldNotSend(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()
	forwarder := NewForwarder(nil, testLogger{})
	defer forwarder.Close()
	request := ForwardRequest{Source: logstore.TraceSourceOTel, Path: "/v1/traces", Body: []byte("x")}

	forwarder.Enqueue(&tables.TableTraceProject{ID: "p"}, request)
	disabled := forwardProject(logstore.TraceSourceOTel, upstream.URL)
	disabled.Forward.Enabled = false
	forwarder.Enqueue(disabled, request)
	forwarder.Enqueue(forwardProject(logstore.TraceSourceBraintrust, upstream.URL), request)
	assert.Zero(t, forwarder.Pending())

	forwarder.Enqueue(forwardProject(logstore.TraceSourceOTel, upstream.URL), request)
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return forwarder.Pending() == 0 }, time.Second, 10*time.Millisecond, "a 401 is final: the same bytes would be refused again")
}

// TestForwarderRewritesBraintrustProjectIDs pins the Braintrust replay: Braintrust does not know
// the project id Bifrost issued, so the project is registered upstream by name and each row is
// sent with the upstream's id. Experiment rows and other projects' rows are left out.
func TestForwarderRewritesBraintrustProjectIDs(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var flushed map[string]any
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/apikey/login":
			_, _ = w.Write([]byte(`{"org_info":[{"api_url":"` + upstream.URL + `/data"}]}`))
		case "/api/project/register":
			_, _ = w.Write([]byte(`{"project":{"id":"bt-upstream-id","name":"agent"}}`))
		case "/data/logs3":
			body, _ := io.ReadAll(r.Body)
			_ = sonic.Unmarshal(body, &flushed)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer upstream.Close()
	forwarder := NewForwarder(nil, testLogger{})
	defer forwarder.Close()
	project := forwardProject(logstore.TraceSourceBraintrust, upstream.URL)
	body := []byte(`{"rows":[
		{"id":"r1","span_id":"s1","root_span_id":"s1","project_id":"bf-project","input":"hi"},
		{"id":"r2","span_id":"s2","root_span_id":"s2","project_id":"bf-project","experiment_id":"exp-bf-project-aaaaaaaaaaaa"},
		{"id":"r3","span_id":"s3","root_span_id":"s3","project_id":"another-project"}
	],"api_version":2}`)
	forwarder.Enqueue(project, ForwardRequest{Source: logstore.TraceSourceBraintrust, Path: "/logs3", Body: body})
	forwarder.Enqueue(project, ForwardRequest{Source: logstore.TraceSourceBraintrust, Path: "/logs3", Body: body})
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(paths) == 4
	}, 3*time.Second, 10*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"/api/apikey/login", "/api/project/register", "/data/logs3", "/data/logs3"}, paths, "login and register happen once, then are reused")
	rows := flushed["rows"].([]any)
	require.Len(t, rows, 1)
	assert.Equal(t, "bt-upstream-id", rows[0].(map[string]any)["project_id"])
	assert.Equal(t, "r1", rows[0].(map[string]any)["id"])
}
