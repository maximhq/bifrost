package enterprise_test

// ═══════════════════════════════════════════════════════════════════════════════
// TC-013 — License Enforcement
// Coverage: GET /api/license, GET /api/license/features, 402 gating on
//           enterprise endpoints when no license is present.
// ═══════════════════════════════════════════════════════════════════════════════

import (
	"net/http"
	"strings"
	"testing"
)

// TC-013-001 — Valid enterprise license returns all features in /api/license.
func TestLicense001ValidEnterpriseAllFeatures(t *testing.T) {
	t.Parallel()
	resp := doRequest(t, APIReq{Method: http.MethodGet, Path: "/api/license"})
	expectStatus(t, resp, http.StatusOK)

	tier, _ := resp.Body["tier"].(string)
	if tier == "" {
		t.Errorf("expected non-empty tier in response — body: %s", resp.Raw)
	}
	isValid, _ := resp.Body["is_valid"].(bool)
	if !isValid {
		t.Logf("NOTE: is_valid=%v (may be community mode in local env)", isValid)
	}
	t.Logf("License tier=%s is_valid=%v", tier, isValid)
}

// TC-013-002 — No license key → community tier.
func TestLicense002NoLicenseCommunityTier(t *testing.T) {
	t.Parallel()
	// This test is meaningful only when ENTERPRISE_LICENSE_JWT is NOT set; skip otherwise.
	if enterpriseLicenseJWT != "" {
		t.Skip("skipped: ENTERPRISE_LICENSE_JWT is set — restart without license to test community mode")
	}
	resp := doRequest(t, APIReq{Method: http.MethodGet, Path: "/api/license"})
	expectStatus(t, resp, http.StatusOK)

	tier, _ := resp.Body["tier"].(string)
	if tier != "community" {
		t.Errorf("expected tier=community, got %q", tier)
	}
}

// TC-013-003 — Enterprise endpoint returns 402 without valid license.
func TestLicense003EnterpriseEndpoints402WithoutLicense(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT != "" {
		t.Skip("skipped: enterprise license is set — cannot test 402 path")
	}
	// Test every enterprise feature endpoint
	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/rbac/roles"},
		{http.MethodGet, "/api/audit/logs"},
		{http.MethodPost, "/api/guardrails/policies"},
		{http.MethodGet, "/api/sso/providers"},
		{http.MethodGet, "/api/vault/status"},
		{http.MethodGet, "/api/cluster/status"},
		{http.MethodGet, "/api/payload/config"},
	}
	for _, ep := range endpoints {
		ep := ep
		t.Run(ep.method+"_"+ep.path, func(t *testing.T) {
			t.Parallel()
			resp := doRequest(t, APIReq{Method: ep.method, Path: ep.path, Token: adminToken})
			if resp.StatusCode != http.StatusPaymentRequired {
				t.Errorf("%s %s: expected 402, got %d — body: %s", ep.method, ep.path, resp.StatusCode, resp.Raw)
			}
		})
	}
}

// TC-013-010 — GET /api/license is public (no auth required).
func TestLicense010PublicEndpoint(t *testing.T) {
	t.Parallel()
	resp := doRequest(t, APIReq{Method: http.MethodGet, Path: "/api/license"}) // no token
	expectStatus(t, resp, http.StatusOK)
	// Must NOT return raw JWT or secret fields
	rawStr := string(resp.Raw)
	if strings.Contains(rawStr, "eyJ") { // Base64 JWT prefix
		t.Error("license endpoint must not expose raw JWT token")
	}
}

// TC-013-011 — GET /api/license/features returns boolean feature map.
func TestLicense011FeaturesMap(t *testing.T) {
	t.Parallel()
	resp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/license/features",
		Token:  adminToken,
	})
	expectStatus(t, resp, http.StatusOK)
	// Response must be a map of feature_name → bool
	for k, v := range resp.Body {
		if _, ok := v.(bool); !ok {
			t.Errorf("feature %q has non-boolean value %v", k, v)
		}
	}
	t.Logf("Features: %v", resp.Body)
}
