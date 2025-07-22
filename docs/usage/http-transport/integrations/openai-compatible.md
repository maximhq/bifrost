# 🤖 OpenAI Compatible API

Complete guide to using Bifrost as a drop-in replacement for OpenAI API with full compatibility and enhanced features.

> **💡 Quick Start:** Change `base_url` from `https://api.openai.com` to `http://localhost:8080/openai` - that's it!

---

## 📋 Overview

Bifrost provides **100% OpenAI API compatibility** with enhanced features:

- **Zero code changes** - Works with existing OpenAI SDK applications
- **Same request/response formats** - Exact OpenAI API specification
- **Enhanced capabilities** - Multi-provider fallbacks, MCP tools, monitoring
- **All endpoints supported** - Chat completions, text completions, function calling
- **Any provider under the hood** - Use any configured provider (OpenAI, Anthropic, etc.)

**Endpoint:** `POST /openai/v1/chat/completions`

> **🔄 Provider Flexibility:** While using OpenAI SDK format, you can specify any model like `"anthropic/claude-3-sonnet-20240229"` or `"openai/gpt-4o-mini"` - Bifrost will route to the appropriate provider automatically.

---

## 🔄 Quick Migration

### **Python (OpenAI SDK)**

```python
import openai

# Before - Direct OpenAI
client = openai.OpenAI(
    base_url="https://api.openai.com",
    api_key="your-openai-key"
)

# After - Via Bifrost
client = openai.OpenAI(
    base_url="http://localhost:8080/openai",  # Only change this
    api_key="your-openai-key"
)

# Everything else stays the same
response = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Hello!"}]
)
```

### **JavaScript (OpenAI SDK)**

```javascript
import OpenAI from "openai";

// Before - Direct OpenAI
const openai = new OpenAI({
  baseURL: "https://api.openai.com",
  apiKey: process.env.OPENAI_API_KEY,
});

// After - Via Bifrost
const openai = new OpenAI({
  baseURL: "http://localhost:8080/openai", // Only change this
  apiKey: process.env.OPENAI_API_KEY,
});

// Everything else stays the same
const response = await openai.chat.completions.create({
  model: "gpt-4o-mini",
  messages: [{ role: "user", content: "Hello!" }],
});
```

---

## 📊 Supported Features

### **✅ Fully Supported**

| Feature                        | Status  | Notes                    |
| ------------------------------ | ------- | ------------------------ |
| **Chat Completions**           | ✅ Full | All parameters supported |
| **Function Calling**           | ✅ Full | Original + MCP tools     |
| **Vision/Multimodal**          | ✅ Full | Images, documents, etc.  |
| **System Messages**            | ✅ Full | All message types        |
| **Temperature/Top-p**          | ✅ Full | All sampling parameters  |
| **Stop Sequences**             | ✅ Full | Custom stop tokens       |
| **Max Tokens**                 | ✅ Full | Token limit control      |
| **Presence/Frequency Penalty** | ✅ Full | Repetition control       |

### **🚀 Enhanced Features**

| Feature                      | Enhancement              | Benefit               |
| ---------------------------- | ------------------------ | --------------------- |
| **Multi-provider Fallbacks** | Automatic failover       | Higher reliability    |
| **MCP Tool Integration**     | External tools available | Extended capabilities |
| **Load Balancing**           | Multiple API keys        | Better performance    |
| **Monitoring**               | Prometheus metrics       | Observability         |
| **Rate Limiting**            | Built-in throttling      | Cost control          |

---

## 🛠️ Request Examples

### **Basic Chat Completion**

```bash
# Use OpenAI provider
curl -X POST http://localhost:8080/openai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "What is the capital of France?"}
    ]
  }'

# Use Anthropic provider via OpenAI SDK format
curl -X POST http://localhost:8080/openai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "anthropic/claude-3-sonnet-20240229",
    "messages": [
      {"role": "user", "content": "What is the capital of France?"}
    ]
  }'
```

**Response:**

```json
{
  "id": "chatcmpl-123",
  "object": "chat.completion",
  "created": 1677652288,
  "model": "gpt-4o-mini",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "The capital of France is Paris."
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 13,
    "completion_tokens": 7,
    "total_tokens": 20
  }
}
```

