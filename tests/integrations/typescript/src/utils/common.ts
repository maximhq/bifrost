/**
 * Common utilities and test data for all integration tests.
 * This module contains shared functions, test data, and assertions
 * that can be used across all integration-specific test files.
 */

import { expect } from 'vitest'

// ============================================================================
// Test Configuration
// ============================================================================

export interface Config {
  timeout: number
  maxRetries: number
  debug: boolean
}

export const defaultConfig: Config = {
  timeout: 30,
  maxRetries: 3,
  debug: false,
}

// ============================================================================
// Image Test Data
// ============================================================================

export const IMAGE_URL =
  'https://pub-cdead89c2f004d8f963fd34010c479d0.r2.dev/Gfp-wisconsin-madison-the-nature-boardwalk.jpg'
export const IMAGE_URL_SECONDARY = 'https://goo.gle/instrument-img'

// Small test image as base64 (1x1 pixel red PNG)
export const BASE64_IMAGE =
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=='

// Base64 PDF test data
export const FILE_DATA_BASE64 =
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

// ============================================================================
// Common Test Messages
// ============================================================================

export interface ChatMessage {
  role: 'user' | 'assistant' | 'system'
  content: string | ContentPart[]
}

export interface ContentPart {
  type: 'text' | 'image_url'
  text?: string
  image_url?: {
    url: string
  }
}

export const SIMPLE_CHAT_MESSAGES: ChatMessage[] = [
  { role: 'user', content: 'Hello! How are you today?' },
]

export const MULTI_TURN_MESSAGES: ChatMessage[] = [
  { role: 'user', content: "What's the capital of France?" },
  { role: 'assistant', content: 'The capital of France is Paris.' },
  { role: 'user', content: "What's the population of that city?" },
]

export const STREAMING_CHAT_MESSAGES: ChatMessage[] = [
  { role: 'user', content: 'Count from 1 to 5, one number per line.' },
]

export const INVALID_ROLE_MESSAGES = [{ role: 'invalid_role', content: 'This should fail' }]

// ============================================================================
// Tool Definitions
// ============================================================================

export interface ToolDefinition {
  name: string
  description: string
  parameters: {
    type: 'object'
    properties: Record<string, { type: string; description: string; enum?: string[] }>
    required?: string[]
  }
}

export const WEATHER_TOOL: ToolDefinition = {
  name: 'get_weather',
  description: 'Get the current weather for a location',
  parameters: {
    type: 'object',
    properties: {
      location: {
        type: 'string',
        description: 'The city and state, e.g. San Francisco, CA',
      },
      unit: {
        type: 'string',
        enum: ['celsius', 'fahrenheit'],
        description: 'The temperature unit',
      },
    },
    required: ['location'],
  },
}

export const CALCULATOR_TOOL: ToolDefinition = {
  name: 'calculate',
  description: 'Perform basic mathematical calculations',
  parameters: {
    type: 'object',
    properties: {
      expression: {
        type: 'string',
        description: "Mathematical expression to evaluate, e.g. '2 + 2'",
      },
    },
    required: ['expression'],
  },
}

export const SEARCH_TOOL: ToolDefinition = {
  name: 'search_web',
  description: 'Search the web for information',
  parameters: {
    type: 'object',
    properties: {
      query: {
        type: 'string',
        description: 'Search query',
      },
    },
    required: ['query'],
  },
}

export const ALL_TOOLS: ToolDefinition[] = [WEATHER_TOOL, CALCULATOR_TOOL, SEARCH_TOOL]

// ============================================================================
// Tool Call Test Messages
// ============================================================================

export const SINGLE_TOOL_CALL_MESSAGES: ChatMessage[] = [
  { role: 'user', content: "What's the weather like in New York?" },
]

export const MULTIPLE_TOOL_CALL_MESSAGES: ChatMessage[] = [
  {
    role: 'user',
    content: "What's the weather in New York and also calculate 25 * 4?",
  },
]

export const STREAMING_TOOL_CALL_MESSAGES: ChatMessage[] = [
  { role: 'user', content: "What's the weather like in San Francisco?" },
]

// ============================================================================
// Image Test Messages
// ============================================================================

export const IMAGE_URL_MESSAGES: ChatMessage[] = [
  {
    role: 'user',
    content: [
      { type: 'text', text: 'What do you see in this image? Describe it briefly.' },
      { type: 'image_url', image_url: { url: IMAGE_URL } },
    ],
  },
]

