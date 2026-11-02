//go:build !tinygo && !wasm

package starlark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/canonical/starlark/starlark"
)

const (
	ripgrepMaxFileBytes   = 1 << 20
	ripgrepMaxScanBytes   = 8 << 20
	ripgrepMaxOutputBytes = 256 << 10
	ripgrepMaxEntries     = 256
	ripgrepMaxFiles       = 32
	ripgrepMaxCalls       = 4
)

// storeRipgrepRoot publishes a copy of an absolute root; nil preserves the current configuration.
func (s *StarlarkCodeMode) storeRipgrepRoot(root *string) {
	if root == nil {
		return
	}
	value := *root
	// Never grant access relative to the gateway's working directory.
	if value != "" && !filepath.IsAbs(value) {
		s.logger.Warn("ignoring relative code mode ripgrep root")
		return
	}
	s.ripgrepRoot.Store(&value)
}

// ripgrepBuiltins captures one configuration snapshot for the whole execution.
// A disabled feature adds no builtin and performs no filesystem/process work.
func (s *StarlarkCodeMode) ripgrepBuiltins() starlark.StringDict {
	root := s.ripgrepRoot.Load()
	if root == nil || *root == "" {
		return nil
	}
	calls := 0
	return starlark.StringDict{"ripgrep": starlark.NewBuiltinWithSafety("ripgrep",
		starlark.CPUSafe|starlark.MemSafe|starlark.TimeSafe,
		func(thread *starlark.Thread, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			pattern, name, maxResults, ignoreCase := "", ".", 100, false
			if err := starlark.UnpackArgs(fn.Name(), args, kwargs, "pattern", &pattern, "path?", &name, "max_results?", &maxResults, "ignore_case?", &ignoreCase); err != nil {
				return nil, err
			}
			if len(pattern) == 0 || len(pattern) > 4096 {
				return nil, fmt.Errorf("ripgrep pattern must contain 1 to 4096 bytes")
			}
			if !fs.ValidPath(name) || strings.Contains(name, "\\") || len(name) > 4096 {
				return nil, fmt.Errorf("ripgrep path must be relative to the configured root without traversal")
			}
			if maxResults < 1 || maxResults > 100 {
				return nil, fmt.Errorf("ripgrep max_results must be between 1 and 100")
			}
			calls++
			if calls > ripgrepMaxCalls {
				return nil, fmt.Errorf("ripgrep call limit exceeded")
			}
			if err := thread.AddSteps(starlark.SafeInt(1)); err != nil {
				return nil, err
			}
			ctx, cancel := context.WithTimeout(thread.Context(), 5*time.Second)
			defer cancel()
			result, err := searchRipgrep(ctx, thread, *root, pattern, name, maxResults, ignoreCase)
			if err != nil {
				return nil, err
			}
			return newValueConversion(thread).fromGo(result, 0)
		})}
}

