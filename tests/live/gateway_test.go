package live

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Bifrost's HTTP API: virtual keys, budgets, pricing overrides and the logs a session leaves.

// apiCall sends one JSON request to the gateway and returns the status and body.
func apiCall(method, path string, body any, headers map[string]string) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, gatewayURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// OSS setup lock: /api needs the setup token while dashboard auth is not active; a deployed
	// gateway with dashboard auth needs the admin's Authorization header instead.
	req.Header.Set("X-Bifrost-Setup-Token", setupToken())
	if auth := os.Getenv("BIFROST_ADMIN_AUTH"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

// setupToken returns the setup token the live test gateway is configured with
// (tests/live/config.json setup_token), overridable through the environment.
func setupToken() string {
	if v := os.Getenv("BIFROST_SETUP_TOKEN"); v != "" {
		return v
	}
	return "bifrost-live-setup-token"
}

func mustAPI(t *testing.T, method, path string, body any) gjson.Result {
	t.Helper()
	status, raw, err := apiCall(method, path, body, nil)
	require.NoError(t, err, "%s %s", method, path)
	require.Less(t, status, 300, "%s %s → %d: %s", method, path, status, raw)
	return gjson.ParseBytes(raw)
}

// virtualKeySpec is what a test needs of a key: which models it may use and what it may spend.
type virtualKeySpec struct {
	allowedModels   []string
	budgetUSD       *float64
	requestMaxLimit *int64
	tokenMaxLimit   *int64
}

type virtualKey struct {
	ID       string
	Value    string
	BudgetID string
}

// createVirtualKey mints a key for one test and deletes it when the test ends.
func createVirtualKey(t *testing.T, spec virtualKeySpec) virtualKey {
	t.Helper()
	allowed := spec.allowedModels
	if len(allowed) == 0 {
		allowed = []string{"*"}
	}
	body := map[string]any{
		"name":             "live-e2e " + t.Name() + " " + fmt.Sprint(time.Now().UnixNano()),
		"provider_configs": []any{map[string]any{"provider": "openai", "weight": 1.0, "allowed_models": allowed, "key_ids": []string{"*"}}},
	}
	if spec.budgetUSD != nil {
		body["budgets"] = []any{map[string]any{"max_limit": *spec.budgetUSD, "reset_duration": "1h"}}
	}
	if spec.requestMaxLimit != nil || spec.tokenMaxLimit != nil {
		limit := map[string]any{}
		if spec.requestMaxLimit != nil {
			limit["request_max_limit"] = *spec.requestMaxLimit
			limit["request_reset_duration"] = "1h"
		}
		if spec.tokenMaxLimit != nil {
			limit["token_max_limit"] = *spec.tokenMaxLimit
			limit["token_reset_duration"] = "1h"
		}
		body["rate_limit"] = limit
	}
	created := mustAPI(t, http.MethodPost, "/api/governance/virtual-keys", body)
	vk := virtualKey{
		ID:       created.Get("virtual_key.id").Str,
		Value:    created.Get("virtual_key.value").Str,
		BudgetID: created.Get("virtual_key.budgets.0.id").Str,
	}
	require.NotEmpty(t, vk.ID, "virtual key id: %s", created.Raw)
	require.NotEmpty(t, vk.Value, "virtual key value: %s", created.Raw)
	t.Cleanup(func() {
		// SQLite serialises writers; parallel tests finishing together can see "database is locked".
		for attempt := 0; attempt < 5; attempt++ {
			if status, _, err := apiCall(http.MethodDelete, "/api/governance/virtual-keys/"+vk.ID, nil, nil); err == nil && status < 500 {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
	})
	return vk
}

// waitBudgetUsage waits for a budget to reflect a spend; governance records it asynchronously.
func waitBudgetUsage(t *testing.T, budgetID string, expected float64) {
	t.Helper()
	deadline := time.Now().Add(rowWaitTimeout)
	for {
		usage := budgetUsage(t, budgetID)
		if usage > expected-1e-9 && usage < expected+1e-9 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("budget %s usage is %v, want %v", budgetID, usage, expected)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// budgetUsage reads what a budget has spent so far.
func budgetUsage(t *testing.T, budgetID string) float64 {
	t.Helper()
	budgets := mustAPI(t, http.MethodGet, "/api/governance/budgets", nil)
	for _, budget := range budgets.Get("budgets").Array() {
		if budget.Get("id").Str == budgetID {
			return budget.Get("current_usage").Float()
		}
	}
	t.Fatalf("budget %s not found", budgetID)
	return 0
}

// Fake-mode prices, chosen so the arithmetic in assertions is exact: a voice second costs a
// tenth of a cent, a backend token a millionth of a dollar in, two out.
const (
	fakeVoiceCostPerSecond = 0.001
	fakeInputCostPerToken  = 0.000001
	fakeOutputCostPerToken = 0.000002
)

// installFakePricing pins the prices the fake-mode gateway bills with, replacing any left by an
// earlier run. Overrides win over the datasheet, so the feed's rows do not matter.
func installFakePricing() error {
	if err := removeFakePricing(); err != nil {
		return err
	}
	overrides := []map[string]any{
		{"name": "live-e2e voice", "scope_kind": "global", "match_type": "exact", "pattern": voiceModel,
			"request_types": []string{"live"}, "patch": map[string]any{"input_cost_per_second": fakeVoiceCostPerSecond}},
	}
	for _, model := range []string{backendModel, backendModel2} {
		overrides = append(overrides, map[string]any{"name": "live-e2e backend " + model, "scope_kind": "global", "match_type": "exact", "pattern": model,
			"request_types": []string{"responses"}, "patch": map[string]any{"input_cost_per_token": fakeInputCostPerToken, "output_cost_per_token": fakeOutputCostPerToken}})
	}
	for _, override := range overrides {
		if status, raw, err := apiCall(http.MethodPost, "/api/governance/pricing-overrides", override, nil); err != nil || status >= 300 {
			return fmt.Errorf("create pricing override %s: %d %s %v", override["name"], status, raw, err)
		}
	}
	return nil
}

// removeFakePricing deletes the suite's pricing overrides, so a gateway reused after the run
// does not keep charging the test models at test prices.
func removeFakePricing() error {
	status, raw, err := apiCall(http.MethodGet, "/api/governance/pricing-overrides?limit=200", nil, nil)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("list pricing overrides: %d %s", status, raw)
	}
	for _, existing := range gjson.GetBytes(raw, "pricing_overrides").Array() {
		if strings.HasPrefix(existing.Get("name").Str, "live-e2e ") {
			if status, raw, err := apiCall(http.MethodDelete, "/api/governance/pricing-overrides/"+existing.Get("id").Str, nil, nil); err != nil || status >= 300 {
				return fmt.Errorf("delete pricing override: %d %s %v", status, raw, err)
			}
		}
	}
	return nil
}

// suiteStart bounds which rows the suite looks at: only those logged since it began.
var suiteStart = time.Now().UTC().Add(-5 * time.Second)

// providerSessionOf caches which provider session each fetched row belongs to; the list
// endpoint returns rows without their payload, so the payload is read once per row.
var providerSessionOf sync.Map

// findLiveLog waits for the one row a session leaves, found by the provider's session id.
func findLiveLog(t *testing.T, providerSessionID string) gjson.Result {
	t.Helper()
	deadline := time.Now().Add(rowWaitTimeout)
	for {
		matches := liveLogRows(t, providerSessionID)
		require.LessOrEqual(t, len(matches), 1, "a session must log exactly one row, found %d for %s", len(matches), providerSessionID)
		if len(matches) == 1 {
			return matches[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no log row for provider session %s within %s", providerSessionID, rowWaitTimeout)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// liveLogRows returns the full rows a provider session left, for the once-and-only-once checks.
func liveLogRows(t *testing.T, providerSessionID string) []gjson.Result {
	t.Helper()
	status, raw, err := apiCall(http.MethodGet, "/api/logs?objects=live.session&limit=500&start_time="+suiteStart.Format(time.RFC3339Nano), nil, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "list logs: %s", raw)
	var matches []gjson.Result
	for _, summary := range gjson.GetBytes(raw, "logs").Array() {
		id := summary.Get("id").Str
		owner, known := providerSessionOf.Load(id)
		if !known {
			row := fetchLog(t, id)
			owner = row.Get("live_session.provider_session_id").Str
			if owner != "" {
				providerSessionOf.Store(id, owner)
			}
		}
		if owner == providerSessionID {
			matches = append(matches, fetchLog(t, id))
		}
	}
	return matches
}

// contentRowSessionOf caches which provider session each download row belongs to.
var contentRowSessionOf sync.Map

// findContentLog waits for the row a recording download leaves, found by the provider's session
// id in its metadata. A download is a request of its own, so it never shares the session's row.
func findContentLog(t *testing.T, providerSessionID string) gjson.Result {
	t.Helper()
	deadline := time.Now().Add(rowWaitTimeout)
	for {
		status, raw, err := apiCall(http.MethodGet, "/api/logs?objects=live_content&limit=500&start_time="+suiteStart.Format(time.RFC3339Nano), nil, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "list logs: %s", raw)
		var matches []gjson.Result
		for _, summary := range gjson.GetBytes(raw, "logs").Array() {
			id := summary.Get("id").Str
			owner, known := contentRowSessionOf.Load(id)
			if !known {
				row := fetchLog(t, id)
				owner = row.Get("metadata.provider_session_id").Str
				if owner != "" {
					contentRowSessionOf.Store(id, owner)
				}
			}
			if owner == providerSessionID {
				matches = append(matches, fetchLog(t, id))
			}
		}
		require.LessOrEqual(t, len(matches), 1, "one download logs one row, found %d for %s", len(matches), providerSessionID)
		if len(matches) == 1 {
			return matches[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no live_content row for provider session %s within %s", providerSessionID, rowWaitTimeout)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// liveLogRowsForVirtualKey lists the session rows a virtual key has logged since the suite began.
// It finds rows of sessions that never reached the provider, which carry no provider session id.
func liveLogRowsForVirtualKey(t *testing.T, vkID string) []gjson.Result {
	t.Helper()
	status, raw, err := apiCall(http.MethodGet, "/api/logs?objects=live.session&virtual_key_ids="+vkID+"&limit=50&start_time="+suiteStart.Format(time.RFC3339Nano), nil, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "list logs: %s", raw)
	var rows []gjson.Result
	for _, summary := range gjson.GetBytes(raw, "logs").Array() {
		rows = append(rows, fetchLog(t, summary.Get("id").Str))
	}
	return rows
}

// findSessionRow waits for a row of an object type grouped under a session id by the client.
func findSessionRow(t *testing.T, sessionID, object string) gjson.Result {
	t.Helper()
	deadline := time.Now().Add(rowWaitTimeout)
	for {
		status, raw, err := apiCall(http.MethodGet, "/api/logs?objects="+object+"&session_id="+sessionID+"&limit=10", nil, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "list logs: %s", raw)
		rows := gjson.GetBytes(raw, "logs").Array()
		if len(rows) == 1 {
			return fetchLog(t, rows[0].Get("id").Str)
		}
		require.LessOrEqual(t, len(rows), 1, "one %s row for session %s, found %d", object, sessionID, len(rows))
		if time.Now().After(deadline) {
			t.Fatalf("no %s row for session %s within %s", object, sessionID, rowWaitTimeout)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// fetchLog reads one row with its full payload.
func fetchLog(t *testing.T, id string) gjson.Result {
	t.Helper()
	status, raw, err := apiCall(http.MethodGet, "/api/logs/"+id, nil, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "get log %s: %s", id, raw)
	row := gjson.ParseBytes(raw)
	if row.Get("log").Exists() {
		return row.Get("log")
	}
	return row
}
