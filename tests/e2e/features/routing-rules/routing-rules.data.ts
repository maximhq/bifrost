import { RoutingRuleConfig } from './pages/routing-rules.page'

// Counter to ensure unique priorities within a test run
let priorityCounter = 0

/**
 * Get a unique priority for each rule created in this test session
 * Priority must be between 0 and 1000 (inclusive)
 */
function getUniquePriority(): number {
  priorityCounter++
  // Per-process and time spread so parallel workers get different priorities (backend rejects duplicate).
  // Use high-resolution time to minimise collisions across test runs.
  const pid = typeof process !== 'undefined' && process.pid ? process.pid : 0
  const now = Date.now()
  return 1 + (pid * 7 + now % 100000 + priorityCounter * 131 + Math.floor(Math.random() * 500)) % 999
}

/**
 * Factory function to create routing rule test data
 * Note: CEL expression is auto-generated from the visual Rule Builder in the UI
 * An empty rule builder means the rule applies to all requests
 */
export function createRoutingRuleData(overrides: Partial<RoutingRuleConfig> = {}): RoutingRuleConfig {
  const timestamp = Date.now()
  return {
    name: `test-rule-${timestamp}`,
    description: 'Test routing rule',
    priority: overrides.priority ?? getUniquePriority(),
    enabled: true,
    ...overrides,
  }
}

// Note: CEL expressions are auto-generated from the visual Rule Builder in the UI
// The Rule Builder allows users to create conditions without writing CEL directly
