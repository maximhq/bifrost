/**
 * Parses a duration string (e.g., "1m", "5m", "1h", "1d", "1w", "1M") into human readable format
 */
export function parseResetPeriod(duration: string): string {
	if (!duration) return "Unknown";

	const timeValue = parseInt(duration.slice(0, -1));
	const timeUnit = duration.slice(-1);

	const unitMap: Record<string, { singular: string; plural: string }> = {
		s: { singular: "second", plural: "seconds" },
		m: { singular: "minute", plural: "minutes" },
		h: { singular: "hour", plural: "hours" },
		d: { singular: "day", plural: "days" },
		w: { singular: "week", plural: "weeks" },
		M: { singular: "month", plural: "months" },
		y: { singular: "year", plural: "years" },
	};

	const unit = unitMap[timeUnit];
	if (!unit) return duration;

	const unitName = timeValue === 1 ? unit.singular : unit.plural;
	return `${timeValue} ${unitName}`;
}

export function formatCurrency(dollars: number) {
	return `$${dollars.toFixed(2)}`;
}

/**
 * Formats a number compactly (e.g. 10000 → "10K", 1500000 → "1.5M").
 * Uses Intl.NumberFormat so boundary values promote correctly (999,950 → "1M", not "1000K")
 * and trailing zeros are dropped (10,000 → "10K", not "10.0K").
 */
const compactNumberFormatter = new Intl.NumberFormat(undefined, {
	notation: "compact",
	maximumFractionDigits: 1,
});

export function formatCompactNumber(n: number): string {
	if (Math.abs(n) >= 1_000) return compactNumberFormatter.format(n);
	return n.toLocaleString();
}

const shortDurationLabels: Record<string, string> = {
	"1m": "/min",
	"5m": "/5min",
	"15m": "/15min",
	"30m": "/30min",
	"1h": "/hr",
	"6h": "/6hr",
	"1d": "/day",
	"1w": "/wk",
	"1M": "/mo",
};

/**
 * Formats rate limit into compact display lines.
 * e.g. ["10K tokens/hr", "100 req/hr"]
 */
export function formatRateLimitLines(rateLimits: {
	token_max_limit?: number | null;
	token_reset_duration?: string | null;
	request_max_limit?: number | null;
	request_reset_duration?: string | null;
} | null | undefined): string[] {
	if (!rateLimits) return [];
	const lines: string[] = [];
	if (rateLimits.token_max_limit != null) {
		const duration = rateLimits.token_reset_duration ?? "";
		const suffix = shortDurationLabels[duration] ?? (duration ? `/${duration}` : "");
		lines.push(`${formatCompactNumber(rateLimits.token_max_limit)} tokens${suffix}`);
	}
	if (rateLimits.request_max_limit != null) {
		const duration = rateLimits.request_reset_duration ?? "";
		const suffix = shortDurationLabels[duration] ?? (duration ? `/${duration}` : "");
		lines.push(`${formatCompactNumber(rateLimits.request_max_limit)} req${suffix}`);
	}
	return lines;
}

/**
 * Calculates usage percentage for rate limits
 */
export function calculateUsagePercentage(current: number, max: number): number {
	if (max === 0) return 0;
	return Math.round((current / max) * 100);
}

/**
 * Gets the appropriate variant for usage percentage badges
 */
export function getUsageVariant(percentage: number): "default" | "secondary" | "destructive" | "outline" {
	if (percentage >= 90) return "destructive";
	if (percentage >= 75) return "secondary";
	return "default";
}