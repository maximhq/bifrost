package utils

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDownloadURLToBase64(t *testing.T) {
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
