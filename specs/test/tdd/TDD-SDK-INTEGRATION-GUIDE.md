# TDD: SDK Integration Test Guide

**Version:** 1.0  
**Status:** Active  
**Source:** Phân tích `tests/integrations/python/` và `tests/integrations/typescript/`  
**Áp dụng cho:** Developers thêm SDK compatibility tests

---

## 1. Mục Đích SDK Integration Tests

SDK integration tests xác minh rằng Bifrost có thể được dùng như **drop-in replacement** cho các LLM provider SDK:

```
Client SDK (openai, anthropic, langchain, etc.)
    │  base_url = http://localhost:8080/v1
    │  api_key  = "bifrost-test-key"
    ▼
Bifrost HTTP Gateway
    │  format conversion
    ▼
Actual LLM Provider (OpenAI, Anthropic, etc.)
```

Mục tiêu: **Không thay đổi 1 dòng client code** khi chuyển từ provider trực tiếp sang qua Bifrost.

---

## 2. Python SDK Tests

### 2.1 Cấu Trúc

```
tests/integrations/python/
├── .python-version                    # Python version requirement
├── requirements.txt                   # pytest, openai, anthropic, langchain, etc.
├── run_all_tests.py                   # Main runner (parallel + reporting)
├── run_integration_tests.py           # Alternative runner
└── tests/integrations/
    ├── __init__.py
    ├── conftest.py                    # Pytest fixtures (bifrost_client, base_url)
    ├── utils/                         # Shared test utilities
    ├── test_openai.py                 # OpenAI Python SDK (179KB)
    ├── test_anthropic.py              # Anthropic Python SDK (158KB)
    ├── test_google.py                 # Google GenAI SDK (151KB)
    ├── test_azure.py                  # OpenAI SDK (Azure mode) (100KB)
    ├── test_bedrock.py                # Boto3 AWS SDK (82KB)
    ├── test_langchain.py              # LangChain Python (60KB)
    ├── test_litellm.py                # LiteLLM (35KB)
    └── test_pydanticai.py             # PydanticAI (30KB)
```

### 2.2 Pytest Fixtures (`conftest.py`)

```python
# conftest.py
import pytest
import openai
import anthropic
import os
from dotenv import load_dotenv

load_dotenv()  # Đọc .env file

BIFROST_BASE_URL = os.getenv("BIFROST_BASE_URL", "http://localhost:8080")

@pytest.fixture(scope="session")
def bifrost_openai_client():
    """OpenAI client pointing to Bifrost"""
    return openai.OpenAI(
        api_key=os.getenv("OPENAI_API_KEY", "bifrost-test-key"),
        base_url=f"{BIFROST_BASE_URL}/v1"
    )

@pytest.fixture(scope="session")
def bifrost_anthropic_client():
    """Anthropic client pointing to Bifrost"""
    return anthropic.Anthropic(
        api_key=os.getenv("ANTHROPIC_API_KEY", "bifrost-test-key"),
        base_url=f"{BIFROST_BASE_URL}"  # Anthropic SDK không cần /v1
    )

@pytest.fixture
def anyio_backend():
    return "asyncio"  # Cho async tests
```

### 2.3 Test File Pattern (OpenAI SDK)

