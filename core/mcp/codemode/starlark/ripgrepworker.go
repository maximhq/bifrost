//go:build !tinygo && !wasm

package starlark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/canonical/starlark/starlark"
)

const ripgrepWorkerArg = "--bifrost-ripgrep-files-v1"

// Slots are held until Wait completes, including after cancellation. A filesystem
// stuck in an uninterruptible kernel call cannot accumulate unlimited children,
// reapers, pipes or buffers. Once all four slots are occupied, searches fail closed.
var ripgrepFilesystemSlots = make(chan struct{}, 4)

type ripgrepSnapshotFile struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}

type ripgrepSnapshotRequest struct {
	Root     string `json:"root"`
	Path     string `json:"path"`
	MaxBytes int    `json:"max_bytes"`
}

type ripgrepSnapshotResponse struct {
	Files []ripgrepSnapshotFile `json:"files"`
	Error string                `json:"error,omitempty"`
}

// init dispatches only the private re-exec mode, before the embedding program's
// main starts. The helper has no listener, runs no MCP tools, and never launches rg.
func init() {
	if len(os.Args) != 2 || os.Args[1] != ripgrepWorkerArg {
		return
	}
	var request ripgrepSnapshotRequest
	response := ripgrepSnapshotResponse{}
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 16<<10)).Decode(&request); err != nil {
		response.Error = "invalid filesystem helper request"
	} else {
		files, err := collectRipgrepSnapshot(request)
		response.Files = files
		if err != nil {
			response.Error = err.Error()
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

// loadRipgrepSnapshot performs no access to the configured filesystem in the
// gateway. Wire buffers, decoded contents and directory metadata share the script's
// memory budget; the child enforces scan bounds before retaining file contents.
func loadRipgrepSnapshot(ctx context.Context, thread *starlark.Thread, root, name string) ([]ripgrepSnapshotFile, error) {
	if err := thread.AddAllocs(starlark.SafeInt(ripgrepMaxEntries * 8192)); err != nil {
		return nil, err
	}
	used, valid := thread.Allocs()
	remaining := int64(sandboxLimits(thread).MaxMemoryBytes) - used
	if !valid || remaining < 1<<20 {
		return nil, fmt.Errorf("ripgrep memory budget exhausted")
	}
	wireLimit := int(min(16<<20, remaining/4))
	scanLimit := min(ripgrepMaxScanBytes, (wireLimit-(64<<10))*3/4)
	request, err := json.Marshal(ripgrepSnapshotRequest{Root: root, Path: name, MaxBytes: scanLimit})
	if err != nil {
		return nil, err
	}
	if len(request) > 16<<10 {
		return nil, fmt.Errorf("ripgrep filesystem request too large")
	}
	if err := thread.CheckAllocs(starlark.SafeInt(4 * wireLimit)); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(executable, ripgrepWorkerArg)
	cmd.Stdin = bytes.NewReader(request)
	data, err := runRipgrepFilesystemWorker(ctx, cmd, wireLimit)
	if err != nil {
		return nil, err
	}
	if err := thread.AddAllocs(starlark.SafeInt(4 * len(data))); err != nil {
		return nil, err
	}
	var response ripgrepSnapshotResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("invalid filesystem helper response: %w", err)
	}
	if response.Error != "" {
		return nil, fmt.Errorf("ripgrep filesystem: %s", response.Error)
	}
	return response.Files, nil
}

// runRipgrepFilesystemWorker bounds both process concurrency and the caller's wait.
// Kill is best effort: unlike Cmd.Run, cancellation never waits for a blocked child
// to leave the kernel. A bounded background reaper owns it until Wait completes.
func runRipgrepFilesystemWorker(ctx context.Context, cmd *exec.Cmd, wireLimit int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case ripgrepFilesystemSlots <- struct{}{}:
	default:
		return nil, fmt.Errorf("ripgrep filesystem workers busy")
	}
	stdout := &ripgrepBuffer{limit: wireLimit}
	stderr := &ripgrepBuffer{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 100 * time.Millisecond
	if err := cmd.Start(); err != nil {
		<-ripgrepFilesystemSlots
		return nil, err
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		<-ripgrepFilesystemSlots
		done <- err
	}()
	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill() // The reaper retains the slot even if termination is deferred by the kernel.
		return nil, ctx.Err()
	case err := <-done:
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if stdout.exceeded || stderr.exceeded {
			return nil, fmt.Errorf("ripgrep filesystem output byte limit exceeded")
		}
		if err != nil {
			return nil, fmt.Errorf("ripgrep filesystem helper failed: %w: %.2048s", err, stderr.buffer.String())
		}
		return stdout.buffer.Bytes(), nil
	}
}

