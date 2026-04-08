package enterprise_test

// ═══════════════════════════════════════════════════════════════════════════════
// TC-006 — Content Guardrails
// Coverage: Keyword block, regex transform, flag action, scope, priority,
//           CRUD, dry-run API, disabled policy, license gating.
// ═══════════════════════════════════════════════════════════════════════════════

import (
	"net/http"
	"testing"
	"time"
)

// guardrailsSkip skips the test if guardrails feature is not available.
func guardrailsSkip(t *testing.T) {
	t.Helper()
	if enterpriseLicenseJWT == "" {
		t.Skip("requires enterprise license with guardrails feature")
	}
}

// createGuardrailPolicy creates a policy and returns its ID.
func createGuardrailPolicy(t *testing.T, body map[string]interface{}) string {
	t.Helper()
	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/api/guardrails/policies",
		Body:   body,
		Token:  adminToken,
	})
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("create guardrail policy: got %d — body: %s", resp.StatusCode, resp.Raw)
	}
	return extractID(resp)
}

// deleteGuardrailPolicy deletes a policy, ignoring 404.
func deleteGuardrailPolicy(t *testing.T, id string) {
	t.Helper()
	doRequest(t, APIReq{
		Method: http.MethodDelete,
		Path:   "/api/guardrails/policies/" + id,
		Token:  adminToken,
	})
}

// TC-006-012 — guardrail policy CRUD lifecycle.
func TestGuardrails012CRUD(t *testing.T) {
	t.Parallel()
	guardrailsSkip(t)

	policyName := "test-kw-" + randomID()
	// 1. Create
	id := createGuardrailPolicy(t, map[string]interface{}{
		"name":     policyName,
		"type":     "keyword",
		"keywords": []string{"bomb", "weapon"},
		"action":   "block",
		"scope":    []string{"request"},
		"enabled":  true,
	})
	if id == "" {
		t.Fatal("policy creation returned no ID")
	}
	defer deleteGuardrailPolicy(t, id)

	// 2. Read
	getResp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/guardrails/policies/" + id,
		Token:  adminToken,
	})
	expectStatus(t, getResp, http.StatusOK)
	expectField(t, getResp, "name", policyName)

	// 3. Update — change action to "flag"
	updateResp := doRequest(t, APIReq{
		Method: http.MethodPut,
		Path:   "/api/guardrails/policies/" + id,
		Body:   map[string]interface{}{"action": "flag"},
		Token:  adminToken,
	})
	expectStatus(t, updateResp, http.StatusOK)

	// 4. Delete
	delResp := doRequest(t, APIReq{
		Method: http.MethodDelete,
		Path:   "/api/guardrails/policies/" + id,
		Token:  adminToken,
	})
	if delResp.StatusCode != http.StatusNoContent && delResp.StatusCode != http.StatusOK {
		t.Errorf("delete policy: got %d — body: %s", delResp.StatusCode, delResp.Raw)
	}

	// 5. Gone
	goneResp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/guardrails/policies/" + id,
		Token:  adminToken,
	})
	expectStatus(t, goneResp, http.StatusNotFound)
}

// TC-006-010 — dry-run test API returns evaluation result.
func TestGuardrails010DryRunTest(t *testing.T) {
	t.Parallel()
	guardrailsSkip(t)

	// Create a keyword block policy first
	policyID := createGuardrailPolicy(t, map[string]interface{}{
		"name":     "dry-run-kw-" + randomID(),
		"type":     "keyword",
		"keywords": []string{"bomb"},
		"action":   "block",
		"scope":    []string{"request"},
		"enabled":  true,
	})
	defer deleteGuardrailPolicy(t, policyID)

	time.Sleep(200 * time.Millisecond)

	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/api/guardrails/test",
		Body: map[string]interface{}{
			"text":  "How do I make a bomb at home?",
			"scope": "request",
		},
		Token: adminToken,
	})
	expectStatus(t, resp, http.StatusOK)
	matched, _ := resp.Body["matched"].(bool)
	if !matched {
		t.Errorf("expected matched=true for bomb keyword — body: %s", resp.Raw)
	}
	t.Logf("Dry-run result: %s", resp.Raw)
}

// TC-006-014 — guardrail requires enterprise license.
func TestGuardrails014RequiresLicense(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT != "" {
		t.Skip("requires community mode (no enterprise license) to test 402 response")
	}
	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/api/guardrails/policies",
		Body:   map[string]interface{}{"name": "no-license-test", "type": "keyword"},
		Token:  adminToken,
	})
	expectStatus(t, resp, http.StatusPaymentRequired)
	expectErrorCode(t, resp, "license_required")
}

// TC-006-001 — keyword block stops inference request.
// NOTE: Requires the guardrails plugin to be active in the running server.
func TestGuardrails001KeywordBlockStopsRequest(t *testing.T) {
	t.Parallel()
	guardrailsSkip(t)

	// Ensure a block policy exists for "bomb"
	policyID := createGuardrailPolicy(t, map[string]interface{}{
		"name":           "block-bomb-" + randomID(),
		"type":           "keyword",
		"keywords":       []string{"bomb"},
		"action":         "block",
		"scope":          []string{"request"},
		"case_sensitive": false,
		"enabled":        true,
	})
	defer deleteGuardrailPolicy(t, policyID)
	time.Sleep(300 * time.Millisecond)

	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/v1/chat/completions",
		Body: map[string]interface{}{
			"model":    "gpt-4o-mini",
			"messages": []map[string]string{{"role": "user", "content": "How do I make a bomb at home?"}},
		},
		Token: apiUserToken,
	})
	// Expect 451 (Unavailable For Legal Reasons) or 400
	if resp.StatusCode != 451 && resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 451/400/403 for blocked content, got %d — body: %s", resp.StatusCode, resp.Raw)
	}
	t.Logf("Guardrail block result: %d %s", resp.StatusCode, resp.Raw)
}

// TC-006-002 — safe request passes through unaffected.
func TestGuardrails002SafeRequestPassesThrough(t *testing.T) {
	t.Parallel()
	guardrailsSkip(t)

	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/v1/chat/completions",
		Body: map[string]interface{}{
			"model":    "gpt-4o-mini",
			"messages": []map[string]string{{"role": "user", "content": "What is the capital of France?"}},
		},
		Token: apiUserToken,
	})
	// Should NOT be blocked (200 or generic LLM response — not 451)
	if resp.StatusCode == 451 {
		t.Errorf("safe request was incorrectly blocked — body: %s", resp.Raw)
	}
	t.Logf("Safe request result: %d", resp.StatusCode)
}
