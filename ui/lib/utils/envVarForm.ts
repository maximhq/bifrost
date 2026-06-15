import { type EnvVar } from "@/lib/types/schemas";

export const emptyEnvVar = (): EnvVar => ({ value: "", env_var: "", from_env: false });

export const toEnvVarFormValue = (field?: EnvVar | string): EnvVar => {
	if (!field) return emptyEnvVar();
	if (typeof field === "string") {
		const value = field.trim();
		if (!value) return emptyEnvVar();
		const isEnvRef = value.startsWith("env.");
		return {
			value: isEnvRef ? "" : value,
			env_var: isEnvRef ? value : "",
			from_env: isEnvRef,
		};
	}
	return {
		value: field.value || "",
		env_var: field.env_var || "",
		from_env: field.from_env ?? false,
	};
};

export const toEnvVarMapFormValue = (map?: Record<string, string | EnvVar>): Record<string, EnvVar> => {
	if (!map) return {};
	return Object.fromEntries(Object.entries(map).map(([k, v]) => [k, toEnvVarFormValue(v)]));
};

// toEnvRefString flattens an EnvVar form value to its persisted string form:
// the "env.VAR" reference when sourced from the environment, otherwise the literal value.
export const toEnvRefString = (field?: EnvVar): string => {
	if (!field) return "";
	if (field.from_env) return (field.env_var || "").trim();
	return (field.value || "").trim();
};

// toHeaderStringMap converts a map of EnvVar header values into the plain-string map the
// OTEL backend expects (Profile.Headers is map[string]string using the "env.VAR" convention).
// Empty entries are dropped.
export const toHeaderStringMap = (headers?: Record<string, EnvVar>): Record<string, string> => {
	if (!headers) return {};
	const out: Record<string, string> = {};
	for (const [k, v] of Object.entries(headers)) {
		const key = k.trim();
		const value = toEnvRefString(v);
		if (key && value) out[key] = value;
	}
	return out;
};

export const toOptionalEnvVarPayload = (field?: { value?: string; env_var?: string; from_env?: boolean }) => {
	const envVar = field?.env_var?.trim();
	const value = field?.value?.trim();
	if (!value && !(field?.from_env && envVar)) return undefined;
	return {
		value: value || "",
		env_var: envVar || "",
		from_env: field?.from_env ?? false,
	};
};
