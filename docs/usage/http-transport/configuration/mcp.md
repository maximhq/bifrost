# 🛠️ MCP Configuration

Complete guide to configuring Model Context Protocol (MCP) integration in Bifrost HTTP transport for external tool execution.

> **💡 Quick Start:** See the [30-second setup](../../../quickstart/http-transport.md) for basic MCP configuration.

---

## 📋 MCP Overview (File Based)

> You can directly use the UI (`http://localhost:{port}/mcp-clients`) to configure the MCP clients.

MCP (Model Context Protocol) configuration enables:

- **External tool integration** (filesystem, web scraping, databases)
- **STDIO, HTTP, and SSE connections** to MCP servers
- **Tool filtering** and access control
- **HTTP endpoint** for manual tool execution (`/v1/mcp/tool/execute`)

```json
{
  "mcp": {
    "client_configs": [
      {
        "name": "filesystem",
        "connection_type": "stdio",
        "stdio_config": {
          "command": "npx",
          "args": ["-y", "@modelcontextprotocol/server-filesystem"]
        }
      }
    ]
  }
}
```

---

## 🔌 Connection Types

### **STDIO Connection**

Most common for local MCP servers:

```json
{
  "mcp": {
    "client_configs": [
      {
        "name": "filesystem-tools",
        "connection_type": "stdio",
        "stdio_config": {
          "command": "npx",
          "args": ["-y", "@modelcontextprotocol/server-filesystem"],
          "envs": ["HOME", "USER"]
        },
        "tools_to_execute": ["read_file", "list_directory"],
        "tools_to_skip": ["delete_file"]
      }
    ]
  }
}
```

### **HTTP Connection**

For remote MCP servers:

```json
{
  "mcp": {
    "client_configs": [
      {
        "name": "remote-api",
        "connection_type": "http",
        "connection_string": "env.MCP_CONNECTION_STRING"
      }
    ]
  }
}
```

> **🔒 Security:** Use `env.PREFIX` for secure connection strings: `"connection_string": "env.MCP_CONNECTION_STRING"`

### **SSE Connection**

For server-sent events:

```json
{
  "mcp": {
    "client_configs": [
      {
        "name": "realtime-data",
        "connection_type": "sse",
        "connection_string": "env.MCP_SSE_CONNECTION_STRING"
      }
    ]
  }
}
```

---

## 🛠️ Popular MCP Servers

### **Filesystem Tools**

```json
{
  "name": "filesystem",
  "connection_type": "stdio",
  "stdio_config": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-filesystem"],
    "envs": ["HOME"]
  },
  "tools_to_execute": ["read_file", "list_directory", "write_file"]
}
```

### **Web Search**

```json
{
  "name": "web-search",
  "connection_type": "stdio",
  "stdio_config": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-web-search"],
    "envs": ["SEARCH_API_KEY"]
  }
}
```

### **Database Access**

```json
{
  "name": "database",
  "connection_type": "stdio",
  "stdio_config": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-postgres"],
    "envs": ["DATABASE_URL"]
  },
  "tools_to_execute": ["query", "schema"]
}
```

### **Git Integration**

```json
{
  "name": "git-tools",
  "connection_type": "stdio",
  "stdio_config": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-git"],
    "envs": ["GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL"]
  }
}
```

---

## 🔒 Tool Filtering

### **Whitelist Approach**

Only allow specific tools:

```json
{
  "name": "safe-filesystem",
  "connection_type": "stdio",
  "stdio_config": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-filesystem"]
  },
  "tools_to_execute": ["read_file", "list_directory"]
}
```

### **Blacklist Approach**

Allow all tools except dangerous ones:

```json
{
  "name": "web-tools",
  "connection_type": "stdio",
  "stdio_config": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-web"]
  },
  "tools_to_skip": ["delete_page", "modify_content", "admin_access"]
}
```

---

## 🌐 Using MCP Tools via HTTP

### **Automatic Tool Integration**

Tools are automatically available in chat completions:

```bash
# Make a request - MCP tools are automatically added
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "List the files in the current directory"}
    ]
  }'

# Response includes tool calls
# {
#   "choices": [{
#     "message": {
#       "tool_calls": [{
#         "id": "call_123",
#         "type": "function",
#         "function": {
#           "name": "list_directory",
#           "arguments": "{\"path\": \".\"}"
#         }
#       }]
#     }
#   }]
# }
```

