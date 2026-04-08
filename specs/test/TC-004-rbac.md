# Test Cases — Role-Based Access Control (RBAC)

**Suite ID:** TC-004  
**SRS Reference:** §3.12 (RBAC-01 → RBAC-10)  
**TR Reference:** TR-F-004  
**Priority:** P0  
**Type:** Integration + API + E2E  
**Dependency:** TC-013 (License must be valid enterprise license)

---

## Preconditions for All Tests
- Enterprise license loaded (`BIFROST_LICENSE_KEY` = valid enterprise JWT)
- 5 test sessions seeded: super_admin, admin, operator, viewer, api_user tokens
- All RBAC tables seeded with default roles

---

## Role × Permission Matrix (Test Reference)

| Endpoint | super_admin | admin | operator | viewer | api_user |
|----------|------------|-------|----------|--------|----------|
| GET /api/providers | ✅ | ✅ | ✅ | ✅ | ❌ |
| POST /api/providers | ✅ | ✅ | ❌ | ❌ | ❌ |
| DELETE /api/providers/{id} | ✅ | ✅ | ❌ | ❌ | ❌ |
| GET /api/governance/virtual-keys | ✅ | ✅ | ✅ | ✅ | ❌ |
| POST /api/governance/virtual-keys | ✅ | ✅ | ✅ | ❌ | ❌ |
| GET /api/users | ✅ | ✅ | ❌ | ❌ | ❌ |
| POST /api/users | ✅ | ❌ | ❌ | ❌ | ❌ |
| POST /api/users/{id}/roles | ✅ | ❌ | ❌ | ❌ | ❌ |
| GET /api/rbac/roles | ✅ | ✅ | ✅ | ✅ | ❌ |
| POST /api/rbac/roles | ✅ | ❌ | ❌ | ❌ | ❌ |
| GET /api/audit/logs | ✅ | ✅ | ✅ | ✅ | ❌ |
| GET /api/audit/export | ✅ | ✅ | ❌ | ❌ | ❌ |
| POST /v1/chat/completions | ✅ | ✅ | ✅ | ✅ | ✅ |

---

### TC-004-001 — super_admin Can Access All Management Endpoints

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-004.2

**Steps:**
1. For each management endpoint in the matrix above
2. Send request with `super_admin_token`

**Expected Result:**
- All requests return 200 or 201 (not 403)
- No `rbac_denied` error in any response

**Status:** READY

---

### TC-004-002 — api_user Cannot Access Any Management Endpoint

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-004.6

**Steps:**
1. `GET /api/providers` with `api_user_token`
2. `GET /api/governance/virtual-keys` with `api_user_token`
3. `GET /api/rbac/roles` with `api_user_token`

**Expected Result:**
- All return HTTP 403
- Body: `{"error":{"code":"rbac_denied","message":"..."}}`

**Status:** READY

---

### TC-004-003 — api_user CAN Access Inference Endpoints

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-004.6

**Steps:**
1. `POST /v1/chat/completions` with `api_user_token`
2. `POST /v1/embeddings` with `api_user_token`

**Expected Result:**
- Both return HTTP 200 (inference is allowed for api_user)

**Status:** READY

---

### TC-004-004 — viewer Cannot Write to Any Resource

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-004.5

**Steps:**
1. `POST /api/providers` with `viewer_token` (write operation)
2. `POST /api/governance/virtual-keys` with `viewer_token`
3. `PUT /api/config` with `viewer_token`

**Expected Result:**
- All return HTTP 403 with `rbac_denied`

**Status:** READY

---

### TC-004-005 — viewer CAN Read All Resources

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-004.5

**Steps:**
1. `GET /api/providers` with `viewer_token`
2. `GET /api/governance/virtual-keys` with `viewer_token`
3. `GET /api/rbac/roles` with `viewer_token`

**Expected Result:**
- All return HTTP 200 with resource lists

**Status:** READY

---

### TC-004-006 — operator CAN Create/Update Virtual Keys

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-004.4

**Steps:**
1. `POST /api/governance/virtual-keys` with `operator_token`
2. `PUT /api/governance/virtual-keys/{id}` with `operator_token`

**Expected Result:**
- HTTP 201 and 200 respectively

**Status:** READY

---

### TC-004-007 — operator CANNOT Create Providers

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-004.4

**Steps:**
1. `POST /api/providers` with `operator_token`

**Expected Result:**
- HTTP 403 with `rbac_denied`

**Status:** READY

---

### TC-004-008 — admin CANNOT Manage Users

**Priority:** P0 | **Type:** API  
**TR Reference:** TR-F-004.3

**Steps:**
1. `POST /api/users` with `admin_token`
2. `POST /api/users/{id}/roles` with `admin_token`

**Expected Result:**
- Both return HTTP 403

**Status:** READY

---

### TC-004-009 — Role Assignment Recorded in Audit Log

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-004.8

**Steps:**
1. `POST /api/users/{id}/roles` with `super_admin_token` — assign "operator" role to user
2. `GET /api/audit/logs?resource=user_role&action=assign`