export const IMAGE_BASE64_MESSAGES: ChatMessage[] = [
  {
    role: 'user',
    content: [
      { type: 'text', text: 'What color is this image?' },
      { type: 'image_url', image_url: { url: `data:image/png;base64,${BASE64_IMAGE}` } },
    ],
  },
]

export const MULTIPLE_IMAGES_MESSAGES: ChatMessage[] = [
  {
    role: 'user',
    content: [
      { type: 'text', text: 'Compare these two images. What do you see?' },
      { type: 'image_url', image_url: { url: IMAGE_URL } },
      { type: 'image_url', image_url: { url: `data:image/png;base64,${BASE64_IMAGE}` } },
    ],
  },
]

// ============================================================================
// Complex End-to-End Test Messages
// ============================================================================

export const COMPLEX_E2E_MESSAGES: ChatMessage[] = [
  { role: 'user', content: "What's the weather in Paris and calculate 100 / 5?" },
]

// ============================================================================
// Speech and Transcription Test Data
// ============================================================================

export const SPEECH_TEST_INPUT = 'Hello, this is a test of speech synthesis through Bifrost.'

export const SPEECH_VOICES = ['alloy', 'echo', 'fable', 'onyx', 'nova', 'shimmer'] as const
export type SpeechVoice = (typeof SPEECH_VOICES)[number]

export const AUDIO_FORMATS = ['mp3', 'opus', 'aac', 'flac', 'wav', 'pcm'] as const
export type AudioFormat = (typeof AUDIO_FORMATS)[number]

// ============================================================================
// Embeddings Test Data
// ============================================================================

export const EMBEDDINGS_SINGLE_TEXT = 'The quick brown fox jumps over the lazy dog.'

export const EMBEDDINGS_MULTIPLE_TEXTS = [
  'The quick brown fox jumps over the lazy dog.',
  'A fast auburn canine leaps above a sleepy hound.',
  'Machine learning is transforming technology.',
]

export const EMBEDDINGS_SIMILAR_TEXTS = [
  'The cat sat on the mat.',
  'A feline rested on the rug.',
]

export const EMBEDDINGS_DIFFERENT_TEXTS = [
  'The weather is sunny today.',
  'Quantum physics explores subatomic particles.',
]

export const EMBEDDINGS_LONG_TEXT =
  'This is a longer piece of text that contains multiple sentences. ' +
  'It is designed to test how embedding models handle longer inputs. ' +
  'The text continues with more content to ensure adequate length for testing purposes. ' +
  'We want to verify that the embedding generation works correctly with extended text.'

// ============================================================================
// Responses API Test Data
// ============================================================================

export const RESPONSES_SIMPLE_TEXT_INPUT = 'What is the capital of France?'

export const RESPONSES_TEXT_WITH_SYSTEM = {
  system: 'You are a helpful geography assistant.',
  user: 'What is the capital of France?',
}

export const RESPONSES_IMAGE_INPUT = {
  text: 'What do you see in this image?',
  imageUrl: IMAGE_URL,
}

export const RESPONSES_TOOL_CALL_INPUT = "What's the weather like in London?"

export const RESPONSES_STREAMING_INPUT = 'Count from 1 to 5.'

export const RESPONSES_REASONING_INPUT = 'Explain step by step how to solve: What is 15% of 80?'

// ============================================================================
// Text Completion Test Data
// ============================================================================

export const TEXT_COMPLETION_SIMPLE_PROMPT = 'Once upon a time, in a land far away,'

export const TEXT_COMPLETION_STREAMING_PROMPT = 'The quick brown fox'

// ============================================================================
// Input Tokens Test Data
// ============================================================================

export const INPUT_TOKENS_SIMPLE_TEXT = 'Hello, how are you?'

export const INPUT_TOKENS_LONG_TEXT =
  'This is a longer piece of text that should result in more tokens being counted. ' +
  'It contains multiple sentences and various words to ensure accurate token counting.'

export const INPUT_TOKENS_WITH_SYSTEM = {
  system: 'You are a helpful assistant.',
  user: 'What is 2 + 2?',
}

// ============================================================================
// Keyword Lists for Response Validation
// ============================================================================

export const WEATHER_KEYWORDS = [
  'weather',
  'temperature',
  'degrees',
  'sunny',
  'cloudy',
  'rain',
  'forecast',
  'warm',
  'cold',
  'humid',
]

export const LOCATION_KEYWORDS = ['new york', 'ny', 'nyc', 'city', 'manhattan']

export const COMPARISON_KEYWORDS = ['both', 'compare', 'similar', 'different', 'first', 'second']

// ============================================================================
// API Key Utilities
// ============================================================================

