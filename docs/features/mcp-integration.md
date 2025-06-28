# 🛠️ MCP Integration

Add external tools and function calling to your AI models with Bifrost's Model Context Protocol (MCP) integration. Connect to external services, run local tools, and enable AI models to perform actions beyond text generation.

## 🎯 Overview

Bifrost's MCP integration provides **seamless tool connectivity** for AI models:

- ✅ **Local Tool Registration** - Register Go functions as MCP tools
- ✅ **External Server Connection** - Connect to HTTP, STDIO, and SSE MCP servers
- ✅ **Automatic Tool Discovery** - Auto-discover tools from connected servers
- ✅ **Multi-Turn Conversations** - Handle tool calls in chat sequences
- ✅ **Tool Filtering** - Control which tools are available per request

---

## ⚡ Quick Start

<details>
<summary><strong>🔧 Go Package Usage</strong></summary>

```go
// 1. Setup MCP configuration
mcpConfig := &schemas.MCPConfig{
    ClientConfigs: []schemas.MCPClientConfig{
        {
            Name:           "filesystem",
            ConnectionType: schemas.MCPConnectionTypeSTDIO,
            StdioConfig: &schemas.MCPStdioConfig{
                Command: "npx",
                Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
            },
        },
    },
}

// 2. Initialize Bifrost with MCP
client, err := bifrost.Init(schemas.BifrostConfig{
    Account:   &myAccount,
    MCPConfig: mcpConfig,
})
if err != nil {
    panic(err)
}
defer client.Cleanup()

// 3. Register a local tool
err = client.RegisterMCPTool("calculate", "Perform calculations",
    func(args map[string]interface{}) (string, error) {
        expression := args["expression"].(string)
        result := evaluate(expression) // Your calculation logic
        return fmt.Sprintf("Result: %v", result), nil
    },
    schemas.Tool{
        Type: "function",
        Function: schemas.Function{
            Name: "calculate",
            Description: "Evaluate mathematical expressions",
            Parameters: schemas.FunctionParameters{
                Type: "object",
                Properties: map[string]interface{}{
                    "expression": map[string]interface{}{
                        "type": "string",
                        "description": "Mathematical expression to evaluate",
                    },
                },
                Required: []string{"expression"},
            },
        },
    })

// 4. Use tools in conversation
response, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input: schemas.RequestInput{
        ChatCompletionInput: &[]schemas.BifrostMessage{{
            Role: schemas.ModelChatMessageRoleUser,
            Content: schemas.MessageContent{
                ContentStr: &[]string{"Calculate 15 * 23 and list files in /tmp"}[0],
            },
        }},
    },
})
```

</details>

<details>
<summary><strong>🌐 HTTP Transport Usage</strong></summary>

**1. Configure MCP in config.json:**

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
          "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
        }
      },
      {
        "name": "web-search",
        "connection_type": "http",
        "connection_string": "http://localhost:3001/mcp"
      }
    ]
  }
}
```

**2. Start Bifrost with MCP:**

```bash
docker run -p 8080:8080 \
  -v $(pwd)/config.json:/app/config/config.json \
  -e OPENAI_API_KEY \
  maximhq/bifrost
```

**3. Chat with automatic tool discovery:**

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "List files in /tmp and search for recent AI news"}
    ]
  }'
```

**4. Execute tool calls:**

```bash
# If AI responds with tool_calls, execute them:
curl -X POST http://localhost:8080/v1/mcp/tool/execute \
  -H "Content-Type: application/json" \
  -d '{
    "id": "call_abc123",
    "type": "function",
    "function": {
      "name": "list_files",
      "arguments": "{\"path\": \"/tmp\"}"
    }
  }'
```

</details>

---

## 🔧 Connection Types

### STDIO Connections

Connect to command-line MCP tools:

<details>
<summary><strong>🔧 Go Package - STDIO Configuration</strong></summary>

