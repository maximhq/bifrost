/**
 * Bedrock Integration Tests - Cross-Provider Support
 *
 * This test suite uses the AWS SDK (v3) to test against multiple AI providers through Bifrost.
 * Tests automatically run against all available providers with proper capability filtering.
 * All requests include the x-model-provider header to route to the appropriate provider.
 *
 * Test Scenarios:
 * 1. Simple chat (converse)
 * 2. Multi-turn conversation (converse)
 * 3. Streaming chat (converse-stream)
 * 4. Single tool call (converse)
 * 5. Multiple tool calls (converse)
 * 6. End-to-end tool calling (converse)
 * 7. Image analysis (converse)
 * 8. System message handling (converse)
 */

import {
  BedrockRuntimeClient,
  ConverseCommand,
  ConverseStreamCommand,
  type ContentBlock,
  type Message,
  type Tool,
  type ToolConfiguration,
  type ToolResultContentBlock,
  type ToolUseBlock,
} from '@aws-sdk/client-bedrock-runtime'
import { describe, expect, it } from 'vitest'

import {
  getConfig,
  getIntegrationUrl,
  getProviderModel,
} from '../src/utils/config-loader'

import {
  BASE64_IMAGE,
  CALCULATOR_TOOL,
  LOCATION_KEYWORDS,
  MULTI_TURN_MESSAGES,
  MULTIPLE_TOOL_CALL_MESSAGES,
  SIMPLE_CHAT_MESSAGES,
  WEATHER_KEYWORDS,
  WEATHER_TOOL,
  mockToolResponse,
  type ChatMessage,
  type ToolDefinition,
} from '../src/utils/common'

import {
  formatProviderModel,
  getCrossProviderParamsWithVkForScenario,
  shouldSkipNoProviders,
  type ProviderModelVkParam,
} from '../src/utils/parametrize'

// ============================================================================
// Helper Functions
// ============================================================================

function getBedrockRuntimeClient(): BedrockRuntimeClient {
  const baseUrl = getIntegrationUrl('bedrock')
  const config = getConfig()
  const integrationSettings = config.getIntegrationSettings('bedrock')
  const region = (integrationSettings.region as string) || 'us-west-2'

  return new BedrockRuntimeClient({
    region,
    endpoint: baseUrl,
    credentials: {
      accessKeyId: process.env.AWS_ACCESS_KEY_ID || '',
      secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY || '',
    },
    requestHandler: {
      requestTimeout: 300000, // 5 minutes
    } as never,
  })
}

function convertToBedrockMessages(messages: ChatMessage[]): Message[] {
  const bedrockMessages: Message[] = []

  for (const msg of messages) {
    if (msg.role === 'system') {
      continue
    }

    const content: ContentBlock[] = []

    if (Array.isArray(msg.content)) {
      for (const item of msg.content) {
        if (item.type === 'text') {
          content.push({ text: item.text })
        } else if (item.type === 'image_url' && item.image_url) {
          const url = item.image_url.url
          if (url.startsWith('data:image')) {
            const [header, data] = url.split(',')
            const mediaType = header.split(';')[0].split(':')[1]
            const format = mediaType.split('/')[1] as 'png' | 'jpeg' | 'gif' | 'webp'
            const imageBytes = Buffer.from(data, 'base64')
            content.push({
              image: {
                format,
                source: { bytes: imageBytes },
              },
            })
          }
        }
      }
    } else {
      content.push({ text: msg.content })
    }

    const role = msg.role === 'user' ? 'user' : 'assistant'
    bedrockMessages.push({ role, content })
  }

  return bedrockMessages
}

function convertToBedrockTools(tools: ToolDefinition[]): ToolConfiguration {
  const bedrockTools: Tool[] = tools.map((tool) => ({
    toolSpec: {
      name: tool.name,
      description: tool.description,
      inputSchema: { json: tool.parameters },
    },
  }))

  return { tools: bedrockTools }
}

function extractSystemMessages(messages: ChatMessage[]): { text: string }[] {
  return messages
    .filter((msg) => msg.role === 'system')
    .map((msg) => ({ text: msg.content as string }))
}

function extractToolCalls(response: { output?: { message?: Message } }): Array<{
  id: string
  name: string
  arguments: Record<string, unknown>
}> {
  const toolCalls: Array<{
    id: string
    name: string
    arguments: Record<string, unknown>
  }> = []

  const message = response.output?.message
  if (!message?.content) return toolCalls

  for (const item of message.content) {
    if ('toolUse' in item && item.toolUse) {
      const toolUse = item.toolUse as ToolUseBlock
      toolCalls.push({
        id: toolUse.toolUseId || '',
        name: toolUse.name || '',
        arguments: (toolUse.input as Record<string, unknown>) || {},
      })
    }
  }

  return toolCalls
}

