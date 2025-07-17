# 🔮 Google GenAI Compatible API

Complete guide to using Bifrost as a drop-in replacement for Google GenAI API with full compatibility and enhanced features.

> **💡 Quick Start:** Change `base_url` from `https://generativelanguage.googleapis.com` to `http://localhost:8080/genai` - that's it!

---

## 📋 Overview

Bifrost provides **100% Google GenAI API compatibility** with enhanced features:

- **Zero code changes** - Works with existing Google GenAI SDK applications
- **Same request/response formats** - Exact Google GenAI API specification
- **Enhanced capabilities** - Multi-provider fallbacks, MCP tools, monitoring
- **Full Gemini model support** - All Gemini models and features
- **Any provider under the hood** - Use any configured provider (Google, OpenAI, Anthropic, etc.)

**Endpoint:** `POST /genai/v1beta/models/{model}:generateContent`

> **🔄 Provider Flexibility:** While using Google GenAI SDK format, you can specify any model like `"gemini-pro"` (uses Google) or `"openai/gpt-4o-mini"` (uses OpenAI) - Bifrost will route to the appropriate provider automatically.

---

## 🔄 Quick Migration

### **Python (Google GenAI SDK)**

```python
from google import genai
from google.genai.types import HttpOptions

# Before - Direct Google GenAI
client = genai.Client(api_key="your-google-api-key")

# After - Via Bifrost
client = genai.Client(
    api_key="your-google-api-key",
    http_options=HttpOptions(base_url="http://localhost:8080/genai")  # Only change this
)

# Everything else stays the same
response = client.models.generate_content(
    model="gemini-pro",
    contents="Hello!"
)
```

### **JavaScript (Google GenAI SDK)**

```javascript
import { GoogleGenerativeAI } from "@google/generative-ai";

// Before - Direct Google GenAI
const genAI = new GoogleGenerativeAI(process.env.GOOGLE_API_KEY);

// After - Via Bifrost
const genAI = new GoogleGenerativeAI(process.env.GOOGLE_API_KEY, {
  baseUrl: "http://localhost:8080/genai", // Only change this
});

// Everything else stays the same
const model = genAI.getGenerativeModel({ model: "gemini-pro" });
const response = await model.generateContent("Hello!");
```

---

## 📊 Supported Features

### **✅ Fully Supported**

| Feature                 | Status     | Notes                           |
| ----------------------- | ---------- | ------------------------------- |
| **GenerateContent**     | ✅ Full    | All parameters supported        |
| **Multi-turn Chat**     | ✅ Full    | Conversation history            |
| **System Instructions** | ✅ Full    | Model behavior control          |
| **Vision/Multimodal**   | ✅ Full    | Images, videos, documents       |
| **Streaming**           | ✅ Full    | Currently returns full response |
| **Safety Settings**     | ✅ Full    | Content filtering               |
| **Generation Config**   | ✅ Full    | Temperature, top-k, etc.        |
| **Function Calling**    | ✅ Full    | Google + MCP tools              |

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

### **Basic Content Generation**

```bash
# Use Google provider
curl -X POST http://localhost:8080/genai/v1beta/models/gemini-pro:generateContent \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{
      "parts": [{"text": "What is the capital of France?"}],
      "role": "user"
    }]
  }'

# Use OpenAI provider via GenAI SDK format
curl -X POST http://localhost:8080/genai/v1beta/models/openai/gpt-4o-mini:generateContent \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{
      "parts": [{"text": "What is the capital of France?"}],
      "role": "user"
    }]
  }'
```

**Response:**

```json
{
  "candidates": [
    {
      "content": {
        "parts": [
          {
            "text": "The capital of France is Paris."
          }
        ],
        "role": "model"
      },
      "finishReason": "STOP",
      "index": 0
    }
  ],
  "usageMetadata": {
    "promptTokenCount": 7,
    "candidatesTokenCount": 7,
    "totalTokenCount": 14
  }
}
```

### **Multi-turn Conversation**

```bash
curl -X POST http://localhost:8080/genai/v1beta/models/gemini-pro:generateContent \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [
      {
        "parts": [{"text": "Hello, who are you?"}],
        "role": "user"
      },
      {
        "parts": [{"text": "I am Gemini, a large language model."}],
        "role": "model"
      },
      {
        "parts": [{"text": "What can you help me with?"}],
        "role": "user"
      }
    ]
  }'
```

### **Vision/Multimodal**

```bash
curl -X POST http://localhost:8080/genai/v1beta/models/gemini-pro-vision:generateContent \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{
      "parts": [
        {"text": "What is in this image?"},
        {
          "inlineData": {
            "mimeType": "image/jpeg",
            "data": "/9j/4AAQSkZJRgABAQEAYABgAAD..."
          }
        }
      ],
      "role": "user"
    }]
  }'
```

### **Function Calling**

