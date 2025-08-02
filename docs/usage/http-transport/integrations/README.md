# 🔗 Drop-in API Compatibility

Complete guide to using Bifrost as a drop-in replacement for existing AI provider APIs with zero code changes.

> **💡 Quick Start:** See the [1-minute drop-in setup](../../../quickstart/http-transport.md) for immediate API replacement.

---

## 📋 Overview

Bifrost provides **drop-in API compatibility** for major AI providers:

- **Zero code changes** required in your applications
- **Same request/response formats** as original APIs
- **Automatic provider routing** and fallbacks
- **Enhanced features** (multi-provider, tools, monitoring)

Simply change your `base_url` and keep everything else the same.

---

## 🔄 Quick Migration

### **Before (Direct Provider)**

```python
import openai

client = openai.OpenAI(
    base_url="https://api.openai.com",  # Original API
    api_key="your-openai-key"
)
```

### **After (Bifrost)**

```python
import openai

client = openai.OpenAI(
    base_url="http://localhost:8080/openai",  # Point to Bifrost
    api_key="your-openai-key"
)
```

**That's it!** Your application now benefits from Bifrost's features with no other changes.

---

## 🌐 Supported Integrations

| Provider         | Endpoint Pattern  | Compatibility       | Documentation                                     |
| ---------------- | ----------------- | ------------------- | ------------------------------------------------- |
| **OpenAI**       | `/openai/v1/*`    | Full compatibility  | [OpenAI Compatible](./openai-compatible.md)       |
| **Anthropic**    | `/anthropic/v1/*` | Full compatibility  | [Anthropic Compatible](./anthropic-compatible.md) |
| **Google GenAI** | `/genai/v1beta/*` | Full compatibility  | [GenAI Compatible](./genai-compatible.md)         |
| **LiteLLM**      | `/litellm/*`      | Proxy compatibility | Coming soon                                       |

---

## ✨ Benefits of Drop-in Integration

### **📈 Enhanced Capabilities**

Your existing code gets these features automatically:

- **Multi-provider fallbacks** - Automatic failover between multiple providers, regardless of the SDK you use
- **Load balancing** - Distribute requests across multiple API keys
- **Rate limiting** - Built-in request throttling and queuing
- **Tool integration** - MCP tools available in all requests
- **Monitoring** - Prometheus metrics and observability
- **Cost optimization** - Smart routing to cheaper models

### **🔒 Security & Control**

- **Centralized API key management** - Store keys in one secure location
- **Request filtering** - Block inappropriate content or requests
- **Usage tracking** - Monitor and control API consumption
- **Access controls** - Fine-grained permissions per client

### **🎯 Operational Benefits**

- **Single deployment** - One service handles all AI providers
- **Unified logging** - Consistent request/response logging
- **Performance insights** - Cross-provider latency comparison
- **Error handling** - Graceful degradation and error recovery

---

## 🛠️ Integration Patterns

### **SDK-based Integration**

Use existing SDKs with modified base URL:

```javascript
// OpenAI SDK
import OpenAI from "openai";
const openai = new OpenAI({
  baseURL: "http://localhost:8080/openai",
  apiKey: process.env.OPENAI_API_KEY,
});

// Anthropic SDK
import Anthropic from "@anthropic-ai/sdk";
const anthropic = new Anthropic({
  baseURL: "http://localhost:8080/anthropic",
  apiKey: process.env.ANTHROPIC_API_KEY,
});
```

### **HTTP Client Integration**

For custom HTTP clients:

```python
import requests

# OpenAI format
response = requests.post(
    "http://localhost:8080/openai/v1/chat/completions",
    headers={
        "Authorization": f"Bearer {openai_key}",
        "Content-Type": "application/json"
    },
    json={
        "model": "gpt-4o-mini",
        "messages": [{"role": "user", "content": "Hello!"}]
    }
)

# Anthropic format
response = requests.post(
    "http://localhost:8080/anthropic/v1/messages",
    headers={
        "Content-Type": "application/json",
    },
    json={
        "model": "claude-3-sonnet-20240229",
        "max_tokens": 1000,
        "messages": [{"role": "user", "content": "Hello!"}]
    }
)
```

