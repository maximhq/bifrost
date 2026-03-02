import { ModelLimitConfig } from './pages/model-limits.page'

export function createModelLimitData(overrides: Partial<ModelLimitConfig> = {}): ModelLimitConfig {
  return {
    provider: 'openai',
    modelName: 'gpt-4o-mini',
    budget: { maxLimit: 10, resetDuration: '1M' },
    ...overrides,
  }
}
