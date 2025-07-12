# 🔄 Migration Guide

Step-by-step guide to migrate from existing AI provider APIs to Bifrost for improved reliability, cost optimization, and enhanced features.

> **💡 Quick Start:** For immediate migration, see the [1-minute drop-in setup](../README.md) - change `base_url` and you're done!

---

## 📋 Migration Overview

### **Why Migrate to Bifrost?**

| Current Pain Point             | Bifrost Solution                         | Business Impact          |
| ------------------------------ | ---------------------------------------- | ------------------------ |
| **Single provider dependency** | Multi-provider fallbacks                 | 99.9% uptime reliability |
| **Rate limit bottlenecks**     | Load balancing + queuing                 | 3x higher throughput     |
| **Limited tool integration**   | Built-in MCP support                     | Extended AI capabilities |
| **Manual monitoring**          | Prometheus metrics                       | Operational visibility   |
| **High API costs**             | Smart routing optimization               | 20-40% cost reduction    |
| **Complex error handling**     | Automatic retries + graceful degradation | Improved user experience |

### **Migration Strategies**

1. **🟢 Drop-in Replacement** - Change base URL only (recommended)
2. **🟡 Gradual Migration** - Migrate endpoint by endpoint
3. **🟠 Canary Deployment** - Route percentage of traffic
4. **🔴 Blue-Green Migration** - Full environment switch

---

## 🚀 Strategy 1: Drop-in Replacement (Recommended)

**Best for:** Teams wanting immediate benefits with zero code changes.

### **Step 1: Deploy Bifrost**

```bash
# Option A: Docker (recommended)
docker run -d --name bifrost \
  -p 8080:8080 \
  -v $(pwd)/config.json:/app/config/config.json \
  -e OPENAI_API_KEY \
  -e ANTHROPIC_API_KEY \
  maximhq/bifrost

# Option B: Binary
npx -y @maximhq/bifrost -port 8080
```

### **Step 2: Create Configuration (Or Use UI)**

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

### **Step 3: Update Base URLs**

#### **Python (OpenAI SDK)**

```python
import openai

# Before
client = openai.OpenAI(
    base_url="https://api.openai.com",
    api_key=openai_key
)

# After
client = openai.OpenAI(
    base_url="http://localhost:8080/openai",  # ← Only change
    api_key=openai_key
)
```

#### **JavaScript (Anthropic SDK)**

```javascript
import Anthropic from "@anthropic-ai/sdk";

// Before
const anthropic = new Anthropic({
  baseURL: "https://api.anthropic.com",
  apiKey: process.env.ANTHROPIC_API_KEY,
});

// After
const anthropic = new Anthropic({
  baseURL: "http://localhost:8080/anthropic", // ← Only change
  apiKey: process.env.ANTHROPIC_API_KEY,
});
```

### **Step 4: Test & Validate**

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

**✅ Migration Complete!** Your application now benefits from:

- Multi-provider fallbacks
- Automatic load balancing
- MCP tool integration
- Prometheus monitoring

---

## 🔄 Strategy 2: Gradual Migration

**Best for:** Large applications wanting to minimize risk by migrating incrementally.

### **Phase 1: Non-critical Endpoints**

Start with development or testing endpoints:

```python
import os

def get_openai_base_url():
    if os.getenv("ENVIRONMENT") == "development":
        return "http://localhost:8080/openai"
    return "https://api.openai.com"

client = openai.OpenAI(
    base_url=get_openai_base_url(),
    api_key=openai_key
)
```

### **Phase 2: Feature-specific Migration**

Migrate specific features or user segments:

```python
def should_use_bifrost(feature: str, user_id: str) -> bool:
    # Migrate specific features first
    if feature in ["chat", "summarization"]:
        return True

    # Migrate percentage of users
    if hash(user_id) % 100 < 25:  # 25% of users
        return True

    return False

def get_client(feature: str, user_id: str):
    if should_use_bifrost(feature, user_id):
        return openai.OpenAI(base_url="http://localhost:8080/openai", api_key=key)
    else:
        return openai.OpenAI(base_url="https://api.openai.com", api_key=key)
```

### **Phase 3: Full Migration**

After validation, migrate all traffic:

```python
# Remove conditional logic, use Bifrost for all requests
client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key=openai_key
)
```

---

## 🎯 Strategy 3: Canary Deployment

**Best for:** High-traffic applications requiring careful validation.

### **Infrastructure Setup**

```yaml
# docker-compose.yml
version: "3.8"
services:
  # Production traffic (90%)
  openai-direct:
    image: your-app:latest
    environment:
      - OPENAI_BASE_URL=https://api.openai.com
    deploy:
      replicas: 9

  # Canary traffic (10%)
  openai-bifrost:
    image: your-app:latest
    environment:
      - OPENAI_BASE_URL=http://bifrost:8080/openai
    deploy:
      replicas: 1

  bifrost:
    image: maximhq/bifrost
    ports:
      - "8080:8080"
    volumes:
      - ./config.json:/app/config/config.json
```

