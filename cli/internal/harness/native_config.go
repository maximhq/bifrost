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
// Tool search lets Claude load tool definitions on demand, while gateway model
// discovery lets it query models exposed by Bifrost's Anthropic-compatible
// endpoint. Existing user overrides always win.
func claudePreLaunch(baseURL, apiKey, model string) ([]string, func(), error) {
	var env []string
	if _, exists := os.LookupEnv("ENABLE_TOOL_SEARCH"); !exists {
		env = append(env, "ENABLE_TOOL_SEARCH=true")
	}
	if _, exists := os.LookupEnv("CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"); !exists {
		env = append(env, "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1")
	}
	if model = strings.TrimSpace(model); model != "" {
		env = append(env, "ANTHROPIC_MODEL="+model)
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
	envMap["ANTHROPIC_AUTH_TOKEN"] = apiKey
	delete(envMap, "ANTHROPIC_API_KEY")
	if _, exists := envMap["ENABLE_TOOL_SEARCH"]; !exists {
		envMap["ENABLE_TOOL_SEARCH"] = "true"
	}
	if _, exists := envMap["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"]; !exists {
		envMap["CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY"] = "1"
	}
	delete(settings, "apiKeyHelper")
	if model = strings.TrimSpace(model); model != "" {
		for key, value := range claudeTierModelEnvMap(model) {
			envMap[key] = value
		}
		envMap["ANTHROPIC_MODEL"] = model
		settings["model"] = model
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

// codexPreLaunch creates an isolated Codex home with a Bifrost Responses provider.
// Isolation prevents a launcher session from overwriting the user's persistent
// Codex authentication, configuration, or session history.
func codexPreLaunch(baseURL, _ string, model string) ([]string, func(), error) {
	temporaryHome, err := os.MkdirTemp("", "bifrost-codex-home-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create temporary Codex home: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(temporaryHome) }
	sourceHome, err := codexUserHome()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	sourceConfig := filepath.Join(sourceHome, "config.toml")
	targetConfig := filepath.Join(temporaryHome, "config.toml")
	if existing, readErr := os.ReadFile(sourceConfig); readErr == nil {
		if writeErr := config.WriteAtomic(targetConfig, existing, 0o600); writeErr != nil {
			cleanup()
			return nil, nil, fmt.Errorf("copy Codex config into temporary home: %w", writeErr)
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		cleanup()
		return nil, nil, fmt.Errorf("read Codex config: %w", readErr)
	}
	if err := codexWriteConfigTOML(targetConfig, baseURL, model); err != nil {
		cleanup()
		return nil, nil, err
	}
	return []string{"CODEX_HOME=" + temporaryHome}, cleanup, nil
}

func codexUserHome() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CODEX_HOME")); configured != "" {
		return configured, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".codex"), nil
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

// codexWriteConfigTOML declares Bifrost as a custom Responses provider.
// Current Codex versions ignore OPENAI_BASE_URL and require a custom provider
// to disable OpenAI OAuth, so the provider table is part of the routing contract.
func codexWriteConfigTOML(path, baseURL, model string) error {
	var existing []byte
	if b, err := os.ReadFile(path); err == nil {
		existing = b
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read codex config: %w", err)
	}

	targets := map[string]string{"model_provider": "bifrost"}
	if m := strings.TrimSpace(model); m != "" {
		targets["model"] = m
	}
	existing = removeTopLevelTOMLKeys(existing, map[string]struct{}{
		"openai_base_url": {},
		"env_key":         {},
	})
	configured := setTopLevelTOMLKeys(existing, targets)
	providerURL := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if !strings.HasSuffix(providerURL, "/v1") {
		providerURL += "/v1"
	}
	providerBody := []string{
		`name = "Bifrost"`,
		"base_url = " + tomlQuote(providerURL),
		`wire_api = "responses"`,
		`env_key = "OPENAI_API_KEY"`,
		`requires_openai_auth = false`,
		`supports_websockets = false`,
	}
	configured = replaceTOMLTable(configured, "model_providers.bifrost", providerBody)
	return config.WriteAtomic(path, configured, 0o600)
}

// removeTopLevelTOMLKeys drops obsolete assignments before the first table.
func removeTopLevelTOMLKeys(data []byte, keys map[string]struct{}) []byte {
	hasTrailingNewline := strings.HasSuffix(string(data), "\n")
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	result := make([]string, 0, len(lines))
	inTopLevel := true
	for _, line := range lines {
		if inTopLevel && isTOMLTableHeader(line) {
			inTopLevel = false
		}
		if inTopLevel {
			head, _, ok := strings.Cut(strings.TrimSpace(line), "=")
			if ok {
				if _, remove := keys[strings.TrimSpace(head)]; remove {
					continue
				}
			}
		}
		result = append(result, line)
	}
	joined := strings.Join(result, "\n")
	if hasTrailingNewline && joined != "" {
		joined += "\n"
	}
	return []byte(joined)
}

// replaceTOMLTable replaces one exact table while preserving all unrelated content.
func replaceTOMLTable(data []byte, name string, body []string) []byte {
	header := "[" + name + "]"
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	result := make([]string, 0, len(lines)+len(body)+2)
	skipping := false
	for _, line := range lines {
		if tomlTableHeaderMatches(line, name) {
			skipping = true
			continue
		}
		if skipping {
			if isTOMLTableHeader(line) {
				skipping = false
			} else {
				continue
			}
		}
		result = append(result, line)
	}
	for len(result) > 0 && strings.TrimSpace(result[len(result)-1]) == "" {
		result = result[:len(result)-1]
	}
	if len(result) > 0 {
		result = append(result, "")
	}
	result = append(result, header)
	result = append(result, body...)
	return []byte(strings.Join(result, "\n") + "\n")
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

// tomlUnquoteKey strips a basic ("...") or literal ('...') TOML string
// wrapper from a key or dotted-path segment, so a quoted identifier can be
// compared against its bare equivalent — TOML treats model_provider,
// "model_provider", and 'model_provider' as the same identifier. Escape
// sequences inside a basic string are deliberately not decoded: a segment
// that needs them is left quoted (and so never matches) rather than risk
// silently mismatching. This is a scoped helper for this file's own
// hardcoded, escape-free target keys and table paths, not a general TOML
// parser (it doesn't handle a literal "." inside a quoted segment).
func tomlUnquoteKey(segment string) string {
	if len(segment) >= 2 {
		if segment[0] == '"' && segment[len(segment)-1] == '"' && !strings.ContainsAny(segment[1:len(segment)-1], `"\`) {
			return segment[1 : len(segment)-1]
		}
		if segment[0] == '\'' && segment[len(segment)-1] == '\'' {
			return segment[1 : len(segment)-1]
		}
	}
	return segment
}

// tomlTableHeaderMatches reports whether line declares the given dotted
// table name, tolerating a trailing inline comment (e.g.
// "[model_providers.bifrost] # custom provider") and a quoted form of any
// dotted segment (e.g. [model_providers."bifrost"]), which TOML treats as
// identical to the bare form.
func tomlTableHeaderMatches(line, name string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return false
	}
	idx := strings.LastIndex(trimmed, "]")
	if idx < 0 {
		return false
	}
	rest := strings.TrimSpace(trimmed[idx+1:])
	if rest != "" && !strings.HasPrefix(rest, "#") {
		return false
	}
	declared := strings.Split(trimmed[1:idx], ".")
	target := strings.Split(name, ".")
	if len(declared) != len(target) {
		return false
	}
	for i, segment := range declared {
		if tomlUnquoteKey(strings.TrimSpace(segment)) != target[i] {
			return false
		}
	}
	return true
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
	name := tomlUnquoteKey(strings.TrimSpace(head))
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
