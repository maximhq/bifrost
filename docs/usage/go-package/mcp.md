# 🛠️ MCP Integration

Complete guide to using Model Context Protocol (MCP) integration for tool calling, external API connections, and custom tool registration in Bifrost.

> **💡 Quick Start:** See the [30-second setup](../../quickstart/go-package.md) for basic MCP configuration.

---

## 📋 MCP Overview

MCP (Model Context Protocol) enables AI models to interact with external tools and services. Bifrost's MCP integration provides:

- **Automatic tool discovery** from external MCP servers
- **Built-in tool execution** with proper error handling
- **Custom tool registration** for in-process tools
- **Multiple connection types** (HTTP, STDIO, SSE)

```go
// Configure MCP during initialization
client, initErr := bifrost.Init(schemas.BifrostConfig{
    Account: &MyAccount{},
    MCPConfig: &schemas.MCPConfig{
        ClientConfigs: []schemas.MCPClientConfig{
            {
                Name:           "filesystem-tools",
                ConnectionType: schemas.MCPConnectionTypeSTDIO,
                StdioConfig: &schemas.MCPStdioConfig{
                    Command: "npx",
                    Args:    []string{"-y", "@modelcontextprotocol/server-filesystem"},
                },
            },
        },
    },
})
```

---

## 🚀 Basic MCP Configuration

### **STDIO Connection (Most Common)**

Connect to MCP servers via standard input/output:

```go
func setupMCPClient() *schemas.MCPConfig {
    return &schemas.MCPConfig{
        ClientConfigs: []schemas.MCPClientConfig{
            {
                Name:           "filesystem-tools",
                ConnectionType: schemas.MCPConnectionTypeSTDIO,
                StdioConfig: &schemas.MCPStdioConfig{
                    Command: "npx",
                    Args:    []string{"-y", "@modelcontextprotocol/server-filesystem"},
                    Envs:    []string{"FILESYSTEM_ROOT"},
                },
            },
            {
                Name:           "web-search",
                ConnectionType: schemas.MCPConnectionTypeSTDIO,
                StdioConfig: &schemas.MCPStdioConfig{
                    Command: "python",
                    Args:    []string{"-m", "web_search_mcp"},
                    Envs:    []string{"SEARCH_API_KEY"},
                },
            },
        },
    }
}

// Set environment variables
os.Setenv("FILESYSTEM_ROOT", "/safe/directory")
os.Setenv("SEARCH_API_KEY", "your-search-api-key")

client, initErr := bifrost.Init(schemas.BifrostConfig{
    Account:   &MyAccount{},
    MCPConfig: setupMCPClient(),
})
```

### **HTTP Connection**

Connect to MCP servers via HTTP:

```go
func setupHTTPMCP() *schemas.MCPConfig {
    endpoint := "http://localhost:8080/mcp"

    return &schemas.MCPConfig{
        ClientConfigs: []schemas.MCPClientConfig{
            {
                Name:             "database-tools",
                ConnectionType:   schemas.MCPConnectionTypeHTTP,
                ConnectionString: &endpoint,
            },
        },
    }
}
```

### **SSE Connection**

Connect to MCP servers via Server-Sent Events:

```go
func setupSSEMCP() *schemas.MCPConfig {
    sseEndpoint := "http://localhost:8080/mcp/sse"

    return &schemas.MCPConfig{
        ClientConfigs: []schemas.MCPClientConfig{
            {
                Name:             "realtime-data",
                ConnectionType:   schemas.MCPConnectionTypeSSE,
                ConnectionString: &sseEndpoint,
            },
        },
    }
}
```

---

## ⚡ Using MCP Tools

### **Automatic Tool Integration**

MCP tools are automatically added to all requests:

```go
// Tools from MCP servers are automatically available
response, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input: schemas.RequestInput{
        ChatCompletionInput: &[]schemas.BifrostMessage{
            {
                Role: schemas.ModelChatMessageRoleUser,
                Content: schemas.MessageContent{ContentStr: &message},
            },
        },
    },
    // No need to specify tools - MCP tools are automatically included
})

// Check if model used any tools
if len(response.Choices) > 0 && response.Choices[0].Message.ToolCalls != nil {
    fmt.Printf("Model used %d tools\n", len(*response.Choices[0].Message.ToolCalls))
}
```