### **Load Balancer Configuration**

```nginx
upstream app_servers {
    server openai-direct:8000 weight=9;
    server openai-bifrost:8000 weight=1;
}

server {
    listen 80;
    location / {
        proxy_pass http://app_servers;
    }
}
```

### **Monitoring & Validation**

```bash
# Monitor error rates
curl http://localhost:8080/metrics | grep bifrost_requests_total

# Compare latency
curl http://localhost:8080/metrics | grep bifrost_request_duration_seconds
```

### **Gradual Rollout**

```bash
# Increase canary traffic gradually
# Week 1: 10% canary
# Week 2: 25% canary
# Week 3: 50% canary
# Week 4: 100% canary (full migration)
```

---

## 🔵 Strategy 4: Blue-Green Migration

**Best for:** Applications requiring instant rollback capability.

### **Environment Setup**

```yaml
# Blue environment (current)
version: "3.8"
services:
  app-blue:
    image: your-app:latest
    environment:
      - OPENAI_BASE_URL=https://api.openai.com
      - ENVIRONMENT=blue
    ports:
      - "8000:8000"

  # Green environment (new)
  app-green:
    image: your-app:latest
    environment:
      - OPENAI_BASE_URL=http://bifrost:8080/openai
      - ENVIRONMENT=green
    ports:
      - "8001:8000"

  bifrost:
    image: maximhq/bifrost
    volumes:
      - ./config.json:/app/config/config.json
```

### **Traffic Switch**

```nginx
# Before migration (Blue)
upstream app {
    server app-blue:8000;
}

# After migration (Green)
upstream app {
    server app-green:8001;
}

# Instant rollback capability
upstream app {
    server app-blue:8000;  # Uncomment to rollback
    # server app-green:8001;
}
```

---

## 🧪 Testing & Validation

### **Compatibility Testing Script**

```python
import openai
import anthropic
import requests

def test_compatibility():
    """Test Bifrost compatibility with existing SDKs"""

    # Test OpenAI compatibility
    openai_client = openai.OpenAI(
        base_url="http://localhost:8080/openai",
        api_key=openai_key
    )

    try:
        response = openai_client.chat.completions.create(
            model="gpt-4o-mini",
            messages=[{"role": "user", "content": "test"}]
        )
        print("✅ OpenAI compatibility verified")
    except Exception as e:
        print(f"❌ OpenAI compatibility failed: {e}")

    # Test Anthropic compatibility
    anthropic_client = anthropic.Anthropic(
        base_url="http://localhost:8080/anthropic",
        api_key=anthropic_key
    )

    try:
        response = anthropic_client.messages.create(
            model="claude-3-sonnet-20240229",
            max_tokens=100,
            messages=[{"role": "user", "content": "test"}]
        )
        print("✅ Anthropic compatibility verified")
    except Exception as e:
        print(f"❌ Anthropic compatibility failed: {e}")

test_compatibility()
```

### **Performance Benchmarking**

```python
import time
import statistics

def benchmark_latency(base_url, provider="openai"):
    """Compare latency between direct API and Bifrost"""

    latencies = []
    for i in range(10):
        start_time = time.time()

        if provider == "openai":
            client = openai.OpenAI(base_url=base_url, api_key=openai_key)
            response = client.chat.completions.create(
                model="gpt-4o-mini",
                messages=[{"role": "user", "content": "Hello"}]
            )

        end_time = time.time()
        latencies.append(end_time - start_time)

    return {
        "mean": statistics.mean(latencies),
        "median": statistics.median(latencies),
        "min": min(latencies),
        "max": max(latencies)
    }

# Compare direct vs Bifrost
direct_stats = benchmark_latency("https://api.openai.com")
bifrost_stats = benchmark_latency("http://localhost:8080/openai")

print(f"Direct API: {direct_stats}")
print(f"Bifrost: {bifrost_stats}")
print(f"Overhead: {bifrost_stats['mean'] - direct_stats['mean']:.3f}s")
```

---

## 🔧 Production Configuration

### **High Availability Setup**

```yaml
# docker-compose.yml
version: "3.8"
services:
  bifrost-1:
    image: maximhq/bifrost
    volumes:
      - ./config.json:/app/config/config.json
    environment:
      - OPENAI_API_KEY
      - ANTHROPIC_API_KEY

  bifrost-2:
    image: maximhq/bifrost
    volumes:
      - ./config.json:/app/config/config.json
    environment:
      - OPENAI_API_KEY
      - ANTHROPIC_API_KEY

  nginx:
    image: nginx:alpine
    ports:
      - "8080:80"
    volumes:
      - ./nginx.conf:/etc/nginx/nginx.conf
    depends_on:
      - bifrost-1
      - bifrost-2
```

