package semanticcache

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
)

// TestMain wires up the run: loads env-based config, sets up a per-run report
// directory, checks Bifrost reachability, ensures the plugin is absent (or
// deletes it under RUN_FORCE=1), then defers to the test functions.
//
// On exit, attempts a teardown DELETE so the env is clean for the next run
// — unless RUN_KEEP_PLUGIN=1.
func TestMain(m *testing.M) {
	loadConfig()
	if err := initLog(); err != nil {
		fmt.Fprintf(os.Stderr, "init log failed: %v\n", err)
		os.Exit(2)
	}
	exitCode := 1
	defer func() {
		closeLog()
		os.Exit(exitCode)
	}()

	// Sanity: Bifrost reachable.
	status, _, _, err := doRaw("GET", "/api/plugins")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[SC-E2E] FATAL: cannot reach Bifrost at %s: %v\n", cfg.BifrostURL, err)
		return
	}
	if status != http.StatusOK {
		fmt.Fprintf(os.Stderr, "[SC-E2E] FATAL: GET /api/plugins returned %d (Bifrost up at %s?)\n", status, cfg.BifrostURL)
		return
	}

	// Plugin pre-check: must be absent unless RUN_FORCE=1.
	status, body, _, err := doRaw("GET", "/api/plugins/"+pluginName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[SC-E2E] FATAL: pre-check GET /api/plugins/%s failed at %s: %v\n",
			pluginName, cfg.BifrostURL, err)
		return
	}
	if status == http.StatusOK {
		if os.Getenv("RUN_FORCE") != "1" {
			fmt.Fprintf(os.Stderr,
				"[SC-E2E] FATAL: plugin %q already exists at %s. "+
					"Set RUN_FORCE=1 to delete it and proceed.\nGET body: %s\n",
				pluginName, cfg.BifrostURL, truncate(string(body), 300))
			return
		}
		fmt.Fprintf(os.Stderr, "[SC-E2E] WARN: RUN_FORCE=1 → deleting pre-existing %q plugin\n", pluginName)
		ds, dbody, _, derr := doRaw("DELETE", "/api/plugins/"+pluginName)
		if derr != nil || (ds != http.StatusOK && ds != http.StatusNotFound) {
			fmt.Fprintf(os.Stderr, "[SC-E2E] FATAL: cannot delete pre-existing plugin: status=%d err=%v body=%s\n",
				ds, derr, truncate(string(dbody), 300))
			return
		}
	}

	fmt.Fprintf(os.Stderr, "[SC-E2E] run starting: bifrost=%s namespace=%s reports=%s trail_sid=%q\n",
		cfg.BifrostURL, cfg.Namespace, runReportDir, trailSID)

	exitCode = m.Run()

	// Teardown — best-effort cleanup so the next run starts clean.
	if os.Getenv("RUN_KEEP_PLUGIN") != "1" {
		ds, _, _, _ := doRaw("DELETE", "/api/plugins/"+pluginName)
		fmt.Fprintf(os.Stderr, "[SC-E2E] teardown: delete plugin → status=%d\n", ds)
	}
	fmt.Fprintf(os.Stderr, "[SC-E2E] run finished: exit=%d reports=%s\n", exitCode, runReportDir)
}

// doRaw is a lightweight stdout-only HTTP helper for TestMain (no *testing.T available).
func doRaw(method, path string) (int, []byte, http.Header, error) {
	req, err := http.NewRequest(method, cfg.BifrostURL+path, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	resp, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	var b []byte
	if resp.Body != nil {
		b, _ = readAllSafe(resp.Body)
	}
	return resp.StatusCode, b, resp.Header, nil
}

func readAllSafe(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return buf, err
		}
	}
}

// providersList fetches the configured providers; used by Phase 0 checks.
type providerSummary struct {
	Name string `json:"name"`
}

func providersList(t *testing.T, lc logCtx, step int) []providerSummary {
	t.Helper()
	status, body, _, err := doJSON(t, "GET", "/api/providers", nil, nil)
	if err != nil {
		t.Fatalf("providersList: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("providersList status=%d body=%s", status, truncate(string(body), 300))
	}
	// /api/providers returns {providers: [...]} based on convention.
	var wrap struct {
		Providers []providerSummary `json:"providers"`
	}
	if err := json.Unmarshal(body, &wrap); err == nil && wrap.Providers != nil {
		logf(t, lc.at(step), "INFO", "providers_list", map[string]any{"count": len(wrap.Providers)})
		return wrap.Providers
	}
	// Fallback: response may be a bare list.
	var bare []providerSummary
	if err := json.Unmarshal(body, &bare); err != nil {
		t.Fatalf("providersList decode: %v\nbody=%s", err, truncate(string(body), 500))
	}
	logf(t, lc.at(step), "INFO", "providers_list", map[string]any{"count": len(bare)})
	return bare
}