// searchRipgrep opens files through os.Root, then feeds bounded contents to rg's
// stdin. rg never receives a host path, so it cannot follow a symlink outside the
// root or interpret an ignore/config file as instructions. No shell is involved.
func searchRipgrep(ctx context.Context, thread *starlark.Thread, rootName, pattern, name string, maxResults int, ignoreCase bool) (map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	executable, err := exec.LookPath("rg")
	if err != nil {
		return nil, fmt.Errorf("ripgrep requires rg on the server PATH: %w", err)
	}
	root, err := os.OpenRoot(rootName)
	if err != nil {
		return nil, fmt.Errorf("ripgrep cannot open configured root: %w", err)
	}
	defer root.Close()
	if err := thread.AddAllocs(starlark.SafeInt(ripgrepMaxEntries * 8192)); err != nil {
		return nil, err
	}
	files, err := ripgrepFiles(ctx, root, name)
	if err != nil {
		return nil, err
	}
	matches := []interface{}{}
	scanBytes, resultBytes := 0, 0
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Nonblocking open prevents a concurrently replaced file becoming a blocking FIFO.
		f, err := root.OpenFile(file, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, fmt.Errorf("ripgrep cannot open %q: %w", file, err)
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			f.Close()
			return nil, fmt.Errorf("ripgrep requires regular files")
		}
		if info.Size() > ripgrepMaxFileBytes || info.Size() > int64(ripgrepMaxScanBytes-scanBytes) {
			f.Close()
			return nil, fmt.Errorf("ripgrep scan byte limit exceeded")
		}
		// Charge maximum read growth and process/JSON buffers before allocation.
		if err := thread.AddAllocs(starlark.SafeInt(int(info.Size()+1) + 4*ripgrepMaxOutputBytes)); err != nil {
			f.Close()
			return nil, err
		}
		content := make([]byte, int(info.Size())+1)
		n, readErr := io.ReadFull(f, content)
		content = content[:n]
		f.Close()
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return nil, readErr
		}
		if int64(len(content)) > info.Size() {
			return nil, fmt.Errorf("ripgrep file changed while reading")
		}
		scanBytes += len(content)
		if len(content) > ripgrepMaxFileBytes || scanBytes > ripgrepMaxScanBytes {
			return nil, fmt.Errorf("ripgrep scan byte limit exceeded")
		}
		// Binary data is outside this text-search API.
		if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
			continue
		}
		argv := []string{"--no-config", "--json", "--color=never", "--threads=1", "--regex-size-limit=1M", "--dfa-size-limit=1M", "--max-count", strconv.Itoa(maxResults + 1), "--regexp", pattern}
		if ignoreCase {
			argv = append(argv, "--ignore-case")
		}
		argv = append(argv, "--", "-")
		cmd := exec.CommandContext(ctx, executable, argv...)
		cmd.Stdin = bytes.NewReader(content)
		stdout, stderr := &ripgrepBuffer{}, &ripgrepBuffer{}
		cmd.Stdout, cmd.Stderr = stdout, stderr
		cmd.WaitDelay = 100 * time.Millisecond
		err = cmd.Run()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if stdout.exceeded || stderr.exceeded {
			return nil, fmt.Errorf("ripgrep output byte limit exceeded")
		}
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				return nil, fmt.Errorf("ripgrep failed: %v: %.2048s", err, stderr.buffer.String())
			}
		}
		if err := thread.AddAllocs(starlark.SafeInt(8 * stdout.buffer.Len())); err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(stdout.buffer.Bytes()))
		for decoder.More() {
			var event struct {
				Type string `json:"type"`
				Data struct {
					LineNumber int `json:"line_number"`
					Lines      struct {
						Text string `json:"text"`
					} `json:"lines"`
				} `json:"data"`
			}
			if err := decoder.Decode(&event); err != nil {
				return nil, fmt.Errorf("invalid ripgrep output: %w", err)
			}
			if event.Type != "match" {
				continue
			}
			if len(matches) == maxResults {
				return map[string]interface{}{"matches": matches, "truncated": true}, nil
			}
			text := strings.TrimSuffix(strings.TrimSuffix(event.Data.Lines.Text, "\n"), "\r")
			resultBytes += len(file) + len(text) + 64
			if resultBytes > 64<<10 {
				return nil, fmt.Errorf("ripgrep result byte limit exceeded")
			}
			matches = append(matches, map[string]interface{}{"path": file, "line": event.Data.LineNumber, "text": text})
		}
	}
	return map[string]interface{}{"matches": matches, "truncated": false}, nil
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

type ripgrepBuffer struct {
	buffer   bytes.Buffer
	exceeded bool
}

// Write bounds process output without exposing bytes.Buffer methods that bypass the limit.
func (b *ripgrepBuffer) Write(p []byte) (int, error) {
	if len(p) > ripgrepMaxOutputBytes-b.buffer.Len() {
		b.exceeded = true
		return 0, fmt.Errorf("ripgrep output byte limit exceeded")
	}
	return b.buffer.Write(p)
}
