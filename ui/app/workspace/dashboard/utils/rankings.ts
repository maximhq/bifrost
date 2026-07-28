/**
 * Number of ranked rows the dashboard requests per rankings tab. The backend
 * caps rankings at 100 by default and accepts a `limit` query param, so this is
 * the single place to change how many rows the dashboard shows.
 *
 * Exports (CSV / PDF) ignore this and request `all=true` instead, so a report
 * always contains every ranked entity.
 */
export const DASHBOARD_RANKINGS_LIMIT = 100;