### **Function Calling**

```bash
curl -X POST http://localhost:8080/openai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "What files are in the current directory?"}
    ],
    "tools": [
      {
        "type": "function",
        "function": {
          "name": "list_directory",
          "description": "List files in a directory",
          "parameters": {
            "type": "object",
            "properties": {
              "path": {"type": "string", "description": "Directory path"}
            },
            "required": ["path"]
          }
        }
      }
    ]
  }'
```

**Response with Tool Call:**

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": null,
        "tool_calls": [
          {
            "id": "call_123",
            "type": "function",
            "function": {
              "name": "list_directory",
              "arguments": "{\"path\": \".\"}"
            }
          }
        ]
      }
    }
  ]
}
```

### **Vision/Multimodal**

```python
import openai

client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key=openai_key
)

response = client.chat.completions.create(
    model="gpt-4o",
    messages=[
        {
            "role": "user",
            "content": [
                {
                    "type": "text",
                    "text": "What's in this image?"
                },
                {
                    "type": "image_url",
                    "image_url": {
                        "url": "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQEAYABgAAD..."
                    }
                }
            ]
        }
    ]
)
```

### **🔊 Audio - Speech Synthesis (Text-to-Speech)**

```python
import openai

client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key=openai_key
)

# Basic speech synthesis
response = client.audio.speech.create(
    model="tts-1",
    voice="alloy",
    input="Hello! This is a test of Bifrost's speech synthesis feature."
)

# Save the audio to a file
with open("speech.mp3", "wb") as f:
    f.write(response.content)

# With different voice and format
response = client.audio.speech.create(
    model="tts-1-hd",  # Higher quality model
    voice="nova",
    input="The weather today is sunny with a chance of rain.",
    response_format="wav"
)

with open("weather.wav", "wb") as f:
    f.write(response.content)
```

**JavaScript Example:**

```javascript
import OpenAI from "openai";
import fs from "fs";

const openai = new OpenAI({
  baseURL: "http://localhost:8080/openai",
  apiKey: process.env.OPENAI_API_KEY,
});

// Generate speech
const mp3 = await openai.audio.speech.create({
  model: "tts-1",
  voice: "alloy",
  input: "Today is a wonderful day to build something people love!"
});

// Save to file
const buffer = Buffer.from(await mp3.arrayBuffer());
await fs.promises.writeFile("speech.mp3", buffer);
```

**cURL Example:**

```bash
curl -X POST http://localhost:8080/openai/v1/audio/speech \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "tts-1",
    "input": "The quick brown fox jumped over the lazy dog.",
    "voice": "alloy"
  }' \
  --output speech.mp3
```

**Available Voices:**
- `alloy` - Neutral, balanced voice
- `echo` - Clear, expressive voice  
- `fable` - Warm, narrative voice
- `onyx` - Deep, authoritative voice
- `nova` - Vibrant, engaging voice
- `shimmer` - Gentle, soothing voice

**Supported Audio Formats:**
- `mp3` (default) - Most compatible
- `opus` - Internet streaming
- `aac` - Digital audio compression
- `flac` - Lossless audio
- `wav` - Uncompressed audio
- `pcm` - Raw audio data

### **🎤 Audio - Transcription (Speech-to-Text)**

```python
import openai

client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key=openai_key
)

# Basic transcription
audio_file = open("speech.mp3", "rb")
transcript = client.audio.transcriptions.create(
    model="whisper-1",
    file=audio_file
)
print(transcript.text)

# With additional parameters
audio_file = open("speech.wav", "rb")
transcript = client.audio.transcriptions.create(
    model="whisper-1",
    file=audio_file,
    language="en",  # Optional: specify language
    prompt="This is a transcription of a weather report.",  # Optional: context
    response_format="verbose_json",  # Get detailed response
    temperature=0.0  # More deterministic output
)

print(f"Text: {transcript.text}")
print(f"Language: {transcript.language}")
print(f"Duration: {transcript.duration}s")

