package realtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Bifrost's management API: virtual keys, pricing overrides and the rows a session logs.

// Prices for every rt-e2e-* model, chosen so the cost assertions are exact.
const (
	inputPrice  = 0.000001 // per input token
	outputPrice = 0.000002 // per output token
)

// costOf is what a response with this usage costs at the suite's prices.
func costOf(usage tokens) float64 {
	return float64(usage.input)*inputPrice + float64(usage.output)*outputPrice
}

// keySpec is what a test needs of a virtual key of its own.
type keySpec struct {
	allowedModels []string // every rt-e2e-* model when empty
	budgetUSD     float64  // no budget when zero
	maxRequests   int      // no request limit when zero
}

type virtualKey struct {
	ID       string
	Value    string
	BudgetID string
}

// createVirtualKey mints a key for one test and deletes it when the test ends.
func createVirtualKey(t *testing.T, spec keySpec) virtualKey {
	t.Helper()
	vk, err := postVirtualKey("rt-e2e "+t.Name(), spec)
	require.NoError(t, err)
	t.Cleanup(func() { deleteVirtualKey(vk.ID) })
	return vk
}

func createSuiteKey() (virtualKey, error) {
	return postVirtualKey(fmt.Sprintf("rt-e2e suite %d", time.Now().UnixNano()), keySpec{})
}

func postVirtualKey(name string, spec keySpec) (virtualKey, error) {
	allowed := spec.allowedModels
	if len(allowed) == 0 {
		// "*" admits only the models the catalog lists for the provider; test models are not in it.
		allowed = []string{"regex:rt-e2e-.*"}
	}
	body := map[string]any{
		"name": name,
		"provider_configs": []any{
			map[string]any{"provider": "openai", "weight": 1.0, "allowed_models": allowed, "key_ids": []string{"*"}},
			map[string]any{"provider": "anthropic", "weight": 1.0, "allowed_models": []string{"*"}, "key_ids": []string{"*"}},
		},
	}
	if spec.budgetUSD > 0 {
		body["budgets"] = []any{map[string]any{"max_limit": spec.budgetUSD, "reset_duration": "1h"}}
	}
	if spec.maxRequests > 0 {
		body["rate_limit"] = map[string]any{"request_max_limit": spec.maxRequests, "request_reset_duration": "1h"}
	}
	status, created, err := apiCall(http.MethodPost, "/api/governance/virtual-keys", body)
	if err != nil || status >= 300 {
		return virtualKey{}, fmt.Errorf("create virtual key: %d %s %v", status, created.Raw, err)
	}
	return virtualKey{
		ID:       created.Get("virtual_key.id").Str,
		Value:    created.Get("virtual_key.value").Str,
		BudgetID: created.Get("virtual_key.budgets.0.id").Str,
	}, nil
}

