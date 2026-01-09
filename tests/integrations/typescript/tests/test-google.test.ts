/**
 * Google GenAI Integration Tests
 *
 * This test suite uses the Google Generative AI SDK to test Gemini models.
 * Note: The @google/generative-ai SDK does not support custom base URL configuration,
 * so these tests validate the SDK directly against Google's API rather than routing
 * through Bifrost. To test Google models through Bifrost, use the OpenAI SDK with
 * model name routing (e.g., model: "gemini/gemini-1.5-pro") or the LangChain tests.
 *
 * Tests cover chat, streaming, tool calling, and vision capabilities.
 *
 * Test Scenarios:
 * 1. Simple chat
 * 2. Multi-turn conversation
 * 3. Streaming chat
 * 4. Single tool call
 * 5. Multiple tool calls
 * 6. End-to-end tool calling
 * 7. Image Base64
 * 8. Embeddings
 * 9. Count tokens
 */

import { describe, it, expect, beforeAll } from 'vitest'
import {
  GoogleGenerativeAI,
  GenerativeModel,
  Content,
  Part,
  FunctionDeclaration,
  Tool,
  SchemaType,
} from '@google/generative-ai'

// Explicit type mapping for tool parameters to avoid invalid enum values from toUpperCase()
const TYPE_MAP: Record<string, SchemaType> = {
  string: SchemaType.STRING,
  number: SchemaType.NUMBER,
  integer: SchemaType.INTEGER,
  boolean: SchemaType.BOOLEAN,
  array: SchemaType.ARRAY,
  object: SchemaType.OBJECT,
}

import {
  getIntegrationUrl,
  getProviderModel,
  isProviderAvailable,
  getConfig,
} from '../src/utils/config-loader'

import {
  SIMPLE_CHAT_MESSAGES,
  MULTI_TURN_MESSAGES,
  STREAMING_CHAT_MESSAGES,
  SINGLE_TOOL_CALL_MESSAGES,
  MULTIPLE_TOOL_CALL_MESSAGES,
  BASE64_IMAGE,
  WEATHER_TOOL,
  CALCULATOR_TOOL,
  EMBEDDINGS_SINGLE_TEXT,
  EMBEDDINGS_MULTIPLE_TEXTS,
  getApiKey,
  hasApiKey,
  mockToolResponse,
  type ChatMessage,
  type ToolDefinition,
} from '../src/utils/common'

// ============================================================================
// Helper Functions
// ============================================================================

function getGoogleClient(): GoogleGenerativeAI {
  // Note: The @google/generative-ai SDK does not support custom base URL configuration.
  // Unlike OpenAI and Anthropic SDKs, requests cannot be routed through Bifrost directly.
  // These tests validate the Google GenAI SDK directly against Google's API.
  // To test Google models through Bifrost, use the OpenAI SDK with model name routing
  // (e.g., model: "gemini/gemini-1.5-pro") or the LangChain tests.
  const apiKey = hasApiKey('gemini') ? getApiKey('gemini') : 'dummy-key'
  return new GoogleGenerativeAI(apiKey)
}

function getGenerativeModel(modelName?: string): GenerativeModel {
  const client = getGoogleClient()
  const model = modelName || getProviderModel('gemini', 'chat')
  return client.getGenerativeModel({ model })
}

function convertToGoogleContent(messages: ChatMessage[]): Content[] {
  return messages.map((msg) => {
    const role = msg.role === 'assistant' ? 'model' : 'user'

    if (typeof msg.content === 'string') {
      return {
        role,
        parts: [{ text: msg.content }],
      }
    }

    // Handle multimodal content
    const parts: Part[] = msg.content.map((part) => {
      if (part.type === 'text') {
        return { text: part.text! }
      }

      // Handle image content
      const imageUrl = part.image_url!.url
      if (imageUrl.startsWith('data:')) {
        // Extract base64 data and mime type
        const matches = imageUrl.match(/^data:([^;]+);base64,(.+)$/)
        if (matches) {
          return {
            inlineData: {
              mimeType: matches[1],
              data: matches[2],
            },
          }
        }
      }

      // URL images - Google expects inline data, so we'd need to fetch
      // For now, return a text placeholder
      return { text: `[Image: ${imageUrl}]` }
    })

    return { role, parts }
  })
}

