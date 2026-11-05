package harness

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func readAgentObject(t *testing.T, path string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPiConfigPreservesOtherProvidersAndOverridesSavedCredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(t.TempDir(), "agent"))
	paths, err := NativeConfigPaths("pi")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths[0]), 0o700); err != nil {
		t.Fatal(err)
	}
	originals := []string{`{"providers":{"other":{"baseUrl":"keep"},"bifrost":{"headers":{"Authorization":"stale"}}}}`, `{"theme":"keep"}`, `{"other":{"type":"api_key","key":"keep"},"bifrost":{"type":"oauth","access":"stale"}}`}
	for i, path := range paths {
		if err := os.WriteFile(path, []byte(originals[i]), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := piWriteNativeConfig("https://gateway/openai/v1", "selected-key", "provider/model"); err != nil {
		t.Fatal(err)
	}
	models := readAgentObject(t, paths[0])["providers"].(map[string]any)
	if models["other"].(map[string]any)["baseUrl"] != "keep" {
		t.Fatal("lost other provider")
	}
	provider := models["bifrost"].(map[string]any)
	if provider["baseUrl"] != "https://gateway/openai/v1" || provider["api"] != "openai-completions" || provider["headers"] != nil {
		t.Fatalf("bad provider: %v", provider)
	}
	auth := readAgentObject(t, paths[2])
	if auth["bifrost"].(map[string]any)["key"] != "selected-key" || auth["other"] == nil {
		t.Fatal("wrong auth")
	}
	if err := piWriteNativeConfig("https://gateway/openai", "replacement", ""); err != nil {
		t.Fatal(err)
	}
	env, cleanup, err := piPreLaunch("https://runtime/openai", "runtime-key", "")
	if err != nil {
		t.Fatal(err)
	}
	temp := strings.TrimPrefix(env[0], "PI_CODING_AGENT_DIR=")
	if readAgentObject(t, filepath.Join(temp, "auth.json"))["bifrost"].(map[string]any)["key"] != "runtime-key" {
		t.Fatal("runtime key not selected")
	}
	if readAgentObject(t, paths[2])["bifrost"].(map[string]any)["key"] != "replacement" {
		t.Fatal("launch changed native credentials")
	}
	cleanup()
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Fatal("temporary credentials retained")
	}
}

func TestAntigravityAliasesAndScopedShellCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/bash")
	for _, alias := range []string{"antigravity", "anti-gravity", "agy", "AGUI"} {
		h, ok := Get(alias)
		if !ok || h.ID != "antigravity" || h.Binary != "agy" {
			t.Fatalf("bad alias %s", alias)
		}
	}
	key := "key'$(touch SHOULD_NOT_EXIST)"
	if err := antigravityWriteNativeConfig("https://gateway/genai", key, "provider/model"); err != nil {
		t.Fatal(err)
	}
	if err := antigravityWriteNativeConfig("https://gateway/genai", key, "provider/next"); err != nil {
		t.Fatal(err)
	}
	args := antigravityRunArgs("")
	if len(args) != 2 || args[0] != "--model" || args[1] != "provider/next" {
		t.Fatalf("saved model not forwarded: %v", args)
	}
	profile := filepath.Join(home, ".bashrc")
	body, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), agyBlockStart) != 1 {
		t.Fatal("duplicate managed block")
	}
	bin := filepath.Join(home, "bin")
	_ = os.MkdirAll(bin, 0o700)
	script := "#!/bin/sh\nprintf '%s\\n' \"$GEMINI_API_KEY\" \"$GOOGLE_GEMINI_BASE_URL\" \"$3\"\n"
	if err := os.WriteFile(filepath.Join(bin, "agy"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-c", `source "$HOME/.bashrc"; agy 'argument with spaces'; printf '%s' "${GEMINI_API_KEY-unset}"`)
	cmd.Dir = home
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "GEMINI_API_KEY=parent-key")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shell: %v: %s", err, out)
	}
	if string(out) != key+"\nhttps://gateway/genai\nargument with spaces\nparent-key" {
		t.Fatalf("wrong shell routing: %s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "SHOULD_NOT_EXIST")); !os.IsNotExist(err) {
		t.Fatal("shell injection")
	}
}

func TestAntigravityLaunchRestoresSettingsAndKeepsNativeEdits(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "native edits"}[edited], func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			path, err := antigravitySettingsPath()
			if err != nil {
				t.Fatal(err)
			}
			_ = os.MkdirAll(filepath.Dir(path), 0o700)
			original := []byte("{\"model\":\"old\",\"theme\":\"keep\",\"customModelsConfig\":{\"customModels\":{\"other\":{\"modelName\":\"other\"}}}}\n")
			if err := os.WriteFile(path, original, 0o640); err != nil {
				t.Fatal(err)
			}
			_, cleanup, err := antigravityPreLaunch("https://gateway/genai", "key", "provider/model")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := antigravityPreLaunch("url", "key", "other"); err == nil {
				t.Fatal("concurrent launch accepted")
			}
			cfg := readAgentObject(t, path)
			if cfg["modelProvider"] != "gemini" {
				t.Fatal("provider not selected")
			}
			if edited {
				cfg["theme"] = "native-change"
				if err := writeNativeJSON(path, cfg); err != nil {
					t.Fatal(err)
				}
			}
			cleanup()
			restored := readAgentObject(t, path)
			if restored["model"] != "old" || restored["modelProvider"] != nil {
				t.Fatal("startup fields not restored")
			}
			if edited && restored["theme"] != "native-change" {
				t.Fatal("lost native edits")
			}
			if _, ok := restored["customModelsConfig"].(map[string]any)["customModels"].(map[string]any)["provider/model"]; ok {
				t.Fatal("temporary model retained")
			}
			if !edited {
				body, _ := os.ReadFile(path)
				info, _ := os.Stat(path)
				if string(body) != string(original) || info.Mode().Perm() != 0o640 {
					t.Fatal("original bytes/mode not restored")
				}
			}
		})
	}
}

func TestManualInstallHarnessesNameInstallInstructions(t *testing.T) {
	for _, id := range IDs() {
		h, _ := Get(id)
		if h.InstallPkg == "" && h.InstallURL == "" {
			t.Errorf("%s has no npm package and no manual install URL", id)
		}
	}
	if h, _ := Get("agy"); h.InstallURL == "" {
		t.Fatal("antigravity must point at its manual install instructions")
	}
}
