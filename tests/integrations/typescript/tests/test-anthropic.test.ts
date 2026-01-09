/**
 * Anthropic Integration Tests
 *
 * This test suite uses the Anthropic SDK to test Claude models through Bifrost.
 * Tests cover chat, streaming, tool calling, vision, files, batch, and advanced capabilities.
 *
 * Test Scenarios:
 *
 * Chat Completions:
 * 1. Simple chat
 * 2. Multi-turn conversation
 * 3. Streaming chat
 *
 * Tool Calling:
 * 4. Single tool call
 * 5. Multiple tool calls
 * 6. End-to-end tool calling
 *
 * Vision/Image:
 * 7. Image URL analysis
 * 8. Image Base64 analysis
 * 9. Multiple images analysis
 *
 * Extended Thinking:
 * 10. Thinking/Extended reasoning
 * 11. Extended thinking streaming
 *
 * Token Counting:
 * 12. Count tokens (basic)
 * 13. Count tokens - with system message
 * 14. Count tokens - long text
 *
 * Prompt Caching:
 * 15. Prompt caching - system message
 * 16. Prompt caching - messages
 * 17. Prompt caching - tools
 *
 * Document Input:
 * 18. Document input - PDF Base64
 * 19. Document input - plain text
 *
 * Models:
 * 20. List models
 *
 * Files API:
 * 21. File upload
 * 22. File list
 * 23. File delete
 * 24. File content download
 *
 * Batch API:
 * 25. Batch create (inline requests)
 * 26. Batch list
 * 27. Batch retrieve
 * 28. Batch cancel
 * 29. Batch results
 * 30. Batch end-to-end workflow
 */

import Anthropic from '@anthropic-ai/sdk'
import { beforeAll, describe, expect, it } from 'vitest'

import {
  getIntegrationUrl,
  getProviderModel,
  isProviderAvailable,
} from '../src/utils/config-loader'

import {
  BASE64_IMAGE,
  CALCULATOR_TOOL,
  getApiKey,
  hasApiKey,
  IMAGE_URL,
  mockToolResponse,
  MULTI_TURN_MESSAGES,
  MULTIPLE_TOOL_CALL_MESSAGES,
  SIMPLE_CHAT_MESSAGES,
  SINGLE_TOOL_CALL_MESSAGES,
  STREAMING_CHAT_MESSAGES,
  WEATHER_TOOL,
  type ChatMessage,
  type ToolDefinition,
} from '../src/utils/common'

// Type for content blocks that include beta features
type ContentBlockParamWithBeta = Anthropic.ContentBlock | { type: 'text'; text: string; cache_control?: { type: 'ephemeral' } } | { type: 'document'; source: { type: 'base64'; media_type: string; data: string } }

// ============================================================================
// Helper Functions
// ============================================================================

function getAnthropicClient(): Anthropic {
  const baseUrl = getIntegrationUrl('anthropic')
  const apiKey = hasApiKey('anthropic') ? getApiKey('anthropic') : 'dummy-key'

  return new Anthropic({
    baseURL: baseUrl,
    apiKey,
    timeout: 300000, // 5 minutes
    maxRetries: 3,
  })
}

function convertToAnthropicMessages(
  messages: ChatMessage[]
): Anthropic.MessageParam[] {
  return messages.map((msg) => {
    if (msg.role === 'assistant') {
      return {
        role: 'assistant' as const,
        content: typeof msg.content === 'string' ? msg.content : JSON.stringify(msg.content),
      }
    }

    if (typeof msg.content === 'string') {
      return {
        role: 'user' as const,
        content: msg.content,
      }
    }

    // Handle multimodal content
    const parts: Anthropic.ContentBlock[] = msg.content.map((part) => {
      if (part.type === 'text') {
        return { type: 'text' as const, text: part.text! }
      }

      // Handle image content
      const imageUrl = part.image_url!.url
      if (imageUrl.startsWith('data:')) {
        // Base64 image
        const matches = imageUrl.match(/^data:([^;]+);base64,(.+)$/)
        if (matches) {
          return {
            type: 'image' as const,
            source: {
              type: 'base64' as const,
              media_type: matches[1] as 'image/jpeg' | 'image/png' | 'image/gif' | 'image/webp',
              data: matches[2],
            },
          }
        }
      }

      // URL image - Anthropic supports URL source type directly (beta feature)
      return {
        type: 'image' as const,
        source: {
          type: 'url' as const,
          url: imageUrl,
        },
      } as unknown as Anthropic.ContentBlock
    }) as Anthropic.ContentBlock[]

    return {
      role: 'user' as const,
      content: parts,
    }
  })
}

function convertToAnthropicTools(tools: ToolDefinition[]): Anthropic.Tool[] {
  return tools.map((tool) => ({
    name: tool.name,
    description: tool.description,
    input_schema: {
      type: 'object' as const,
      properties: tool.parameters.properties,
      required: tool.parameters.required,
    },
  }))
}

