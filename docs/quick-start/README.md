# 🚀 Quick Start Guide

Get up and running with Bifrost in under 30 seconds. Choose your preferred integration method and follow the step-by-step guides below.

## 📋 Quick Start Options

| Method                                         | Best For                                          | Time to Setup | Documentation              |
| ---------------------------------------------- | ------------------------------------------------- | ------------- | -------------------------- |
| **[🔧 Go Package](go-package.md)**             | Go applications, custom logic, direct integration | ~30 seconds   | Direct code integration    |
| **[🌐 HTTP Transport](http-transport.md)**     | Any language, microservices, existing APIs        | ~60 seconds   | REST API via Docker/binary |
| **[🔄 Drop-in Integrations](integrations.md)** | Migrating existing apps, zero-code changes        | ~15 seconds   | Replace existing endpoints |

---

## 🎯 Which Option Should I Choose?

### 🔧 **Go Package** - Choose if you:

- ✅ Are building a Go application
- ✅ Want direct code integration and type safety
- ✅ Need custom business logic and advanced features
- ✅ Prefer compile-time configuration validation
- ✅ Want maximum performance with minimal overhead

### 🌐 **HTTP Transport** - Choose if you:

- ✅ Use any programming language (Python, Node.js, etc.)
- ✅ Want to keep AI logic separate from your application
- ✅ Need a centralized AI gateway for multiple services
- ✅ Prefer REST API integration patterns
- ✅ Want to scale AI requests independently

### 🔄 **Drop-in Integrations** - Choose if you:

- ✅ Have existing OpenAI/Anthropic/GenAI code
- ✅ Want zero-code migration with instant benefits
- ✅ Need immediate fallback and load balancing
- ✅ Want to test Bifrost without changing your app
- ✅ Are migrating from LiteLLM or similar gateways

---

## ⚡ 30-Second Setup

### Option 1: Go Package (Fastest)

```bash
# Install package
go get github.com/maximhq/bifrost/core

# Set API key
export OPENAI_API_KEY="your-openai-key"

# Run the example
go run main.go
```

**[→ Complete Go Package Guide](go-package.md)**

### Option 2: HTTP Transport (Universal)

```bash
# Start Bifrost
docker run -p 8080:8080 -e OPENAI_API_KEY maximhq/bifrost

# Test with curl
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"provider":"openai","model":"gpt-4o-mini","messages":[{"role":"user","content":"Hello!"}]}'
```

**[→ Complete HTTP Transport Guide](http-transport.md)**

### Option 3: Drop-in Integration (Zero Code Changes)

```bash
# Start Bifrost with OpenAI compatibility
docker run -p 8080:8080 -e OPENAI_API_KEY maximhq/bifrost

# Replace OpenAI endpoint in your existing code
# Before: https://api.openai.com/v1/chat/completions
# After:  http://localhost:8080/openai/v1/chat/completions
```

**[→ Complete Integration Guide](integrations.md)**

---

## 🆚 Feature Comparison

Confused about which method offers what features? Check our comprehensive feature matrix:

**[→ Feature Comparison Table](feature-comparison.md)**

---

## 🚀 What's Next?

After completing the quick start:

1. **[🔧 Configuration](../configuration/README.md)** - Customize Bifrost for your needs
2. **[🎯 Features](../features/README.md)** - Explore advanced capabilities
3. **[📚 API Reference](../usage/README.md)** - Complete API documentation
4. **[📖 Guides](../guides/README.md)** - Detailed tutorials and examples

---

## 💡 Need Help?

- **[🔍 Troubleshooting](../guides/troubleshooting.md)** - Common issues and solutions
- **[❓ FAQ](../guides/faq.md)** - Frequently asked questions
- **[🤝 Contributing](../contributing/README.md)** - Get involved in development

---

**⚡ Ready to get started? Pick your preferred method above and follow the guide!**