```go
mcpConfig := &schemas.MCPConfig{
    ClientConfigs: []schemas.MCPClientConfig{
        {
            Name:           "filesystem",
            ConnectionType: schemas.MCPConnectionTypeSTDIO,
            StdioConfig: &schemas.MCPStdioConfig{
                Command: "npx",
                Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
                Envs:    []string{"NODE_ENV", "FILESYSTEM_ROOT"},
            },
            ToolsToSkip: []string{"rm", "delete"}, // Skip dangerous operations
        },
        {
            Name:           "weather",
            ConnectionType: schemas.MCPConnectionTypeSTDIO,
            StdioConfig: &schemas.MCPStdioConfig{
                Command: "python",
                Args:    []string{"weather_server.py"},
                Envs:    []string{"WEATHER_API_KEY"},
            },
        },
    },
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - STDIO Configuration</strong></summary>

```json
{
  "mcp": {
    "client_configs": [
      {
        "name": "filesystem",
        "connection_type": "stdio",
        "stdio_config": {
          "command": "npx",
          "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
          "envs": ["NODE_ENV", "FILESYSTEM_ROOT"]
        },
        "tools_to_skip": ["rm", "delete"]
      },
      {
        "name": "weather",
        "connection_type": "stdio",
        "stdio_config": {
          "command": "python",
          "args": ["weather_server.py"],
          "envs": ["WEATHER_API_KEY"]
        }
      }
    ]
  }
}
```

</details>

### HTTP Connections

Connect to web-based MCP services:

<details>
<summary><strong>🔧 Go Package - HTTP Configuration</strong></summary>

```go
mcpConfig := &schemas.MCPConfig{
    ClientConfigs: []schemas.MCPClientConfig{
        {
            Name:             "web-search",
            ConnectionType:   schemas.MCPConnectionTypeHTTP,
            ConnectionString: &[]string{"http://localhost:3001/mcp"}[0],
            ToolsToExecute:   []string{"search_web", "get_url"}, // Only use these tools
        },
        {
            Name:             "database",
            ConnectionType:   schemas.MCPConnectionTypeHTTP,
            ConnectionString: &[]string{"https://db-service.company.com/mcp"}[0],
            ToolsToSkip:      []string{"delete_all"}, // Skip dangerous operations
        },
    },
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - HTTP Configuration</strong></summary>

```json
{
  "mcp": {
    "client_configs": [
      {
        "name": "web-search",
        "connection_type": "http",
        "connection_string": "http://localhost:3001/mcp",
        "tools_to_execute": ["search_web", "get_url"]
      },
      {
        "name": "database",
        "connection_type": "http",
        "connection_string": "https://db-service.company.com/mcp",
        "tools_to_skip": ["delete_all"]
      }
    ]
  }
}
```

</details>

### SSE Connections

Connect to Server-Sent Events MCP streams:

<details>
<summary><strong>🔧 Go Package - SSE Configuration</strong></summary>

```go
mcpConfig := &schemas.MCPConfig{
    ClientConfigs: []schemas.MCPClientConfig{
        {
            Name:             "real-time-data",
            ConnectionType:   schemas.MCPConnectionTypeSSE,
            ConnectionString: &[]string{"http://localhost:3002/sse"}[0],
        },
        {
            Name:             "live-metrics",
            ConnectionType:   schemas.MCPConnectionTypeSSE,
            ConnectionString: &[]string{"https://metrics.company.com/mcp/stream"}[0],
        },
    },
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - SSE Configuration</strong></summary>

```json
{
  "mcp": {
    "client_configs": [
      {
        "name": "real-time-data",
        "connection_type": "sse",
        "connection_string": "http://localhost:3002/sse"
      },
      {
        "name": "live-metrics",
        "connection_type": "sse",
        "connection_string": "https://metrics.company.com/mcp/stream"
      }
    ]
  }
}
```

</details>

---

## 🔨 Local Tool Registration

### Simple Function Tools

<details>
<summary><strong>🔧 Go Package - Register Local Tools</strong></summary>

```go
// Math calculator tool
err := client.RegisterMCPTool("calculate", "Perform mathematical calculations",
    func(args map[string]interface{}) (string, error) {
        expression, ok := args["expression"].(string)
        if !ok {
            return "", fmt.Errorf("expression parameter required")
        }

        result, err := evaluate(expression) // Your calculation logic
        if err != nil {
            return "", fmt.Errorf("calculation error: %v", err)
        }

        return fmt.Sprintf("Result: %v", result), nil
    },
    schemas.Tool{
        Type: "function",
        Function: schemas.Function{
            Name:        "calculate",
            Description: "Evaluate mathematical expressions",
            Parameters: schemas.FunctionParameters{
                Type: "object",
                Properties: map[string]interface{}{
                    "expression": map[string]interface{}{
                        "type":        "string",
                        "description": "Mathematical expression to evaluate (e.g., '2+2', 'sqrt(16)')",
                    },
                },
                Required: []string{"expression"},
            },
        },
    })

// Database query tool
err = client.RegisterMCPTool("query_database", "Query the company database",
    func(args map[string]interface{}) (string, error) {
        query := args["query"].(string)
        table := args["table"].(string)

        results, err := db.Query(query, table) // Your DB logic
        if err != nil {
            return "", err
        }

        return fmt.Sprintf("Query results: %v", results), nil
    },
    schemas.Tool{
        Type: "function",
        Function: schemas.Function{
            Name:        "query_database",
            Description: "Execute safe database queries",
            Parameters: schemas.FunctionParameters{
                Type: "object",
                Properties: map[string]interface{}{
                    "query": map[string]interface{}{
                        "type":        "string",
                        "description": "SQL SELECT query to execute",
                    },
                    "table": map[string]interface{}{
                        "type":        "string",
                        "description": "Table name to query",
                    },
                },
                Required: []string{"query", "table"},
            },
        },
    })
```

</details>

---

## 💬 Multi-Turn Conversations

### Handling Tool Calls

<details>
<summary><strong>🔧 Go Package - Complete Tool Conversation</strong></summary>

```go
func handleToolConversation(client *bifrost.Bifrost) {
    messages := []schemas.BifrostMessage{
        {
            Role: schemas.ModelChatMessageRoleUser,
            Content: schemas.MessageContent{
                ContentStr: &[]string{"What's the weather in San Francisco and calculate 15 * 23?"}[0],
            },
        },
    }

    for {
        // Send current conversation
        response, err := client.Request(context.Background(), &schemas.BifrostRequest{
            Provider: schemas.OpenAI,
            Model:    "gpt-4o-mini",
            Input: schemas.RequestInput{
                ChatCompletionInput: &messages,
            },
        })
        if err != nil {
            log.Printf("Request failed: %v", err)
            return
        }

        // Add assistant's response to conversation
        messages = append(messages, response.Choices[0].Message)

        // Check if the model wants to use tools
        if len(response.Choices[0].Message.ToolCalls) > 0 {
            // Execute each tool call
            for _, toolCall := range response.Choices[0].Message.ToolCalls {
                result, err := client.ExecuteMCPTool(context.Background(), toolCall)
                if err != nil {
                    log.Printf("Tool execution failed: %v", err)
                    continue
                }

                // Add tool result to conversation
                messages = append(messages, schemas.BifrostMessage{
                    Role: schemas.ModelChatMessageRoleTool,
                    Content: schemas.MessageContent{
                        ContentStr: &result,
                    },
                    ToolCallID: &toolCall.ID,
                })
            }
            // Continue the loop to get the final response
        } else {
            // No more tool calls, conversation complete
            fmt.Printf("Final response: %s\n", *response.Choices[0].Message.Content.ContentStr)
            break
        }
    }
}
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Multi-Turn Tool Usage</strong></summary>

**Step 1: Initial request**

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "What files are in /tmp and what is 15 * 23?"}
    ]
  }'
