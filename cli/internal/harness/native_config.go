package harness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/cli/internal/config"
)

// claudePreLaunch pins the selected model across Claude Code's model tiers.
func claudePreLaunch(baseURL, apiKey, model string) ([]string, func(), error) {
	var env []string
	if model = strings.TrimSpace(model); model != "" {
		env = append(env, claudeTierModelEnv(model)...)
	}
	return env, func() {}, nil
}

// claudeWriteNativeConfig writes the bifrost endpoint, API key, and model
// into Claude Code's settings file (~/.claude/settings.json) so the same
// configuration is available when users launch Claude Code directly.
//
// It merges into the existing file, preserving any user-defined settings.
func claudeWriteNativeConfig(baseURL, apiKey, model string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}

	dir := filepath.Join(home, ".claude")
	settingsPath := filepath.Join(dir, "settings.json")

	// Read existing settings or start fresh
	settings := make(map[string]any)
	if b, err := os.ReadFile(settingsPath); err == nil {
		if err := sonic.Unmarshal(b, &settings); err != nil {
			return fmt.Errorf("parse existing claude settings: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read claude settings: %w", err)
	}

	// Get or create the env map
	envRaw, ok := settings["env"]
	var envMap map[string]any
	if ok {
		envMap, ok = envRaw.(map[string]any)
		if !ok {
			envMap = make(map[string]any)
		}
	} else {
		envMap = make(map[string]any)
	}

	envMap["ANTHROPIC_BASE_URL"] = baseURL
	envMap["ANTHROPIC_API_KEY"] = apiKey
	if model = strings.TrimSpace(model); model != "" {
		for key, value := range claudeTierModelEnvMap(model) {
			envMap[key] = value
		}
		delete(envMap, "ANTHROPIC_MODEL")
	}

	settings["env"] = envMap

	b, err := sonic.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal claude settings: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create claude config dir: %w", err)
	}
	return config.WriteAtomic(settingsPath, b, 0o600)
}

func claudeTierModelEnv(model string) []string {
	envMap := claudeTierModelEnvMap(model)
	return []string{
		"ANTHROPIC_DEFAULT_SONNET_MODEL=" + envMap["ANTHROPIC_DEFAULT_SONNET_MODEL"],
		"ANTHROPIC_DEFAULT_OPUS_MODEL=" + envMap["ANTHROPIC_DEFAULT_OPUS_MODEL"],
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=" + envMap["ANTHROPIC_DEFAULT_HAIKU_MODEL"],
	}
}

func claudeTierModelEnvMap(model string) map[string]string {
	return map[string]string{
		"ANTHROPIC_DEFAULT_SONNET_MODEL": model,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   model,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  model,
	}
}

// codexPreLaunch persists the bifrost endpoint and virtual key into Codex
// CLI's native config files. Codex resolves its credentials from
// ~/.codex/auth.json (which takes precedence over OPENAI_API_KEY in the
// process env) and its endpoint from ~/.codex/config.toml, so a stale
// auth.json from a prior run will otherwise shadow whatever Bifrost passes
// via env vars. Returns no extra env (BuildEnv already exports the key) and
// no cleanup — the writes are intentionally persistent so direct `codex`
// launches outside Bifrost keep working too.
func codexPreLaunch(baseURL, apiKey, model string) ([]string, func(), error) {
	if err := codexWriteNativeConfig(baseURL, apiKey, model); err != nil {
		return nil, nil, err
	}
	return nil, func() {}, nil
}

// codexWriteNativeConfig writes the bifrost endpoint, API key, and model
// into Codex CLI's config files (~/.codex/auth.json and
// ~/.codex/config.toml) so the same configuration is available when users
// launch Codex directly.
//
// It merges into the existing files, preserving any user-defined settings.
func codexWriteNativeConfig(baseURL, apiKey, model string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}

	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create codex config dir: %w", err)
	}

	if apiKey = strings.TrimSpace(apiKey); apiKey != "" && apiKey != "dummy-key" {
		if err := codexWriteAuth(filepath.Join(dir, "auth.json"), apiKey); err != nil {
			return err
		}
	}
	return codexWriteConfigTOML(filepath.Join(dir, "config.toml"), baseURL, model)
}

