import type { MCPCodeModeLimits } from "@/lib/types/config";

// Server-side bounds, mirrored from core/schemas MCPCodeModeLimits.Validate.
const MIN_VALUE_BYTES = 1024;
const MAX_NESTING_DEPTH = 1000;

export const CODE_MODE_LIMIT_FIELDS: { key: keyof MCPCodeModeLimits; label: string; description: string; defaultValue: number }[] = [
	{
		key: "max_source_bytes",
		label: "Max Source Size (bytes)",
		description: "Largest code a single execution may submit.",
		defaultValue: 65536,
	},
	{
		key: "max_steps",
		label: "Max Computation Steps",
		description: "Interpreter steps per execution, including work inside builtins.",
		defaultValue: 1000000,
	},
	{
		key: "max_memory_bytes",
		label: "Max Memory (bytes)",
		description: "Estimated allocation budget per execution. Every concurrent execution gets its own budget.",
		defaultValue: 67108864,
	},
	{
		key: "max_log_bytes",
		label: "Max Log Output (bytes)",
		description: "Print and tool log output per execution, including newlines.",
		defaultValue: 65536,
	},
	{ key: "max_tool_calls", label: "Max Tool Calls", description: "Nested MCP tool calls per execution.", defaultValue: 64 },
	{
		key: "max_value_bytes",
		label: "Max Value Size (bytes)",
		description: `Size budget for each tool payload and converted result. Must be at least ${MIN_VALUE_BYTES}.`,
		defaultValue: 1048576,
	},
	{
		key: "max_nesting_depth",
		label: "Max Nesting Depth",
		description: `Nesting depth of values crossing the tool boundary. At most ${MAX_NESTING_DEPTH}.`,
		defaultValue: 64,
	},
];

// Missing, undefined and 0 all mean "use the default", so they compare equal.
export function codeModeLimitsEqual(a: MCPCodeModeLimits | undefined, b: MCPCodeModeLimits | undefined): boolean {
	return CODE_MODE_LIMIT_FIELDS.every(({ key }) => (a?.[key] ?? 0) === (b?.[key] ?? 0));
}

// Returns 0 for an empty input (the default), the value for a whole number >= 0,
// and undefined for anything else so the caller can keep the previous value.
export function parseCodeModeLimitInput(value: string, { badInput = false }: { badInput?: boolean } = {}): number | undefined {
	// A number input reports "" while it holds unparseable text; only a truly empty field means the default.
	if (badInput) return undefined;
	if (value.trim() === "") return 0;
	if (!/^\d+$/.test(value.trim())) return undefined;
	return Number.parseInt(value, 10);
}

export function validateCodeModeLimits(limits: MCPCodeModeLimits | undefined): string | null {
	if (!limits) return null;
	for (const { key, label } of CODE_MODE_LIMIT_FIELDS) {
		if ((limits[key] ?? 0) < 0) return `${label} must not be negative.`;
	}
	const valueBytes = limits.max_value_bytes ?? 0;
	if (valueBytes !== 0 && valueBytes < MIN_VALUE_BYTES) return `Max value size must be at least ${MIN_VALUE_BYTES} bytes.`;
	if ((limits.max_nesting_depth ?? 0) > MAX_NESTING_DEPTH) return `Max nesting depth must be at most ${MAX_NESTING_DEPTH}.`;
	return null;
}