const API_KEY_MAP: Record<string, string> = {
  openai: 'OPENAI_API_KEY',
  anthropic: 'ANTHROPIC_API_KEY',
  google: 'GEMINI_API_KEY',
  gemini: 'GEMINI_API_KEY',
  litellm: 'LITELLM_API_KEY',
  bedrock: 'AWS_ACCESS_KEY_ID',
  cohere: 'COHERE_API_KEY',
  xai: 'XAI_API_KEY',
  azure: 'AZURE_API_KEY',
}

export function getApiKey(integration: string): string {
  const envVar = API_KEY_MAP[integration.toLowerCase()]
  if (!envVar) {
    throw new Error(`Unknown integration: ${integration}`)
  }

  const apiKey = process.env[envVar]
  if (!apiKey) {
    throw new Error(`${envVar} environment variable not set`)
  }

  return apiKey
}

export function hasApiKey(integration: string): boolean {
  try {
    getApiKey(integration)
    return true
  } catch {
    return false
  }
}

export function skipIfNoApiKey(integration: string): void {
  if (!hasApiKey(integration)) {
    const envVar = API_KEY_MAP[integration.toLowerCase()]
    throw new Error(`Skipping: ${envVar} not set`)
  }
}

// ============================================================================
// Response Content Extraction
// ============================================================================

export function getContentString(content: unknown): string {
  if (typeof content === 'string') {
    return content
  }

  if (Array.isArray(content)) {
    return content
      .map((item) => {
        if (typeof item === 'string') return item
        if (item?.text) return item.text
        if (item?.content) return item.content
        return ''
      })
      .join('')
  }

  if (content && typeof content === 'object') {
    const obj = content as Record<string, unknown>
    if (obj.text) return String(obj.text)
    if (obj.content) return getContentString(obj.content)
  }

  return ''
}

// ============================================================================
// Tool Call Extraction
// ============================================================================

export interface ExtractedToolCall {
  name: string
  arguments: Record<string, unknown>
}

export function extractToolCalls(response: unknown): ExtractedToolCall[] {
  const toolCalls: ExtractedToolCall[] = []

  // Handle OpenAI-style response
  if (response && typeof response === 'object') {
    const obj = response as Record<string, unknown>

    // OpenAI format: response.choices[0].message.tool_calls
    if (Array.isArray(obj.choices)) {
      const choice = obj.choices[0] as Record<string, unknown>
      const message = choice?.message as Record<string, unknown>
      const calls = message?.tool_calls as Array<Record<string, unknown>>

      if (Array.isArray(calls)) {
        for (const call of calls) {
          const fn = call.function as Record<string, unknown>
          if (fn?.name) {
            let args: Record<string, unknown> = {}
            try {
              args =
                typeof fn.arguments === 'string'
                  ? JSON.parse(fn.arguments)
                  : (fn.arguments as Record<string, unknown>) || {}
            } catch {
              // Keep empty args
            }
            toolCalls.push({
              name: String(fn.name),
              arguments: args,
            })
          }
        }
      }
    }

    // Direct tool_calls property
    if (Array.isArray(obj.tool_calls)) {
      for (const call of obj.tool_calls) {
        const callObj = call as Record<string, unknown>
        const fn = callObj.function as Record<string, unknown>
        if (fn?.name) {
          let args: Record<string, unknown> = {}
          try {
            args =
              typeof fn.arguments === 'string'
                ? JSON.parse(fn.arguments)
                : (fn.arguments as Record<string, unknown>) || {}
          } catch {
            // Keep empty args
          }
          toolCalls.push({
            name: String(fn.name),
            arguments: args,
          })
        }
      }
    }
  }

  return toolCalls
}

// ============================================================================
// Mock Tool Responses
// ============================================================================

export function mockToolResponse(toolName: string, args: Record<string, unknown>): string {
  switch (toolName) {
    case 'get_weather':
      return JSON.stringify({
        temperature: 72,
        condition: 'sunny',
        location: args.location || 'Unknown',
        unit: args.unit || 'fahrenheit',
      })
    case 'calculate':
      try {
        // Safe evaluation for simple math
        const expr = String(args.expression || '0')
        // Guardrails against pathological model output
        if (expr.length > 200) {
          return JSON.stringify({ error: 'Expression too long', expression: args.expression })
        }
        const sanitized = expr.replace(/[^0-9+\-*/().% ]/g, '')
        // Reject empty expressions after sanitization
        if (sanitized.trim() === '') {
          return JSON.stringify({ error: 'Empty expression', expression: args.expression })
        }
        const result = Function(`"use strict"; return (${sanitized})`)()
        return JSON.stringify({ result, expression: args.expression })
      } catch {
        return JSON.stringify({ error: 'Invalid expression', expression: args.expression })
      }
    case 'search_web':
      return JSON.stringify({
        results: [
          { title: 'Sample Result 1', url: 'https://example.com/1' },
          { title: 'Sample Result 2', url: 'https://example.com/2' },
        ],
        query: args.query,
      })
    default:
      return JSON.stringify({ status: 'ok', tool: toolName, args })
  }
}

