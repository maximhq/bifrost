// Bulk rotation returns one previous_value_expires_at per key, each computed from
// its own time.Now() on the server, so the deadlines in one response can differ.

/**
 * Picks the single grace deadline to show for a bulk rotation: the latest
 * deadline across the returned keys, i.e. the time after which no retired
 * value authenticates anymore. Returns null when no key has a grace window.
 */
export function latestGraceDeadline(virtualKeys: { previous_value_expires_at?: string | null }[]): string | null {
	let latest: string | null = null;
	for (const vk of virtualKeys) {
		const deadline = vk.previous_value_expires_at;
		if (deadline && (latest === null || new Date(deadline).getTime() > new Date(latest).getTime())) {
			latest = deadline;
		}
	}
	return latest;
}
