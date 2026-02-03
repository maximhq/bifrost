/**
 * Test data factories for logs tests
 */

/**
 * Sample log entry data for testing
 */
export interface SampleLogData {
  provider: string
  model: string
  status: 'success' | 'error' | 'pending'
  content?: string
}

/**
 * Create sample log search query
 */
export function createLogSearchQuery(overrides: Partial<{ query: string }> = {}): string {
  return overrides.query || `test-query-${Date.now()}`
}

/**
 * Sample providers for filtering
 */
export const SAMPLE_PROVIDERS = ['openai', 'anthropic', 'gemini'] as const

/**
 * Sample models for filtering
 */
export const SAMPLE_MODELS = ['gpt-4', 'claude-3-opus', 'gemini-pro'] as const

/**
 * Sample statuses for filtering
 */
export const SAMPLE_STATUSES = ['success', 'error', 'pending'] as const
