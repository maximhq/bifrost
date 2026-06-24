import { CustomerConfig, TeamConfig } from './pages/governance.page'

export function createTeamData(overrides: Partial<TeamConfig> = {}): TeamConfig {
  const timestamp = Date.now()
  return {
    name: `E2E Team ${timestamp}`,
    budget: { maxLimit: 100, resetDuration: '1M' },
    ...overrides,
  }
}

export function createCustomerData(overrides: Partial<CustomerConfig> = {}): CustomerConfig {
  const timestamp = Date.now()
  return {
    name: `E2E Customer ${timestamp}`,
    budget: { maxLimit: 50, resetDuration: '1d' },
    ...overrides,
  }
}
