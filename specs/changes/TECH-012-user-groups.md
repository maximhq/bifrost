# TECH-012 — User Groups

**Feature ID:** UGRP  
**SRS Reference:** §3.23 (UGRP-01 → UGRP-07)  
**CR Reference:** CR-ENT-001, CR-ENT-002  
**Version:** 1.0 | **Date:** 2026-04-08  
**Status:** Design Ready

---

## 1. Overview

Implement user groups to batch-assign virtual keys, MCP tool groups, and roles to collections of users. Groups can be defined locally in Bifrost or synced from an IdP via SCIM (TECH-005).

**Key capabilities:**
- `UGRP-01`: Create/manage named user groups
- `UGRP-02`: Assign users to groups; users can belong to multiple groups
- `UGRP-03`: Assign virtual keys to groups (all users in group share access policy)
- `UGRP-04`: Assign MCP tool groups to user groups (TECH-011 integration)
- `UGRP-05`: Assign RBAC role to group (TECH-001 integration)
- `UGRP-06`: Sync groups from SCIM/OIDC IdP (TECH-005 integration)
- `UGRP-07`: Per-group budget and rate limit overrides

---

## 2. Architecture Mapping

```
transports/bifrost-http/
├── handlers/
│   └── user_groups.go         (NEW) /api/user-groups/* CRUD

framework/configstore/
├── tables/
│   └── user_groups.go         (NEW) UserGroupTable, UserGroupMemberTable, etc.
└── user_groups.go             (NEW) ConfigStore CRUD operations

plugins/governance/
└── group_resolver.go          (NEW) Resolve effective VK + budget for user groups
```

---

## 3. Database Schema

```go
// framework/configstore/tables/user_groups.go

type UserGroupTable struct {
    ID          string    `gorm:"primaryKey;type:text"`
    Name        string    `gorm:"uniqueIndex;not null"`
    Description string
    Role        string    // RBAC role inherited by all members
    ExternalID  string    `gorm:"index"`  // IdP group ID for SCIM sync
    SyncedAt    *time.Time                // last SCIM sync timestamp
    
    // Per-group budget override
    BudgetMaxLimit      *float64
    BudgetResetDuration *int64   // seconds
    
    // Per-group rate limit override
    RateLimitRequestsPerMin *int64
    RateLimitTokensPerMin   *int64
    
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type UserGroupMemberTable struct {
    ID          string    `gorm:"primaryKey;type:text"`
    GroupID     string    `gorm:"index;not null"`
    UserID      string    `gorm:"index;not null"`  // ExternalUserTable.ID
    AddedBy     string    // user ID who added; empty = SCIM-provisioned
    AddedAt     time.Time
}

type UserGroupVirtualKeyTable struct {
    ID            string    `gorm:"primaryKey;type:text"`
    GroupID       string    `gorm:"index;not null"`
    VirtualKeyID  string    `gorm:"index;not null"`
    // Groups can override VK-level budget for their members
    BudgetOverride *float64
}

type UserGroupMCPGroupTable struct {
    ID          string    `gorm:"primaryKey;type:text"`
    GroupID     string    `gorm:"index;not null"`
    MCPGroupID  string    `gorm:"index;not null"`  // MCPToolGroupTable.ID
}
```

---

## 4. Effective Permission Resolution

When a user makes a request, their effective permissions come from multiple sources:

```go
// plugins/governance/group_resolver.go

type EffectivePermissions struct {
    Role             string
    VirtualKeyIDs    []string
    MCPGroupIDs      []string
    BudgetLimit      *float64   // most restrictive wins
    RateLimitReqMin  *int64
    RateLimitTokMin  *int64
}

type GroupResolver struct {
    configStore framework.ConfigStore
    cache       sync.Map  // userID → *EffectivePermissions, TTL-based
}

func (r *GroupResolver) Resolve(ctx context.Context, userID string) (*EffectivePermissions, error) {
    if cached, ok := r.cache.Load(userID); ok {
        return cached.(*EffectivePermissions), nil
    }
    
    // Get all groups for user
    groups, err := r.configStore.GetUserGroups(userID)
    if err != nil { return nil, err }
    
    perm := &EffectivePermissions{}
    
    for _, group := range groups {
        // Highest role wins
        if roleRank(group.Role) > roleRank(perm.Role) {
            perm.Role = group.Role
        }
        
        // Collect VK IDs
        vks, _ := r.configStore.GetUserGroupVirtualKeys(group.ID)
        for _, vk := range vks {
            perm.VirtualKeyIDs = append(perm.VirtualKeyIDs, vk.VirtualKeyID)
        }
        
        // Collect MCP group IDs
        mcpGroups, _ := r.configStore.GetUserGroupMCPGroups(group.ID)
        for _, mg := range mcpGroups {
            perm.MCPGroupIDs = append(perm.MCPGroupIDs, mg.MCPGroupID)
        }
        
        // Most restrictive budget wins
        if group.BudgetMaxLimit != nil {
            if perm.BudgetLimit == nil || *group.BudgetMaxLimit < *perm.BudgetLimit {
                perm.BudgetLimit = group.BudgetMaxLimit
            }
        }
    }
    
    // Deduplicate VK IDs
    perm.VirtualKeyIDs = unique(perm.VirtualKeyIDs)
    perm.MCPGroupIDs = unique(perm.MCPGroupIDs)
    
    // Cache with TTL
    go time.AfterFunc(5*time.Minute, func() { r.cache.Delete(userID) })
    r.cache.Store(userID, perm)
    return perm, nil
}
```

