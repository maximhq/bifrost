package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// Harness defines a coding assistant CLI that Bifrost can launch and manage.
type Harness struct {
	ID               string
	Label            string
	Binary           string
	InstallPkg       string
	VersionArgs      []string
	BasePath         string
	BaseURLEnv       string
	APIKeyEnv        string
	ModelEnv         string
	SupportsMCP      bool
	SupportsWorktree bool
	RunArgsForMod    func(model string) []string
	WorktreeArgs     func(name string) []string
	// PreLaunch is called before launching the harness binary. It can write
	// config files and return extra environment variables to inject. The
	// returned cleanup function is deferred after the process exits.
	PreLaunch func(baseURL, apiKey, model string) (extraEnv []string, cleanup func(), err error)
	// WriteNativeConfig persists the bifrost connection settings into the
	// harness CLI's own config file so the same configuration is available
	// when users launch the CLI directly outside bifrost.
	WriteNativeConfig func(baseURL, apiKey, model string) error
	// NativeConfigPath is the human-readable path to the file that
	// WriteNativeConfig modifies (e.g. "~/.claude/settings.json").
	// Used to inform the user in the confirmation prompt.
	NativeConfigPath string
}

var all = map[string]Harness{
	"claude": {
		ID:               "claude",
		Label:            "Claude Code",
		Binary:           "claude",
		InstallPkg:       "@anthropic-ai/claude-code",
		VersionArgs:      []string{"--version"},
		BasePath:         "/anthropic",
		BaseURLEnv:       "ANTHROPIC_BASE_URL",
		APIKeyEnv:        "ANTHROPIC_API_KEY",
		SupportsMCP:      true,
		SupportsWorktree: true,
		RunArgsForMod: func(model string) []string {
			if strings.TrimSpace(model) == "" {
				return nil
			}
			return []string{"--model", model}
		},
		WorktreeArgs: func(name string) []string {
			name = strings.TrimSpace(name)
			if name == "" {
				return []string{"--worktree"}
			}
			return []string{"--worktree", name}
		},
		PreLaunch:         claudePreLaunch,
		WriteNativeConfig: claudeWriteNativeConfig,
		NativeConfigPath:  "~/.claude/settings.json",
	},
	"codex": {
		ID:         "codex",
		Label:      "Codex CLI",
		Binary:     "codex",
		InstallPkg: "@openai/codex",
		VersionArgs: []string{
			"--version",
		},
		BasePath:   "/openai",
		BaseURLEnv: "OPENAI_BASE_URL",
		APIKeyEnv:  "OPENAI_API_KEY",
		ModelEnv:   "OPENAI_MODEL",
		RunArgsForMod: func(model string) []string {
			if strings.TrimSpace(model) == "" {
				return nil
			}
			return []string{"--model", model}
		},
	},
	"gemini": {
		ID:         "gemini",
		Label:      "Gemini CLI",
		Binary:     "gemini",
		InstallPkg: "@google/gemini-cli",
		VersionArgs: []string{
			"--version",
		},
		BasePath:   "/genai",
		BaseURLEnv: "GOOGLE_GEMINI_BASE_URL",
		APIKeyEnv:  "GEMINI_API_KEY",
		ModelEnv:   "GEMINI_MODEL",
		RunArgsForMod: func(model string) []string {
			if strings.TrimSpace(model) == "" {
				return nil
			}
			return []string{"--model", model}
		},
	},
	"opencode": {
		ID:         "opencode",
		Label:      "Opencode",
		Binary:     "opencode",
		InstallPkg: "opencode-ai",
		VersionArgs: []string{
			"--version",
		},
		BasePath:   "/openai",
		BaseURLEnv: "OPENAI_BASE_URL",
		APIKeyEnv:  "OPENAI_API_KEY",
		RunArgsForMod: func(model string) []string {
			if strings.TrimSpace(model) == "" {
				return nil
			}
			return []string{"--model", opencodeModelRef(model)}
		},
		PreLaunch: opencodePreLaunch,
	},
}

// Get returns the harness with the given ID and whether it exists.
func Get(id string) (Harness, bool) {
	h, ok := all[id]
	return h, ok
}

// IDs returns the sorted list of all registered harness IDs.
func IDs() []string {
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Labels returns display labels for all harnesses in the format "Label (id)".
func Labels() []string {
	ids := IDs()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, fmt.Sprintf("%s (%s)", all[id].Label, id))
	}
	return out
}

// ParseChoice extracts the harness ID from a label string like "Label (id)".
func ParseChoice(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.LastIndex(raw, "("); i >= 0 && strings.HasSuffix(raw, ")") {
		return strings.TrimSuffix(raw[i+1:], ")")
	}
	return raw
}

