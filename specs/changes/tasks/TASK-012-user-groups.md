# TASK-012 — User Groups

**Feature:** User Groups  
**TECH Spec:** [TECH-012-user-groups.md](../TECH-012-user-groups.md)  
**Phase:** 1 (Security Core)  
**Depends on:** TASK-014 (license), TASK-001 (RBAC — role assignment), TASK-005 (SSO/SCIM — group sync)  
**Estimate:** 4 days  
**Assignee:** —  
**Status:** 🟢 Completed

---

## Context

User groups provide batch assignment of VKs, MCP tool groups, and RBAC roles to collections of users. Groups can be created manually or synced from an IdP via SCIM. This unlocks team-based access policies without per-user configuration.

---

## Tasks

### TASK-012-01 — Database schema + GORM migration

**Files to create:**
- `framework/configstore/tables/user_groups.go` — `UserGroupTable`, `UserGroupMemberTable`, `UserGroupVirtualKeyTable`, `UserGroupMCPGroupTable`
- Migration file

**Schema summary:** (see TECH-012 §3 for full schema)
```go
UserGroupTable {
    ID, Name, Description, Role, ExternalID, SyncedAt,
    BudgetMaxLimit, BudgetResetDuration,
    RateLimitRequestsPerMin, RateLimitTokensPerMin,
    CreatedAt, UpdatedAt
}
UserGroupMemberTable { ID, GroupID, UserID, AddedBy, AddedAt }
UserGroupVirtualKeyTable { ID, GroupID, VirtualKeyID, BudgetOverride }
UserGroupMCPGroupTable { ID, GroupID, MCPGroupID }
```

**Acceptance criteria:**
- [ ] Migration runs cleanly; idempotent
- [ ] `UserGroupTable.ExternalID` indexed for SCIM sync lookup
- [ ] Cascade delete: deleting group removes all member and VK/MCP assignments

---

### TASK-012-02 — ConfigStore operations

**Files to create:**
- `framework/configstore/user_groups.go`

**Operations required:**
```go
CreateUserGroup(*UserGroupTable) error
GetUserGroup(id string) (*UserGroupTable, error)
ListUserGroups() ([]UserGroupTable, error)
UpdateUserGroup(id string, updates map[string]any) error
DeleteUserGroup(id string) error
UpsertUserGroup(group *UserGroupTable) (*UserGroupTable, error)  // for SCIM sync
FindUserGroupByExternalID(externalID string) (*UserGroupTable, error)
DeactivateUserGroup(externalID string) error

AddUserToGroup(groupID, userID, addedBy string) error
RemoveUserFromGroup(groupID, userID string) error
GetUserGroups(userID string) ([]UserGroupTable, error)
GetUserGroupMembers(groupID string) ([]UserGroupMemberTable, error)

AssignVirtualKeyToGroup(groupID, vkID string, budgetOverride *float64) error
UnassignVirtualKeyFromGroup(groupID, vkID string) error
GetUserGroupVirtualKeys(groupID string) ([]UserGroupVirtualKeyTable, error)

AssignMCPGroupToUserGroup(groupID, mcpGroupID string) error
UnassignMCPGroupFromUserGroup(groupID, mcpGroupID string) error
GetUserGroupMCPGroups(groupID string) ([]UserGroupMCPGroupTable, error)
```

**Acceptance criteria:**
- [ ] All write operations are transactional where multiple rows affected
- [ ] `UpsertUserGroup` updates existing group by `ExternalID` match

---

### TASK-012-03 — Effective permission resolver

**Files to create:**
- `plugins/governance/group_resolver.go` — `GroupResolver`

**Implementation:** (see TECH-012 §4 for full code)

**Resolution rules:**
- **Role**: highest role across all groups wins
- **VKs**: union of all group-assigned VKs (deduplicated)
- **MCP Groups**: union of all group-assigned MCP groups (deduplicated)
- **Budget**: most restrictive (lowest limit) wins
- **Rate limit**: most restrictive (lowest limit) wins

**Acceptance criteria:**
- [ ] `Resolve(ctx, userID)` returns `*EffectivePermissions`
- [ ] User with no groups → empty permissions (nil fields)
- [ ] Cache TTL: 5 minutes; invalidated on group membership change
- [ ] `roleRank()` correctly ranks: viewer(1) < operator(2) < admin(3) < super_admin(4)

---

### TASK-012-04 — Governance plugin integration

**Files to modify:**
- `plugins/governance/precheck.go` — inject `GroupResolver` into `PreLLMHook`

