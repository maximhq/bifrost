package postgresconn

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestParseConnMaxIdleTime(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "unset uses default", raw: "", want: defaultConnMaxIdleTime},
		{name: "explicit value", raw: "90s", want: 90 * time.Second},
		{name: "minutes", raw: "10m", want: 10 * time.Minute},
		{name: "unparseable rejected", raw: "not-a-duration", wantErr: true},
		{name: "zero rejected", raw: "0s", wantErr: true},
		{name: "negative rejected", raw: "-5m", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseConnMaxIdleTime(&Config{ConnMaxIdleTime: tc.raw})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("parseConnMaxIdleTime(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestParsePasswordCommandCacheTTL(t *testing.T) {
	got, err := parsePasswordCommandCacheTTL(&PasswordCommandConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != defaultPasswordCommandCacheTTL {
		t.Fatalf("default cache TTL = %v, want %v", got, defaultPasswordCommandCacheTTL)
	}

	got, err = parsePasswordCommandCacheTTL(&PasswordCommandConfig{CacheTTL: "5m"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 5*time.Minute {
		t.Fatalf("cache TTL = %v, want 5m", got)
	}

	if _, err := parsePasswordCommandCacheTTL(&PasswordCommandConfig{CacheTTL: "0s"}); err == nil {
		t.Fatal("expected a non-positive cache_ttl to be rejected")
	}
	if _, err := parsePasswordCommandCacheTTL(&PasswordCommandConfig{CacheTTL: "nope"}); err == nil {
		t.Fatal("expected an unparseable cache_ttl to be rejected")
	}
}

// countingPasswordCommand writes a fixed password to stdout and appends a byte to
// a marker file on each run, so the test can count executions.
func countingPasswordCommand(t *testing.T) (*PasswordCommandConfig, func() int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("password command test uses a POSIX shell script")
	}

	dir := t.TempDir()
	marker := filepath.Join(dir, "runs")
	script := filepath.Join(dir, "pw.sh")
	body := "#!/bin/sh\nprintf x >> " + marker + "\nprintf 'secret'\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}

	return &PasswordCommandConfig{Command: script}, func() int {
		data, err := os.ReadFile(marker)
		if err != nil {
			return 0
		}
		return len(data)
	}
}

// TestPasswordCacheCollapsesConcurrentResolutions is the regression for the
// per-connection subprocess fork: BeforeConnect runs for every new physical
// connection, so a pool burst previously ran the command once per connection.
func TestPasswordCacheCollapsesConcurrentResolutions(t *testing.T) {
	config, runs := countingPasswordCommand(t)
	cache := &passwordCache{}

	const callers = 32
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			password, err := cache.get(context.Background(), config)
			if err != nil {
				errs <- err
				return
			}
			if password != "secret" {
				t.Errorf("password = %q, want %q", password, "secret")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("cache.get: %v", err)
	}

	if got := runs(); got != 1 {
		t.Fatalf("password command ran %d times for %d concurrent callers, want 1", got, callers)
	}
}

func TestPasswordCacheReusesWithinTTLAndRefreshesAfter(t *testing.T) {
	config, runs := countingPasswordCommand(t)
	config.CacheTTL = "1h"
	cache := &passwordCache{}

	for range 5 {
		if _, err := cache.get(context.Background(), config); err != nil {
			t.Fatalf("cache.get: %v", err)
		}
	}
	if got := runs(); got != 1 {
		t.Fatalf("password command ran %d times within the TTL, want 1", got)
	}

	// Expire the entry; the next call must resolve again.
	cache.mu.Lock()
	cache.expiresAt = time.Now().Add(-time.Second)
	cache.mu.Unlock()

	if _, err := cache.get(context.Background(), config); err != nil {
		t.Fatalf("cache.get after expiry: %v", err)
	}
	if got := runs(); got != 2 {
		t.Fatalf("password command ran %d times after expiry, want 2", got)
	}
}

// TestPasswordCacheDoesNotCacheFailures ensures a transient failure does not
// persist for the whole TTL.
func TestPasswordCacheDoesNotCacheFailures(t *testing.T) {
	cache := &passwordCache{}
	failing := &PasswordCommandConfig{Command: filepath.Join(t.TempDir(), "does-not-exist")}

	for range 3 {
		if _, err := cache.get(context.Background(), failing); err == nil {
			t.Fatal("expected an error from a missing password command")
		}
	}

	cache.mu.RLock()
	cached := cache.value
	cache.mu.RUnlock()
	if cached != "" {
		t.Fatalf("a failed resolution was cached as %q", cached)
	}
}