// ============================================================================
// Assertion Helpers
// ============================================================================

export function assertValidChatResponse(response: unknown): void {
  expect(response).toBeDefined()
  expect(response).not.toBeNull()

  const obj = response as Record<string, unknown>

  // OpenAI-style response
  if (obj.choices) {
    expect(Array.isArray(obj.choices)).toBe(true)
    expect((obj.choices as unknown[]).length).toBeGreaterThan(0)

    const choice = (obj.choices as Array<Record<string, unknown>>)[0]
    expect(choice.message).toBeDefined()

    const message = choice.message as Record<string, unknown>
    const content = getContentString(message.content)

    // Allow empty content if there are tool calls
    if (!message.tool_calls) {
      expect(content.length).toBeGreaterThan(0)
    }
  }
  // Direct content response
  else if (obj.content !== undefined) {
    const content = getContentString(obj.content)
    if (!obj.tool_calls) {
      expect(content.length).toBeGreaterThan(0)
    }
  }
}

export function assertHasToolCalls(response: unknown, expectedCount?: number): void {
  const toolCalls = extractToolCalls(response)
  expect(toolCalls.length).toBeGreaterThan(0)

  if (expectedCount !== undefined) {
    expect(toolCalls.length).toBe(expectedCount)
  }
}

export function assertValidImageResponse(response: unknown): void {
  assertValidChatResponse(response)

  const obj = response as Record<string, unknown>
  let content = ''

  if (obj.choices) {
    const choice = (obj.choices as Array<Record<string, unknown>>)[0]
    const message = choice.message as Record<string, unknown>
    content = getContentString(message.content)
  } else if (obj.content !== undefined) {
    content = getContentString(obj.content)
  }

  // Image analysis responses should have meaningful content
  expect(content.length).toBeGreaterThan(10)
}

export function assertValidEmbeddingResponse(response: unknown): void {
  expect(response).toBeDefined()
  const obj = response as Record<string, unknown>

  // OpenAI-style embedding response
  if (obj.data) {
    expect(Array.isArray(obj.data)).toBe(true)
    expect((obj.data as unknown[]).length).toBeGreaterThan(0)

    const embedding = (obj.data as Array<Record<string, unknown>>)[0]
    expect(embedding.embedding).toBeDefined()
    expect(Array.isArray(embedding.embedding)).toBe(true)
    expect((embedding.embedding as number[]).length).toBeGreaterThan(0)
  }
  // Direct embedding array
  else if (obj.embedding) {
    expect(Array.isArray(obj.embedding)).toBe(true)
    expect((obj.embedding as number[]).length).toBeGreaterThan(0)
  }
}

export function assertValidEmbeddingsBatchResponse(response: unknown, expectedCount: number): void {
  expect(response).toBeDefined()
  const obj = response as Record<string, unknown>

  if (obj.data) {
    expect(Array.isArray(obj.data)).toBe(true)
    expect((obj.data as unknown[]).length).toBe(expectedCount)
  }
}

export function assertValidSpeechResponse(response: unknown): void {
  expect(response).toBeDefined()

  // Response should be audio data (ArrayBuffer or similar)
  if (response instanceof ArrayBuffer) {
    expect(response.byteLength).toBeGreaterThan(0)
  } else if (response instanceof Uint8Array) {
    expect(response.length).toBeGreaterThan(0)
  } else if (response && typeof response === 'object') {
    const obj = response as Record<string, unknown>
    // Check for content property (some SDKs wrap it)
    if (obj.content) {
      expect(obj.content).toBeDefined()
    }
  }
}

export function assertValidTranscriptionResponse(response: unknown): void {
  expect(response).toBeDefined()
  const obj = response as Record<string, unknown>

  // Should have transcribed text
  if (typeof obj.text === 'string') {
    expect(obj.text.length).toBeGreaterThan(0)
  } else if (typeof response === 'string') {
    expect(response.length).toBeGreaterThan(0)
  }
}