```bash
curl -X POST http://localhost:8080/genai/v1beta/models/gemini-pro:generateContent \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{
      "parts": [{"text": "What is the weather like in Paris?"}],
      "role": "user"
    }],
    "tools": [{
      "functionDeclarations": [{
        "name": "get_weather",
        "description": "Get weather information for a location",
        "parameters": {
          "type": "object",
          "properties": {
            "location": {"type": "string", "description": "City name"}
          },
          "required": ["location"]
        }
      }]
    }]
  }'
```

**Response with Function Call:**

```json
{
  "candidates": [
    {
      "content": {
        "parts": [
          {
            "functionCall": {
              "name": "get_weather",
              "args": {
                "location": "Paris"
              }
            }
          }
        ],
        "role": "model"
      },
      "finishReason": "STOP"
    }
  ]
}
```

---

## 🔧 Advanced Usage

### **System Instructions**

```python
from google import genai
from google.genai.types import HttpOptions, GenerateContentConfig

client = genai.Client(
    api_key="your-google-api-key",
    http_options=HttpOptions(base_url="http://localhost:8080/genai")
)

response = client.models.generate_content(
    model="gemini-pro",
    contents="What is the capital of France?",
    config=GenerateContentConfig(
        system_instruction="You are a helpful assistant that answers questions about geography."
    )
)
```

### **Generation Configuration**

```python
from google import genai
from google.genai.types import HttpOptions, GenerateContentConfig

client = genai.Client(
    api_key="your-google-api-key",
    http_options=HttpOptions(base_url="http://localhost:8080/genai")
)

response = client.models.generate_content(
    model="gemini-pro",
    contents="Tell me a story",
    config=GenerateContentConfig(
        candidate_count=1,
        max_output_tokens=1000,
        temperature=0.7,
        top_p=0.8,
        top_k=40,
        stop_sequences=["END"]
    )
)
```

### **Safety Settings**

```python
from google import genai
from google.genai.types import HttpOptions, GenerateContentConfig, SafetySetting

client = genai.Client(
    api_key="your-google-api-key",
    http_options=HttpOptions(base_url="http://localhost:8080/genai")
)

safety_settings = [
    SafetySetting(
        category="HARM_CATEGORY_HARASSMENT",
        threshold="BLOCK_MEDIUM_AND_ABOVE"
    ),
    SafetySetting(
        category="HARM_CATEGORY_HATE_SPEECH",
        threshold="BLOCK_MEDIUM_AND_ABOVE"
    )
]

response = client.models.generate_content(
    model="gemini-pro",
    contents="Your content here",
    config=GenerateContentConfig(safety_settings=safety_settings)
)
```

### **Error Handling**

```python
from google import genai
from google.genai.types import HttpOptions
from google.api_core import exceptions

try:
    client = genai.Client(
        api_key=google_api_key,
        http_options=HttpOptions(base_url="http://localhost:8080/genai")
    )
    response = client.models.generate_content(
        model="gemini-pro",
        contents="Hello!"
    )
except exceptions.InvalidArgument as e:
    print(f"Invalid argument: {e}")
except exceptions.PermissionDenied as e:
    print(f"Permission denied: {e}")
except Exception as e:
    print(f"Other error: {e}")
```

---

## ⚡ Enhanced Features

### **Automatic MCP Tool Integration**

MCP tools are automatically available in GenAI-compatible requests:

```python
from google import genai
from google.genai.types import HttpOptions, Tool, FunctionDeclaration, Schema

client = genai.Client(
    api_key="your-google-api-key",
    http_options=HttpOptions(base_url="http://localhost:8080/genai")
)

# Define tools if needed (or use auto-discovered MCP tools)
tools = [
    Tool(function_declarations=[
        FunctionDeclaration(
            name="list_files",
            description="List files in a directory",
            parameters=Schema(
                type="OBJECT",
                properties={
                    "path": Schema(type="STRING", description="Directory path")
                },
                required=["path"]
            )
        )
    ])
]

response = client.models.generate_content(
    model="gemini-pro",
    contents="List the files in the current directory and tell me about the project structure",
    config=GenerateContentConfig(tools=tools)
)

# Check for function calls in response
if response.candidates[0].content.parts[0].function_call:
    function_call = response.candidates[0].content.parts[0].function_call
    print(f"Called tool: {function_call.name}")
```

### **Multi-provider Support**

Use multiple providers with Google GenAI SDK format by prefixing model names:

```python
from google import genai
from google.genai.types import HttpOptions

client = genai.Client(
    api_key="dummy",  # API keys configured in Bifrost
    http_options=HttpOptions(base_url="http://localhost:8080/genai")
)

# Google models (default)
response1 = client.models.generate_content(
    model="gemini-pro",
    contents="Hello!"
)

# OpenAI models via GenAI SDK
response2 = client.models.generate_content(
    model="openai/gpt-4o-mini",
    contents="Hello!"
)

# Anthropic models via GenAI SDK
response3 = client.models.generate_content(
    model="anthropic/claude-3-sonnet-20240229",
    contents="Hello!"
)
````

---

## 🧪 Testing & Validation

### **Compatibility Testing**

Test your existing Google GenAI code with Bifrost:

```python
from google import genai
from google.genai.types import HttpOptions