### **Manual Tool Execution**

Execute tools directly via HTTP endpoint:

```bash
curl -X POST http://localhost:8080/v1/mcp/tool/execute \
  -H "Content-Type: application/json" \
  -d '{
    "id": "call_123",
    "type": "function",
    "function": {
      "name": "read_file",
      "arguments": "{\"path\": \"config.json\"}"
    }
  }'

# Response
# {
#   "role": "tool",
#   "content": {
#     "content_str": "{\n  \"providers\": {\n    ...\n  }\n}"
#   },
#   "tool_call_id": "call_123"
# }
```

### **Multi-Turn Conversations with MCP Tools**

When MCP is configured, Bifrost automatically adds available tools to requests. Here's an example of a multi-turn conversation where the AI uses tools:

**Initial Request (AI decides to use a tool):**

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "Can you list the files in the /tmp directory?"}
    ]
  }'
```

**Response includes tool calls:**

```json
{
  "data": {
    "choices": [
      {
        "message": {
          "role": "assistant",
          "content": null,
          "tool_calls": [
            {
              "id": "call_abc123",
              "type": "function",
              "function": {
                "name": "list_directory",
                "arguments": "{\"path\": \"/tmp\"}"
              }
            }
          ]
        }
      }
    ]
  }
}
```

**Execute Tool (Use Bifrost's MCP tool execution endpoint):**

**💡 Note:** The request body of this endpoint is a tool call block you received from `/v1/chat/completions` route - **you can directly copy and paste the tool call block as the request body**.

```bash
curl -X POST http://localhost:8080/v1/mcp/tool/execute \
  -H "Content-Type: application/json" \
  -d '{
      "id": "call_abc123",
      "type": "function",
      "function": {
        "name": "list_directory",
        "arguments": "{\"path\": \"/tmp\"}"
      }
  }'
```

**Response with tool result:**

```json
{
  "role": "tool",
  "content": "config.json\nreadme.txt\ndata.csv",
  "tool_call_id": "call_abc123"
}
```

**Continue Conversation (Add tool result and get final response):**

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "Can you list the files in the /tmp directory?"},
      {
        "role": "assistant",
        "content": null,
        "tool_calls": [{
          "id": "call_abc123",
          "type": "function",
          "function": {
            "name": "list_directory",
            "arguments": "{\"path\": \"/tmp\"}"
          }
        }]
      },
      {
        "role": "tool",
        "content": "config.json\nreadme.txt\ndata.csv",
        "tool_call_id": "call_abc123"
      }
    ]
  }'
```

**Final response:**

```json
{
  "data": {
    "choices": [
      {
        "message": {
          "role": "assistant",
          "content": "I found 3 files in the /tmp directory:\n1. config.json\n2. readme.txt\n3. data.csv\n\nWould you like me to read the contents of any of these files?"
        }
      }
    ]
  }
}
```

**Tool Execution Flow Summary:**

1. Send chat completion request → AI responds with tool_calls
2. Send tool_calls to `/v1/mcp/tool/execute` → Get tool_result message
3. Append tool_result to conversation → Send back for final response

### **Request-Level Tool Filtering**

Control which MCP tools are available per request using context:

```bash
# Include only specific MCP clients
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "List files and search web"}
    ],
    "mcp-include-clients": ["filesystem"],
    "mcp-exclude-clients": ["web-search", "database"]
  }'

# Include specific tools only
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "Help me with file operations"}
    ],
    "mcp-include-tools": ["read_file", "list_directory"],
    "mcp-exclude-tools": ["delete_file", "write_file"]
  }'
```

---

## 🌐 MCP Management API Endpoints

Bifrost HTTP transport provides REST API endpoints for dynamic MCP client management.

### **GET /api/mcp/clients - List All MCP Clients**

Get information about all configured MCP clients:

```bash
curl -X GET http://localhost:8080/api/mcp/clients

# Response
[
  {
    "name": "filesystem",
    "config": {
      "name": "filesystem",
      "connection_type": "stdio",
      "stdio_config": {
        "command": "npx",
        "args": ["-y", "@modelcontextprotocol/server-filesystem"]
      },
      "tools_to_execute": ["read_file", "list_directory"]
    },
    "tools": ["read_file", "list_directory", "write_file"],
    "state": "connected"
  },
  {
    "name": "web-search",
    "config": {
      "name": "web-search", 
      "connection_type": "stdio",
      "stdio_config": {
        "command": "npx",
        "args": ["-y", "@modelcontextprotocol/server-web-search"]
      }
    },
    "tools": [],
    "state": "error"
  }
]
```

