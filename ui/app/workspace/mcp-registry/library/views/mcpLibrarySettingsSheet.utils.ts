/**
 * toIntervalHours converts the stored MCP library sync interval (seconds) into
 * the hours the settings form shows.
 *
 * Two values have to survive the round trip untouched, because the form writes
 * `hours * 3600` back on every save - even a save that only changed the URL:
 *   - 0 means "background syncing disabled" and must not collapse into the 24h
 *     default the way a missing value does.
 *   - Non-whole-hour intervals are legal. transports/config.schema.json declares
 *     mcp_library_sync_interval as `anyOf: [{const: 0}, {minimum: 3600}]`, so a
 *     stored 5400 (1.5h) is valid config; rounding it to 2h would silently
 *     rewrite it to 7200 on the next save.
 */
export function toIntervalHours(seconds: number | undefined | null): number {
	if (seconds === undefined || seconds === null) return 24;
	return seconds / 3600;
}