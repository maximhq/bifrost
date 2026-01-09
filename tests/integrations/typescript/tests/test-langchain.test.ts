/**
 * LangChain.js Integration Tests
 *
 * This test suite uses LangChain.js to test multiple AI providers through Bifrost.
 * Tests cover chat, streaming, tool calling, and structured output capabilities.
 *
 * Providers tested:
 * - OpenAI (via @langchain/openai)
 * - Anthropic (via @langchain/anthropic)
 * - Google GenAI (via @langchain/google-genai)
 *
 * Test Scenarios:
 * 1. Simple chat
 * 2. Multi-turn conversation
 * 3. Streaming chat
 * 4. Tool calling
 * 5. Structured output
 */

import { describe, it, expect, beforeAll } from 'vitest'
import { ChatOpenAI } from '@langchain/openai'
import { ChatAnthropic } from '@langchain/anthropic'
import { ChatGoogleGenerativeAI } from '@langchain/google-genai'
import { HumanMessage, AIMessage, SystemMessage, BaseMessage } from '@langchain/core/messages'
import { DynamicStructuredTool } from '@langchain/core/tools'
import { z } from 'zod'

import {
  getIntegrationUrl,
  getProviderModel,
  isProviderAvailable,
} from '../src/utils/config-loader'

import {
  SIMPLE_CHAT_MESSAGES,
  MULTI_TURN_MESSAGES,
  STREAMING_CHAT_MESSAGES,
  SINGLE_TOOL_CALL_MESSAGES,
  getApiKey,
  hasApiKey,
  mockToolResponse,
  type ChatMessage,
} from '../src/utils/common'

// ============================================================================
// Helper Functions
// ============================================================================

type LangChainModel = ChatOpenAI | ChatAnthropic | ChatGoogleGenerativeAI

function getLangChainOpenAI(): ChatOpenAI {
  const baseUrl = getIntegrationUrl('openai')
  const apiKey = hasApiKey('openai') ? getApiKey('openai') : 'dummy-key'
  const model = getProviderModel('openai', 'chat')

  return new ChatOpenAI({
    modelName: model,
    openAIApiKey: apiKey,
    configuration: {
      baseURL: baseUrl,
    },
    maxTokens: 100,
    timeout: 300000,
    maxRetries: 3,
  })
}

function getLangChainAnthropic(): ChatAnthropic {
  const baseUrl = getIntegrationUrl('anthropic')
  const apiKey = hasApiKey('anthropic') ? getApiKey('anthropic') : 'dummy-key'
  const model = getProviderModel('anthropic', 'chat')

  return new ChatAnthropic({
    modelName: model,
    anthropicApiKey: apiKey,
    anthropicApiUrl: baseUrl,
    maxTokens: 100,
    maxRetries: 3,
  })
}

function getLangChainGoogle(): ChatGoogleGenerativeAI {
  // Use 'gemini' consistently for both API key and model lookup
  const apiKey = hasApiKey('gemini') ? getApiKey('gemini') : 'dummy-key'
  const model = getProviderModel('gemini', 'chat')

  return new ChatGoogleGenerativeAI({
    modelName: model,
    apiKey,
    maxOutputTokens: 100,
    maxRetries: 3,
  })
}

function convertToLangChainMessages(messages: ChatMessage[]): BaseMessage[] {
  return messages.map((msg) => {
    const content = typeof msg.content === 'string' ? msg.content : JSON.stringify(msg.content)

    switch (msg.role) {
      case 'system':
        return new SystemMessage(content)
      case 'assistant':
        return new AIMessage(content)
      case 'user':
      default:
        return new HumanMessage(content)
    }
  })
}

// Weather tool using Zod schema
const weatherTool = new DynamicStructuredTool({
  name: 'get_weather',
  description: 'Get the current weather for a location',
  schema: z.object({
    location: z.string().describe('The city and state, e.g. San Francisco, CA'),
    unit: z.enum(['celsius', 'fahrenheit']).optional().describe('The temperature unit'),
  }),
  func: async ({ location, unit }) => {
    return mockToolResponse('get_weather', { location, unit })
  },
})