### **Manual Tool Execution**

Execute MCP tools directly for security and control. You can pass tool calls directly from assistant message responses:

```go
// Option 1: Use tool calls from assistant response
response, err := client.ChatCompletionRequest(ctx, request)
if err != nil {
    return err
}

// Execute each tool call from the assistant's response
if len(response.Choices) > 0 && response.Choices[0].Message.ToolCalls != nil {
    for _, toolCall := range *response.Choices[0].Message.ToolCalls {
        // Execute the tool call directly - gives you full control for security
        toolResult, err := client.ExecuteMCPTool(context.Background(), toolCall)
        if err != nil {
            log.Printf("Tool execution failed: %v", err)
            continue
        }

        // Process result as needed
        if toolResult.Content.ContentStr != nil {
            fmt.Printf("Tool result: %s\n", *toolResult.Content.ContentStr)
        }
    }
}

// Option 2: Create custom tool calls
toolCall := schemas.ToolCall{
    ID:   &[]string{"call_123"}[0],
    Type: &[]string{"function"}[0],
    Function: schemas.FunctionCall{
        Name:      &[]string{"read_file"}[0],
        Arguments: `{"path": "/path/to/file.txt"}`,
    },
}

// Execute the tool manually
toolResult, err := client.ExecuteMCPTool(context.Background(), toolCall)
if err != nil {
    log.Printf("Tool execution failed: %v", err)
    return
}

// Use the result
if toolResult.Content.ContentStr != nil {
    fmt.Printf("Tool result: %s\n", *toolResult.Content.ContentStr)
}
```

> **🔒 Security Note:** Manual execution gives you full control over tool calls. This allows you to validate arguments, implement access controls, and audit tool usage before execution.

---

## 🔧 Custom Tool Registration

### **Register In-Process Tools**

Register custom tools that run within your application:

```go
// Define your tool function
func echoTool(args any) (string, error) {
    argsMap, ok := args.(map[string]interface{})
    if !ok {
        return "", fmt.Errorf("invalid arguments")
    }

    message, ok := argsMap["message"].(string)
    if !ok {
        return "", fmt.Errorf("message parameter required")
    }

    return fmt.Sprintf("Echo: %s", message), nil
}

// Define tool schema
echoToolSchema := schemas.Tool{
    Type: "function",
    Function: schemas.Function{
        Name:        "echo",
        Description: "Echo a message back to the user",
        Parameters: schemas.FunctionParameters{
            Type: "object",
            Properties: map[string]interface{}{
                "message": map[string]interface{}{
                    "type":        "string",
                    "description": "Message to echo back",
                },
            },
            Required: []string{"message"},
        },
    },
}

// Register the tool
err := client.RegisterMCPTool("echo", "Echo a message", echoTool, echoToolSchema)
if err != nil {
    log.Printf("Failed to register tool: %v", err)
}

// Now the tool is available to all AI requests
```

### **Advanced Custom Tools**

More complex tools with error handling and validation:

```go
// Database query tool
func databaseQueryTool(args any) (string, error) {
    argsMap, ok := args.(map[string]interface{})
    if !ok {
        return "", fmt.Errorf("invalid arguments")
    }

    query, ok := argsMap["query"].(string)
    if !ok {
        return "", fmt.Errorf("query parameter required")
    }

    // Validate query (prevent dangerous operations)
    if strings.Contains(strings.ToLower(query), "drop") ||
       strings.Contains(strings.ToLower(query), "delete") ||
       strings.Contains(strings.ToLower(query), "update") {
        return "", fmt.Errorf("only SELECT queries are allowed")
    }

    // Execute query (pseudo-code)
    db := getDatabase()
    rows, err := db.Query(query)
    if err != nil {
        return "", fmt.Errorf("query failed: %w", err)
    }
    defer rows.Close()

    // Format results as JSON
    results := []map[string]interface{}{}
    for rows.Next() {
        // Scan row data...
        row := map[string]interface{}{
            "id":   1,
            "name": "example",
        }
        results = append(results, row)
    }

    jsonData, _ := json.Marshal(results)
    return string(jsonData), nil
}

// Register database tool
dbToolSchema := schemas.Tool{
    Type: "function",
    Function: schemas.Function{
        Name:        "database_query",
        Description: "Execute a safe SELECT query on the database",
        Parameters: schemas.FunctionParameters{
            Type: "object",
            Properties: map[string]interface{}{
                "query": map[string]interface{}{
                    "type":        "string",
                    "description": "SQL SELECT query to execute",
                },
            },
            Required: []string{"query"},
        },
    },
}

err := client.RegisterMCPTool("database_query", "Query database", databaseQueryTool, dbToolSchema)
```