### **Environment-based Configuration**

Use environment variables for easy switching:

```bash
# Development - direct to providers
export OPENAI_BASE_URL="https://api.openai.com"
export ANTHROPIC_BASE_URL="https://api.anthropic.com"

# Production - via Bifrost
export OPENAI_BASE_URL="http://bifrost:8080/openai"
export ANTHROPIC_BASE_URL="http://bifrost:8080/anthropic"
```

---

## 🌐 Multi-Provider Usage

### **Provider-Prefixed Models**

Use multiple providers seamlessly by prefixing model names with the provider:

```python
import openai

# Single client, multiple providers
client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key="dummy"  # API keys configured in Bifrost
)

# OpenAI models
response1 = client.chat.completions.create(
    model="gpt-4o-mini", # (default OpenAI since it's OpenAI's SDK)
    messages=[{"role": "user", "content": "Hello!"}]
)

# Anthropic models using OpenAI SDK format
response2 = client.chat.completions.create(
    model="anthropic/claude-3-sonnet-20240229",
    messages=[{"role": "user", "content": "Hello!"}]
)

# Google Vertex models
response3 = client.chat.completions.create(
    model="vertex/gemini-pro",
    messages=[{"role": "user", "content": "Hello!"}]
)

# Azure OpenAI models
response4 = client.chat.completions.create(
    model="azure/gpt-4o",
    messages=[{"role": "user", "content": "Hello!"}]
)

# Local Ollama models
response5 = client.chat.completions.create(
    model="ollama/llama3.1:8b",
    messages=[{"role": "user", "content": "Hello!"}]
)
```

### **Provider-Specific Optimization**

```python
import openai

client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key="dummy"
)

def choose_optimal_model(task_type: str, content: str):
    """Choose the best model based on task requirements"""

    if task_type == "code":
        # OpenAI excels at code generation
        return "openai/gpt-4o-mini"

    elif task_type == "creative":
        # Anthropic is great for creative writing
        return "anthropic/claude-3-sonnet-20240229"

    elif task_type == "analysis" and len(content) > 10000:
        # Anthropic has larger context windows
        return "anthropic/claude-3-sonnet-20240229"

    elif task_type == "multilingual":
        # Google models excel at multilingual tasks
        return "vertex/gemini-pro"

    else:
        # Default to fastest/cheapest
        return "openai/gpt-4o-mini"

# Usage examples
code_response = client.chat.completions.create(
    model=choose_optimal_model("code", ""),
    messages=[{"role": "user", "content": "Write a Python web scraper"}]
)

creative_response = client.chat.completions.create(
    model=choose_optimal_model("creative", ""),
    messages=[{"role": "user", "content": "Write a short story about AI"}]
)
```

---

## 🚀 Deployment Scenarios

### **Microservices Architecture**

```yaml
# docker-compose.yml
version: "3.8"
services:
  bifrost:
    image: maximhq/bifrost
    ports:
      - "8080:8080"
    volumes:
      - ./data:/app/data  # Recommended: persist both config and database
    environment:
      - OPENAI_API_KEY
      - ANTHROPIC_API_KEY

  my-app:
    build: .
    environment:
      - OPENAI_BASE_URL=http://bifrost:8080/openai
      - ANTHROPIC_BASE_URL=http://bifrost:8080/anthropic
    depends_on:
      - bifrost
```

### **Kubernetes Deployment**

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: bifrost
spec:
  replicas: 3
  selector:
    matchLabels:
      app: bifrost
  template:
    metadata:
      labels:
        app: bifrost
    spec:
      containers:
        - name: bifrost
          image: maximhq/bifrost:latest
          ports:
            - containerPort: 8080
          env:
            - name: OPENAI_API_KEY
              valueFrom:
                secretKeyRef:
                  name: ai-keys
                  key: openai-key
---
apiVersion: v1
kind: Service
metadata:
  name: bifrost-service
