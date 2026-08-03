import { ModelLimitConfig } from './pages/model-limits.page'

export function createModelLimitData(overrides: Partial<ModelLimitConfig> = {}): ModelLimitConfig {
  return {
    provider: 'openai',
    modelName: 'gpt-4o-mini',
    budget: { maxLimit: 10, resetDuration: '1M' },
    rateLimits: [
      { metric: 'requests', maxLimit: 15, resetDuration: '1m' },
      { metric: 'requests', maxLimit: 1500, resetDuration: '1d' },
      { metric: 'tokens', maxLimit: 1000, resetDuration: '1m' },
      { metric: 'tokens', maxLimit: 10000, resetDuration: '1d' },
    ],
    ...overrides,
  }
}