---

## 🔍 Tool Discovery and Filtering

### **Tool Filtering by Client (Config Level)**

Control which tools from each MCP client are available at the configuration level:

```go
mcpConfig := &schemas.MCPConfig{
    ClientConfigs: []schemas.MCPClientConfig{
        {
            Name:           "filesystem-tools",
            ConnectionType: schemas.MCPConnectionTypeSTDIO,
            StdioConfig: &schemas.MCPStdioConfig{
                Command: "npx",
                Args:    []string{"-y", "@modelcontextprotocol/server-filesystem"},
            },
            // Whitelist approach: Only allow specific tools
            ToolsToExecute: []string{"read_file", "list_directory"},
        },
        {
            Name:           "web-tools",
            ConnectionType: schemas.MCPConnectionTypeSTDIO,
            StdioConfig: &schemas.MCPStdioConfig{
                Command: "npx",
                Args:    []string{"-y", "@modelcontextprotocol/server-web"},
            },
            // Blacklist approach: Block dangerous tools
            ToolsToSkip: []string{"delete_page", "modify_content"},
        },
    },
}
```

> **💡 Filtering Rules:**
>
> - `ToolsToExecute`: Whitelist - only these tools are available (overrides ToolsToSkip)
> - `ToolsToSkip`: Blacklist - all tools except these are available
> - If both are specified, `ToolsToExecute` takes precedence

### **Context-Based Tool Filtering (Request Level)**

Filter tools at runtime for specific requests using context keys:

```go
import "context"

// Whitelist specific clients (only these clients' tools will be available)
ctx := context.WithValue(context.Background(), "mcp-include-clients", []string{"filesystem-tools", "database-client"})

response, err := client.ChatCompletionRequest(ctx, &schemas.BifrostRequest{
    Provider: schemas.OpenAI,
    Model:    "gpt-4o-mini",
    Input:    input, // your input here
})

// Blacklist specific clients (all tools except these clients' tools will be available)
ctx = context.WithValue(context.Background(), "mcp-exclude-clients", []string{"web-tools", "admin-tools"})

response, err = client.ChatCompletionRequest(ctx, &schemas.BifrostRequest{
    Provider: schemas.Anthropic,
    Model:    "claude-3-sonnet-20240229",
    Input:    input, // your input here
})

// Combine both approaches for fine-grained control
func createRestrictedContext() context.Context {
    ctx := context.Background()

    // Only allow safe tools for user-facing operations
    ctx = context.WithValue(ctx, "mcp-include-clients", []string{"search-tools", "calculator"})

    return ctx
}

// Use in production
userCtx := createRestrictedContext()
response, err := client.ChatCompletionRequest(userCtx, userRequest)
```

> **💡 Context Filtering Rules:**
>
> - `mcp-include-clients`: Whitelist - only tools from these named MCP clients are available
> - `mcp-exclude-clients`: Blacklist - tools from these named MCP clients are excluded
> - If both are specified, `mcp-include-clients` takes precedence
>   Similarly you can pass values for `mcp-include-tools` and `mcp-exclude-tools` to filter tools at runtime.
> - These filters work at runtime and can be different for each request
> - Useful for user-based permissions, request-specific security, or A/B testing different tool sets

---

## 🔧 Dynamic Client Management

### **Adding Clients at Runtime**

Add new MCP clients after initialization:

