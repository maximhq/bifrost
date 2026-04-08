# TECH-013 — Data Connectors

**Feature ID:** CONN  
**SRS Reference:** §3.24 (CONN-01 → CONN-12)  
**CR Reference:** CR-ENT-001, CR-ENT-002  
**Version:** 1.0 | **Date:** 2026-04-08  
**Status:** Design Ready

---

## 1. Overview

Implement a data connector framework that allows LLM-backed workflows to retrieve context from enterprise data sources (databases, document stores, APIs, message queues) via MCP tools. Connectors are pre-built MCP tool implementations packaged as a library.

**Supported connector types (SRS CONN-01):**
- `postgresql` — SQL query execution
- `mysql` / `sqlite` — SQL databases
- `mongodb` — document queries
- `elasticsearch` — full-text search
- `redis` — key-value lookups
- `rest_api` — generic HTTP REST calls with auth templates
- `s3` — object listing and retrieval
- `google_drive` — document retrieval
- `slack_read` — channel/thread reading
- `confluence` — page retrieval
- `jira` — issue queries
- `custom_mcp` — passthrough to any MCP server

---

## 2. Architecture Mapping

```
core/mcp/
└── connectors/                (NEW package)
    ├── connector.go           DataConnector interface
    ├── registry.go            ConnectorRegistry
    ├── sql/
    │   ├── postgresql.go      PostgreSQLConnector
    │   ├── mysql.go           MySQLConnector
    │   └── sqlite.go          SQLiteConnector
    ├── nosql/
    │   ├── mongodb.go         MongoDBConnector
    │   ├── elasticsearch.go   ElasticsearchConnector
    │   └── redis.go           RedisConnector
    ├── cloud/
    │   ├── s3.go              S3Connector
    │   └── gdrive.go          GoogleDriveConnector
    ├── saas/
    │   ├── slack.go           SlackConnector
    │   ├── confluence.go      ConfluenceConnector
    │   └── jira.go            JiraConnector
    └── rest/
        └── rest_api.go        RestAPIConnector

framework/configstore/tables/
└── connectors.go              ConnectorTable

transports/bifrost-http/
└── handlers/connectors.go     (NEW) /api/connectors/* CRUD
```

---

## 3. Data Connector Interface

```go
// core/mcp/connectors/connector.go

type DataConnector interface {
    // Type returns the connector type identifier
    Type() ConnectorType
    
    // Tools returns the MCP tool definitions exposed by this connector
    Tools() []schemas.ChatToolFunction
    
    // Execute runs a tool call and returns the result
    Execute(ctx context.Context, toolName string, args map[string]any) (*schemas.MCPToolResult, error)
    
    // Test verifies connectivity to the data source
    Test(ctx context.Context) error
    
    // Close releases connections
    Close() error
}

type ConnectorType string
const (
    ConnectorPostgreSQL    ConnectorType = "postgresql"
    ConnectorMySQL         ConnectorType = "mysql"
    ConnectorSQLite        ConnectorType = "sqlite"
    ConnectorMongoDB       ConnectorType = "mongodb"
    ConnectorElasticsearch ConnectorType = "elasticsearch"
    ConnectorRedis         ConnectorType = "redis"
    ConnectorS3            ConnectorType = "s3"
    ConnectorGoogleDrive   ConnectorType = "google_drive"
    ConnectorSlack         ConnectorType = "slack_read"
    ConnectorConfluence    ConnectorType = "confluence"
    ConnectorJira          ConnectorType = "jira"
    ConnectorRestAPI       ConnectorType = "rest_api"
)
```

---

## 4. Database Schema

```go
// framework/configstore/tables/connectors.go

type ConnectorTable struct {
    ID          string    `gorm:"primaryKey;type:text"`
    Name        string    `gorm:"uniqueIndex;not null"`
    Type        string    `gorm:"index;not null"`      // ConnectorType
    Enabled     bool      `gorm:"default:true"`
    ConfigJSON  string    `gorm:"type:text;not null"`  // connector-specific config, encrypted
    
    // Access control
    MCPGroupIDs string    `gorm:"type:text"`  // JSON: which tool groups can use this connector
    
    // Metadata
    Description string
    LastTestedAt *time.Time
    LastTestOK   bool
    CreatedAt    time.Time
    UpdatedAt    time.Time
}
```

