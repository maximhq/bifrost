package otel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/network/proxytest"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestOtelClientHTTPUsesGlobalProxy pins that the OTLP/HTTP trace exporter honours the
// global proxy when it is enabled for API traffic. It used to follow only the
// environment proxy.
func TestOtelClientHTTPUsesGlobalProxy(t *testing.T) {
	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)
	set := proxytest.NewSet(t)
	network.SetDefaultHTTPClientFactory(network.NewHTTPClientFactory(&network.GlobalProxyConfig{
		Enabled: true, Type: network.GlobalProxyTypeHTTP, URL: "http://127.0.0.1:" + set.Config.Port(), EnableForAPI: true,
	}, nil))
	t.Cleanup(func() { network.SetDefaultHTTPClientFactory(nil) })

	client, err := NewOtelClientHTTP("http://203.0.113.10:4318", nil, "", true, 3*time.Second)
	if err != nil {
		t.Fatalf("NewOtelClientHTTP: %v", err)
	}
	defer client.Close()
	_ = client.Emit(context.Background(), nil)
	proxytest.AssertRoute(t, set, proxytest.Route{Proxy: "config"}, "203.0.113.10:4318", nil)
}

const (
	locationMarker = "redirect-location-marker"
	bodyMarker     = "redirect-body-marker"
)

var redirectCodes = []int{
	http.StatusMultipleChoices,
	http.StatusMovedPermanently,
	http.StatusFound,
	http.StatusSeeOther,
	http.StatusNotModified,
	http.StatusTemporaryRedirect,
	http.StatusPermanentRedirect,
}

// redirectPair starts a target and a collector that answers every request with code, a
// Location pointing at the target and a body. Both count their requests.
func redirectPair(t *testing.T, code int) (collectorURL string, collectorHits, targetHits *atomic.Int32) {
	t.Helper()
	collectorHits, targetHits = &atomic.Int32{}, &atomic.Int32{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
	}))
	t.Cleanup(target.Close)

	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		collectorHits.Add(1)
		w.Header().Set("Location", target.URL+"/"+locationMarker)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(bodyMarker))
	}))
	t.Cleanup(collector.Close)
	return collector.URL, collectorHits, targetHits
}

func assertRedirectRefused(t *testing.T, err error, collectorHits, targetHits *atomic.Int32) {
	t.Helper()
	if err == nil {
		t.Fatal("export succeeded across a redirect, want an error")
	}
	if !strings.Contains(err.Error(), errRedirectRefused.Error()) {
		t.Errorf("error %q does not report the refused redirect", err)
	}
	for _, marker := range []string{locationMarker, bodyMarker} {
		if strings.Contains(err.Error(), marker) {
			t.Errorf("error %q carries %q", err, marker)
		}
	}
	if got := targetHits.Load(); got != 0 {
		t.Errorf("redirect target saw %d requests, want 0", got)
	}
	if got := collectorHits.Load(); got != 1 {
		t.Errorf("collector saw %d requests, want exactly 1 (no retry)", got)
	}
}

func TestTraceExportRefusesRedirects(t *testing.T) {
	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)
	for _, code := range redirectCodes {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			collectorURL, collectorHits, targetHits := redirectPair(t, code)

			client, err := NewOtelClientHTTP(collectorURL, nil, "", true, 3*time.Second)
			if err != nil {
				t.Fatalf("NewOtelClientHTTP: %v", err)
			}
			defer client.Close()

			assertRedirectRefused(t, client.Emit(context.Background(), nil), collectorHits, targetHits)
		})
	}
}
