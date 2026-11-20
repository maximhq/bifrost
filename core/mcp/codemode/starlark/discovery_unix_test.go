//go:build unix && !tinygo && !wasm

package starlark

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A token_file that names a named pipe is refused without opening it, so a
// pipe with no writer cannot stall the ranked listing.
func TestReadDiscoveryTokenRejectsNamedPipe(t *testing.T) {
	pipe := filepath.Join(t.TempDir(), "token")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readDiscoveryToken(pipe)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a named pipe to be rejected")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readDiscoveryToken blocked on a named pipe")
	}

	// A regular file is still read on every call, so a rotated token is used.
	file := filepath.Join(t.TempDir(), "token")
	for _, want := range []string{"first", "second"} {
		if err := os.WriteFile(file, []byte(want+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := readDiscoveryToken(file); err != nil || got != want {
			t.Fatalf("expected token %q, got %q (%v)", want, got, err)
		}
	}
}
