# 🌐 HTTP Transport Quick Start

Get Bifrost running as an HTTP API in 60 seconds using Docker or Go binary.

## ⚡ 60-Second Setup

### Option 1: Docker (Recommended)

```bash
# 1. Set your API key
export OPENAI_API_KEY="your-openai-api-key"

# 2. Start Bifrost (runs on port 8080)
docker run -p 8080:8080 \
  -e OPENAI_API_KEY \
  maximhq/bifrost

# 3. Test with curl
# 📖 [Complete curl examples →](../usage/examples.md#basic-chat-completion)
```

### Option 2: Go Binary

```bash
# 1. Install binary
go install github.com/maximhq/bifrost/transports/bifrost-http@latest

# 2. Create minimal config
echo '{
  "providers": {
    "openai": {
      "keys": [{"value": "env.OPENAI_API_KEY", "models": ["gpt-4o-mini"], "weight": 1.0}]
    }
  }
}' > config.json

# 3. Set API key and start
export OPENAI_API_KEY="your-openai-api-key"
bifrost-http -config config.json -port 8080

# 4. Test with curl (same as above)
```

**🎉 Success!** You should see an AI response in JSON format.

---

## 🧪 Test Different Features

### Basic Chat

**📖 [Chat with parameters example →](../usage/examples.md#chat-with-parameters)**

### Text Completion

```bash
curl -X POST http://localhost:8080/v1/text/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-3.5-turbo-instruct",
    "text": "The future of artificial intelligence is",
    "params": {
      "max_tokens": 100,
      "temperature": 0.8
    }
  }'
```

### With Fallbacks

**📖 [Fallback request example →](../usage/examples.md#request-with-fallbacks)**

---

## 🚀 Add More Providers

### Multi-Provider Configuration

<details>
<summary><strong>Configure Multiple Providers</strong></summary>

Create `config.json`:

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
    },
    "anthropic": {
      "keys": [
        {
          "value": "env.ANTHROPIC_API_KEY",
          "models": ["claude-3-sonnet-20240229"],
          "weight": 1.0
        }
      ]
    },
    "cohere": {
      "keys": [
        { "value": "env.COHERE_API_KEY", "models": ["command"], "weight": 1.0 }
      ]
    }
  }
}
```

Set environment variables:

```bash
export OPENAI_API_KEY="your-openai-key"
export ANTHROPIC_API_KEY="your-anthropic-key"
export COHERE_API_KEY="your-cohere-key"
```

Start with Docker:

**📖 [Multi-provider Docker setup →](../usage/examples.md#multi-provider-setup)**

</details>

### Using Different Providers

```bash
# OpenAI
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"provider": "openai", "model": "gpt-4o-mini", "messages": [{"role": "user", "content": "Hello"}]}'

# Anthropic
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"provider": "anthropic", "model": "claude-3-sonnet-20240229", "messages": [{"role": "user", "content": "Hello"}]}'

# Cohere
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"provider": "cohere", "model": "command", "messages": [{"role": "user", "content": "Hello"}]}'
```

---

## 🔗 Language Examples

### Python

```python
import requests

response = requests.post(
    "http://localhost:8080/v1/chat/completions",
    headers={"Content-Type": "application/json"},
    json={
        "provider": "openai",
        "model": "gpt-4o-mini",
        "messages": [{"role": "user", "content": "Hello from Python!"}]
    }
)

print(response.json())
```

### Node.js

```javascript
const response = await fetch("http://localhost:8080/v1/chat/completions", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({
    provider: "openai",
    model: "gpt-4o-mini",
    messages: [{ role: "user", content: "Hello from Node.js!" }],
  }),
});

const data = await response.json();
console.log(data);
```

### PHP

```php
$response = file_get_contents('http://localhost:8080/v1/chat/completions', false,
  stream_context_create([
    'http' => [
      'method' => 'POST',
      'header' => 'Content-Type: application/json',
      'content' => json_encode([
        'provider' => 'openai',
        'model' => 'gpt-4o-mini',
        'messages' => [['role' => 'user', 'content' => 'Hello from PHP!']]
      ])
    ]
  ])
);

echo $response;
```

---

## 🔧 System Health

### Check Status

```bash
# Health check
curl http://localhost:8080/health

# Prometheus metrics
curl http://localhost:8080/metrics
```

---

## 📚 Learn More

| Topic                      | Link                                                  | Description                           |
| -------------------------- | ----------------------------------------------------- | ------------------------------------- |
| **Complete Setup**         | [HTTP Configuration](../configuration/http-config.md) | Production configuration              |
| **All Endpoints**          | [HTTP API Reference](../usage/http-api/README.md)     | Complete API documentation            |
| **Provider Compatibility** | [Drop-in Integrations](integrations.md)               | OpenAI/Anthropic compatible endpoints |
| **MCP Tools**              | [MCP Integration](../features/mcp-integration.md)     | Add external tools and functions      |
| **Advanced Features**      | [Features Overview](../features/README.md)            | Explore all capabilities              |

---

## 🔄 Alternative: Go Package

Prefer direct Go integration? Try the **[Go Package Quick Start](go-package.md)** instead.

---

**🎯 Got it working? Move to [HTTP Configuration](../configuration/http-config.md) for production setup!**