# Process segments for timing information
for segment in transcript.segments:
    print(f"[{segment.start:.2f}s - {segment.end:.2f}s]: {segment.text}")
```

**JavaScript Example:**

```javascript
import OpenAI from "openai";
import fs from "fs";

const openai = new OpenAI({
  baseURL: "http://localhost:8080/openai",
  apiKey: process.env.OPENAI_API_KEY,
});

// Transcribe audio file
const transcription = await openai.audio.transcriptions.create({
  file: fs.createReadStream("audio.mp3"),
  model: "whisper-1",
  language: "en",
  response_format: "verbose_json"
});

console.log(`Transcription: ${transcription.text}`);
console.log(`Language: ${transcription.language}`);
console.log(`Duration: ${transcription.duration}s`);
```

**cURL Example:**

```bash
curl -X POST http://localhost:8080/openai/v1/audio/transcriptions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -F file="@audio.mp3" \
  -F model="whisper-1" \
  -F language="en" \
  -F response_format="json"
```

**Response Formats:**
- `json` (default) - Basic JSON with text
- `text` - Plain text only
- `srt` - SubRip subtitle format
- `verbose_json` - Detailed JSON with timing and metadata
- `vtt` - WebVTT subtitle format

**Supported Audio Formats:**
- `mp3`, `mp4`, `mpeg`, `mpga`, `m4a`, `wav`, `webm`
- File uploads are limited to 25 MB

### **🎵 Audio Round-Trip Example**

Complete workflow from text → speech → text:

```python
import openai
import tempfile

client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key=openai_key
)

# Step 1: Convert text to speech
original_text = "The quick brown fox jumps over the lazy dog."
speech_response = client.audio.speech.create(
    model="tts-1",
    voice="alloy",
    input=original_text,
    response_format="wav"
)

# Step 2: Save speech to temporary file
with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as temp_audio:
    temp_audio.write(speech_response.content)
    temp_audio_path = temp_audio.name

# Step 3: Transcribe speech back to text
with open(temp_audio_path, "rb") as audio_file:
    transcription = client.audio.transcriptions.create(
        model="whisper-1",
        file=audio_file
    )

print(f"Original: {original_text}")
print(f"Transcribed: {transcription.text}")

# Cleanup
import os
os.unlink(temp_audio_path)
```

---

## 🔧 Advanced Usage

### **Streaming Responses**

```python
import openai

client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key=openai_key
)

# Note: Streaming not yet supported
# This will work but return complete response
stream = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Tell me a story"}],
    stream=True
)

for chunk in stream:
    if chunk.choices[0].delta.content is not None:
        print(chunk.choices[0].delta.content, end="")
```

### **Custom Headers**

```python
import openai

client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key=openai_key,
    default_headers={
        "X-Organization": "your-org-id",
        "X-Environment": "production"
    }
)
```

### **Error Handling**

```python
import openai
from openai import OpenAIError

client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key=openai_key
)

try:
    response = client.chat.completions.create(
        model="gpt-4o-mini",
        messages=[{"role": "user", "content": "Hello!"}]
    )
except OpenAIError as e:
    print(f"OpenAI API error: {e}")
except Exception as e:
    print(f"Other error: {e}")
```

---

## ⚡ Enhanced Features

### **Automatic MCP Tool Integration**

MCP tools are automatically available in OpenAI-compatible requests:

```python
# No tool definitions needed - MCP tools auto-discovered
response = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[
        {"role": "user", "content": "Read the config.json file and tell me about the providers"}
    ]
)

# Response may include automatic tool calls
if response.choices[0].message.tool_calls:
    for tool_call in response.choices[0].message.tool_calls:
        print(f"Called: {tool_call.function.name}")
```

### **Load Balancing**

Multiple API keys automatically load balanced:

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

---

## 🧪 Testing & Validation

### **Compatibility Testing**

Test your existing OpenAI code with Bifrost:

```python
import openai

