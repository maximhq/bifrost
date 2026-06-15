// Placeholder for enterprise reducers
// Export noop reducers when enterprise features are not available
import { licenseApi } from "../apis/licenseApi";

export const scimReducer = (state = {}) => state;
export const userReducer = (state = {}) => state;
export const guardrailReducer = (state = {}) => state;

// licenseApi exists in OSS too (the endpoints just 404) — register its reducer
// so store.ts can wire licenseApi.middleware and LicenseGate can query status.
export const reducers = {
	[licenseApi.reducerPath]: licenseApi.reducer,
};

// Empty enterprise state type when enterprise features are not available
export type EnterpriseState = {};

export { licenseApi };