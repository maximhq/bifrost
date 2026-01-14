# Bifrost TypeScript Integration Tests

TypeScript/JavaScript integration test suite for testing AI providers through Bifrost proxy. This test suite uses Vitest and provides comprehensive coverage across multiple AI SDKs.

## Quick Start

```bash
# 1. Install dependencies
cd bifrost/tests/integrations/typescript
npm install

# 2. Set environment variables
export BIFROST_BASE_URL="http://localhost:8080"
export OPENAI_API_KEY="your-key"
export ANTHROPIC_API_KEY="your-key"
export GEMINI_API_KEY="your-key"

# 3. Run tests
npm test                           # All tests
npm test -- tests/test-openai.test.ts  # Specific SDK
npm test -- -t "Simple Chat"       # By pattern
```

## Architecture Overview

The TypeScript integration tests use the same centralized configuration as the Python tests, routing all AI requests through Bifrost:

```text
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   Test Client   │───▶│  Bifrost Gateway │───▶│  AI Provider    │
│  (TypeScript)   │    │  localhost:8080  │    │  (OpenAI, etc.) │
└─────────────────┘    └─────────────────┘    └─────────────────┘
```

## Supported SDKs

| SDK | Package | Features |
|-----|---------|----------|
| **OpenAI** | `openai` | Chat, Streaming, Tools, Vision, Speech, Embeddings |
| **Anthropic** | `@anthropic-ai/sdk` | Chat, Streaming, Tools, Vision, Thinking |
| **Google GenAI** | `@google/generative-ai` | Chat, Streaming, Tools, Vision, Embeddings |
| **LangChain.js** | `@langchain/*` | Chat, Streaming, Tools, Structured Output |

## Test Scenarios

Each SDK test file covers these scenarios where supported:

### Core Chat
1. **Simple Chat** - Basic single-message conversations
2. **Multi-turn Conversation** - Context retention across messages
3. **Streaming Chat** - Real-time streaming responses

### Tool Calling
4. **Single Tool Call** - Basic function calling
5. **Multiple Tool Calls** - Multiple tools in single request
6. **End-to-End Tool Calling** - Complete workflow with results

### Vision
7. **Image URL** - Image analysis from URLs
8. **Image Base64** - Image analysis from base64 data
9. **Multiple Images** - Multi-image comparison

### Advanced Features
10. **Speech Synthesis** - Text-to-speech (OpenAI)
11. **Transcription** - Speech-to-text (OpenAI)
12. **Embeddings** - Text-to-vector conversion
13. **Structured Output** - Schema-based responses
14. **Thinking/Reasoning** - Extended reasoning modes

## Directory Structure

```text
typescript/
├── package.json              # Dependencies and scripts
├── tsconfig.json             # TypeScript configuration
├── vitest.config.ts          # Vitest test configuration
├── config.yml                # Shared config (mirrors ../python/config.yml)
├── README.md                 # This file
├── src/
│   └── utils/
│       ├── config-loader.ts  # Configuration loading
│       ├── common.ts         # Test data and assertions
│       ├── parametrize.ts    # Cross-provider utilities
│       └── index.ts          # Barrel export
└── tests/
    ├── setup.ts              # Global test setup
    ├── test-openai.test.ts   # OpenAI SDK tests
    ├── test-anthropic.test.ts # Anthropic SDK tests
    ├── test-google.test.ts   # Google GenAI tests
    └── test-langchain.test.ts # LangChain.js tests
```

## Configuration

### Shared Configuration

The TypeScript tests share configuration with Python tests. The `config.yml` file mirrors the Python test configuration to ensure consistency:

```bash
# Both test suites use the same configuration format
tests/integrations/typescript/config.yml  # TypeScript tests
tests/integrations/python/config.yml      # Python tests
```

This ensures consistent:
- Provider model configurations
- Scenario capability mappings
- API settings (timeouts, retries)
- Virtual key settings

### Environment Variables

**Required:**
```bash
export BIFROST_BASE_URL="http://localhost:8080"
```

**Provider API Keys (at least one required):**
```bash
export OPENAI_API_KEY="sk-..."
export ANTHROPIC_API_KEY="sk-ant-..."
export GEMINI_API_KEY="AIza..."
```

**Optional:**
```bash
export AWS_ACCESS_KEY_ID="..."      # For Bedrock
export AWS_SECRET_ACCESS_KEY="..."
export COHERE_API_KEY="..."
```

## Running Tests

### Using npm scripts

```bash
# Run all tests
npm test

# Run tests with verbose output
npm test -- --reporter=verbose

# Run tests in watch mode
npm run test:watch

# Run with coverage
npm run test:coverage

# Run with UI
npm run test:ui
```

### Filtering tests

