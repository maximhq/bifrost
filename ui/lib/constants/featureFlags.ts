/**
 * Feature flag ids the dashboard gates on. Each must match an id registered in
 * Go (transports/bifrost-http/lib/config.go, registerFeatureFlags).
 */
export const FEATURE_FLAGS = {
	/** Warp, the in-dashboard agent. Off by default. */
	warp: "warp",
} as const;