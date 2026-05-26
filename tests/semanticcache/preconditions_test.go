package semanticcache

import (
	"net/http"
	"strings"
	"testing"
)

// TestPreconditions verifies the test env is ready (Bifrost reachable,
// providers configured, plugin absent at run start). Pure checks, no state
// changes. Trusts env for vector-store config (per plan §13.4).
func TestPreconditions(t *testing.T) {
	lc := newLogCtx("preconditions", "preconditions")
	logf(t, lc.at(0), "SETUP", "phase_start", map[string]any{"bifrost_url": cfg.BifrostURL})

	t.Run("0.1_bifrost_reachable", func(t *testing.T) {
		lc := lc
		lc.name = "0.1_bifrost_reachable"
		status, body, _, err := doJSON(t, "GET", "/api/plugins", nil, nil)
		if err != nil || status != http.StatusOK {
			logf(t, lc.at(1), "FAIL", "bifrost_unreachable", map[string]any{
				"status": status, "err": err,
			})
			t.Fatalf("GET /api/plugins failed: status=%d err=%v body=%s",
				status, err, truncate(string(body), 200))
		}
		logf(t, lc.at(1), "PASS", "bifrost_reachable", map[string]any{"status": status})
	})

	t.Run("0.2_openai_configured", func(t *testing.T) {
		lc := lc
		lc.name = "0.2_openai_configured"
		ps := providersList(t, lc, 1)
		if !hasProvider(ps, "openai") {
			logf(t, lc.at(2), "FAIL", "openai_missing", nil)
			t.Fatalf("openai provider not configured (got %d providers)", len(ps))
		}
		logf(t, lc.at(2), "PASS", "openai_present", nil)
	})

	t.Run("0.3_optional_providers", func(t *testing.T) {
		lc := lc
		lc.name = "0.3_optional_providers"
		ps := providersList(t, lc, 1)
		for _, want := range []string{"gemini", "anthropic"} {
			if hasProvider(ps, want) {
				logf(t, lc.at(2), "PASS", "provider_present", map[string]any{"provider": want})
			} else {
				logf(t, lc.at(2), "WARN", "provider_absent", map[string]any{
					"provider": want,
					"effect":   "cross-provider cases will skip",
				})
			}
		}
	})

	// The plugin-absent precondition is enforced in TestMain (with RUN_FORCE=1
	// auto-deleting a pre-existing row). We don't re-check here because tests
	// run in alphabetical file order — TestDirect / TestSemantic / TestLifecycle
	// create their own plugin and may leave it loaded for the next test.

	logf(t, lc.at(99), "TEARDOWN", "phase_end", nil)
}

func hasProvider(ps []providerSummary, name string) bool {
	for _, p := range ps {
		if strings.EqualFold(p.Name, name) {
			return true
		}
	}
	return false
}
