# Test Cases — User Groups & MCP Integration

**Suite ID:** TC-UG — User Groups  
**SRS Reference:** §3.23  
**Priority:** P2  
**Type:** Integration + API  
**Dependency:** TC-004 (RBAC), TC-008 (SSO/SCIM), TC-013 (License)

---

## Preconditions
- Enterprise license with `user_groups` feature
- RBAC enabled, users table populated (SCIM-provisioned or manual)
- `super_admin_token` available

---

### TC-UG-001 — Create User Group

**Priority:** P2 | **Type:** API

**Steps:**
1. `POST /api/user-groups` with `super_admin_token`
2. Body:
```json
{
  "name": "Platform Engineering",
  "description": "All platform engineers",
  "roles": ["operator"],
  "virtual_key_ids": ["vk_dev_shared", "vk_prod_read"]
}
```

**Expected Result:**
- HTTP 201
- Group created with `id`, `name`, role assignment, VK assignments

**Status:** READY

---

### TC-UG-002 — Add User to Group

**Priority:** P2 | **Type:** API

**Steps:**
1. `POST /api/user-groups/{id}/members` with `super_admin_token`
2. Body: `{"user_id": "{user_bob_id}"}`

**Expected Result:**
- HTTP 201
- Bob now member of "Platform Engineering" group
- Bob inherits "operator" role

**Status:** READY

---

### TC-UG-003 — User Inherits VK Access from Group

**Priority:** P2 | **Type:** Integration

**Preconditions:** Bob added to group with VK `vk_dev_shared`.

**Steps:**
1. Send inference request as Bob using `vk_dev_shared`

**Expected Result:**
- HTTP 200 (Bob can use the group's assigned VK)

**Status:** READY

---

### TC-UG-004 — Remove User from Group Revokes Access

**Priority:** P2 | **Type:** Integration

**Steps:**
1. Bob is member of "Platform Engineering"
2. `DELETE /api/user-groups/{id}/members/{bob_id}` with `super_admin_token`
3. Bob attempts inference with `vk_dev_shared`

**Expected Result:**
- HTTP 401 or 403 (Bob no longer has access to group VK)

**Status:** READY

---

### TC-UG-005 — User in Multiple Groups Gets All Roles (Max Privilege)

**Priority:** P2 | **Type:** Integration

**Steps:**
1. Bob in "Platform Engineering" (role=operator)
2. Add Bob to "Security Auditors" (role=viewer)
3. Check Bob's effective permissions

**Expected Result:**
- Bob's effective role = operator (highest of {operator, viewer})
- `GET /api/rbac/permissions` for Bob includes operator permissions

**Status:** READY

---

### TC-UG-006 — Group Role Assignment is Audited

**Priority:** P2 | **Type:** Integration

**Steps:**
1. Create user group and assign roles
2. `GET /api/audit/logs?resource=user_group`

**Expected Result:**
- Audit entry exists for group creation with roles

**Status:** READY

---

### TC-UG-007 — SCIM Group Provisioning → User Group Sync

**Priority:** P2 | **Type:** Integration

**Preconditions:** SCIM `Groups` mapping configured: SCIM group `engineering` → Bifrost group `Platform Engineering`.

**Steps:**
1. Provision group via SCIM: `POST /scim/v2/Groups` with `displayName: engineering`
2. Add user to SCIM group: `PATCH /scim/v2/Groups/{id}` with member

**Expected Result:**
- User added to Bifrost group "Platform Engineering"
- User gains `operator` role automatically

**Status:** READY

---

### TC-UG-008 — User Group CRUD Lifecycle

**Priority:** P2 | **Type:** API

**Steps:**
1. POST → 201
2. GET → 200
3. PUT (rename group) → 200
4. DELETE → 204 (with warning if users still in group)
5. GET → 404

**Status:** READY