```go
// Add a new filesystem client
newClientConfig := schemas.MCPClientConfig{
    Name:           "new-filesystem",
    ConnectionType: schemas.MCPConnectionTypeSTDIO,
    StdioConfig: &schemas.MCPStdioConfig{
        Command: "npx",
        Args:    []string{"-y", "@modelcontextprotocol/server-filesystem"},
        Envs:    []string{"FILESYSTEM_ROOT"},
    },
}

err := client.AddMCPClient(newClientConfig)
if err != nil {
    log.Printf("Failed to add MCP client: %v", err)
}

// Add HTTP client
httpEndpoint := "http://localhost:8080/mcp"
httpClientConfig := schemas.MCPClientConfig{
    Name:             "api-tools",
    ConnectionType:   schemas.MCPConnectionTypeHTTP,
    ConnectionString: &httpEndpoint,
}

err = client.AddMCPClient(httpClientConfig)
if err != nil {
    log.Printf("Failed to add HTTP MCP client: %v", err)
}
```

### **Removing Clients**

Remove MCP clients when no longer needed:

```go
// Remove a specific client
err := client.RemoveMCPClient("filesystem-tools")
if err != nil {
    log.Printf("Failed to remove MCP client: %v", err)
}

// The client will be disconnected and its tools will no longer be available
```

### **Editing Client Tool Filters**

Dynamically modify which tools are available from a client:

```go
// Allow only specific tools from a client
toolsToAdd := []string{"read_file", "list_directory"}
toolsToRemove := []string{"write_file", "delete_file"}

err := client.EditMCPClientTools("filesystem-tools", toolsToAdd, toolsToRemove)
if err != nil {
    log.Printf("Failed to edit client tools: %v", err)
}

// Clear all restrictions (allow all tools)
err = client.EditMCPClientTools("filesystem-tools", []string{}, []string{})
if err != nil {
    log.Printf("Failed to clear tool restrictions: %v", err)
}

// Block specific dangerous tools
dangerousTools := []string{"delete_file", "format_disk", "system_shutdown"}
err = client.EditMCPClientTools("filesystem-tools", []string{}, dangerousTools)
if err != nil {
    log.Printf("Failed to block dangerous tools: %v", err)
}
```

### **Listing All Clients**

Get information about all connected MCP clients:

```go
import "strings"

clients, err := client.GetMCPClients()
if err != nil {
    log.Printf("Failed to get MCP clients: %v", err)
    return
}

for _, mcpClient := range clients {
    fmt.Printf("Client: %s\n", mcpClient.Name)
    fmt.Printf("  Type: %s\n", mcpClient.Config.ConnectionType)
    fmt.Printf("  State: %s\n", mcpClient.State)
    
    if mcpClient.Config.ConnectionString != nil {
        fmt.Printf("  URL: %s\n", *mcpClient.Config.ConnectionString)
    }
    
    if mcpClient.Config.StdioConfig != nil {
        cmdString := fmt.Sprintf("%s %s", mcpClient.Config.StdioConfig.Command, 
                                strings.Join(mcpClient.Config.StdioConfig.Args, " "))
        fmt.Printf("  Command: %s\n", cmdString)
    }
    
    fmt.Printf("  Tools: %d available\n", len(mcpClient.Tools))
    for _, toolName := range mcpClient.Tools {
        fmt.Printf("    - %s\n", toolName)
    }
    fmt.Println()
}

// Filter clients by connection state
connectedClients := []schemas.MCPClient{}
disconnectedClients := []schemas.MCPClient{}

for _, mcpClient := range clients {
    if mcpClient.State == schemas.MCPConnectionStateConnected {
        connectedClients = append(connectedClients, mcpClient)
    } else {
        disconnectedClients = append(disconnectedClients, mcpClient)
    }
}

fmt.Printf("Connected clients: %d\n", len(connectedClients))
fmt.Printf("Disconnected clients: %d\n", len(disconnectedClients))
```

### **Reconnecting Disconnected Clients**

Reconnect clients that have lost their connection:

```go
// Reconnect a specific client
err := client.ReconnectMCPClient("web-search")
if err != nil {
    log.Printf("Failed to reconnect MCP client: %v", err)
}

// Check client status before reconnecting
clients, err := client.GetMCPClients()
if err == nil {
    for _, mcpClient := range clients {
        if mcpClient.State != schemas.MCPConnectionStateConnected {
            fmt.Printf("Client %s is %s, attempting reconnect...\n", mcpClient.Name, mcpClient.State)
            if err := client.ReconnectMCPClient(mcpClient.Name); err != nil {
                log.Printf("Failed to reconnect %s: %v", mcpClient.Name, err)
            } else {
                fmt.Printf("Successfully reconnected %s\n", mcpClient.Name)
            }
        }
    }
}
```

