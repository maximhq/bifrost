import { PluginLogEntry } from "@/lib/types/logs";

// Keeps only entries carrying every field the view reads, so one malformed element
// in a stored payload cannot take down the whole detail sheet.
function isRenderablePluginLogEntry(value: unknown): value is PluginLogEntry {
	if (!value || typeof value !== "object" || Array.isArray(value)) return false;
	const entry = value as Record<string, unknown>;
	return (
		typeof entry.level === "string" &&
		typeof entry.message === "string" &&
		typeof entry.timestamp === "number" &&
		Number.isFinite(entry.timestamp)
	);
}

/**
 * Parses the stored `plugin_logs` JSON (entries grouped by plugin name). Returns null
 * when the payload is not an object or holds no renderable entries. Plugins whose value
 * is not an array, and array elements missing a consumed field, are dropped.
 */
export function parsePluginLogs(pluginLogs: string): Record<string, PluginLogEntry[]> | null {
	let raw: unknown;
	try {
		raw = JSON.parse(pluginLogs);
	} catch {
		return null;
	}
	if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;

	// Prototype-free so a plugin named "__proto__" is stored as a group instead of
	// rebinding the accumulator's prototype and losing the entries.
	const parsed: Record<string, PluginLogEntry[]> = Object.create(null);
	for (const [name, value] of Object.entries(raw as Record<string, unknown>)) {
		if (!Array.isArray(value)) continue;
		const entries = value.filter(isRenderablePluginLogEntry);
		if (entries.length > 0) parsed[name] = entries;
	}
	return Object.keys(parsed).length > 0 ? parsed : null;
}