**Changes:**
- Extract `userID` from `BifrostContext`
- Call `GroupResolver.Resolve(ctx, userID)`
- If no explicit VK in request but user has group-assigned VKs → auto-select first
- Apply group budget/rate limit if more restrictive than VK-level
- Store resolved permissions in context for downstream plugins

**Acceptance criteria:**
- [ ] Auto-VK selection: user in group with VK → request with no `x-bf-vk` header uses group VK
- [ ] Budget override: `UserGroupVirtualKeyTable.BudgetOverride` applied when more restrictive
- [ ] Resolution bypassed if `userID` not set in context (unauthenticated requests)

---

### TASK-012-05 — User group management API

**Files to create:**
- `transports/bifrost-http/handlers/user_groups.go`

**Endpoints:**
```
GET    /api/user-groups               — list groups (admin+)
POST   /api/user-groups               — create group (admin+)
GET    /api/user-groups/{id}          — group detail
PUT    /api/user-groups/{id}          — update group (admin+)
DELETE /api/user-groups/{id}          — delete group (admin+)

GET    /api/user-groups/{id}/members  — list members
POST   /api/user-groups/{id}/members  — add member (admin+)
DELETE /api/user-groups/{id}/members/{uid} — remove member (admin+)

GET    /api/user-groups/{id}/virtual-keys        — VKs assigned to group
POST   /api/user-groups/{id}/virtual-keys        — assign VK (admin+)
DELETE /api/user-groups/{id}/virtual-keys/{vkid} — unassign VK

GET    /api/user-groups/{id}/mcp-groups          — MCP groups assigned
POST   /api/user-groups/{id}/mcp-groups          — assign MCP group
DELETE /api/user-groups/{id}/mcp-groups/{mgid}   — unassign

GET    /api/users/{id}/groups                     — groups for a user (admin+)
GET    /api/users/{id}/effective-permissions      — resolved effective permissions

POST   /api/user-groups/migrate-from-teams        — migrate TeamsTable to UserGroups (super_admin)
```

**Acceptance criteria:**
- [ ] All endpoints require `user_groups` feature enabled
- [ ] Group CRUD audit-logged
- [ ] `GET /api/users/{id}/effective-permissions` returns resolved role, VKs, MCP groups, budget limit
- [ ] Migration endpoint non-destructive (TeamsTable not deleted)

---

### TASK-012-06 — SCIM group provisioner integration

**Files to modify:**
- `framework/scim/provisioner.go` (from TASK-005-04) — `UpsertGroup()` and `DeprovisionGroup()` implemented

**Acceptance criteria:**
- [ ] SCIM `POST /scim/v2/Groups` → creates `UserGroupTable` with `ExternalID`
- [ ] SCIM `PUT /scim/v2/Groups/{id}` → updates group name + syncs member list
- [ ] SCIM `DELETE /scim/v2/Groups/{id}` → `DeactivateUserGroup()` (soft delete)
- [ ] `UpsertGroup()` resolves IdP group name to RBAC role via `RoleMappingJSON`

---

### TASK-012-07 — UI: user group management

**Files to create:**
- `ui/app/enterprise/user-groups/page.tsx`
- `ui/app/enterprise/user-groups/[id]/page.tsx`
- `ui/app/enterprise/user-groups/components/GroupCard.tsx`
- `ui/app/enterprise/user-groups/components/MemberList.tsx`
- `ui/app/enterprise/user-groups/components/VirtualKeyAssigner.tsx`
- `ui/app/enterprise/user-groups/components/MCPGroupAssigner.tsx`
- `ui/app/enterprise/user-groups/components/EffectivePermissions.tsx`

**Acceptance criteria:**
- [ ] Group list: member count, role badge, VK count, sync source (manual/SCIM)
- [ ] Member list: user search autocomplete for adding members
- [ ] `EffectivePermissions` component: shows resolved permissions for a selected user (fetches `GET /api/users/{id}/effective-permissions`)
- [ ] SCIM-synced groups show lock icon (members cannot be manually added/removed)
- [ ] Page inside `<EnterpriseGate feature="user_groups">`

---

## Definition of Done

- [ ] All subtasks complete
- [ ] Unit test: `GroupResolver.Resolve()` with multiple groups having conflicting limits (most restrictive wins)
- [ ] Integration test: user in group with VK → request without `x-bf-vk` header → group VK auto-selected
- [ ] Integration test: SCIM `PUT /scim/v2/Groups/{id}` with updated members → `UserGroupMemberTable` updated
- [ ] Integration test: budget override applied when group override < VK limit
- [ ] `make build` passes
