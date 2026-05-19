package utils

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

// TestBuildStreamingClient_ZerosReadWriteTimeout verifies the streaming client
// has ReadTimeout=0 / WriteTimeout=0 / MaxConnDuration=0 while preserving other
// config from the base.
func TestBuildStreamingClient_ZerosReadWriteTimeout(t *testing.T) {
	base := &fasthttp.Client{
		ReadTimeout:        30 * time.Second,
		WriteTimeout:       30 * time.Second,
		MaxConnDuration:    5 * time.Minute,
		MaxConnWaitTimeout: 15 * time.Second,
		MaxConnsPerHost:    123,
	}
	ConfigureDialer(base)

	stream := BuildStreamingClient(base)

	if stream.ReadTimeout != 0 {
		t.Errorf("ReadTimeout: got %v, want 0", stream.ReadTimeout)
	}
	if stream.WriteTimeout != 0 {
		t.Errorf("WriteTimeout: got %v, want 0", stream.WriteTimeout)
	}
	if stream.MaxConnDuration != 0 {
		t.Errorf("MaxConnDuration: got %v, want 0", stream.MaxConnDuration)
	}
	if !stream.StreamResponseBody {
		t.Error("StreamResponseBody: got false, want true")
	}
	if stream.MaxConnWaitTimeout != base.MaxConnWaitTimeout {
		t.Errorf("MaxConnWaitTimeout should be preserved: got %v, want %v",
			stream.MaxConnWaitTimeout, base.MaxConnWaitTimeout)
	}
	if stream.MaxConnsPerHost != base.MaxConnsPerHost {
		t.Errorf("MaxConnsPerHost should be preserved: got %v, want %v",
			stream.MaxConnsPerHost, base.MaxConnsPerHost)
	}
}

// TestBuildStreamingClient_BaseUnchanged verifies BuildStreamingClient does not
// mutate the base client (since unary callers still need the 30s timeout).
func TestBuildStreamingClient_BaseUnchanged(t *testing.T) {
	base := &fasthttp.Client{
		ReadTimeout:     30 * time.Second,
		WriteTimeout:    30 * time.Second,
		MaxConnDuration: 5 * time.Minute,
	}
	_ = BuildStreamingClient(base)

	if base.ReadTimeout != 30*time.Second {
		t.Errorf("base ReadTimeout mutated: got %v, want 30s", base.ReadTimeout)
	}
	if base.MaxConnDuration != 5*time.Minute {
		t.Errorf("base MaxConnDuration mutated: got %v, want 5m", base.MaxConnDuration)
	}
}

// TestBuildStreamingClient_LongStreamSurvives verifies that a stream sending
// chunks every 500ms for 2.5s (total) is not killed by the base client's 1s
// ReadTimeout. Before the fix, fasthttp would abort at ~1s.
func TestBuildStreamingClient_LongStreamSurvives(t *testing.T) {
	const chunkInterval = 500 * time.Millisecond
	const totalChunks = 5 // 2.5s total, well past base ReadTimeout=1s

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < totalChunks; i++ {
			fmt.Fprintf(w, "data: chunk-%d\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(chunkInterval)
		}
	}))
	defer srv.Close()

	base := &fasthttp.Client{
		ReadTimeout:  1 * time.Second, // would abort the stream without the fix
		WriteTimeout: 1 * time.Second,
	}
	ConfigureDialer(base)
	stream := BuildStreamingClient(base)

	req := fasthttp.AcquireRequest()
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(srv.URL)
	req.Header.SetMethod(http.MethodGet)
	resp.StreamBody = true

	if err := stream.Do(req, resp); err != nil {
		t.Fatalf("Do: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode())
	}

	scanner := bufio.NewScanner(resp.BodyStream())
	got := 0
	for scanner.Scan() {
		if line := scanner.Text(); len(line) >= 5 && line[:5] == "data:" {
			got++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}
	if got != totalChunks {
		t.Errorf("chunks received: got %d, want %d (stream was likely killed early)", got, totalChunks)
	}
}

// TestBuildStreamingHTTPClient_ZerosTimeout verifies the net/http streaming
// client has Timeout=0 and shares the base's Transport.
func TestBuildStreamingHTTPClient_ZerosTimeout(t *testing.T) {
	transport := &http.Transport{ResponseHeaderTimeout: 10 * time.Second}
	base := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	stream := BuildStreamingHTTPClient(base)

	if stream.Timeout != 0 {
		t.Errorf("Timeout: got %v, want 0", stream.Timeout)
	}
	if stream.Transport != base.Transport {
		t.Error("Transport: streaming client should share base's Transport")
	}
	if base.Timeout != 30*time.Second {
		t.Errorf("base Timeout mutated: got %v, want 30s", base.Timeout)
	}
}

// TestBuildStreamingHTTPClient_Nil verifies nil base returns empty client
// (not a panic).
func TestBuildStreamingHTTPClient_Nil(t *testing.T) {
	stream := BuildStreamingHTTPClient(nil)
	if stream == nil {
		t.Fatal("BuildStreamingHTTPClient(nil) returned nil")
	}
	if stream.Timeout != 0 {
		t.Errorf("Timeout: got %v, want 0", stream.Timeout)
	}
}

// TestBuildStreamingHTTPClient_LongStreamSurvives verifies that the streaming
// client can read a response body that takes longer than the base client's
// Timeout — proving Timeout=0 actually lifts the whole-request deadline.
func TestBuildStreamingHTTPClient_LongStreamSurvives(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 4; i++ {
			fmt.Fprintf(w, "data: chunk-%d\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(400 * time.Millisecond)
		}
	}))
	defer srv.Close()

	base := &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			ResponseHeaderTimeout: 5 * time.Second,
		},
		Timeout: 500 * time.Millisecond, // would abort the stream without the fix
	}
	stream := BuildStreamingHTTPClient(base)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := stream.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	got := 0
	for scanner.Scan() {
		if line := scanner.Text(); len(line) >= 5 && line[:5] == "data:" {
			got++
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}
	if got != 4 {
		t.Errorf("chunks received: got %d, want 4 (stream was likely killed by Timeout)", got)
	}
}
