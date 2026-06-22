import { type SecretVar } from "@/lib/types/schemas";

export const emptySecretVar = (): SecretVar => ({ value: "", secret_ref: "", from_secret: false });

export const toSecretVarFormValue = (field?: SecretVar | string): SecretVar => {
	if (!field) return emptySecretVar();
	if (typeof field === "string") {
		const value = field.trim();
		if (!value) return emptySecretVar();
		const isSecretRef = value.startsWith("env.") || value.startsWith("vault.");
		return {
			value: isSecretRef ? "" : value,
			secret_ref: isSecretRef ? value : "",
			from_secret: isSecretRef,
		};
	}
	return {
		value: field.value || "",
		secret_ref: field.secret_ref || "",
		from_secret: field.from_secret ?? false,
	};
};

export const toSecretVarMapFormValue = (map?: Record<string, string | SecretVar>): Record<string, SecretVar> => {
	if (!map) return {};
	return Object.fromEntries(Object.entries(map).map(([k, v]) => [k, toSecretVarFormValue(v)]));
};

// toEnvRefString flattens a SecretVar form value to its persisted string form:
// the "vault.path" or "env.VAR" reference when secret-backed, otherwise the literal value.
export const toEnvRefString = (field?: SecretVar): string => {
	if (!field) return "";
	if (field.from_secret) return (field.secret_ref || "").trim();
	return (field.value || "").trim();
};

// toHeaderStringMap converts a map of SecretVar header values into the plain-string map the
// OTEL backend expects (Profile.Headers is map[string]string using the "env.VAR" convention).
// Empty entries are dropped.
export const toHeaderStringMap = (headers?: Record<string, SecretVar>): Record<string, string> => {
	if (!headers) return {};
	const out: Record<string, string> = {};
	for (const [k, v] of Object.entries(headers)) {
		const key = k.trim();
		const value = toEnvRefString(v);
		if (key && value) out[key] = value;
	}
	return out;
};

export const toOptionalSecretVarPayload = (field?: {
	value?: string;
	secret_ref?: string;
	from_secret?: boolean;
}) => {
	const secretRef = field?.secret_ref?.trim();
	const value = field?.value?.trim();
	if (!value && !(field?.from_secret && secretRef)) return undefined;
	return {
		value: value || "",
		secret_ref: secretRef || "",
		from_secret: field?.from_secret ?? false,
	};
};
