# 🧠 Anthropic Compatible API

Complete guide to using Bifrost as a drop-in replacement for Anthropic API with full compatibility and enhanced features.

> **💡 Quick Start:** Change `base_url` from `https://api.anthropic.com` to `http://localhost:8080/anthropic` - that's it!

---

## 📋 Overview

Bifrost provides **100% Anthropic API compatibility** with enhanced features:

- **Zero code changes** - Works with existing Anthropic SDK applications
- **Same request/response formats** - Exact Anthropic API specification
- **Enhanced capabilities** - Multi-provider fallbacks, MCP tools, monitoring
- **Full tool use support** - Native Anthropic tool calling + MCP integration
- **Any provider under the hood** - Use any configured provider (Anthropic, OpenAI, etc.)

**Endpoint:** `POST /anthropic/v1/messages`

> **🔄 Provider Flexibility:** While using Anthropic SDK format, you can specify any model like `"claude-3-sonnet-20240229"` (uses Anthropic) or `"openai/gpt-4o-mini"` (uses OpenAI) - Bifrost will route to the appropriate provider automatically.

---

## 🔄 Quick Migration

### **Python (Anthropic SDK)**

```python
import anthropic

# Before - Direct Anthropic
client = anthropic.Anthropic(
    base_url="https://api.anthropic.com",
    api_key="your-anthropic-key"
)

# After - Via Bifrost
client = anthropic.Anthropic(
    base_url="http://localhost:8080/anthropic",  # Only change this
    api_key="your-anthropic-key"
)

# Everything else stays the same
response = client.messages.create(
    model="claude-3-sonnet-20240229",
    max_tokens=1000,
    messages=[{"role": "user", "content": "Hello!"}]
)
```

### **JavaScript (Anthropic SDK)**

```javascript
import Anthropic from "@anthropic-ai/sdk";

// Before - Direct Anthropic
const anthropic = new Anthropic({
  baseURL: "https://api.anthropic.com",
  apiKey: process.env.ANTHROPIC_API_KEY,
});

// After - Via Bifrost
const anthropic = new Anthropic({
  baseURL: "http://localhost:8080/anthropic", // Only change this
  apiKey: process.env.ANTHROPIC_API_KEY,
});

// Everything else stays the same
const response = await anthropic.messages.create({
  model: "claude-3-sonnet-20240229",
  max_tokens: 1000,
  messages: [{ role: "user", content: "Hello!" }],
});
```

---

## 📊 Supported Features

### **✅ Fully Supported**

| Feature             | Status     | Notes                           |
| ------------------- | ---------- | ------------------------------- |
| **Messages API**    | ✅ Full    | All parameters supported        |
| **Tool Use**        | ✅ Full    | Native + MCP tools              |
| **System Messages** | ✅ Full    | Anthropic system prompts        |
| **Vision/Images**   | ✅ Full    | Image analysis                  |
| **Streaming**       | ✅ Full | Currently returns full response |
| **Max Tokens**      | ✅ Full    | Token limit control             |
| **Temperature**     | ✅ Full    | Sampling control                |
| **Stop Sequences**  | ✅ Full    | Custom stop tokens              |

### **🚀 Enhanced Features**

| Feature                      | Enhancement              | Benefit               |
| ---------------------------- | ------------------------ | --------------------- |
| **Multi-provider Fallbacks** | Automatic failover       | Higher reliability    |
| **MCP Tool Integration**     | External tools available | Extended capabilities |
| **Load Balancing**           | Multiple API keys        | Better performance    |
| **Monitoring**               | Prometheus metrics       | Observability         |
| **Cross-provider Tools**     | Use with any provider    | Flexibility           |

---

## 🛠️ Request Examples

### **Basic Message**

