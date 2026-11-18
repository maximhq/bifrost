//go:build unix

package tokencache

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestReadCredentialFileRefusesFIFO pins that a FIFO in an allowed directory is refused without
// blocking the exchange on an open that has no writer.
func TestReadCredentialFileRefusesFIFO(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(CredentialDirsEnv, dir)
	fifo := filepath.Join(dir, "token")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))

	done := make(chan error, 1)
	go func() {
		_, err := ReadCredentialFile(fifo, maxSubjectTokenBytes)
		done <- err
	}()
	select {
	case err := <-done:
		require.Error(t, err)
		require.Contains(t, err.Error(), "not a regular file")
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
	_ = os.Remove(fifo)
}