### **POST /api/mcp/client - Add New MCP Client**

Add a new MCP client at runtime:

```bash
# Add STDIO client
curl -X POST http://localhost:8080/api/mcp/client \
  -H "Content-Type: application/json" \
  -d '{
    "name": "git-tools",
    "connection_type": "stdio",
    "stdio_config": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-git"],
      "envs": ["GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL"]
    },
    "tools_to_execute": ["git_log", "git_status"]
  }'

# Add HTTP client
curl -X POST http://localhost:8080/api/mcp/client \
  -H "Content-Type: application/json" \
  -d '{
    "name": "remote-api",
    "connection_type": "http",
    "connection_string": "https://api.example.com/mcp"
  }'

# Add SSE client
curl -X POST http://localhost:8080/api/mcp/client \
  -H "Content-Type: application/json" \
  -d '{
    "name": "realtime-data",
    "connection_type": "sse", 
    "connection_string": "https://api.example.com/mcp/sse"
  }'

# Success Response
{
  "status": "success",
  "message": "MCP client added successfully"
}
```

### **PUT /api/mcp/client/{name} - Edit Client Tools**

Modify which tools are available from a client:

```bash
# Allow only specific tools (whitelist)
curl -X PUT http://localhost:8080/api/mcp/client/filesystem \
  -H "Content-Type: application/json" \
  -d '{
    "tools_to_execute": ["read_file", "list_directory"]
  }'

# Block specific tools (blacklist)
curl -X PUT http://localhost:8080/api/mcp/client/filesystem \
  -H "Content-Type: application/json" \
  -d '{
    "tools_to_skip": ["delete_file", "write_file"]
  }'

# Clear all restrictions (allow all tools)
curl -X PUT http://localhost:8080/api/mcp/client/filesystem \
  -H "Content-Type: application/json" \
  -d '{
    "tools_to_execute": [],
    "tools_to_skip": []
  }'

# Success Response
{
  "status": "success", 
  "message": "MCP client tools edited successfully"
}
```

### **DELETE /api/mcp/client/{name} - Remove MCP Client**

Remove an MCP client and disconnect it:

```bash
curl -X DELETE http://localhost:8080/api/mcp/client/filesystem

# Success Response
{
  "status": "success",
  "message": "MCP client removed successfully"
}
```

### **POST /api/mcp/client/{name}/reconnect - Reconnect Client**

Reconnect a disconnected or errored MCP client:

```bash
curl -X POST http://localhost:8080/api/mcp/client/filesystem/reconnect

# Success Response
{
  "status": "success",
  "message": "MCP client reconnected successfully"
}
```

### **POST /v1/mcp/tool/execute - Execute MCP Tool**

Execute an MCP tool directly (see detailed examples above):

```bash
curl -X POST http://localhost:8080/v1/mcp/tool/execute \
  -H "Content-Type: application/json" \
  -d '{
    "id": "call_123",
    "type": "function", 
    "function": {
      "name": "read_file",
      "arguments": "{\"path\": \"config.json\"}"
    }
  }'

# Response - Tool execution result
{
  "role": "tool",
  "content": "{\n  \"providers\": {\n    ...\n  }\n}",
  "tool_call_id": "call_123"
}
```

### **Error Responses**

All endpoints return consistent error responses:

```json
// 400 Bad Request
{
  "error": {
    "message": "Invalid request format: missing required field 'name'",
    "type": "invalid_request"
  }
}

// 500 Internal Server Error  
{
  "error": {
    "message": "Failed to connect to MCP client: connection timeout",
    "type": "internal_error"
  }
}
```

### **Management Workflow Example**

Complete client lifecycle management via API:

