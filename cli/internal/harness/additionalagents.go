package harness

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/maximhq/bifrost/cli/internal/config"
)

// Provider contracts and upstream references are recorded in cli/HARNESS_SOURCES.md.
func piWriteNativeConfig(baseURL, apiKey, model string) error {
	paths, err := NativeConfigPaths("pi")
	if err != nil {
		return err
	}
	return writePiConfig(paths, baseURL, apiKey, model)
}

func writePiConfig(paths []string, baseURL, apiKey, model string) error {
	models, err := readNativeJSON(paths[0])
	if err != nil {
		return err
	}
	settings, err := readNativeJSON(paths[1])
	if err != nil {
		return err
	}
	auth, err := readNativeJSON(paths[2])
	if err != nil {
		return err
	}
	model = strings.TrimSpace(model)
	if model == "" && settings["defaultProvider"] == "bifrost" {
		model, _ = settings["defaultModel"].(string)
	}
	if model == "" {
		return fmt.Errorf("Pi requires a model; pass --model provider/model")
	}
	providers, err := nativeObject(models, "providers")
	if err != nil {
		return err
	}
	// Replace only our provider, removing stale per-model URLs, headers and keys.
	// A saved project selector is kept so a plain reconfigure does not drop
	// project attribution; WriteProjectConfig replaces it when one is given.
	provider := map[string]any{
		"name": "Bifrost", "baseUrl": openAIBaseURL(baseURL), "api": "openai-completions", "authHeader": true,
		"models": []any{map[string]any{"id": model, "name": model}},
	}
	if previous, ok := providers["bifrost"].(map[string]any); ok {
		if headers, ok := previous["headers"].(map[string]any); ok {
			kept := map[string]any{}
			for key, value := range headers {
				if config.IsProjectHeader(key) {
					kept[key] = value
				}
			}
			if len(kept) > 0 {
				provider["headers"] = kept
			}
		}
	}
	providers["bifrost"] = provider
	// Saved credentials take precedence over models.json; update the same provider
	// in auth.json, retaining all other providers' credentials.
	auth["bifrost"] = map[string]any{"type": "api_key", "key": apiKey}
	settings["defaultProvider"], settings["defaultModel"] = "bifrost", model
	for i, value := range []map[string]any{models, settings, auth} {
		if err := writeNativeJSON(paths[i], value); err != nil {
			return err
		}
	}
	return nil
}

// A temporary agent directory keeps Pi's native config untouched while retaining
// access to existing resources, sessions and credentials for other providers.
func piPreLaunch(baseURL, apiKey, model string) ([]string, func(), error) {
	paths, err := NativeConfigPaths("pi")
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "bifrost-pi-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	source := filepath.Dir(paths[0])
	if err := os.MkdirAll(filepath.Join(source, "sessions"), 0o700); err != nil {
		cleanup()
		return nil, nil, err
	}
	entries, err := os.ReadDir(source)
	if err != nil && !os.IsNotExist(err) {
		cleanup()
		return nil, nil, err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "models.json", "settings.json", "auth.json":
			continue
		}
		if err := os.Symlink(filepath.Join(source, entry.Name()), filepath.Join(dir, entry.Name())); err != nil {
			cleanup()
			return nil, nil, err
		}
	}
	tempPaths := make([]string, len(paths))
	for i, path := range paths {
		tempPaths[i] = filepath.Join(dir, filepath.Base(path))
		body, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		if err := config.WriteAtomic(tempPaths[i], body, 0o600); err != nil {
			cleanup()
			return nil, nil, err
		}
	}
	if err := writePiConfig(tempPaths, baseURL, apiKey, model); err != nil {
		cleanup()
		return nil, nil, err
	}
	return []string{"PI_CODING_AGENT_DIR=" + dir}, cleanup, nil
}

func antigravityShellProfile(home string) (string, error) {
	switch filepath.Base(os.Getenv("SHELL")) {
	case "zsh":
		dir := os.Getenv("ZDOTDIR")
		if dir == "" {
			dir = home
		}
		return filepath.Abs(filepath.Join(dir, ".zshrc"))
	case "bash":
		return filepath.Join(home, ".bashrc"), nil
	default:
		return "", fmt.Errorf("persistent Antigravity configuration requires bash or zsh (SHELL); launch with 'bifrost antigravity' on other shells")
	}
}

func antigravitySettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"), nil
}

// ValidRecordedAntigravityPaths reports whether a restoration receipt names the
// current Antigravity settings file and a shell profile configure could have
// written: ~/.bashrc, or a .zshrc under any (possibly former) ZDOTDIR.
func ValidRecordedAntigravityPaths(recorded []string) bool {
	settings, err := antigravitySettingsPath()
	if err != nil || len(recorded) != 2 || recorded[0] != settings {
		return false
	}
	profile := recorded[1]
	if !filepath.IsAbs(profile) || filepath.Clean(profile) != profile {
		return false
	}
	switch filepath.Base(profile) {
	case ".bashrc":
		home, err := os.UserHomeDir()
		return err == nil && filepath.Dir(profile) == home
	case ".zshrc":
		return true
	}
	return false
}

