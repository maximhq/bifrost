# TECH-001 — Role-Based Access Control (RBAC)

**Feature ID:** RBAC  
**SRS Reference:** §3.12 (RBAC-01 → RBAC-10)  
**CR Reference:** CR-ENT-001, CR-ENT-002  
**Version:** 1.0 | **Date:** 2026-04-08  
**Status:** Design Ready

---

## 1. Overview

Implement a 5-tier RBAC system gating all management API endpoints and UI features. RBAC must integrate with the existing session/virtual-key auth stack without breaking backward compatibility.

**Roles (descending privilege):**

| Role | Code | Description |
|------|------|-------------|
| `super_admin` | `SA` | Full access — manages all system settings, licenses, users |
| `admin` | `AD` | Manages providers, governance, plugins; cannot manage users or license |
| `operator` | `OP` | Manages virtual keys, routing rules, MCP clients; read-only on providers |
| `viewer` | `VW` | Read-only across all management APIs |
| `api_user` | `AU` | Inference-only; cannot access management API |

---

## 2. Architecture Mapping

### 2.1 Layer Placement

```
transports/bifrost-http/
├── handlers/
│   └── rbac.go           (NEW) CRUD for roles, user-role assignments
├── lib/
│   ├── middleware.go      (MODIFY) Add RBACMiddleware after AuthMiddleware
│   └── rbac.go           (NEW) Permission matrix, hasPermission()

framework/configstore/
├── tables/
│   └── rbac.go           (NEW) RolesTable, UserRolesTable, PermissionsTable
└── rbac.go               (NEW) ConfigStore RBAC operations

plugins/governance/
└── rbac.go               (NEW) GovernancePlugin RBAC enforcement hook
```

### 2.2 Database Schema (GORM)

```go
// framework/configstore/tables/rbac.go

type RolesTable struct {
    ID          string    `gorm:"primaryKey;type:text"`
    Name        string    `gorm:"uniqueIndex;not null"`  // "super_admin", "admin", etc.
    DisplayName string    `gorm:"not null"`
    Description string
    IsSystem    bool      `gorm:"default:true"`   // system roles cannot be deleted
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type UserRolesTable struct {
    ID        string    `gorm:"primaryKey;type:text"`
    UserID    string    `gorm:"index;not null"`   // maps to SessionsTable.UserID
    RoleID    string    `gorm:"index;not null"`
    GrantedBy string    // user ID who granted
    GrantedAt time.Time
    ExpiresAt *time.Time // nil = permanent
}

type PermissionsTable struct {
    ID         string `gorm:"primaryKey;type:text"`
    RoleID     string `gorm:"index;not null"`
    Resource   string `gorm:"not null"`   // e.g., "providers", "virtual-keys"
    Action     string `gorm:"not null"`   // "read", "write", "delete", "admin"
}
```

### 2.3 SessionsTable Extension

```go
// Extend existing SessionsTable to carry role
type SessionsTable struct {
    // ... existing fields ...
    UserID   string `gorm:"index"`   // NEW: links to UserRolesTable
    Username string                  // NEW: display name
    Role     string                  // NEW: denormalized for fast auth middleware
}
```

---

## 3. Permission Matrix

```go
// transports/bifrost-http/lib/rbac.go

type Permission struct {
    Resource string
    Action   string
}

var permissionMatrix = map[string][]Permission{
    "super_admin": {
        {"*", "*"}, // all resources, all actions
    },
    "admin": {
        {"providers", "read"}, {"providers", "write"}, {"providers", "delete"},
        {"virtual-keys", "read"}, {"virtual-keys", "write"}, {"virtual-keys", "delete"},
        {"teams", "read"}, {"teams", "write"}, {"teams", "delete"},
        {"customers", "read"}, {"customers", "write"}, {"customers", "delete"},
        {"routing-rules", "read"}, {"routing-rules", "write"}, {"routing-rules", "delete"},
        {"plugins", "read"}, {"plugins", "write"},
        {"logs", "read"},
        {"config", "read"}, {"config", "write"},
        {"mcp", "read"}, {"mcp", "write"},
        // Cannot: {"users", "*"}, {"license", "*"}
    },
    "operator": {
        {"virtual-keys", "read"}, {"virtual-keys", "write"},
        {"routing-rules", "read"}, {"routing-rules", "write"},
        {"mcp", "read"}, {"mcp", "write"},
        {"logs", "read"},
        {"providers", "read"}, // read-only
        {"teams", "read"}, {"customers", "read"},
    },
    "viewer": {
        {"providers", "read"},
        {"virtual-keys", "read"}, {"teams", "read"}, {"customers", "read"},
        {"routing-rules", "read"}, {"plugins", "read"},
        {"logs", "read"}, {"config", "read"}, {"mcp", "read"},
    },
    "api_user": {
        // inference only — no management API access
    },
}

func HasPermission(role, resource, action string) bool {
    perms, ok := permissionMatrix[role]
    if !ok {
        return false
    }
    for _, p := range perms {
        if (p.Resource == "*" || p.Resource == resource) &&
           (p.Action == "*" || p.Action == action) {
            return true
        }
    }
    return false
}
```

---

## 4. Middleware Implementation