function assertValidChatResponse(response: { output?: { message?: Message } }): void {
  expect(response).toBeDefined()
  expect(response.output).toBeDefined()
  expect(response.output?.message).toBeDefined()
  expect(response.output?.message?.content).toBeDefined()
  expect(response.output?.message?.content?.length).toBeGreaterThan(0)
}

function assertHasToolCalls(
  response: { output?: { message?: Message } },
  expectedCount?: number
): void {
  const toolCalls = extractToolCalls(response)
  expect(toolCalls.length).toBeGreaterThan(0)
  if (expectedCount !== undefined) {
    expect(toolCalls.length).toBe(expectedCount)
  }
}

function getTextContent(response: { output?: { message?: Message } }): string {
  const message = response.output?.message
  if (!message?.content) return ''

  for (const item of message.content) {
    if ('text' in item && item.text) {
      return item.text
    }
  }
  return ''
}

// ============================================================================
// Test Suite
// ============================================================================

describe('Bedrock SDK Integration Tests', () => {
  // ============================================================================
  // Simple Chat Tests
  // ============================================================================

  describe('Simple Chat', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('simple_chat', ['bedrock'])

    it.each(testCases)(
      'should complete a simple chat - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for simple_chat')
          return
        }

        const client = getBedrockRuntimeClient()
        const messages = convertToBedrockMessages(SIMPLE_CHAT_MESSAGES)
        const modelId = formatProviderModel(provider, model)

        const command = new ConverseCommand({
          modelId,
          messages,
          inferenceConfig: { maxTokens: 100 },
        })

        const response = await client.send(command)
        assertValidChatResponse(response)

        const textContent = getTextContent(response)
        expect(textContent.length).toBeGreaterThan(0)
        console.log(`✅ Simple chat passed for ${modelId}`)
      }
    )
  })

  // ============================================================================
  // Multi-turn Conversation Tests
  // ============================================================================

  describe('Multi-turn Conversation', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('multi_turn_conversation', ['bedrock'])

    it.each(testCases)(
      'should handle multi-turn conversation - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for multi_turn_conversation')
          return
        }

        const client = getBedrockRuntimeClient()
        const messages = convertToBedrockMessages(MULTI_TURN_MESSAGES)
        const modelId = formatProviderModel(provider, model)

        const command = new ConverseCommand({
          modelId,
          messages,
          inferenceConfig: { maxTokens: 150 },
        })

        const response = await client.send(command)
        assertValidChatResponse(response)

        const textContent = getTextContent(response).toLowerCase()
        const populationKeywords = ['population', 'million', 'people', 'inhabitants', 'resident']
        expect(populationKeywords.some((word) => textContent.includes(word))).toBe(true)
        console.log(`✅ Multi-turn conversation passed for ${modelId}`)
      }
    )
  })

  // ============================================================================
  // Streaming Tests
  // ============================================================================

  describe('Streaming Chat', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('streaming', ['bedrock'])

    it.each(testCases)(
      'should stream chat response - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for streaming')
          return
        }

        const client = getBedrockRuntimeClient()
        const messages = convertToBedrockMessages([
          { role: 'user', content: 'Say hello in exactly 3 words.' },
        ])
        const modelId = formatProviderModel(provider, model)

        const command = new ConverseStreamCommand({
          modelId,
          messages,
          inferenceConfig: { maxTokens: 100 },
        })

        const response = await client.send(command)
        const chunks: string[] = []

        if (response.stream) {
          for await (const event of response.stream) {
            if (event.contentBlockDelta) {
              const delta = event.contentBlockDelta.delta
              if (delta && 'text' in delta && delta.text) {
                chunks.push(delta.text)
              }
            }
          }
        }

        const combinedText = chunks.join('')
        expect(combinedText.length).toBeGreaterThan(0)
        console.log(`✅ Streaming chat passed for ${modelId}`)
      }
    )
  })

  // ============================================================================
  // Streaming Client Disconnect Tests
  // ============================================================================

  describe('Streaming Chat - Client Disconnect', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('streaming', ['bedrock'])

    it.each(testCases)(
      'should handle client disconnect mid-stream - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for streaming')
          return
        }

        const client = getBedrockRuntimeClient()
        const abortController = new AbortController()

        // Request a longer response to ensure we have time to abort mid-stream
        const messages = convertToBedrockMessages([
          { role: 'user', content: 'Write a detailed essay about the history of computing, including at least 10 paragraphs.' },
        ])
        const modelId = formatProviderModel(provider, model)

        const command = new ConverseStreamCommand({
          modelId,
          messages,
          inferenceConfig: { maxTokens: 1000 },
        })

        const response = await client.send(command, {
          abortSignal: abortController.signal,
        })

        let chunkCount = 0
        let content = ''
        let wasAborted = false

        try {
          if (response.stream) {
            for await (const event of response.stream) {
              chunkCount++
              if (event.contentBlockDelta) {
                const delta = event.contentBlockDelta.delta
                if (delta && 'text' in delta && delta.text) {
                  content += delta.text
                }
              }

              // Abort after receiving a few chunks
              if (chunkCount >= 5) {
                abortController.abort()
              }
            }
          }
        } catch (error) {
          wasAborted = true
          expect(error).toBeDefined()
          // The error should be an AbortError or contain abort-related message
          const errorMessage = error instanceof Error ? error.message.toLowerCase() : String(error).toLowerCase()
          const errorName = (error as { name?: string })?.name?.toLowerCase() || ''
          const isAbortError = errorMessage.includes('abort') ||
                               errorMessage.includes('cancel') ||
                               errorName.includes('abort') ||
                               error instanceof DOMException ||
                               (error as { name?: string })?.name === 'AbortError'
          expect(isAbortError).toBe(true)
        }

        // Verify we received some content before aborting
        expect(chunkCount).toBeGreaterThanOrEqual(5)
        expect(content.length).toBeGreaterThan(0)
        expect(wasAborted).toBe(true)
        console.log(`✅ Streaming client disconnect passed for ${modelId} (${chunkCount} chunks before abort)`)
      }
    )
  })

  // ============================================================================
  // Tool Calling Tests
  // ============================================================================

  describe('Single Tool Call', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('tool_calls', ['bedrock'])

    it.each(testCases)(
      'should make a single tool call - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for tool_calls')
          return
        }

        const client = getBedrockRuntimeClient()
        const toolModel = getProviderModel(provider, 'tools')
        const modelId = formatProviderModel(provider, toolModel || model)

        const messages = convertToBedrockMessages([
          { role: 'user', content: "What's the weather in Boston?" },
        ])
        const toolConfig = convertToBedrockTools([WEATHER_TOOL])
        toolConfig.toolChoice = { any: {} }

        const command = new ConverseCommand({
          modelId,
          messages,
          toolConfig,
          inferenceConfig: { maxTokens: 500 },
        })

        const response = await client.send(command)
        assertHasToolCalls(response, 1)

        const toolCalls = extractToolCalls(response)
        expect(toolCalls[0].name).toBe('get_weather')
        console.log(`✅ Single tool call passed for ${modelId}`)
      }
    )
  })

  describe('Multiple Tool Calls', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('multiple_tool_calls', ['bedrock'])

    it.each(testCases)(
      'should make multiple tool calls - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for multiple_tool_calls')
          return
        }

        const client = getBedrockRuntimeClient()
        const toolModel = getProviderModel(provider, 'tools')
        const modelId = formatProviderModel(provider, toolModel || model)

        const messages = convertToBedrockMessages(MULTIPLE_TOOL_CALL_MESSAGES)
        const toolConfig = convertToBedrockTools([WEATHER_TOOL, CALCULATOR_TOOL])
        toolConfig.toolChoice = { any: {} }

        const command = new ConverseCommand({
          modelId,
          messages,
          toolConfig,
          inferenceConfig: { maxTokens: 200 },
        })

        const response = await client.send(command)
        const toolCalls = extractToolCalls(response)
        expect(toolCalls.length).toBeGreaterThanOrEqual(1)

        const toolNames = toolCalls.map((tc) => tc.name)
        const expectedTools = ['get_weather', 'calculate']
        expect(toolNames.some((name) => expectedTools.includes(name))).toBe(true)
        console.log(`✅ Multiple tool calls passed for ${modelId}`)
      }
    )
  })

  describe('End-to-End Tool Calling', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('end2end_tool_calling', ['bedrock'])

    it.each(testCases)(
      'should complete end-to-end tool calling - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for end2end_tool_calling')
          return
        }

        const client = getBedrockRuntimeClient()
        const toolModel = getProviderModel(provider, 'tools')
        const modelId = formatProviderModel(provider, toolModel || model)

        // Step 1: Initial request
        let messages = convertToBedrockMessages([
          { role: 'user', content: "What's the weather in San Francisco?" },
        ])
        const toolConfig = convertToBedrockTools([WEATHER_TOOL])
        toolConfig.toolChoice = { any: {} }

        const command1 = new ConverseCommand({
          modelId,
          messages,
          toolConfig,
          inferenceConfig: { maxTokens: 500 },
        })

        const response1 = await client.send(command1)
        assertHasToolCalls(response1, 1)

        const toolCalls = extractToolCalls(response1)
        expect(toolCalls[0].name).toBe('get_weather')

        // Step 2: Append assistant response and tool result
        const assistantMessage = response1.output?.message
        if (assistantMessage) {
          messages = [...messages, assistantMessage]
        }

        const toolCall = toolCalls[0]
        const toolResponseText = mockToolResponse(toolCall.name, toolCall.arguments)

        const toolResultContent: ToolResultContentBlock[] = [{ text: toolResponseText }]
        messages.push({
          role: 'user',
          content: [
            {
              toolResult: {
                toolUseId: toolCall.id,
                content: toolResultContent,
                status: 'success',
              },
            },
          ],
        })

        // Step 3: Final request with tool results
        const command2 = new ConverseCommand({
          modelId,
          messages,
          toolConfig,
          inferenceConfig: { maxTokens: 500 },
        })

        const response2 = await client.send(command2)
        assertValidChatResponse(response2)

        const finalText = getTextContent(response2).toLowerCase()
        const weatherLocationKeywords = [...WEATHER_KEYWORDS, ...LOCATION_KEYWORDS, 'san francisco', 'sf']
        expect(weatherLocationKeywords.some((word) => finalText.includes(word))).toBe(true)
        console.log(`✅ End-to-end tool calling passed for ${modelId}`)
      }
    )
  })

  // ============================================================================
  // Image Analysis Tests
  // ============================================================================

  describe('Image Base64', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('image_base64', ['bedrock'])

    it.each(testCases)(
      'should analyze image from Base64 - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for image_base64')
          return
        }

        const client = getBedrockRuntimeClient()
        const visionModel = getProviderModel(provider, 'vision')
        const modelId = formatProviderModel(provider, visionModel || model)

        const messages = convertToBedrockMessages([
          {
            role: 'user',
            content: [
              {
                type: 'text',
                text: 'What do you see in this image? Describe what you see.',
              },
              {
                type: 'image_url',
                image_url: { url: `data:image/png;base64,${BASE64_IMAGE}` },
              },
            ],
          },
        ])

        const command = new ConverseCommand({
          modelId,
          messages,
          inferenceConfig: { maxTokens: 500 },
        })

        const response = await client.send(command)
        assertValidChatResponse(response)

        const textContent = getTextContent(response).toLowerCase()
        const imageKeywords = [
          'image', 'picture', 'photo', 'see', 'visual', 'show',
          'appear', 'color', 'scene', 'pixel', 'red', 'square',
        ]
        const hasImageReference = imageKeywords.some((keyword) => textContent.includes(keyword))
        expect(hasImageReference || textContent.length > 5).toBe(true)
        console.log(`✅ Image Base64 analysis passed for ${modelId}`)
      }
    )
  })

  // ============================================================================
  // System Message Tests
  // ============================================================================

  describe('System Message', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('simple_chat', ['bedrock'])

    it.each(testCases)(
      'should handle system message - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for simple_chat')
          return
        }

        const client = getBedrockRuntimeClient()
        const modelId = formatProviderModel(provider, model)

        const messagesWithSystem: ChatMessage[] = [
          { role: 'system', content: 'You are a helpful assistant that always responds in exactly 5 words.' },
          { role: 'user', content: 'Hello, how are you?' },
        ]

        const systemMessages = extractSystemMessages(messagesWithSystem)
        const bedrockMessages = convertToBedrockMessages(messagesWithSystem)

        const command = new ConverseCommand({
          modelId,
          messages: bedrockMessages,
          system: systemMessages,
          inferenceConfig: { maxTokens: 50 },
        })

        const response = await client.send(command)
        assertValidChatResponse(response)

        const textContent = getTextContent(response)
        expect(textContent.length).toBeGreaterThan(0)

        // Check if response is approximately 5 words (allow some flexibility)
        const wordCount = textContent.split(/\s+/).length
        expect(wordCount).toBeGreaterThanOrEqual(3)
        expect(wordCount).toBeLessThanOrEqual(10)
        console.log(`✅ System message handling passed for ${modelId}`)
      }
    )
  })
})
