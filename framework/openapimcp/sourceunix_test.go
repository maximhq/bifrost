//go:build unix

package openapimcp

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLoadFile_RequiresRegularFile: special files pass a size check of 0 and
// then block or stream forever; only regular files are read.
func TestLoadFile_RequiresRegularFile(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "spec.fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o644))
	done := make(chan error, 1)
	go func() {
		_, err := LoadFile("spec.fifo", dir)
		done <- err
	}()
	select {
	case err := <-done:
		require.ErrorContains(t, err, "not a regular file")
	case <-time.After(2 * time.Second):
		// Unblock the reader so the goroutine can exit, then fail.
		if f, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			_ = f.Close()
		}
		t.Fatal("LoadFile blocked on a FIFO instead of refusing it")
	}
}
