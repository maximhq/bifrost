export const LOG_LEVELS = ["debug", "info", "warn", "error"] as const;
export type LogLevel = (typeof LOG_LEVELS)[number];

const LOG_LEVEL_RANK: Record<LogLevel, number> = { debug: 0, info: 1, warn: 2, error: 3 };

export function isLogLevel(value: unknown): value is LogLevel {
	return typeof value === "string" && (LOG_LEVELS as readonly string[]).includes(value);
}

/**
 * Whether an entry at `level` is shown when the filter floor is `min`. The floor
 * keeps its own level and everything more severe, so `debug` shows every entry.
 * An entry with an unrecognised level is kept: hiding it would make a malformed
 * entry look like nothing was logged.
 */
export function meetsMinLogLevel(level: string | null | undefined, min: LogLevel): boolean {
	if (!isLogLevel(level)) return true;
	return LOG_LEVEL_RANK[level] >= LOG_LEVEL_RANK[min];
}

/** How many of `levels` each floor would show, keyed by that floor. Feeds the counts on the level tabs. */
export function countAtOrAboveEachLevel(levels: Iterable<string | null | undefined>): Record<LogLevel, number> {
	const counts: Record<LogLevel, number> = { debug: 0, info: 0, warn: 0, error: 0 };
	for (const level of levels) {
		for (const min of LOG_LEVELS) {
			if (meetsMinLogLevel(level, min)) counts[min] += 1;
		}
	}
	return counts;
}

/** Badge palette shared by every place a level is rendered next to a log line. */
export const LOG_LEVEL_BADGE_CLASSES: Record<LogLevel, string> = {
	debug: "bg-gray-100 text-gray-700 dark:bg-gray-800 dark:text-gray-300",
	info: "bg-blue-100 text-blue-700 dark:bg-blue-900 dark:text-blue-300",
	warn: "bg-amber-100 text-amber-700 dark:bg-amber-900 dark:text-amber-300",
	error: "bg-red-100 text-red-700 dark:bg-red-900 dark:text-red-300",
};