```python
# tests/integrations/test_openai.py
import pytest
import openai

class TestOpenAIChatCompletions:
    """Chat completion API compatibility tests"""

    def test_basic_chat_completion(self, bifrost_openai_client):
        """Verify basic chat completion works identically to OpenAI SDK"""
        response = bifrost_openai_client.chat.completions.create(
            model="openai/gpt-4o",
            messages=[{"role": "user", "content": "Say hello"}]
        )

        # Verify response shape matches OpenAI spec
        assert response.id is not None
        assert response.object == "chat.completion"
        assert len(response.choices) > 0
        assert response.choices[0].message.role == "assistant"
        assert response.choices[0].message.content is not None
        assert response.usage.prompt_tokens > 0
        assert response.usage.completion_tokens > 0

    def test_streaming_chat_completion(self, bifrost_openai_client):
        """Verify SSE streaming works"""
        stream = bifrost_openai_client.chat.completions.create(
            model="openai/gpt-4o",
            messages=[{"role": "user", "content": "Count 1 to 5"}],
            stream=True
        )

        chunks = []
        for chunk in stream:
            chunks.append(chunk)
            if chunk.choices[0].delta.content:
                pass  # process content

        assert len(chunks) > 0
        # Last chunk should have finish_reason
        assert chunks[-1].choices[0].finish_reason is not None

    def test_function_calling(self, bifrost_openai_client):
        """Verify tool/function calling works"""
        tools = [{
            "type": "function",
            "function": {
                "name": "get_weather",
                "description": "Get current weather",
                "parameters": {
                    "type": "object",
                    "properties": {
                        "location": {"type": "string"}
                    },
                    "required": ["location"]
                }
            }
        }]

        response = bifrost_openai_client.chat.completions.create(
            model="openai/gpt-4o",
            messages=[{"role": "user", "content": "What's the weather in Hanoi?"}],
            tools=tools,
            tool_choice="auto"
        )

        assert response.choices[0].finish_reason in ["tool_calls", "stop"]

    def test_embeddings(self, bifrost_openai_client):
        """Verify embeddings API"""
        response = bifrost_openai_client.embeddings.create(
            model="openai/text-embedding-3-small",
            input=["Hello world", "Bifrost AI gateway"]
        )

        assert len(response.data) == 2
        assert len(response.data[0].embedding) > 0
        assert response.model is not None

    def test_error_handling(self, bifrost_openai_client):
        """Verify errors propagate correctly as OpenAI errors"""
        with pytest.raises(openai.BadRequestError):
            bifrost_openai_client.chat.completions.create(
                model="openai/gpt-4o",
                messages=[]  # Empty messages → should fail
            )
```

### 2.4 Async Test Pattern

```python
import pytest
import asyncio

@pytest.mark.anyio
async def test_async_chat_completion(bifrost_openai_client):
    """Async chat completion"""
    async_client = openai.AsyncOpenAI(
        api_key="test-key",
        base_url=f"{BIFROST_BASE_URL}/v1"
    )

    response = await async_client.chat.completions.create(
        model="openai/gpt-4o",
        messages=[{"role": "user", "content": "Hello async"}]
    )

    assert response.choices[0].message.content is not None
```

### 2.5 Environment Setup

```bash
# .env file (tạo ở tests/integrations/python/)
BIFROST_BASE_URL=http://localhost:8080
OPENAI_API_KEY=sk-your-key
ANTHROPIC_API_KEY=sk-ant-your-key
GOOGLE_API_KEY=your-google-key
AWS_ACCESS_KEY_ID=your-aws-key
AWS_SECRET_ACCESS_KEY=your-aws-secret
AWS_DEFAULT_REGION=us-east-1
AZURE_OPENAI_API_KEY=your-azure-key
AZURE_OPENAI_ENDPOINT=https://your-deployment.openai.azure.com

# Install dependencies
cd tests/integrations/python
pip install -r requirements.txt
```

### 2.6 Run Commands

```bash
# Tất cả integrations (sequential)
python run_all_tests.py

# Specific integration
python run_all_tests.py --integration openai
python run_all_tests.py --integration anthropic
python run_all_tests.py --integration google
python run_all_tests.py --integration langchain
python run_all_tests.py --integration litellm
python run_all_tests.py --integration bedrock

# Parallel (max 3 workers)
python run_all_tests.py --parallel

# Verbose
python run_all_tests.py --integration openai --verbose

# List trạng thái env vars
python run_all_tests.py --list

# Direct pytest
cd tests/integrations/python
pytest tests/integrations/test_openai.py -v
pytest tests/integrations/test_openai.py::TestOpenAIChatCompletions::test_streaming -v
pytest tests/integrations/ -k "streaming" -v  # Filter by name

# Makefile
make test-integrations-py
```

---

## 3. TypeScript SDK Tests

### 3.1 Cấu Trúc

