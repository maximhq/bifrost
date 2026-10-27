package azure

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

var _ schemas.LiveProvider = (*AzureProvider)(nil)

func liveTestKey(endpoint string) schemas.Key {
	return schemas.Key{
		Value:          *schemas.NewSecretVar("azure-test-key"),
		AzureKeyConfig: &schemas.AzureKeyConfig{Endpoint: *schemas.NewSecretVar(endpoint)},
	}
}

func TestLiveWebSocketURL(t *testing.T) {
	t.Parallel()

	provider := &AzureProvider{}
	key := liveTestKey("https://example.openai.azure.com/")
	cases := []struct {
		kind      schemas.LiveConnectionKind
		sessionID string
		want      string
	}{
		{schemas.LiveConnectionPrimary, "", "wss://example.openai.azure.com/openai/v1/live/sessions"},
		{schemas.LiveConnectionSideband, "sess_123", "wss://example.openai.azure.com/openai/v1/live/sessions/sess_123/attach"},
	}
	for _, tc := range cases {
		got, err := provider.LiveWebSocketURL(key, tc.kind, tc.sessionID)
		if err != nil || got != tc.want {
			t.Fatalf("LiveWebSocketURL(%s, %q) = %q, %v; want %q", tc.kind, tc.sessionID, got, err, tc.want)
		}
	}

	if got, err := provider.LiveWebSocketURL(liveTestKey("http://localhost:9000"), schemas.LiveConnectionPrimary, ""); err != nil || got != "ws://localhost:9000/openai/v1/live/sessions" {
		t.Fatalf("LiveWebSocketURL() = %q, %v", got, err)
	}
	if got, err := provider.LiveWebSocketURL(key, schemas.LiveConnectionFork, "sess_123"); err == nil {
		t.Fatalf("LiveWebSocketURL(fork) = %q, want error: Azure has no fork route", got)
	}
	if got, err := provider.LiveWebSocketURL(key, schemas.LiveConnectionKind("bogus"), "sess_123"); err == nil {
		t.Fatalf("LiveWebSocketURL(bogus) = %q, want error", got)
	}
	for _, sessionID := range []string{"", "..", "sess_1/../../responses", "sess_1%2Fattach", "sess_1?x=1", "sess_1#frag"} {
		if got, err := provider.LiveWebSocketURL(key, schemas.LiveConnectionSideband, sessionID); err == nil {
			t.Fatalf("LiveWebSocketURL(sideband, %q) = %q, want error", sessionID, got)
		}
	}
}