```nginx
# nginx.conf
upstream bifrost {
    server bifrost-1:8080;
    server bifrost-2:8080;
}

server {
    listen 80;
    location / {
        proxy_pass http://bifrost;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
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
            - name: ANTHROPIC_API_KEY
              valueFrom:
                secretKeyRef:
                  name: ai-keys
                  key: anthropic-key
          volumeMounts:
            - name: config
              mountPath: /app/config
      volumes:
        - name: config
          configMap:
            name: bifrost-config
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

---

## 📊 Migration Checklist

### **Pre-Migration**

- [ ] **Identify dependencies** - Catalog all AI API usage
- [ ] **Set up monitoring** - Baseline current performance metrics
- [ ] **Configure Bifrost** - Create config.json with all providers
- [ ] **Test compatibility** - Verify all SDKs work with Bifrost
- [ ] **Plan rollback** - Prepare quick revert procedures

### **During Migration**

- [ ] **Start with dev/staging** - Test in non-production first
- [ ] **Monitor error rates** - Watch for compatibility issues
- [ ] **Validate responses** - Ensure output quality is maintained
- [ ] **Check performance** - Monitor latency and throughput
- [ ] **Gradual rollout** - Increase traffic percentage slowly

### **Post-Migration**

- [ ] **Monitor enhanced features** - Verify fallbacks work
- [ ] **Optimize configuration** - Tune timeouts and concurrency
- [ ] **Set up alerting** - Monitor Bifrost health metrics
- [ ] **Document changes** - Update team documentation
- [ ] **Cost analysis** - Measure cost savings from optimization

---

## 🚨 Common Migration Issues

### **Issue: Authentication Errors**

**Symptoms:** 401 Unauthorized responses

**Solution:**

```python
# Ensure API keys are properly configured
import os
client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key=os.getenv("OPENAI_API_KEY")  # Explicit env var
)
```

### **Issue: Model Not Found**

**Symptoms:** 404 Model not found errors

**Solution:** Add models to config.json:

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o-mini", "gpt-4o", "gpt-4-turbo"],
          "weight": 1.0
        }
      ]
    }
  }
}
```

### **Issue: Increased Latency**

**Symptoms:** Slower response times

**Solution:** Optimize configuration:

```json
{
  "providers": {
    "openai": {
      "concurrency_and_buffer_size": {
        "concurrency": 10, // Increase from 3
        "buffer_size": 50 // Increase from 10
      }
    }
  }
}
```

### **Issue: Feature Differences**

**Symptoms:** Missing features or different behavior

**Solution:** Check feature compatibility in integration guides:

- [OpenAI Compatible](./openai-compatible.md)
- [Anthropic Compatible](./anthropic-compatible.md)
- [GenAI Compatible](./genai-compatible.md)

---

## 📈 Post-Migration Optimization

### **Cost Optimization**

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o-mini"],
          "weight": 0.8
        }
      ]
    },
    "anthropic": {
      "keys": [
        {
          "value": "env.ANTHROPIC_API_KEY",
          "models": ["claude-3-haiku-20240307"],
          "weight": 0.2
        }
      ]
    }
  }
}
```

### **Performance Tuning**

```json
{
  "providers": {
    "openai": {
      "network_config": {
        "default_request_timeout_in_seconds": 45,
        "max_retries": 3,
        "retry_backoff_initial_ms": 500,
        "retry_backoff_max_ms": 5000
      },
      "concurrency_and_buffer_size": {
        "concurrency": 15,
        "buffer_size": 100
      }
    }
  }
}
```

### **Monitoring Setup**

```yaml
# Prometheus + Grafana monitoring
version: "3.8"
services:
  prometheus:
    image: prom/prometheus
    ports:
      - "9090:9090"
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml

  grafana:
    image: grafana/grafana
    ports:
      - "3000:3000"
    environment:
      - GF_SECURITY_ADMIN_PASSWORD=admin
```

---

## 📚 Related Documentation

- **[🔗 Drop-in Overview](./README.md)** - Quick integration patterns
- **[🤖 OpenAI Compatible](./openai-compatible.md)** - OpenAI SDK migration
- **[🧠 Anthropic Compatible](./anthropic-compatible.md)** - Anthropic SDK migration
- **[🔮 GenAI Compatible](./genai-compatible.md)** - Google GenAI migration
- **[🌐 Endpoints](../endpoints.md)** - Complete API reference
- **[🔧 Configuration](../configuration/)** - Advanced configuration

> **🏛️ Architecture:** For migration architecture patterns and best practices, see [Architecture Documentation](../../../architecture/README.md).
