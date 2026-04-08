package enterprise_test

// ═══════════════════════════════════════════════════════════════════════════════
// TC-012 — HashiCorp Vault Integration
// Coverage: Status API, connectivity test, config save, license gating.
// Note: Full Vault integration tests require a live Vault Dev server.
// ═══════════════════════════════════════════════════════════════════════════════

import (
	"net/http"
	"testing"
)

func vaultSkip(t *testing.T) {
	t.Helper()
	if enterpriseLicenseJWT == "" {
		t.Skip("requires enterprise license with vault feature")
	}
}

// TC-012-003 — Vault status endpoint is reachable.
func TestVault003StatusEndpoint(t *testing.T) {
	t.Parallel()
	vaultSkip(t)
	resp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/vault/status",
		Token:  superAdminToken,
	})
	expectStatus(t, resp, http.StatusOK)
	// Even if Vault is not configured, endpoint must respond
	t.Logf("Vault status: %s", resp.Raw)
}

// TC-012-003b — Vault connectivity test endpoint.
func TestVault003bVaultTestEndpoint(t *testing.T) {
	t.Parallel()
	vaultSkip(t)
	resp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/vault/test",
		Token:  superAdminToken,
	})
	expectStatus(t, resp, http.StatusOK)
	t.Logf("Vault test: %s", resp.Raw)
}

// TC-012-002 — Vault configuration can be saved via API.
func TestVault002ConfigSaveReturns200(t *testing.T) {
	t.Parallel()
	vaultSkip(t)
	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/api/vault/sync",
		Token:  superAdminToken,
	})
	// Expect 200 (even if Vault is unreachable — sync is triggered)
	expectStatus(t, resp, http.StatusOK)
	t.Logf("Vault sync: %s", resp.Raw)
}

// TC-012 — Vault feature requires enterprise license.
func TestVaultRequiresLicense(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT != "" {
		t.Skip("requires community mode to test 402")
	}
	resp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/vault/status",
		Token:  superAdminToken,
	})
	expectStatus(t, resp, http.StatusPaymentRequired)
	expectErrorCode(t, resp, "license_required")
}

// ═══════════════════════════════════════════════════════════════════════════════
// TC-010 — Clustering
// Coverage: Status endpoint, node listing, license gating.
// ═══════════════════════════════════════════════════════════════════════════════

// TestCluster001Status — GET /api/cluster/status responds.
func TestCluster001Status(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT == "" {
		t.Skip("requires enterprise license with clustering feature")
	}
	resp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/cluster/status",
		Token:  adminToken,
	})
	expectStatus(t, resp, http.StatusOK)
	nodeID, _ := resp.Body["node_id"].(string)
	if nodeID == "" {
		t.Errorf("expected node_id in cluster status — body: %s", resp.Raw)
	}
	t.Logf("Cluster status: %s", resp.Raw)
}

// TestCluster002NodeList — GET /api/cluster/nodes responds.
func TestCluster002NodeList(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT == "" {
		t.Skip("requires enterprise license with clustering feature")
	}
	resp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/cluster/nodes",
		Token:  adminToken,
	})
	expectStatus(t, resp, http.StatusOK)
	t.Logf("Cluster nodes: %s", resp.Raw)
}

// TestClusterRequiresLicense — clustering endpoints return 402 without license.
func TestClusterRequiresLicense(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT != "" {
		t.Skip("requires community mode to test 402")
	}
	resp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/cluster/status",
		Token:  adminToken,
	})
	expectStatus(t, resp, http.StatusPaymentRequired)
}

// ═══════════════════════════════════════════════════════════════════════════════
// TC-010 (Large Payload) — Payload config/stats endpoints
// ═══════════════════════════════════════════════════════════════════════════════

// TestPayload001Config — GET /api/payload/config responds.
func TestPayload001Config(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT == "" {
		t.Skip("requires enterprise license with large_payload feature")
	}
	resp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/payload/config",
		Token:  adminToken,
	})
	expectStatus(t, resp, http.StatusOK)
	t.Logf("Payload config: %s", resp.Raw)
}

// TestPayload002Stats — GET /api/payload/stats responds.
func TestPayload002Stats(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT == "" {
		t.Skip("requires enterprise license with large_payload feature")
	}
	resp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/payload/stats",
		Token:  adminToken,
	})
	expectStatus(t, resp, http.StatusOK)
	t.Logf("Payload stats: %s", resp.Raw)
}
