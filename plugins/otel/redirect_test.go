package otel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// redirectPair starts a target that counts requests and a collector that answers every
// request with a 307 to it.
func redirectPair(t *testing.T) (collectorURL, locationMarker string, targetHits *atomic.Int32) {
	t.Helper()
	targetHits = &atomic.Int32{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
	}))
	t.Cleanup(target.Close)

	locationMarker = "redirect-location-marker"
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/"+locationMarker, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(collector.Close)
	return collector.URL, locationMarker, targetHits
}

func assertRedirectRefused(t *testing.T, err error, marker string, targetHits *atomic.Int32) {
	t.Helper()
	if err == nil {
		t.Fatal("export succeeded across a redirect, want an error")
	}
	if !strings.Contains(err.Error(), errRedirectRefused.Error()) {
		t.Errorf("error %q does not report the refused redirect", err)
	}
	if strings.Contains(err.Error(), marker) {
		t.Errorf("error %q carries the redirect Location", err)
	}
	if got := targetHits.Load(); got != 0 {
		t.Errorf("redirect target saw %d requests, want 0", got)
	}
}

func TestTraceExportRefusesRedirects(t *testing.T) {
	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)
	collectorURL, marker, targetHits := redirectPair(t)

	client, err := NewOtelClientHTTP(collectorURL, nil, "", true, 3*time.Second)
	if err != nil {
		t.Fatalf("NewOtelClientHTTP: %v", err)
	}
	defer client.Close()

	assertRedirectRefused(t, client.Emit(context.Background(), nil), marker, targetHits)
}

func TestMetricsExportRefusesRedirects(t *testing.T) {
	collectorURL, marker, targetHits := redirectPair(t)

	exp, err := createHTTPExporter(context.Background(), &MetricsConfig{Endpoint: collectorURL + "/v1/metrics", Insecure: true})
	if err != nil {
		t.Fatalf("createHTTPExporter: %v", err)
	}
	defer func() { _ = exp.Shutdown(context.Background()) }()

	assertRedirectRefused(t, exp.Export(context.Background(), oneMetric()), marker, targetHits)
}