---

## 5. Governance Integration

Groups affect the request path in the governance pre-hook:

```go
// plugins/governance/precheck.go (MODIFY)

func (g *GovernancePlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (
    *schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
    
    userID := getUserID(ctx)
    
    // Resolve user group permissions
    if userID != "" && g.groupResolver != nil {
        perm, err := g.groupResolver.Resolve(ctx, userID)
        if err == nil && perm != nil {
            // If no explicit VK in request but user has group-assigned VKs, auto-select first
            vkID := getVirtualKeyID(ctx)
            if vkID == "" && len(perm.VirtualKeyIDs) > 0 {
                ctx.SetValue(schemas.BifrostContextKeyVirtualKey, perm.VirtualKeyIDs[0])
            }
            // Apply group budget/rate limit if more restrictive than VK-level
            ctx.SetValue("bifrost-group-permissions", perm)
        }
    }
    
    // Continue with existing VK/budget/rate-limit checks
    // ...
}
```

---

## 6. SCIM Group Sync

When SCIM provisioner (TECH-005) receives a group push, create/update user groups:

```go
// framework/scim/provisioner.go (MODIFY)

func (p *SCIMProvisioner) UpsertGroup(scimGroup SCIMGroup) (*tables.UserGroupTable, error) {
    group := &tables.UserGroupTable{
        Name:       scimGroup.DisplayName,
        ExternalID: scimGroup.ID,
        Role:       p.resolveGroupRole(scimGroup.DisplayName),
    }
    
    // Upsert group
    dbGroup, err := p.configStore.UpsertUserGroup(group)
    if err != nil { return nil, err }
    
    // Sync members
    for _, member := range scimGroup.Members {
        user, _ := p.configStore.FindExternalUserByExternalID(member.Value)
        if user != nil {
            p.configStore.AddUserToGroup(dbGroup.ID, user.ID)
        }
    }
    
    return dbGroup, nil
}

func (p *SCIMProvisioner) DeprovisionGroup(externalID string) error {
    return p.configStore.DeactivateUserGroup(externalID)
}
```

---

## 7. Management API

```go
// transports/bifrost-http/handlers/user_groups.go

// GET    /api/user-groups                    — list groups
// POST   /api/user-groups                    — create group (admin+)
// GET    /api/user-groups/{id}               — get group detail
// PUT    /api/user-groups/{id}               — update group (admin+)
// DELETE /api/user-groups/{id}               — delete group (admin+)

// GET    /api/user-groups/{id}/members       — list members
// POST   /api/user-groups/{id}/members       — add member (admin+)
// DELETE /api/user-groups/{id}/members/{uid} — remove member (admin+)

// GET    /api/user-groups/{id}/virtual-keys        — VKs assigned to group
// POST   /api/user-groups/{id}/virtual-keys        — assign VK (admin+)
// DELETE /api/user-groups/{id}/virtual-keys/{vkid} — unassign VK

// GET    /api/user-groups/{id}/mcp-groups          — MCP groups assigned
// POST   /api/user-groups/{id}/mcp-groups          — assign MCP group
// DELETE /api/user-groups/{id}/mcp-groups/{mgid}   — unassign

// GET    /api/user-groups/{id}/budget              — group budget usage
// GET    /api/users/{id}/groups                    — groups for a user
// GET    /api/users/{id}/effective-permissions     — resolved effective permissions
```

---

## 8. UI Components

```
ui/app/enterprise/user-groups/
├── page.tsx                      — Group list with member counts + role badges
├── [id]/page.tsx                 — Group editor
└── components/
    ├── GroupCard.tsx             — Compact group overview
    ├── MemberList.tsx            — Member add/remove with user search autocomplete
    ├── VirtualKeyAssigner.tsx    — Multi-select VK assignment
    ├── MCPGroupAssigner.tsx      — MCP tool group assignment
    └── EffectivePermissions.tsx  — Visual resolved permissions for a selected user
```

---

## 9. Migration from Flat VK Model

Current `TeamsTable` and `CustomersTable` partially overlap with User Groups:

| Current | Target |
|---------|--------|
| `TeamsTable` | Can be migrated to `UserGroupTable` with role="operator" |
| `CustomersTable` | Represents external customer org — keep as separate entity |
| `VirtualKeysTable.team_id` | Add `UserGroupVirtualKeyTable` entries |

Migration script: `POST /api/user-groups/migrate-from-teams` (super_admin only, non-destructive).