```

**Response with tool calls:**

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": null,
        "tool_calls": [
          {
            "id": "call_1",
            "type": "function",
            "function": {
              "name": "list_files",
              "arguments": "{\"path\": \"/tmp\"}"
            }
          },
          {
            "id": "call_2",
            "type": "function",
            "function": {
              "name": "calculate",
              "arguments": "{\"expression\": \"15 * 23\"}"
            }
          }
        ]
      }
    }
  ]
}
```

**Step 2: Execute tools**

```bash
# Execute first tool
curl -X POST http://localhost:8080/v1/mcp/tool/execute \
  -H "Content-Type: application/json" \
  -d '{
    "id": "call_1",
    "type": "function",
    "function": {"name": "list_files", "arguments": "{\"path\": \"/tmp\"}"}
  }'

# Execute second tool
curl -X POST http://localhost:8080/v1/mcp/tool/execute \
  -H "Content-Type: application/json" \
  -d '{
    "id": "call_2",
    "type": "function",
    "function": {"name": "calculate", "arguments": "{\"expression\": \"15 * 23\"}"}
  }'
```

**Step 3: Continue conversation with tool results**

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "What files are in /tmp and what is 15 * 23?"},
      {
        "role": "assistant",
        "content": null,
        "tool_calls": [
          {"id": "call_1", "type": "function", "function": {"name": "list_files", "arguments": "{\"path\": \"/tmp\"}"}},
          {"id": "call_2", "type": "function", "function": {"name": "calculate", "arguments": "{\"expression\": \"15 * 23\"}"}}
        ]
      },
      {"role": "tool", "content": "config.json\nreadme.txt\ndata.csv", "tool_call_id": "call_1"},
      {"role": "tool", "content": "Result: 345", "tool_call_id": "call_2"}
    ]
  }'
```

</details>

---

## 🎛️ Tool Filtering

### Control Available Tools

<details>
<summary><strong>🔧 Go Package - Dynamic Tool Filtering</strong></summary>

```go
// Configure tools per client
mcpConfig := &schemas.MCPConfig{
    ClientConfigs: []schemas.MCPClientConfig{
        {
            Name:           "filesystem",
            ConnectionType: schemas.MCPConnectionTypeSTDIO,
            StdioConfig: &schemas.MCPStdioConfig{
                Command: "npx",
                Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
            },
            // Only allow safe file operations
            ToolsToExecute: []string{"list_files", "read_file"},
            ToolsToSkip:    []string{"delete_file", "write_file"},
        },
        {
            Name:           "calculator",
            ConnectionType: schemas.MCPConnectionTypeHTTP,
            ConnectionString: &[]string{"http://localhost:3001/calc"}[0],
            // Allow all calculator tools (no filtering)
        },
    },
}

