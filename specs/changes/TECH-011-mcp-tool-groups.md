# TECH-011 — MCP Tool Groups

**Feature ID:** MCPGRP  
**SRS Reference:** §3.22 (MCPGRP-01 → MCPGRP-07)  
**CR Reference:** CR-ENT-001, CR-ENT-002  
**Version:** 1.0 | **Date:** 2026-04-08  
**Status:** Design Ready

---

## 1. Overview

Extend the existing MCP tool system with group-based access control. Tool groups allow administrators to bundle related MCP tools and assign them to virtual keys or user groups, providing fine-grained tool access control.

**Current state:** Virtual keys have a flat `mcp_configs` array mapping client_id → allowed_tool list.

**Target state:**
- Named tool groups (e.g., "database-tools", "search-tools", "code-tools")
- Groups assigned to virtual keys or user groups
- Tool execution audit trail
- Tool usage quotas per group

---

## 2. Architecture Mapping

```
transports/bifrost-http/
├── handlers/
│   └── mcp_groups.go          (NEW) /api/mcp/groups/* CRUD

framework/configstore/
├── tables/
│   └── mcp_groups.go          (NEW) MCPToolGroupTable, MCPToolGroupMemberTable
└── mcp_groups.go              (NEW) ConfigStore operations

core/mcp/
├── toolmanager.go             (MODIFY) Add group-aware tool filtering
└── groupresolver.go           (NEW) Resolve effective tool list from groups

plugins/governance/
└── mcp_groups.go              (NEW) Tool group quota enforcement
```

---

## 3. Database Schema

```go
// framework/configstore/tables/mcp_groups.go

type MCPToolGroupTable struct {
    ID          string    `gorm:"primaryKey;type:text"`
    Name        string    `gorm:"uniqueIndex;not null"`
    Description string
    Enabled     bool      `gorm:"default:true"`
    // Quota
    MaxCallsPerHour   *int64   // nil = unlimited
    MaxCallsPerDay    *int64
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type MCPToolGroupMemberTable struct {
    ID          string    `gorm:"primaryKey;type:text"`
    GroupID     string    `gorm:"index;not null"`
    ClientID    string    `gorm:"index;not null"`  // MCP client ID
    ToolName    string    `gorm:"not null"`          // empty = all tools from client
    CreatedAt   time.Time
}

// Extend VirtualKeysTable to reference groups
type VirtualKeyMCPGroupTable struct {
    ID           string    `gorm:"primaryKey;type:text"`
    VirtualKeyID string    `gorm:"index;not null"`
    GroupID      string    `gorm:"index;not null"`
}

// Extend UserGroupsTable (TECH-012) to reference tool groups
type UserGroupMCPGroupTable struct {
    ID          string    `gorm:"primaryKey;type:text"`
    UserGroupID string    `gorm:"index;not null"`
    GroupID     string    `gorm:"index;not null"`
}
```

---

## 4. Tool Group Resolver

```go
// core/mcp/groupresolver.go

type ToolGroupResolver struct {
    configStore framework.ConfigStore
    cache       sync.Map  // vkID → []string (allowed tool names), TTL-based
    cacheTTL    time.Duration
}

// GetAllowedTools returns the effective tool list for a given virtual key
func (r *ToolGroupResolver) GetAllowedTools(ctx context.Context, vkID string) ([]AllowedTool, error) {
    if cached, ok := r.cache.Load(vkID); ok {
        return cached.([]AllowedTool), nil
    }
    
    // Load group assignments for this VK
    groups, err := r.configStore.GetVirtualKeyMCPGroups(vkID)
    if err != nil { return nil, err }
    
    // Flatten group members into tool list
    var tools []AllowedTool
    for _, group := range groups {
        members, _ := r.configStore.GetMCPToolGroupMembers(group.ID)
        for _, m := range members {
            tools = append(tools, AllowedTool{
                ClientID: m.ClientID,
                ToolName: m.ToolName,  // empty = all tools from client
            })
        }
    }
    
    // Cache with TTL
    go time.AfterFunc(r.cacheTTL, func() { r.cache.Delete(vkID) })
    r.cache.Store(vkID, tools)
    return tools, nil
}

type AllowedTool struct {
    ClientID string
    ToolName string  // empty = all tools from this client
}
```

---

## 5. Tool Manager Integration

Modify existing `core/mcp/toolmanager.go` to filter tools based on group resolver:

```go
// core/mcp/toolmanager.go (MODIFY)

func (m *ToolManager) GetFilteredTools(ctx *schemas.BifrostContext) ([]schemas.ChatToolFunction, error) {
    vkID := getVirtualKeyID(ctx)
    
    // Get all registered tools
    allTools, err := m.GetAllTools(ctx)
    if err != nil { return nil, err }
    
    // No VK or no group resolver → return all tools (backward compatible)
    if vkID == "" || m.groupResolver == nil {
        return allTools, nil
    }
    
    // Get allowed tools for this VK
    allowed, err := m.groupResolver.GetAllowedTools(ctx, vkID)
    if err != nil { return allTools, nil }  // fail open
    
    if len(allowed) == 0 {
        return nil, nil  // no tools allowed
    }
    
    // Filter
    allowedMap := buildAllowedMap(allowed)
    var filtered []schemas.ChatToolFunction
    for _, tool := range allTools {
        if isToolAllowed(tool, allowedMap) {
            filtered = append(filtered, tool)
        }
    }
    return filtered, nil
}

func isToolAllowed(tool schemas.ChatToolFunction, allowedMap map[string]map[string]bool) bool {
    // Check: clientID with wildcard ToolName, or specific tool name
    clientTools, ok := allowedMap[tool.ClientID]
    if !ok { return false }
    return clientTools["*"] || clientTools[tool.Function.Name]
}
```

---

## 6. Tool Quota Enforcement

```go
// plugins/governance/mcp_groups.go

type MCPGroupQuotaChecker struct {
    configStore framework.ConfigStore
    kvStore     schemas.KVStore
}

// Called from MCPPlugin.PreMCPHook
func (c *MCPGroupQuotaChecker) CheckQuota(ctx context.Context, groupID, toolName string) error {
    group, err := c.configStore.GetMCPToolGroup(groupID)
    if err != nil || group.MaxCallsPerHour == nil { return nil }
    
    // Redis-style atomic counter
    key := fmt.Sprintf("mcp:quota:%s:%d", groupID, time.Now().Truncate(time.Hour).Unix())
    count, err := c.kvStore.IncrBy(ctx, key, 1, time.Hour)
    if err != nil { return nil }  // fail open
    
    if count > *group.MaxCallsPerHour {
        return fmt.Errorf("tool group %q quota exceeded (%d calls/hour)", group.Name, *group.MaxCallsPerHour)
    }
    return nil
}
```

---

## 7. Management API

```go
// transports/bifrost-http/handlers/mcp_groups.go

// GET    /api/mcp/groups               — list tool groups
// POST   /api/mcp/groups               — create group (admin+)
// GET    /api/mcp/groups/{id}          — get group + members
// PUT    /api/mcp/groups/{id}          — update group (admin+)
// DELETE /api/mcp/groups/{id}          — delete group (admin+)

// GET    /api/mcp/groups/{id}/members  — list tools in group
// POST   /api/mcp/groups/{id}/members  — add tool to group
// DELETE /api/mcp/groups/{id}/members/{member_id} — remove tool from group

// GET    /api/governance/virtual-keys/{id}/mcp-groups   — groups assigned to VK
// POST   /api/governance/virtual-keys/{id}/mcp-groups   — assign group to VK
// DELETE /api/governance/virtual-keys/{id}/mcp-groups/{gid} — unassign

// GET    /api/mcp/groups/{id}/usage    — quota usage stats
```

---

## 8. UI Components

```
ui/app/workspace/mcp/groups/
├── page.tsx                    — Tool group list
├── [id]/page.tsx               — Group editor (member list + quota settings)
└── components/
    ├── ToolGroupCard.tsx       — Group preview with member count + quota
    ├── ToolPicker.tsx          — Multi-select tool browser grouped by MCP client
    └── QuotaGauge.tsx          — Real-time quota usage visualization
```

---

## 9. Backward Compatibility

- Virtual keys with legacy `mcp_configs` (flat client_id + allowed_tools) continue to work unchanged.
- The resolver checks for group assignments first; falls back to legacy `mcp_configs` if no groups assigned.
- Migration: Admin can optionally convert legacy `mcp_configs` to groups via `POST /api/mcp/groups/migrate`.

---

## 10. Tool Execution Audit

All MCP tool executions are recorded in the existing MCP log table, extended with group information:

```go
// framework/logstore/tables.go (MODIFY)
// Add GroupID to existing MCPLogTable
type MCPLogTable struct {
    // ... existing fields ...
    GroupID   string    `gorm:"index"`   // NEW
    GroupName string                     // NEW
}
```
