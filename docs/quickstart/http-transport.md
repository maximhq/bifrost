# 🌐 HTTP Transport Quick Start

Get Bifrost running as an HTTP API in **15 seconds** with **zero configuration**! Perfect for any programming language.

## 🚀 Zero-Config Setup (15 seconds!)

### 1. Start Bifrost (No config needed)

```bash
# 🐳 Docker
docker pull maximhq/bifrost
docker run -p 8080:8080 maximhq/bifrost

# 🔧 OR Binary
npx bifrost@latest  # use -port flag to specify the port
```

### 2. Open the Web Interface

```bash
# 🖥️ Beautiful web UI for zero-config setup
# macOS:
open http://localhost:8080
# Linux:
xdg-open http://localhost:8080
# Windows:
start http://localhost:8080
# Or simply open http://localhost:8080 manually in your browser
```

**🎉 That's it!** Configure providers visually, monitor requests in real-time, and get analytics - all through the web interface!

---

## 📂 File-Based Configuration (Optional)

Want to use a config file instead? Bifrost automatically looks for `config.json` in your app directory:

### 1. Create `config.json` in your app directory

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

### 2. Set environment variables and start

```bash
export OPENAI_API_KEY="your-openai-api-key"

# Docker with volume mount for persistence
docker run -p 8080:8080 \
  -v $(pwd):/app/data \
  -e OPENAI_API_KEY \
  maximhq/bifrost

# OR Binary with app directory
npx bifrost@latest -port 8080
```

---

## 📁 Understanding App Directory & Docker Volumes

### **How the `-app-dir` Flag Works**

The `-app-dir` flag tells Bifrost where to store and look for data:

```bash
# Use current directory as app directory
npx bifrost@latest -app-dir .

# Use specific directory as app directory
npx bifrost@latest -app-dir /path/to/bifrost-data

# Default: current directory if no flag specified
npx bifrost@latest -port 8080
```

**What Bifrost stores in the app directory:**

- `config.json` - Configuration file (if using file-based config)
- `logs/` - Database logs and request history
- Any other persistent data

### **How Docker Volumes Work with App Directory**

Docker volumes map your host directory to Bifrost's app directory:

```bash
# Map current host directory → /app/data inside container
docker run -p 8080:8080 -v $(pwd):/app/data maximhq/bifrost

# Map specific host directory → /app/data inside container
docker run -p 8080:8080 -v /host/path/bifrost-data:/app/data maximhq/bifrost

# No volume = ephemeral storage (lost when container stops)
docker run -p 8080:8080 maximhq/bifrost
```

### **Persistence Scenarios**

| Scenario                     | Command                                                       | Result                                  |
| ---------------------------- | ------------------------------------------------------------- | --------------------------------------- |
| **Ephemeral (testing)**      | `docker run -p 8080:8080 maximhq/bifrost`                     | No persistence, configure via web UI    |
| **Persistent (recommended)** | `docker run -p 8080:8080 -v $(pwd):/app/data maximhq/bifrost` | Saves config & logs to host directory   |
| **Pre-configured**           | Create `config.json`, then run with volume                    | Starts with your existing configuration |

### **Best Practices**

- **🔧 Development**: Use `-v $(pwd):/app/data` to persist config between restarts
- **🚀 Production**: Mount dedicated volume for data persistence
- **🧪 Testing**: Run without volume for clean ephemeral instances
- **👥 Teams**: Share `config.json` in version control, mount directory with volume

### 3. Test the API

```bash
# Make your first request
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello, Bifrost!"}]
  }'
```

**🎉 Success!** You should see an AI response in JSON format.

> **📋 Note**: All Bifrost responses follow OpenAI's response structure, regardless of the underlying provider. This ensures consistent integration across different AI providers.

---

## 🔄 Drop-in Integrations (Zero Code Changes!)

**Already using OpenAI, Anthropic, or Google GenAI?** Get instant benefits with **zero code changes**:

### 🤖 **OpenAI SDK Replacement**

```python
# Before
from openai import OpenAI
client = OpenAI(api_key="your-key")

# After - Just change base_url!
from openai import OpenAI
client = OpenAI(
    api_key="dummy",  # Not used
    base_url="http://localhost:8080/openai"
)

# All your existing code works unchanged! ✨
response = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Hello!"}]
)
```

### 🧠 **Anthropic SDK Replacement**

```python
# Before
from anthropic import Anthropic
client = Anthropic(api_key="your-key")

# After - Just change base_url!
from anthropic import Anthropic
client = Anthropic(
    api_key="dummy",  # Not used
    base_url="http://localhost:8080/anthropic"
)

# All your existing code works unchanged! ✨
```

### 🔍 **Google GenAI Replacement**

```python
# Before
from google import genai
client = genai.Client(api_key="your-key")

# After - Just change base_url!
from google import genai
client = genai.Client(
    api_key="dummy",  # Not used
    http_options=genai.types.HttpOptions(
        base_url="http://localhost:8080/genai"
    )
)

# All your existing code works unchanged! ✨
```