spec:
  selector:
    app: bifrost
  ports:
    - port: 8080
      targetPort: 8080
  type: LoadBalancer
```

### **Reverse Proxy Setup**

```nginx
# nginx.conf
upstream bifrost {
    server bifrost:8080;
}

server {
    listen 80;
    server_name api.yourcompany.com;

    # OpenAI proxy
    location /openai/ {
        proxy_pass http://bifrost/openai/;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }

    # Anthropic proxy
    location /anthropic/ {
        proxy_pass http://bifrost/anthropic/;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
```

---

## 🧪 Testing Integration

### **Compatibility Testing**

Verify your application works with Bifrost:

```bash
# Test OpenAI compatibility
curl -X POST http://localhost:8080/openai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o-mini", "messages": [{"role": "user", "content": "test"}]}'

# Test Anthropic compatibility
curl -X POST http://localhost:8080/anthropic/v1/messages \
  -H "Content-Type: application/json" \
  -d '{"model": "claude-3-sonnet-20240229", "max_tokens": 100, "messages": [{"role": "user", "content": "test"}]}'
```

### **Feature Validation**

Test enhanced features through compatible APIs:

```python
import openai

client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key=openai_key
)

# This request automatically gets:
# - Fallback handling
# - MCP tool integration
# - Monitoring
# - Load balancing
response = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[
        {"role": "user", "content": "List files in current directory"}
    ]
)

# Tools are automatically available
print(response.choices[0].message.tool_calls)
```

---

## 📊 Migration Strategies

### **Gradual Migration**

1. **Start with development** - Test Bifrost in dev environment
2. **Canary deployment** - Route 5% of traffic through Bifrost
3. **Feature-by-feature** - Migrate specific endpoints gradually
4. **Full migration** - Switch all traffic to Bifrost

### **Blue-Green Migration**

```python
import os
import random

# Route traffic based on feature flag
def get_base_url(provider: str) -> str:
    if os.getenv("USE_BIFROST", "false") == "true":
        return f"http://bifrost:8080/{provider}"
    else:
        return f"https://api.{provider}.com"

# Gradual rollout
def should_use_bifrost() -> bool:
    rollout_percentage = int(os.getenv("BIFROST_ROLLOUT", "0"))
    return random.randint(1, 100) <= rollout_percentage
```

### **Feature Flag Integration**

```python
# Using feature flags for safe migration
import openai
from feature_flags import get_flag

def create_client():
    if get_flag("use_bifrost_openai"):
        base_url = "http://bifrost:8080/openai"
    else:
        base_url = "https://api.openai.com"

    return openai.OpenAI(
        base_url=base_url,
        api_key=os.getenv("OPENAI_API_KEY")
    )
```

---

## 📚 Integration Guides

Choose your provider integration:

### **🤖 OpenAI Compatible**

- Full ChatCompletion API support
- Function calling compatibility
- Vision and multimodal requests
- **[📖 OpenAI Integration Guide](./openai-compatible.md)**

### **🧠 Anthropic Compatible**

- Messages API compatibility
- Tool use integration
- System message handling
- **[📖 Anthropic Integration Guide](./anthropic-compatible.md)**

### **🔮 Google GenAI Compatible**

- GenerateContent API support
- Multi-turn conversations
- Content filtering
- **[📖 GenAI Integration Guide](./genai-compatible.md)**

### **🔄 Migration Guide**

- Step-by-step migration process
- Common pitfalls and solutions
- Performance optimization tips
- **[📖 Complete Migration Guide](./migration-guide.md)**

---

## 📚 Related Documentation

- **[🌐 HTTP Transport Overview](../README.md)** - Main HTTP transport guide
- **[🌐 Endpoints](../endpoints.md)** - Complete API reference
- **[🔧 Configuration](../configuration/)** - Provider setup and config
- **[🚀 Quick Start](../../../quickstart/http-transport.md)** - 30-second setup

> **🏛️ Architecture:** For integration design patterns and performance details, see [Architecture Documentation](../../../architecture/README.md).
