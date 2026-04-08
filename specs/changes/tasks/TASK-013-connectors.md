# TASK-013 — Data Connectors

**Feature:** Data Connectors Framework  
**TECH Spec:** [TECH-013-connectors.md](../TECH-013-connectors.md)  
**Phase:** 4 (Intelligence)  
**Depends on:** TASK-014 (license), TASK-011 (MCP Tool Groups — connectors Surface as MCP tools)  
**Estimate:** 8 days  
**Assignee:** —  
**Status:** 🟢 Completed

---

## Context

Data connectors are pre-built MCP tool implementations packaged as a library (`core/mcp/connectors/`). Each connector exposes a set of typed MCP tools that LLM agents can call to retrieve context from enterprise data sources (PostgreSQL, MongoDB, REST APIs, Slack, Jira, etc.).

Connectors are registered in a `ConnectorRegistry` which is merged into the MCP tool discovery pipeline. All connector credentials are stored encrypted in the database.

**Connector naming convention:** Tool names are prefixed with `{connectorID}__` to avoid collisions:  
e.g., connector `"prod-db"` → tool name `"prod-db__postgresql_query"`

---

## Tasks

### TASK-013-01 — Database schema + GORM migration

**Files to create:**
- `framework/configstore/tables/connectors.go` — `ConnectorTable`
- Migration file

**Schema:**
```go
type ConnectorTable struct {
    ID          string    // UUID
    Name        string    // uniqueIndex
    Type        string    // ConnectorType
    Enabled     bool
    ConfigJSON  string    // encrypted connector-specific config
    MCPGroupIDs string    // JSON: which MCP tool groups can use this connector
    Description string
    LastTestedAt *time.Time
    LastTestOK  bool
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

**Acceptance criteria:**
- [ ] Migration runs cleanly; idempotent
- [ ] `ConfigJSON` stored encrypted (using `framework/encrypt` or Vault Transit)
- [ ] `Type` indexed for filtering by connector type

---

### TASK-013-02 — `DataConnector` interface + `ConnectorRegistry`

**Files to create:**
- `core/mcp/connectors/connector.go` — `DataConnector` interface, `ConnectorType` constants
- `core/mcp/connectors/registry.go` — `ConnectorRegistry`

**Registry:**
```go
type ConnectorRegistry struct {
    connectors map[string]DataConnector
    mu         sync.RWMutex
}