### **Dynamic Client Management Example**

Complete example showing dynamic client lifecycle:

```go
func manageMCPClients(client *bifrost.Bifrost) {
    // 1. Add clients based on runtime conditions
    if needsFileAccess() {
        fileClientConfig := schemas.MCPClientConfig{
            Name:           "runtime-filesystem",
            ConnectionType: schemas.MCPConnectionTypeSTDIO,
            StdioConfig: &schemas.MCPStdioConfig{
                Command: "npx",
                Args:    []string{"-y", "@modelcontextprotocol/server-filesystem"},
                Envs:    []string{"FILESYSTEM_ROOT"},
            },
        }
        
        if err := client.AddMCPClient(fileClientConfig); err != nil {
            log.Printf("Failed to add filesystem client: %v", err)
        }
    }

    // 2. Configure tool access based on user permissions
    if isRestrictedUser() {
        // Only allow safe read operations
        safeTools := []string{"read_file", "list_directory"}
        err := client.EditMCPClientTools("runtime-filesystem", safeTools, []string{})
        if err != nil {
            log.Printf("Failed to restrict tools: %v", err)
        }
    }

    // 3. Monitor and maintain connections
    go func() {
        ticker := time.NewTicker(30 * time.Second)
        defer ticker.Stop()
        
        for range ticker.C {
            clients, err := client.GetMCPClients()
            if err != nil {
                continue
            }
            
            for _, mcpClient := range clients {
                if mcpClient.State != schemas.MCPConnectionStateConnected {
                    log.Printf("Detected disconnected client: %s (state: %s)", mcpClient.Name, mcpClient.State)
                    if err := client.ReconnectMCPClient(mcpClient.Name); err != nil {
                        log.Printf("Failed to reconnect %s: %v", mcpClient.Name, err)
                    }
                }
            }
        }
    }()

    // 4. Cleanup when done
    defer func() {
        // Remove temporary clients
        if err := client.RemoveMCPClient("runtime-filesystem"); err != nil {
            log.Printf("Failed to remove temporary client: %v", err)
        }
    }()
}

func needsFileAccess() bool {
    // Your logic to determine if file access is needed
    return true
}

func isRestrictedUser() bool {
    // Your logic to determine user permissions
    return false
}
```

---

## 🔄 Multi-Turn Tool Conversations

### **Handling Tool Call Loops**

Implement proper tool calling conversations:

```go
func handleToolConversation(client *bifrost.Bifrost, initialMessage string) {
    conversation := []schemas.BifrostMessage{
        {
            Role: schemas.ModelChatMessageRoleUser,
            Content: schemas.MessageContent{ContentStr: &initialMessage},
        },
    }

    maxTurns := 10
    for turn := 0; turn < maxTurns; turn++ {
        response, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
            Provider: schemas.OpenAI,
            Model:    "gpt-4o-mini",
            Input: schemas.RequestInput{
                ChatCompletionInput: &conversation,
            },
        })

        if err != nil {
            log.Printf("Request failed: %v", err)
            return
        }

        choice := response.Choices[0]

        // Add assistant's response to conversation
        conversation = append(conversation, choice.Message)

        // Check if model wants to call tools
        if choice.Message.ToolCalls != nil {
            // Execute all tool calls
            for _, toolCall := range *choice.Message.ToolCalls {
                toolResult, err := client.ExecuteMCPTool(context.Background(), toolCall)
                if err != nil {
                    log.Printf("Tool execution failed: %v", err)
                    continue
                }

                // Add tool result to conversation
                conversation = append(conversation, *toolResult)
            }

            // Continue conversation with tool results
            continue
        }

        // No more tool calls - conversation is complete
        if choice.Message.Content.ContentStr != nil {
            fmt.Printf("Final response: %s\n", *choice.Message.Content.ContentStr)
        }
        break
    }
}

// Usage
handleToolConversation(client, "Analyze the files in the current directory and summarize what the project does")
```

---

## 📊 MCP Monitoring and Debugging

### **Tool Execution Monitoring**

Track tool usage and performance:

