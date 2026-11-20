import type { ClientGatewayTelemetry } from "@/lib/types/config";
import type { SecretVar } from "@/lib/types/schemas";

// One collector as edited in the form; headers maps each name to its env or vault reference.
export interface TelemetryDestinationDraft {
	id: string;
	endpoint: string;
	headers: Record<string, SecretVar>;
	headerConflict?: boolean; // the headers table holds a rename onto a name another row already has
	storedEndpoint?: string; // the endpoint as loaded, which the server matches redacted headers by
}

// The telemetry section's form state. dropAttributes is a comma-separated list.
export interface TelemetryDraft {
	enabled: boolean;
	destinations: TelemetryDestinationDraft[];
	dropAttributes: string;
}

// The telemetry block sent to PUT /api/config.
export interface TelemetryPayload {
	enabled: boolean;
	destinations: { endpoint: string; headers?: Record<string, string> }[];
	drop_attributes?: string[];
}

// A reason the draft cannot be saved; the component turns it into a message.
export type TelemetryDraftError =
	| { kind: "noDestination" }
	| { kind: "endpointInvalid" }
	| { kind: "endpointDuplicate" }
	| { kind: "headerMalformed" }
	| { kind: "headerConflict" };

let nextDestinationId = 0;

// newDestinationId returns a stable React key for a collector row.
export function newDestinationId(): string {
	nextDestinationId += 1;
	return `telemetry-destination-${nextDestinationId}`;
}

// secretVarText is how a stored endpoint or header shows in the form: the
// reference for an env or vault value, the plain value otherwise.
export function secretVarText(v?: SecretVar | string): string {
	if (typeof v === "string") return v;
	if (!v) return "";
	if (v.type === "env" || v.type === "vault") return v.ref ?? "";
	return v.value ?? "";
}

// headerSecretVar turns a stored header value into what the headers table edits: an env or vault
// value keeps only its reference (the server sends its resolved value redacted).
export function headerSecretVar(v?: SecretVar | string): SecretVar {
	const text = secretVarText(v);
	if (text.startsWith("env.")) return { value: "", ref: text, type: "env" };
	if (text.startsWith("vault.")) return { value: "", ref: text, type: "vault" };
	return { value: text, ref: "" };
}

// telemetryDraftFromConfig builds the form state from the stored block.
export function telemetryDraftFromConfig(telemetry?: ClientGatewayTelemetry): TelemetryDraft {
	return {
		enabled: telemetry?.enabled ?? false,
		destinations: (telemetry?.destinations ?? []).map((d) => ({
			id: newDestinationId(),
			endpoint: secretVarText(d.endpoint),
			storedEndpoint: secretVarText(d.endpoint),
			headers: Object.fromEntries(Object.entries(d.headers ?? {}).map(([name, value]) => [name, headerSecretVar(value)])),
		})),
		dropAttributes: (telemetry?.drop_attributes ?? []).join(", "),
	};
}

// The value GET /api/config shows in place of a plain header value.
const REDACTED_PLACEHOLDER = "<REDACTED>";

const isRedactedPlaceholder = (v: SecretVar) => v.type !== "env" && v.type !== "vault" && v.value === REDACTED_PLACEHOLDER;

// withEndpoint sets a collector's endpoint. Once it differs from the stored one, a header still
// showing the redacted placeholder is blanked: the server can no longer tell which stored value
// it stands for and refuses it, so the user types it again before saving.
export function withEndpoint(row: TelemetryDestinationDraft, endpoint: string): TelemetryDestinationDraft {
	if (row.storedEndpoint === undefined || endpoint.trim() === row.storedEndpoint.trim()) return { ...row, endpoint };
	const headers = Object.fromEntries(
		Object.entries(row.headers).map(([name, value]) => [name, isRedactedPlaceholder(value) ? { value: "", ref: "" } : value]),
	);
	return { ...row, endpoint, headers };
}

// telemetryDraftKey is a comparable form of a draft (row ids ignored), used to
// tell whether the form differs from what is stored.
export function telemetryDraftKey(draft: TelemetryDraft): string {
	return JSON.stringify({
		enabled: draft.enabled,
		destinations: draft.destinations.map((d) => [
			d.endpoint.trim(),
			Object.entries(d.headers)
				.map(([name, value]) => [name.trim(), secretVarText(value).trim()])
				.filter(([name, value]) => name || value)
				.sort(),
		]),
		dropAttributes: splitList(draft.dropAttributes),
	});
}

function splitList(value: string): string[] {
	return value
		.split(",")
		.map((s) => s.trim())
		.filter(Boolean);
}

function isHttpURL(value: string): boolean {
	try {
		const u = new URL(value);
		return (u.protocol === "http:" || u.protocol === "https:") && !!u.host;
	} catch {
		return false;
	}
}

const isReference = (value: string) => value.startsWith("env.") || value.startsWith("vault.");

// telemetryPayloadFromDraft validates the draft the way the server does and
// returns the block to save. Rows with an empty endpoint are ignored; with
// telemetry off and no collectors, no block is sent at all.
export function telemetryPayloadFromDraft(draft: TelemetryDraft): { telemetry?: TelemetryPayload; error?: TelemetryDraftError } {
	const destinations: TelemetryPayload["destinations"] = [];
	const seen = new Set<string>();
	for (const row of draft.destinations) {
		if (row.headerConflict) return { error: { kind: "headerConflict" } };
		const endpoint = row.endpoint.trim();
		if (!endpoint) continue;
		// The server checks endpoints only while forwarding is on, so a paused collector never blocks a save.
		if (draft.enabled && !isReference(endpoint) && !isHttpURL(endpoint)) return { error: { kind: "endpointInvalid" } };
		// The server refuses a collector listed twice, even while forwarding is paused.
		if (seen.has(endpoint)) return { error: { kind: "endpointDuplicate" } };
		seen.add(endpoint);
		const headers: Record<string, string> = {};
		for (const [rawName, rawValue] of Object.entries(row.headers)) {
			const name = rawName.trim();
			const value = secretVarText(rawValue).trim();
			if (!name && !value) continue;
			if (!name || !value) return { error: { kind: "headerMalformed" } };
			headers[name] = value;
		}
		destinations.push(Object.keys(headers).length ? { endpoint, headers } : { endpoint });
	}
	if (draft.enabled && destinations.length === 0) return { error: { kind: "noDestination" } };
	const dropAttributes = splitList(draft.dropAttributes);
	if (!draft.enabled && destinations.length === 0 && dropAttributes.length === 0) return {};
	return {
		telemetry: {
			enabled: draft.enabled,
			destinations,
			...(dropAttributes.length ? { drop_attributes: dropAttributes } : {}),
		},
	};
}