// DetectVersion runs the harness binary with its version flag and returns the version string.
func DetectVersion(h Harness) string {
	if _, err := exec.LookPath(h.Binary); err != nil {
		return "not-installed"
	}

	args := h.VersionArgs
	if len(args) == 0 {
		args = []string{"--version"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()

	out, err := exec.CommandContext(ctx, h.Binary, args...).CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "timeout"
		}
		return "unknown"
	}

	s := strings.TrimSpace(string(out))
	if s == "" {
		return "unknown"
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// opencodePreLaunch writes temporary OpenCode config files when Bifrost needs
// to override runtime model/provider settings and/or supply an adaptive TUI
// theme. The returned cleanup removes any generated temp files after exit.
func opencodePreLaunch(baseURL, apiKey, model string) ([]string, func(), error) {
	var env []string
	var cleanupFns []func()

	tuiEnv, tuiCleanup, err := opencodeTUIPreLaunch()
	if err != nil {
		return nil, nil, err
	}
	env = append(env, tuiEnv...)
	if tuiCleanup != nil {
		cleanupFns = append(cleanupFns, tuiCleanup)
	}

	model = strings.TrimSpace(model)
	if model == "" {
		return env, combineCleanup(cleanupFns), nil
	}

	modelRef := opencodeModelRef(model)
	runtimeCfg := fmt.Sprintf(`{
  "$schema": "https://opencode.ai/config.json",
  "model": %q,
  "provider": {
    "bifrost": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Bifrost",
      "options": {
        "baseURL": %q,
        "apiKey": %q
      },
      "models": {
        %q: {
          "name": %q
        }
      }
    }
  }
}`, modelRef, strings.TrimSpace(baseURL), strings.TrimSpace(apiKey), model, model)

	f, err := os.CreateTemp("", "bifrost-opencode-*.json")
	if err != nil {
		return nil, nil, fmt.Errorf("create opencode config: %w", err)
	}
	if _, err := f.WriteString(runtimeCfg); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, nil, fmt.Errorf("write opencode config: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return nil, nil, fmt.Errorf("close opencode config: %w", err)
	}

	env = append(env, "OPENCODE_CONFIG="+f.Name())
	cleanupFns = append(cleanupFns, func() { os.Remove(f.Name()) })
	return env, combineCleanup(cleanupFns), nil
}

// opencodeModelRef returns the Opencode model reference.
func opencodeModelRef(model string) string {
	return "bifrost/" + strings.TrimSpace(model)
}

// opencodeTUIPreLaunch loads the Opencode TUI config from the user's home directory.
func opencodeTUIPreLaunch() ([]string, func(), error) {
	path, err := opencodeTUIConfigPath()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve opencode tui config: %w", err)
	}

	cfg, hasTheme, err := loadOpencodeTUIConfig(path)
	if err != nil {
		return nil, nil, err
	}
	if hasTheme {
		return nil, nil, nil
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["theme"] = "system"

	b, err := sonic.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal opencode tui config: %w", err)
	}

	f, err := os.CreateTemp("", "bifrost-opencode-tui-*.json")
	if err != nil {
		return nil, nil, fmt.Errorf("create opencode tui config: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, nil, fmt.Errorf("write opencode tui config: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return nil, nil, fmt.Errorf("close opencode tui config: %w", err)
	}

	return []string{"OPENCODE_TUI_CONFIG=" + f.Name()}, func() { os.Remove(f.Name()) }, nil
}

// opencodeTUIConfigPath returns the path to the Opencode TUI config.
func opencodeTUIConfigPath() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "opencode", "tui.json"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode", "tui.json"), nil
}

// loadOpencodeTUIConfig loads the Opencode TUI config from the given path.
func loadOpencodeTUIConfig(path string) (map[string]any, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read opencode tui config: %w", err)
	}

	normalized := normalizeJSONC(b)
	if len(bytes.TrimSpace(normalized)) == 0 {
		return map[string]any{}, false, nil
	}

	var cfg map[string]any
	if err := sonic.Unmarshal(normalized, &cfg); err != nil {
		return nil, false, fmt.Errorf("parse opencode tui config: %w", err)
	}
	theme, ok := cfg["theme"]
	return cfg, ok && strings.TrimSpace(fmt.Sprint(theme)) != "", nil
}

// combineCleanup combines multiple cleanup functions into a single function.
func combineCleanup(cleanups []func()) func() {
	return func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			if cleanups[i] != nil {
				cleanups[i]()
			}
		}
	}
}

// normalizeJSONC removes trailing commas and comments from JSONC data.
func normalizeJSONC(data []byte) []byte {
	return stripTrailingCommas(stripJSONComments(data))
}

// stripJSONComments removes comments from JSONC data.
func stripJSONComments(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escape := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(data); i++ {
		ch := data[i]

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
				out = append(out, ch)
			}
			continue
		}
		if inBlockComment {
			if ch == '*' && i+1 < len(data) && data[i+1] == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if inString {
			out = append(out, ch)
			if escape {
				escape = false
				continue
			}
			if ch == '\\' {
				escape = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}
		if ch == '/' && i+1 < len(data) {
			switch data[i+1] {
			case '/':
				inLineComment = true
				i++
				continue
			case '*':
				inBlockComment = true
				i++
				continue
			}
		}

		out = append(out, ch)
	}

	return out
}

func stripTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escape := false

	for i := 0; i < len(data); i++ {
		ch := data[i]

		if inString {
			out = append(out, ch)
			if escape {
				escape = false
				continue
			}
			if ch == '\\' {
				escape = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}
		if ch == ',' {
			j := i + 1
			for j < len(data) {
				switch data[j] {
				case ' ', '\t', '\r', '\n':
					j++
					continue
				case '}', ']':
					ch = 0
				}
				break
			}
			if ch == 0 {
				continue
			}
		}

		out = append(out, ch)
	}

	return out
}
