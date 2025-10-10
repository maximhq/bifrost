// Placeholder for enterprise reducers
// Export noop reducers when enterprise features are not available

export const scimReducer = (state = {}) => state
export const userReducer = (state = {}) => state
export const guardrailReducer = (state = {}) => state

// Empty reducers map when enterprise features are not available
export const reducers = {}

// Empty enterprise state type when enterprise features are not available
export type EnterpriseState = {}