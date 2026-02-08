import { RoutingRuleConfig } from './pages/routing-rules.page'

// Counter to ensure unique priorities within a test run
let priorityCounter = 0

/**
 * Get a unique priority for each rule created in this test session
 * Priority must be between 0 and 1000 (inclusive)
 */
function getUniquePriority(): number {
  priorityCounter++
  // Use modulo to keep within valid range (0-1000)
  // Start at 100 to avoid conflicts with low-priority rules
  return (100 + (Date.now() % 800) + priorityCounter) % 1000
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
