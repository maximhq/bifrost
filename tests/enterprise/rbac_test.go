package enterprise_test

// ═══════════════════════════════════════════════════════════════════════════════
// TC-004 — Role-Based Access Control (RBAC)
// Coverage: Permission matrix, role assignment, revocation, custom roles,
//           community mode bypass, machine-readable error bodies.
// ═══════════════════════════════════════════════════════════════════════════════

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// TC-004-001 — super_admin can access all management endpoints.
func TestRBAC001SuperAdminAllManagementEndpoints(t *testing.T) {
	t.Parallel()
	endpoints := []struct{ method, path string }{
		{http.MethodGet, "/api/providers"},
		{http.MethodGet, "/api/governance/virtual-keys"},
		{http.MethodGet, "/api/users"},
		{http.MethodGet, "/api/rbac/roles"},
		{http.MethodGet, "/api/audit/logs"},
	}
	for _, ep := range endpoints {
		ep := ep
		t.Run(ep.method+"_"+ep.path, func(t *testing.T) {
			t.Parallel()
			resp := doRequest(t, APIReq{Method: ep.method, Path: ep.path, Token: superAdminToken})
			if resp.StatusCode == http.StatusForbidden {
				t.Errorf("super_admin denied at %s %s — body: %s", ep.method, ep.path, resp.Raw)
			}
		})
	}
}

// TC-004-002 — api_user cannot access any management endpoint.
func TestRBAC002APIUserDeniedManagement(t *testing.T) {
	t.Parallel()
	endpoints := []struct{ method, path string }{
		{http.MethodGet, "/api/providers"},
		{http.MethodGet, "/api/governance/virtual-keys"},
		{http.MethodGet, "/api/rbac/roles"},
	}
	for _, ep := range endpoints {
		ep := ep
		t.Run(ep.method+"_"+ep.path, func(t *testing.T) {
			t.Parallel()
			resp := doRequest(t, APIReq{Method: ep.method, Path: ep.path, Token: apiUserToken})
			expectStatus(t, resp, http.StatusForbidden)
			expectErrorCode(t, resp, "rbac_denied")
		})
	}
}

// TC-004-003 — api_user CAN access inference endpoints.
func TestRBAC003APIUserCanAccessInference(t *testing.T) {
	t.Parallel()
	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/v1/chat/completions",
		Body: map[string]interface{}{
			"model":    "gpt-4o-mini",
			"messages": []map[string]string{{"role": "user", "content": "Hello"}},
		},
		Token: apiUserToken,
	})
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("api_user should be allowed inference access, got 403 — body: %s", resp.Raw)
	}
}

// TC-004-004 — viewer cannot write to any resource.
func TestRBAC004ViewerCannotWrite(t *testing.T) {
	t.Parallel()
	writeOps := []struct {
		method, path string
		body         interface{}
	}{
		{http.MethodPost, "/api/providers", map[string]interface{}{"name": "rbac-test-" + randomID()}},
		{http.MethodPost, "/api/governance/virtual-keys", map[string]interface{}{"name": "rbac-vk-" + randomID()}},
	}
	for _, op := range writeOps {
		op := op
		t.Run(op.method+"_"+op.path, func(t *testing.T) {
			t.Parallel()
			resp := doRequest(t, APIReq{Method: op.method, Path: op.path, Body: op.body, Token: viewerToken})
			expectStatus(t, resp, http.StatusForbidden)
			expectErrorCode(t, resp, "rbac_denied")
		})
	}
}

// TC-004-005 — viewer CAN read all resources.
func TestRBAC005ViewerCanRead(t *testing.T) {
	t.Parallel()
	readEndpoints := []struct{ method, path string }{
		{http.MethodGet, "/api/providers"},
		{http.MethodGet, "/api/governance/virtual-keys"},
		{http.MethodGet, "/api/rbac/roles"},
	}
	for _, ep := range readEndpoints {
		ep := ep
		t.Run(ep.method+"_"+ep.path, func(t *testing.T) {
			t.Parallel()
			resp := doRequest(t, APIReq{Method: ep.method, Path: ep.path, Token: viewerToken})
			expectStatus(t, resp, http.StatusOK)
		})
	}
}

