# Bifrost Transports

This package contains clients for various transports that can be used to spin up your Bifrost client with just a single line of code.

📖 **Comprehensive HTTP API documentation is available in** _[`docs/http-transport-api.md`](../docs/http-transport-api.md)_.

## 📑 Table of Contents

- [Bifrost Transports](#bifrost-transports)
  - [📑 Table of Contents](#-table-of-contents)
  - [🚀 Setting Up Transports](#-setting-up-transports)
    - [Prerequisites](#prerequisites)
    - [Configuration](#configuration)
    - [MCP (Model Context Protocol) Configuration](#mcp-model-context-protocol-configuration)
      - [MCP Environment Variables](#mcp-environment-variables)
    - [Docker Setup](#docker-setup)
    - [Go Setup](#go-setup)
  - [🧰 Usage](#-usage)
    - [Text Completions](#text-completions)
    - [Chat Completions](#chat-completions)
    - [Multi-Turn Conversations with MCP Tools](#multi-turn-conversations-with-mcp-tools)
  - [🔧 Advanced Features](#-advanced-features)
    - [Prometheus Support](#prometheus-support)
    - [Plugin Support](#plugin-support)
    - [Fallbacks](#fallbacks)

---

## 🚀 Setting Up Transports

### Prerequisites

- Go 1.23 or higher (if not using Docker)
- Access to at least one AI model provider (OpenAI, Anthropic, etc.)
- API keys for the providers you wish to use

### Configuration

Bifrost uses a combination of a JSON configuration file and environment variables:

1. **JSON Configuration File**: Bifrost requires a configuration file to set up the gateway. This includes all your provider-level settings, keys, and meta configs for each of your providers.
2. **Environment Variables**: If you don't want to include your keys in your config file, you can add a prefix of `env.` followed by its key in your environment.

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
  }
}
```

In this example config file, `OPENAI_API_KEY` refers to a key set in your environment. At runtime, its value will be used to replace the placeholder.

The same setup applies to keys in meta configs of all providers:

```json
{
  "providers": {
    "bedrock": {
      "keys": [
        {
          "value": "env.BEDROCK_API_KEY",
          "models": ["anthropic.claude-v2:1"],
          "weight": 1.0
        }
      ],
      "meta_config": {
        "secret_access_key": "env.AWS_SECRET_ACCESS_KEY",
        "region": "env.AWS_REGION"
      }
    }
  }
}
```

In this example, `AWS_SECRET_ACCESS_KEY` and `AWS_REGION` refer to keys in the environment.

**Please refer to `config.example.json` for examples.**

### MCP (Model Context Protocol) Configuration

Bifrost supports MCP integration for tool usage with AI models. You can configure MCP servers and tools in your configuration file:

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
          "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
          "envs": ["NODE_ENV", "FILESYSTEM_ROOT"]
        },
        "tools_to_skip": [],
        "tools_to_execute": []
      },
      {
        "name": "web-search",
        "connection_type": "http",
        "http_connection_string": "http://localhost:3001/mcp",
        "tools_to_skip": [],
        "tools_to_execute": []
      }
    ]
  }
}
```

#### MCP Environment Variables

The `envs` field in STDIO MCP configuration serves a different purpose than regular environment variables in Bifrost:

- **Regular Bifrost environment variables** (like `"env.OPENAI_API_KEY"`) use the `env.` prefix and are accessed directly by Bifrost
- **MCP environment variables** (in the `envs` array) do **NOT** use the `env.` prefix and are not accessed by Bifrost directly

Instead, Bifrost checks if the environment variables listed in `envs` are available in the environment **before establishing the MCP client connection**. This ensures that MCP tools that require specific environment variables (like API keys or configuration values) have their dependencies available before attempting to connect.

For example:

```json
{
  "name": "weather-service",
  "connection_type": "stdio",
  "stdio_config": {
    "command": "npx",
    "args": ["weather-mcp-server"],
    "envs": ["WEATHER_API_KEY", "DEFAULT_LOCATION"]
  }
}
```

In this case, Bifrost will verify that `WEATHER_API_KEY` and `DEFAULT_LOCATION` exist in the environment before attempting to start the weather MCP server.

**Configuration Summary:**

- Connects to a filesystem MCP tool via STDIO (requires `NODE_ENV` and `FILESYSTEM_ROOT` environment variables)
- Connects to a web-search MCP service via HTTP

**For comprehensive MCP documentation including Go package usage, local tool registration, and advanced configurations, see [MCP Integration Guide](../docs/mcp.md).** This section focuses on HTTP transport specific MCP usage.

### Docker Setup

1. Pull the Docker image:

   ```bash
   docker pull maximhq/bifrost
   ```

2. Run the Docker container:

   ```bash
   docker run -p 8080:8080 \
     -v $(pwd)/config.json:/app/config/config.json \
     -e OPENAI_API_KEY \
     -e ANTHROPIC_API_KEY \
     maximhq/bifrost
   ```

Note: In the command above, `OPENAI_API_KEY` and `ANTHROPIC_API_KEY` are just example environment variables.
Ensure you mount your config file and use the `-e` flag to pass all environment variables referenced in your `config.json` that are prefixed with `env.` to the container. This ensures Docker sets them correctly inside the container.

Example usage: Suppose your config.json only contains one environment variable placeholder, `env.COHERE_API_KEY`. Here's how you would run it:

```bash
export COHERE_API_KEY=your_cohere_api_key

docker run -p 8080:8080 \
  -v $(pwd)/config.example.json:/app/config/config.json \
  -e COHERE_API_KEY \
  maximhq/bifrost
```

You can also set runtime environment variables for configuration:

- `APP_PORT`: Server port (default: 8080)
- `APP_POOL_SIZE`: Connection pool size (default: 300)
- `APP_DROP_EXCESS_REQUESTS`: Drop excess requests when buffer is full (default: false)
- `APP_PLUGINS`: Comma-separated list of plugins

Read more about these [configurations](https://github.com/maximhq/bifrost/tree/main?tab=README-ov-file#additional-configurations).

---

### Go Setup

If you wish to run Bifrost in your Go environment, follow these steps:

1. Install your binary:

   ```bash
   go install github.com/maximhq/bifrost/transports/bifrost-http@latest
   ```

2. Run your binary (ensure Go is in your PATH):

```bash
bifrost-http -config config.json -port 8080 -pool-size 300
```

You can also add a flag for `-drop-excess-requests=false` in your command to drop excess requests when the buffer is full. Read more about `DROP_EXCESS_REQUESTS` and `POOL_SIZE` in [additional configurations](https://github.com/maximhq/bifrost/tree/main?tab=README-ov-file#additional-configurations).

## 🧰 Usage

Ensure that:

- Bifrost's HTTP server is running
- The providers/models you use are configured in your JSON config file

### Text Completions

```bash
# Make sure to set up Anthropic and claude-2.1 in your config.json
curl -X POST http://localhost:8080/v1/text/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "anthropic",
    "model": "claude-2.1",
    "text": "Once upon a time in the land of AI,",
    "params": {
      "temperature": 0.7,
      "max_tokens": 100
    }
  }'
```

### Chat Completions

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "Tell me about Bifrost in Norse mythology."}
    ],
    "params": {
      "temperature": 0.8,
      "max_tokens": 500
    }
  }'
```

### Multi-Turn Conversations with MCP Tools

When MCP is configured, Bifrost automatically adds available tools to requests. Here's an example of a multi-turn conversation where the AI uses tools:

1. **Initial Request** (AI decides to use a tool):

```bash
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "Can you list the files in the /tmp directory?"}
    ]
  }'
```

Response includes tool calls:

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
                    "name": "list_files",
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

2. **Execute Tool** (Use Bifrost's MCP tool execution endpoint):

   ```bash
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

   Response with tool result:

   ```json
   {
     "role": "tool",
     "content": "config.json\nreadme.txt\ndata.csv",
     "tool_call_id": "call_abc123"
   }
   ```

3. **Continue Conversation** (Add tool result and get final response):

   ```bash
   curl -X POST http://localhost:8080/v1/chat/completions \
     -H "Content-Type: application/json" \
     -d '{
       "provider": "openai",
       "model": "gpt-4o-mini",
       "messages": [
         {"role": "user", "content": "Can you list the files in the /tmp directory?"},
         {
           "role": "assistant",
           "content": null,
           "tool_calls": [{
             "id": "call_abc123",
             "type": "function",
             "function": {
               "name": "list_files",
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

   Final response:

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

**Key Endpoints:**

- `POST /v1/chat/completions` - Chat with automatic tool discovery
- `POST /v1/mcp/tool/execute` - Execute tool calls returned by the AI

> 🔧 **For Go package integration and advanced tool execution patterns, see [Implementing Chat Conversations with MCP Tools](../docs/mcp.md#implementing-chat-conversations-with-mcp-tools).**

---

## 🔧 Advanced Features

### Prometheus Support

HTTP transport supports Prometheus out of the box. By default, all metrics are available at the `/metrics` endpoint. It provides metrics for httpRequestsTotal, httpRequestDuration, httpRequestSizeBytes, httpResponseSizeBytes, bifrostUpstreamRequestsTotal, and bifrostUpstreamLatencySeconds. To add custom labels to these metrics, pass the `-prometheus-labels` flag while running the HTTP transport.

e.g., `-prometheus-labels team-id,task-id,location`

Values for labels are then picked up from the HTTP request headers with the prefix `x-bf-prom-`.

### Plugin Support

You can explore the [available plugins](https://github.com/maximhq/bifrost/tree/main/plugins). To attach these plugins to your HTTP transport, pass the `-plugins` flag.

e.g., `-plugins maxim`

Note: Check plugin-specific documentation (github.com/maximhq/bifrost/tree/main/plugins/{plugin_name}) for more granular control and additional setup requirements.

### Fallbacks

Configure fallback options in your requests:

```json
{
  "provider": "openai",
  "model": "gpt-4",
  "messages": [...],
  "fallbacks": [
    {
      "provider": "anthropic",
      "model": "claude-3-opus-20240229"
    },
    {
      "provider": "bedrock",
      "model": "anthropic.claude-3-sonnet-20240229-v1:0"
    }
  ]
}
```

Read more about fallbacks and other [additional configurations](https://github.com/maximhq/bifrost/tree/main?tab=README-ov-file#additional-configurations).

Built with ❤️ by [Maxim](https://github.com/maximhq)
