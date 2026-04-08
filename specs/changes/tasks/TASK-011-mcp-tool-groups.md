# TASK-011 — MCP Tool Groups

**Feature:** MCP Tool Groups  
**TECH Spec:** [TECH-011-mcp-tool-groups.md](../TECH-011-mcp-tool-groups.md)  
**Phase:** 4 (Intelligence)  
**Depends on:** TASK-014 (license), TASK-001 (RBAC), TASK-012 (User Groups — cross-ref schema)  
**Estimate:** 4 days  
**Assignee:** —  
**Status:** 🟢 Completed

---

## Context

Currently VKs have a flat `mcp_configs` array (client_id → allowed_tool list). MCP Tool Groups add a named grouping layer allowing administrators to bundle tools and assign them to VKs or user groups. Backward compatibility with existing `mcp_configs` is required.

---

## Tasks

### TASK-011-01 — Database schema + GORM migration

**Files to create:**
- `framework/configstore/tables/mcp_groups.go` — `MCPToolGroupTable`, `MCPToolGroupMemberTable`, `VirtualKeyMCPGroupTable`, `UserGroupMCPGroupTable`
- Migration file

**Acceptance criteria:**
- [ ] Migration runs cleanly; idempotent
- [ ] Foreign key constraints: `GroupID` references `MCPToolGroupTable.ID`
- [ ] `MCPToolGroupTable.MaxCallsPerHour` and `MaxCallsPerDay` nullable (nil = unlimited)

---

### TASK-011-02 — ConfigStore operations

**Files to create:**
- `framework/configstore/mcp_groups.go`

**Operations:**
```go
CreateMCPToolGroup(group MCPToolGroupTable) error
GetMCPToolGroup(id string) (*MCPToolGroupTable, error)
ListMCPToolGroups() ([]MCPToolGroupTable, error)
UpdateMCPToolGroup(id string, updates map[string]any) error
DeleteMCPToolGroup(id string) error

AddMCPToolGroupMember(groupID, clientID, toolName string) error
RemoveMCPToolGroupMember(memberID string) error
GetMCPToolGroupMembers(groupID string) ([]MCPToolGroupMemberTable, error)

AssignVirtualKeyMCPGroup(vkID, groupID string) error
UnassignVirtualKeyMCPGroup(vkID, groupID string) error
GetVirtualKeyMCPGroups(vkID string) ([]MCPToolGroupTable, error)
```

**Acceptance criteria:**
- [ ] All operations are transactional where multiple rows affected
- [ ] `DeleteMCPToolGroup` cascade-deletes members and VK/user group assignments

---

### TASK-011-03 — `ToolGroupResolver` in `core/mcp`

**Files to create:**
- `core/mcp/groupresolver.go` — `ToolGroupResolver`

**Implementation:** (see TECH-011 §4 for full code)

**Acceptance criteria:**
- [ ] `GetAllowedTools(ctx, vkID)` returns flattened tool list from all assigned groups
- [ ] Empty `ToolName` in `MCPToolGroupMemberTable` → all tools from that client allowed
- [ ] TTL-based in-memory cache per VK (5-minute TTL)
- [ ] Cache invalidated via pub/sub event when group membership changes (TASK-007-05)
- [ ] Backward compat: VK with no group assignments → resolver returns nil (toolmanager uses legacy `mcp_configs`)

---

### TASK-011-04 — Tool manager integration

**Files to modify:**
- `core/mcp/toolmanager.go` — add `GetFilteredTools(ctx)` using `ToolGroupResolver`

**Changes:**
- New method `GetFilteredTools(ctx)` wraps existing `GetAllTools(ctx)` with filtering
- Filtering: if VK has group assignments → filter against group tool list; else use legacy path
- `isToolAllowed()`: checks `clientID` wildcard and specific `toolName`

