//go:build !tinygo && !wasm

package starlark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

// searchRipgrep obtains a confined snapshot from a killable helper, then feeds its
// bounded contents to rg's stdin. rg receives no host path, so it cannot follow
// symlinks outside the root or read ignore/config files. No shell is involved.
func searchRipgrep(ctx context.Context, thread *starlark.Thread, rootName, pattern, name string, maxResults int, ignoreCase bool) (map[string]interface{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	executable, err := exec.LookPath("rg")
	if err != nil {
		return nil, fmt.Errorf("ripgrep requires rg on the server PATH: %w", err)
	}
	// Filesystem syscalls run outside the gateway: a slow mount cannot hold this thread.
	snapshot, err := loadRipgrepSnapshot(ctx, thread, rootName, name)
	if err != nil {
		return nil, err
	}
	matches := []interface{}{}
	resultBytes := 0
	for _, file := range snapshot {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := thread.AddAllocs(starlark.SafeInt(4 * ripgrepMaxOutputBytes)); err != nil {
			return nil, err
		}
		content := file.Content
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
			resultBytes += len(file.Path) + len(text) + 64
			if resultBytes > 64<<10 {
				return nil, fmt.Errorf("ripgrep result byte limit exceeded")
			}
			matches = append(matches, map[string]interface{}{"path": file.Path, "line": event.Data.LineNumber, "text": text})
		}
	}
	return map[string]interface{}{"matches": matches, "truncated": false}, nil
}

type ripgrepBuffer struct {
	buffer   bytes.Buffer
	exceeded bool
	limit    int
}

// Write bounds process output without exposing bytes.Buffer methods that bypass the limit.
func (b *ripgrepBuffer) Write(p []byte) (int, error) {
	limit := b.limit
	if limit == 0 {
		limit = ripgrepMaxOutputBytes
	}
	if len(p) > limit-b.buffer.Len() {
		b.exceeded = true
		return 0, fmt.Errorf("ripgrep output byte limit exceeded")
	}
	return b.buffer.Write(p)
}
