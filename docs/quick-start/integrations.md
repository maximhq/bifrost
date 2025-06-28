# 🔄 Drop-in Integrations Quick Start

Replace your existing AI provider APIs with zero code changes. Get instant fallbacks, load balancing, and cost optimization.

## 🎯 What You'll Get

- ✅ **Zero code changes** - Just update your base URL
- ✅ **Instant fallbacks** - Automatic provider redundancy
- ✅ **Better reliability** - Built-in retry and error handling
- ✅ **Cost optimization** - Key rotation and usage balancing
- ✅ **Observability** - Built-in metrics and monitoring

---

## ⚡ Prerequisites

- ✅ Existing application using OpenAI, Anthropic, or other supported providers
- ✅ Bifrost HTTP server running ([30-second setup](http-transport.md))
- ✅ Your existing API keys

---

## 🚀 Choose Your Integration

### 🤖 OpenAI → Bifrost

**Before:**

```bash
curl https://api.openai.com/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY"
```

**After:**

```bash
curl http://localhost:8080/openai/chat/completions \
  # No authorization header needed - keys in config
```

### 🧠 Anthropic → Bifrost

**Before:**

```bash
curl https://api.anthropic.com/v1/messages \
  -H "x-api-key: YOUR_API_KEY"
```

**After:**

```bash
curl http://localhost:8080/anthropic/v1/messages \
  # No API key header needed - keys in config
```

### 🔍 Google GenAI → Bifrost

**Before:**

```bash
curl https://generativelanguage.googleapis.com/v1beta/models/gemini-pro:generateContent \
  -H "Authorization: Bearer YOUR_API_KEY"
```

**After:**

```bash
curl http://localhost:8080/genai/v1beta/models/gemini-pro:generateContent \
  # No authorization header needed - keys in config
```

---

## 🔧 Setup (1 minute)

### Step 1: Configure Bifrost

Create `config.json` with your providers:

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o-mini", "gpt-4o"],
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
    }
  }
}
```

### Step 2: Start Bifrost

```bash
export OPENAI_API_KEY="your-openai-key"
export ANTHROPIC_API_KEY="your-anthropic-key"

docker run -p 8080:8080 \
  -v $(pwd)/config.json:/app/config/config.json \
  -e OPENAI_API_KEY \
  -e ANTHROPIC_API_KEY \
  maximhq/bifrost
```

### Step 3: Update Your Application

<details open>
<summary><strong>🐍 Python Applications</strong></summary>

```python
# Before
from openai import OpenAI
client = OpenAI(api_key="your-key")

# After - Just change base_url!
from openai import OpenAI
client = OpenAI(
    api_key="dummy",  # Not used, but required by SDK
    base_url="http://localhost:8080/openai"
)

# Your existing code works unchanged!
response = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Hello!"}]
)
```

</details>

<details>
<summary><strong>🟨 JavaScript/Node.js Applications</strong></summary>

```javascript
// Before
import OpenAI from "openai";
const client = new OpenAI({ apiKey: "your-key" });

// After - Just change baseURL!
import OpenAI from "openai";
const client = new OpenAI({
  apiKey: "dummy", // Not used, but required by SDK
  baseURL: "http://localhost:8080/openai",
});

// Your existing code works unchanged!
const response = await client.chat.completions.create({
  model: "gpt-4o-mini",
  messages: [{ role: "user", content: "Hello!" }],
});
```

</details>

<details>
<summary><strong>🦀 Rust Applications</strong></summary>

```rust
// Before
let client = async_openai::Client::new()
    .with_api_key("your-key");

// After - Just change base_url!
let client = async_openai::Client::new()
    .with_api_key("dummy")  // Not used, but required
    .with_api_base("http://localhost:8080/openai");

// Your existing code works unchanged!
let request = CreateChatCompletionRequestArgs::default()
    .model("gpt-4o-mini")
    .messages(messages)
    .build()?;
```

</details>

<details>
<summary><strong>🔤 cURL/REST Applications</strong></summary>

```bash
# Before
curl https://api.openai.com/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o-mini", "messages": [...]}'

# After - Just change URL!
curl http://localhost:8080/openai/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o-mini", "messages": [...]}'
  # No Authorization header needed!
```

</details>

---

## 🎯 You Did It!

🎉 **Congratulations!** You've successfully:

- ✅ **Zero-downtime migration** - No application changes needed
- ✅ **Enhanced reliability** - Built-in error handling and retries
- ✅ **Provider flexibility** - Can switch providers transparently
- ✅ **Better observability** - Metrics and monitoring included

---

## 🚀 Power Features (5 minutes each)

### Add Automatic Fallbacks

Your applications automatically get fallbacks! Configure in Bifrost:

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
    }
  }
}
```

Now make requests with automatic fallbacks:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "provider": "openai",
    "model": "gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello!"}],
    "fallbacks": [
      {"provider": "anthropic", "model": "claude-3-sonnet-20240229"}
    ]
  }'
```

### Multiple API Keys per Provider

Load balance and rotate keys automatically:

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY_1",
          "models": ["gpt-4o-mini"],
          "weight": 0.7
        },
        {
          "value": "env.OPENAI_API_KEY_2",
          "models": ["gpt-4o-mini"],
          "weight": 0.3
        }
      ]
    }
  }
}
```