```
tests/integrations/typescript/
├── package.json
├── tsconfig.json
├── vitest.config.ts                   # Vitest configuration
└── tests/
    ├── setup.ts                       # Global test setup
    ├── test-openai.test.ts            # openai npm package (75KB)
    ├── test-anthropic.test.ts         # @anthropic-ai/sdk (75KB)
    ├── test-langchain.test.ts         # LangChain JS (31KB)
    ├── test-azure.test.ts             # openai (Azure mode) (51KB)
    ├── test-google.test.ts            # @google/generative-ai (25KB)
    └── test-bedrock.test.ts           # @aws-sdk/client-bedrock (23KB)
```

### 3.2 Setup (`setup.ts`)

```typescript
// tests/setup.ts
import { beforeAll } from 'vitest'

const BIFROST_BASE_URL = process.env.BIFROST_BASE_URL ?? 'http://localhost:8080'

beforeAll(async () => {
    // Health check
    const res = await fetch(`${BIFROST_BASE_URL}/health`)
    if (!res.ok) {
        throw new Error(`Bifrost not healthy at ${BIFROST_BASE_URL}`)
    }
    console.log(`✓ Bifrost ready at ${BIFROST_BASE_URL}`)
})
```

### 3.3 Test File Pattern (OpenAI TS SDK)

```typescript
// tests/test-openai.test.ts
import { describe, it, expect } from 'vitest'
import OpenAI from 'openai'

const BIFROST_BASE_URL = process.env.BIFROST_BASE_URL ?? 'http://localhost:8080'

const client = new OpenAI({
    apiKey: process.env.OPENAI_API_KEY ?? 'test-key',
    baseURL: `${BIFROST_BASE_URL}/v1`,
})

describe('OpenAI TypeScript SDK Integration', () => {
    describe('Chat Completions', () => {
        it('should return valid chat completion', async () => {
            const response = await client.chat.completions.create({
                model: 'openai/gpt-4o',
                messages: [{ role: 'user', content: 'Hello from TypeScript' }],
            })

            expect(response.id).toBeTruthy()
            expect(response.object).toBe('chat.completion')
            expect(response.choices).toHaveLength(1)
            expect(response.choices[0].message.role).toBe('assistant')
            expect(response.choices[0].message.content).toBeTruthy()
            expect(response.usage?.total_tokens).toBeGreaterThan(0)
        })

        it('should support streaming', async () => {
            const stream = client.chat.completions.stream({
                model: 'openai/gpt-4o',
                messages: [{ role: 'user', content: 'Count to 3' }],
            })

            const chunks: string[] = []
            for await (const chunk of stream) {
                const content = chunk.choices[0]?.delta?.content
                if (content) chunks.push(content)
            }

            expect(chunks.length).toBeGreaterThan(0)
        })

        it('should support vision (image input)', async () => {
            const response = await client.chat.completions.create({
                model: 'openai/gpt-4o',
                messages: [{
                    role: 'user',
                    content: [
                        { type: 'text', text: 'What is in this image?' },
                        {
                            type: 'image_url',
                            image_url: { url: 'https://example.com/image.jpg' }
                        }
                    ]
                }]
            })

            expect(response.choices[0].message.content).toBeTruthy()
        })
    })

    describe('Embeddings', () => {
        it('should return embeddings', async () => {
            const response = await client.embeddings.create({
                model: 'openai/text-embedding-3-small',
                input: 'Hello Bifrost',
            })

            expect(response.data).toHaveLength(1)
            expect(response.data[0].embedding.length).toBeGreaterThan(0)
        })
    })
})
```

### 3.4 Anthropic TS SDK Pattern

```typescript
// tests/test-anthropic.test.ts
import Anthropic from '@anthropic-ai/sdk'

const client = new Anthropic({
    apiKey: process.env.ANTHROPIC_API_KEY ?? 'test-key',
    baseURL: BIFROST_BASE_URL,  // Không cần /v1 cho Anthropic
})

it('should return anthropic-format response via bifrost', async () => {
    const response = await client.messages.create({
        model: 'anthropic/claude-3-7-sonnet',
        max_tokens: 100,
        messages: [{ role: 'user', content: 'Hello' }]
    })

    expect(response.type).toBe('message')
    expect(response.role).toBe('assistant')
    expect(response.content[0].type).toBe('text')
    expect(response.usage.input_tokens).toBeGreaterThan(0)
})
```

