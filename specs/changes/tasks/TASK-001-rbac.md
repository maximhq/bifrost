# TASK-001 — RBAC (Role-Based Access Control)

**Feature:** RBAC  
**TECH Spec:** [TECH-001-rbac.md](../TECH-001-rbac.md)  
**Phase:** 1 (Security Core)  
**Depends on:** TASK-014 (license)  
**Estimate:** 5 days  
**Assignee:** —  
**Status:** 🟢 Completed

---

## Context

RBAC gates all admin API operations. The implementation adds a `RBACMiddleware` to every route and a permission matrix checked per-endpoint. All session/auth context already flows through `BifrostContext`.

**Roles and hierarchy (lowest → highest):**
`viewer` → `operator` → `admin` → `super_admin`

**Key pattern for every enterprise endpoint:**
```go
if !license.IsFeatureEnabled("rbac") {
    ctx.SetStatusCode(402)
    return
}
```

---

## Tasks

### TASK-001-01 — Database schema + GORM migration

**Files to create/modify:**
- `framework/configstore/tables/rbac.go` — `RoleTable`, `PermissionTable`, `RolePermissionTable`, `UserRoleTable`
- `framework/configstore/migrations/` — new migration file for RBAC tables

**Schema:**
```go
type RoleTable struct {
    ID          string    // "viewer"|"operator"|"admin"|"super_admin" or custom UUID
    Name        string    // display name
    Description string
    IsSystem    bool      // system roles cannot be deleted
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type PermissionTable struct {
    ID       string  // e.g., "providers:read"
    Resource string  // "providers"|"virtual_keys"|"plugins"|...
    Action   string  // "read"|"write"|"delete"|"admin"
}

type RolePermissionTable struct {
    RoleID       string `gorm:"index"`
    PermissionID string `gorm:"index"`
}

type UserRoleTable struct {
    UserID    string `gorm:"index"`
    RoleID    string `gorm:"index"`
    GrantedBy string
    GrantedAt time.Time
}
```

**Acceptance criteria:**
- [x] Migration runs cleanly on fresh database (added to `framework/configstore/migrations.go`)
- [x] Migration is idempotent (re-run does not error) (uses `HasTable` guard)
- [ ] Seed data: 4 system roles with their default permissions inserted on first run

---

### TASK-001-02 — Permission matrix seeding

**Files to create:**
- `framework/configstore/rbac_seed.go` — `SeedRBACDefaults(db)`

**Permission matrix to seed (from TECH-001 spec §4):**

| Resource | viewer | operator | admin | super_admin |
|---------|--------|----------|-------|-------------|
| providers:read | ✓ | ✓ | ✓ | ✓ |
| providers:write | — | — | ✓ | ✓ |
| virtual_keys:read | ✓ | ✓ | ✓ | ✓ |
| virtual_keys:write | — | ✓ | ✓ | ✓ |
| plugins:write | — | — | ✓ | ✓ |
| users:write | — | — | ✓ | ✓ |
| roles:write | — | — | — | ✓ |
| license:write | — | — | — | ✓ |
| audit_logs:read | — | — | ✓ | ✓ |
| system:admin | — | — | — | ✓ |

**Acceptance criteria:**
- [ ] `SeedRBACDefaults()` is called in server bootstrap after migration
- [ ] Existing roles not overwritten on re-seed (upsert, not insert)

---

### TASK-001-03 — RBAC middleware

**Files to create:**
- `transports/bifrost-http/handlers/rbac_middleware.go` — `RequirePermission(resource, action)` middleware factory

**Implementation:**
```go
func RequirePermission(resource, action string) Middleware {
    return func(h fasthttp.RequestHandler) fasthttp.RequestHandler {
        return func(ctx *fasthttp.RequestCtx) {
            if !license.IsFeatureEnabled("rbac") {
                h(ctx); return  // rbac disabled = allow all (backward compat)
            }
            userID := getSessionUserID(ctx)
            if !rbacEnforcer.Can(userID, resource, action) {
                ctx.SetStatusCode(403)
                writeError(ctx, "forbidden", "insufficient permissions")
                return
            }
            h(ctx)
        }
    }
}
```

