package utils

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDownloadURLToBase64(t *testing.T) {
	restore := AllowPrivateAudioURLsForTest()
	defer restore()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("audio-bytes"))
	}))
	defer server.Close()

	got, err := DownloadURLToBase64(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("DownloadURLToBase64() error = %v", err)
	}

	want := base64.StdEncoding.EncodeToString([]byte("audio-bytes"))
	if got != want {
		t.Fatalf("DownloadURLToBase64() = %q, want %q", got, want)
	}
}

func TestDownloadURLToBase64HonorsContextCancellation(t *testing.T) {
	restore := AllowPrivateAudioURLsForTest()
	defer restore()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := DownloadURLToBase64(ctx, server.URL)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DownloadURLToBase64() error = %v, want context.Canceled", err)
	}
}

func TestDownloadURLToBase64RejectsHTTP(t *testing.T) {
	// Guard active: httptest server is loopback http, so the validator
	// should reject the URL before any network call.
	_, err := DownloadURLToBase64(context.Background(), "http://example.com/audio.mp3")
	if err == nil {
		t.Fatal("expected error for http URL, got nil")
	}
	if !strings.Contains(err.Error(), "plaintext http") {
		t.Fatalf("expected plaintext-http rejection, got %v", err)
	}
}

func TestDownloadURLToBase64RejectsUnsupportedScheme(t *testing.T) {
	_, err := DownloadURLToBase64(context.Background(), "ftp://example.com/audio.mp3")
	if err == nil {
		t.Fatal("expected error for ftp URL, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported URL scheme") {
		t.Fatalf("expected scheme rejection, got %v", err)
	}
}

func TestDownloadURLToBase64RejectsLoopbackInProduction(t *testing.T) {
	// No AllowPrivateAudioURLsForTest call: guard is active.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("should-never-reach"))
	}))
	defer server.Close()

	_, err := DownloadURLToBase64(context.Background(), server.URL)
	if err == nil {
		t.Fatal("expected error for loopback URL in production mode, got nil")
	}
}

func TestDownloadURLToBase64RejectsPrivateIPLiteral(t *testing.T) {
	cases := []string{
		"https://10.0.0.1/audio.mp3",        // RFC 1918
		"https://192.168.1.1/audio.mp3",     // RFC 1918
		"https://172.16.0.1/audio.mp3",      // RFC 1918
		"https://169.254.169.254/meta-data", // AWS IMDS link-local
		"https://[::1]/audio.mp3",           // IPv6 loopback
	}
	for _, rawURL := range cases {
		t.Run(rawURL, func(t *testing.T) {
			_, err := DownloadURLToBase64(context.Background(), rawURL)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", rawURL)
			}
			if !strings.Contains(err.Error(), "private/internal address") {
				t.Fatalf("expected private/internal rejection for %s, got %v", rawURL, err)
			}
		})
	}
}

func TestDownloadURLToBase64RefusesRedirects(t *testing.T) {
	restore := AllowPrivateAudioURLsForTest()
	defer restore()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("target"))
	}))
	defer target.Close()

	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirecting.Close()

	_, err := DownloadURLToBase64(context.Background(), redirecting.URL)
	if err == nil {
		t.Fatal("expected redirect not to be followed, got nil")
	}
	if !strings.Contains(err.Error(), "redirect not followed") {
		t.Fatalf("expected redirect rejection, got %v", err)
	}
}