### 3.5 LangChain Pattern

```typescript
// tests/test-langchain.test.ts
import { ChatOpenAI } from '@langchain/openai'
import { HumanMessage } from '@langchain/core/messages'

it('should work with LangChain via Bifrost', async () => {
    const model = new ChatOpenAI({
        modelName: 'openai/gpt-4o',
        openAIApiKey: process.env.OPENAI_API_KEY,
        configuration: {
            baseURL: `${BIFROST_BASE_URL}/v1`,
        }
    })

    const response = await model.invoke([
        new HumanMessage('What is 2+2?')
    ])

    expect(response.content).toBeTruthy()
})
```

### 3.6 Run Commands

```bash
# Install
cd tests/integrations/typescript && npm install

# Tất cả TS tests
npx vitest run

# Specific file
npx vitest run tests/test-openai.test.ts

# Watch mode  
npx vitest

# Verbose
npx vitest run --reporter=verbose

# Filter by name
npx vitest run --grep "streaming"

# Makefile
make test-integrations-ts
```

---

## 4. Thêm SDK Provider Mới

### 4.1 Python

```python
# 1. Thêm entry vào run_all_tests.py
self.integrations["my-sdk"] = {
    "file": "tests/integrations/test_my_sdk.py",
    "description": "My SDK integration tests",
    "env_vars": ["MY_SDK_API_KEY"],
}

# 2. Tạo test file
# tests/integrations/test_my_sdk.py

import pytest
import my_sdk  # pip install my-sdk
import os

BIFROST_BASE_URL = os.getenv("BIFROST_BASE_URL", "http://localhost:8080")

@pytest.fixture(scope="session")
def my_sdk_client():
    return my_sdk.Client(
        api_key=os.getenv("MY_SDK_API_KEY"),
        base_url=BIFROST_BASE_URL
    )

class TestMySdkIntegration:
    def test_basic_inference(self, my_sdk_client):
        # ... tests ...
```

### 4.2 TypeScript

```typescript
// 1. Thêm vào package.json dependencies
// "@my-sdk/client": "^1.0.0"

// 2. Tạo test file: tests/test-my-sdk.test.ts
import { describe, it, expect } from 'vitest'
import MyClient from '@my-sdk/client'

const client = new MyClient({
    apiKey: process.env.MY_SDK_API_KEY,
    baseURL: BIFROST_BASE_URL,
})

describe('My SDK Integration', () => {
    it('should work via Bifrost', async () => {
        // ... tests ...
    })
})
```

---

## 5. Provider Model Naming Convention

**IMPORTANT:** Khi gọi qua Bifrost, model phải có prefix provider:

```python
# ❌ WRONG: Gọi trực tiếp OpenAI
model = "gpt-4o"

# ✅ CORRECT: Qua Bifrost
model = "openai/gpt-4o"
model = "anthropic/claude-3-7-sonnet-20250219"
model = "google/gemini-2.0-flash"
model = "bedrock/anthropic.claude-3-7-sonnet-20250219-v1:0"
```

**Ngoại lệ:**
- Openai SDK qua Bifrost chấp nhận cả `gpt-4o` (no prefix) → Bifrost auto-routes đến OpenAI provider
- Anthropic SDK qua Bifrost nhận model format riêng của Anthropic

---

## 6. Error Assertion Patterns

```python
# Python: Verify error propagates as SDK-native error
import openai

with pytest.raises(openai.RateLimitError):
    # Request that should trigger rate limit via Bifrost VK
    response = bifrost_openai_client.chat.completions.create(...)

with pytest.raises(openai.AuthenticationError):
    client_with_bad_key = openai.OpenAI(api_key="invalid-key")
    client_with_bad_key.chat.completions.create(...)

with pytest.raises(openai.BadRequestError) as exc_info:
    # Invalid model
    client.chat.completions.create(model="non-existent/model", messages=[...])
assert "model" in str(exc_info.value).lower()
```

