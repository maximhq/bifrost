import { isRegexEntry, regexEntryPattern } from "@/components/modelAccess/utils";
import i18n from "@/lib/i18n";
import type { MCPClient } from "@/lib/types/mcp";
import type { MCPExposeItem } from "./mcpExposeList";

const sameKey = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

// matchesEntry mirrors the gateway: plain entries match case-insensitively, regex: entries as a full
// case-insensitive match; a pattern that does not compile never matches.
function matchesEntry(entry: string, key: string): boolean {
	if (!isRegexEntry(entry)) return sameKey(entry, key);
	try {
		return new RegExp(`^(?:${regexEntryPattern(entry)})$`, "i").test(key);
	} catch {
		return false;
	}
}

const isUnrestricted = (value: string[] | null | undefined) => !value || value.includes("*");

// isExposed reports whether key is served under an allow-list; absent or ["*"] serves everything.
export function isExposed(value: string[] | null | undefined, key: string): boolean {
	return isUnrestricted(value) || value!.some((entry) => matchesEntry(entry, key));
}

// exposedByRule reports whether a regex: entry serves key, so its switch cannot turn it off.
export function exposedByRule(value: string[] | null | undefined, key: string): boolean {
	return !isUnrestricted(value) && value!.some((entry) => isRegexEntry(entry) && matchesEntry(entry, key));
}

// nextExposeList is the allow-list after toggling key. Rules and saved keys missing from discovery are
// kept; it collapses to ["*"] only when the list is exactly the discovered keys.
export function nextExposeList(value: string[] | null | undefined, discovered: string[], key: string, exposed: boolean): string[] {
	// Keys differing only by case are one entry to the gateway, and it rejects both in one list.
	const allKeys = discovered.filter((k, i) => discovered.findIndex((other) => sameKey(other, k)) === i);
	if (isUnrestricted(value)) {
		return exposed ? ["*"] : allKeys.filter((k) => !sameKey(k, key));
	}
	const rest = value!.filter((entry) => isRegexEntry(entry) || !sameKey(entry, key));
	const next = exposed && !rest.some((entry) => matchesEntry(entry, key)) ? [...rest, key] : rest;
	const isExactlyDiscovered =
		next.length === allKeys.length && !next.some(isRegexEntry) && allKeys.every((k) => next.some((entry) => sameKey(entry, k)));
	return isExactlyDiscovered ? ["*"] : next;
}

export type ExposeRowState = "served" | "notServed" | "rule";

// exposeRowState is a row's state where the list is shown without its switches.
export function exposeRowState(value: string[] | null | undefined, key: string): ExposeRowState {
	if (exposedByRule(value, key)) return "rule";
	return isExposed(value, key) ? "served" : "notServed";
}

// exposeRowLabel is a row's state as text, where the list is shown without its switches.
export function exposeRowLabel(value: string[] | null | undefined, key: string): string {
	switch (exposeRowState(value, key)) {
		case "rule":
			return i18n.t("registry.expose.servedByRule", { ns: "mcp" });
		case "served":
			return i18n.t("registry.expose.served", { ns: "mcp" });
		default:
			return i18n.t("registry.expose.notServed", { ns: "mcp" });
	}
}

// promptExposeItems lists a server's prompts by their own names: the gateway lists them with the
// client prefix, while the allow-list holds the server's names.
export function promptExposeItems(client: Pick<MCPClient, "config" | "prompts">): MCPExposeItem[] {
	const prefix = `${client.config.name}-`;
	return (client.prompts ?? []).map((prompt) => {
		const name = prompt.name.startsWith(prefix) ? prompt.name.slice(prefix.length) : prompt.name;
		return {
			key: name,
			label: prompt.title || name,
			detail: prompt.title ? name : undefined,
			description: prompt.description,
			arguments: (prompt.arguments ?? []).map((arg) => ({ name: arg.name, required: arg.required === true })),
		};
	});
}

// resourceExposeItems lists a server's resources by URI and its resource templates by URI template.
export function resourceExposeItems(client: Pick<MCPClient, "resources" | "resource_templates">): MCPExposeItem[] {
	return [
		...(client.resources ?? []).map((r) => ({
			key: r.uri,
			label: r.title || r.name,
			detail: r.uri,
			description: r.description,
			mimeType: r.mimeType,
		})),
		...(client.resource_templates ?? []).map((t) => ({
			key: t.uriTemplate,
			label: t.title || t.name,
			detail: t.uriTemplate,
			description: t.description,
			mimeType: t.mimeType,
		})),
	];
}