export function assertValidBatchResponse(response: unknown): void {
  expect(response).toBeDefined()
  const obj = response as Record<string, unknown>

  expect(obj.id).toBeDefined()
  expect(typeof obj.id).toBe('string')
}

export function assertValidBatchListResponse(response: unknown): void {
  expect(response).toBeDefined()
  const obj = response as Record<string, unknown>

  expect(obj.data).toBeDefined()
  expect(Array.isArray(obj.data)).toBe(true)
}

export function assertValidFileResponse(response: unknown): void {
  expect(response).toBeDefined()
  const obj = response as Record<string, unknown>

  expect(obj.id).toBeDefined()
  expect(typeof obj.id).toBe('string')
}

export function assertValidFileListResponse(response: unknown): void {
  expect(response).toBeDefined()
  const obj = response as Record<string, unknown>

  expect(obj.data).toBeDefined()
  expect(Array.isArray(obj.data)).toBe(true)
}

export function assertValidFileDeleteResponse(response: unknown): void {
  expect(response).toBeDefined()
  const obj = response as Record<string, unknown>

  expect(obj.deleted).toBe(true)
}

export function assertValidInputTokensResponse(response: unknown): void {
  expect(response).toBeDefined()
  const obj = response as Record<string, unknown>

  // Should have token count information
  if (typeof obj.total_tokens === 'number') {
    expect(obj.total_tokens).toBeGreaterThan(0)
  } else if (typeof obj.input_tokens === 'number') {
    expect(obj.input_tokens).toBeGreaterThan(0)
  }
}

export function assertValidResponsesResponse(response: unknown): void {
  expect(response).toBeDefined()
  const obj = response as Record<string, unknown>

  // Responses API should have output
  if (obj.output) {
    expect(obj.output).toBeDefined()
  } else if (obj.choices) {
    expect(Array.isArray(obj.choices)).toBe(true)
    expect((obj.choices as unknown[]).length).toBeGreaterThan(0)
  }
}

export function assertValidTextCompletionResponse(response: unknown): void {
  expect(response).toBeDefined()
  const obj = response as Record<string, unknown>

  if (obj.choices) {
    expect(Array.isArray(obj.choices)).toBe(true)
    const choice = (obj.choices as Array<Record<string, unknown>>)[0]
    expect(choice.text).toBeDefined()
  }
}

export function assertErrorPropagation(error: unknown): void {
  expect(error).toBeDefined()

  if (error instanceof Error) {
    expect(error.message).toBeDefined()
    expect(error.message.length).toBeGreaterThan(0)
  }
}

export function assertValidErrorResponse(error: unknown): void {
  assertErrorPropagation(error)
}

// ============================================================================
// Streaming Utilities
// ============================================================================

export async function collectStreamingContent(stream: AsyncIterable<unknown>): Promise<string> {
  let content = ''

  for await (const chunk of stream) {
    const chunkObj = chunk as Record<string, unknown>

    // OpenAI-style streaming
    if (chunkObj.choices) {
      const choice = (chunkObj.choices as Array<Record<string, unknown>>)[0]
      const delta = choice?.delta as Record<string, unknown>
      if (delta?.content) {
        content += String(delta.content)
      }
    }
    // Direct content delta
    else if (chunkObj.delta) {
      const delta = chunkObj.delta as Record<string, unknown>
      if (delta.content) {
        content += String(delta.content)
      }
    }
    // Text chunk
    else if (chunkObj.text) {
      content += String(chunkObj.text)
    }
  }

  return content
}

export async function collectStreamingTranscriptionContent(stream: AsyncIterable<unknown>): Promise<string> {
  let content = ''

  for await (const chunk of stream) {
    if (typeof chunk === 'string') {
      content += chunk
    } else {
      const chunkObj = chunk as Record<string, unknown>
      if (chunkObj.text) {
        content += String(chunkObj.text)
      }
    }
  }

  return content
}

export async function collectTextCompletionStreamingContent(stream: AsyncIterable<unknown>): Promise<string> {
  let content = ''

  for await (const chunk of stream) {
    const chunkObj = chunk as Record<string, unknown>

    if (chunkObj.choices) {
      const choice = (chunkObj.choices as Array<Record<string, unknown>>)[0]
      if (choice?.text) {
        content += String(choice.text)
      }
    }
  }

  return content
}

export async function collectResponsesStreamingContent(stream: AsyncIterable<unknown>): Promise<string> {
  let content = ''

  for await (const chunk of stream) {
    const chunkObj = chunk as Record<string, unknown>

    if (chunkObj.output) {
      content += String(chunkObj.output)
    } else if (chunkObj.delta) {
      const delta = chunkObj.delta as Record<string, unknown>
      if (delta.content) {
        content += String(delta.content)
      }
    }
  }

  return content
}

