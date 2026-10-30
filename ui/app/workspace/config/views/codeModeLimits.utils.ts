import i18n from "@/lib/i18n";
import type { MCPCodeModeLimits } from "@/lib/types/config";

// Server-side bounds, mirrored from core/schemas MCPCodeModeLimits.Validate.
const MIN_VALUE_BYTES = 1024;
const MAX_NESTING_DEPTH = 1000;

type CodeModeLimitField = { key: keyof MCPCodeModeLimits; label: string; description: string; defaultValue: number };

// label and description resolve on access, so they follow the active language each time a component renders them.
const limitField = (
	key: keyof MCPCodeModeLimits,
	defaultValue: number,
	label: () => string,
	description: () => string,
): CodeModeLimitField => ({
	key,
	defaultValue,
	get label() {
		return label();
	},
	get description() {
		return description();
	},
});

export const CODE_MODE_LIMIT_FIELDS: CodeModeLimitField[] = [
	limitField(
		"max_source_bytes",
		65536,
		() => i18n.t("mcpSettings.limitMaxSourceBytes", { ns: "config" }),
		() => i18n.t("mcpSettings.limitMaxSourceBytesDesc", { ns: "config" }),
	),
	limitField(
		"max_steps",
		1000000,
		() => i18n.t("mcpSettings.limitMaxSteps", { ns: "config" }),
		() => i18n.t("mcpSettings.limitMaxStepsDesc", { ns: "config" }),
	),
	limitField(
		"max_memory_bytes",
		67108864,
		() => i18n.t("mcpSettings.limitMaxMemoryBytes", { ns: "config" }),
		() => i18n.t("mcpSettings.limitMaxMemoryBytesDesc", { ns: "config" }),
	),
	limitField(
		"max_log_bytes",
		65536,
		() => i18n.t("mcpSettings.limitMaxLogBytes", { ns: "config" }),
		() => i18n.t("mcpSettings.limitMaxLogBytesDesc", { ns: "config" }),
	),
	limitField(
		"max_tool_calls",
		64,
		() => i18n.t("mcpSettings.limitMaxToolCalls", { ns: "config" }),
		() => i18n.t("mcpSettings.limitMaxToolCallsDesc", { ns: "config" }),
	),
	limitField(
		"max_value_bytes",
		1048576,
		() => i18n.t("mcpSettings.limitMaxValueBytes", { ns: "config" }),
		() => i18n.t("mcpSettings.limitMaxValueBytesDesc", { ns: "config", min: MIN_VALUE_BYTES }),
	),
	limitField(
		"max_nesting_depth",
		64,
		() => i18n.t("mcpSettings.limitMaxNestingDepth", { ns: "config" }),
		() => i18n.t("mcpSettings.limitMaxNestingDepthDesc", { ns: "config", max: MAX_NESTING_DEPTH }),
	),
];

// Missing, undefined and 0 all mean "use the default", so they compare equal.
export function codeModeLimitsEqual(a: MCPCodeModeLimits | undefined, b: MCPCodeModeLimits | undefined): boolean {
	return CODE_MODE_LIMIT_FIELDS.every(({ key }) => (a?.[key] ?? 0) === (b?.[key] ?? 0));
}

// Returns 0 for an empty input (the default), the value for a whole number >= 0,
// and undefined for anything else so the caller can keep the previous value.
export function parseCodeModeLimitInput(value: string): number | undefined {
	if (value.trim() === "") return 0;
	if (!/^\d+$/.test(value.trim())) return undefined;
	return Number.parseInt(value, 10);
}

export function validateCodeModeLimits(limits: MCPCodeModeLimits | undefined): string | null {
	if (!limits) return null;
	for (const { key, label } of CODE_MODE_LIMIT_FIELDS) {
		if ((limits[key] ?? 0) < 0) return i18n.t("mcpSettings.limitNegative", { ns: "config", label });
	}
	const valueBytes = limits.max_value_bytes ?? 0;
	if (valueBytes !== 0 && valueBytes < MIN_VALUE_BYTES)
		return i18n.t("mcpSettings.limitValueBytesMin", { ns: "config", min: MIN_VALUE_BYTES });
	if ((limits.max_nesting_depth ?? 0) > MAX_NESTING_DEPTH)
		return i18n.t("mcpSettings.limitNestingMax", { ns: "config", max: MAX_NESTING_DEPTH });
	return null;
}