### Built-in Monitoring

Access metrics automatically:

```bash
curl http://localhost:8080/metrics
```

Returns Prometheus metrics for:

- Request success/failure rates
- Latency per provider
- Key usage distribution
- Error rates and types

---

## 🔄 Provider-Specific Guides

### 🤖 OpenAI Integration

Your OpenAI applications get these **instant upgrades**:

| Feature          | Before      | After        |
| ---------------- | ----------- | ------------ |
| **Fallbacks**    | ❌ Manual   | ✅ Automatic |
| **Key Rotation** | ❌ Manual   | ✅ Automatic |
| **Retries**      | ❌ SDK only | ✅ Advanced  |
| **Monitoring**   | ❌ Manual   | ✅ Built-in  |

**Compatible Endpoints:**

- ✅ `/openai/chat/completions` - Chat completions
- ✅ `/openai/completions` - Text completions
- ✅ `/openai/embeddings` - Text embeddings

### 🧠 Anthropic Integration

Your Anthropic applications get these **instant upgrades**:

| Feature            | Before    | After          |
| ------------------ | --------- | -------------- |
| **Fallbacks**      | ❌ None   | ✅ Automatic   |
| **Vision Support** | ✅ Native | ✅ Enhanced    |
| **Tool Calling**   | ✅ Native | ✅ + MCP Tools |

**Compatible Endpoints:**

- ✅ `/anthropic/v1/messages` - Messages API

### 🔍 Google GenAI Integration

**Compatible Endpoints:**

- ✅ `/genai/v1beta/models/{model}:generateContent`
- ✅ `/genai/v1beta/models/{model}:streamGenerateContent`

---

## 💡 Migration Strategies

<details>
<summary><strong>🔄 Gradual Migration (Recommended)</strong></summary>

1. **Week 1:** Deploy Bifrost alongside existing setup
2. **Week 2:** Route 10% of traffic through Bifrost
3. **Week 3:** Route 50% of traffic through Bifrost
4. **Week 4:** Route 100% of traffic through Bifrost
5. **Week 5:** Remove old direct provider integration

**Benefits:**

- ✅ Zero risk
- ✅ Performance validation
- ✅ Easy rollback

</details>

<details>
<summary><strong>⚡ Instant Migration</strong></summary>

1. **Deploy Bifrost** with current provider configuration
2. **Update base URLs** in your applications
3. **Remove API key headers** from requests
4. **Deploy applications** with new configuration

**Benefits:**

- ✅ Immediate benefits
- ✅ Simple process
- ✅ Quick wins

</details>

<details>
<summary><strong>🧪 Blue-Green Migration</strong></summary>

1. **Set up Bifrost** in staging environment
2. **Test all endpoints** with production traffic patterns
3. **Switch DNS/load balancer** to point to Bifrost
4. **Monitor and validate** performance

**Benefits:**

- ✅ Production validation
- ✅ Instant rollback capability
- ✅ Confidence in migration

</details>

---

## 📊 Benefits You Get Immediately

### 🛡️ Reliability Improvements

| Metric             | Before          | After                     |
| ------------------ | --------------- | ------------------------- |
| **Uptime**         | Single provider | Multi-provider redundancy |
| **Error Handling** | SDK-dependent   | Advanced retry logic      |
| **Rate Limits**    | Hard failures   | Automatic key rotation    |

### 💰 Cost Optimization

- **🔑 Key Rotation:** Distribute load across multiple keys
- **📊 Usage Tracking:** Monitor spend per provider/model
- **⚖️ Load Balancing:** Optimize costs with weighted distribution

### 📈 Observability

- **📊 Metrics:** Request rates, latency, error rates
- **🔍 Tracing:** Full request lifecycle visibility
- **⚠️ Alerting:** Built-in failure detection

---

## 📚 What's Next?

### Advanced Features (10 minutes each)

| Feature                    | Documentation                                     | Value                           |
| -------------------------- | ------------------------------------------------- | ------------------------------- |
| **🛠️ MCP Tools**           | [MCP Integration](../features/mcp-integration.md) | Add external tools to any model |
| **🔌 Custom Plugins**      | [Plugin System](../features/plugins.md)           | Custom business logic           |
| **📊 Advanced Monitoring** | [Observability](../features/observability.md)     | Comprehensive monitoring        |

### Production Setup

- **[🚀 Production Deployment](../configuration/deployment/production.md)** - Scale for production traffic
- **[🔒 Security Guide](../configuration/security.md)** - Secure your deployment
- **[⚡ Performance Tuning](../benchmarks.md)** - Optimize for your workload

---

## ❓ Need Help?

- **[📱 Migration Guides](../guides/migration/)** - Provider-specific migration steps
- **[🔧 Troubleshooting](../guides/troubleshooting.md)** - Common issues and solutions
- **[❓ FAQ](../guides/faq.md)** - Frequently asked questions
- **[💬 GitHub Discussions](https://github.com/maximhq/bifrost/discussions)** - Community support

---

**Questions about your specific provider?** Check our [provider-specific migration guides](../guides/migration/) for detailed instructions.

Built with ❤️ by [Maxim](https://github.com/maximhq)