// ============================================================================
// Cosine Similarity for Embeddings
// ============================================================================

export function calculateCosineSimilarity(a: number[], b: number[]): number {
  if (a.length !== b.length) {
    throw new Error('Vectors must have the same length')
  }

  let dotProduct = 0
  let normA = 0
  let normB = 0

  for (let i = 0; i < a.length; i++) {
    dotProduct += a[i] * b[i]
    normA += a[i] * a[i]
    normB += b[i] * b[i]
  }

  // Handle zero vectors to avoid division by zero
  if (normA === 0 || normB === 0) {
    return 0
  }

  return dotProduct / (Math.sqrt(normA) * Math.sqrt(normB))
}

// ============================================================================
// Audio Generation for Testing
// ============================================================================

export function generateTestAudio(durationMs: number = 1000, sampleRate: number = 16000): Buffer {
  // Generate a simple sine wave audio for testing
  const numSamples = Math.floor((durationMs / 1000) * sampleRate)
  const frequency = 440 // A4 note

  // Create WAV header
  const headerSize = 44
  const dataSize = numSamples * 2 // 16-bit samples
  const fileSize = headerSize + dataSize - 8

  const buffer = Buffer.alloc(headerSize + dataSize)

  // RIFF header
  buffer.write('RIFF', 0)
  buffer.writeUInt32LE(fileSize, 4)
  buffer.write('WAVE', 8)

  // fmt chunk
  buffer.write('fmt ', 12)
  buffer.writeUInt32LE(16, 16) // chunk size
  buffer.writeUInt16LE(1, 20) // audio format (PCM)
  buffer.writeUInt16LE(1, 22) // num channels
  buffer.writeUInt32LE(sampleRate, 24) // sample rate
  buffer.writeUInt32LE(sampleRate * 2, 28) // byte rate
  buffer.writeUInt16LE(2, 32) // block align
  buffer.writeUInt16LE(16, 34) // bits per sample

  // data chunk
  buffer.write('data', 36)
  buffer.writeUInt32LE(dataSize, 40)

  // Generate sine wave samples
  for (let i = 0; i < numSamples; i++) {
    const t = i / sampleRate
    const sample = Math.sin(2 * Math.PI * frequency * t) * 32767 * 0.5
    buffer.writeInt16LE(Math.round(sample), headerSize + i * 2)
  }

  return buffer
}

// ============================================================================
// Provider Voice Mapping
// ============================================================================

const PROVIDER_VOICES: Record<string, string[]> = {
  openai: ['alloy', 'echo', 'fable', 'onyx', 'nova', 'shimmer'],
  google: ['Puck', 'Charon', 'Kore', 'Fenrir', 'Aoede'],
}

export function getProviderVoices(provider: string): string[] {
  return PROVIDER_VOICES[provider] || PROVIDER_VOICES.openai
}

export function getProviderVoice(provider: string, index: number = 0): string {
  const voices = getProviderVoices(provider)
  return voices[index % voices.length]
}

// ============================================================================
// OpenAI Tool Format Conversion
// ============================================================================

export interface OpenAITool {
  type: 'function'
  function: ToolDefinition
}

export function convertToOpenAITools(tools: ToolDefinition[]): OpenAITool[] {
  return tools.map((tool) => ({
    type: 'function',
    function: tool,
  }))
}

// ============================================================================
// Responses API Tool Format Conversion
// ============================================================================

export function convertToResponsesTools(tools: ToolDefinition[]): OpenAITool[] {
  return convertToOpenAITools(tools)
}

// ============================================================================
// Batch API Utilities
// ============================================================================

export interface BatchRequest {
  custom_id: string
  method: string
  url: string
  body: Record<string, unknown>
}

export function createBatchJsonlContent(requests: BatchRequest[]): string {
  return requests.map((r) => JSON.stringify(r)).join('\n')
}

export function createBatchInlineRequests(
  model: string,
  messages: ChatMessage[][],
  idPrefix: string = 'req'
): BatchRequest[] {
  return messages.map((msgs, index) => ({
    custom_id: `${idPrefix}-${index}`,
    method: 'POST',
    url: '/v1/chat/completions',
    body: {
      model,
      messages: msgs,
      max_tokens: 100,
    },
  }))
}

// ============================================================================
// Citations Test Data and Utilities
// ============================================================================

