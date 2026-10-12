import type { InspectAgentCardResponse } from "@/lib/types/agents";

function asRecord(value: unknown): Record<string, unknown> | null {
	return value !== null && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, unknown>) : null;
}
function asArray(value: unknown): unknown[] | null {
	return Array.isArray(value) ? value : null;
}
function asString(value: unknown): string | undefined {
	return typeof value === "string" && value !== "" ? value : undefined;
}

// mapAgentCard normalizes a protocol card for the registration and log previews.
export function mapAgentCard(card: Record<string, unknown>): InspectAgentCardResponse {
	const strings = (value: unknown) => (asArray(value) ?? []).filter((item): item is string => typeof item === "string");
	const requirementDetails = (value: unknown) =>
		(asArray(value) ?? []).map((entry) => {
			const schemes = asRecord(asRecord(entry)?.schemes) ?? asRecord(entry) ?? {};
			return Object.fromEntries(Object.entries(schemes).map(([name, scopes]) => [name, strings(scopes)]));
		});
	const requirements = (value: unknown) => requirementDetails(value).map((entry) => Object.keys(entry));
	const safeURL = (value: unknown) => {
		const text = asString(value);
		if (!text) return undefined;
		try {
			const url = new URL(text);
			return url.protocol === "http:" || url.protocol === "https:" ? text : undefined;
		} catch {
			return undefined;
		}
	};
	const provider = asRecord(card.provider);
	const capabilities = asRecord(card.capabilities) ?? {};
	const model: InspectAgentCardResponse = {
		name: asString(card.name),
		description: asString(card.description),
		version: asString(card.version),
		provider: asString(provider?.organization),
		provider_url: safeURL(provider?.url),
		documentation_url: safeURL(card.documentationUrl),
		icon_url: safeURL(card.iconUrl),
		capabilities: Object.fromEntries(
			Object.entries(capabilities).filter((entry): entry is [string, boolean] => typeof entry[1] === "boolean"),
		),
		default_input_modes: strings(card.defaultInputModes),
		default_output_modes: strings(card.defaultOutputModes),
		interfaces: (asArray(card.supportedInterfaces) ?? []).flatMap((entry) => {
			const item = asRecord(entry);
			if (!item) return [];
			return [
				{
					url: asString(item.url) ?? "",
					protocol_binding: asString(item.protocolBinding) ?? "",
					protocol_version: asString(item.protocolVersion) ?? "",
					tenant: asString(item.tenant),
				},
			];
		}),
		skills: (asArray(card.skills) ?? []).flatMap((entry) => {
			const skill = asRecord(entry);
			if (!skill) return [];
			return [
				{
					id: asString(skill.id) ?? "",
					name: asString(skill.name) ?? "Unnamed skill",
					description: asString(skill.description),
					tags: strings(skill.tags),
					examples: strings(skill.examples),
					input_modes: strings(skill.inputModes),
					output_modes: strings(skill.outputModes),
					security_requirements: requirements(skill.securityRequirements),
					security_requirement_details: requirementDetails(skill.securityRequirements),
				},
			];
		}),
		extensions: (asArray(capabilities.extensions) ?? []).flatMap((entry) => {
			const extension = asRecord(entry);
			if (!extension) return [];
			return [
				{
					uri: asString(extension.uri) ?? "",
					description: asString(extension.description),
					required: extension.required === true,
					params: asRecord(extension.params) ?? undefined,
				},
			];
		}),
		signature_count: (asArray(card.signatures) ?? []).length,
		security_requirements: requirements(card.securityRequirements),
		security_requirement_details: requirementDetails(card.securityRequirements),
		security_schemes: Object.entries(asRecord(card.securitySchemes) ?? {}).flatMap(([name, value]) => {
			const scheme = asRecord(value);
			if (!scheme) return [];
			const variant = Object.entries(scheme).find(([key]) => key.endsWith("SecurityScheme"));
			const definition = asRecord(variant?.[1]) ?? scheme;
			return [
				{
					name,
					type: asString(scheme.type) ?? variant?.[0]?.replace("SecurityScheme", "") ?? "Unknown",
					description: asString(definition.description),
					definition,
				},
			];
		}),
		signatures: (asArray(card.signatures) ?? []).flatMap((entry) => {
			const signature = asRecord(entry);
			return signature ? [signature] : [];
		}),
	};
	return model;
}