**Acceptance criteria:**
- [ ] VK with no group → all tools returned (legacy behavior preserved)
- [ ] VK with group → only tools in assigned groups returned
- [ ] VK with group but empty group (no members) → empty tool list returned
- [ ] `isToolAllowed()` handles wildcard `ToolName=""` → all tools from that client allowed

---

### TASK-011-05 — Tool quota enforcement

**Files to create:**
- `plugins/governance/mcp_groups.go` — `MCPGroupQuotaChecker`

**Called from:** `GovernancePlugin.PreMCPHook()` (add MCPPlugin interface to governance plugin)

**Acceptance criteria:**
- [ ] Hourly quota enforced via Redis atomic counter (`INCR` + `EXPIRE`)
- [ ] Daily quota enforced via separate counter with 24h TTL
- [ ] Redis unavailable → fail open (allow tool call) with warning log
- [ ] Quota exceeded → return error that appears as MCP tool error in agent response

---

### TASK-011-06 — MCP group management API

**Files to create:**
- `transports/bifrost-http/handlers/mcp_groups.go`

**Endpoints:**
```
GET    /api/mcp/groups               — list groups (admin+)
POST   /api/mcp/groups               — create group (admin+)
GET    /api/mcp/groups/{id}          — get group + members
PUT    /api/mcp/groups/{id}          — update group (admin+)
DELETE /api/mcp/groups/{id}          — delete group (admin+)

GET    /api/mcp/groups/{id}/members  — list tools
POST   /api/mcp/groups/{id}/members  — add tool (clientID + toolName)
DELETE /api/mcp/groups/{id}/members/{mid} — remove tool

GET    /api/governance/virtual-keys/{id}/mcp-groups   — groups assigned to VK
POST   /api/governance/virtual-keys/{id}/mcp-groups   — assign group to VK
DELETE /api/governance/virtual-keys/{id}/mcp-groups/{gid}

GET    /api/mcp/groups/{id}/usage    — quota usage (calls/hour, calls/day)

POST   /api/mcp/groups/migrate       — convert legacy mcp_configs to groups (super_admin)
```

**Acceptance criteria:**
- [ ] All endpoints require `mcp_tool_groups` feature enabled
- [ ] Group CRUD operations audit-logged
- [ ] Migration endpoint non-destructive (does not remove legacy `mcp_configs`)

---

### TASK-011-07 — MCPLog table extension

**Files to modify:**
- `framework/logstore/tables.go` — add `GroupID string` and `GroupName string` to `MCPLogTable`
- Migration file

**Acceptance criteria:**
- [ ] New columns added without breaking existing log entries (nullable)
- [ ] Populated when tool group resolved for a tool execution

---

### TASK-011-08 — UI: Tool group management

**Files to create:**
- `ui/app/workspace/mcp/groups/page.tsx`
- `ui/app/workspace/mcp/groups/[id]/page.tsx`
- `ui/app/workspace/mcp/groups/components/ToolGroupCard.tsx`
- `ui/app/workspace/mcp/groups/components/ToolPicker.tsx`
- `ui/app/workspace/mcp/groups/components/QuotaGauge.tsx`

**Acceptance criteria:**
- [ ] Group list with member count, assigned VK count, quota settings
- [ ] Tool picker: tree view of MCP clients → tools with multi-select checkboxes
- [ ] Quota gauge: real-time usage bar (calls/hour, calls/day)
- [ ] Page inside `<EnterpriseGate feature="mcp_tool_groups">`

---

## Definition of Done

- [ ] All subtasks complete
- [ ] Unit test: `ToolGroupResolver.GetAllowedTools()` with mixed group assignments
- [ ] Unit test: `isToolAllowed()` wildcard and specific tool cases
- [ ] Integration test: VK with group → only group tools visible in tool discovery
- [ ] Integration test: quota exceeded → tool execution blocked with error
- [ ] Backward compat test: VK with legacy `mcp_configs` and no groups → all tools accessible
- [ ] `make build` passes