// Test document content for citations
export const CITATION_TEXT_DOCUMENT = `The Theory of Relativity was developed by Albert Einstein in the early 20th century.
It consists of two parts: Special Relativity published in 1905, and General Relativity published in 1915.

Special Relativity deals with objects moving at constant velocities and introduced the famous equation E=mc².
General Relativity extends this to accelerating objects and provides a new understanding of gravity.

Einstein's work revolutionized our understanding of space, time, and gravity, and its predictions have been
confirmed by numerous experiments and observations over the past century.`

// Multiple documents for testing document_index
export const CITATION_MULTI_DOCUMENT_SET = [
  {
    title: 'Physics Document',
    content: `Quantum mechanics is a fundamental theory in physics that describes the behavior of matter and energy at the atomic and subatomic level.
It was developed in the early 20th century by physicists including Max Planck, Albert Einstein, Niels Bohr, and Werner Heisenberg.`,
  },
  {
    title: 'Chemistry Document',
    content: `The periodic table organizes chemical elements by their atomic number, electron configuration, and recurring chemical properties.
It was first published by Dmitri Mendeleev in 1869 and has become a fundamental tool in chemistry.`,
  },
]

// Citation types
export interface CharLocationCitation {
  type: 'char_location'
  cited_text: string
  document_index: number
  document_title?: string
  start_char_index: number
  end_char_index: number
}

export interface PageLocationCitation {
  type: 'page_location'
  cited_text: string
  document_index: number
  document_title?: string
  start_page_number: number
  end_page_number: number
}

export interface WebSearchCitation {
  type: 'web_search_result_location'
  url: string
  title: string
  encrypted_index: string
  cited_text?: string
}

export type AnthropicCitation = CharLocationCitation | PageLocationCitation | WebSearchCitation

// Document block interface
export interface AnthropicDocument {
  type: 'document'
  title: string
  source: {
    type: 'text' | 'base64'
    media_type: string
    data: string
  }
  citations: {
    enabled: boolean
  }
}

/**
 * Create a properly formatted document block for Anthropic API with citations.
 */
export function createAnthropicDocument(
  content: string,
  docType: 'text' | 'pdf' | 'base64',
  title: string = 'Test Document',
  citationsEnabled: boolean = true
): AnthropicDocument {
  const document: AnthropicDocument = {
    type: 'document',
    title,
    source: {
      type: docType === 'text' ? 'text' : 'base64',
      media_type: docType === 'text' ? 'text/plain' : 'application/pdf',
      data: content,
    },
    citations: {
      enabled: citationsEnabled,
    },
  }

  return document
}

/**
 * Validate citation indices based on type.
 */
export function validateCitationIndices(
  citation: AnthropicCitation,
  citationType: 'char_location' | 'page_location' | 'web_search_result_location'
): void {
  if (citationType === 'char_location') {
    const charCitation = citation as CharLocationCitation
    expect(charCitation.start_char_index).toBeDefined()
    expect(charCitation.end_char_index).toBeDefined()
    expect(charCitation.start_char_index).toBeGreaterThanOrEqual(0)
    expect(charCitation.end_char_index).toBeGreaterThan(charCitation.start_char_index)
  } else if (citationType === 'page_location') {
    const pageCitation = citation as PageLocationCitation
    expect(pageCitation.start_page_number).toBeDefined()
    expect(pageCitation.end_page_number).toBeDefined()
    expect(pageCitation.start_page_number).toBeGreaterThanOrEqual(1)
    expect(pageCitation.end_page_number).toBeGreaterThan(pageCitation.start_page_number)
  } else if (citationType === 'web_search_result_location') {
    const webCitation = citation as WebSearchCitation
    expect(webCitation.url).toBeDefined()
    expect(webCitation.title).toBeDefined()
    expect(webCitation.encrypted_index).toBeDefined()
  }
}

/**
 * Assert that an Anthropic citation is valid and matches expected structure.
 */
export function assertValidAnthropicCitation(
  citation: AnthropicCitation,
  expectedType: 'char_location' | 'page_location' | 'web_search_result_location',
  documentIndex: number = 0
): void {
  // Check basic structure
  expect(citation.type).toBeDefined()
  expect(citation.type).toBe(expectedType)

  // Check required fields
  expect(citation.cited_text).toBeDefined()
  expect(typeof citation.cited_text).toBe('string')
  
  if (expectedType !== 'web_search_result_location') {
    expect(citation.cited_text?.length ?? 0).toBeGreaterThan(0)
    
    // Check document reference
    expect((citation as CharLocationCitation | PageLocationCitation).document_index).toBeDefined()
    expect((citation as CharLocationCitation | PageLocationCitation).document_index).toBe(documentIndex)
  }

  // Validate type-specific indices
  validateCitationIndices(citation, expectedType)
}