```bash
# Use Anthropic provider
curl -X POST http://localhost:8080/anthropic/v1/messages \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-sonnet-20240229",
    "max_tokens": 1000,
    "messages": [
      {"role": "user", "content": "What is the capital of France?"}
    ]
  }'

# Use OpenAI provider via Anthropic SDK format
curl -X POST http://localhost:8080/anthropic/v1/messages \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "max_tokens": 1000,
    "messages": [
      {"role": "user", "content": "What is the capital of France?"}
    ]
  }'
```

**Response:**

```json
{
  "id": "msg_123",
  "type": "message",
  "role": "assistant",
  "content": [
    {
      "type": "text",
      "text": "The capital of France is Paris."
    }
  ],
  "model": "claude-3-sonnet-20240229",
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {
    "input_tokens": 13,
    "output_tokens": 7
  }
}
```

### **System Message**

```bash
curl -X POST http://localhost:8080/anthropic/v1/messages \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-sonnet-20240229",
    "max_tokens": 1000,
    "system": "You are a helpful assistant that answers questions about geography.",
    "messages": [
      {"role": "user", "content": "What is the capital of France?"}
    ]
  }'
```

### **Tool Use**

```bash
curl -X POST http://localhost:8080/anthropic/v1/messages \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-sonnet-20240229",
    "max_tokens": 1000,
    "tools": [
      {
        "name": "get_weather",
        "description": "Get weather information for a location",
        "input_schema": {
          "type": "object",
          "properties": {
            "location": {"type": "string", "description": "City name"}
          },
          "required": ["location"]
        }
      }
    ],
    "messages": [
      {"role": "user", "content": "What is the weather in Paris?"}
    ]
  }'
```

**Response with Tool Use:**

```json
{
  "id": "msg_123",
  "type": "message",
  "role": "assistant",
  "content": [
    {
      "type": "tool_use",
      "id": "toolu_123",
      "name": "get_weather",
      "input": {
        "location": "Paris"
      }
    }
  ],
  "model": "claude-3-sonnet-20240229",
  "stop_reason": "tool_use",
  "usage": {
    "input_tokens": 25,
    "output_tokens": 15
  }
}
```

### **Vision/Image Analysis**

```python
import anthropic

client = anthropic.Anthropic(
    base_url="http://localhost:8080/anthropic",
    api_key=anthropic_key
)

response = client.messages.create(
    model="claude-3-sonnet-20240229",
    max_tokens=1000,
    messages=[
        {
            "role": "user",
            "content": [
                {
                    "type": "text",
                    "text": "What's in this image?"
                },
                {
                    "type": "image",
                    "source": {
                        "type": "base64",
                        "media_type": "image/jpeg",
                        "data": "/9j/4AAQSkZJRgABAQEAYABgAAD..."
                    }
                }
            ]
        }
    ]
)
```

---

## 🔧 Advanced Usage

### **Multi-turn Conversation**

```python
import anthropic

client = anthropic.Anthropic(
    base_url="http://localhost:8080/anthropic",
    api_key=anthropic_key
)

response = client.messages.create(
    model="claude-3-sonnet-20240229",
    max_tokens=1000,
    messages=[
        {"role": "user", "content": "What is 2+2?"},
        {"role": "assistant", "content": "2+2 equals 4."},
        {"role": "user", "content": "What about 3+3?"}
    ]
)
```

### **Tool Use with Results**

```python
import anthropic

client = anthropic.Anthropic(
    base_url="http://localhost:8080/anthropic",
    api_key=anthropic_key
)

# First request with tool use
response = client.messages.create(
    model="claude-3-sonnet-20240229",
    max_tokens=1000,
    tools=[
        {
            "name": "list_directory",
            "description": "List files in a directory",
            "input_schema": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "Directory path"}
                },
                "required": ["path"]
            }
        }
    ],
    messages=[
        {"role": "user", "content": "List files in the current directory"}
    ]
)

# Tool was called, now provide results
if response.content[0].type == "tool_use":
    tool_use = response.content[0]

    # Continue conversation with tool result
    follow_up = client.messages.create(
        model="claude-3-sonnet-20240229",
        max_tokens=1000,
        messages=[
            {"role": "user", "content": "List files in the current directory"},
            {"role": "assistant", "content": response.content},
            {
                "role": "user",
                "content": [
                    {
                        "type": "tool_result",
                        "tool_use_id": tool_use.id,
                        "content": "README.md\nconfig.json\nsrc/"
                    }
                ]
            }
        ]
    )
```