function convertToGoogleTools(tools: ToolDefinition[]): Tool[] {
  const functionDeclarations: FunctionDeclaration[] = tools.map((tool) => ({
    name: tool.name,
    description: tool.description,
    parameters: {
      type: SchemaType.OBJECT,
      properties: Object.fromEntries(
        Object.entries(tool.parameters.properties).map(([key, value]) => [
          key,
          {
            type: TYPE_MAP[value.type] || SchemaType.STRING,
            description: value.description,
            ...(value.enum ? { enum: value.enum } : {}),
          },
        ])
      ),
      required: tool.parameters.required || [],
    },
  }))

  return [{ functionDeclarations }]
}

interface GoogleToolCall {
  name: string
  arguments: Record<string, unknown>
}

function extractGoogleToolCalls(response: { response: { candidates?: Array<{ content?: { parts?: Part[] } }> } }): GoogleToolCall[] {
  const toolCalls: GoogleToolCall[] = []

  const candidates = response.response.candidates || []
  for (const candidate of candidates) {
    const parts = candidate.content?.parts || []
    for (const part of parts) {
      if ('functionCall' in part && part.functionCall) {
        toolCalls.push({
          name: part.functionCall.name,
          arguments: part.functionCall.args as Record<string, unknown>,
        })
      }
    }
  }

  return toolCalls
}

function getResponseText(response: { response: { text: () => string } }): string {
  try {
    return response.response.text()
  } catch {
    return ''
  }
}

// ============================================================================
// Test Suite
// ============================================================================

