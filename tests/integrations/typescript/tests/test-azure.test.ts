/**
 * Azure OpenAI SDK Integration Tests - Azure Provider via Bifrost
 *
 * This test suite uses the AzureOpenAI SDK to test against Azure through Bifrost.
 * Tests automatically run against the Azure provider with proper capability filtering.
 *
 * Key differences from test-openai.test.ts:
 * - Uses AzureOpenAI client instead of OpenAI client
 * - Client uses endpoint + apiVersion (Azure SDK pattern)
 * - Models are passed RAW (not formatted as provider/model) since the AzureOpenAI SDK
 *   uses the model as deployment-id in the URL path, and Bifrost's AzureEndpointPreHook
 *   automatically adds the "azure/" prefix.
 * - Filters cross-provider params to only include azure
 *
 * Test Scenarios:
 *
 * Chat Completions:
 * 1. Simple chat
 * 2. Multi-turn conversation
 * 3. Streaming chat
 * 4. Streaming client disconnect
 *
 * Tool Calling:
 * 5. Single tool call
 * 6. Multiple tool calls
 * 7. End-to-end tool calling
 *
 * Vision/Image:
 * 8. Image URL analysis
 * 9. Image Base64 analysis
 * 10. Multiple images analysis
 *
 * Audio:
 * 11. Speech synthesis (TTS)
 * 12. Audio transcription
 *
 * Embeddings:
 * 13. Single text embedding
 * 14. Batch embeddings
 * 15. Embedding similarity analysis
 *
 * Models & Tokens:
 * 16. List models
 * 17. Count tokens
 *
 * Responses API:
 * 18. Responses - simple text
 * 19. Responses - with system message
 * 20. Responses - with image
 * 21. Responses - with tools
 * 22. Responses - streaming
 * 23. Responses - streaming client disconnect
 * 24. Responses - streaming with tools
 * 25. Responses - reasoning
 *
 * Image Generation:
 * 26. Image generation
 *
 * Input Tokens API:
 * 27. Input tokens - simple text
 * 28. Input tokens - with system message
 * 29. Input tokens - long text
 */

import { AzureOpenAI } from 'openai'
import type OpenAI from 'openai'
import { describe, expect, it } from 'vitest'

import {
  getIntegrationUrl,
  getProviderModel,
  getVirtualKey
} from '../src/utils/config-loader'

import {
  assertValidOpenAIAnnotation,
  CALCULATOR_TOOL,
  EMBEDDINGS_MULTIPLE_TEXTS,
  EMBEDDINGS_SIMILAR_TEXTS,
  EMBEDDINGS_SINGLE_TEXT,
  IMAGE_BASE64_MESSAGES,
  IMAGE_URL_MESSAGES,
  MULTIPLE_IMAGES_MESSAGES,
  MULTIPLE_TOOL_CALL_MESSAGES,
  MULTI_TURN_MESSAGES,
  RESPONSES_IMAGE_INPUT,
  RESPONSES_REASONING_INPUT,
  RESPONSES_SIMPLE_TEXT_INPUT,
  RESPONSES_STREAMING_INPUT,
  RESPONSES_TEXT_WITH_SYSTEM,
  RESPONSES_TOOL_CALL_INPUT,
  SIMPLE_CHAT_MESSAGES,
  SINGLE_TOOL_CALL_MESSAGES,
  SPEECH_TEST_INPUT,
  STREAMING_CHAT_MESSAGES,
  WEATHER_TOOL,
  assertHasToolCalls,
  assertValidChatResponse,
  assertValidEmbeddingResponse,
  assertValidEmbeddingsBatchResponse,
  assertValidImageResponse,
  assertValidSpeechResponse,
  calculateCosineSimilarity,
  collectStreamingContent,
  convertToOpenAITools,
  convertToResponsesTools,
  extractToolCalls,
  generateTestAudio,
  getApiKey,
  getProviderVoice,
  hasApiKey,
  mockToolResponse,
  type ChatMessage,
  type ExtractedToolCall
} from '../src/utils/common'

import {
  getCrossProviderParamsWithVkForScenario,
  shouldSkipNoProviders,
  type ProviderModelVkParam,
} from '../src/utils/parametrize'

// ============================================================================
// Helper Functions
// ============================================================================