### **Error Handling**

```python
import anthropic
from anthropic import AnthropicError

client = anthropic.Anthropic(
    base_url="http://localhost:8080/anthropic",
    api_key=anthropic_key
)

try:
    response = client.messages.create(
        model="claude-3-sonnet-20240229",
        max_tokens=1000,
        messages=[{"role": "user", "content": "Hello!"}]
    )
except AnthropicError as e:
    print(f"Anthropic API error: {e}")
except Exception as e:
    print(f"Other error: {e}")
```

---

## ⚡ Enhanced Features

### **Automatic MCP Tool Integration**

MCP tools are automatically available in Anthropic-compatible requests:

```python
# No tool definitions needed - MCP tools auto-discovered
response = client.messages.create(
    model="claude-3-sonnet-20240229",
    max_tokens=1000,
    messages=[
        {"role": "user", "content": "Read the config.json file and tell me about the providers"}
    ]
)

# Response may include automatic tool use
if response.content[0].type == "tool_use":
    print(f"Called MCP tool: {response.content[0].name}")
```

### **Load Balancing**

Multiple API keys automatically load balanced:

```json
{
  "providers": {
    "anthropic": {
      "keys": [
        {
          "value": "env.ANTHROPIC_API_KEY_1",
          "models": ["claude-3-sonnet-20240229"],
          "weight": 0.6
        },
        {
          "value": "env.ANTHROPIC_API_KEY_2",
          "models": ["claude-3-sonnet-20240229"],
          "weight": 0.4
        }
      ]
    }
  }
}
```

---

## 🧪 Testing & Validation

### **Compatibility Testing**

Test your existing Anthropic code with Bifrost:

```python
import anthropic

def test_bifrost_compatibility():
    # Test with Bifrost
    bifrost_client = anthropic.Anthropic(
        base_url="http://localhost:8080/anthropic",
        api_key=anthropic_key
    )

    # Test with direct Anthropic (for comparison)
    anthropic_client = anthropic.Anthropic(
        base_url="https://api.anthropic.com",
        api_key=anthropic_key
    )

    test_message = [{"role": "user", "content": "Hello, test!"}]

    # Both should work identically
    bifrost_response = bifrost_client.messages.create(
        model="claude-3-sonnet-20240229",
        max_tokens=100,
        messages=test_message
    )

    anthropic_response = anthropic_client.messages.create(
        model="claude-3-sonnet-20240229",
        max_tokens=100,
        messages=test_message
    )

    # Compare response structure
    assert bifrost_response.content[0].text is not None
    assert anthropic_response.content[0].text is not None

    print("✅ Bifrost Anthropic compatibility verified")

test_bifrost_compatibility()
```

### **Tool Use Testing**

```python
import anthropic

def test_tool_use():
    client = anthropic.Anthropic(
        base_url="http://localhost:8080/anthropic",
        api_key=anthropic_key
    )

    # Test tool use
    response = client.messages.create(
        model="claude-3-sonnet-20240229",
        max_tokens=1000,
        tools=[
            {
                "name": "get_time",
                "description": "Get current time",
                "input_schema": {"type": "object", "properties": {}}
            }
        ],
        messages=[
            {"role": "user", "content": "What time is it?"}
        ]
    )

    # Should include tool use
    assert any(content.type == "tool_use" for content in response.content)
    print("✅ Tool use compatibility verified")

test_tool_use()
```

---

## 🌐 Multi-Provider Support

