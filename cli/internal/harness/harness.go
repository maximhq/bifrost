package harness

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
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
}

var all = map[string]Harness{
	"claude": {
		ID:          "claude",
		Label:       "Claude Code",
		Binary:      "claude",
		InstallPkg:  "@anthropic-ai/claude-code",
		VersionArgs: []string{"--version"},
		BasePath:    "/anthropic",
		BaseURLEnv:  "ANTHROPIC_BASE_URL",
		APIKeyEnv:   "ANTHROPIC_API_KEY",
		ModelEnv:    "ANTHROPIC_MODEL",
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
		ModelEnv:   "OPENAI_MODEL",
		RunArgsForMod: func(model string) []string {
			if strings.TrimSpace(model) == "" {
				return nil
			}
			return []string{"--model", model}
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

// opencodePreLaunch writes a temporary opencode.json config file with the
// selected model and provider settings, then returns OPENCODE_CONFIG pointing
// to it. Opencode merges this with any existing user config.
func opencodePreLaunch(baseURL, apiKey, model string) ([]string, func(), error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, func() {}, nil
	}

	cfg := fmt.Sprintf(`{
  "$schema": "https://opencode.ai/config.json",
  "model": %q,
  "provider": {
    "openai": {
      "options": {
        "baseURL": %q,
        "apiKey": %q
      }
    }
  }
}`, model, strings.TrimSpace(baseURL), strings.TrimSpace(apiKey))

	f, err := os.CreateTemp("", "bifrost-opencode-*.json")
	if err != nil {
		return nil, nil, fmt.Errorf("create opencode config: %w", err)
	}
	if _, err := f.WriteString(cfg); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, nil, fmt.Errorf("write opencode config: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return nil, nil, fmt.Errorf("close opencode config: %w", err)
	}

	cleanup := func() { os.Remove(f.Name()) }
	return []string{"OPENCODE_CONFIG=" + f.Name()}, cleanup, nil
}