// agy 1.4.8 headless mode does not reliably apply the saved model itself.
func antigravityRunArgs(model string) []string {
	if strings.TrimSpace(model) == "" {
		if path, err := antigravitySettingsPath(); err == nil {
			if settings, err := readNativeJSON(path); err == nil {
				model, _ = settings["model"].(string)
			}
		}
	}
	if strings.TrimSpace(model) == "" {
		return nil
	}
	return []string{"--model", model}
}

func setAntigravitySettings(settings map[string]any, model string) error {
	settings["modelProvider"] = "gemini"
	if model = strings.TrimSpace(model); model != "" {
		custom, err := nativeObject(settings, "customModelsConfig")
		if err != nil {
			return err
		}
		models, err := nativeObject(custom, "customModels")
		if err != nil {
			return err
		}
		// agy 1.4.8 resolves the suffix as the model ID when GOOGLE_GEMINI_BASE_URL
		// is supplied; embedding the URL here incorrectly sends it as part of the ID.
		models[model] = map[string]any{"modelName": "gemini-api://" + model, "label": model, "toolFormatterType": "TOOL_FORMATTER_TYPE_NONE"}
		settings["model"] = model
	}
	return nil
}

const agyBlockStart = "# >>> bifrost antigravity >>>"
const agyBlockEnd = "# <<< bifrost antigravity <<<"

func shellLiteral(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("shell configuration values must not contain newlines or NUL")
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'", nil
}

func antigravityWriteNativeConfig(baseURL, apiKey, model string) error {
	paths, err := NativeConfigPaths("antigravity")
	if err != nil {
		return err
	}
	settings, err := readNativeJSON(paths[0])
	if err != nil {
		return err
	}
	key, err := shellLiteral(apiKey)
	if err != nil {
		return err
	}
	endpoint, err := shellLiteral(baseURL)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(paths[1])
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := string(body)
	if strings.Contains(text, agyBlockStart) || strings.Contains(text, agyBlockEnd) {
		start, end := strings.Index(text, agyBlockStart), strings.Index(text, agyBlockEnd)
		if start < 0 || end < start || strings.Count(text, agyBlockStart) != 1 || strings.Count(text, agyBlockEnd) != 1 {
			return fmt.Errorf("malformed Bifrost Antigravity shell block")
		}
		text = text[:start] + text[end+len(agyBlockEnd):]
	}
	// Scope credentials to agy rather than exporting them to unrelated commands.
	if err := setAntigravitySettings(settings, model); err != nil {
		return err
	}
	modelArg := ""
	if saved, _ := settings["model"].(string); strings.TrimSpace(saved) != "" {
		quoted, err := shellLiteral(saved)
		if err != nil {
			return err
		}
		modelArg = " --model " + quoted
	}
	block := "\n" + agyBlockStart + "\nfunction agy() {\n  GEMINI_API_KEY=" + key + " GOOGLE_GEMINI_BASE_URL=" + endpoint + " command agy" + modelArg + " \"$@\"\n}\n" + agyBlockEnd + "\n"
	if err := writeNativeJSON(paths[0], settings); err != nil {
		return err
	}
	return config.WriteAtomic(paths[1], []byte(text+block), 0o600)
}

// Antigravity has no documented temporary-settings flag. Guard the startup
// fields with an exclusive lock, then restore them on exit without discarding
// unrelated settings the native CLI may have saved during the session.
func antigravityPreLaunch(baseURL, apiKey, model string) ([]string, func(), error) {
	path, err := antigravitySettingsPath()
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, err
	}
	lockPath := path + ".bifrost-lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("Antigravity settings are in use (or a previous launch left %s): %w", lockPath, err)
	}
	_ = lock.Close()
	release := func() { _ = os.Remove(lockPath) }
	original, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		release()
		return nil, nil, err
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	settings, err := readNativeJSON(path)
	if err != nil {
		release()
		return nil, nil, err
	}
	old := map[string]any{}
	originalSettings, err := readNativeJSON(path)
	if err != nil {
		release()
		return nil, nil, err
	}
	for _, name := range []string{"modelProvider", "model", "customModelsConfig"} {
		if value, ok := originalSettings[name]; ok {
			old[name] = value
		}
	}
	if err := setAntigravitySettings(settings, model); err != nil {
		release()
		return nil, nil, err
	}
	if err := writeNativeJSON(path, settings); err != nil {
		release()
		return nil, nil, err
	}
	written, _ := os.ReadFile(path)
	cleanup := func() {
		defer release()
		current, err := os.ReadFile(path)
		if err != nil {
			return
		}
		if bytes.Equal(current, written) {
			if existed {
				_ = config.WriteAtomic(path, original, mode)
			} else {
				_ = os.Remove(path)
			}
			return
		}
		currentSettings, err := readNativeJSON(path)
		if err != nil {
			return
		}
		for _, name := range []string{"modelProvider", "model", "customModelsConfig"} {
			if name == "model" && strings.TrimSpace(model) == "" {
				continue
			}
			// Retain a user's explicit edit to the same field.
			if !reflect.DeepEqual(currentSettings[name], settings[name]) {
				continue
			}
			if value, ok := old[name]; ok {
				currentSettings[name] = value
			} else {
				delete(currentSettings, name)
			}
		}
		_ = writeNativeJSON(path, currentSettings)
	}
	return []string{"GEMINI_API_KEY=" + apiKey, "GOOGLE_GEMINI_BASE_URL=" + baseURL}, cleanup, nil
}
