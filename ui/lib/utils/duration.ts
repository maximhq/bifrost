// Nanoseconds per unit, largest first, matching Go duration string suffixes.
const NS_PER_UNIT: Array<[suffix: string, ns: number]> = [
	["h", 3_600_000_000_000],
	["m", 60_000_000_000],
	["s", 1_000_000_000],
	["ms", 1_000_000],
	["µs", 1_000],
	["ns", 1],
];

// The API returns duration settings (e.g. vk_rotation_cooldown) as int64
// nanoseconds and accepts Go duration strings on write; render nanoseconds as
// the largest exact unit so sub-second values (e.g. 500ms) are preserved
// rather than floored to whole seconds.
export const formatCooldown = (value?: number | string): string => {
	if (!value) return "";
	if (typeof value === "string") return value.trim();
	if (value <= 0) return "";
	for (const [suffix, ns] of NS_PER_UNIT) {
		if (value % ns === 0) return `${value / ns}${suffix}`;
	}
	return `${value}ns`;
};