---

## 🚀 Next Steps (30 seconds each)

### **🖥️ Add Multiple Providers via Web UI**

1. Open `http://localhost:8080` in your browser
2. Click **"Add Provider"**
3. Select **OpenAI**, enter your API key, choose models
4. Click **"Add Provider"** again
5. Select **Anthropic**, enter your API key, choose models
6. **Done!** Your providers are now load-balanced automatically

### **📡 Or Add Multiple Providers via API**

```bash
# Add OpenAI
curl -X POST http://localhost:8080/providers \
  -H "Content-Type: application/json" \
  -d '{"provider": "openai", "keys": [{"value": "env.OPENAI_API_KEY", "models": ["gpt-4o-mini"], "weight": 1.0}]}'

# Add Anthropic
curl -X POST http://localhost:8080/providers \
  -H "Content-Type: application/json" \
  -d '{"provider": "anthropic", "keys": [{"value": "env.ANTHROPIC_API_KEY", "models": ["claude-3-sonnet-20240229"], "weight": 1.0}]}'

# Set environment variables
export OPENAI_API_KEY="your-openai-key"
export ANTHROPIC_API_KEY="your-anthropic-key"
```

### **⚡ Test Different Providers**

```bash
# Use OpenAI
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "openai/gpt-4o-mini", "messages": [{"role": "user", "content": "Hello from OpenAI!"}]}'

# Use Anthropic
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "anthropic/claude-3-sonnet-20240229", "messages": [{"role": "user", "content": "Hello from Anthropic!"}]}'
```

### **🔄 Add Automatic Fallbacks**

```bash
# Request with fallback
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "openai/gpt-4o-mini",
    "messages": [{"role": "user", "content": "Hello!"}],
    "fallbacks": ["anthropic/claude-3-sonnet-20240229"]
  }'
```

---

## 🔗 Language Examples

### Python

```python
import requests

response = requests.post(
    "http://localhost:8080/v1/chat/completions",
    json={
        "model": "openai/gpt-4o-mini",
        "messages": [{"role": "user", "content": "Hello from Python!"}]
    }
)
print(response.json())
```

### JavaScript/Node.js

```javascript
const response = await fetch("http://localhost:8080/v1/chat/completions", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({
    model: "openai/gpt-4o-mini",
    messages: [{ role: "user", content: "Hello from Node.js!" }],
  }),
});
console.log(await response.json());
```

### Go

```go
response, err := http.Post(
    "http://localhost:8080/v1/chat/completions",
    "application/json",
    strings.NewReader(`{
        "model": "openai/gpt-4o-mini",
        "messages": [{"role": "user", "content": "Hello from Go!"}]
    }`)
)
```

---

## 🔧 Setup Methods Comparison

| Method          | Pros                                                 | Use When                         |
| --------------- | ---------------------------------------------------- | -------------------------------- |
| **Zero Config** | No files needed, visual setup, instant start         | Quick testing, demos, new users  |
| **File-Based**  | Version control, automation, reproducible deployment | Production, CI/CD, team setups   |
| **Docker**      | No Go installation needed, isolated environment      | Production, CI/CD, quick testing |
| **Go Binary**   | Direct execution, easier debugging                   | Development, custom builds       |

**Note:** When using file-based config, Bifrost only looks for `config.json` in your specified app directory.

---

## 💬 Need Help?

**🔗 [Join our Discord](https://getmax.im/bifrost-discord)** for real-time setup assistance and HTTP integration support!

---

## 📚 Learn More

| What You Want                  | Where to Go                                                | Time      |
| ------------------------------ | ---------------------------------------------------------- | --------- |
| **Drop-in integrations guide** | [🔄 Integrations](../usage/http-transport/integrations/)   | 5 min     |
| **Complete HTTP setup**        | [📖 HTTP Transport Usage](../usage/http-transport/)        | 10 min    |
| **Production configuration**   | [🔧 Configuration](../usage/http-transport/configuration/) | 15 min    |
| **All endpoints**              | [🎯 API Endpoints](../usage/http-transport/endpoints.md)   | Reference |
| **OpenAPI specification**      | [📊 OpenAPI Spec](../usage/http-transport/openapi.json)    | Reference |

---

## 🔄 Prefer Go Package?

If you're building a Go application and want direct integration, try the **[Go Package Quick Start](go-package.md)** instead.

---

## 💡 Why HTTP Transport?

- **🖥️ Built-in Web UI** - Visual configuration, monitoring, and analytics
- **🚀 Zero configuration** - Start instantly, configure dynamically
- **🌐 Language agnostic** - Use from Python, Node.js, PHP, etc.
- **🔄 Drop-in replacement** - Zero code changes for existing apps
- **🔗 OpenAI compatible** - All responses follow OpenAI structure
- **⚙️ Microservices ready** - Centralized AI gateway
- **📊 Production features** - Health checks, metrics, monitoring

**🎯 Ready for production? Check out [Complete HTTP Usage Guide](../usage/http-transport/) →**