```bash
# Run specific test file
npm test -- tests/test-openai.test.ts

# Run tests matching pattern
npm test -- -t "Simple Chat"
npm test -- -t "Tool"
npm test -- -t "Streaming"

# Run tests for specific provider
npm test -- tests/test-anthropic.test.ts -t "Streaming"
```

### Using Makefile

From the repository root:

```bash
# Run TypeScript integration tests
make test-integrations LANG=ts

# Run specific SDK tests
make test-integrations LANG=ts INTEGRATION=openai

# Run with pattern
make test-integrations LANG=ts PATTERN="tool"

# Verbose output
make test-integrations LANG=ts VERBOSE=1
```

## Cross-Provider Testing

The OpenAI test file supports cross-provider testing through Bifrost's model name routing. By formatting the model name as `provider/model`, Bifrost routes the request to the appropriate provider:

```typescript
import { formatProviderModel } from '../src/utils'

const client = new OpenAI({
  baseURL: 'http://localhost:8080/openai',
  apiKey: 'your-api-key',
})

// Route to Anthropic using the model name format
const response = await client.chat.completions.create({
  model: formatProviderModel('anthropic', 'claude-sonnet-4-20250514'),
  // Results in: "anthropic/claude-sonnet-4-20250514"
  messages: [{ role: 'user', content: 'Hello' }],
})

// Route to Bedrock
const bedrockResponse = await client.chat.completions.create({
  model: formatProviderModel('bedrock', 'global.anthropic.claude-sonnet-4-20250514-v1:0'),
  // Results in: "bedrock/global.anthropic.claude-sonnet-4-20250514-v1:0"
  messages: [{ role: 'user', content: 'Hello' }],
})
```

This allows testing any provider using the OpenAI SDK format while Bifrost handles the routing based on the model name prefix.

## Writing New Tests

### Basic Test Structure

```typescript
import { describe, it, expect } from 'vitest'
import OpenAI from 'openai'
import { getIntegrationUrl, getProviderModel } from '../src/utils'

describe('My Feature Tests', () => {
  it('should do something', async () => {
    const client = new OpenAI({
      baseURL: getIntegrationUrl('openai'),
      apiKey: process.env.OPENAI_API_KEY,
    })

    const response = await client.chat.completions.create({
      model: getProviderModel('openai', 'chat'),
      messages: [{ role: 'user', content: 'Hello' }],
    })

    expect(response.choices[0].message.content).toBeDefined()
  })
})
```

### Using Test Utilities

```typescript
import {
  SIMPLE_CHAT_MESSAGES,
  WEATHER_TOOL,
  assertValidChatResponse,
  assertHasToolCalls,
  convertToOpenAITools,
} from '../src/utils'

// Use predefined test messages
const response = await client.chat.completions.create({
  model,
  messages: SIMPLE_CHAT_MESSAGES,
})

// Use assertion helpers
assertValidChatResponse(response)
assertHasToolCalls(response, 1)

// Use tool conversion utilities
const tools = convertToOpenAITools([WEATHER_TOOL])
```

### Cross-Provider Parametrization

```typescript
import { getCrossProviderParamsWithVkForScenario } from '../src/utils'

describe('Cross-Provider Tests', () => {
  const testCases = getCrossProviderParamsWithVkForScenario('simple_chat')

  it.each(testCases)(
    'should work - $provider (VK: $vkEnabled)',
    async ({ provider, model, vkEnabled }) => {
      // Test implementation
    }
  )
})
```

## Troubleshooting

### Common Issues

**1. Connection Refused**
```text
Error: connect ECONNREFUSED 127.0.0.1:8080
```
Solution: Ensure Bifrost is running on the expected port.

**2. API Key Not Set**
```text
Error: OPENAI_API_KEY environment variable not set
```
Solution: Set the required environment variables.

**3. Timeout Errors**
```text
Error: Timeout of 300000ms exceeded
```
Solution: Check network connectivity and Bifrost logs.

### Debug Mode

```bash
# Run with debug output
DEBUG=* npm test -- tests/test-openai.test.ts

# Check Bifrost logs
tail -f /tmp/bifrost-test.log
```

## Integration with Python Tests

The TypeScript and Python test suites share:
- **Configuration** (`config.yml`) - Same provider/model settings
- **Test Scenarios** - Same test categories and assertions
- **Makefile Integration** - Unified `test-integrations` command

To run both:
```bash
# Python tests
make test-integrations-py

# TypeScript tests
make test-integrations-ts

# Both
make test-integrations-py && make test-integrations-ts
```

## Contributing

1. Follow the existing test structure
2. Use the shared utilities from `src/utils/`
3. Add tests for all applicable scenarios
4. Ensure tests pass locally before submitting
5. Update this README if adding new SDKs or features