def test_bifrost_compatibility():
    # Test with Bifrost
    bifrost_client = openai.OpenAI(
        base_url="http://localhost:8080/openai",
        api_key=openai_key
    )

    # Test with direct OpenAI (for comparison)
    openai_client = openai.OpenAI(
        base_url="https://api.openai.com",
        api_key=openai_key
    )

    test_message = [{"role": "user", "content": "Hello, test!"}]

    # Both should work identically
    bifrost_response = bifrost_client.chat.completions.create(
        model="gpt-4o-mini",
        messages=test_message
    )

    openai_response = openai_client.chat.completions.create(
        model="gpt-4o-mini",
        messages=test_message
    )

    # Compare response structure
    assert bifrost_response.choices[0].message.content is not None
    assert openai_response.choices[0].message.content is not None

    print("✅ Bifrost OpenAI compatibility verified")

test_bifrost_compatibility()
```

### **Performance Comparison**

```python
import time
import openai

def benchmark_response_time(client, name):
    start_time = time.time()

    response = client.chat.completions.create(
        model="gpt-4o-mini",
        messages=[{"role": "user", "content": "Hello!"}]
    )

    end_time = time.time()
    print(f"{name} response time: {end_time - start_time:.2f}s")
    return response

# Compare Bifrost vs Direct OpenAI
bifrost_client = openai.OpenAI(base_url="http://localhost:8080/openai", api_key=key)
openai_client = openai.OpenAI(base_url="https://api.openai.com", api_key=key)

benchmark_response_time(bifrost_client, "Bifrost")
benchmark_response_time(openai_client, "Direct OpenAI")
```

---

## 🌐 Multi-Provider Support

Use multiple providers with OpenAI SDK format by prefixing model names:

```python
import openai

client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key="dummy"  # API keys configured in Bifrost
)

# OpenAI models (default)
response1 = client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "Hello!"}]
)

# Anthropic models via OpenAI SDK
response2 = client.chat.completions.create(
    model="anthropic/claude-3-sonnet-20240229",
    messages=[{"role": "user", "content": "Hello!"}]
)

# Vertex models via OpenAI SDK
response3 = client.chat.completions.create(
    model="vertex/gemini-pro",
    messages=[{"role": "user", "content": "Hello!"}]
)
```

---

## 🔧 Configuration

### **Bifrost Config for OpenAI**

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": [
            "gpt-3.5-turbo",
            "gpt-4",
            "gpt-4o",
            "gpt-4o-mini",
            "gpt-4-turbo",
            "gpt-4-vision-preview"
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
        "concurrency": 5,
        "buffer_size": 20
      }
    }
  }
}
```

### **Environment Variables**

```bash
# Required
export OPENAI_API_KEY="sk-..."

# Optional - for enhanced features
export ANTHROPIC_API_KEY="sk-ant-..."  # For fallbacks
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
client = openai.OpenAI(
    base_url="http://localhost:8080/openai",
    api_key=os.getenv("OPENAI_API_KEY")  # Explicit env var
)
```

### **Issue: "Model not found"**

**Problem:** Model not configured in Bifrost

**Solution:** Add model to config.json:

```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "value": "env.OPENAI_API_KEY",
          "models": ["gpt-4o-mini", "gpt-4o", "gpt-4-turbo"], // Add your model
          "weight": 1.0
        }
      ]
    }
  }
}
```

### **Issue: "Connection refused"**

**Problem:** Bifrost not running or wrong port

**Solution:**

```bash
# Check Bifrost is running
curl http://localhost:8080/metrics

# If not running, start it
docker run -p 8080:8080 maximhq/bifrost
```

### **Issue: "Timeout errors"**

**Problem:** Network timeout too low

**Solution:** Increase timeout in config.json:

```json
{
  "providers": {
    "openai": {
      "network_config": {
        "default_request_timeout_in_seconds": 60 // Increase from 30
      }
    }
  }
}
```

---

## 📚 Related Documentation

- **[🔗 Drop-in Overview](./README.md)** - All provider integrations
- **[🌐 Endpoints](../endpoints.md)** - Complete API reference
- **[🔧 Configuration](../configuration/providers.md)** - Provider setup
- **[🔄 Migration Guide](./migration-guide.md)** - Step-by-step migration

> **🏛️ Architecture:** For OpenAI integration implementation details, see [Architecture Documentation](../../../architecture/README.md).