// codexWriteAuth merges the bifrost virtual key into Codex's auth.json,
// preserving any other fields the user (or Codex itself) may have written.
func codexWriteAuth(path, apiKey string) error {
	auth := make(map[string]any)
	if b, err := os.ReadFile(path); err == nil {
		if err := sonic.Unmarshal(b, &auth); err != nil {
			// Unparseable auth.json — overwrite rather than fail the launch,
			// since the user can't easily recover from a corrupt file.
			auth = make(map[string]any)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read codex auth: %w", err)
	}

	auth["auth_mode"] = "apikey"
	auth["OPENAI_API_KEY"] = apiKey

	b, err := sonic.MarshalIndent(auth, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal codex auth: %w", err)
	}
	return config.WriteAtomic(path, b, 0o600)
}

// codexWriteConfigTOML merges Bifrost's endpoint and (optionally) model into
// the top-level section of Codex's config.toml. Existing top-level keys
// (e.g. model_reasoning_effort) and tables (e.g. [projects.*], [tui.*]) are
// preserved verbatim, including comments and ordering.
func codexWriteConfigTOML(path, baseURL, model string) error {
	var existing []byte
	if b, err := os.ReadFile(path); err == nil {
		existing = b
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read codex config: %w", err)
	}

	targets := map[string]string{
		"openai_base_url": baseURL,
		"env_key":         "OPENAI_API_KEY",
	}
	if m := strings.TrimSpace(model); m != "" {
		targets["model"] = m
	}

	return config.WriteAtomic(path, setTopLevelTOMLKeys(existing, targets), 0o600)
}

// setTopLevelTOMLKeys returns data with each key in targets set to its
// target value in the top-level section (above any [table] header). Keys
// that already exist in the top-level section are replaced in place; keys
// that don't are appended just before the first table header (or at EOF if
// none). Everything else — table contents, comments, blank lines, ordering
// — is preserved as-is.
func setTopLevelTOMLKeys(data []byte, targets map[string]string) []byte {
	if len(targets) == 0 {
		return data
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		keys := make([]string, 0, len(targets))
		for k := range targets {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			b.WriteString(k)
			b.WriteString(" = ")
			b.WriteString(tomlQuote(targets[k]))
			b.WriteByte('\n')
		}
		return []byte(b.String())
	}

	hasTrailingNewline := strings.HasSuffix(string(data), "\n")
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")

	seen := make(map[string]bool, len(targets))
	out := make([]string, 0, len(lines)+len(targets))
	firstTableIdx := -1

	for _, raw := range lines {
		if firstTableIdx < 0 && isTOMLTableHeader(raw) {
			firstTableIdx = len(out)
		}
		if firstTableIdx < 0 {
			if key, ok := matchTopLevelTOMLKey(raw, targets); ok {
				if seen[key] {
					// Drop duplicate top-level definitions.
					continue
				}
				out = append(out, key+" = "+tomlQuote(targets[key]))
				seen[key] = true
				continue
			}
		}
		out = append(out, raw)
	}

	missing := make([]string, 0, len(targets))
	for k := range targets {
		if !seen[k] {
			missing = append(missing, k+" = "+tomlQuote(targets[k]))
		}
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		if firstTableIdx < 0 {
			for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
				out = out[:len(out)-1]
			}
			out = append(out, missing...)
		} else {
			head := out[:firstTableIdx]
			tail := append([]string{}, out[firstTableIdx:]...)
			for len(head) > 0 && strings.TrimSpace(head[len(head)-1]) == "" {
				head = head[:len(head)-1]
			}
			head = append(head, missing...)
			head = append(head, "")
			out = append(head, tail...)
		}
	}

	result := strings.Join(out, "\n")
	if hasTrailingNewline || len(missing) > 0 {
		result += "\n"
	}
	return []byte(result)
}

// isTOMLTableHeader reports whether a line is a [table] or [[array]] header,
// optionally followed by an inline comment.
func isTOMLTableHeader(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "[") {
		return false
	}
	idx := strings.LastIndex(t, "]")
	if idx < 0 {
		return false
	}
	rest := strings.TrimSpace(t[idx+1:])
	return rest == "" || strings.HasPrefix(rest, "#")
}

// matchTopLevelTOMLKey returns the target key name if line is a simple
// `key = ...` assignment whose key appears in targets.
func matchTopLevelTOMLKey(line string, targets map[string]string) (string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", false
	}
	head, _, ok := strings.Cut(t, "=")
	if !ok {
		return "", false
	}
	name := strings.TrimSpace(head)
	if _, ok := targets[name]; ok {
		return name, true
	}
	return "", false
}

// tomlQuote returns a TOML basic string literal for s.
func tomlQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
