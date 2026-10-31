import type { ParseKeys } from "i18next";
import type {
	CustomProviderConfig,
	ModelProvider,
	PassthroughPolicy,
	PassthroughRouteRule,
	UpdateProviderRequest,
} from "@/lib/types/config";
import { PASSTHROUGH_METHODS, type PassthroughFormSchema, type PassthroughRuleRowSchema } from "@/lib/types/schemas";

export type PassthroughMode = PassthroughFormSchema["mode"];

// ALL_ROUTES_PATH is the path rule that matches every route.
export const ALL_ROUTES_PATH = "/**";

// GENAI_PASSTHROUGH_PREFIX keeps the API version in the rule path, unlike the other prefixes.
export const GENAI_PASSTHROUGH_PREFIX = "/genai_passthrough";

const BUILT_IN_PASSTHROUGH_PREFIXES: Record<string, string> = {
	openai: "/openai_passthrough",
	anthropic: "/anthropic_passthrough",
	azure: "/azure_passthrough",
	gemini: GENAI_PASSTHROUGH_PREFIX,
	vertex: GENAI_PASSTHROUGH_PREFIX,
};

// A custom provider is reached through its base provider's route.
const CUSTOM_BASE_PASSTHROUGH_PREFIXES: Record<string, string> = {
	openai: "/openai_passthrough",
	anthropic: "/anthropic_passthrough",
	gemini: GENAI_PASSTHROUGH_PREFIX,
	bedrock: "/bedrock_passthrough",
};

// passthroughRoutePrefix is the route clients call to reach the provider through passthrough,
// or undefined when it has none (and so no Passthrough tab).
export function passthroughRoutePrefix(provider: Pick<ModelProvider, "name" | "custom_provider_config">): string | undefined {
	const custom = provider.custom_provider_config;
	if (custom) return CUSTOM_BASE_PASSTHROUGH_PREFIXES[custom.base_provider_type];
	return BUILT_IN_PASSTHROUGH_PREFIXES[provider.name];
}

// defaultPassthroughMode is what an absent policy means: every route for a built-in provider,
// none for a custom one.
export function defaultPassthroughMode(isCustom: boolean): PassthroughMode {
	return isCustom ? "off" : "all";
}

// passthroughModeOptions lists the modes with the provider kind's default first. labelKey is
// translated where the option is rendered.
export function passthroughModeOptions(isCustom: boolean): Array<{ value: PassthroughMode; labelKey: ParseKeys<"models"> }> {
	if (isCustom) {
		return [
			{ value: "off", labelKey: "providers.passthrough.modeOffDefault" },
			{ value: "all", labelKey: "providers.passthrough.modeAll" },
			{ value: "custom", labelKey: "providers.passthrough.modeCustom" },
		];
	}
	return [
		{ value: "all", labelKey: "providers.passthrough.modeAllDefault" },
		{ value: "custom", labelKey: "providers.passthrough.modeCustom" },
		{ value: "off", labelKey: "providers.passthrough.modeOff" },
	];
}

function hasMethods(rule: PassthroughRouteRule): boolean {
	return (rule.methods?.length ?? 0) > 0;
}

// passthroughModeOf derives the tab's mode from the stored policy. Anything that is not
// exactly one of the named shapes is custom.
export function passthroughModeOf(policy: PassthroughPolicy | null | undefined, isCustom: boolean): PassthroughMode {
	if (!policy) return defaultPassthroughMode(isCustom);
	const routes = policy.allowed_routes ?? [];
	if (routes.length === 0) return "off";
	if (routes.length === 1 && routes[0].path === ALL_ROUTES_PATH && !hasMethods(routes[0])) return "all";
	return "custom";
}

// emptyRuleRow is the row "Add rule" appends and Custom mode starts with.
export function emptyRuleRow(): PassthroughRuleRowSchema {
	return { methods: [], path: "" };
}

function toRuleRow(rule: PassthroughRouteRule): PassthroughRuleRowSchema {
	const methods = PASSTHROUGH_METHODS.filter((method) => rule.methods?.some((m) => m.toUpperCase() === method));
	return { methods, path: rule.path };
}

// savedPassthroughFormValues is the form for the stored policy. Only custom mode loads rows.
export function savedPassthroughFormValues(policy: PassthroughPolicy | null | undefined, isCustom: boolean): PassthroughFormSchema {
	const mode = passthroughModeOf(policy, isCustom);
	return { mode, rules: mode === "custom" ? (policy?.allowed_routes ?? []).map(toRuleRow) : [] };
}

// rulesForCustomMode keeps rows already in the editor, or starts it with one empty rule.
export function rulesForCustomMode(rules: PassthroughRuleRowSchema[]): PassthroughRuleRowSchema[] {
	return rules.length > 0 ? rules : [emptyRuleRow()];
}

function toRouteRule(row: PassthroughRuleRowSchema): PassthroughRouteRule {
	// Canonical method order keeps the saved bytes stable however the methods were picked.
	const methods = PASSTHROUGH_METHODS.filter((method) => row.methods.includes(method));
	return methods.length > 0 ? { methods, path: row.path } : { path: row.path };
}

// toPassthroughPolicy is what Save writes for the form. null is the provider kind's default.
export function toPassthroughPolicy(values: PassthroughFormSchema, isCustom: boolean): PassthroughPolicy | null {
	switch (values.mode) {
		case "all":
			return isCustom ? { allowed_routes: [{ path: ALL_ROUTES_PATH }] } : null;
		case "off":
			return isCustom ? null : { allowed_routes: [] };
		case "custom":
			return { allowed_routes: values.rules.map(toRouteRule) };
	}
}

// passthroughChanged compares what each form would save, so switching modes and back, or
// rows left behind by a mode that ignores them, never count as a change.
export function passthroughChanged(values: PassthroughFormSchema, saved: PassthroughFormSchema, isCustom: boolean): boolean {
	return JSON.stringify(toPassthroughPolicy(values, isCustom)) !== JSON.stringify(toPassthroughPolicy(saved, isCustom));
}

// withPassthroughRequestsAllowed is the custom provider config with passthrough and
// passthrough_stream turned on, or undefined when nothing needs to change. A missing
// allowed_requests block already allows every request; an omitted flag inside one is off.
export function withPassthroughRequestsAllowed(config: CustomProviderConfig | undefined): CustomProviderConfig | undefined {
	const allowed = config?.allowed_requests;
	if (!config || !allowed || (allowed.passthrough === true && allowed.passthrough_stream === true)) return undefined;
	return { ...config, allowed_requests: { ...allowed, passthrough: true, passthrough_stream: true } };
}

// passthroughUpdates is the provider update Save sends. A custom provider that forwards
// anything also gets its passthrough request types turned on; the API replaces
// custom_provider_config wholesale, so the whole block is sent.
export function passthroughUpdates(
	values: PassthroughFormSchema,
	isCustom: boolean,
	customConfig: CustomProviderConfig | undefined,
): Partial<UpdateProviderRequest> {
	const passthrough = toPassthroughPolicy(values, isCustom);
	const enabled = isCustom && values.mode !== "off" ? withPassthroughRequestsAllowed(customConfig) : undefined;
	return enabled ? { passthrough, custom_provider_config: enabled } : { passthrough };
}