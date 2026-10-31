/**
 * Feature flag ids the dashboard gates on. Each must match an id registered in
 * Go (transports/bifrost-http/lib/config.go, registerFeatureFlags).
 */
export const FEATURE_FLAGS = {
	/** Warp, the in-dashboard agent. On by default in OSS, off in enterprise. */
	warp: "warp",
} as const;