```typescript
// TypeScript
import { expect } from 'vitest'
import { APIError } from 'openai'

it('should propagate errors as SDK errors', async () => {
    await expect(
        client.chat.completions.create({
            model: 'openai/invalid-model',
            messages: [{ role: 'user', content: 'test' }]
        })
    ).rejects.toThrow(APIError)
})
```

---

## 7. Streaming Validation Patterns

```python
# Python streaming validation
def test_streaming_accumulation(bifrost_openai_client):
    """Verify streams can be accumulated into full response"""
    accumulated = ""

    stream = bifrost_openai_client.chat.completions.create(
        model="openai/gpt-4o",
        messages=[{"role": "user", "content": "Say 'hello world' exactly"}],
        stream=True
    )

    for chunk in stream:
        delta = chunk.choices[0].delta
        if delta.content:
            accumulated += delta.content

    assert len(accumulated) > 0
    assert "hello" in accumulated.lower()
```

```typescript
// TypeScript streaming with abort
it('should support early termination', async () => {
    const controller = new AbortController()

    const stream = client.chat.completions.stream({
        model: 'openai/gpt-4o',
        messages: [{ role: 'user', content: 'Tell me a very long story' }]
    }, { signal: controller.signal })

    let chunkCount = 0
    for await (const chunk of stream) {
        chunkCount++
        if (chunkCount >= 3) {
            controller.abort()
            break
        }
    }

    expect(chunkCount).toBeGreaterThanOrEqual(3)
})
```

---

## 8. LangChain Integration Patterns

```python
# Python LangChain
from langchain_openai import ChatOpenAI
from langchain_core.messages import HumanMessage, AIMessage, SystemMessage

def test_langchain_conversation(bifrost_langchain_client):
    """Multi-turn conversation via LangChain"""
    messages = [
        SystemMessage(content="You are a helpful assistant."),
        HumanMessage(content="My name is Alice."),
        AIMessage(content="Hello Alice!"),
        HumanMessage(content="What is my name?"),
    ]

    response = bifrost_langchain_client.invoke(messages)

    assert "alice" in response.content.lower()

def test_langchain_with_tools(bifrost_langchain_client):
    """LangChain tool calling"""
    from langchain_core.tools import tool

    @tool
    def get_time() -> str:
        """Get current time"""
        return "12:00 PM"

    model_with_tools = bifrost_langchain_client.bind_tools([get_time])
    response = model_with_tools.invoke("What time is it?")

    # Should either call tool or respond directly
    assert response.content or response.tool_calls
```

---

## 9. Checklist Thêm Integration Test Mới

### Python SDK Test

- [ ] Tạo file `test_<sdk>.py` trong `tests/integrations/`
- [ ] Thêm fixture `scope="session"` cho client (tránh re-create)
- [ ] Đọc API key từ `os.getenv()` với fallback test value
- [ ] Target `BIFROST_BASE_URL`, không phải provider trực tiếp
- [ ] Test các trường hợp: basic, streaming, tools, errors
- [ ] Dùng `pytest.mark.skip` nếu env var thiếu
- [ ] Thêm entry vào `run_all_tests.py::integrations`
- [ ] Thêm required `env_vars` list

### TypeScript SDK Test

- [ ] Tạo file `test-<sdk>.test.ts` trong `tests/`
- [ ] Import từ Vitest: `describe, it, expect`
- [ ] Đọc `process.env.BIFROST_BASE_URL` với fallback
- [ ] Client creation ở top-level (shared across tests trong file)
- [ ] Dùng `it.skip` hoặc `it.skipIf(!apiKey)` thay vì hard fail
- [ ] Test sync và async patterns
- [ ] Verify response schema khớp với SDK types

### Cả Hai

- [ ] Verify model naming convention (prefix `openai/`, `anthropic/`, etc.)
- [ ] Test response shape match provider spec (không chỉ `response is not null`)
- [ ] Test streaming accumulation (full content từ chunks)
- [ ] Test error propagation là SDK-native error types
- [ ] Không hardcode AWS keys/endpoints — đọc từ env