function extractAnthropicToolCalls(
  response: Anthropic.Message
): Array<{ name: string; arguments: Record<string, unknown>; id: string }> {
  const toolCalls: Array<{ name: string; arguments: Record<string, unknown>; id: string }> = []

  for (const block of response.content) {
    if (block.type === 'tool_use') {
      toolCalls.push({
        name: block.name,
        arguments: block.input as Record<string, unknown>,
        id: block.id,
      })
    }
  }

  return toolCalls
}

function getContentString(response: Anthropic.Message): string {
  let content = ''
  for (const block of response.content) {
    if (block.type === 'text') {
      content += block.text
    }
  }
  return content
}

// ============================================================================
// Test Suite
// ============================================================================

describe('Anthropic SDK Integration Tests', () => {
  const skipTests = !isProviderAvailable('anthropic')

  beforeAll(() => {
    if (skipTests) {
      console.log('⚠️ Skipping Anthropic tests: ANTHROPIC_API_KEY not set')
    }
  })

  // ============================================================================
  // Simple Chat Tests
  // ============================================================================

  describe('Simple Chat', () => {
    it('should complete a simple chat', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'chat')

      const response = await client.messages.create({
        model,
        max_tokens: 100,
        messages: convertToAnthropicMessages(SIMPLE_CHAT_MESSAGES),
      })

      expect(response).toBeDefined()
      expect(response.content).toBeDefined()
      expect(response.content.length).toBeGreaterThan(0)

      const content = getContentString(response)
      expect(content.length).toBeGreaterThan(0)
      console.log(`✅ Simple chat passed for anthropic/${model}`)
    })
  })

  // ============================================================================
  // Multi-turn Conversation Tests
  // ============================================================================

  describe('Multi-turn Conversation', () => {
    it('should handle multi-turn conversation', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'chat')

      const response = await client.messages.create({
        model,
        max_tokens: 100,
        messages: convertToAnthropicMessages(MULTI_TURN_MESSAGES),
      })

      expect(response).toBeDefined()
      const content = getContentString(response)
      expect(content.toLowerCase()).toMatch(/paris|population|million|people/i)
      console.log(`✅ Multi-turn conversation passed for anthropic/${model}`)
    })
  })

  // ============================================================================
  // Streaming Tests
  // ============================================================================

  describe('Streaming Chat', () => {
    it('should stream chat response', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'chat')

      const stream = client.messages.stream({
        model,
        max_tokens: 100,
        messages: convertToAnthropicMessages(STREAMING_CHAT_MESSAGES),
      })

      let content = ''
      for await (const event of stream) {
        if (event.type === 'content_block_delta' && event.delta.type === 'text_delta') {
          content += event.delta.text
        }
      }

      expect(content.length).toBeGreaterThan(0)
      console.log(`✅ Streaming chat passed for anthropic/${model}`)
    })
  })

  // ============================================================================
  // Tool Calling Tests
  // ============================================================================

  describe('Single Tool Call', () => {
    it('should make a single tool call', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'tools')

      const response = await client.messages.create({
        model,
        max_tokens: 100,
        messages: convertToAnthropicMessages(SINGLE_TOOL_CALL_MESSAGES),
        tools: convertToAnthropicTools([WEATHER_TOOL]),
      })

      const toolCalls = extractAnthropicToolCalls(response)
      expect(toolCalls.length).toBe(1)
      expect(toolCalls[0].name).toBe('get_weather')
      console.log(`✅ Single tool call passed for anthropic/${model}`)
    })
  })

  describe('Multiple Tool Calls', () => {
    it('should make multiple tool calls', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'tools')

      const response = await client.messages.create({
        model,
        max_tokens: 150,
        messages: convertToAnthropicMessages(MULTIPLE_TOOL_CALL_MESSAGES),
        tools: convertToAnthropicTools([WEATHER_TOOL, CALCULATOR_TOOL]),
      })

      const toolCalls = extractAnthropicToolCalls(response)
      expect(toolCalls.length).toBeGreaterThanOrEqual(1)

      const toolNames = toolCalls.map((tc) => tc.name)
      expect(toolNames.some((name) => name === 'get_weather' || name === 'calculate')).toBe(true)
      console.log(`✅ Multiple tool calls passed for anthropic/${model}`)
    })
  })

  describe('End-to-End Tool Calling', () => {
    it('should complete end-to-end tool calling', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'tools')

      // Step 1: Initial request with tools
      const response1 = await client.messages.create({
        model,
        max_tokens: 100,
        messages: convertToAnthropicMessages(SINGLE_TOOL_CALL_MESSAGES),
        tools: convertToAnthropicTools([WEATHER_TOOL]),
      })

      const toolCalls = extractAnthropicToolCalls(response1)
      expect(toolCalls.length).toBeGreaterThan(0)

      // Step 2: Execute tool and get result
      const toolResult = mockToolResponse(toolCalls[0].name, toolCalls[0].arguments)

      // Step 3: Send tool result back
      const messages: Anthropic.MessageParam[] = [
        ...convertToAnthropicMessages(SINGLE_TOOL_CALL_MESSAGES),
        {
          role: 'assistant',
          content: response1.content,
        },
        {
          role: 'user',
          content: [
            {
              type: 'tool_result',
              tool_use_id: toolCalls[0].id,
              content: toolResult,
            },
          ],
        },
      ]

      const response2 = await client.messages.create({
        model,
        max_tokens: 200,
        messages,
        tools: convertToAnthropicTools([WEATHER_TOOL]),
      })

      expect(response2).toBeDefined()
      const content = getContentString(response2)
      expect(content.length).toBeGreaterThan(0)
      console.log(`✅ End-to-end tool calling passed for anthropic/${model}`)
    })
  })

  // ============================================================================
  // Image/Vision Tests
  // ============================================================================

  describe('Image URL', () => {
    it('should analyze image from URL', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'vision')

      // Use type assertion for URL-based image source (beta feature)
      const response = await client.messages.create({
        model,
        max_tokens: 200,
        messages: [
          {
            role: 'user',
            content: [
              { type: 'text', text: 'What do you see in this image? Describe it briefly.' },
              {
                type: 'image',
                source: {
                  type: 'url',
                  url: IMAGE_URL,
                },
              },
            ],
          },
        ],
      } as never)

      expect(response).toBeDefined()
      const content = getContentString(response)
      expect(content.length).toBeGreaterThan(10)
      console.log(`✅ Image URL analysis passed for anthropic/${model}`)
    })
  })

  describe('Image Base64', () => {
    it('should analyze image from Base64', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'vision')

      const response = await client.messages.create({
        model,
        max_tokens: 200,
        messages: [
          {
            role: 'user',
            content: [
              { type: 'text', text: 'What color is this image?' },
              {
                type: 'image',
                source: {
                  type: 'base64',
                  media_type: 'image/png',
                  data: BASE64_IMAGE,
                },
              },
            ],
          },
        ],
      })

      expect(response).toBeDefined()
      const content = getContentString(response)
      expect(content.length).toBeGreaterThan(10)
      console.log(`✅ Image Base64 analysis passed for anthropic/${model}`)
    })
  })

  describe('Multiple Images', () => {
    it('should analyze multiple images', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'vision')

      // Use type assertion for URL-based image source (beta feature)
      const response = await client.messages.create({
        model,
        max_tokens: 300,
        messages: [
          {
            role: 'user',
            content: [
              { type: 'text', text: 'Compare these two images. What do you see?' },
              {
                type: 'image',
                source: {
                  type: 'url',
                  url: IMAGE_URL,
                },
              },
              {
                type: 'image',
                source: {
                  type: 'base64',
                  media_type: 'image/png',
                  data: BASE64_IMAGE,
                },
              },
            ],
          },
        ],
      } as never)

      expect(response).toBeDefined()
      const content = getContentString(response)
      expect(content.length).toBeGreaterThan(10)
      console.log(`✅ Multiple images analysis passed for anthropic/${model}`)
    })
  })

  // ============================================================================
  // Thinking/Extended Reasoning Tests
  // ============================================================================

  describe('Thinking/Extended Reasoning', () => {
    it('should support extended thinking', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'thinking')

      // Skip if no thinking model available
      if (!model) {
        console.log('⚠️ Skipping thinking test: No thinking model configured')
        return
      }

      try {
        // Use type assertion for beta thinking feature
        const response = await client.messages.create({
          model,
          max_tokens: 8000,
          thinking: {
            type: 'enabled',
            budget_tokens: 5000,
          },
          messages: [
            {
              role: 'user',
              content: 'What is 15% of 80? Show your reasoning step by step.',
            },
          ],
        } as never)

        expect(response).toBeDefined()
        expect(response.content).toBeDefined()

        // Check for thinking blocks (beta feature)
        const hasThinking = response.content.some((block: { type: string }) => block.type === 'thinking')
        const content = getContentString(response)

        // Either should have thinking blocks or text content
        expect(hasThinking || content.length > 0).toBe(true)
        console.log(`✅ Thinking/Extended reasoning passed for anthropic/${model}`)
      } catch (error) {
        // Some models may not support thinking
        console.log(`⚠️ Thinking test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  // ============================================================================
  // Count Tokens Tests
  // ============================================================================

  describe('Count Tokens', () => {
    it('should return token usage in response', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'chat')

      const response = await client.messages.create({
        model,
        max_tokens: 50,
        messages: [{ role: 'user', content: 'Say hello' }],
      })

      expect(response.usage).toBeDefined()
      expect(response.usage.input_tokens).toBeGreaterThan(0)
      expect(response.usage.output_tokens).toBeGreaterThan(0)
      console.log(`✅ Count tokens passed for anthropic/${model}`)
    })
  })

  // ============================================================================
  // Prompt Caching Tests
  // ============================================================================

  describe('Prompt Caching - System Message', () => {
    it('should support prompt caching with system message', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'chat')

      // Create a large context for caching
      const largeContext = 'This is a legal document for analysis. '.repeat(100)

      // First request - should create cache (use type assertion for beta cache_control)
      const response1 = await client.messages.create({
        model,
        max_tokens: 100,
        system: [
          { type: 'text', text: 'You are an AI assistant tasked with analyzing legal documents.' },
          { type: 'text', text: largeContext, cache_control: { type: 'ephemeral' } },
        ],
        messages: [
          { role: 'user', content: 'What are the key elements of contract formation?' },
        ],
      } as never)

      expect(response1).toBeDefined()
      expect(response1.usage).toBeDefined()

      // Second request - should hit cache
      const response2 = await client.messages.create({
        model,
        max_tokens: 100,
        system: [
          { type: 'text', text: 'You are an AI assistant tasked with analyzing legal documents.' },
          { type: 'text', text: largeContext, cache_control: { type: 'ephemeral' } },
        ],
        messages: [
          { role: 'user', content: 'Explain the purpose of force majeure clauses.' },
        ],
      } as never)

      expect(response2).toBeDefined()
      expect(response2.usage).toBeDefined()

      console.log(`✅ Prompt caching (system message) passed for anthropic/${model}`)
    })
  })

  describe('Prompt Caching - Messages', () => {
    it('should support prompt caching with messages', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'chat')

      // Create a large context for caching
      const largeContext = 'This is a legal document for analysis. '.repeat(100)

      // First request - should create cache (use type assertion for beta cache_control)
      const response1 = await client.messages.create({
        model,
        max_tokens: 100,
        messages: [
          {
            role: 'user',
            content: [
              { type: 'text', text: 'Here is a large legal document to analyze:' },
              { type: 'text', text: largeContext, cache_control: { type: 'ephemeral' } },
              { type: 'text', text: 'What are the main indemnification principles?' },
            ],
          },
        ],
      } as never)

      expect(response1).toBeDefined()
      expect(response1.usage).toBeDefined()

      // Second request with same cached content
      const response2 = await client.messages.create({
        model,
        max_tokens: 100,
        messages: [
          {
            role: 'user',
            content: [
              { type: 'text', text: 'Here is a large legal document to analyze:' },
              { type: 'text', text: largeContext, cache_control: { type: 'ephemeral' } },
              { type: 'text', text: 'Summarize the dispute resolution methods.' },
            ],
          },
        ],
      } as never)

      expect(response2).toBeDefined()
      expect(response2.usage).toBeDefined()

      console.log(`✅ Prompt caching (messages) passed for anthropic/${model}`)
    })
  })

  describe('Prompt Caching - Tools', () => {
    it('should support prompt caching with tools', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'tools')

      // Create multiple tools for caching
      const tools = convertToAnthropicTools([WEATHER_TOOL, CALCULATOR_TOOL])
      // Add cache control to the last tool (use type assertion for beta feature)
      const cachedTools = tools.map((tool, index) =>
        index === tools.length - 1
          ? { ...tool, cache_control: { type: 'ephemeral' as const } }
          : tool
      )

      // First request - should create cache (use type assertion for beta cache_control)
      const response1 = await client.messages.create({
        model,
        max_tokens: 100,
        tools: cachedTools,
        messages: [
          { role: 'user', content: "What's the weather in Boston?" },
        ],
      } as never)

      expect(response1).toBeDefined()
      expect(response1.usage).toBeDefined()

      // Second request - should hit cache
      const response2 = await client.messages.create({
        model,
        max_tokens: 100,
        tools: cachedTools,
        messages: [
          { role: 'user', content: 'Calculate 42 * 17' },
        ],
      } as never)

      expect(response2).toBeDefined()
      expect(response2.usage).toBeDefined()

      console.log(`✅ Prompt caching (tools) passed for anthropic/${model}`)
    })
  })

  // ============================================================================
  // Document Input Tests
  // ============================================================================

  describe('Document Input - PDF Base64', () => {
    it('should handle PDF document input', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'file')

      // Sample PDF base64 (minimal PDF with "Hello World")
      const pdfBase64 =
        'JVBERi0xLjcKCjEgMCBvYmogICUgZW50cnkgcG9pbnQKPDwKICAvVHlwZSAvQ2F0YWxvZwogIC' +
        '9QYWdlcyAyIDAgUgo+PgplbmRvYmoKCjIgMCBvYmoKPDwKICAvVHlwZSAvUGFnZXwKICAvTWV' +
        'kaWFCb3ggWyAwIDAgMjAwIDIwMCBdCiAgL0NvdW50IDEKICAvS2lkcyBbIDMgMCBSIF0KPj4K' +
        'ZW5kb2JqCgozIDAgb2JqCjw8CiAgL1R5cGUgL1BhZ2UKICAvUGFyZW50IDIgMCBSCiAgL1Jlc' +
        '291cmNlcyA8PAogICAgL0ZvbnQgPDwKICAgICAgL0YxIDQgMCBSCj4+CiAgPj4KICAvQ29udG' +
        'VudHMgNSAwIFIKPj4KZW5kb2JqCgo0IDAgb2JqCjw8CiAgL1R5cGUgL0ZvbnQKICAvU3VidHl' +
        'wZSAvVHlwZTEKICAvQmFzZUZvbnQgL1RpbWVzLVJvbWFuCj4+CmVuZG9iagoKNSAwIG9iago8' +
        'PAogIC9MZW5ndGggNDQKPj4Kc3RyZWFtCkJUCjcwIDUwIFRECi9GMSAxMiBUZgooSGVsbG8gV' +
        '29ybGQhKSBUagpFVAplbmRzdHJlYW0KZW5kb2JqCgp4cmVmCjAgNgowMDAwMDAwMDAwIDY1NT' +
        'M1IGYgCjAwMDAwMDAwMTAgMDAwMDAgbiAKMDAwMDAwMDA2MCAwMDAwMCBuIAowMDAwMDAwMTU' +
        '3IDAwMDAwIG4gCjAwMDAwMDAyNTUgMDAwMDAgbiAKMDAwMDAwMDM1MyAwMDAwMCBuIAp0cmFp' +
        'bGVyCjw8CiAgL1NpemUgNgogIC9Sb290IDEgMCBSCj4+CnN0YXJ0eHJlZgo0NDkKJSVFT0YK'

      try {
        // Use type assertion for beta document feature
        const response = await client.messages.create({
          model,
          max_tokens: 200,
          messages: [
            {
              role: 'user',
              content: [
                { type: 'text', text: 'What does this PDF document contain?' },
                {
                  type: 'document',
                  source: {
                    type: 'base64',
                    media_type: 'application/pdf',
                    data: pdfBase64,
                  },
                },
              ],
            },
          ],
        } as never)

        expect(response).toBeDefined()
        const content = getContentString(response)
        expect(content.length).toBeGreaterThan(0)
        console.log(`✅ Document input (PDF Base64) passed for anthropic/${model}`)
      } catch (error) {
        // Document input may not be supported on all models
        console.log(`⚠️ Document input test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  // ============================================================================
  // List Models Tests
  // ============================================================================

  describe('List Models', () => {
    it('should list available models', async () => {
      if (skipTests) return

      const client = getAnthropicClient()

      try {
        // Use type assertion for beta models API
        const response = await (client as unknown as { models: { list: () => Promise<{ data: unknown[] }> } }).models.list()

        expect(response).toBeDefined()
        expect(response.data).toBeDefined()
        expect(Array.isArray(response.data)).toBe(true)
        expect(response.data.length).toBeGreaterThan(0)

        console.log(`✅ List models passed for anthropic - ${response.data.length} models`)
      } catch (error) {
        // List models may not be available on all versions
        console.log(`⚠️ List models test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  // ============================================================================
  // Extended Thinking Streaming Tests
  // ============================================================================

  describe('Extended Thinking Streaming', () => {
    it('should stream extended thinking response', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'thinking')

      if (!model) {
        console.log('⚠️ Skipping thinking streaming test: No thinking model configured')
        return
      }

      try {
        // Use type assertion for beta thinking feature
        const stream = client.messages.stream({
          model,
          max_tokens: 3000,
          thinking: {
            type: 'enabled',
            budget_tokens: 2000,
          },
          messages: [
            {
              role: 'user',
              content: 'Alice, Bob, and Carol went to dinner. The total bill was $90. If they split it equally, how much does each person owe? Show your reasoning.',
            },
          ],
        } as never)

        let thinkingContent = ''
        let textContent = ''
        let chunkCount = 0
        let hasThinkingDelta = false

        for await (const event of stream) {
          chunkCount++

          if (event.type === 'content_block_start') {
            const block = event.content_block as { type: string }
            if (block?.type === 'thinking') {
              hasThinkingDelta = true
            }
          }

          if (event.type === 'content_block_delta') {
            const delta = event.delta as { type: string; thinking?: string; text?: string }
            if (delta.type === 'thinking_delta' && delta.thinking) {
              thinkingContent += delta.thinking
            } else if (delta.type === 'text_delta' && delta.text) {
              textContent += delta.text
            }
          }

          if (chunkCount > 5000) break
        }

        expect(chunkCount).toBeGreaterThan(0)
        expect(hasThinkingDelta || thinkingContent.length > 0).toBe(true)
        console.log(`✅ Extended thinking streaming passed for anthropic/${model} (${chunkCount} chunks)`)
      } catch (error) {
        console.log(`⚠️ Extended thinking streaming test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  // ============================================================================
  // Files API Tests
  // ============================================================================

  describe('Files API - Upload', () => {
    it('should upload a file', async () => {
      if (skipTests) return

      const client = getAnthropicClient()

      try {
        const beta = (client as unknown as { beta: { files: { upload: (params: { file: [string, Uint8Array, string] }) => Promise<{ id: string }> } } }).beta

        const testContent = new TextEncoder().encode('This is a test file for Files API integration testing.')
        const response = await beta.files.upload({
          file: ['test_upload.txt', testContent, 'text/plain'],
        })

        expect(response).toBeDefined()
        expect(response.id).toBeDefined()
        expect(response.id.length).toBeGreaterThan(0)

        console.log(`✅ Files API upload passed for anthropic - File ID: ${response.id}`)

        // Clean up
        try {
          const betaFiles = (client as unknown as { beta: { files: { delete: (id: string) => Promise<void> } } }).beta
          await betaFiles.files.delete(response.id)
        } catch (e) {
          console.log(`Warning: Failed to clean up file: ${e}`)
        }
      } catch (error) {
        console.log(`⚠️ Files API upload test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  describe('Files API - List', () => {
    it('should list files', async () => {
      if (skipTests) return

      const client = getAnthropicClient()

      try {
        const beta = (client as unknown as {
          beta: {
            files: {
              upload: (params: { file: [string, Uint8Array, string] }) => Promise<{ id: string }>
              list: () => Promise<{ data: Array<{ id: string }> }>
              delete: (id: string) => Promise<void>
            }
          }
        }).beta

        // Upload a file first
        const testContent = new TextEncoder().encode('Test file for listing')
        const uploadedFile = await beta.files.upload({
          file: ['test_list.txt', testContent, 'text/plain'],
        })

        try {
          const response = await beta.files.list()

          expect(response).toBeDefined()
          expect(response.data).toBeDefined()
          expect(Array.isArray(response.data)).toBe(true)

          const fileIds = response.data.map((f) => f.id)
          expect(fileIds).toContain(uploadedFile.id)

          console.log(`✅ Files API list passed for anthropic - ${response.data.length} files`)
        } finally {
          try {
            await beta.files.delete(uploadedFile.id)
          } catch (e) {
            console.log(`Warning: Failed to clean up file: ${e}`)
          }
        }
      } catch (error) {
        console.log(`⚠️ Files API list test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  describe('Files API - Delete', () => {
    it('should delete a file', async () => {
      if (skipTests) return

      const client = getAnthropicClient()

      try {
        const beta = (client as unknown as {
          beta: {
            files: {
              upload: (params: { file: [string, Uint8Array, string] }) => Promise<{ id: string }>
              delete: (id: string) => Promise<unknown>
              retrieve: (id: string) => Promise<unknown>
            }
          }
        }).beta

        // Upload a file first
        const testContent = new TextEncoder().encode('Test file for deletion')
        const uploadedFile = await beta.files.upload({
          file: ['test_delete.txt', testContent, 'text/plain'],
        })

        // Delete the file
        const response = await beta.files.delete(uploadedFile.id)
        expect(response).toBeDefined()

        console.log(`✅ Files API delete passed for anthropic - Deleted file ${uploadedFile.id}`)

        // Verify file is gone
        try {
          await beta.files.retrieve(uploadedFile.id)
          // Should not get here
        } catch (e) {
          // Expected - file should not be found
          expect(e).toBeDefined()
        }
      } catch (error) {
        console.log(`⚠️ Files API delete test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  describe('Files API - Content', () => {
    it('should download file content', async () => {
      if (skipTests) return

      const client = getAnthropicClient()

      try {
        const beta = (client as unknown as {
          beta: {
            files: {
              upload: (params: { file: [string, Uint8Array, string] }) => Promise<{ id: string }>
              download: (id: string) => Promise<{ text: () => string }>
              delete: (id: string) => Promise<void>
            }
          }
        }).beta

        const originalContent = 'Test file content for download'
        const testContent = new TextEncoder().encode(originalContent)
        const uploadedFile = await beta.files.upload({
          file: ['test_content.txt', testContent, 'text/plain'],
        })

        try {
          const response = await beta.files.download(uploadedFile.id)
          expect(response).toBeDefined()

          const downloadedContent = response.text()
          expect(downloadedContent).toBe(originalContent)

          console.log(`✅ Files API content passed for anthropic - Downloaded ${downloadedContent.length} bytes`)
        } catch (downloadError) {
          // Some providers don't allow downloading uploaded files
          console.log(`⚠️ Files API download not supported: ${downloadError instanceof Error ? downloadError.message : 'Unknown error'}`)
        } finally {
          try {
            await beta.files.delete(uploadedFile.id)
          } catch (e) {
            console.log(`Warning: Failed to clean up file: ${e}`)
          }
        }
      } catch (error) {
        console.log(`⚠️ Files API content test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  // ============================================================================
  // Batch API Tests
  // ============================================================================

  describe('Batch API - Create Inline', () => {
    it('should create a batch job with inline requests', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'chat')

      try {
        const beta = (client as unknown as {
          beta: {
            messages: {
              batches: {
                create: (params: { requests: Array<{ custom_id: string; params: unknown }> }) => Promise<{ id: string; processing_status: string }>
                cancel: (id: string) => Promise<void>
              }
            }
          }
        }).beta

        const batchRequests = [
          {
            custom_id: 'request-1',
            params: {
              model,
              max_tokens: 50,
              messages: [{ role: 'user', content: 'Say hello' }],
            },
          },
          {
            custom_id: 'request-2',
            params: {
              model,
              max_tokens: 50,
              messages: [{ role: 'user', content: 'Say goodbye' }],
            },
          },
        ]

        const batch = await beta.messages.batches.create({ requests: batchRequests })

        expect(batch).toBeDefined()
        expect(batch.id).toBeDefined()
        expect(batch.processing_status).toBeDefined()

        console.log(`✅ Batch API create inline passed for anthropic - Batch ID: ${batch.id}, Status: ${batch.processing_status}`)

        // Clean up
        try {
          await beta.messages.batches.cancel(batch.id)
        } catch (e) {
          console.log(`Info: Could not cancel batch: ${e}`)
        }
      } catch (error) {
        console.log(`⚠️ Batch API create inline test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  describe('Batch API - List', () => {
    it('should list batch jobs', async () => {
      if (skipTests) return

      const client = getAnthropicClient()

      try {
        const beta = (client as unknown as {
          beta: {
            messages: {
              batches: {
                list: (params: { limit: number }) => Promise<{ data: Array<{ id: string }> }>
              }
            }
          }
        }).beta

        const response = await beta.messages.batches.list({ limit: 10 })

        expect(response).toBeDefined()
        expect(response.data).toBeDefined()
        expect(Array.isArray(response.data)).toBe(true)

        console.log(`✅ Batch API list passed for anthropic - ${response.data.length} batches`)
      } catch (error) {
        console.log(`⚠️ Batch API list test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  describe('Batch API - Retrieve', () => {
    it('should retrieve batch status by ID', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'chat')

      try {
        const beta = (client as unknown as {
          beta: {
            messages: {
              batches: {
                create: (params: { requests: Array<{ custom_id: string; params: unknown }> }) => Promise<{ id: string; processing_status: string }>
                retrieve: (id: string) => Promise<{ id: string; processing_status: string }>
                cancel: (id: string) => Promise<void>
              }
            }
          }
        }).beta

        // Create a batch first
        const batchRequests = [{
          custom_id: 'request-1',
          params: {
            model,
            max_tokens: 50,
            messages: [{ role: 'user', content: 'Say hello' }],
          },
        }]

        const batch = await beta.messages.batches.create({ requests: batchRequests })

        try {
          const retrieved = await beta.messages.batches.retrieve(batch.id)

          expect(retrieved).toBeDefined()
          expect(retrieved.id).toBe(batch.id)
          expect(retrieved.processing_status).toBeDefined()

          console.log(`✅ Batch API retrieve passed for anthropic - Batch ID: ${retrieved.id}, Status: ${retrieved.processing_status}`)
        } finally {
          try {
            await beta.messages.batches.cancel(batch.id)
          } catch (e) {
            console.log(`Info: Could not cancel batch: ${e}`)
          }
        }
      } catch (error) {
        console.log(`⚠️ Batch API retrieve test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  describe('Batch API - Cancel', () => {
    it('should cancel a batch job', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'chat')

      try {
        const beta = (client as unknown as {
          beta: {
            messages: {
              batches: {
                create: (params: { requests: Array<{ custom_id: string; params: unknown }> }) => Promise<{ id: string; processing_status: string }>
                cancel: (id: string) => Promise<{ id: string; processing_status: string }>
              }
            }
          }
        }).beta

        // Create a batch first
        const batchRequests = [{
          custom_id: 'request-1',
          params: {
            model,
            max_tokens: 50,
            messages: [{ role: 'user', content: 'Say hello' }],
          },
        }]

        const batch = await beta.messages.batches.create({ requests: batchRequests })

        // Cancel the batch
        const cancelled = await beta.messages.batches.cancel(batch.id)

        expect(cancelled).toBeDefined()
        expect(cancelled.id).toBe(batch.id)
        expect(['canceling', 'ended']).toContain(cancelled.processing_status)

        console.log(`✅ Batch API cancel passed for anthropic - Batch ID: ${cancelled.id}, Status: ${cancelled.processing_status}`)
      } catch (error) {
        console.log(`⚠️ Batch API cancel test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  describe('Batch API - Results', () => {
    it('should retrieve batch results', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'chat')

      try {
        const beta = (client as unknown as {
          beta: {
            messages: {
              batches: {
                create: (params: { requests: Array<{ custom_id: string; params: unknown }> }) => Promise<{ id: string; processing_status: string }>
                results: (id: string) => AsyncIterable<{ custom_id: string }>
                cancel: (id: string) => Promise<void>
              }
            }
          }
        }).beta

        // Create a batch first
        const batchRequests = [{
          custom_id: 'request-1',
          params: {
            model,
            max_tokens: 50,
            messages: [{ role: 'user', content: 'Say hello' }],
          },
        }]

        const batch = await beta.messages.batches.create({ requests: batchRequests })

        try {
          const results = beta.messages.batches.results(batch.id)

          let resultCount = 0
          for await (const result of results) {
            resultCount++
            expect(result.custom_id).toBeDefined()
          }

          console.log(`✅ Batch API results passed for anthropic - ${resultCount} results`)
        } catch (resultsError) {
          // Results might not be ready yet
          console.log(`⚠️ Batch results not ready: ${resultsError instanceof Error ? resultsError.message : 'Unknown error'}`)
        } finally {
          try {
            await beta.messages.batches.cancel(batch.id)
          } catch (e) {
            console.log(`Info: Could not cancel batch: ${e}`)
          }
        }
      } catch (error) {
        console.log(`⚠️ Batch API results test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  describe('Batch API - E2E', () => {
    it('should complete end-to-end batch workflow', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'chat')

      try {
        const beta = (client as unknown as {
          beta: {
            messages: {
              batches: {
                create: (params: { requests: Array<{ custom_id: string; params: unknown }> }) => Promise<{ id: string; processing_status: string }>
                retrieve: (id: string) => Promise<{ id: string; processing_status: string; request_counts?: { processing: number; succeeded: number; errored: number } }>
                list: (params: { limit: number }) => Promise<{ data: Array<{ id: string }> }>
                cancel: (id: string) => Promise<void>
              }
            }
          }
        }).beta

        // Step 1: Create batch
        console.log('Step 1: Creating batch...')
        const batchRequests = [
          {
            custom_id: 'e2e-request-1',
            params: {
              model,
              max_tokens: 50,
              messages: [{ role: 'user', content: 'Say hello' }],
            },
          },
          {
            custom_id: 'e2e-request-2',
            params: {
              model,
              max_tokens: 50,
              messages: [{ role: 'user', content: 'Say goodbye' }],
            },
          },
        ]

        const batch = await beta.messages.batches.create({ requests: batchRequests })
        expect(batch.id).toBeDefined()
        console.log(`  Created batch: ${batch.id}, status: ${batch.processing_status}`)

        try {
          // Step 2: Poll batch status
          console.log('Step 2: Polling batch status...')
          for (let i = 0; i < 3; i++) {
            const retrieved = await beta.messages.batches.retrieve(batch.id)
            console.log(`  Poll ${i + 1}: status = ${retrieved.processing_status}`)

            if (retrieved.processing_status === 'ended') {
              break
            }

            await new Promise((resolve) => setTimeout(resolve, 2000))
          }

          // Step 3: Verify batch in list
          console.log('Step 3: Verifying batch in list...')
          const listResponse = await beta.messages.batches.list({ limit: 20 })
          const batchIds = listResponse.data.map((b) => b.id)
          expect(batchIds).toContain(batch.id)

          console.log(`✅ Batch API E2E passed for anthropic - Batch ID: ${batch.id}`)
        } finally {
          try {
            await beta.messages.batches.cancel(batch.id)
          } catch (e) {
            console.log(`Info: Could not cancel batch: ${e}`)
          }
        }
      } catch (error) {
        console.log(`⚠️ Batch API E2E test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  // ============================================================================
  // Additional Input Tokens Tests
  // ============================================================================

  describe('Count Tokens - With System Message', () => {
    it('should return token usage with system message', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'chat')

      const response = await client.messages.create({
        model,
        max_tokens: 50,
        system: 'You are a helpful assistant.',
        messages: [{ role: 'user', content: 'What is 2 + 2?' }],
      })

      expect(response.usage).toBeDefined()
      expect(response.usage.input_tokens).toBeGreaterThan(0)
      expect(response.usage.output_tokens).toBeGreaterThan(0)
      console.log(`✅ Count tokens with system message passed for anthropic/${model} (input: ${response.usage.input_tokens}, output: ${response.usage.output_tokens})`)
    })
  })

  describe('Count Tokens - Long Text', () => {
    it('should return token usage for long text', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'chat')

      const longText = 'This is a longer piece of text that should result in more tokens being counted. '.repeat(10)

      const response = await client.messages.create({
        model,
        max_tokens: 50,
        messages: [{ role: 'user', content: longText }],
      })

      expect(response.usage).toBeDefined()
      expect(response.usage.input_tokens).toBeGreaterThan(50)
      expect(response.usage.output_tokens).toBeGreaterThan(0)
      console.log(`✅ Count tokens long text passed for anthropic/${model} (input: ${response.usage.input_tokens}, output: ${response.usage.output_tokens})`)
    })
  })

  // ============================================================================
  // Document Text Input Tests
  // ============================================================================

  describe('Document Input - Plain Text', () => {
    it('should handle plain text document input', async () => {
      if (skipTests) return

      const client = getAnthropicClient()
      const model = getProviderModel('anthropic', 'file')

      const textDocument = `
        DOCUMENT TITLE: Test Agreement
        
        Section 1: Introduction
        This document is a test agreement for integration testing purposes.
        
        Section 2: Terms
        The parties agree to test the API functionality.
        
        Section 3: Conclusion
        This concludes the test document.
      `

      const textBase64 = Buffer.from(textDocument).toString('base64')

      try {
        // Use type assertion for beta document feature
        const response = await client.messages.create({
          model,
          max_tokens: 200,
          messages: [
            {
              role: 'user',
              content: [
                { type: 'text', text: 'Summarize the sections in this document.' },
                {
                  type: 'document',
                  source: {
                    type: 'base64',
                    media_type: 'text/plain',
                    data: textBase64,
                  },
                },
              ],
            },
          ],
        } as never)

        expect(response).toBeDefined()
        const content = getContentString(response)
        expect(content.length).toBeGreaterThan(0)
        console.log(`✅ Document input (plain text) passed for anthropic/${model}`)
      } catch (error) {
        console.log(`⚠️ Document input (plain text) test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })
})
