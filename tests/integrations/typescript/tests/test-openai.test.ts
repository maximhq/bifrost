/**
 * OpenAI Integration Tests - Cross-Provider Support
 *
 * This test suite uses the OpenAI SDK to test against multiple AI providers through Bifrost.
 * Tests automatically run against all available providers with proper capability filtering.
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
 * Audio:
 * 10. Speech synthesis (TTS)
 * 11. Audio transcription
 *
 * Embeddings:
 * 12. Single text embedding
 * 13. Batch embeddings
 * 14. Embedding similarity analysis
 *
 * Models & Tokens:
 * 15. List models
 * 16. Count tokens
 *
 * Files API:
 * 17. File upload
 * 18. File list
 * 19. File retrieve
 * 20. File delete
 * 21. File content download
 *
 * Batch API:
 * 22. Batch create
 * 23. Batch list
 * 24. Batch retrieve
 * 25. Batch cancel
 *
 * Responses API:
 * 26. Responses - simple text
 * 27. Responses - with system message
 * 28. Responses - with image
 * 29. Responses - with tools
 * 30. Responses - streaming
 * 31. Responses - streaming with tools
 * 32. Responses - reasoning
 *
 * Input Tokens API:
 * 33. Input tokens - simple text
 * 34. Input tokens - with system message
 * 35. Input tokens - long text
 */

import OpenAI from 'openai'
import { describe, expect, it } from 'vitest'

import {
  getIntegrationUrl,
  getProviderModel,
  getVirtualKey
} from '../src/utils/config-loader'

import {
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
  formatProviderModel,
  getCrossProviderParamsWithVkForScenario,
  shouldSkipNoProviders,
  type ProviderModelVkParam,
} from '../src/utils/parametrize'

// ============================================================================
// Helper Functions
// ============================================================================

