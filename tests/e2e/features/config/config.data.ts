/**
 * Test data factories for config settings tests
 */

/**
 * Config toggle state interface
 */
export interface ConfigToggleState {
  name: string
  enabled: boolean
}

/**
 * Client settings data factory
 */
export function createClientSettingsData(overrides: Partial<{
  dropExcessRequests: boolean
  enableLiteLLMFallbacks: boolean
  disableDBPings: boolean
}> = {}) {
  return {
    dropExcessRequests: false,
    enableLiteLLMFallbacks: true,
    disableDBPings: false,
    ...overrides
  }
}

/**
 * Logging settings data factory
 */
export function createLoggingSettingsData(overrides: Partial<{
  enableLogging: boolean
  disableContentLogging: boolean
  retentionDays: number
}> = {}) {
  return {
    enableLogging: true,
    disableContentLogging: false,
    retentionDays: 30,
    ...overrides
  }
}

/**
 * Performance tuning settings data factory
 */
export function createPerformanceTuningData(overrides: Partial<{
  workerPoolSize: number
  maxRequestBodySize: number
}> = {}) {
  return {
    workerPoolSize: 100,
    maxRequestBodySize: 10485760, // 10MB
    ...overrides
  }
}