// Filter tools per request
response, err := client.RequestWithToolFilter(context.Background(),
    &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "gpt-4o-mini",
        Input:    input,
    },
    []string{"list_files", "calculate"}, // Only these tools available for this request
)
```

</details>

<details>
<summary><strong>🌐 HTTP Transport - Configuration-Based Filtering</strong></summary>

```json
{
  "mcp": {
    "client_configs": [
      {
        "name": "filesystem",
        "connection_type": "stdio",
        "stdio_config": {
          "command": "npx",
          "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
        },
        "tools_to_execute": ["list_files", "read_file"],
        "tools_to_skip": ["delete_file", "write_file"]
      },
      {
        "name": "calculator",
        "connection_type": "http",
        "connection_string": "http://localhost:3001/calc"
      }
    ]
  }
}
```

</details>

---

## 🔍 Available MCP Tools

### Popular MCP Servers

| Server         | Connection | Tools                                   | Use Case         |
| -------------- | ---------- | --------------------------------------- | ---------------- |
| **Filesystem** | STDIO      | `list_files`, `read_file`, `write_file` | File operations  |
| **Git**        | STDIO      | `git_log`, `git_diff`, `git_commit`     | Git operations   |
| **Web Search** | HTTP       | `search_web`, `get_url`                 | Web searches     |
| **Database**   | HTTP       | `query`, `schema`                       | Database queries |
| **Weather**    | HTTP/SSE   | `current_weather`, `forecast`           | Weather data     |
| **Email**      | HTTP       | `send_email`, `read_inbox`              | Email operations |

### Installation Examples

<details>
<summary><strong>Filesystem Server</strong></summary>

```bash
# Install and configure filesystem MCP server
npm install -g @modelcontextprotocol/server-filesystem

# In your config:
{
  "name": "filesystem",
  "connection_type": "stdio",
  "stdio_config": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-filesystem", "/workspace"]
  }
}
```

</details>

<details>
<summary><strong>Git Server</strong></summary>

```bash
# Install git MCP server
npm install -g @modelcontextprotocol/server-git

# In your config:
{
  "name": "git",
  "connection_type": "stdio",
  "stdio_config": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-git", "/path/to/repo"]
  }
}
```

</details>

---

## 🚨 Error Handling

### Tool Execution Errors

<details>
<summary><strong>🔧 Go Package - Error Handling</strong></summary>

```go
response, err := client.Request(context.Background(), &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input:    input,
})

if err != nil {
    if bifrostErr, ok := err.(*schemas.BifrostError); ok {
        switch bifrostErr.ErrorType {
        case schemas.BifrostErrorTypeMCPToolNotFound:
            log.Printf("Tool not found: %s", bifrostErr.Message)
        case schemas.BifrostErrorTypeMCPConnectionFailed:
            log.Printf("MCP connection failed: %s", bifrostErr.Message)
        case schemas.BifrostErrorTypeMCPToolExecutionFailed:
            log.Printf("Tool execution failed: %s", bifrostErr.Message)
        }
    }
    return
}

// Handle tool calls with error recovery
for _, toolCall := range response.Choices[0].Message.ToolCalls {
    result, err := client.ExecuteMCPTool(context.Background(), toolCall)
    if err != nil {
        // Create error message for the AI
        errorMessage := fmt.Sprintf("Tool execution failed: %v", err)
        messages = append(messages, schemas.BifrostMessage{
            Role: schemas.ModelChatMessageRoleTool,
            Content: schemas.MessageContent{
                ContentStr: &errorMessage,
            },
            ToolCallID: &toolCall.ID,
        })
        continue
    }

    // Success - add result
    messages = append(messages, schemas.BifrostMessage{
        Role: schemas.ModelChatMessageRoleTool,
        Content: schemas.MessageContent{
            ContentStr: &result,
        },
        ToolCallID: &toolCall.ID,
    })
}
```

</details>

---

## 📚 Learn More

| Topic                  | Link                                                 | Description                          |
| ---------------------- | ---------------------------------------------------- | ------------------------------------ |
| **Tool Development**   | [MCP Protocol Docs](https://modelcontextprotocol.io) | Official MCP documentation           |
| **Function Calling**   | [Provider Guides](providers/README.md)               | Function calling support by provider |
| **Error Handling**     | [Error Reference](../usage/errors.md)                | MCP-specific error types             |
| **Plugin Integration** | [Plugin System](plugins.md)                          | Extend MCP with plugins              |

---

**🎯 Next Step:** Start with [simple local tools](https://modelcontextprotocol.io) and gradually add external MCP servers as your needs grow!