GetAllTools() []schemas.ChatToolFunction  // prefixed with connectorID__
GetTool(connectorID, toolName string) (DataConnector, bool)
Register(id string, connector DataConnector)
Unregister(id string)
```

**Acceptance criteria:**
- [ ] `GetAllTools()` returns tools with `{connectorID}__{toolName}` namespacing
- [ ] `Register()` and `Unregister()` thread-safe
- [ ] Registry integrated into `core/mcp/clientmanager.go` via `RegisterConnectors()`

---

### TASK-013-03 — SQL connectors (PostgreSQL, MySQL, SQLite)

**Files to create:**
- `core/mcp/connectors/sql/postgresql.go`
- `core/mcp/connectors/sql/mysql.go`
- `core/mcp/connectors/sql/sqlite.go`

**Tools exposed per SQL connector:**
1. `{id}__postgresql_query` — execute SELECT query, returns JSON array of rows
2. `{id}__postgresql_schema` — get table schema and column info

**Security requirements:**
- `ReadOnly: true` (default) — reject any non-SELECT statement using parser check
- `AllowedSchemas` list — query blocked if it accesses unlisted schemas
- `MaxRowsReturned` (default: 1000) — hard limit via `LIMIT` wrapper
- `QueryTimeout` (default: 30s) — context deadline

**Acceptance criteria:**
- [ ] `ReadOnly=true`: any non-SELECT rejected with explicit error message
- [ ] `AllowedSchemas`: query validated against schema list (parse `FROM` clause)
- [ ] Max rows enforced by wrapping query: `SELECT * FROM ({query}) AS _q LIMIT {max}`
- [ ] Connection pool: `MaxOpenConns=5`, configurable
- [ ] Tool description clearly states SELECT-only restriction
- [ ] Unit tests: valid SELECT → rows returned; INSERT → rejected; schema query → column list

---

### TASK-013-04 — NoSQL connectors (MongoDB, Elasticsearch, Redis)

**Files to create:**
- `core/mcp/connectors/nosql/mongodb.go`
- `core/mcp/connectors/nosql/elasticsearch.go`
- `core/mcp/connectors/nosql/redis.go`

**Tools per connector:**

| Connector | Tools |
|-----------|-------|
| MongoDB | `find` (filter + projection), `aggregate` (pipeline), `list_collections` |
| Elasticsearch | `search` (query DSL), `get_mapping` (index schema) |
| Redis | `get`, `hgetall`, `lrange`, `smembers`, `keys` (with pattern) |

**Acceptance criteria per connector:**
- [ ] MongoDB: result size limit configurable (default: 1000 documents)
- [ ] MongoDB: `aggregate` pipeline blocked if it contains `$out` or `$merge` stages
- [ ] Elasticsearch: response hits limited to `max_hits` (default: 100)
- [ ] Redis: `keys *` blocked; pattern must contain at least one non-wildcard character
- [ ] All connectors: timeout configurable (default: 30s)

---

### TASK-013-05 — Cloud connectors (S3, Google Drive)

**Files to create:**
- `core/mcp/connectors/cloud/s3.go`
- `core/mcp/connectors/cloud/gdrive.go`

**Tools:**

| Connector | Tools |
|-----------|-------|
| S3 | `list_objects` (prefix filter), `get_object` (returns content as text/base64), `get_presigned_url` |
| Google Drive | `list_files` (name/type filter), `get_file_content` (text files only), `search_files` (full-text) |

**Acceptance criteria:**
- [ ] S3: `get_object` contents limited to 1MB (configurable); binary files returned as base64
- [ ] S3: allowed bucket list configurable — requests to non-allowed buckets blocked
- [ ] Google Drive: OAuth2 service account auth (no interactive consent flow)
- [ ] Both: `Test()` verifies connectivity without side effects (list with empty prefix)

---

### TASK-013-06 — SaaS connectors (Slack, Confluence, Jira)

**Files to create:**
- `core/mcp/connectors/saas/slack.go`
- `core/mcp/connectors/saas/confluence.go`
- `core/mcp/connectors/saas/jira.go`

**Tools:**

| Connector | Tools |
|-----------|-------|
| Slack | `list_channels`, `get_channel_messages` (last N messages), `search_messages` |
| Confluence | `search_pages` (CQL query), `get_page_content` (markdown), `list_spaces` |
| Jira | `search_issues` (JQL), `get_issue`, `list_projects` |

**Acceptance criteria:**
- [ ] Slack: read-only API scope (`channels:read`, `messages:read`); no posting
- [ ] Confluence + Jira: API token auth (Atlassian Cloud) and personal access token (Server)
- [ ] Message/content size limit: 10KB per item (truncated with `[truncated]` marker)

---

### TASK-013-07 — REST API connector

**Files to create:**
- `core/mcp/connectors/rest/rest_api.go`

**This is the most complex connector** — admin defines tool definitions in config:

```go
type RestToolDef struct {
    Name, Description string
    Method            string  // GET|POST
    PathTemplate      string  // "/users/{user_id}"
    Parameters        []RestParam
}
type RestParam struct {
    Name, In, Type string  // In: "path"|"query"|"body"
    Required        bool
    Description     string
}
```

**Auth types:** `bearer`, `api_key`, `basic`, `oauth2`

**Acceptance criteria:**
- [ ] Path template: `{param_name}` interpolated from tool call arguments
- [ ] Query params: appended to URL for `In="query"` parameters
- [ ] Body: constructed as JSON object for `In="body"` parameters
- [ ] OAuth2 client credentials flow: token cached until near expiry
- [ ] Response size limit: 1MB (hard cap, returns error if exceeded)
- [ ] Timeout: 30s default, configurable per connector
- [ ] `Test()`: calls first tool with default/empty parameters and checks for non-5xx response

---

### TASK-013-08 — Integration with MCP client manager

**Files to modify:**
- `core/mcp/clientmanager.go` — `RegisterConnectors()`, override `GetTools()` to merge connector tools

**Acceptance criteria:**
- [ ] Connector tools appear in MCP tool discovery alongside regular MCP client tools
- [ ] Tool execution: `{connectorID}__toolName` → parsed → routed to correct connector
- [ ] Connector execution errors returned as MCP tool result errors (not crash)
- [ ] Connector execution audit-logged with connector ID + tool name (TECH-002 integration)

---

### TASK-013-09 — Connector management API

**Files to create:**
- `transports/bifrost-http/handlers/connectors.go`

**Endpoints:**
```
GET    /api/connectors              — list connectors (admin+)
POST   /api/connectors              — create connector (admin+)
GET    /api/connectors/{id}         — get connector (configJSON masked)
PUT    /api/connectors/{id}         — update connector (admin+)
DELETE /api/connectors/{id}         — delete connector (admin+)
POST   /api/connectors/{id}/test    — test connectivity
POST   /api/connectors/{id}/enable
POST   /api/connectors/{id}/disable
GET    /api/connectors/{id}/tools   — list tools exposed by this connector
GET    /api/connectors/types        — list supported types + config JSON schema per type
```

**Acceptance criteria:**
- [ ] All endpoints require `data_connectors` feature enabled
- [ ] `configJSON` fields with secrets (passwords, tokens) masked in GET responses
- [ ] CRUD operations audit-logged
- [ ] `GET /api/connectors/types` returns JSON schema for each connector type (used by UI form)

---

### TASK-013-10 — UI: connector management

**Files to create:**
- `ui/app/enterprise/connectors/page.tsx`
- `ui/app/enterprise/connectors/new/page.tsx`
- `ui/app/enterprise/connectors/[id]/page.tsx`
- `ui/app/enterprise/connectors/components/ConnectorTypeCard.tsx`
- `ui/app/enterprise/connectors/components/ConnectorConfigForm.tsx` (dynamic, driven by type schema)
- `ui/app/enterprise/connectors/components/ToolPreview.tsx`
- `ui/app/enterprise/connectors/components/ConnectionTestPanel.tsx`

**Acceptance criteria:**
- [ ] Connector type selector with icons + descriptions for all 12 connector types
- [ ] Config form dynamically renders fields based on `GET /api/connectors/types` JSON schema
- [ ] Tool preview shows generated tool definitions after config entered
- [ ] "Test Connection" real-time output panel with streaming log display
- [ ] Page inside `<EnterpriseGate feature="data_connectors">`

---

## Definition of Done

- [ ] All subtasks complete
- [ ] Unit tests: PostgreSQL connector — SELECT allowed, INSERT rejected, max rows enforced
- [ ] Unit tests: REST connector — path template interpolation, query params, body construction
- [ ] Integration test: PostgreSQL connector → tool discovery includes `{id}__postgresql_query` → tool execution returns rows
- [ ] Integration test: REST connector with bearer auth → HTTP call made with correct Authorization header
- [ ] Integration test: connector execution audit-logged with connector ID
- [ ] Security test: SQL connector with `ReadOnly=true` rejects `DROP TABLE`
- [ ] `make build` passes