/**
 * Collect text content and citations from an Anthropic streaming response.
 */
export async function collectAnthropicStreamingCitations(
  stream: AsyncIterable<unknown>
): Promise<{ content: string; citations: AnthropicCitation[]; chunkCount: number }> {
  let content = ''
  const citations: AnthropicCitation[] = []
  let chunkCount = 0

  for await (const event of stream) {
    chunkCount++
    const eventObj = event as Record<string, unknown>

    if (eventObj.type === 'content_block_delta') {
      const delta = eventObj.delta as Record<string, unknown>
      
      if (delta.type === 'text_delta' && delta.text) {
        content += String(delta.text)
      } else if (delta.type === 'citations_delta' && delta.citation) {
        citations.push(delta.citation as AnthropicCitation)
      }
    }
  }

  return { content, citations, chunkCount }
}

/**
 * Validate web search citation structure.
 */
export function assertValidWebSearchCitation(
  citation: unknown,
  sdkType: 'anthropic' | 'openai' = 'anthropic'
): void {
  const citationObj = citation as Record<string, unknown>
  
  if (sdkType === 'anthropic') {
    expect(citationObj.type).toBeDefined()
    expect(citationObj.type).toBe('web_search_result_location')
    expect(citationObj.url).toBeDefined()
    expect(typeof citationObj.url).toBe('string')
    expect(citationObj.title).toBeDefined()
    expect(typeof citationObj.title).toBe('string')
    expect(citationObj.encrypted_index).toBeDefined()
    
    if (citationObj.cited_text) {
      expect(typeof citationObj.cited_text).toBe('string')
      expect((citationObj.cited_text as string).length).toBeLessThanOrEqual(150)
    }
  } else {
    // OpenAI format (url_citation)
    expect(citationObj.type).toBeDefined()
    expect(citationObj.type).toBe('url_citation')
    expect(citationObj.url).toBeDefined()
    expect(typeof citationObj.url).toBe('string')
  }
}


/**
 * Assert that an OpenAI annotation is valid and matches expected structure.
 */
export function assertValidOpenAIAnnotation(
  annotation: unknown,
  expectedType: 'file_citation' | 'url_citation' | 'container_file_citation' | 'file_path' | 'char_location' = 'url_citation'
): void {
  const annotationObj = annotation as Record<string, unknown>
  
  expect(annotationObj.type).toBeDefined()
  expect(annotationObj.type).toBe(expectedType)

  // Validate based on type
  if (expectedType === 'file_citation') {
    if (annotationObj.file_id) {
      expect(typeof annotationObj.file_id).toBe('string')
    }
    if (annotationObj.filename) {
      expect(typeof annotationObj.filename).toBe('string')
    }
    if (annotationObj.index !== undefined) {
      expect(typeof annotationObj.index).toBe('number')
      expect(annotationObj.index as number).toBeGreaterThanOrEqual(0)
    }
  } else if (expectedType === 'url_citation') {
    if (annotationObj.url) {
      expect(typeof annotationObj.url).toBe('string')
    }
    if (annotationObj.title) {
      expect(typeof annotationObj.title).toBe('string')
    }
    if (annotationObj.start_index !== undefined && annotationObj.end_index !== undefined) {
      expect(typeof annotationObj.start_index).toBe('number')
      expect(typeof annotationObj.end_index).toBe('number')
      expect(annotationObj.end_index as number).toBeGreaterThan(annotationObj.start_index as number)
    }
  } else if (expectedType === 'container_file_citation') {
    if (annotationObj.container_id) {
      expect(typeof annotationObj.container_id).toBe('string')
    }
    if (annotationObj.file_id) {
      expect(typeof annotationObj.file_id).toBe('string')
    }
    if (annotationObj.filename) {
      expect(typeof annotationObj.filename).toBe('string')
    }
  } else if (expectedType === 'file_path') {
    if (annotationObj.file_id) {
      expect(typeof annotationObj.file_id).toBe('string')
    }
    if (annotationObj.index !== undefined) {
      expect(typeof annotationObj.index).toBe('number')
      expect(annotationObj.index as number).toBeGreaterThanOrEqual(0)
    }
  } else if (expectedType === 'char_location') {
    if (annotationObj.start_char_index !== undefined) {
      expect(typeof annotationObj.start_char_index).toBe('number')
    }
    if (annotationObj.end_char_index !== undefined) {
      expect(typeof annotationObj.end_char_index).toBe('number')
    }
  }
}
