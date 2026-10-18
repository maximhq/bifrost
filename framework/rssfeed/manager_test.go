package rssfeed

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newFeedServer(t *testing.T, hits *atomic.Int32, body func() (int, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		status, payload := body()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestManagerCachesAndCoalesces(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	srv := newFeedServer(t, &hits, func() (int, string) {
		<-release
		return http.StatusOK, rss2Fixture
	})
	m := NewManagerWithClient(srv.Client())

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Get(context.Background(), srv.URL); err != nil {
				t.Errorf("Get: %v", err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := hits.Load(); got != 1 {
		t.Fatalf("concurrent Gets should share one fetch, got %d", got)
	}

	if _, err := m.Get(context.Background(), srv.URL); err != nil {
		t.Fatalf("cached Get: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("fresh entry should be served from cache, got %d fetches", got)
	}

	now := time.Now()
	m.now = func() time.Time { return now.Add(DefaultTTL + time.Second) }
	if _, err := m.Get(context.Background(), srv.URL); err != nil {
		t.Fatalf("expired Get: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("expired entry should refetch, got %d fetches", got)
	}

	m.Forget(srv.URL)
	m.now = time.Now
	if _, err := m.Get(context.Background(), srv.URL); err != nil {
		t.Fatalf("Get after Forget: %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("Forget should drop the entry, got %d fetches", got)
	}
}

// TestManagerSharedFetchIgnoresCallerCancellation pins that a caller whose
// context is already cancelled neither fails the shared fetch nor leaves a
// cached error behind for the callers after it.
func TestManagerSharedFetchIgnoresCallerCancellation(t *testing.T) {
	var hits atomic.Int32
	srv := newFeedServer(t, &hits, func() (int, string) { return http.StatusOK, rss2Fixture })
	m := NewManagerWithClient(srv.Client())

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	items, err := m.Get(cancelled, srv.URL)
	if err != nil || len(items) == 0 {
		t.Fatalf("cancelled caller should still get the shared fetch, got %d items, err %v", len(items), err)
	}
	if _, err := m.Get(context.Background(), srv.URL); err != nil {
		t.Fatalf("next caller must not see a cached cancellation: %v", err)
	}
}

// TestManagerForgetDuringFetchDropsResult pins that a fetch already in flight
// when its URL is forgotten does not write its result back into the cache.
func TestManagerForgetDuringFetchDropsResult(t *testing.T) {
	var hits atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	srv := newFeedServer(t, &hits, func() (int, string) {
		if hits.Load() == 1 {
			close(started)
			<-release
		}
		return http.StatusOK, rss2Fixture
	})
	m := NewManagerWithClient(srv.Client())

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := m.Get(context.Background(), srv.URL); err != nil {
			t.Errorf("Get: %v", err)
		}
	}()
	<-started
	m.Forget(srv.URL)
	close(release)
	<-done

	m.mu.RLock()
	_, cached := m.cache[srv.URL]
	m.mu.RUnlock()
	if cached {
		t.Fatal("a fetch that was in flight when Forget ran must not repopulate the cache")
	}
	if _, err := m.Get(context.Background(), srv.URL); err != nil {
		t.Fatalf("Get after Forget: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("Get after Forget should refetch, got %d fetches", got)
	}
}

func TestManagerFetchErrors(t *testing.T) {
	var hits atomic.Int32
	status, payload := http.StatusOK, ""
	srv := newFeedServer(t, &hits, func() (int, string) { return status, payload })
	m := NewManagerWithClient(srv.Client())

	status, payload = http.StatusNotFound, "missing"
	if _, err := m.Fetch(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404: expected status error, got %v", err)
	}

	status, payload = http.StatusOK, "<html><body>landing page</body></html>"
	if _, err := m.Fetch(context.Background(), srv.URL); !errors.Is(err, ErrNotAFeed) {
		t.Errorf("html: expected ErrNotAFeed, got %v", err)
	}

	status, payload = http.StatusOK, "<rss><channel>"+strings.Repeat("x", maxFeedBytes)+"</channel></rss>"
	if _, err := m.Fetch(context.Background(), srv.URL); !errors.Is(err, ErrFeedTooLarge) {
		t.Errorf("oversize: expected ErrFeedTooLarge, got %v", err)
	}
}

func TestManagerCachesErrorsBriefly(t *testing.T) {
	var hits atomic.Int32
	srv := newFeedServer(t, &hits, func() (int, string) { return http.StatusInternalServerError, "" })
	m := NewManagerWithClient(srv.Client())
	if _, err := m.Get(context.Background(), srv.URL); err == nil {
		t.Fatal("expected error")
	}
	if _, err := m.Get(context.Background(), srv.URL); err == nil {
		t.Fatal("expected cached error")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("error should be cached, got %d fetches", got)
	}
	now := time.Now()
	m.now = func() time.Time { return now.Add(errorTTL + time.Second) }
	_, _ = m.Get(context.Background(), srv.URL)
	if got := hits.Load(); got != 2 {
		t.Fatalf("error entry should expire after errorTTL, got %d fetches", got)
	}
}

// TestNewManagerRefusesPrivateTargets pins that every non-public or
// credential-bearing feed URL is refused before any request is sent, so the
// caller's own URL is always reported as invalid rather than failing at dial
// time, and the dial-time guard is left for redirects and DNS rebinding.
func TestNewManagerRefusesPrivateTargets(t *testing.T) {
	m := NewManager()
	for _, target := range []string{
		"http://127.0.0.1:1/feed",
		"http://localhost:1/feed",
		"http://[::1]:1/feed",
		"http://10.0.0.5/feed",
		"http://169.254.169.254/latest/meta-data",
		"http://[::ffff:169.254.169.254]/latest/meta-data",
		"http://user:secret@93.184.216.34/feed",
		"https://token@93.184.216.34/feed",
		"file:///etc/passwd",
		"ftp://93.184.216.34/feed",
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := m.Fetch(ctx, target)
		cancel()
		if err == nil || !strings.HasPrefix(err.Error(), "invalid feed url:") {
			t.Errorf("%s: expected the url to be refused before fetching, got %v", target, err)
		}
	}
}

// TestManagerCapsDecompressedBody pins that the 2 MB cap applies to the
// decompressed body, so a small gzip bomb is refused as too large.
func TestManagerCapsDecompressedBody(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte("<rss><channel>" + strings.Repeat("x", 3*maxFeedBytes) + "</channel></rss>"))
	_ = zw.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(buf.Bytes())
	}))
	t.Cleanup(srv.Close)
	if buf.Len() > maxFeedBytes/10 {
		t.Fatalf("fixture should be a small compressed body, got %d bytes", buf.Len())
	}
	if _, err := NewManagerWithClient(srv.Client()).Fetch(context.Background(), srv.URL); !errors.Is(err, ErrFeedTooLarge) {
		t.Fatalf("expected ErrFeedTooLarge for a gzip body that expands past the cap, got %v", err)
	}
}