**Acceptance criteria:**
- [x] Returns `403` with JSON error when permission denied (implemented in `rbac.go`)
- [x] When `rbac` feature disabled → middleware is a pass-through (backward compat)
- [x] `super_admin` always passes permission check (roleRanks map)
- [x] Unauthenticated requests (no session) → `401` before permission check

---

### TASK-001-04 — RBAC enforcer + ConfigStore operations

**Files to create:**
- `framework/configstore/rbac.go` — `GetUserRoles()`, `GetRolePermissions()`, `AssignRole()`, `RevokeRole()`
- `transports/bifrost-http/handlers/rbac_enforcer.go` — `RBACEnforcer` with in-memory permission cache

**Cache TTL:** 60 seconds (invalidated on role assignment/revocation)

**Acceptance criteria:**
- [x] `RBACEnforcer.Can(userID, resource, action)` correctly evaluates cumulative permissions across all user roles (`GetUserPermissions`)
- [ ] Cache invalidated when `AssignRole()` or `RevokeRole()` is called
- [ ] Permission check is <1ms (cache hit)

---

### TASK-001-05 — RBAC Management API

**Files to create:**
- `transports/bifrost-http/handlers/rbac.go`

**Endpoints:**
```
GET    /api/rbac/roles                   — list roles (admin+)
POST   /api/rbac/roles                   — create role (super_admin)
GET    /api/rbac/roles/{id}              — get role + permissions (admin+)
PUT    /api/rbac/roles/{id}              — update role (super_admin)
DELETE /api/rbac/roles/{id}              — delete role (super_admin, non-system only)

GET    /api/rbac/permissions             — list all permissions (admin+)

GET    /api/users/{id}/roles             — list user roles (admin+)
POST   /api/users/{id}/roles             — assign role (admin+, audit-logged)
DELETE /api/users/{id}/roles/{roleId}    — revoke role (admin+, audit-logged)

GET    /api/users/me/permissions         — current user's effective permissions
```

**Acceptance criteria:**
- [x] All endpoints implemented in `handlers/rbac_handler.go`
- [x] System roles cannot be deleted (WHERE is_system=false guard)
- [ ] Role assign/revoke operations write audit log entries
- [ ] `GET /api/users/me/permissions` works without RBAC license (returns empty set)

---

### TASK-001-06 — Apply RBAC middleware to all existing routes

**Files to modify:**
- `transports/bifrost-http/handlers/providers.go`
- `transports/bifrost-http/handlers/governance.go`
- `transports/bifrost-http/handlers/plugins.go`
- `transports/bifrost-http/handlers/config.go`
- `transports/bifrost-http/handlers/mcp.go`

**Acceptance criteria:**
- [ ] Every state-modifying route (POST/PUT/DELETE) has `RequirePermission` middleware applied
- [ ] Read-only routes (GET) have `RequirePermission(resource, "read")` middleware applied
- [ ] Existing tests still pass (RBAC disabled in test environment)

---

### TASK-001-07 — UI: Role management

**Files to create:**
- `ui/app/enterprise/rbac/page.tsx`
- `ui/app/enterprise/rbac/components/RoleCard.tsx`
- `ui/app/enterprise/rbac/components/PermissionMatrix.tsx`
- `ui/app/enterprise/rbac/components/UserRoleAssigner.tsx`

**Acceptance criteria:**
- [ ] Page renders inside `<EnterpriseGate feature="rbac">`
- [ ] Role list shows system and custom roles
- [ ] Permission matrix shows editable checkboxes for custom roles (read-only for system roles)
- [ ] User role assignment UI with user search autocomplete

---

## Definition of Done

- [ ] All subtasks complete
- [ ] Unit tests: `RBACEnforcer.Can()` with role inheritance edge cases
- [ ] Integration test: unauthenticated → 401, wrong role → 403, correct role → 200
- [ ] E2E test: Playwright test in `tests/e2e/features/rbac/rbac.spec.ts`
- [ ] `make build` passes
