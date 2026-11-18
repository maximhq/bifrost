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

// TestReadCredentialFileSymlinkedAllowedDir pins that when only a symlink to a directory is
// allowed, a file under it reads by both the configured and the resolved spelling, and an
// unlisted sibling is still refused.
func TestReadCredentialFileSymlinkedAllowedDir(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "workload")
	require.NoError(t, os.Mkdir(real, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(real, "token"), []byte("tok\n"), 0o600))
	link := filepath.Join(base, "token-mount")
	require.NoError(t, os.Symlink(real, link))
	t.Setenv(CredentialDirsEnv, link)

	got, err := ReadCredentialFile(filepath.Join(link, "token"), maxSubjectTokenBytes)
	require.NoError(t, err)
	require.Equal(t, "tok", got)

	resolved, err := filepath.EvalSymlinks(real)
	require.NoError(t, err)
	got, err = ReadCredentialFile(filepath.Join(resolved, "token"), maxSubjectTokenBytes)
	require.NoError(t, err, "the resolved spelling of an allowed directory is allowed")
	require.Equal(t, "tok", got)

	other := filepath.Join(base, "other")
	require.NoError(t, os.Mkdir(other, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(other, "token"), []byte("x"), 0o600))
	_, err = ReadCredentialFile(filepath.Join(other, "token"), maxSubjectTokenBytes)
	require.ErrorIs(t, err, ErrCredentialPathNotAllowed)
}

// TestReadCredentialFileRefusesDotDot pins that a path with a .. component is refused rather
// than cleaned: current/../token names version/token when current is a symlink to
// version/subdir, so cleaning it first would open a different file.
func TestReadCredentialFileRefusesDotDot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(CredentialDirsEnv, dir)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "version", "subdir"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "version", "token"), []byte("version"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "token"), []byte("root"), 0o600))
	require.NoError(t, os.Symlink(filepath.Join("version", "subdir"), filepath.Join(dir, "current")))

	_, err := ReadCredentialFile(dir+"/current/../token", maxSubjectTokenBytes)
	require.ErrorIs(t, err, ErrCredentialPathNotAllowed)
	require.Contains(t, err.Error(), "..")
}

// TestReadCredentialFileAbsoluteSymlink pins that an absolute symlink is followed when its
// target is inside an allowed directory and refused when it is not.
func TestReadCredentialFileAbsoluteSymlink(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(CredentialDirsEnv, dir)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "v1"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "v1", "token"), []byte("inside\n"), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(dir, "v1", "token"), filepath.Join(dir, "current")))
	got, err := ReadCredentialFile(filepath.Join(dir, "current"), maxSubjectTokenBytes)
	require.NoError(t, err)
	require.Equal(t, "inside", got)

	outside := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "escape")))
	_, err = ReadCredentialFile(filepath.Join(dir, "escape"), maxSubjectTokenBytes)
	require.ErrorIs(t, err, ErrCredentialPathNotAllowed)

	// An absolute link to an in-root directory followed by a relative link out is refused too.
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "v1", "out")))
	require.NoError(t, os.Symlink(filepath.Join(dir, "v1"), filepath.Join(dir, "cur")))
	_, err = ReadCredentialFile(filepath.Join(dir, "cur", "out"), maxSubjectTokenBytes)
	require.ErrorIs(t, err, ErrCredentialPathNotAllowed)
}
