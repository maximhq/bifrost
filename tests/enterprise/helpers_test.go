package enterprise_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// ─── Configuration ─────────────────────────────────────────────────────────────

var (
	// Base URL of the running Bifrost HTTP gateway.
	baseURL = envOr("BIFROST_BASE_URL", "http://localhost:8080")

	// Pre-seeded session tokens. In CI these are short-lived JWTs created by
	// the test fixture seeder; locally they can be long-lived dev tokens.
	superAdminToken = envOr("SUPER_ADMIN_TOKEN", "dev-super-admin")
	adminToken      = envOr("ADMIN_TOKEN", "dev-admin")
	operatorToken   = envOr("OPERATOR_TOKEN", "dev-operator")
	viewerToken     = envOr("VIEWER_TOKEN", "dev-viewer")
	apiUserToken    = envOr("API_USER_TOKEN", "dev-api-user")

	// Enterprise license JWT — set to empty string to test community mode.
	enterpriseLicenseJWT = envOr("ENTERPRISE_LICENSE_JWT", "")
	proLicenseJWT        = envOr("PRO_LICENSE_JWT", "")
	expiredLicenseJWT    = envOr("EXPIRED_LICENSE_JWT", "")
	tamperedLicenseJWT   = envOr("TAMPERED_LICENSE_JWT", "")
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ─── HTTP client helpers ──────────────────────────────────────────────────────

type APIReq struct {
	Method  string
	Path    string
	Body    interface{}
	Token   string
	Headers map[string]string
}

type APIResp struct {
	StatusCode int
	Body       map[string]interface{}
	Raw        []byte
}

func doRequest(t *testing.T, req APIReq) *APIResp {
	t.Helper()
	var bodyReader io.Reader
	if req.Body != nil {
		b, err := json.Marshal(req.Body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	httpReq, err := http.NewRequest(req.Method, baseURL+req.Path, bodyReader)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if req.Body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if req.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+req.Token)
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		t.Fatalf("execute request %s %s: %v", req.Method, req.Path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]interface{}
	_ = json.Unmarshal(raw, &body)
	return &APIResp{StatusCode: resp.StatusCode, Body: body, Raw: raw}
}

// expectStatus asserts the response status code equals want.
func expectStatus(t *testing.T, resp *APIResp, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Errorf("expected HTTP %d, got %d — body: %s", want, resp.StatusCode, resp.Raw)
	}
}

// expectField asserts that resp.Body[key] deep-contains value.
func expectField(t *testing.T, resp *APIResp, key string, value interface{}) {
	t.Helper()
	got, ok := resp.Body[key]
	if !ok {
		t.Errorf("expected field %q in response, not present — body: %s", key, resp.Raw)
		return
	}
	gotStr := fmt.Sprintf("%v", got)
	wantStr := fmt.Sprintf("%v", value)
	if !strings.Contains(gotStr, wantStr) {
		t.Errorf("field %q: expected to contain %q, got %q", key, wantStr, gotStr)
	}
}

// expectErrorCode asserts resp.Body["error"]["code"] == code.
func expectErrorCode(t *testing.T, resp *APIResp, code string) {
	t.Helper()
	errObj, ok := resp.Body["error"].(map[string]interface{})
	if !ok {
		t.Errorf("expected error object in response — body: %s", resp.Raw)
		return
	}
	got, _ := errObj["code"].(string)
	if got != code {
		t.Errorf("expected error.code=%q, got %q — body: %s", code, got, resp.Raw)
	}
}

// ─── Resource helpers ─────────────────────────────────────────────────────────

func randomID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Intn(10000))
}

// cleanupProvider deletes a provider by name, ignoring 404.
func cleanupProvider(t *testing.T, name string) {
	t.Helper()
	resp := doRequest(t, APIReq{
		Method: http.MethodDelete,
		Path:   "/api/providers/" + name,
		Token:  adminToken,
	})
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		t.Logf("[CLEANUP] DELETE /api/providers/%s returned %d", name, resp.StatusCode)
	}
}

// cleanupVirtualKey deletes a VK by ID, ignoring 404.
func cleanupVirtualKey(t *testing.T, id string) {
	t.Helper()
	resp := doRequest(t, APIReq{
		Method: http.MethodDelete,
		Path:   "/api/governance/virtual-keys/" + id,
		Token:  adminToken,
	})
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		t.Logf("[CLEANUP] DELETE virtual-key %s returned %d", id, resp.StatusCode)
	}
}

// extractID extracts the first-found "id" from common response shapes.
func extractID(resp *APIResp) string {
	for _, key := range []string{"id", "virtual_key_id", "role_id"} {
		if v, ok := resp.Body[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// getNestedString navigates dot-separated path in a map.
func getNestedString(m map[string]interface{}, path string) string {
	parts := strings.SplitN(path, ".", 2)
	v := m[parts[0]]
	if len(parts) == 1 {
		s, _ := v.(string)
		return s
	}
	sub, ok := v.(map[string]interface{})
	if !ok {
		return ""
	}
	return getNestedString(sub, parts[1])
}