func deleteVirtualKey(id string) {
	// SQLite serialises writers; tests finishing together can see "database is locked".
	for attempt := 0; attempt < 5; attempt++ {
		if status, _, err := apiCall(http.MethodDelete, "/api/governance/virtual-keys/"+id, nil); err == nil && status < 500 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitBudgetUsage waits for a budget to record a spend; governance debits it asynchronously.
func waitBudgetUsage(t *testing.T, budgetID string, want float64) {
	t.Helper()
	deadline := time.Now().Add(rowWaitTimeout)
	for {
		status, budget, err := apiCall(http.MethodGet, "/api/governance/budgets", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "list budgets: %s", budget.Raw)
		used := budget.Get(`budgets.#(id=="` + budgetID + `").current_usage`).Float()
		if used >= want-1e-12 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("budget %s spent %v, want %v", budgetID, used, want)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// installPricing prices every rt-e2e-* model for realtime turns, replacing a previous run's override.
func installPricing() error {
	if err := removePricing(); err != nil {
		return err
	}
	status, raw, err := apiCall(http.MethodPost, "/api/governance/pricing-overrides", map[string]any{
		"name": "rt-e2e models", "scope_kind": "global", "match_type": "wildcard", "pattern": "rt-e2e-*",
		"request_types": []string{"realtime"},
		"patch":         map[string]any{"input_cost_per_token": inputPrice, "output_cost_per_token": outputPrice},
	})
	if err != nil || status >= 300 {
		return fmt.Errorf("create pricing override: %d %s %v", status, raw.Raw, err)
	}
	return nil
}

func removePricing() error {
	status, raw, err := apiCall(http.MethodGet, "/api/governance/pricing-overrides?limit=200", nil)
	if err != nil || status >= 300 {
		return fmt.Errorf("list pricing overrides: %d %s %v", status, raw.Raw, err)
	}
	for _, override := range raw.Get("pricing_overrides").Array() {
		if strings.HasPrefix(override.Get("name").Str, "rt-e2e ") {
			if status, raw, err := apiCall(http.MethodDelete, "/api/governance/pricing-overrides/"+override.Get("id").Str, nil); err != nil || status >= 300 {
				return fmt.Errorf("delete pricing override: %d %s %v", status, raw.Raw, err)
			}
		}
	}
	return nil
}

// turnRow is the log row of one realtime turn, with the fields the tests assert on.
type turnRow struct {
	Status          string  `json:"status"`
	Provider        string  `json:"provider"`
	Model           string  `json:"model"`
	SelectedKeyName string  `json:"selected_key_name"`
	VirtualKeyID    string  `json:"virtual_key_id"`
	Cost            float64 `json:"cost"`
	TokenUsage      struct {
		Input  int `json:"prompt_tokens"`
		Output int `json:"completion_tokens"`
		Total  int `json:"total_tokens"`
	} `json:"token_usage"`
	Error struct {
		StatusCode int `json:"status_code"`
		Error      struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	} `json:"error_details"`
	Metadata struct {
		ProviderSessionID string `json:"provider_session_id"`
		Transport         string `json:"realtime_transport"`
	} `json:"metadata"`

	raw gjson.Result // the whole row, for anything not named above
}

// inputText is the user's side of the turn as logged: every text in its input history, joined.
func (r turnRow) inputText() string {
	var texts []string
	for _, message := range r.raw.Get("input_history").Array() {
		texts = append(texts, messageText(message))
	}
	return strings.Join(texts, "\n")
}

// outputText is the assistant's side of the turn as logged.
func (r turnRow) outputText() string { return messageText(r.raw.Get("output_message")) }

// toolCalls lists the functions the turn's output called, by name.
func (r turnRow) toolCalls() []string {
	var names []string
	for _, call := range r.raw.Get("output_message.tool_calls").Array() {
		names = append(names, call.Get("function.name").Str)
	}
	return names
}

func messageText(message gjson.Result) string {
	content := message.Get("content")
	if content.Type == gjson.String {
		return content.Str
	}
	var texts []string
	for _, block := range content.Array() {
		if text := block.Get("text").Str; text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

// turnRow waits for the session's one finished turn row.
func (s *session) turnRow() turnRow {
	s.t.Helper()
	return s.turnRows(1)[0]
}

// turnRows waits until the session has logged n finished turns and returns them oldest first.
func (s *session) turnRows(n int) []turnRow {
	s.t.Helper()
	return waitTurnRows(s.t, s.model, n)
}

// waitTurnRows waits until a model has logged n finished turns, and fails if it logs more.
func waitTurnRows(t *testing.T, model string, n int) []turnRow {
	t.Helper()
	deadline := time.Now().Add(rowWaitTimeout)
	for {
		rows := turnRowsFor(t, model)
		finished := 0
		for _, row := range rows {
			if row.Status != "processing" {
				finished++
			}
		}
		require.LessOrEqual(t, len(rows), n, "model %s logged %d turns, want %d", model, len(rows), n)
		if finished == n {
			return rows
		}
		if time.Now().After(deadline) {
			t.Fatalf("model %s logged %d finished turns of %d within %s, want %d", model, finished, len(rows), rowWaitTimeout, n)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// turnRowsFor lists the realtime turn rows a model has logged, oldest first.
func turnRowsFor(t *testing.T, model string) []turnRow {
	t.Helper()
	query := url.Values{"objects": {"realtime.turn"}, "models": {model}, "limit": {"50"}, "order": {"asc"}, "sort_by": {"timestamp"}}
	status, list, err := apiCall(http.MethodGet, "/api/logs?"+query.Encode(), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, "list logs: %s", list.Raw)
	var rows []turnRow
	for _, summary := range list.Get("logs").Array() {
		status, full, err := apiCall(http.MethodGet, "/api/logs/"+summary.Get("id").Str, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "get log: %s", full.Raw)
		if full.Get("log").Exists() {
			full = full.Get("log")
		}
		var row turnRow
		require.NoError(t, json.Unmarshal([]byte(full.Raw), &row), "decode row: %s", full.Raw)
		row.raw = full
		rows = append(rows, row)
	}
	return rows
}

// apiCall sends one JSON request to the management API and returns the status and parsed body.
func apiCall(method, path string, body any) (int, gjson.Result, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, gjson.Result{}, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, gatewayURL+path, reader)
	if err != nil {
		return 0, gjson.Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	// The setup token opens /api while dashboard auth is off (config.json); a gateway with
	// dashboard auth takes the admin's Authorization header instead.
	req.Header.Set("X-Bifrost-Setup-Token", envOr("BIFROST_SETUP_TOKEN", "bifrost-realtime-setup-token"))
	if auth := os.Getenv("BIFROST_ADMIN_AUTH"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return 0, gjson.Result{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, gjson.ParseBytes(raw), err
}

// postJSON sends an inference request, such as a client secret, with a bearer credential.
func postJSON(t *testing.T, path, bearer string, body any) (int, gjson.Result) {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, gatewayURL+path, bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	require.NoError(t, err)
	return resp.StatusCode, gjson.Parse(responseBody(resp))
}