---

## 5. SQL Connector Implementation

```go
// core/mcp/connectors/sql/postgresql.go

type PostgreSQLConfig struct {
    Host       string
    Port       int
    Database   string
    User       string
    Password   string  // or env.VAR or vault://path
    SSLMode    string  // "disable" | "require" | "verify-full"
    MaxConns   int
    
    // Query restrictions (security)
    AllowedSchemas  []string    // empty = all
    ReadOnly        bool        // if true, reject DML
    MaxRowsReturned int         // default: 1000
    QueryTimeout    time.Duration
}

type PostgreSQLConnector struct {
    config PostgreSQLConfig
    db     *sql.DB
}

func (c *PostgreSQLConnector) Tools() []schemas.ChatToolFunction {
    return []schemas.ChatToolFunction{
        {
            Type: "function",
            Function: &schemas.ChatToolFunctionDef{
                Name: "postgresql_query",
                Description: "Execute a SELECT query against the PostgreSQL database. " +
                    "Only SELECT statements are allowed. Returns results as JSON array.",
                Parameters: map[string]any{
                    "type": "object",
                    "properties": map[string]any{
                        "query": map[string]any{
                            "type":        "string",
                            "description": "The SQL SELECT query to execute",
                        },
                        "limit": map[string]any{
                            "type":        "integer",
                            "description": "Maximum number of rows to return (max 1000)",
                        },
                    },
                    "required": []string{"query"},
                },
            },
        },
        {
            Type: "function",
            Function: &schemas.ChatToolFunctionDef{
                Name:        "postgresql_schema",
                Description: "Get table schema and column information",
                Parameters: map[string]any{
                    "type": "object",
                    "properties": map[string]any{
                        "table": map[string]any{"type": "string", "description": "Table name"},
                        "schema": map[string]any{"type": "string", "description": "Schema name (default: public)"},
                    },
                },
            },
        },
    }
}

func (c *PostgreSQLConnector) Execute(ctx context.Context, toolName string, args map[string]any) (*schemas.MCPToolResult, error) {
    switch toolName {
    case "postgresql_query":
        return c.executeQuery(ctx, args)
    case "postgresql_schema":
        return c.getSchema(ctx, args)
    }
    return nil, fmt.Errorf("unknown tool: %s", toolName)
}

func (c *PostgreSQLConnector) executeQuery(ctx context.Context, args map[string]any) (*schemas.MCPToolResult, error) {
    query := args["query"].(string)
    
    // Security: validate query type
    if c.config.ReadOnly && !isSelectQuery(query) {
        return nil, fmt.Errorf("only SELECT queries are allowed")
    }
    
    // Apply schema restrictions
    if len(c.config.AllowedSchemas) > 0 && !queryUsesAllowedSchema(query, c.config.AllowedSchemas) {
        return nil, fmt.Errorf("query accesses restricted schema")
    }
    
    // Execute with timeout
    queryCtx, cancel := context.WithTimeout(ctx, c.config.QueryTimeout)
    defer cancel()
    
    limit := min(toInt(args["limit"], c.config.MaxRowsReturned), c.config.MaxRowsReturned)
    limitedQuery := fmt.Sprintf("SELECT * FROM (%s) AS _q LIMIT %d", query, limit)
    
    rows, err := c.db.QueryContext(queryCtx, limitedQuery)
    if err != nil { return nil, err }
    defer rows.Close()
    
    // Convert to JSON
    result, err := rowsToJSON(rows)
    if err != nil { return nil, err }
    
    return &schemas.MCPToolResult{
        Content: []schemas.MCPToolResultContent{{Type: "text", Text: result}},
    }, nil
}
```

---

## 6. REST API Connector