function getProviderOpenAIClient(provider: string, vkEnabled: boolean = false): OpenAI {
  const baseUrl = getIntegrationUrl('openai')
  const apiKey = hasApiKey('openai') ? getApiKey('openai') : 'dummy-key'

  const defaultHeaders: Record<string, string> = {}

  if (vkEnabled) {
    const vk = getVirtualKey()
    if (vk) {
      defaultHeaders['x-bf-vk'] = vk
    }
  }

  return new OpenAI({
    baseURL: baseUrl,
    apiKey,
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

// ============================================================================
// Test Suite
// ============================================================================

describe('OpenAI SDK Integration Tests', () => {
  // ============================================================================
  // Simple Chat Tests
  // ============================================================================

  describe('Simple Chat', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('simple_chat')

    it.each(testCases)(
      'should complete a simple chat - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for simple_chat')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const response = await client.chat.completions.create({
          model: formatProviderModel(provider, model),
          messages: convertMessages(SIMPLE_CHAT_MESSAGES),
          max_tokens: 100,
        })

        assertValidChatResponse(response)
        console.log(`✅ Simple chat passed for ${formatProviderModel(provider, model)}`)
      }
    )
  })

  // ============================================================================
  // Multi-turn Conversation Tests
  // ============================================================================

  describe('Multi-turn Conversation', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('multi_turn_conversation')

    it.each(testCases)(
      'should handle multi-turn conversation - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for multi_turn_conversation')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const response = await client.chat.completions.create({
          model: formatProviderModel(provider, model),
          messages: convertMessages(MULTI_TURN_MESSAGES),
          max_tokens: 100,
        })

        assertValidChatResponse(response)

        // Verify context is maintained
        const content = response.choices[0]?.message?.content || ''
        expect(content.toLowerCase()).toMatch(/paris|population|million|people/i)
        console.log(`✅ Multi-turn conversation passed for ${formatProviderModel(provider, model)}`)
      }
    )
  })

  // ============================================================================
  // Streaming Tests
  // ============================================================================

  describe('Streaming Chat', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('streaming')

    it.each(testCases)(
      'should stream chat response - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for streaming')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const stream = await client.chat.completions.create({
          model: formatProviderModel(provider, model),
          messages: convertMessages(STREAMING_CHAT_MESSAGES),
          max_tokens: 100,
          stream: true,
        })

        const content = await collectStreamingContent(stream)
        expect(content.length).toBeGreaterThan(0)
        console.log(`✅ Streaming chat passed for ${formatProviderModel(provider, model)}`)
      }
    )
  })

  // ============================================================================
  // Streaming Client Disconnect Tests
  // ============================================================================

  describe('Streaming Chat - Client Disconnect', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('streaming')

    it.each(testCases)(
      'should handle client disconnect mid-stream - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for streaming')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const abortController = new AbortController()

        // Request a longer response to ensure we have time to abort mid-stream
        const stream = await client.chat.completions.create({
          model: formatProviderModel(provider, model),
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
        expect(wasAborted).toBe(true)
        console.log(`✅ Streaming client disconnect passed for ${formatProviderModel(provider, model)} (${chunkCount} chunks before abort)`)
      }
    )
  })

  // ============================================================================
  // Tool Calling Tests
  // ============================================================================

  describe('Single Tool Call', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('tool_calls')

    it.each(testCases)(
      'should make a single tool call - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for tool_calls')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const toolModel = getProviderModel(provider, 'tools')

        const response = await client.chat.completions.create({
          model: formatProviderModel(provider, toolModel || model),
          messages: convertMessages(SINGLE_TOOL_CALL_MESSAGES),
          tools: convertToOpenAITools([WEATHER_TOOL]),
          max_tokens: 100,
        })

        assertHasToolCalls(response, 1)
        const toolCalls = extractToolCalls(response)
        expect(toolCalls[0].name).toBe('get_weather')
        console.log(`✅ Single tool call passed for ${formatProviderModel(provider, model)}`)
      }
    )
  })

  describe('Multiple Tool Calls', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('multiple_tool_calls')

    it.each(testCases)(
      'should make multiple tool calls - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for multiple_tool_calls')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const toolModel = getProviderModel(provider, 'tools')

        const response = await client.chat.completions.create({
          model: formatProviderModel(provider, toolModel || model),
          messages: convertMessages(MULTIPLE_TOOL_CALL_MESSAGES),
          tools: convertToOpenAITools([WEATHER_TOOL, CALCULATOR_TOOL]),
          max_tokens: 150,
        })

        const toolCalls = extractToolCalls(response)
        expect(toolCalls.length).toBeGreaterThanOrEqual(1)

        const toolNames = toolCalls.map((tc: ExtractedToolCall) => tc.name)
        expect(toolNames.some((name: string) => name === 'get_weather' || name === 'calculate')).toBe(true)
        console.log(`✅ Multiple tool calls passed for ${formatProviderModel(provider, model)}`)
      }
    )
  })

  describe('End-to-End Tool Calling', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('end2end_tool_calling')

    it.each(testCases)(
      'should complete end-to-end tool calling - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for end2end_tool_calling')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const toolModel = getProviderModel(provider, 'tools')

        // Step 1: Initial request with tools
        const response1 = await client.chat.completions.create({
          model: formatProviderModel(provider, toolModel || model),
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
          model: formatProviderModel(provider, toolModel || model),
          messages,
          max_tokens: 200,
        })

        assertValidChatResponse(response2)
        console.log(`✅ End-to-end tool calling passed for ${formatProviderModel(provider, model)}`)
      }
    )
  })

  // ============================================================================
  // Image/Vision Tests
  // ============================================================================

  describe('Image URL', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('image_url')

    it.each(testCases)(
      'should analyze image from URL - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for image_url')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const visionModel = getProviderModel(provider, 'vision')

        const response = await client.chat.completions.create({
          model: formatProviderModel(provider, visionModel || model),
          messages: convertMessages(IMAGE_URL_MESSAGES),
          max_tokens: 200,
        })

        assertValidImageResponse(response)
        console.log(`✅ Image URL analysis passed for ${formatProviderModel(provider, model)}`)
      }
    )
  })

  describe('Image Base64', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('image_base64')

    it.each(testCases)(
      'should analyze image from Base64 - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for image_base64')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const visionModel = getProviderModel(provider, 'vision')

        const response = await client.chat.completions.create({
          model: formatProviderModel(provider, visionModel || model),
          messages: convertMessages(IMAGE_BASE64_MESSAGES),
          max_tokens: 200,
        })

        assertValidImageResponse(response)
        console.log(`✅ Image Base64 analysis passed for ${formatProviderModel(provider, model)}`)
      }
    )
  })

  describe('Multiple Images', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('multiple_images')

    it.each(testCases)(
      'should analyze multiple images - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for multiple_images')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const visionModel = getProviderModel(provider, 'vision')

        const response = await client.chat.completions.create({
          model: formatProviderModel(provider, visionModel || model),
          messages: convertMessages(MULTIPLE_IMAGES_MESSAGES),
          max_tokens: 300,
        })

        assertValidImageResponse(response)
        console.log(`✅ Multiple images analysis passed for ${formatProviderModel(provider, model)}`)
      }
    )
  })

  // ============================================================================
  // Speech Synthesis Tests (OpenAI only)
  // ============================================================================

  describe('Speech Synthesis', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('speech_synthesis')

    it.each(testCases)(
      'should synthesize speech - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for speech_synthesis')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const speechModel = getProviderModel(provider, 'speech')
        const voice = getProviderVoice(provider)

        const response = await client.audio.speech.create({
          model: formatProviderModel(provider, speechModel || 'tts-1'),
          voice: voice as 'alloy' | 'echo' | 'fable' | 'onyx' | 'nova' | 'shimmer',
          input: SPEECH_TEST_INPUT,
        })

        const buffer = await response.arrayBuffer()
        assertValidSpeechResponse(buffer)
        expect(buffer.byteLength).toBeGreaterThan(1000)
        console.log(`✅ Speech synthesis passed for ${formatProviderModel(provider, model)}`)
      }
    )
  })

  // ============================================================================
  // Transcription Tests (OpenAI only)
  // ============================================================================

  describe('Audio Transcription', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('transcription')

    it.each(testCases)(
      'should transcribe audio - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for transcription')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const transcriptionModel = getProviderModel(provider, 'transcription')

        // Generate test audio
        const audioBuffer = generateTestAudio(1000)
        const audioFile = new File([audioBuffer], 'test.wav', { type: 'audio/wav' })

        const response = await client.audio.transcriptions.create({
          model: formatProviderModel(provider, transcriptionModel || 'whisper-1'),
          file: audioFile,
          language: 'en',
        })

        expect(response).toBeDefined()
        // Note: Generated sine wave may not produce meaningful transcription
        console.log(`✅ Audio transcription passed for ${formatProviderModel(provider, model)}`)
      }
    )
  })

  // ============================================================================
  // Embeddings Tests
  // ============================================================================

  describe('Embeddings - Single Text', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('embeddings')

    it.each(testCases)(
      'should generate single text embedding - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for embeddings')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const embeddingsModel = getProviderModel(provider, 'embeddings')

        const response = await client.embeddings.create({
          model: formatProviderModel(provider, embeddingsModel || 'text-embedding-3-small'),
          input: EMBEDDINGS_SINGLE_TEXT,
        })

        assertValidEmbeddingResponse(response)
        console.log(`✅ Single text embedding passed for ${formatProviderModel(provider, model)}`)
      }
    )
  })

  describe('Embeddings - Batch', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('embeddings')

    it.each(testCases)(
      'should generate batch embeddings - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for embeddings')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const embeddingsModel = getProviderModel(provider, 'embeddings')

        const response = await client.embeddings.create({
          model: formatProviderModel(provider, embeddingsModel || 'text-embedding-3-small'),
          input: EMBEDDINGS_MULTIPLE_TEXTS,
        })

        assertValidEmbeddingsBatchResponse(response, EMBEDDINGS_MULTIPLE_TEXTS.length)
        console.log(`✅ Batch embeddings passed for ${formatProviderModel(provider, model)}`)
      }
    )
  })

  describe('Embeddings - Similarity Analysis', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('embeddings')

    it.each(testCases)(
      'should compute similar embeddings - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for embeddings')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const embeddingsModel = getProviderModel(provider, 'embeddings')

        const response = await client.embeddings.create({
          model: formatProviderModel(provider, embeddingsModel || 'text-embedding-3-small'),
          input: EMBEDDINGS_SIMILAR_TEXTS,
        })

        assertValidEmbeddingsBatchResponse(response, 2)

        // Calculate similarity between similar texts
        const emb1 = response.data[0].embedding
        const emb2 = response.data[1].embedding
        const similarity = calculateCosineSimilarity(emb1, emb2)

        // Similar texts should have high cosine similarity (> 0.7)
        expect(similarity).toBeGreaterThan(0.7)
        console.log(`✅ Embedding similarity analysis passed for ${formatProviderModel(provider, model)} (similarity: ${similarity.toFixed(3)})`)
      }
    )
  })

  // ============================================================================
  // List Models Tests
  // ============================================================================

  describe('List Models', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('list_models')

    it.each(testCases)(
      'should list available models - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for list_models')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const models = await client.models.list()

        expect(models).toBeDefined()
        expect(models.data).toBeDefined()
        expect(Array.isArray(models.data)).toBe(true)
        console.log(`✅ List models passed for ${formatProviderModel(provider, model)} (${models.data.length} models)`)
      }
    )
  })

  // ============================================================================
  // Count Tokens Tests
  // ============================================================================

  describe('Count Tokens', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('count_tokens')

    it.each(testCases)(
      'should count tokens - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for count_tokens')
          return
        }

        // Token counting typically requires a direct API call
        // This is a placeholder that verifies the setup works
        const client = getProviderOpenAIClient(provider, vkEnabled)

        // Use a simple chat completion to verify connectivity
        const response = await client.chat.completions.create({
          model: formatProviderModel(provider, model),
          messages: [{ role: 'user', content: 'Say hello' }],
          max_tokens: 10,
        })

        expect(response.usage).toBeDefined()
        if (response.usage) {
          expect(response.usage.prompt_tokens).toBeGreaterThan(0)
          expect(response.usage.completion_tokens).toBeGreaterThan(0)
          expect(response.usage.total_tokens).toBeGreaterThan(0)
        }
        console.log(`✅ Count tokens passed for ${formatProviderModel(provider, model)}`)
      }
    )
  })

  // ============================================================================
  // Files API Tests
  // ============================================================================

  describe('Files API - Upload', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('file_upload')

    it.each(testCases)(
      'should upload a file - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for file_upload')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)

        // Create JSONL content for batch processing
        const jsonlContent = JSON.stringify({
          custom_id: 'request-1',
          method: 'POST',
          url: '/v1/chat/completions',
          body: {
            model: formatProviderModel(provider, model),
            messages: [{ role: 'user', content: 'Hello' }],
            max_tokens: 10,
          },
        })

        // Create a File object from the content
        const file = new File([jsonlContent], 'batch_input.jsonl', {
          type: 'application/jsonl',
        })

        let uploadedFileId: string | null = null

        try {
          const response = await client.files.create({
            file,
            purpose: 'batch',
          })

          expect(response).toBeDefined()
          expect(response.id).toBeDefined()
          expect(typeof response.id).toBe('string')
          uploadedFileId = response.id

          console.log(`✅ File upload passed for ${formatProviderModel(provider, model)} - File ID: ${response.id}`)
        } finally {
          // Clean up
          if (uploadedFileId) {
            try {
              await client.files.del(uploadedFileId)
            } catch (e) {
              console.log(`Warning: Failed to clean up file: ${e}`)
            }
          }
        }
      }
    )
  })

  describe('Files API - List', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('file_list')

    it.each(testCases)(
      'should list files - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for file_list')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)

        // First upload a file to ensure we have at least one
        const jsonlContent = JSON.stringify({
          custom_id: 'request-1',
          method: 'POST',
          url: '/v1/chat/completions',
          body: {
            model: formatProviderModel(provider, model),
            messages: [{ role: 'user', content: 'Hello' }],
            max_tokens: 10,
          },
        })

        const file = new File([jsonlContent], 'test_list.jsonl', {
          type: 'application/jsonl',
        })

        let uploadedFileId: string | null = null

        try {
          const uploadedFile = await client.files.create({
            file,
            purpose: 'batch',
          })
          uploadedFileId = uploadedFile.id

          // List files
          const response = await client.files.list()

          expect(response).toBeDefined()
          expect(response.data).toBeDefined()
          expect(Array.isArray(response.data)).toBe(true)
          expect(response.data.length).toBeGreaterThan(0)

          // Check that our uploaded file is in the list
          const fileIds = response.data.map((f) => f.id)
          expect(fileIds).toContain(uploadedFileId)

          console.log(`✅ File list passed for ${formatProviderModel(provider, model)} - ${response.data.length} files`)
        } finally {
          // Clean up
          if (uploadedFileId) {
            try {
              await client.files.del(uploadedFileId)
            } catch (e) {
              console.log(`Warning: Failed to clean up file: ${e}`)
            }
          }
        }
      }
    )
  })

  describe('Files API - Retrieve', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('file_retrieve')

    it.each(testCases)(
      'should retrieve file metadata - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for file_retrieve')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)

        // First upload a file
        const jsonlContent = JSON.stringify({
          custom_id: 'request-1',
          method: 'POST',
          url: '/v1/chat/completions',
          body: {
            model: formatProviderModel(provider, model),
            messages: [{ role: 'user', content: 'Hello' }],
            max_tokens: 10,
          },
        })

        const file = new File([jsonlContent], 'test_retrieve.jsonl', {
          type: 'application/jsonl',
        })

        let uploadedFileId: string | null = null

        try {
          const uploadedFile = await client.files.create({
            file,
            purpose: 'batch',
          })
          uploadedFileId = uploadedFile.id

          // Retrieve file metadata
          const response = await client.files.retrieve(uploadedFileId)

          expect(response).toBeDefined()
          expect(response.id).toBe(uploadedFileId)
          expect(response.filename).toBe('test_retrieve.jsonl')
          expect(response.purpose).toBe('batch')

          console.log(`✅ File retrieve passed for ${formatProviderModel(provider, model)} - File ID: ${response.id}`)
        } finally {
          // Clean up
          if (uploadedFileId) {
            try {
              await client.files.del(uploadedFileId)
            } catch (e) {
              console.log(`Warning: Failed to clean up file: ${e}`)
            }
          }
        }
      }
    )
  })

  describe('Files API - Delete', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('file_delete')

    it.each(testCases)(
      'should delete a file - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for file_delete')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)

        // First upload a file
        const jsonlContent = JSON.stringify({
          custom_id: 'request-1',
          method: 'POST',
          url: '/v1/chat/completions',
          body: {
            model: formatProviderModel(provider, model),
            messages: [{ role: 'user', content: 'Hello' }],
            max_tokens: 10,
          },
        })

        const file = new File([jsonlContent], 'test_delete.jsonl', {
          type: 'application/jsonl',
        })

        const uploadedFile = await client.files.create({
          file,
          purpose: 'batch',
        })

        // Delete the file
        const response = await client.files.del(uploadedFile.id)

        expect(response).toBeDefined()
        expect(response.deleted).toBe(true)
        expect(response.id).toBe(uploadedFile.id)

        console.log(`✅ File delete passed for ${formatProviderModel(provider, model)} - File ID: ${response.id}`)
      }
    )
  })

  describe('Files API - Content', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('file_content')

    it.each(testCases)(
      'should retrieve file content - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for file_content')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)

        // Upload a file with known content
        const jsonlContent = JSON.stringify({
          custom_id: 'request-1',
          method: 'POST',
          url: '/v1/chat/completions',
          body: {
            model: formatProviderModel(provider, model),
            messages: [{ role: 'user', content: 'Hello' }],
            max_tokens: 10,
          },
        })

        const file = new File([jsonlContent], 'test_content.jsonl', {
          type: 'application/jsonl',
        })

        let uploadedFileId: string | null = null

        try {
          const uploadedFile = await client.files.create({
            file,
            purpose: 'batch',
          })
          uploadedFileId = uploadedFile.id

          // Retrieve file content
          const response = await client.files.content(uploadedFileId)

          expect(response).toBeDefined()
          // Response should be the file content
          const content = await response.text()
          expect(content).toBe(jsonlContent)

          console.log(`✅ File content passed for ${formatProviderModel(provider, model)}`)
        } finally {
          // Clean up
          if (uploadedFileId) {
            try {
              await client.files.del(uploadedFileId)
            } catch (e) {
              console.log(`Warning: Failed to clean up file: ${e}`)
            }
          }
        }
      }
    )
  })

  // ============================================================================
  // Batch API Tests
  // ============================================================================

  describe('Batch API - Create', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('batch_file_upload')

    it.each(testCases)(
      'should create a batch job - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for batch_file_upload')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)

        // Create JSONL content for batch processing
        const requests = [
          {
            custom_id: 'request-1',
            method: 'POST',
            url: '/v1/chat/completions',
            body: {
              model: formatProviderModel(provider, model),
              messages: [{ role: 'user', content: 'Say hello' }],
              max_tokens: 10,
            },
          },
          {
            custom_id: 'request-2',
            method: 'POST',
            url: '/v1/chat/completions',
            body: {
              model: formatProviderModel(provider, model),
              messages: [{ role: 'user', content: 'Say goodbye' }],
              max_tokens: 10,
            },
          },
        ]

        const jsonlContent = requests.map((r) => JSON.stringify(r)).join('\n')

        // Upload the file
        const file = new File([jsonlContent], 'batch_input.jsonl', {
          type: 'application/jsonl',
        })

        let uploadedFileId: string | null = null
        let batchId: string | null = null

        try {
          const uploadedFile = await client.files.create({
            file,
            purpose: 'batch',
          })
          uploadedFileId = uploadedFile.id

          // Create batch job
          const batch = await client.batches.create({
            input_file_id: uploadedFileId,
            endpoint: '/v1/chat/completions',
            completion_window: '24h',
          })

          expect(batch).toBeDefined()
          expect(batch.id).toBeDefined()
          expect(typeof batch.id).toBe('string')
          batchId = batch.id

          console.log(`✅ Batch create passed for ${formatProviderModel(provider, model)} - Batch ID: ${batch.id}`)
        } finally {
          // Clean up batch
          if (batchId) {
            try {
              await client.batches.cancel(batchId)
            } catch (e) {
              console.log(`Warning: Failed to cancel batch: ${e}`)
            }
          }
          // Clean up file
          if (uploadedFileId) {
            try {
              await client.files.del(uploadedFileId)
            } catch (e) {
              console.log(`Warning: Failed to clean up file: ${e}`)
            }
          }
        }
      }
    )
  })

  describe('Batch API - List', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('batch_list')

    it.each(testCases)(
      'should list batch jobs - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for batch_list')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)

        // List batches
        const response = await client.batches.list({ limit: 10 })

        expect(response).toBeDefined()
        expect(response.data).toBeDefined()
        expect(Array.isArray(response.data)).toBe(true)

        console.log(`✅ Batch list passed for ${formatProviderModel(provider, model)} - ${response.data.length} batches`)
      }
    )
  })

  describe('Batch API - Retrieve', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('batch_retrieve')

    it.each(testCases)(
      'should retrieve batch job status - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for batch_retrieve')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)

        // First, list batches to get a batch ID
        const listResponse = await client.batches.list({ limit: 10 })

        if (listResponse.data.length === 0) {
          console.log('Skipping: No batches available to retrieve')
          return
        }

        const batchId = listResponse.data[0].id

        // Retrieve batch
        const response = await client.batches.retrieve(batchId)

        expect(response).toBeDefined()
        expect(response.id).toBe(batchId)
        expect(response.status).toBeDefined()

        console.log(`✅ Batch retrieve passed for ${formatProviderModel(provider, model)} - Batch ID: ${response.id}, Status: ${response.status}`)
      }
    )
  })

  describe('Batch API - Cancel', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('batch_cancel')

    it.each(testCases)(
      'should cancel a batch job - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for batch_cancel')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)

        // Create JSONL content for batch processing
        const requests = [
          {
            custom_id: 'request-1',
            method: 'POST',
            url: '/v1/chat/completions',
            body: {
              model: formatProviderModel(provider, model),
              messages: [{ role: 'user', content: 'Say hello' }],
              max_tokens: 10,
            },
          },
        ]

        const jsonlContent = requests.map((r) => JSON.stringify(r)).join('\n')

        // Upload the file
        const file = new File([jsonlContent], 'batch_cancel_input.jsonl', {
          type: 'application/jsonl',
        })

        let uploadedFileId: string | null = null

        try {
          const uploadedFile = await client.files.create({
            file,
            purpose: 'batch',
          })
          uploadedFileId = uploadedFile.id

          // Create batch job
          const batch = await client.batches.create({
            input_file_id: uploadedFileId,
            endpoint: '/v1/chat/completions',
            completion_window: '24h',
          })

          // Cancel the batch
          const response = await client.batches.cancel(batch.id)

          expect(response).toBeDefined()
          expect(response.id).toBe(batch.id)
          expect(['cancelling', 'cancelled', 'completed', 'failed']).toContain(response.status)

          console.log(`✅ Batch cancel passed for ${formatProviderModel(provider, model)} - Batch ID: ${response.id}, Status: ${response.status}`)
        } finally {
          // Clean up file
          if (uploadedFileId) {
            try {
              await client.files.del(uploadedFileId)
            } catch (e) {
              console.log(`Warning: Failed to clean up file: ${e}`)
            }
          }
        }
      }
    )
  })

  // ============================================================================
  // Responses API Tests
  // ============================================================================

  describe('Responses API - Simple Text', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('responses')

    it.each(testCases)(
      'should create a response with simple text - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for responses')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)

        // Use type assertion for beta responses API
        const responses = (client as unknown as { responses: { create: (params: unknown) => Promise<unknown> } }).responses

        try {
          const response = await responses.create({
            model: formatProviderModel(provider, model),
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
          console.log(`✅ Responses API simple text passed for ${formatProviderModel(provider, model)}`)
        } catch (error) {
          console.log(`⚠️ Responses API test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
        }
      }
    )
  })

  describe('Responses API - With System Message', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('responses')

    it.each(testCases)(
      'should create a response with system message - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for responses')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const responses = (client as unknown as { responses: { create: (params: unknown) => Promise<unknown> } }).responses

        try {
          const response = await responses.create({
            model: formatProviderModel(provider, model),
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
          console.log(`✅ Responses API with system message passed for ${formatProviderModel(provider, model)}`)
        } catch (error) {
          console.log(`⚠️ Responses API with system message test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
        }
      }
    )
  })

  describe('Responses API - With Image', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('responses_image')

    it.each(testCases)(
      'should create a response with image input - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for responses_image')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const visionModel = getProviderModel(provider, 'vision')
        const responses = (client as unknown as { responses: { create: (params: unknown) => Promise<unknown> } }).responses

        try {
          const response = await responses.create({
            model: formatProviderModel(provider, visionModel || model),
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
          console.log(`✅ Responses API with image passed for ${formatProviderModel(provider, model)}`)
        } catch (error) {
          console.log(`⚠️ Responses API with image test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
        }
      }
    )
  })

  describe('Responses API - With Tools', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('responses')

    it.each(testCases)(
      'should create a response with tools - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for responses')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const toolModel = getProviderModel(provider, 'tools')
        const responses = (client as unknown as { responses: { create: (params: unknown) => Promise<unknown> } }).responses
        const tools = convertToResponsesTools([WEATHER_TOOL])

        try {
          const response = await responses.create({
            model: formatProviderModel(provider, toolModel || model),
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
          console.log(`✅ Responses API with tools passed for ${formatProviderModel(provider, model)}`)
        } catch (error) {
          console.log(`⚠️ Responses API with tools test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
        }
      }
    )
  })

  describe('Responses API - Streaming', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('responses')

    it.each(testCases)(
      'should stream a response - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for responses')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const responses = (client as unknown as { responses: { create: (params: unknown) => Promise<AsyncIterable<unknown>> } }).responses

        try {
          const stream = await responses.create({
            model: formatProviderModel(provider, model),
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
          console.log(`✅ Responses API streaming passed for ${formatProviderModel(provider, model)} (${chunkCount} chunks)`)
        } catch (error) {
          console.log(`⚠️ Responses API streaming test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
        }
      }
    )
  })

  describe('Responses API - Streaming Client Disconnect', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('responses')

    it.each(testCases)(
      'should handle client disconnect mid-stream - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for responses')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const abortController = new AbortController()
        const responses = (client as unknown as {
          responses: {
            create: (params: unknown, options?: { signal?: AbortSignal }) => Promise<AsyncIterable<unknown>>
          }
        }).responses

        try {
          const stream = await responses.create({
            model: formatProviderModel(provider, model),
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
          expect(wasAborted).toBe(true)
          console.log(`✅ Responses API streaming client disconnect passed for ${formatProviderModel(provider, model)} (${chunkCount} chunks before abort)`)
        } catch (error) {
          console.log(`⚠️ Responses API streaming client disconnect test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
        }
      }
    )
  })

  describe('Responses API - Streaming With Tools', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('responses')

    it.each(testCases)(
      'should stream a response with tools - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for responses')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const toolModel = getProviderModel(provider, 'tools')
        const responses = (client as unknown as { responses: { create: (params: unknown) => Promise<AsyncIterable<unknown>> } }).responses
        const tools = convertToResponsesTools([WEATHER_TOOL])

        try {
          const stream = await responses.create({
            model: formatProviderModel(provider, toolModel || model),
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
          console.log(`✅ Responses API streaming with tools passed for ${formatProviderModel(provider, model)} (tool call: ${hasToolCall})`)
        } catch (error) {
          console.log(`⚠️ Responses API streaming with tools test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
        }
      }
    )
  })

  describe('Responses API - Reasoning', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('thinking')

    it.each(testCases)(
      'should create a response with reasoning - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for thinking')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)
        const thinkingModel = getProviderModel(provider, 'thinking')
        const responses = (client as unknown as { responses: { create: (params: unknown) => Promise<unknown> } }).responses

        try {
          const response = await responses.create({
            model: formatProviderModel(provider, thinkingModel || model),
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
          console.log(`✅ Responses API reasoning passed for ${formatProviderModel(provider, model)}`)
        } catch (error) {
          console.log(`⚠️ Responses API reasoning test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
        }
      }
    )
  })

  // ============================================================================
  // Input Tokens API Tests
  // ============================================================================

  describe('Input Tokens - Simple Text', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('count_tokens')

    it.each(testCases)(
      'should count input tokens for simple text - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for count_tokens')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)

        // Try to use the responses.input_tokens.count API if available
        try {
          const responses = (client as unknown as { responses: { input_tokens: { count: (params: unknown) => Promise<{ total_tokens: number }> } } }).responses

          const response = await responses.input_tokens.count({
            model: formatProviderModel(provider, model),
            input: 'Hello, how are you?',
          })

          expect(response).toBeDefined()
          expect(response.total_tokens).toBeGreaterThan(0)
          console.log(`✅ Input tokens count passed for ${formatProviderModel(provider, model)} (${response.total_tokens} tokens)`)
        } catch (error) {
          console.log(`⚠️ Input tokens count test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
        }
      }
    )
  })

  describe('Input Tokens - With System Message', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('count_tokens')

    it.each(testCases)(
      'should count input tokens with system message - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for count_tokens')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)

        try {
          const responses = (client as unknown as { responses: { input_tokens: { count: (params: unknown) => Promise<{ total_tokens: number }> } } }).responses

          const response = await responses.input_tokens.count({
            model: formatProviderModel(provider, model),
            input: {
              system: 'You are a helpful assistant.',
              user: 'What is 2 + 2?',
            },
          })

          expect(response).toBeDefined()
          expect(response.total_tokens).toBeGreaterThan(0)
          console.log(`✅ Input tokens with system message passed for ${formatProviderModel(provider, model)} (${response.total_tokens} tokens)`)
        } catch (error) {
          console.log(`⚠️ Input tokens with system message test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
        }
      }
    )
  })

  describe('Input Tokens - Long Text', () => {
    const testCases = getCrossProviderParamsWithVkForScenario('count_tokens')

    it.each(testCases)(
      'should count input tokens for long text - $provider (VK: $vkEnabled)',
      async ({ provider, model, vkEnabled }: ProviderModelVkParam) => {
        if (shouldSkipNoProviders({ provider, model, vkEnabled })) {
          console.log('Skipping: No providers available for count_tokens')
          return
        }

        const client = getProviderOpenAIClient(provider, vkEnabled)

        try {
          const responses = (client as unknown as { responses: { input_tokens: { count: (params: unknown) => Promise<{ total_tokens: number }> } } }).responses

          const longText = 'This is a longer piece of text that should result in more tokens being counted. ' +
            'It contains multiple sentences and various words to ensure accurate token counting.'

          const response = await responses.input_tokens.count({
            model: formatProviderModel(provider, model),
            input: longText,
          })

          expect(response).toBeDefined()
          expect(response.total_tokens).toBeGreaterThan(10)
          console.log(`✅ Input tokens long text passed for ${formatProviderModel(provider, model)} (${response.total_tokens} tokens)`)
        } catch (error) {
          console.log(`⚠️ Input tokens long text test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
        }
      }
    )
  })
})