```bash
# 1. Add a new client
curl -X POST http://localhost:8080/api/mcp/client \
  -H "Content-Type: application/json" \
  -d '{
    "name": "temp-filesystem",
    "connection_type": "stdio",
    "stdio_config": {
      "command": "npx", 
      "args": ["-y", "@modelcontextprotocol/server-filesystem"]
    }
  }'

# 2. Check client status
curl -X GET http://localhost:8080/api/mcp/clients | jq '.[] | select(.name=="temp-filesystem")'

# 3. Restrict to safe tools only
curl -X PUT http://localhost:8080/api/mcp/client/temp-filesystem \
  -H "Content-Type: application/json" \
  -d '{
    "tools_to_execute": ["read_file", "list_directory"]
  }'

# 4. Test tool execution
curl -X POST http://localhost:8080/v1/mcp/tool/execute \
  -H "Content-Type: application/json" \
  -d '{
    "id": "test_call",
    "type": "function",
    "function": {
      "name": "list_directory", 
      "arguments": "{\"path\": \".\"}"
    }
  }'

# 5. Remove when done
curl -X DELETE http://localhost:8080/api/mcp/client/temp-filesystem
```

---

## 🔧 Environment Variables

### **Required Variables for MCP Servers**

```bash
# Filesystem tools
export HOME="/home/user"

# Web search
export SEARCH_API_KEY="your-search-api-key"

# Database
export DATABASE_URL="postgresql://user:pass@localhost/db"

# Git tools
export GIT_AUTHOR_NAME="Your Name"
export GIT_AUTHOR_EMAIL="you@example.com"

# Custom MCP servers
export YOUR_MCP_SERVER_API_KEY="your-key"
```

### **Docker with MCP**

> **⚠️ Important:** Docker currently does **NOT** support STDIO connection for MCP. Use Go binary if STDIO connection is required.

```bash
# For HTTP/SSE MCP connections only
docker run -p 8080:8080 \
  -v $(pwd)/config.json:/app/config/config.json \
  -e OPENAI_API_KEY \
  -e SEARCH_API_KEY \
  -e MCP_CONNECTION_STRING \
  -e MCP_SSE_CONNECTION_STRING \
  -e APP_PLUGINS=maxim \
  maximhq/bifrost
```

### **Go Binary with MCP (Supports all connection types)**

```bash
# All environment variables are picked up automatically
export OPENAI_API_KEY="your-openai-key"
export SEARCH_API_KEY="your-search-key"

go install github.com/maximhq/bifrost/transports/bifrost-http@latest
bifrost-http -config config.json -port 8080 -plugins maxim
```

---

## 🧪 Testing MCP Integration

### **Verify MCP Tools Are Available**

```bash
# Make a request that should use tools
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "What files are in this directory?"}
    ]
  }'
```

### **Test Manual Tool Execution**

```bash
# Test filesystem tool
curl -X POST http://localhost:8080/v1/mcp/tool/execute \
  -H "Content-Type: application/json" \
  -d '{
    "id": "test_call",
    "type": "function",
    "function": {
      "name": "list_directory",
      "arguments": "{\"path\": \".\"}"
    }
  }'
```

### **Check Server Logs**

```bash
# Look for MCP connection logs
docker logs bifrost-container | grep MCP

# Expected output:
# [Bifrost MCP] MCP Manager initialized
# [Bifrost MCP] Connected to MCP client: filesystem
```

---

## 🔄 Multi-Tool Workflow Example

### **Complete Configuration**

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o-mini"],
          "weight": 1.0
        }
      ]
    }
  },
  "mcp": {
    "client_configs": [
      {
        "name": "filesystem",
        "connection_type": "stdio",
        "stdio_config": {
          "command": "npx",
          "args": ["-y", "@modelcontextprotocol/server-filesystem"]
        }
      },
      {
        "name": "web-search",
        "connection_type": "stdio",
        "stdio_config": {
          "command": "npx",
          "args": ["-y", "@modelcontextprotocol/server-web-search"],
          "envs": ["SEARCH_API_KEY"]
        }
      }
    ]
  }
}
```

### **Complex Request**

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [
      {
        "role": "user",
        "content": "Search the web for the latest Node.js version, then create a package.json file with that version"
      }
    ]
  }'
```

---

## 📚 Related Documentation

- **[🌐 HTTP Transport Overview](../README.md)** - Main HTTP transport guide
- **[🔧 Provider Configuration](./providers.md)** - Configure AI providers
- **[🌐 Endpoints](../endpoints.md)** - HTTP API endpoints
- **[🛠️ Go Package MCP](../../go-package/mcp.md)** - MCP usage in Go package

> **🏛️ Architecture:** For MCP system design and performance details, see [Architecture Documentation](../../../architecture/README.md).