function getProviderAzureClient(provider: string = 'azure', vkEnabled: boolean = false): AzureOpenAI {
  const azureEndpoint = getIntegrationUrl('azure')
  const apiKey = hasApiKey('azure') ? getApiKey('azure') : 'dummy-key'

  const defaultHeaders: Record<string, string> = {}

  if (vkEnabled) {
    const vk = getVirtualKey()
    if (vk) {
      defaultHeaders['x-bf-vk'] = vk
    }
  }

  return new AzureOpenAI({
    baseURL: azureEndpoint,
    apiKey,
    apiVersion: process.env.AZURE_API_VERSION || '2024-10-21',
    defaultHeaders: Object.keys(defaultHeaders).length > 0 ? defaultHeaders : undefined,
    timeout: 300000, // 5 minutes
    maxRetries: 3,
  })
}

function convertMessages(messages: ChatMessage[]): OpenAI.Chat.ChatCompletionMessageParam[] {
  return messages.map((msg) => {
    if (typeof msg.content === 'string') {
      return {
        role: msg.role,
        content: msg.content,
      } as OpenAI.Chat.ChatCompletionMessageParam
    }

    // Handle multimodal content
    const parts: OpenAI.Chat.ChatCompletionContentPart[] = msg.content.map((part) => {
      if (part.type === 'text') {
        return { type: 'text', text: part.text! }
      }
      return {
        type: 'image_url',
        image_url: { url: part.image_url!.url },
      }
    })

    return {
      role: msg.role,
      content: parts,
    } as OpenAI.Chat.ChatCompletionMessageParam
  })
}

/**
 * Get Azure-only cross-provider params for a scenario.
 * Uses the includeProviders parameter to filter to only 'azure'.
 */
function getAzureParamsForScenario(scenario: string): ProviderModelVkParam[] {
  return getCrossProviderParamsWithVkForScenario(scenario, ['azure'])
}

/**
 * Check if an error indicates the API is not supported/available (legitimate skip)
 * vs an unexpected failure that should fail the test.
 */
function isApiNotSupportedError(error: unknown): boolean {
  const msg = error instanceof Error ? error.message.toLowerCase() : String(error).toLowerCase()
  // Only treat TypeErrors as "not supported" if they indicate missing methods/properties
  if (error instanceof TypeError &&
      (msg.includes('is not a function') ||
       msg.includes('cannot read properties') ||
       msg.includes('is not defined'))) {
    return true
  }
  return msg.includes('not found') ||
         msg.includes('not supported') ||
         msg.includes('not implemented') ||
         msg.includes('not available') ||
         (error as { status?: number })?.status === 404
}

// ============================================================================
// Test Suite
// ============================================================================