func TestLiveRejectsKeyWithoutEndpoint(t *testing.T) {
	t.Parallel()

	provider := &AzureProvider{client: &fasthttp.Client{}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	for _, key := range []schemas.Key{{Value: *schemas.NewSecretVar("azure-test-key")}, liveTestKey("  ")} {
		if got, err := provider.LiveWebSocketURL(key, schemas.LiveConnectionPrimary, ""); err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusBadRequest {
			t.Fatalf("LiveWebSocketURL() = %q, %+v; want a 400", got, err)
		}
		if _, err := provider.CreateLiveWebRTCSession(ctx, key, []byte(`{}`)); err == nil || err.StatusCode == nil || *err.StatusCode != http.StatusBadRequest {
			t.Fatalf("CreateLiveWebRTCSession() error = %+v; want a 400", err)
		}
	}
}

func TestLiveHeaders(t *testing.T) {
	t.Parallel()

	provider := &AzureProvider{networkConfig: schemas.NetworkConfig{ExtraHeaders: schemas.NewSecretVarMap(map[string]string{"X-Extra": "1"})}}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	headers, err := provider.LiveHeaders(ctx, liveTestKey("https://example.openai.azure.com"))
	if err != nil {
		t.Fatalf("LiveHeaders() error = %v", err)
	}
	if headers["api-key"] != "azure-test-key" || headers["Authorization"] != "" || headers["X-Extra"] != "1" {
		t.Fatalf("LiveHeaders() = %v", headers)
	}

	ctx.SetValue(AzureAuthorizationTokenKey, "entra-token")
	headers, err = provider.LiveHeaders(ctx, liveTestKey("https://example.openai.azure.com"))
	if err != nil {
		t.Fatalf("LiveHeaders() error = %v", err)
	}
	if headers["Authorization"] != "Bearer entra-token" || headers["api-key"] != "" {
		t.Fatalf("LiveHeaders() with a bearer token = %v", headers)
	}
}

func TestCreateLiveWebRTCSession(t *testing.T) {
	t.Parallel()

	body := []byte(`{"session":{"model":"live-voice"},"transport":{"type":"webrtc","sdp":"v=0 offer"}}`)
	var status int
	var reply string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/openai/v1/live/sessions" || r.URL.RawQuery != "" || r.Header.Get("api-key") != "azure-test-key" ||
			r.Header.Get("Content-Type") != "application/json" || string(got) != string(body) {
			t.Errorf("unexpected request %s %s?%s api-key=%q type=%q body=%s", r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("api-key"), r.Header.Get("Content-Type"), got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)

	provider := &AzureProvider{client: &fasthttp.Client{}}
	key := liveTestKey(srv.URL + "/")
	newCtx := func() *schemas.BifrostContext {
		return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	}

	status, reply = http.StatusCreated, `{"session":{"id":"sess_123"},"transport":{"type":"webrtc","sdp":"v=0 answer"}}`
	created, bifrostErr := provider.CreateLiveWebRTCSession(newCtx(), key, body)
	if bifrostErr != nil {
		t.Fatalf("CreateLiveWebRTCSession() error = %v", bifrostErr.Error)
	}
	if created.Session.ID != "sess_123" || created.Transport.SDP != "v=0 answer" {
		t.Fatalf("CreateLiveWebRTCSession() = %+v", created)
	}

	status, reply = http.StatusBadRequest, `{"error":{"type":"invalid_request_error","code":"DeploymentNotFound","message":"The deployment does not exist."}}`
	_, bifrostErr = provider.CreateLiveWebRTCSession(newCtx(), key, body)
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest ||
		bifrostErr.ExtraFields.RequestType != schemas.LiveRequest || bifrostErr.ExtraFields.RoutingInfo.Provider != schemas.Azure ||
		bifrostErr.Error.Message != "The deployment does not exist." {
		t.Fatalf("upstream error = %+v", bifrostErr)
	}

	// A 201 is a created, billable session even when its answer is unusable: the caller settles it.
	status, reply = http.StatusCreated, `{"session":{"id":"sess_123"}}`
	created, bifrostErr = provider.CreateLiveWebRTCSession(newCtx(), key, body)
	if bifrostErr != nil {
		t.Fatalf("a created session without an SDP answer must be returned, got error %v", bifrostErr.Error)
	}
	if created.Session.ID != "sess_123" || (created.Transport != nil && created.Transport.SDP != "") {
		t.Fatalf("CreateLiveWebRTCSession() = %+v", created)
	}

	status, reply = http.StatusCreated, `{"transport":{"type":"webrtc","sdp":"v=0 answer"}}`
	if _, bifrostErr = provider.CreateLiveWebRTCSession(newCtx(), key, body); bifrostErr == nil {
		t.Fatal("a create response without a session id must be an error")
	}
}

func TestLiveSessionContentIsUnsupported(t *testing.T) {
	t.Parallel()

	provider := &AzureProvider{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	content, bifrostErr := provider.LiveSessionContent(ctx, liveTestKey("https://example.openai.azure.com"), "sess_123")
	if content != nil || bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "unsupported_operation" {
		t.Fatalf("LiveSessionContent() = %+v, %+v; want unsupported_operation", content, bifrostErr)
	}
}