```go
// transports/bifrost-http/lib/middleware.go — new RBACMiddleware

func RBACMiddleware(resource, action string) Middleware {
    return func(h fasthttp.RequestHandler) fasthttp.RequestHandler {
        return func(ctx *fasthttp.RequestCtx) {
            // Extract role from session (set by AuthMiddleware)
            role := string(ctx.UserValue("session_role").(string))
            if role == "" {
                role = "api_user"
            }

            if !HasPermission(role, resource, action) {
                ctx.SetStatusCode(fasthttp.StatusForbidden)
                ctx.SetBodyString(`{"error":{"message":"Insufficient permissions","code":"rbac_denied"}}`)
                return
            }
            h(ctx)
        }
    }
}
```

### 4.1 Route Registration with RBAC

```go
// transports/bifrost-http/server/server.go — existing RegisterAPIRoutes()

// Providers — admin+
router.GET("/api/providers", ChainMiddlewares(h, AuthMiddleware, RBACMiddleware("providers", "read")))
router.POST("/api/providers", ChainMiddlewares(h, AuthMiddleware, RBACMiddleware("providers", "write")))
router.DELETE("/api/providers/{id}", ChainMiddlewares(h, AuthMiddleware, RBACMiddleware("providers", "delete")))

// Virtual keys — operator+
router.GET("/api/governance/virtual-keys", ChainMiddlewares(h, AuthMiddleware, RBACMiddleware("virtual-keys", "read")))
router.POST("/api/governance/virtual-keys", ChainMiddlewares(h, AuthMiddleware, RBACMiddleware("virtual-keys", "write")))

// User management — super_admin only
router.GET("/api/users", ChainMiddlewares(h, AuthMiddleware, RBACMiddleware("users", "read")))
router.POST("/api/users", ChainMiddlewares(h, AuthMiddleware, RBACMiddleware("users", "admin")))
```

---

## 5. RBAC Handler (New)

```go
// transports/bifrost-http/handlers/rbac.go

type RBACHandler struct {
    configStore framework.ConfigStore
}

// GET /api/rbac/roles
func (h *RBACHandler) ListRoles(ctx *fasthttp.RequestCtx) { ... }

// POST /api/rbac/roles  (super_admin only)
func (h *RBACHandler) CreateRole(ctx *fasthttp.RequestCtx) { ... }

// GET /api/rbac/users
func (h *RBACHandler) ListUsers(ctx *fasthttp.RequestCtx) { ... }

// POST /api/users/{id}/roles  — assign role
func (h *RBACHandler) AssignRole(ctx *fasthttp.RequestCtx) { ... }

// DELETE /api/users/{id}/roles/{role_id}  — revoke role
func (h *RBACHandler) RevokeRole(ctx *fasthttp.RequestCtx) { ... }

// GET /api/rbac/permissions  — list effective permissions for caller's role
func (h *RBACHandler) GetMyPermissions(ctx *fasthttp.RequestCtx) { ... }
```

---

## 6. Feature Gating

```go
// transports/bifrost-http/lib/features.go (NEW)

func IsEnterpriseEnabled() bool {
    return os.Getenv("BIFROST_ENTERPRISE_LICENSE") != ""
}

// In RBACMiddleware:
if !IsEnterpriseEnabled() {
    // Fall through without RBAC check (backward compatible)
    h(ctx)
    return
}
```

---

## 7. Migration

```go
// framework/configstore/migrations.go — add migration step

func migrateV3RBACTables(db *gorm.DB) error {
    return db.AutoMigrate(
        &tables.RolesTable{},
        &tables.UserRolesTable{},
        &tables.PermissionsTable{},
    )
}

// Seed default roles on first run
func seedDefaultRoles(db *gorm.DB) error {
    defaults := []tables.RolesTable{
        {ID: "role_super_admin", Name: "super_admin", DisplayName: "Super Admin", IsSystem: true},
        {ID: "role_admin",       Name: "admin",       DisplayName: "Admin",       IsSystem: true},
        {ID: "role_operator",    Name: "operator",    DisplayName: "Operator",    IsSystem: true},
        {ID: "role_viewer",      Name: "viewer",      DisplayName: "Viewer",      IsSystem: true},
        {ID: "role_api_user",    Name: "api_user",    DisplayName: "API User",    IsSystem: true},
    }
    return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&defaults).Error
}
```

---

## 8. UI Components

```
ui/app/enterprise/rbac/
├── page.tsx              — Role management list
├── users/page.tsx        — User list with role assignments
└── components/
    ├── RoleMatrix.tsx    — Visual permission matrix
    ├── UserRoleForm.tsx  — Assign/revoke role form
    └── RBACGuard.tsx     — Client-side route guard using /api/rbac/permissions
```

---

## 9. API Endpoints Summary

| Method | Path | Permission Required |
|--------|------|-------------------|
| `GET` | `/api/rbac/roles` | `viewer` |
| `POST` | `/api/rbac/roles` | `super_admin` |
| `GET` | `/api/users` | `admin` |
| `POST` | `/api/users` | `super_admin` |
| `POST` | `/api/users/{id}/roles` | `super_admin` |
| `DELETE` | `/api/users/{id}/roles/{rid}` | `super_admin` |
| `GET` | `/api/rbac/permissions` | authenticated |

---

## 10. Testing Strategy

- Unit: `HasPermission()` matrix coverage for all role×resource×action combinations
- Integration: Each route returns 403 for under-privileged session tokens
- E2E: `tests/e2e/features/rbac/` — create viewer session, verify 403 on write routes