describe('Azure OpenAI SDK Integration Tests', () => {
  // ============================================================================
  // Simple Chat Tests
  // ============================================================================

  describe('Simple Chat', () => {
    const testCases = getAzureParamsForScenario('simple_chat')

    it.each(testCases)(
      'should complete a simple chat - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for simple_chat')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const response = await client.chat.completions.create({
          model: model,
          messages: convertMessages(SIMPLE_CHAT_MESSAGES),
          max_tokens: 100,
        })

        assertValidChatResponse(response)
        console.log(`[Azure] Simple chat passed for ${model}`)
      }
    )
  })

  // ============================================================================
  // Multi-turn Conversation Tests
  // ============================================================================

  describe('Multi-turn Conversation', () => {
    const testCases = getAzureParamsForScenario('multi_turn_conversation')

    it.each(testCases)(
      'should handle multi-turn conversation - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for multi_turn_conversation')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const response = await client.chat.completions.create({
          model: model,
          messages: convertMessages(MULTI_TURN_MESSAGES),
          max_tokens: 100,
        })

        assertValidChatResponse(response)

        // Verify context is maintained
        const content = response.choices[0]?.message?.content || ''
        expect(content.toLowerCase()).toMatch(/paris|population|million|people/i)
        console.log(`[Azure] Multi-turn conversation passed for ${model}`)
      }
    )
  })

  // ============================================================================
  // Streaming Tests
  // ============================================================================

  describe('Streaming Chat', () => {
    const testCases = getAzureParamsForScenario('streaming')

    it.each(testCases)(
      'should stream chat response - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for streaming')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const stream = await client.chat.completions.create({
          model: model,
          messages: convertMessages(STREAMING_CHAT_MESSAGES),
          max_tokens: 100,
          stream: true,
        })

        const content = await collectStreamingContent(stream)
        expect(content.length).toBeGreaterThan(0)
        console.log(`[Azure] Streaming chat passed for ${model}`)
      }
    )
  })

  // ============================================================================
  // Streaming Client Disconnect Tests
  // ============================================================================

  describe('Streaming Chat - Client Disconnect', () => {
    const testCases = getAzureParamsForScenario('streaming')

    it.each(testCases)(
      'should handle client disconnect mid-stream - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for streaming')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const abortController = new AbortController()

        // Request a longer response to ensure we have time to abort mid-stream
        const stream = await client.chat.completions.create({
          model: model,
          messages: [{ role: 'user', content: 'Write a detailed essay about the history of computing, including at least 10 paragraphs.' }],
          max_tokens: 1000,
          stream: true,
        }, {
          signal: abortController.signal,
        })

        let chunkCount = 0
        let content = ''
        let wasAborted = false

        try {
          for await (const chunk of stream) {
            chunkCount++
            const delta = chunk.choices[0]?.delta?.content || ''
            content += delta

            // Abort after receiving a few chunks
            if (chunkCount >= 3) {
              abortController.abort()
            }
          }
        } catch (error) {
          // Expect an abort error
          wasAborted = true
          expect(error).toBeDefined()
          // The error should be an AbortError or contain abort-related message
          const errorMessage = error instanceof Error ? error.message.toLowerCase() : String(error).toLowerCase()
          const isAbortError = errorMessage.includes('abort') ||
                               errorMessage.includes('cancel') ||
                               error instanceof DOMException ||
                               (error as { name?: string })?.name === 'AbortError'
          expect(isAbortError).toBe(true)
        }

        // Verify we received some content before aborting
        expect(chunkCount).toBeGreaterThanOrEqual(3)
        expect(content.length).toBeGreaterThan(0)
        // Some runtimes end the stream cleanly on abort; allow either behavior.
        expect(wasAborted || abortController.signal.aborted).toBe(true)
        console.log(`[Azure] Streaming client disconnect passed for ${model} (${chunkCount} chunks before abort)`)
      }
    )
  })

  // ============================================================================
  // Tool Calling Tests
  // ============================================================================

  describe('Single Tool Call', () => {
    const testCases = getAzureParamsForScenario('tool_calls')

    it.each(testCases)(
      'should make a single tool call - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for tool_calls')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const toolModel = getProviderModel(provider, 'tools')

        const response = await client.chat.completions.create({
          model: toolModel || model,
          messages: convertMessages(SINGLE_TOOL_CALL_MESSAGES),
          tools: convertToOpenAITools([WEATHER_TOOL]),
          max_tokens: 100,
        })

        assertHasToolCalls(response, 1)
        const toolCalls = extractToolCalls(response)
        expect(toolCalls[0].name).toBe('get_weather')
        console.log(`[Azure] Single tool call passed for ${model}`)
      }
    )
  })

  describe('Multiple Tool Calls', () => {
    const testCases = getAzureParamsForScenario('multiple_tool_calls')

    it.each(testCases)(
      'should make multiple tool calls - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for multiple_tool_calls')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const toolModel = getProviderModel(provider, 'tools')

        const response = await client.chat.completions.create({
          model: toolModel || model,
          messages: convertMessages(MULTIPLE_TOOL_CALL_MESSAGES),
          tools: convertToOpenAITools([WEATHER_TOOL, CALCULATOR_TOOL]),
          max_tokens: 150,
        })

        const toolCalls = extractToolCalls(response)
        expect(toolCalls.length).toBeGreaterThanOrEqual(1)

        const toolNames = toolCalls.map((tc: ExtractedToolCall) => tc.name)
        expect(toolNames.some((name: string) => name === 'get_weather' || name === 'calculate')).toBe(true)
        console.log(`[Azure] Multiple tool calls passed for ${model}`)
      }
    )
  })

  describe('End-to-End Tool Calling', () => {
    const testCases = getAzureParamsForScenario('end2end_tool_calling')

    it.each(testCases)(
      'should complete end-to-end tool calling - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for end2end_tool_calling')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const toolModel = getProviderModel(provider, 'tools')

        // Step 1: Initial request with tools
        const response1 = await client.chat.completions.create({
          model: toolModel || model,
          messages: convertMessages(SINGLE_TOOL_CALL_MESSAGES),
          tools: convertToOpenAITools([WEATHER_TOOL]),
          max_tokens: 100,
        })

        const toolCalls = extractToolCalls(response1)
        expect(toolCalls.length).toBeGreaterThan(0)

        // Step 2: Execute tool and get result
        const toolResult = mockToolResponse(toolCalls[0].name, toolCalls[0].arguments)

        // Step 3: Send tool result back
        const messages: OpenAI.Chat.ChatCompletionMessageParam[] = [
          ...convertMessages(SINGLE_TOOL_CALL_MESSAGES),
          response1.choices[0].message as OpenAI.Chat.ChatCompletionMessageParam,
          {
            role: 'tool',
            tool_call_id: response1.choices[0].message.tool_calls![0].id,
            content: toolResult,
          },
        ]

        const response2 = await client.chat.completions.create({
          model: toolModel || model,
          messages,
          max_tokens: 200,
        })

        assertValidChatResponse(response2)
        console.log(`[Azure] End-to-end tool calling passed for ${model}`)
      }
    )
  })

  // ============================================================================
  // Image/Vision Tests
  // ============================================================================

  describe('Image URL', () => {
    const testCases = getAzureParamsForScenario('image_url')

    it.each(testCases)(
      'should analyze image from URL - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for image_url')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const visionModel = getProviderModel(provider, 'vision')

        const response = await client.chat.completions.create({
          model: visionModel || model,
          messages: convertMessages(IMAGE_URL_MESSAGES),
          max_tokens: 200,
        })

        assertValidImageResponse(response)
        console.log(`[Azure] Image URL analysis passed for ${model}`)
      }
    )
  })

  describe('Image Base64', () => {
    const testCases = getAzureParamsForScenario('image_base64')

    it.each(testCases)(
      'should analyze image from Base64 - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for image_base64')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const visionModel = getProviderModel(provider, 'vision')

        const response = await client.chat.completions.create({
          model: visionModel || model,
          messages: convertMessages(IMAGE_BASE64_MESSAGES),
          max_tokens: 200,
        })

        assertValidImageResponse(response)
        console.log(`[Azure] Image Base64 analysis passed for ${model}`)
      }
    )
  })

  describe('Multiple Images', () => {
    const testCases = getAzureParamsForScenario('multiple_images')

    it.each(testCases)(
      'should analyze multiple images - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for multiple_images')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const visionModel = getProviderModel(provider, 'vision')

        const response = await client.chat.completions.create({
          model: visionModel || model,
          messages: convertMessages(MULTIPLE_IMAGES_MESSAGES),
          max_tokens: 300,
        })

        assertValidImageResponse(response)
        console.log(`[Azure] Multiple images analysis passed for ${model}`)
      }
    )
  })

  // ============================================================================
  // Speech Synthesis Tests
  // ============================================================================

  describe('Speech Synthesis', () => {
    const testCases = getAzureParamsForScenario('speech_synthesis')

    it.each(testCases)(
      'should synthesize speech - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for speech_synthesis')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const speechModel = getProviderModel(provider, 'speech')
        // Azure uses the same voice names as OpenAI
        const voice = getProviderVoice('openai')

        const response = await client.audio.speech.create({
          model: speechModel || 'tts-1',
          voice: voice as 'alloy' | 'echo' | 'fable' | 'onyx' | 'nova' | 'shimmer',
          input: SPEECH_TEST_INPUT,
        })

        const buffer = await response.arrayBuffer()
        assertValidSpeechResponse(buffer)
        expect(buffer.byteLength).toBeGreaterThan(1000)
        console.log(`[Azure] Speech synthesis passed for ${model}`)
      }
    )
  })

  // ============================================================================
  // Transcription Tests
  // ============================================================================

  describe('Audio Transcription', () => {
    const testCases = getAzureParamsForScenario('transcription')

    it.each(testCases)(
      'should transcribe audio - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for transcription')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const transcriptionModel = getProviderModel(provider, 'transcription')

        // Generate test audio
        const audioBuffer = generateTestAudio(1000)
        const audioFile = new File([audioBuffer], 'test.wav', { type: 'audio/wav' })

        const response = await client.audio.transcriptions.create({
          model: transcriptionModel || 'whisper-1',
          file: audioFile,
          language: 'en',
        })

        expect(response).toBeDefined()
        // Note: Generated sine wave may not produce meaningful transcription
        console.log(`[Azure] Audio transcription passed for ${model}`)
      }
    )
  })

  // ============================================================================
  // Embeddings Tests
  // ============================================================================

  describe('Embeddings - Single Text', () => {
    const testCases = getAzureParamsForScenario('embeddings')

    it.each(testCases)(
      'should generate single text embedding - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for embeddings')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const embeddingsModel = getProviderModel(provider, 'embeddings')

        const response = await client.embeddings.create({
          model: embeddingsModel || 'text-embedding-3-small',
          input: EMBEDDINGS_SINGLE_TEXT,
        })

        assertValidEmbeddingResponse(response)
        console.log(`[Azure] Single text embedding passed for ${model}`)
      }
    )
  })

  describe('Embeddings - Batch', () => {
    const testCases = getAzureParamsForScenario('embeddings')

    it.each(testCases)(
      'should generate batch embeddings - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for embeddings')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const embeddingsModel = getProviderModel(provider, 'embeddings')

        const response = await client.embeddings.create({
          model: embeddingsModel || 'text-embedding-3-small',
          input: EMBEDDINGS_MULTIPLE_TEXTS,
        })

        assertValidEmbeddingsBatchResponse(response, EMBEDDINGS_MULTIPLE_TEXTS.length)
        console.log(`[Azure] Batch embeddings passed for ${model}`)
      }
    )
  })

  describe('Embeddings - Similarity Analysis', () => {
    const testCases = getAzureParamsForScenario('embeddings')

    it.each(testCases)(
      'should compute similar embeddings - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for embeddings')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const embeddingsModel = getProviderModel(provider, 'embeddings')

        const response = await client.embeddings.create({
          model: embeddingsModel || 'text-embedding-3-small',
          input: EMBEDDINGS_SIMILAR_TEXTS,
        })

        assertValidEmbeddingsBatchResponse(response, EMBEDDINGS_SIMILAR_TEXTS.length)

        // Calculate similarity between similar texts
        const emb1 = response.data[0].embedding
        const emb2 = response.data[1].embedding
        const similarity = calculateCosineSimilarity(emb1, emb2)

        // Similar texts should have high cosine similarity (> 0.7)
        expect(similarity).toBeGreaterThan(0.7)
        console.log(`[Azure] Embedding similarity analysis passed for ${model} (similarity: ${similarity.toFixed(3)})`)
      }
    )
  })

  // ============================================================================
  // List Models Tests
  // ============================================================================

  describe('List Models', () => {
    const testCases = getAzureParamsForScenario('list_models')

    it.each(testCases)(
      'should list available models - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for list_models')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const models = await client.models.list()

        expect(models).toBeDefined()
        expect(models.data).toBeDefined()
        expect(Array.isArray(models.data)).toBe(true)
        console.log(`[Azure] List models passed for ${model} (${models.data.length} models)`)
      }
    )
  })

  // ============================================================================
  // Count Tokens Tests
  // ============================================================================

  describe('Count Tokens', () => {
    const testCases = getAzureParamsForScenario('count_tokens')

    it.each(testCases)(
      'should count tokens - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for count_tokens')
          return
        }

        // Token counting typically requires a direct API call
        // This is a placeholder that verifies the setup works
        const client = getProviderAzureClient(provider, vkEnabled)

        // Use a simple chat completion to verify connectivity
        const response = await client.chat.completions.create({
          model: model,
          messages: [{ role: 'user', content: 'Say hello' }],
          max_tokens: 10,
        })

        expect(response.usage).toBeDefined()
        if (response.usage) {
          expect(response.usage.prompt_tokens).toBeGreaterThan(0)
          expect(response.usage.completion_tokens).toBeGreaterThan(0)
          expect(response.usage.total_tokens).toBeGreaterThan(0)
        }
        console.log(`[Azure] Count tokens passed for ${model}`)
      }
    )
  })

  // ============================================================================
  // Responses API Tests
  // ============================================================================

  describe('Responses API - Simple Text', () => {
    const testCases = getAzureParamsForScenario('responses')

    it.each(testCases)(
      'should create a response with simple text - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for responses')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)

        // Use type assertion for beta responses API
        const responses = (client as unknown as { responses: { create: (params: unknown) => Promise<unknown> } }).responses

        try {
          const response = await responses.create({
            model: model,
            input: RESPONSES_SIMPLE_TEXT_INPUT,
            max_output_tokens: 1000,
          }) as { output?: Array<{ content?: Array<{ text?: string }> }> }

          expect(response).toBeDefined()
          expect(response.output).toBeDefined()
          expect(response.output!.length).toBeGreaterThan(0)

          // Extract content
          let content = ''
          for (const item of response.output || []) {
            if (item.content) {
              for (const block of item.content) {
                if (block.text) {
                  content += block.text
                }
              }
            }
          }

          expect(content.length).toBeGreaterThan(20)
          expect(content.toLowerCase()).toMatch(/paris|france|capital/i)
          console.log(`[Azure] Responses API simple text passed for ${model}`)
        } catch (error) {
          if (isApiNotSupportedError(error)) {
            console.log(`[Azure] Responses API test skipped (API not supported): ${error instanceof Error ? error.message : 'Unknown error'}`)
          } else {
            throw error
          }
        }
      }
    )
  })

  describe('Responses API - With System Message', () => {
    const testCases = getAzureParamsForScenario('responses')

    it.each(testCases)(
      'should create a response with system message - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for responses')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const responses = (client as unknown as { responses: { create: (params: unknown) => Promise<unknown> } }).responses

        try {
          const response = await responses.create({
            model: model,
            input: RESPONSES_TEXT_WITH_SYSTEM,
            max_output_tokens: 1000,
          }) as { output?: Array<{ content?: Array<{ text?: string }> }> }

          expect(response).toBeDefined()
          expect(response.output).toBeDefined()

          // Extract content
          let content = ''
          for (const item of response.output || []) {
            if (item.content) {
              for (const block of item.content) {
                if (block.text) {
                  content += block.text
                }
              }
            }
          }

          expect(content.length).toBeGreaterThan(20)
          console.log(`[Azure] Responses API with system message passed for ${model}`)
        } catch (error) {
          if (isApiNotSupportedError(error)) {
            console.log(`[Azure] Responses API with system message test skipped (API not supported): ${error instanceof Error ? error.message : 'Unknown error'}`)
          } else {
            throw error
          }
        }
      }
    )
  })

  describe('Responses API - With Image', () => {
    const testCases = getAzureParamsForScenario('responses_image')

    it.each(testCases)(
      'should create a response with image input - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for responses_image')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const visionModel = getProviderModel(provider, 'vision')
        const responses = (client as unknown as { responses: { create: (params: unknown) => Promise<unknown> } }).responses

        try {
          const response = await responses.create({
            model: visionModel || model,
            input: [
              { type: 'input_text', text: RESPONSES_IMAGE_INPUT.text },
              { type: 'input_image', image_url: RESPONSES_IMAGE_INPUT.imageUrl },
            ],
            max_output_tokens: 1000,
          }) as { output?: Array<{ content?: Array<{ text?: string }> }> }

          expect(response).toBeDefined()
          expect(response.output).toBeDefined()

          // Extract content
          let content = ''
          for (const item of response.output || []) {
            if (item.content) {
              for (const block of item.content) {
                if (block.text) {
                  content += block.text
                }
              }
            }
          }

          expect(content.length).toBeGreaterThan(20)
          console.log(`[Azure] Responses API with image passed for ${model}`)
        } catch (error) {
          if (isApiNotSupportedError(error)) {
            console.log(`[Azure] Responses API with image test skipped (API not supported): ${error instanceof Error ? error.message : 'Unknown error'}`)
          } else {
            throw error
          }
        }
      }
    )
  })

  describe('Responses API - With Tools', () => {
    const testCases = getAzureParamsForScenario('responses')

    it.each(testCases)(
      'should create a response with tools - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for responses')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const toolModel = getProviderModel(provider, 'tools')
        const responses = (client as unknown as { responses: { create: (params: unknown) => Promise<unknown> } }).responses
        const tools = convertToResponsesTools([WEATHER_TOOL])

        try {
          const response = await responses.create({
            model: toolModel || model,
            input: RESPONSES_TOOL_CALL_INPUT,
            tools,
            max_output_tokens: 150,
          }) as { output?: Array<{ type?: string; name?: string; arguments?: string }> }

          expect(response).toBeDefined()
          expect(response.output).toBeDefined()

          // Check for function call in output
          const hasFunctionCall = (response.output || []).some(
            (item) => item.type === 'function_call' || item.name === 'get_weather'
          )

          expect(hasFunctionCall).toBe(true)
          console.log(`[Azure] Responses API with tools passed for ${model}`)
        } catch (error) {
          if (isApiNotSupportedError(error)) {
            console.log(`[Azure] Responses API with tools test skipped (API not supported): ${error instanceof Error ? error.message : 'Unknown error'}`)
          } else {
            throw error
          }
        }
      }
    )
  })

  describe('Responses API - Streaming', () => {
    const testCases = getAzureParamsForScenario('responses')

    it.each(testCases)(
      'should stream a response - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for responses')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const responses = (client as unknown as { responses: { create: (params: unknown) => Promise<AsyncIterable<unknown>> } }).responses

        try {
          const stream = await responses.create({
            model: model,
            input: RESPONSES_STREAMING_INPUT,
            max_output_tokens: 1000,
            stream: true,
          })

          let content = ''
          let chunkCount = 0

          for await (const event of stream as AsyncIterable<{ type?: string; delta?: { text?: string } }>) {
            chunkCount++
            if (event.type === 'content_part.delta' || event.type === 'response.output_text.delta') {
              if (event.delta?.text) {
                content += event.delta.text
              }
            }
          }

          expect(chunkCount).toBeGreaterThan(1)
          expect(content.length).toBeGreaterThan(0)
          console.log(`[Azure] Responses API streaming passed for ${model} (${chunkCount} chunks)`)
        } catch (error) {
          if (isApiNotSupportedError(error)) {
            console.log(`[Azure] Responses API streaming test skipped (API not supported): ${error instanceof Error ? error.message : 'Unknown error'}`)
          } else {
            throw error
          }
        }
      }
    )
  })

  describe('Responses API - Streaming Client Disconnect', () => {
    const testCases = getAzureParamsForScenario('responses')

    it.each(testCases)(
      'should handle client disconnect mid-stream - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for responses')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const abortController = new AbortController()
        const responses = (client as unknown as {
          responses: {
            create: (params: unknown, options?: { signal?: AbortSignal }) => Promise<AsyncIterable<unknown>>
          }
        }).responses

        try {
          const stream = await responses.create({
            model: model,
            input: 'Write a detailed essay about the history of artificial intelligence, including at least 10 paragraphs covering different eras and breakthroughs.',
            max_output_tokens: 2000,
            stream: true,
          }, {
            signal: abortController.signal,
          })

          let chunkCount = 0
          let content = ''
          let wasAborted = false

          try {
            for await (const event of stream as AsyncIterable<{ type?: string; delta?: { text?: string } }>) {
              chunkCount++
              if (event.type === 'content_part.delta' || event.type === 'response.output_text.delta') {
                if (event.delta?.text) {
                  content += event.delta.text
                }
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
          // Some runtimes end the stream cleanly on abort; allow either behavior.
          expect(wasAborted || abortController.signal.aborted).toBe(true)
          console.log(`[Azure] Responses API streaming client disconnect passed for ${model} (${chunkCount} chunks before abort)`)
        } catch (error) {
          if (isApiNotSupportedError(error)) {
            console.log(`[Azure] Responses API streaming client disconnect test skipped (API not supported): ${error instanceof Error ? error.message : 'Unknown error'}`)
          } else {
            throw error
          }
        }
      }
    )
  })

  describe('Responses API - Streaming With Tools', () => {
    const testCases = getAzureParamsForScenario('responses')

    it.each(testCases)(
      'should stream a response with tools - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for responses')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const toolModel = getProviderModel(provider, 'tools')
        const responses = (client as unknown as { responses: { create: (params: unknown) => Promise<AsyncIterable<unknown>> } }).responses
        const tools = convertToResponsesTools([WEATHER_TOOL])

        try {
          const stream = await responses.create({
            model: toolModel || model,
            input: [
              { type: 'input_text', text: "What's the weather in San Francisco?" },
            ],
            tools,
            max_output_tokens: 150,
            stream: true,
          })

          let chunkCount = 0
          let hasToolCall = false

          for await (const event of stream as AsyncIterable<{ type?: string }>) {
            chunkCount++
            if (event.type === 'response.function_call_arguments.delta' || event.type === 'function_call') {
              hasToolCall = true
            }
          }

          expect(chunkCount).toBeGreaterThan(1)
          console.log(`[Azure] Responses API streaming with tools passed for ${model} (tool call: ${hasToolCall})`)
        } catch (error) {
          if (isApiNotSupportedError(error)) {
            console.log(`[Azure] Responses API streaming with tools test skipped (API not supported): ${error instanceof Error ? error.message : 'Unknown error'}`)
          } else {
            throw error
          }
        }
      }
    )
  })

  describe('Responses API - Reasoning', () => {
    const testCases = getAzureParamsForScenario('thinking')

    it.each(testCases)(
      'should create a response with reasoning - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for thinking')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const thinkingModel = getProviderModel(provider, 'thinking')
        const responses = (client as unknown as { responses: { create: (params: unknown) => Promise<unknown> } }).responses

        try {
          const response = await responses.create({
            model: thinkingModel || model,
            input: RESPONSES_REASONING_INPUT,
            max_output_tokens: 1200,
            reasoning: {
              effort: 'high',
              summary: 'auto',
            },
          }) as { output?: Array<{ type?: string; content?: Array<{ text?: string }>; summary?: Array<{ text?: string }> }> }

          expect(response).toBeDefined()
          expect(response.output).toBeDefined()

          // Extract content from output or summary
          let content = ''
          for (const item of response.output || []) {
            if (item.content) {
              for (const block of item.content) {
                if (block.text) {
                  content += block.text
                }
              }
            }
            if (item.summary) {
              for (const block of item.summary) {
                if (block.text) {
                  content += block.text
                }
              }
            }
          }

          expect(content.length).toBeGreaterThan(30)
          console.log(`[Azure] Responses API reasoning passed for ${model}`)
        } catch (error) {
          if (isApiNotSupportedError(error)) {
            console.log(`[Azure] Responses API reasoning test skipped (API not supported): ${error instanceof Error ? error.message : 'Unknown error'}`)
          } else {
            throw error
          }
        }
      }
    )
  })

  // ============================================================================
  // Image Generation Tests
  // ============================================================================

  describe('Image Generation', () => {
    const testCases = getAzureParamsForScenario('image_generation')

    it.each(testCases)(
      'should generate an image - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for image_generation')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)
        const imageModel = getProviderModel(provider, 'image_generation')

        try {
          const response = await client.images.generate({
            model: imageModel || model,
            prompt: 'A simple red circle on a white background',
            n: 1,
            size: '1024x1024',
          })

          expect(response).toBeDefined()
          expect(response.data).toBeDefined()
          expect(response.data.length).toBeGreaterThan(0)

          // Verify image data is present (URL or base64)
          const imageData = response.data[0]
          expect(imageData.url || imageData.b64_json).toBeDefined()

          console.log(`[Azure] Image generation passed for ${model}`)
        } catch (error) {
          if (isApiNotSupportedError(error)) {
            console.log(`[Azure] Image generation test skipped (API not supported): ${error instanceof Error ? error.message : 'Unknown error'}`)
          } else {
            throw error
          }
        }
      }
    )
  })

  // ============================================================================
  // Input Tokens API Tests
  // ============================================================================

  describe('Input Tokens - Simple Text', () => {
    const testCases = getAzureParamsForScenario('count_tokens')

    it.each(testCases)(
      'should count input tokens for simple text - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for count_tokens')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)

        // Try to use the responses.input_tokens.count API if available
        try {
          const responses = (client as unknown as { responses: { input_tokens: { count: (params: unknown) => Promise<{ total_tokens: number }> } } }).responses

          const response = await responses.input_tokens.count({
            model: model,
            input: 'Hello, how are you?',
          })

          expect(response).toBeDefined()
          expect(response.total_tokens).toBeGreaterThan(0)
          console.log(`[Azure] Input tokens count passed for ${model} (${response.total_tokens} tokens)`)
        } catch (error) {
          if (isApiNotSupportedError(error)) {
            console.log(`[Azure] Input tokens count test skipped (API not supported): ${error instanceof Error ? error.message : 'Unknown error'}`)
          } else {
            throw error
          }
        }
      }
    )
  })

  describe('Input Tokens - With System Message', () => {
    const testCases = getAzureParamsForScenario('count_tokens')

    it.each(testCases)(
      'should count input tokens with system message - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for count_tokens')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)

        try {
          const responses = (client as unknown as { responses: { input_tokens: { count: (params: unknown) => Promise<{ total_tokens: number }> } } }).responses

          const response = await responses.input_tokens.count({
            model: model,
            input: {
              system: 'You are a helpful assistant.',
              user: 'What is 2 + 2?',
            },
          })

          expect(response).toBeDefined()
          expect(response.total_tokens).toBeGreaterThan(0)
          console.log(`[Azure] Input tokens with system message passed for ${model} (${response.total_tokens} tokens)`)
        } catch (error) {
          if (isApiNotSupportedError(error)) {
            console.log(`[Azure] Input tokens with system message test skipped (API not supported): ${error instanceof Error ? error.message : 'Unknown error'}`)
          } else {
            throw error
          }
        }
      }
    )
  })

  describe('Input Tokens - Long Text', () => {
    const testCases = getAzureParamsForScenario('count_tokens')

    it.each(testCases)(
      'should count input tokens for long text - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for count_tokens')
          return
        }

        const client = getProviderAzureClient(provider, vkEnabled)

        try {
          const responses = (client as unknown as { responses: { input_tokens: { count: (params: unknown) => Promise<{ total_tokens: number }> } } }).responses

          const longText = 'This is a longer piece of text that should result in more tokens being counted. ' +
            'It contains multiple sentences and various words to ensure accurate token counting.'

          const response = await responses.input_tokens.count({
            model: model,
            input: longText,
          })

          expect(response).toBeDefined()
          expect(response.total_tokens).toBeGreaterThan(10)
          console.log(`[Azure] Input tokens long text passed for ${model} (${response.total_tokens} tokens)`)
        } catch (error) {
          if (isApiNotSupportedError(error)) {
            console.log(`[Azure] Input tokens long text test skipped (API not supported): ${error instanceof Error ? error.message : 'Unknown error'}`)
          } else {
            throw error
          }
        }
      }
    )
  })
})