**Expected Result:**
- Audit log entry exists with:
  - `action` = "assign"
  - `resource` = "user_role"
  - `actor_id` = super_admin user ID
  - `new_value` contains the assigned role

**Status:** READY

---

### TC-004-010 — RBAC Bypassed When No Enterprise License

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-004.9

**Preconditions:** Restart Bifrost with `BIFROST_LICENSE_KEY` unset.

**Steps:**
1. `GET /api/providers` with any valid session token
2. `POST /api/providers` with any valid session token

**Expected Result:**
- Both succeed (HTTP 200 / 201)
- RBAC middleware passes through (community mode, no role enforcement)

**Status:** READY

---

### TC-004-011 — super_admin Creates Custom Role

**Priority:** P1 | **Type:** API  
**TR Reference:** TR-F-004.10

**Steps:**
1. `POST /api/rbac/roles` with `super_admin_token`
2. Body: `{"name":"custom_reviewer","permissions":[{"resource":"logs","action":"read"},{"resource":"providers","action":"read"}]}`
3. `GET /api/rbac/roles` — verify role appears
4. Assign role to test user via `POST /api/users/{id}/roles`
5. Verify user with `custom_reviewer` role can GET providers but not POST

**Expected Result:**
- Role created (201)
- Permission matrix correctly applied

**Status:** READY

---

### TC-004-012 — Attempt to Delete System Role Rejected

**Priority:** P1 | **Type:** API

**Steps:**
1. `DELETE /api/rbac/roles/role_viewer` with `super_admin_token` (system role)

**Expected Result:**
- HTTP 409 or 403
- Error message: "system roles cannot be deleted"

**Status:** READY

---

### TC-004-013 — Expired Role Assignment (Time-Bounded Role)

**Priority:** P1 | **Type:** Integration

**Steps:**
1. Assign "operator" role to user with `expires_at` = 1 second in the future
2. Wait 2 seconds
3. Make request with that user's token to operator-only endpoint

**Expected Result:**
- After expiry: HTTP 403 (role no longer valid)

**Status:** READY

---

### TC-004-014 — Unauthenticated Request Returns 401 (Not 403)

**Priority:** P0 | **Type:** API

**Steps:**
1. `GET /api/providers` with no Authorization header

**Expected Result:**
- HTTP 401 (not 403)
- Error type: `authentication_error` (not `rbac_denied`)

**Status:** READY

---

### TC-004-015 — GET /api/rbac/permissions Returns Caller's Effective Permissions

**Priority:** P1 | **Type:** API

**Steps:**
1. `GET /api/rbac/permissions` with `operator_token`

**Expected Result:**
- HTTP 200
- Response lists operator's allowed resource+action combinations
- Does NOT include `{"resource":"users","action":"admin"}`

**Status:** READY

---

### TC-004-016 — Concurrent Role Check (Race Condition)

**Priority:** P1 | **Type:** Integration

**Steps:**
1. 100 concurrent goroutines each calling `GET /api/providers` with `viewer_token`
2. Simultaneously, update viewer's role to "admin" mid-flight

**Expected Result:**
- No panics or 500 errors
- Responses consistently 200 (read allowed for both reader and admin)

**Status:** READY

---

### TC-004-017 — Role Revocation Takes Effect Immediately

**Priority:** P0 | **Type:** Integration

**Steps:**
1. User has "operator" role — can POST virtual keys
2. `DELETE /api/users/{id}/roles/role_operator` with `super_admin_token`
3. Immediately send `POST /api/governance/virtual-keys` with the demoted user's token

**Expected Result:**
- Request after revocation returns HTTP 403

**Status:** READY

---

### TC-004-018 — RBAC UI — Role Matrix Displays Correctly (E2E)

**Priority:** P1 | **Type:** E2E

**Steps:**
1. Login as super_admin via UI
2. Navigate to Enterprise → RBAC → Roles
3. Verify 5 default roles are displayed with correct permissions

**Expected Result:**
- Role list shows: super_admin, admin, operator, viewer, api_user
- Permission matrix shows correct allow/deny for each resource

**Status:** READY

---

### TC-004-019 — RBAC UI — Viewer Cannot See Write Actions (E2E)

**Priority:** P1 | **Type:** E2E

**Steps:**
1. Login as "viewer" via UI
2. Navigate to Provider management page

**Expected Result:**
- "Add Provider" button is hidden or disabled
- Edit/Delete buttons are hidden or disabled

**Status:** READY

---

### TC-004-020 — RBAC Error Codes Are Machine-Readable

**Priority:** P0 | **Type:** API

**Steps:**
1. Trigger RBAC denial (viewer POSTing to providers)
2. Inspect response body

**Expected Result:**
```json
{
  "error": {
    "type": "authorization_error",
    "code": "rbac_denied",
    "message": "Role 'viewer' does not have permission 'write' on resource 'providers'",
    "required_role": "admin"
  }
}
```

**Status:** READY