// collectRipgrepSnapshot is called only in the helper. Every access to the
// configured mount, including OpenRoot, Lstat, ReadDir, Stat and ReadFull, happens
// here so cancellation of any one operation cannot block the gateway interpreter.
func collectRipgrepSnapshot(request ripgrepSnapshotRequest) ([]ripgrepSnapshotFile, error) {
	if !filepath.IsAbs(request.Root) || !fs.ValidPath(request.Path) || strings.Contains(request.Path, "\\") || request.MaxBytes < 0 || request.MaxBytes > ripgrepMaxScanBytes {
		return nil, fmt.Errorf("invalid filesystem helper bounds")
	}
	root, err := os.OpenRoot(request.Root)
	if err != nil {
		return nil, fmt.Errorf("cannot open configured root: %w", err)
	}
	defer root.Close()
	names, err := ripgrepFiles(context.Background(), root, request.Path)
	if err != nil {
		return nil, err
	}
	files := []ripgrepSnapshotFile{}
	total := 0
	for _, name := range names {
		f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			f.Close()
			return nil, fmt.Errorf("ripgrep requires regular files")
		}
		if info.Size() > ripgrepMaxFileBytes || info.Size() > int64(request.MaxBytes-total) {
			f.Close()
			return nil, fmt.Errorf("ripgrep scan byte limit exceeded")
		}
		content := make([]byte, int(info.Size())+1)
		n, readErr := io.ReadFull(f, content)
		f.Close()
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return nil, readErr
		}
		if int64(n) > info.Size() {
			return nil, fmt.Errorf("ripgrep file changed while reading")
		}
		content = content[:n]
		total += n
		if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
			continue
		}
		files = append(files, ripgrepSnapshotFile{Path: name, Content: content})
	}
	return files, nil
}

// ripgrepFiles bounds directory enumeration itself, not just returned matches.
// Symlinks and special files are excluded. OpenRoot also confines later opens
// if the tree changes between enumeration and reading.
func ripgrepFiles(ctx context.Context, root *os.Root, name string) ([]string, error) {
	files := []string{}
	pending := []string{name}
	entries := 0
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		entries++
		if entries > ripgrepMaxEntries {
			return nil, fmt.Errorf("ripgrep directory entry limit exceeded")
		}
		info, err := root.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			files = append(files, current)
			if len(files) > ripgrepMaxFiles {
				return nil, fmt.Errorf("ripgrep file limit exceeded")
			}
			continue
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("ripgrep path must be a regular file or directory")
		}
		dir, err := root.OpenFile(current, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, err
		}
		for {
			if err := ctx.Err(); err != nil {
				dir.Close()
				return nil, err
			}
			batch, readErr := dir.ReadDir(32)
			for _, entry := range batch {
				entries++
				if entries > ripgrepMaxEntries {
					dir.Close()
					return nil, fmt.Errorf("ripgrep directory entry limit exceeded")
				}
				if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
					continue
				}
				pending = append(pending, path.Join(current, entry.Name()))
			}
			if readErr != nil {
				dir.Close()
				if readErr != io.EOF {
					return nil, readErr
				}
				break
			}
		}
	}
	sort.Strings(files)
	return files, nil
}
