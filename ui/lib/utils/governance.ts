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

/**
 * Formats a USD amount with adaptive precision so small values don't collapse to $0.00.
 *
 * Rules:
 *   - NaN / non-finite → "$0.00"
 *   - exactly 0       → "$0.00"
 *   - |amount| ≥ 1    → 2 fixed decimals (e.g. $1.00, $123.45)
 *   - |amount| < 1    → up to 6 decimals, trailing zeros trimmed, but always ≥ 2 decimals
 *                       (e.g. 0.001 → $0.001, 0.00025 → $0.00025, 0.5 → $0.50)
 *   - |amount| smaller than 1e-6 still renders the truncated value (does NOT become $0.00 when nonzero
 *     — falls back to "<$0.000001" style if truncation would otherwise zero it out).
 */
export function formatCurrency(dollars: number) {
	if (!Number.isFinite(dollars)) return "$0.00";
	if (dollars === 0) return "$0.00";
	const abs = Math.abs(dollars);
	const sign = dollars < 0 ? "-" : "";
	if (abs >= 1) return `${sign}$${abs.toFixed(2)}`;
	// Sub-dollar: render with up to 6 decimals, then trim trailing zeros while keeping at least 2 decimals.
	let s = abs.toFixed(6); // "0.001000"
	if (s.includes(".")) {
		s = s.replace(/0+$/, "").replace(/\.$/, ""); // "0.001"
		const decimals = s.split(".")[1]?.length ?? 0;
		if (decimals < 2) s = `${s}${"0".repeat(2 - decimals)}`; // ensure min 2 decimals (e.g. "0.5" → "0.50")
	}
	// Guard: if rounding to 6 decimals zeroed out a tiny non-zero amount, surface it explicitly.
	if (s === "0" || s === "0.00") return `${sign}<$0.000001`;
	return `${sign}$${s}`;
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
export function formatRateLimitLines(
	rateLimits:
		| {
				token_max_limit?: number | null;
				token_reset_duration?: string | null;
				request_max_limit?: number | null;
				request_reset_duration?: string | null;
		  }
		| null
		| undefined,
): string[] {
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