Use multiple providers with Anthropic SDK format by prefixing model names:

```python
import anthropic

client = anthropic.Anthropic(
    base_url="http://localhost:8080/anthropic",
    api_key="dummy"  # API keys configured in Bifrost
)

# Anthropic models (default)
response1 = client.messages.create(
    model="claude-3-sonnet-20240229",
    max_tokens=100,
    messages=[{"role": "user", "content": "Hello!"}]
)

# OpenAI models via Anthropic SDK
response2 = client.messages.create(
    model="openai/gpt-4o-mini",
    max_tokens=100,
    messages=[{"role": "user", "content": "Hello!"}]
)

# Vertex models via Anthropic SDK
response3 = client.messages.create(
    model="vertex/gemini-pro",
    max_tokens=100,
    messages=[{"role": "user", "content": "Hello!"}]
)
```

---

## 🔧 Configuration

### **Bifrost Config for Anthropic**

```json
{
  "providers": {
    "anthropic": {
      "keys": [
        {
          "value": "env.ANTHROPIC_API_KEY",
          "models": [
            "claude-2.1",
            "claude-3-sonnet-20240229",
            "claude-3-haiku-20240307",
            "claude-3-opus-20240229",
            "claude-3-5-sonnet-20240620"
          ],
          "weight": 1.0
        }
      ],
      "network_config": {
        "default_request_timeout_in_seconds": 30,
        "max_retries": 2,
        "retry_backoff_initial_ms": 100,
        "retry_backoff_max_ms": 2000
      },
      "concurrency_and_buffer_size": {
        "concurrency": 3,
        "buffer_size": 10
      }
    }
  }
}
```

### **Environment Variables**

```bash
# Required
export ANTHROPIC_API_KEY="sk-ant-..."

# Optional - for enhanced features
export OPENAI_API_KEY="sk-..."  # For fallbacks
export BIFROST_LOG_LEVEL="info"
```

---

## 🚨 Common Issues & Solutions

### **Issue: "Invalid API Key"**

**Problem:** API key not being passed correctly

**Solution:**

```python
# Ensure API key is properly set
import os
client = anthropic.Anthropic(
    base_url="http://localhost:8080/anthropic",
    api_key=os.getenv("ANTHROPIC_API_KEY")  # Explicit env var
)
```

### **Issue: "Model not found"**

**Problem:** Model not configured in Bifrost

**Solution:** Add model to config.json:

```json
{
  "providers": {
    "anthropic": {
      "keys": [
        {
          "value": "env.ANTHROPIC_API_KEY",
          "models": ["claude-3-sonnet-20240229", "claude-3-haiku-20240307"],
          "weight": 1.0
        }
      ]
    }
  }
}
```

### **Issue: "Missing anthropic-version header"**

**Problem:** Required Anthropic API version header missing

**Solution:**

```python
# Add default headers for version
client = anthropic.Anthropic(
    base_url="http://localhost:8080/anthropic",
    api_key=anthropic_key,
    default_headers={"anthropic-version": "2023-06-01"}
)
```

### **Issue: "Tool schema validation error"**

**Problem:** Tool schema format incorrect

**Solution:**

```python
# Ensure proper tool schema format
tools = [
    {
        "name": "tool_name",
        "description": "Tool description",
        "input_schema": {
            "type": "object",
            "properties": {
                "param": {"type": "string", "description": "Parameter description"}
            },
            "required": ["param"]
        }
    }
]
```

---

## 📚 Related Documentation

- **[🔗 Drop-in Overview](./README.md)** - All provider integrations
- **[🌐 Endpoints](../endpoints.md)** - Complete API reference
- **[🔧 Configuration](../configuration/providers.md)** - Provider setup
- **[🔄 Migration Guide](./migration-guide.md)** - Step-by-step migration

> **🏛️ Architecture:** For Anthropic integration implementation details, see [Architecture Documentation](../../../architecture/README.md).