def test_bifrost_compatibility():
    # Test with Bifrost
    bifrost_client = genai.Client(
        api_key=google_api_key,
        http_options=HttpOptions(base_url="http://localhost:8080/genai")
    )

    # Test with direct Google GenAI (for comparison)
    google_client = genai.Client(
        api_key=google_api_key
    )

    test_prompt = "Hello, test!"

    # Both should work identically
    bifrost_response = bifrost_client.models.generate_content(
        model="gemini-pro",
        contents=test_prompt
    )
    google_response = google_client.models.generate_content(
        model="gemini-pro",
        contents=test_prompt
    )

    # Compare response structure
    assert bifrost_response.candidates[0].content.parts[0].text is not None
    assert google_response.candidates[0].content.parts[0].text is not None

    print("✅ Bifrost Google GenAI compatibility verified")

test_bifrost_compatibility()
```

### **Function Calling Testing**

```python
from google import genai
from google.genai.types import HttpOptions, Tool, FunctionDeclaration, Schema

def test_function_calling():
    client = genai.Client(
        api_key=google_api_key,
        http_options=HttpOptions(base_url="http://localhost:8080/genai")
    )

    # Define a test tool
    tools = [
        Tool(function_declarations=[
            FunctionDeclaration(
                name="get_time",
                description="Get current time",
                parameters=Schema(
                    type="OBJECT",
                    properties={},
                    required=[]
                )
            )
        ])
    ]

    response = client.models.generate_content(
        model="gemini-pro",
        contents="What time is it?",
        config=GenerateContentConfig(tools=tools)
    )

    # Should include function call
    if response.candidates[0].content.parts[0].function_call:
        print("✅ Function calling compatibility verified")
    else:
        print("⚠️ Function calling not triggered")

test_function_calling()
```

---

## 🌐 Multi-Provider Support

Use multiple providers with Google GenAI SDK format by prefixing model names:

```python
from google import genai

genai.configure(
    api_key="dummy",  # API keys configured in Bifrost
    client_options={"api_endpoint": "http://localhost:8080/genai"}
)

# Google models (default)
model1 = genai.GenerativeModel('gemini-pro')
response1 = model1.generate_content("Hello!")

# OpenAI models via GenAI SDK
model2 = genai.GenerativeModel('openai/gpt-4o-mini')
response2 = model2.generate_content("Hello!")

# Anthropic models via GenAI SDK
model3 = genai.GenerativeModel('anthropic/claude-3-sonnet-20240229')
response3 = model3.generate_content("Hello!")
```

---

## 🔧 Configuration

### **Bifrost Config for Google GenAI**

```json
{
  "providers": {
    "vertex": {
      "keys": [],
      "meta_config": {
        "project_id": "env.VERTEX_PROJECT_ID",
        "region": "us-central1",
        "auth_credentials": "env.VERTEX_CREDENTIALS"
      },
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
# Required for Google GenAI
export GOOGLE_API_KEY="your-api-key"

# OR for Vertex AI
export VERTEX_PROJECT_ID="your-project-id"
export VERTEX_CREDENTIALS="path/to/service-account.json"

# Optional - for enhanced features
export OPENAI_API_KEY="sk-..."  # For fallbacks
export BIFROST_LOG_LEVEL="info"
```

---

## 🚨 Common Issues & Solutions

### **Issue: "API Key not valid"**

**Problem:** Google API key not being passed correctly

**Solution:**

```python
# Ensure API key is properly set
import os
genai.configure(
    api_key=os.getenv("GOOGLE_API_KEY"),  # Explicit env var
    client_options={"api_endpoint": "http://localhost:8080/genai"}
)
```

### **Issue: "Model not found"**

**Problem:** Gemini model not available in your region/project

**Solution:** Configure fallback in config.json:

```json
{
  "providers": {
    "vertex": {
      "meta_config": {
        "project_id": "env.VERTEX_PROJECT_ID",
        "region": "us-central1"
      }
    },
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

### **Issue: "Authentication failed"**

**Problem:** Service account credentials not configured

**Solution:**

```bash
# Set up service account for Vertex AI
export GOOGLE_APPLICATION_CREDENTIALS="path/to/service-account.json"
export VERTEX_PROJECT_ID="your-project-id"
```

### **Issue: "Generation failed"**

**Problem:** Content blocked by safety filters

**Solution:**

```python
# Adjust safety settings
safety_settings = [
    {
        "category": "HARM_CATEGORY_HARASSMENT",
        "threshold": "BLOCK_ONLY_HIGH"  # Less restrictive
    }
]

response = model.generate_content(
    "Your content",
    safety_settings=safety_settings
)
```

---

## 📚 Related Documentation

- **[🔗 Drop-in Overview](./README.md)** - All provider integrations
- **[🌐 Endpoints](../endpoints.md)** - Complete API reference
- **[🔧 Configuration](../configuration/providers.md)** - Provider setup
- **[🔄 Migration Guide](./migration-guide.md)** - Step-by-step migration

> **🏛️ Architecture:** For Google GenAI integration implementation details, see [Architecture Documentation](../../../architecture/README.md).
