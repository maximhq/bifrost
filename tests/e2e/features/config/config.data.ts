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

/**
 * A self-signed CA certificate (valid until 2126) for the proxy settings tests. Only its
 * PEM shape matters: the server stores it and nothing dials with it.
 */
export const PROXY_TEST_CA_PEM = [
  '-----BEGIN CERTIFICATE-----',
  'MIIBlTCCATugAwIBAgIUbGjUzQp5Cyax7LN2Pk5HXqmvxxowCgYIKoZIzj0EAwIw',
  'HzEdMBsGA1UEAwwUQmlmcm9zdCBlMmUgcHJveHkgQ0EwIBcNMjYwOTI1MjA1MTM1',
  'WhgPMjEyNjA5MDEyMDUxMzVaMB8xHTAbBgNVBAMMFEJpZnJvc3QgZTJlIHByb3h5',
  'IENBMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEoN2he3VoQpkehwQg0MFLl7sc',
  'WaEnbFmv1ps6To7X0hHPHTvELafUlj6sKR6kgw1CLZqRONt/MLK9rtHOjeXlfaNT',
  'MFEwHQYDVR0OBBYEFHXZc0tqe8vlKYEyWIcH52zLz3rMMB8GA1UdIwQYMBaAFHXZ',
  'c0tqe8vlKYEyWIcH52zLz3rMMA8GA1UdEwEB/wQFMAMBAf8wCgYIKoZIzj0EAwID',
  'SAAwRQIhAPBn1uidgntBnNBkd6VYRctRVQm615YEOnW7x8rRCA6+AiBoMQ264gOR',
  '5i5DCRm8z6miAjBfXCcHKdVUUaxGxQi0fA==',
  '-----END CERTIFICATE-----',
].join('\n') + '\n'
