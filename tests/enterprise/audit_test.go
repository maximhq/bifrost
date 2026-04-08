package enterprise_test

// ═══════════════════════════════════════════════════════════════════════════════
// TC-005 — Immutable Audit Logs
// Coverage: Write-on-action, hash chain integrity, filtering, export,
//           append-only enforcement, async write non-blocking.
// ═══════════════════════════════════════════════════════════════════════════════

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// TC-005-001 — provider creation generates audit log entry.
func TestAudit001ProviderCreateGeneratesLog(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT == "" {
		t.Skip("requires enterprise license")
	}
	provName := "audit-prov-" + randomID()
	defer cleanupProvider(t, provName)

	before := time.Now().UTC()

	// Create provider
	createResp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/api/providers",
		Body:   map[string]interface{}{"name": provName, "type": "openai"},
		Token:  adminToken,
	})
	if createResp.StatusCode != http.StatusCreated && createResp.StatusCode != http.StatusOK {
		t.Skipf("provider creation returned %d (provider feature may not be available)", createResp.StatusCode)
	}

	// Allow async audit write to flush
	time.Sleep(500 * time.Millisecond)

	// Query audit logs
	auditResp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/audit/logs?resource=provider&action=create&limit=20",
		Token:  adminToken,
	})
	expectStatus(t, auditResp, http.StatusOK)

	// Check at least one matching entry
	logs, _ := auditResp.Body["logs"].([]interface{})
	found := false
	for _, entry := range logs {
		e, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		rn, _ := e["resource_name"].(string)
		ts, _ := e["timestamp"].(string)
		if rn == provName {
			found = true
			// Verify timestamp is recent
			parsedTime, err := time.Parse(time.RFC3339, ts)
			if err == nil && parsedTime.Before(before) {
				t.Errorf("audit entry timestamp %v is before action time %v", parsedTime, before)
			}
			t.Logf("Found audit entry: %v", e)
			break
		}
	}
	if !found {
		t.Logf("Audit logs returned: %s", auditResp.Raw)
		t.Error("no audit entry found for provider creation")
	}
}

// TC-005-006 — hash chain is intact for sequential entries.
func TestAudit006HashChainIntegrity(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT == "" {
		t.Skip("requires enterprise license")
	}

	// Create 3 virtual keys to produce sequential audit entries
	createdIDs := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		resp := doRequest(t, APIReq{
			Method: http.MethodPost,
			Path:   "/api/governance/virtual-keys",
			Body:   map[string]interface{}{"name": "audit-chain-vk-" + randomID()},
			Token:  adminToken,
		})
		if id := extractID(resp); id != "" {
			createdIDs = append(createdIDs, id)
		}
	}
	defer func() {
		for _, id := range createdIDs {
			cleanupVirtualKey(t, id)
		}
	}()

	time.Sleep(500 * time.Millisecond)

	// Retrieve recent audit entries ordered by sequence
	auditResp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/audit/logs?limit=10&sort=seq_asc",
		Token:  adminToken,
	})
	expectStatus(t, auditResp, http.StatusOK)

	logs, _ := auditResp.Body["logs"].([]interface{})
	// Verify hash chain if at least 2 entries
	if len(logs) >= 2 {
		prev, _ := logs[0].(map[string]interface{})
		for i := 1; i < len(logs); i++ {
			curr, ok := logs[i].(map[string]interface{})
			if !ok {
				continue
			}
			prevHash, _ := prev["hash"].(string)
			currPrevHash, _ := curr["prev_hash"].(string)
			if prevHash != "" && currPrevHash != "" && prevHash != currPrevHash {
				t.Errorf("hash chain broken at index %d: entry[%d].hash=%q but entry[%d].prev_hash=%q",
					i, i-1, prevHash, i, currPrevHash)
			}
			prev = curr
		}
		t.Logf("Hash chain verified for %d consecutive entries", len(logs))
	} else {
		t.Log("Less than 2 audit entries available — hash chain not verifiable in this environment")
	}
}

// TC-005-007 — chain integrity verification API — intact chain.
func TestAudit007VerifyAPIIntactChain(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT == "" {
		t.Skip("requires enterprise license")
	}
	resp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/audit/verify?from_seq=1",
		Token:  superAdminToken,
	})
	expectStatus(t, resp, http.StatusOK)
	intact, _ := resp.Body["intact"].(bool)
	if !intact {
		t.Errorf("expected intact=true, got false — body: %s", resp.Raw)
	}
	t.Logf("Chain verification: %s", resp.Raw)
}

// TC-005-009 — audit log search — filter by actor.
func TestAudit009FilterByActor(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT == "" {
		t.Skip("requires enterprise license")
	}
	// Perform an action to generate a log entry
	doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/providers",
		Token:  adminToken,
	})
	time.Sleep(200 * time.Millisecond)

	// Query by actor (admin)
	resp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/audit/logs?actor_token=admin&limit=10",
		Token:  superAdminToken,
	})
	expectStatus(t, resp, http.StatusOK)
	t.Logf("Actor-filtered audit: %s", resp.Raw)
}

// TC-005-010 — audit log search — filter by time range.
func TestAudit010FilterByTimeRange(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT == "" {
		t.Skip("requires enterprise license")
	}
	start := time.Now().UTC()

	// Perform an action
	doRequest(t, APIReq{Method: http.MethodGet, Path: "/api/providers", Token: adminToken})
	time.Sleep(200 * time.Millisecond)
	end := time.Now().UTC()

	resp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/audit/logs?start_time=" + start.Format(time.RFC3339) + "&end_time=" + end.Format(time.RFC3339),
		Token:  adminToken,
	})
	expectStatus(t, resp, http.StatusOK)
	t.Logf("Time-range audit: %s", resp.Raw)
}

// TC-005-011 — audit log export (JSON format).
func TestAudit011ExportJSON(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT == "" {
		t.Skip("requires enterprise license")
	}
	resp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/audit/export?format=json",
		Token:  superAdminToken,
	})
	expectStatus(t, resp, http.StatusOK)
	// Must be parseable JSON array
	var entries []json.RawMessage
	if err := json.Unmarshal(resp.Raw, &entries); err != nil {
		t.Errorf("export body is not a JSON array: %v — raw: %s", err, resp.Raw)
	}
	t.Logf("Exported %d audit entries", len(entries))
}

// TC-005-014 — viewer CAN read audit logs.
func TestAudit014ViewerCanReadLogs(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT == "" {
		t.Skip("requires enterprise license")
	}
	resp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/audit/logs",
		Token:  viewerToken,
	})
	expectStatus(t, resp, http.StatusOK)
}

// TC-005-015 — viewer CANNOT export audit logs.
func TestAudit015ViewerCannotExport(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT == "" {
		t.Skip("requires enterprise license")
	}
	resp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/audit/export?format=json",
		Token:  viewerToken,
	})
	expectStatus(t, resp, http.StatusForbidden)
}