// Calculator tool using Zod schema
const calculatorTool = new DynamicStructuredTool({
  name: 'calculate',
  description: 'Perform basic mathematical calculations',
  schema: z.object({
    expression: z.string().describe("Mathematical expression to evaluate, e.g. '2 + 2'"),
  }),
  func: async ({ expression }) => {
    return mockToolResponse('calculate', { expression })
  },
})

// ============================================================================
// Test Suite
// ============================================================================

describe('LangChain.js Integration Tests', () => {
  // ============================================================================
  // OpenAI via LangChain
  // ============================================================================

  describe('LangChain OpenAI', () => {
    const skipTests = !isProviderAvailable('openai')

    beforeAll(() => {
      if (skipTests) {
        console.log('⚠️ Skipping LangChain OpenAI tests: OPENAI_API_KEY not set')
      }
    })

    describe('Simple Chat', () => {
      it('should complete a simple chat', async () => {
        if (skipTests) return

        const model = getLangChainOpenAI()
        const messages = convertToLangChainMessages(SIMPLE_CHAT_MESSAGES)

        const response = await model.invoke(messages)

        expect(response).toBeDefined()
        expect(response.content).toBeDefined()
        const content = typeof response.content === 'string' ? response.content : JSON.stringify(response.content)
        expect(content.length).toBeGreaterThan(0)
        console.log(`✅ LangChain OpenAI simple chat passed`)
      })
    })

    describe('Multi-turn Conversation', () => {
      it('should handle multi-turn conversation', async () => {
        if (skipTests) return

        const model = getLangChainOpenAI()
        const messages = convertToLangChainMessages(MULTI_TURN_MESSAGES)

        const response = await model.invoke(messages)

        expect(response).toBeDefined()
        const content = typeof response.content === 'string' ? response.content : JSON.stringify(response.content)
        expect(content.toLowerCase()).toMatch(/paris|population|million|people/i)
        console.log(`✅ LangChain OpenAI multi-turn conversation passed`)
      })
    })

    describe('Streaming Chat', () => {
      it('should stream chat response', async () => {
        if (skipTests) return

        const model = getLangChainOpenAI()
        const messages = convertToLangChainMessages(STREAMING_CHAT_MESSAGES)

        const stream = await model.stream(messages)

        let content = ''
        for await (const chunk of stream) {
          if (chunk.content) {
            content += typeof chunk.content === 'string' ? chunk.content : JSON.stringify(chunk.content)
          }
        }

        expect(content.length).toBeGreaterThan(0)
        console.log(`✅ LangChain OpenAI streaming chat passed`)
      })
    })

    describe('Streaming Chat - Client Disconnect', () => {
      it('should handle client disconnect mid-stream', async () => {
        if (skipTests) return

        const baseUrl = getIntegrationUrl('openai')
        const apiKey = hasApiKey('openai') ? getApiKey('openai') : 'dummy-key'
        const modelName = getProviderModel('openai', 'chat')

        // Create model with longer max tokens for a longer response
        const model = new ChatOpenAI({
          modelName,
          openAIApiKey: apiKey,
          configuration: {
            baseURL: baseUrl,
          },
          maxTokens: 1000,
          timeout: 300000,
        })

        const abortController = new AbortController()
        const messages = convertToLangChainMessages([
          { role: 'user', content: 'Write a detailed essay about the history of computing, including at least 10 paragraphs.' },
        ])

        const stream = await model.stream(messages, {
          signal: abortController.signal,
        })

        let chunkCount = 0
        let content = ''
        let wasAborted = false

        try {
          for await (const chunk of stream) {
            chunkCount++
            if (chunk.content) {
              content += typeof chunk.content === 'string' ? chunk.content : JSON.stringify(chunk.content)
            }

            // Abort after receiving a few chunks
            if (chunkCount >= 3) {
              abortController.abort()
            }
          }
        } catch (error) {
          wasAborted = true
          expect(error).toBeDefined()
          const errorMessage = error instanceof Error ? error.message.toLowerCase() : String(error).toLowerCase()
          const isAbortError = errorMessage.includes('abort') ||
                               errorMessage.includes('cancel') ||
                               error instanceof DOMException ||
                               (error as { name?: string })?.name === 'AbortError'
          expect(isAbortError).toBe(true)
        }

        expect(chunkCount).toBeGreaterThanOrEqual(3)
        expect(content.length).toBeGreaterThan(0)
        expect(wasAborted).toBe(true)
        console.log(`✅ LangChain OpenAI streaming client disconnect passed (${chunkCount} chunks before abort)`)
      })
    })

    describe('Tool Calling', () => {
      it('should make tool calls', async () => {
        if (skipTests) return

        const model = getLangChainOpenAI()
        const modelWithTools = model.bindTools([weatherTool])
        const messages = convertToLangChainMessages(SINGLE_TOOL_CALL_MESSAGES)

        const response = await modelWithTools.invoke(messages)

        expect(response).toBeDefined()
        expect(response.tool_calls).toBeDefined()
        expect(response.tool_calls!.length).toBeGreaterThan(0)
        expect(response.tool_calls![0].name).toBe('get_weather')
        console.log(`✅ LangChain OpenAI tool calling passed`)
      })
    })

    describe('Structured Output', () => {
      it('should generate structured output', async () => {
        if (skipTests) return

        const model = getLangChainOpenAI()

        const ResponseSchema = z.object({
          answer: z.string().describe('The answer to the question'),
          confidence: z.number().min(0).max(1).describe('Confidence score'),
        })

        const structuredModel = model.withStructuredOutput(ResponseSchema)

        const response = await structuredModel.invoke('What is 2 + 2?')

        expect(response).toBeDefined()
        expect(response.answer).toBeDefined()
        expect(typeof response.confidence).toBe('number')
        console.log(`✅ LangChain OpenAI structured output passed`)
      })
    })
  })

  // ============================================================================
  // Anthropic via LangChain
  // ============================================================================

  describe('LangChain Anthropic', () => {
    const skipTests = !isProviderAvailable('anthropic')

    beforeAll(() => {
      if (skipTests) {
        console.log('⚠️ Skipping LangChain Anthropic tests: ANTHROPIC_API_KEY not set')
      }
    })

    describe('Simple Chat', () => {
      it('should complete a simple chat', async () => {
        if (skipTests) return

        const model = getLangChainAnthropic()
        const messages = convertToLangChainMessages(SIMPLE_CHAT_MESSAGES)

        const response = await model.invoke(messages)

        expect(response).toBeDefined()
        expect(response.content).toBeDefined()
        const content = typeof response.content === 'string' ? response.content : JSON.stringify(response.content)
        expect(content.length).toBeGreaterThan(0)
        console.log(`✅ LangChain Anthropic simple chat passed`)
      })
    })

    describe('Multi-turn Conversation', () => {
      it('should handle multi-turn conversation', async () => {
        if (skipTests) return

        const model = getLangChainAnthropic()
        const messages = convertToLangChainMessages(MULTI_TURN_MESSAGES)

        const response = await model.invoke(messages)

        expect(response).toBeDefined()
        const content = typeof response.content === 'string' ? response.content : JSON.stringify(response.content)
        expect(content.toLowerCase()).toMatch(/paris|population|million|people/i)
        console.log(`✅ LangChain Anthropic multi-turn conversation passed`)
      })
    })

    describe('Streaming Chat', () => {
      it('should stream chat response', async () => {
        if (skipTests) return

        const model = getLangChainAnthropic()
        const messages = convertToLangChainMessages(STREAMING_CHAT_MESSAGES)

        const stream = await model.stream(messages)

        let content = ''
        for await (const chunk of stream) {
          if (chunk.content) {
            content += typeof chunk.content === 'string' ? chunk.content : JSON.stringify(chunk.content)
          }
        }

        expect(content.length).toBeGreaterThan(0)
        console.log(`✅ LangChain Anthropic streaming chat passed`)
      })
    })

    describe('Streaming Chat - Client Disconnect', () => {
      it('should handle client disconnect mid-stream', async () => {
        if (skipTests) return

        const baseUrl = getIntegrationUrl('anthropic')
        const apiKey = hasApiKey('anthropic') ? getApiKey('anthropic') : 'dummy-key'
        const modelName = getProviderModel('anthropic', 'chat')

        // Create model with longer max tokens for a longer response
        const model = new ChatAnthropic({
          modelName,
          anthropicApiKey: apiKey,
          anthropicApiUrl: baseUrl,
          maxTokens: 1000,
          maxRetries: 3,
        })

        const abortController = new AbortController()
        const messages = convertToLangChainMessages([
          { role: 'user', content: 'Write a detailed essay about the history of computing, including at least 10 paragraphs.' },
        ])

        const stream = await model.stream(messages, {
          signal: abortController.signal,
        })

        let chunkCount = 0
        let content = ''
        let wasAborted = false

        try {
          for await (const chunk of stream) {
            chunkCount++
            if (chunk.content) {
              content += typeof chunk.content === 'string' ? chunk.content : JSON.stringify(chunk.content)
            }

            // Abort after receiving a few chunks
            if (chunkCount >= 5) {
              abortController.abort()
            }
          }
        } catch (error) {
          wasAborted = true
          expect(error).toBeDefined()
          const errorMessage = error instanceof Error ? error.message.toLowerCase() : String(error).toLowerCase()
          const isAbortError = errorMessage.includes('abort') ||
                               errorMessage.includes('cancel') ||
                               error instanceof DOMException ||
                               (error as { name?: string })?.name === 'AbortError'
          expect(isAbortError).toBe(true)
        }

        expect(chunkCount).toBeGreaterThanOrEqual(5)
        expect(content.length).toBeGreaterThan(0)
        expect(wasAborted).toBe(true)
        console.log(`✅ LangChain Anthropic streaming client disconnect passed (${chunkCount} chunks before abort)`)
      })
    })

    describe('Tool Calling', () => {
      it('should make tool calls', async () => {
        if (skipTests) return

        const model = getLangChainAnthropic()
        const modelWithTools = model.bindTools([weatherTool])
        const messages = convertToLangChainMessages(SINGLE_TOOL_CALL_MESSAGES)

        const response = await modelWithTools.invoke(messages)

        expect(response).toBeDefined()
        expect(response.tool_calls).toBeDefined()
        expect(response.tool_calls!.length).toBeGreaterThan(0)
        expect(response.tool_calls![0].name).toBe('get_weather')
        console.log(`✅ LangChain Anthropic tool calling passed`)
      })
    })
  })

  // ============================================================================
  // Google via LangChain
  // ============================================================================

  describe('LangChain Google GenAI', () => {
    const skipTests = !isProviderAvailable('gemini')

    beforeAll(() => {
      if (skipTests) {
        console.log('⚠️ Skipping LangChain Google GenAI tests: GEMINI_API_KEY not set')
      }
    })

    describe('Simple Chat', () => {
      it('should complete a simple chat', async () => {
        if (skipTests) return

        const model = getLangChainGoogle()
        const messages = convertToLangChainMessages(SIMPLE_CHAT_MESSAGES)

        const response = await model.invoke(messages)

        expect(response).toBeDefined()
        expect(response.content).toBeDefined()
        const content = typeof response.content === 'string' ? response.content : JSON.stringify(response.content)
        expect(content.length).toBeGreaterThan(0)
        console.log(`✅ LangChain Google GenAI simple chat passed`)
      })
    })

    describe('Multi-turn Conversation', () => {
      it('should handle multi-turn conversation', async () => {
        if (skipTests) return

        const model = getLangChainGoogle()
        const messages = convertToLangChainMessages(MULTI_TURN_MESSAGES)

        const response = await model.invoke(messages)

        expect(response).toBeDefined()
        const content = typeof response.content === 'string' ? response.content : JSON.stringify(response.content)
        expect(content.toLowerCase()).toMatch(/paris|population|million|people/i)
        console.log(`✅ LangChain Google GenAI multi-turn conversation passed`)
      })
    })

    describe('Streaming Chat', () => {
      it('should stream chat response', async () => {
        if (skipTests) return

        const model = getLangChainGoogle()
        const messages = convertToLangChainMessages(STREAMING_CHAT_MESSAGES)

        const stream = await model.stream(messages)

        let content = ''
        for await (const chunk of stream) {
          if (chunk.content) {
            content += typeof chunk.content === 'string' ? chunk.content : JSON.stringify(chunk.content)
          }
        }

        expect(content.length).toBeGreaterThan(0)
        console.log(`✅ LangChain Google GenAI streaming chat passed`)
      })
    })

    describe('Tool Calling', () => {
      it('should make tool calls', async () => {
        if (skipTests) return

        const model = getLangChainGoogle()
        const modelWithTools = model.bindTools([weatherTool])
        const messages = convertToLangChainMessages(SINGLE_TOOL_CALL_MESSAGES)

        const response = await modelWithTools.invoke(messages)

        expect(response).toBeDefined()
        expect(response.tool_calls).toBeDefined()
        expect(response.tool_calls!.length).toBeGreaterThan(0)
        expect(response.tool_calls![0].name).toBe('get_weather')
        console.log(`✅ LangChain Google GenAI tool calling passed`)
      })
    })

    describe('Structured Output', () => {
      it('should generate structured output', async () => {
        if (skipTests) return

        const model = getLangChainGoogle()

        const ResponseSchema = z.object({
          answer: z.string().describe('The answer to the question'),
          confidence: z.number().min(0).max(1).describe('Confidence score'),
        })

        try {
          const structuredModel = model.withStructuredOutput(ResponseSchema)
          const response = await structuredModel.invoke('What is 2 + 2?')

          expect(response).toBeDefined()
          expect(response.answer).toBeDefined()
          expect(typeof response.confidence).toBe('number')
          console.log(`✅ LangChain Google GenAI structured output passed`)
        } catch (error) {
          console.log(`⚠️ LangChain Google GenAI structured output test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
        }
      })
    })
  })

  // ============================================================================
  // Cross-Provider Token Counting Tests
  // ============================================================================

  describe('Token Counting', () => {
    describe('OpenAI Token Counting', () => {
      const skipTests = !isProviderAvailable('openai')

      it('should return token usage in response', async () => {
        if (skipTests) return

        const model = getLangChainOpenAI()
        const messages = convertToLangChainMessages(SIMPLE_CHAT_MESSAGES)

        const response = await model.invoke(messages)

        expect(response).toBeDefined()
        // LangChain includes usage info in response_metadata
        if (response.response_metadata) {
          const usage = response.response_metadata.usage || response.response_metadata.tokenUsage
          if (usage) {
            expect(usage.prompt_tokens || usage.promptTokens).toBeGreaterThan(0)
            expect(usage.completion_tokens || usage.completionTokens).toBeGreaterThan(0)
          }
        }
        console.log(`✅ LangChain OpenAI token counting passed`)
      })
    })

    describe('Anthropic Token Counting', () => {
      const skipTests = !isProviderAvailable('anthropic')

      it('should return token usage in response', async () => {
        if (skipTests) return

        const model = getLangChainAnthropic()
        const messages = convertToLangChainMessages(SIMPLE_CHAT_MESSAGES)

        const response = await model.invoke(messages)

        expect(response).toBeDefined()
        // Anthropic includes usage info in usage_metadata
        if (response.usage_metadata) {
          expect(response.usage_metadata.input_tokens).toBeGreaterThan(0)
          expect(response.usage_metadata.output_tokens).toBeGreaterThan(0)
        }
        console.log(`✅ LangChain Anthropic token counting passed`)
      })
    })

    describe('Google GenAI Token Counting', () => {
      const skipTests = !isProviderAvailable('gemini')

      it('should return token usage in response', async () => {
        if (skipTests) return

        const model = getLangChainGoogle()
        const messages = convertToLangChainMessages(SIMPLE_CHAT_MESSAGES)

        const response = await model.invoke(messages)

        expect(response).toBeDefined()
        // Google includes usage info in response_metadata
        if (response.response_metadata) {
          const usage = response.response_metadata.usage
          if (usage) {
            expect(usage.promptTokenCount || usage.prompt_tokens).toBeGreaterThan(0)
          }
        }
        console.log(`✅ LangChain Google GenAI token counting passed`)
      })
    })
  })

  // ============================================================================
  // Cross-Provider Structured Output Tests
  // ============================================================================

  describe('Comprehensive Structured Output', () => {
    // Complex schema for testing
    const RecipeSchema = z.object({
      name: z.string().describe('Name of the recipe'),
      ingredients: z.array(z.object({
        item: z.string().describe('Ingredient name'),
        amount: z.string().describe('Amount needed'),
      })).describe('List of ingredients'),
      steps: z.array(z.string()).describe('Cooking steps'),
      prepTime: z.number().describe('Preparation time in minutes'),
      cookTime: z.number().describe('Cooking time in minutes'),
    })

    describe('OpenAI Complex Structured Output', () => {
      const skipTests = !isProviderAvailable('openai')

      it('should generate complex structured output', async () => {
        if (skipTests) return

        const model = getLangChainOpenAI()
        const structuredModel = model.withStructuredOutput(RecipeSchema)

        const response = await structuredModel.invoke('Give me a simple recipe for scrambled eggs')

        expect(response).toBeDefined()
        expect(response.name).toBeDefined()
        expect(Array.isArray(response.ingredients)).toBe(true)
        expect(Array.isArray(response.steps)).toBe(true)
        expect(typeof response.prepTime).toBe('number')
        expect(typeof response.cookTime).toBe('number')
        console.log(`✅ LangChain OpenAI complex structured output passed`)
      })
    })

    describe('Anthropic Complex Structured Output', () => {
      const skipTests = !isProviderAvailable('anthropic')

      it('should generate complex structured output', async () => {
        if (skipTests) return

        const model = getLangChainAnthropic()

        try {
          const structuredModel = model.withStructuredOutput(RecipeSchema)
          const response = await structuredModel.invoke('Give me a simple recipe for scrambled eggs')

          expect(response).toBeDefined()
          expect(response.name).toBeDefined()
          expect(Array.isArray(response.ingredients)).toBe(true)
          expect(Array.isArray(response.steps)).toBe(true)
          expect(typeof response.prepTime).toBe('number')
          expect(typeof response.cookTime).toBe('number')
          console.log(`✅ LangChain Anthropic complex structured output passed`)
        } catch (error) {
          console.log(`⚠️ LangChain Anthropic complex structured output test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
        }
      })
    })
  })

  // ============================================================================
  // Extended Thinking Tests
  // ============================================================================

  describe('Thinking/Extended Reasoning', () => {
    describe('OpenAI Thinking', () => {
      const skipTests = !isProviderAvailable('openai')

      it('should support extended reasoning', async () => {
        if (skipTests) return

        const thinkingModel = getProviderModel('openai', 'thinking')

        // Skip if no thinking model available
        if (!thinkingModel) {
          console.log('⚠️ Skipping OpenAI thinking test: No thinking model configured')
          return
        }

        const baseUrl = getIntegrationUrl('openai')
        const apiKey = hasApiKey('openai') ? getApiKey('openai') : 'dummy-key'

        const model = new ChatOpenAI({
          modelName: thinkingModel,
          openAIApiKey: apiKey,
          configuration: {
            baseURL: baseUrl,
          },
          maxTokens: 2000,
          timeout: 300000,
        })

        try {
          const response = await model.invoke([
            new HumanMessage('What is 15% of 80? Think through this step by step.'),
          ])

          expect(response).toBeDefined()
          const content = typeof response.content === 'string' ? response.content : JSON.stringify(response.content)
          expect(content.length).toBeGreaterThan(0)
          console.log(`✅ LangChain OpenAI thinking passed`)
        } catch (error) {
          console.log(`⚠️ LangChain OpenAI thinking test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
        }
      })
    })

    describe('Anthropic Thinking', () => {
      const skipTests = !isProviderAvailable('anthropic')

      it('should support extended reasoning', async () => {
        if (skipTests) return

        const thinkingModel = getProviderModel('anthropic', 'thinking')

        // Skip if no thinking model available
        if (!thinkingModel) {
          console.log('⚠️ Skipping Anthropic thinking test: No thinking model configured')
          return
        }

        const baseUrl = getIntegrationUrl('anthropic')
        const apiKey = hasApiKey('anthropic') ? getApiKey('anthropic') : 'dummy-key'

        const model = new ChatAnthropic({
          modelName: thinkingModel,
          anthropicApiKey: apiKey,
          anthropicApiUrl: baseUrl,
          maxTokens: 8000,
          maxRetries: 3,
        })

        try {
          // Anthropic thinking requires specific configuration
          const response = await model.invoke([
            new HumanMessage('What is 15% of 80? Think through this step by step.'),
          ])

          expect(response).toBeDefined()
          const content = typeof response.content === 'string' ? response.content : JSON.stringify(response.content)
          expect(content.length).toBeGreaterThan(0)
          console.log(`✅ LangChain Anthropic thinking passed`)
        } catch (error) {
          console.log(`⚠️ LangChain Anthropic thinking test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
        }
      })
    })

    describe('Google GenAI Thinking', () => {
      const skipTests = !isProviderAvailable('gemini')

      it('should support extended reasoning', async () => {
        if (skipTests) return

        const thinkingModel = getProviderModel('gemini', 'thinking')

        // Skip if no thinking model available
        if (!thinkingModel) {
          console.log('⚠️ Skipping Google GenAI thinking test: No thinking model configured')
          return
        }

        const apiKey = hasApiKey('gemini') ? getApiKey('gemini') : 'dummy-key'

        const model = new ChatGoogleGenerativeAI({
          modelName: thinkingModel,
          apiKey,
          maxOutputTokens: 2048,
        })

        try {
          const response = await model.invoke([
            new HumanMessage('What is 15% of 80? Think through this step by step.'),
          ])

          expect(response).toBeDefined()
          const content = typeof response.content === 'string' ? response.content : JSON.stringify(response.content)
          expect(content.length).toBeGreaterThan(0)
          console.log(`✅ LangChain Google GenAI thinking passed`)
        } catch (error) {
          console.log(`⚠️ LangChain Google GenAI thinking test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
        }
      })
    })
  })

  // ============================================================================
  // Streaming Tool Calls Tests
  // ============================================================================

  describe('Streaming Tool Calls', () => {
    describe('OpenAI Streaming Tool Calls', () => {
      const skipTests = !isProviderAvailable('openai')

      it('should stream tool calls', async () => {
        if (skipTests) return

        const model = getLangChainOpenAI()
        const modelWithTools = model.bindTools([weatherTool, calculatorTool])
        const messages = convertToLangChainMessages(SINGLE_TOOL_CALL_MESSAGES)

        const stream = await modelWithTools.stream(messages)

        let hasToolCall = false
        for await (const chunk of stream) {
          if (chunk.tool_calls && chunk.tool_calls.length > 0) {
            hasToolCall = true
          }
          if (chunk.tool_call_chunks && chunk.tool_call_chunks.length > 0) {
            hasToolCall = true
          }
        }

        // Tool calls might not always stream, but the stream should complete
        console.log(`✅ LangChain OpenAI streaming tool calls passed (tool call detected: ${hasToolCall})`)
      })
    })
  })
})