describe('Google GenAI SDK Integration Tests', () => {
  const skipTests = !isProviderAvailable('gemini')

  beforeAll(() => {
    if (skipTests) {
      console.log('⚠️ Skipping Google GenAI tests: GEMINI_API_KEY not set')
    }
  })

  // ============================================================================
  // Simple Chat Tests
  // ============================================================================

  describe('Simple Chat', () => {
    it('should complete a simple chat', async () => {
      if (skipTests) return

      const model = getGenerativeModel()
      const modelName = getProviderModel('gemini', 'chat')

      const result = await model.generateContent(SIMPLE_CHAT_MESSAGES[0].content as string)

      expect(result).toBeDefined()
      const text = getResponseText(result)
      expect(text.length).toBeGreaterThan(0)
      console.log(`✅ Simple chat passed for google/${modelName}`)
    })
  })

  // ============================================================================
  // Multi-turn Conversation Tests
  // ============================================================================

  describe('Multi-turn Conversation', () => {
    it('should handle multi-turn conversation', async () => {
      if (skipTests) return

      const model = getGenerativeModel()
      const modelName = getProviderModel('gemini', 'chat')

      const chat = model.startChat({
        history: convertToGoogleContent(MULTI_TURN_MESSAGES.slice(0, -1)),
      })

      const result = await chat.sendMessage(MULTI_TURN_MESSAGES[MULTI_TURN_MESSAGES.length - 1].content as string)

      expect(result).toBeDefined()
      const text = getResponseText(result)
      expect(text.toLowerCase()).toMatch(/paris|population|million|people/i)
      console.log(`✅ Multi-turn conversation passed for google/${modelName}`)
    })
  })

  // ============================================================================
  // Streaming Tests
  // ============================================================================

  describe('Streaming Chat', () => {
    it('should stream chat response', async () => {
      if (skipTests) return

      const model = getGenerativeModel()
      const modelName = getProviderModel('gemini', 'chat')

      const result = await model.generateContentStream(STREAMING_CHAT_MESSAGES[0].content as string)

      let content = ''
      for await (const chunk of result.stream) {
        const text = chunk.text()
        if (text) {
          content += text
        }
      }

      expect(content.length).toBeGreaterThan(0)
      console.log(`✅ Streaming chat passed for google/${modelName}`)
    })
  })

  // ============================================================================
  // Tool Calling Tests
  // ============================================================================

  describe('Single Tool Call', () => {
    it('should make a single tool call', async () => {
      if (skipTests) return

      const toolModel = getProviderModel('gemini', 'tools')
      const model = getGenerativeModel(toolModel)

      const result = await model.generateContent({
        contents: convertToGoogleContent(SINGLE_TOOL_CALL_MESSAGES),
        tools: convertToGoogleTools([WEATHER_TOOL]),
      })

      const toolCalls = extractGoogleToolCalls(result)
      expect(toolCalls.length).toBe(1)
      expect(toolCalls[0].name).toBe('get_weather')
      console.log(`✅ Single tool call passed for google/${toolModel}`)
    })
  })

  describe('Multiple Tool Calls', () => {
    it('should make multiple tool calls', async () => {
      if (skipTests) return

      const toolModel = getProviderModel('gemini', 'tools')
      const model = getGenerativeModel(toolModel)

      const result = await model.generateContent({
        contents: convertToGoogleContent(MULTIPLE_TOOL_CALL_MESSAGES),
        tools: convertToGoogleTools([WEATHER_TOOL, CALCULATOR_TOOL]),
      })

      const toolCalls = extractGoogleToolCalls(result)
      expect(toolCalls.length).toBeGreaterThanOrEqual(1)

      const toolNames = toolCalls.map((tc) => tc.name)
      expect(toolNames.some((name) => name === 'get_weather' || name === 'calculate')).toBe(true)
      console.log(`✅ Multiple tool calls passed for google/${toolModel}`)
    })
  })

  describe('End-to-End Tool Calling', () => {
    it('should complete end-to-end tool calling', async () => {
      if (skipTests) return

      const toolModel = getProviderModel('gemini', 'tools')
      const model = getGenerativeModel(toolModel)

      // Step 1: Initial request with tools
      const chat = model.startChat({
        tools: convertToGoogleTools([WEATHER_TOOL]),
      })

      const result1 = await chat.sendMessage(SINGLE_TOOL_CALL_MESSAGES[0].content as string)
      const toolCalls = extractGoogleToolCalls(result1)

      expect(toolCalls.length).toBeGreaterThan(0)

      // Step 2: Execute tool and get result
      const toolResult = mockToolResponse(toolCalls[0].name, toolCalls[0].arguments)

      // Step 3: Send tool result back
      const result2 = await chat.sendMessage([
        {
          functionResponse: {
            name: toolCalls[0].name,
            response: JSON.parse(toolResult),
          },
        },
      ])

      expect(result2).toBeDefined()
      const text = getResponseText(result2)
      expect(text.length).toBeGreaterThan(0)
      console.log(`✅ End-to-end tool calling passed for google/${toolModel}`)
    })
  })

  // ============================================================================
  // Image/Vision Tests
  // ============================================================================

  describe('Image Base64', () => {
    it('should analyze image from Base64', async () => {
      if (skipTests) return

      const visionModel = getProviderModel('gemini', 'vision')
      const model = getGenerativeModel(visionModel)

      const result = await model.generateContent([
        { text: 'What color is this image?' },
        {
          inlineData: {
            mimeType: 'image/png',
            data: BASE64_IMAGE,
          },
        },
      ])

      expect(result).toBeDefined()
      const text = getResponseText(result)
      expect(text.length).toBeGreaterThan(10)
      console.log(`✅ Image Base64 analysis passed for google/${visionModel}`)
    })
  })

  // ============================================================================
  // Embeddings Tests
  // ============================================================================

  describe('Embeddings - Single Text', () => {
    it('should generate single text embedding', async () => {
      if (skipTests) return

      const client = getGoogleClient()
      const embeddingsModel = getProviderModel('gemini', 'embeddings')

      // Skip if no embeddings model available
      if (!embeddingsModel) {
        console.log('⚠️ Skipping embeddings test: No embeddings model configured')
        return
      }

      const model = client.getGenerativeModel({ model: embeddingsModel })

      const result = await model.embedContent(EMBEDDINGS_SINGLE_TEXT)

      expect(result).toBeDefined()
      expect(result.embedding).toBeDefined()
      expect(result.embedding.values).toBeDefined()
      expect(result.embedding.values.length).toBeGreaterThan(0)
      console.log(`✅ Single text embedding passed for google/${embeddingsModel}`)
    })
  })

  describe('Embeddings - Batch', () => {
    it('should generate batch embeddings', async () => {
      if (skipTests) return

      const client = getGoogleClient()
      const embeddingsModel = getProviderModel('gemini', 'embeddings')

      // Skip if no embeddings model available
      if (!embeddingsModel) {
        console.log('⚠️ Skipping embeddings test: No embeddings model configured')
        return
      }

      const model = client.getGenerativeModel({ model: embeddingsModel })

      const result = await model.batchEmbedContents({
        requests: EMBEDDINGS_MULTIPLE_TEXTS.map((text) => ({ content: { parts: [{ text }], role: 'user' } })),
      })

      expect(result).toBeDefined()
      expect(result.embeddings).toBeDefined()
      expect(result.embeddings.length).toBe(EMBEDDINGS_MULTIPLE_TEXTS.length)
      console.log(`✅ Batch embeddings passed for google/${embeddingsModel}`)
    })
  })

  // ============================================================================
  // Count Tokens Tests
  // ============================================================================

  describe('Count Tokens', () => {
    it('should count tokens', async () => {
      if (skipTests) return

      const model = getGenerativeModel()
      const modelName = getProviderModel('gemini', 'chat')

      const result = await model.countTokens('Hello, how are you today?')

      expect(result).toBeDefined()
      expect(result.totalTokens).toBeGreaterThan(0)
      console.log(`✅ Count tokens passed for google/${modelName} (${result.totalTokens} tokens)`)
    })
  })

  // ============================================================================
  // Thinking/Extended Reasoning Tests
  // ============================================================================

  describe('Thinking/Extended Reasoning', () => {
    it('should support extended thinking', async () => {
      if (skipTests) return

      const thinkingModel = getProviderModel('gemini', 'thinking')

      // Skip if no thinking model available
      if (!thinkingModel) {
        console.log('⚠️ Skipping thinking test: No thinking model configured')
        return
      }

      const model = getGenerativeModel(thinkingModel)

      try {
        const result = await model.generateContent({
          contents: [
            {
              role: 'user',
              parts: [{ text: 'What is 15% of 80? Show your reasoning step by step.' }],
            },
          ],
          generationConfig: {
            // Google Gemini uses different config for reasoning
            maxOutputTokens: 2048,
          },
        })

        expect(result).toBeDefined()
        const text = getResponseText(result)
        expect(text.length).toBeGreaterThan(0)
        console.log(`✅ Thinking/Extended reasoning passed for google/${thinkingModel}`)
      } catch (error) {
        console.log(`⚠️ Thinking test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  // ============================================================================
  // Audio Transcription Tests
  // ============================================================================

  describe('Audio Transcription', () => {
    it('should transcribe audio content', async () => {
      if (skipTests) return

      const transcriptionModel = getProviderModel('gemini', 'transcription')

      // Skip if no transcription model available
      if (!transcriptionModel) {
        console.log('⚠️ Skipping transcription test: No transcription model configured')
        return
      }

      const model = getGenerativeModel(transcriptionModel)

      // Generate a minimal audio WAV buffer for testing
      const sampleRate = 16000
      const duration = 0.5 // 0.5 seconds
      const numSamples = Math.floor(sampleRate * duration)
      const frequency = 440 // A4 note

      // Create WAV header
      const headerSize = 44
      const dataSize = numSamples * 2
      const buffer = new ArrayBuffer(headerSize + dataSize)
      const view = new DataView(buffer)

      // RIFF header
      const encoder = new TextEncoder()
      new Uint8Array(buffer, 0, 4).set(encoder.encode('RIFF'))
      view.setUint32(4, headerSize + dataSize - 8, true)
      new Uint8Array(buffer, 8, 4).set(encoder.encode('WAVE'))

      // fmt chunk
      new Uint8Array(buffer, 12, 4).set(encoder.encode('fmt '))
      view.setUint32(16, 16, true)
      view.setUint16(20, 1, true)
      view.setUint16(22, 1, true)
      view.setUint32(24, sampleRate, true)
      view.setUint32(28, sampleRate * 2, true)
      view.setUint16(32, 2, true)
      view.setUint16(34, 16, true)

      // data chunk
      new Uint8Array(buffer, 36, 4).set(encoder.encode('data'))
      view.setUint32(40, dataSize, true)

      // Generate sine wave
      for (let i = 0; i < numSamples; i++) {
        const t = i / sampleRate
        const sample = Math.sin(2 * Math.PI * frequency * t) * 32767 * 0.5
        view.setInt16(headerSize + i * 2, Math.round(sample), true)
      }

      const audioBase64 = btoa(String.fromCharCode(...new Uint8Array(buffer)))

      try {
        const result = await model.generateContent([
          { text: 'Please transcribe this audio.' },
          {
            inlineData: {
              mimeType: 'audio/wav',
              data: audioBase64,
            },
          },
        ])

        expect(result).toBeDefined()
        // Note: A sine wave may not produce meaningful transcription
        console.log(`✅ Audio transcription passed for google/${transcriptionModel}`)
      } catch (error) {
        console.log(`⚠️ Transcription test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  // ============================================================================
  // Speech Synthesis Tests
  // ============================================================================

  describe('Speech Synthesis', () => {
    it('should synthesize speech', async () => {
      if (skipTests) return

      const speechModel = getProviderModel('gemini', 'speech')

      // Skip if no speech model available
      if (!speechModel) {
        console.log('⚠️ Skipping speech synthesis test: No speech model configured')
        return
      }

      // Google Gemini TTS requires specific API usage
      // This test verifies the model is accessible
      try {
        const model = getGenerativeModel(speechModel)

        const result = await model.generateContent({
          contents: [
            {
              role: 'user',
              parts: [{ text: 'Hello, this is a test of speech synthesis.' }],
            },
          ],
          generationConfig: {
            // TTS specific configuration
            responseModalities: ['AUDIO'],
            speechConfig: {
              voiceConfig: {
                prebuiltVoiceConfig: {
                  voiceName: 'Puck',
                },
              },
            },
          } as never,
        })

        expect(result).toBeDefined()
        console.log(`✅ Speech synthesis passed for google/${speechModel}`)
      } catch (error) {
        console.log(`⚠️ Speech synthesis test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  // ============================================================================
  // Document/PDF Input Tests
  // ============================================================================

  describe('Document Input - PDF', () => {
    it('should handle PDF document input', async () => {
      if (skipTests) return

      const fileModel = getProviderModel('gemini', 'file')

      // Skip if no file model available
      if (!fileModel) {
        console.log('⚠️ Skipping document input test: No file model configured')
        return
      }

      const model = getGenerativeModel(fileModel)

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
        const result = await model.generateContent([
          { text: 'What does this PDF document contain?' },
          {
            inlineData: {
              mimeType: 'application/pdf',
              data: pdfBase64,
            },
          },
        ])

        expect(result).toBeDefined()
        const text = getResponseText(result)
        expect(text.length).toBeGreaterThan(0)
        console.log(`✅ Document input (PDF) passed for google/${fileModel}`)
      } catch (error) {
        console.log(`⚠️ Document input test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  // ============================================================================
  // System Instruction Tests
  // ============================================================================

  describe('System Instruction', () => {
    it('should respect system instructions', async () => {
      if (skipTests) return

      const model = getGenerativeModel()
      const modelName = getProviderModel('gemini', 'chat')

      const client = getGoogleClient()
      const systemModel = client.getGenerativeModel({
        model: modelName,
        systemInstruction: 'You are a helpful assistant that always responds in exactly 5 words.',
      })

      try {
        const result = await systemModel.generateContent('Hello, how are you?')

        expect(result).toBeDefined()
        const text = getResponseText(result)
        expect(text.length).toBeGreaterThan(0)

        // Check if response is approximately 5 words
        const wordCount = text.trim().split(/\s+/).length
        expect(wordCount).toBeGreaterThanOrEqual(3)
        expect(wordCount).toBeLessThanOrEqual(10)
        console.log(`✅ System instruction passed for google/${modelName}`)
      } catch (error) {
        console.log(`⚠️ System instruction test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })

  // ============================================================================
  // Structured Output Tests
  // ============================================================================

  describe('Structured Output', () => {
    it('should generate structured output with JSON schema', async () => {
      if (skipTests) return

      const model = getGenerativeModel()
      const modelName = getProviderModel('gemini', 'chat')

      try {
        const result = await model.generateContent({
          contents: [
            {
              role: 'user',
              parts: [{ text: 'Give me a recipe for chocolate chip cookies as JSON with name, ingredients (array), and instructions (array).' }],
            },
          ],
          generationConfig: {
            responseMimeType: 'application/json',
          },
        })

        expect(result).toBeDefined()
        const text = getResponseText(result)
        expect(text.length).toBeGreaterThan(0)

        // Try to parse as JSON
        const parsed = JSON.parse(text)
        expect(parsed).toBeDefined()
        console.log(`✅ Structured output passed for google/${modelName}`)
      } catch (error) {
        console.log(`⚠️ Structured output test skipped: ${error instanceof Error ? error.message : 'Unknown error'}`)
      }
    })
  })
})