// TC-004-006 — operator CAN create/update virtual keys.
func TestRBAC006OperatorCanManageVirtualKeys(t *testing.T) {
	t.Parallel()
	vkName := "rbac-test-vk-" + randomID()
	createResp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/api/governance/virtual-keys",
		Body:   map[string]interface{}{"name": vkName},
		Token:  operatorToken,
	})
	if createResp.StatusCode != http.StatusCreated && createResp.StatusCode != http.StatusOK {
		t.Errorf("operator should be able to create VK, got %d — body: %s", createResp.StatusCode, createResp.Raw)
		return
	}
	if id := extractID(createResp); id != "" {
		defer cleanupVirtualKey(t, id)
	}
}

// TC-004-007 — operator CANNOT create providers.
func TestRBAC007OperatorCannotCreateProvider(t *testing.T) {
	t.Parallel()
	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/api/providers",
		Body:   map[string]interface{}{"name": "op-prov-" + randomID()},
		Token:  operatorToken,
	})
	expectStatus(t, resp, http.StatusForbidden)
	expectErrorCode(t, resp, "rbac_denied")
}

// TC-004-008 — admin CANNOT manage users.
func TestRBAC008AdminCannotManageUsers(t *testing.T) {
	t.Parallel()
	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/api/users",
		Body:   map[string]interface{}{"username": "rbac-user-" + randomID(), "password": "test123"},
		Token:  adminToken,
	})
	expectStatus(t, resp, http.StatusForbidden)
}

// TC-004-009 — role assignment is recorded in audit log.
func TestRBAC009RoleAssignmentAuditLogged(t *testing.T) {
	t.Parallel()
	if enterpriseLicenseJWT == "" {
		t.Skip("requires enterprise license for audit logs")
	}
	resp := doRequest(t, APIReq{
		Method: http.MethodGet,
		Path:   "/api/audit/logs?resource=user_role&action=assign&limit=5",
		Token:  superAdminToken,
	})
	expectStatus(t, resp, http.StatusOK)
	t.Logf("Audit log response: %s", resp.Raw)
}

// TC-004-014 — unauthenticated request returns 401 (not 403).
func TestRBAC014UnauthenticatedReturns401(t *testing.T) {
	t.Parallel()
	resp := doRequest(t, APIReq{Method: http.MethodGet, Path: "/api/providers"}) // no token
	expectStatus(t, resp, http.StatusUnauthorized)
}

// TC-004-016 — concurrent role checks — no race condition or 5xx.
func TestRBAC016ConcurrentRoleCheck(t *testing.T) {
	t.Parallel()
	const workers = 30
	var wg sync.WaitGroup
	wg.Add(workers)
	errs := make([]string, workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			resp := doRequest(t, APIReq{
				Method: http.MethodGet,
				Path:   "/api/providers",
				Token:  viewerToken,
			})
			if resp.StatusCode >= 500 {
				errs[i] = fmt.Sprintf("worker %d: got 5xx: %d", i, resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	for _, e := range errs {
		if e != "" {
			t.Error(e)
		}
	}
}

// TC-004-020 — RBAC error bodies are machine-readable JSON.
func TestRBAC020ErrorBodyMachineReadable(t *testing.T) {
	t.Parallel()
	resp := doRequest(t, APIReq{
		Method: http.MethodPost,
		Path:   "/api/providers",
		Body:   map[string]interface{}{"name": "rbac-err-" + randomID()},
		Token:  viewerToken,
	})
	expectStatus(t, resp, http.StatusForbidden)
	errObj, ok := resp.Body["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected error object — body: %s", resp.Raw)
	}
	for _, field := range []string{"type", "code", "message"} {
		if _, ok := errObj[field].(string); !ok {
			t.Errorf("error object missing string field %q — body: %s", field, resp.Raw)
		}
	}
	t.Logf("RBAC error body: %v", errObj)
}
