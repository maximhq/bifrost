package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/maximhq/bifrost/cli/internal/config"
)

// ValidateProject refuses unsupported integrations rather than launching unscoped inference.
func ValidateProject(agent string, project config.Project, persistent bool) error {
	if err := project.Validate(); err != nil {
		return err
	}
	if project == (config.Project{}) {
		return nil
	}
	switch agent {
	case "pi":
		_, value := project.Header()
		if strings.HasPrefix(value, "!") || strings.Contains(value, "$") {
			return fmt.Errorf("project selectors for Pi must be literal values; use --project-id")
		}
		return nil
	case "opencode":
		_, value := project.Header()
		if strings.Contains(value, "{env:") || strings.Contains(value, "{file:") {
			return fmt.Errorf("project selectors for OpenCode must be literal values; use --project-id")
		}
		return nil
	case "claude", "codex":
		return nil
	case "gemini":
		if !persistent {
			if _, value := project.Header(); containsComma(value) {
				return fmt.Errorf("project selectors for Gemini cannot contain commas; use --project-id")
			}
			return nil
		}
	}
	return fmt.Errorf("project selection is not supported for %s in this mode; use a supported harness instead", agent)
}

// containsComma guards Gemini's comma-delimited header transport.
func containsComma(value string) bool {
	for _, c := range value {
		if c == ',' {
			return true
		}
	}
	return false
}

// ClaudeProjectHeaders preserves configured custom headers while replacing the project selection.
func ClaudeProjectHeaders(project config.Project) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	// Claude uses the repository root for project settings, including when launched below it.
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			cwd = dir
			break
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	configDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if configDir == "" {
		configDir = filepath.Join(home, ".claude")
	}
	paths := []string{filepath.Join(configDir, "settings.json"), filepath.Join(cwd, ".claude", "settings.json"), filepath.Join(cwd, ".claude", "settings.local.json")}
	headers := os.Getenv("ANTHROPIC_CUSTOM_HEADERS")
	for _, path := range paths {
		settings, err := readNativeJSON(path)
		if err != nil {
			return "", err
		}
		if env, ok := settings["env"].(map[string]any); ok {
			if value, ok := env["ANTHROPIC_CUSTOM_HEADERS"].(string); ok {
				headers = value
			}
		}
	}
	return project.MergeHeaders(headers, "\n"), nil
}

// WriteProjectConfig modifies only the files already covered by the native configuration receipt.
func WriteProjectConfig(agent string, paths []string, project config.Project) error {
	if project == (config.Project{}) {
		return nil
	}
	if err := project.Validate(); err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("missing native project configuration path for %s", agent)
	}
	name, value := project.Header()
	if agent == "codex" {
		path := paths[len(paths)-1]
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Bifrost owns this provider; replace both static and environment project selectors.
		body = setProjectTOMLHeaders(body, "model_providers.bifrost.http_headers", name, value)
		body = setProjectTOMLHeaders(body, "model_providers.bifrost.env_http_headers", "", "")
		return config.WriteAtomic(path, body, 0o600)
	}
	settings, err := readNativeJSON(paths[0])
	if err != nil {
		return err
	}
	target := settings
	var keys []string
	switch agent {
	case "claude":
		keys = []string{"env"}
	case "opencode":
		keys = []string{"provider", "bifrost", "options", "headers"}
	case "pi":
		keys = []string{"providers", "bifrost", "headers"}
	default:
		return fmt.Errorf("unsupported project configuration for %s", agent)
	}
	for _, key := range keys {
		target, err = nativeObject(target, key)
		if err != nil {
			return err
		}
	}
	if agent == "claude" {
		existing, _ := target["ANTHROPIC_CUSTOM_HEADERS"].(string)
		target["ANTHROPIC_CUSTOM_HEADERS"] = project.MergeHeaders(existing, "\n")
	} else {
		for key := range target {
			if config.IsProjectHeader(key) {
				delete(target, key)
			}
		}
		target[name] = value
	}
	return writeNativeJSON(paths[0], settings)
}

// setProjectTOMLHeaders preserves unrelated header entries while replacing project selectors.
func setProjectTOMLHeaders(body []byte, table, name, value string) []byte {
	var lines []string
	inside := false
	for _, line := range strings.Split(string(body), "\n") {
		if isTOMLTableHeader(line) {
			inside = tomlTableHeaderMatches(line, table)
			continue
		}
		if !inside {
			continue
		}
		key, _, assignment := strings.Cut(strings.TrimSpace(line), "=")
		if assignment && config.IsProjectHeader(strings.Trim(strings.TrimSpace(key), "\"'")) {
			continue
		}
		lines = append(lines, line)
	}
	if name != "" {
		lines = append(lines, tomlQuote(name)+" = "+tomlQuote(value))
	}
	return replaceTOMLTable(body, table, lines)
}