```go
type MCPMonitoringPlugin struct {
    toolCalls map[string]int
    errors    map[string]int
    mu        sync.RWMutex
}

func (p *MCPMonitoringPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
    // Add monitoring context
    *ctx = context.WithValue(*ctx, "mcp_monitor_start", time.Now())
    return req, nil, nil
}

func (p *MCPMonitoringPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
    if result != nil && len(result.Choices) > 0 && result.Choices[0].Message.ToolCalls != nil {
        p.mu.Lock()
        for _, toolCall := range *result.Choices[0].Message.ToolCalls {
            if toolCall.Function.Name != nil {
                p.toolCalls[*toolCall.Function.Name]++
            }
        }
        p.mu.Unlock()
    }

    return result, err, nil
}

// Get monitoring data
func (p *MCPMonitoringPlugin) GetToolStats() map[string]int {
    p.mu.RLock()
    defer p.mu.RUnlock()

    stats := make(map[string]int)
    for tool, count := range p.toolCalls {
        stats[tool] = count
    }
    return stats
}
```

### **Debug Tool Execution**

Enable detailed logging for MCP operations:

```go
// Create logger that shows MCP operations
logger := log.New(os.Stdout, "[MCP] ", log.LstdFlags|log.Lshortfile)

client, initErr := bifrost.Init(schemas.BifrostConfig{
    Account:   &MyAccount{},
    Logger:    customLogger, // Use custom logger for MCP debug info
    MCPConfig: mcpConfig,
})

// MCP operations will be logged with detailed information
```

---

## 🧪 Testing MCP Integration

### **Unit Testing Custom Tools**

Test your custom tools in isolation:

```go
func TestEchoTool(t *testing.T) {
    args := map[string]interface{}{
        "message": "Hello, World!",
    }

    result, err := echoTool(args)
    assert.NoError(t, err)
    assert.Equal(t, "Echo: Hello, World!", result)

    // Test error case
    invalidArgs := map[string]interface{}{
        "wrong_param": "value",
    }

    _, err = echoTool(invalidArgs)
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "message parameter required")
}
```

### **Integration Testing with MCP**

Test MCP integration with real tools:

```go
func TestMCPIntegration(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping MCP integration test")
    }

    // Setup MCP client with echo tool
    client, initErr := bifrost.Init(schemas.BifrostConfig{
        Account: &TestAccount{},
        MCPConfig: &schemas.MCPConfig{
            ClientConfigs: []schemas.MCPClientConfig{
                // Configure test MCP server
            },
        },
    })
    require.Nil(t, initErr)
    defer client.Cleanup()

    // Register test tool
    err = client.RegisterMCPTool("test_echo", "Test echo", echoTool, echoToolSchema)
    require.NoError(t, err)

    // Test tool is available in requests
    message := "Use the echo tool to repeat this message"
    response, err := client.ChatCompletionRequest(context.Background(), &schemas.BifrostRequest{
        Provider: schemas.OpenAI,
        Model:    "gpt-4o-mini",
        Input: schemas.RequestInput{
            ChatCompletionInput: &[]schemas.BifrostMessage{
                {Role: schemas.ModelChatMessageRoleUser, Content: schemas.MessageContent{ContentStr: &message}},
            },
        },
    })

    assert.NoError(t, err)
    assert.NotNil(t, response)

    // Check if tool was called
    if len(response.Choices) > 0 && response.Choices[0].Message.ToolCalls != nil {
        foundEchoTool := false
        for _, toolCall := range *response.Choices[0].Message.ToolCalls {
            if toolCall.Function.Name != nil && *toolCall.Function.Name == "test_echo" {
                foundEchoTool = true
                break
            }
        }
        assert.True(t, foundEchoTool, "Echo tool should have been called")
    }
}
```

---

## 📚 Related Documentation

- **[🤖 Bifrost Client](./bifrost-client.md)** - Using MCP with client requests
- **[🔌 Plugins](./plugins.md)** - MCP monitoring plugins
- **[📋 Schemas](./schemas.md)** - MCP configuration structures
- **[🌐 HTTP Transport](../http-transport/)** - MCP configuration via JSON

> **🏛️ Architecture:** For MCP system design and integration details, see [Architecture Documentation](../../architecture/).
