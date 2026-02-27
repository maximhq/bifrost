import { ProviderKeyConfig, CustomProviderConfig } from '../../core/fixtures/test-data.fixture'

/**
 * Factory function to create provider key test data
 */
export function createProviderKeyData(overrides: Partial<ProviderKeyConfig> = {}): ProviderKeyConfig {
  const timestamp = Date.now()
  return {
    name: `Test Key ${timestamp}`,
    value: `sk-test-${timestamp}-${Math.random().toString(36).substring(7)}`,
    models: [],
    weight: 1.0,
    ...overrides,
  }
}

/**
 * Factory function to create custom provider test data
 */
export function createCustomProviderData(overrides: Partial<CustomProviderConfig> = {}): CustomProviderConfig {
  const timestamp = Date.now()
  return {
    name: `test-provider-${timestamp}`,
    baseProviderType: 'openai',
    baseUrl: 'https://api.example.com',
    isKeyless: false,
    ...overrides,
  }
}

/**
 * Known provider names for testing
 */
export const KNOWN_PROVIDERS = [
  'openai',
  'anthropic',
  'gemini',
  'cohere',
  'bedrock',
  'azure',
  'vertex',
  'groq',
  'mistral',
  'deepseek',
  'cerebras',
  'nebius',
  'sambanova',
] as const

/**
 * Sample API keys for testing (fake values)
 */
export const SAMPLE_API_KEYS = {
  openai: 'sk-test-openai-key-12345678901234567890',
  anthropic: 'sk-ant-test-key-12345678901234567890',
  gemini: 'test-gemini-api-key-1234567890',
}

/**
 * Sample models for each provider
 */
export const SAMPLE_MODELS = {
  openai: ['gpt-4', 'gpt-4-turbo', 'gpt-3.5-turbo'],
  anthropic: ['claude-3-opus', 'claude-3-sonnet', 'claude-3-haiku'],
  gemini: ['gemini-pro', 'gemini-pro-vision'],
}