```go
// core/mcp/connectors/rest/rest_api.go

type RestAPIConfig struct {
    BaseURL     string
    Auth        RestAuthConfig
    Headers     map[string]string
    Timeout     time.Duration
    // Tool definitions configured by admin (not hardcoded)
    ToolDefs    []RestToolDef
}

type RestAuthConfig struct {
    Type        string   // "bearer" | "api_key" | "basic" | "oauth2"
    Token       string   // or env.VAR or vault://path
    HeaderName  string   // for api_key type: header name
    // OAuth2
    TokenURL    string
    ClientID    string
    ClientSecret string
}

type RestToolDef struct {
    Name        string
    Description string
    Method      string    // GET | POST | PUT | DELETE
    PathTemplate string   // "/users/{user_id}/orders"
    // Parameters mapped to path, query, body
    Parameters  []RestParam
}

type RestParam struct {
    Name        string
    In          string   // "path" | "query" | "body"
    Type        string   // "string" | "integer" | "boolean"
    Required    bool
    Description string
}

func (c *RestAPIConnector) Tools() []schemas.ChatToolFunction {
    // Dynamically generate tool definitions from RestToolDef list
    var tools []schemas.ChatToolFunction
    for _, def := range c.config.ToolDefs {
        tools = append(tools, buildToolFromDef(def))
    }
    return tools
}
```

---

## 7. Connector Registry

```go
// core/mcp/connectors/registry.go

type ConnectorRegistry struct {
    connectors map[string]DataConnector  // connectorID → DataConnector
    mu         sync.RWMutex
}

func (r *ConnectorRegistry) Register(id string, connector DataConnector) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.connectors[id] = connector
}

func (r *ConnectorRegistry) GetTool(connectorID, toolName string) (DataConnector, bool) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    c, ok := r.connectors[connectorID]
    return c, ok
}

// GetAllTools returns all tools across all registered connectors
// prefixed with connector ID to avoid name collisions
func (r *ConnectorRegistry) GetAllTools() []schemas.ChatToolFunction {
    r.mu.RLock()
    defer r.mu.RUnlock()
    
    var tools []schemas.ChatToolFunction
    for id, connector := range r.connectors {
        for _, tool := range connector.Tools() {
            // Prefix: "postgresql_query" → "{connectorID}__postgresql_query"
            tool.Function.Name = id + "__" + tool.Function.Name
            tools = append(tools, tool)
        }
    }
    return tools
}
```

---

## 8. Integration with MCP Manager

```go
// core/mcp/clientmanager.go (MODIFY)
// Register connector registry as a special internal MCP client

func (m *MCPManager) RegisterConnectors(registry *connectors.ConnectorRegistry) {
    // Connectors appear as a built-in MCP client with ID "bifrost-connectors"
    m.connectorRegistry = registry
}

// Override GetTools() to merge connector tools
func (m *MCPManager) GetTools(ctx *schemas.BifrostContext) ([]schemas.ChatToolFunction, error) {
    tools, err := m.getRegisteredClientTools(ctx)
    if err != nil { return nil, err }
    
    if m.connectorRegistry != nil {
        connectorTools := m.connectorRegistry.GetAllTools()
        tools = append(tools, connectorTools...)
    }
    return tools, nil
}
```

---

## 9. Management API

```go
// transports/bifrost-http/handlers/connectors.go

// GET    /api/connectors              — list connectors
// POST   /api/connectors              — create connector (admin+)
// GET    /api/connectors/{id}         — get connector (config with secrets masked)
// PUT    /api/connectors/{id}         — update connector (admin+)
// DELETE /api/connectors/{id}         — delete connector (admin+)
// POST   /api/connectors/{id}/test    — test connectivity
// POST   /api/connectors/{id}/enable
// POST   /api/connectors/{id}/disable
// GET    /api/connectors/{id}/tools   — list tools exposed by this connector
// GET    /api/connectors/types        — list supported connector types + config schemas
```

---

## 10. UI Components

```
ui/app/enterprise/connectors/
├── page.tsx                      — Connector list with status indicators
├── new/page.tsx                  — Connector creation wizard
├── [id]/page.tsx                 — Connector detail + tool preview
└── components/
    ├── ConnectorTypeCard.tsx     — Type selector with icon + description
    ├── ConnectorConfigForm.tsx   — Dynamic config form per connector type
    ├── ToolPreview.tsx           — Preview generated tool definitions
    └── ConnectionTestPanel.tsx  — Real-time connection test with output
```

---

## 11. Security Requirements

- All connector credentials stored encrypted in `ConnectorTable.ConfigJSON` (using `framework/encrypt` or Vault Transit)
- SQL connectors: `ReadOnly=true` by default; DML requires explicit admin opt-in + audit log
- REST connectors: response size limit (default: 1MB) to prevent memory exhaustion
- Request timeout enforced on all connector calls (default: 30s)
- Connector execution is audit-logged (TECH-002) with tool name and connector ID
