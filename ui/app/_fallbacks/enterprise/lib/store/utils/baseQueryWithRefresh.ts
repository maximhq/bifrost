// Fallback base query for non-enterprise builds
// Simply passes through the base query without any refresh logic

import type { BaseQueryFn } from '@reduxjs/toolkit/query/react'

/**
 * Fallback base query wrapper that does nothing
 * Used when enterprise features are not available
 */
export function createBaseQueryWithRefresh (
  baseQuery: BaseQueryFn
): BaseQueryFn {
  // Simply return the base query as-is (no refresh logic)
  return baseQuery
}

