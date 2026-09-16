import type { PricingOverrideMatchType } from "@/lib/types/governance";

export function patternError(matchType: PricingOverrideMatchType, pattern: string): string | undefined {
	const trimmed = pattern.trim();
	if (!trimmed) return "Pattern is required";

	if (matchType === "exact") {
		if (trimmed.includes("*")) return "Exact pattern cannot contain *";
		return undefined;
	}

	if (trimmed === "*") return undefined;

	const startsWithWildcard = trimmed.startsWith("*");
	const endsWithWildcard = trimmed.endsWith("*");
	const literal = trimmed.slice(startsWithWildcard ? 1 : 0, endsWithWildcard ? -1 : undefined);

	if (!startsWithWildcard && !endsWithWildcard) {
		return trimmed.includes("*")
			? "Wildcard (*) is only supported at the beginning, end, or both ends"
			: "Wildcard pattern must include * at the beginning, end, or both ends";
	}
	if (!literal) return "Wildcard pattern must include at least one non-wildcard character";
	if (literal.includes("*")) return "Wildcard (*) is only supported at the beginning, end, or both ends";

	return undefined;
}