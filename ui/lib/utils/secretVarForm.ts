import { type SecretVar } from "@/lib/types/schemas";

function inferType(ref: string | undefined): SecretVar["type"] | undefined {
	if (!ref) return undefined;
	if (ref.startsWith("vault.")) return "vault";
	if (ref.startsWith("env.")) return "env";
	return undefined;
}

export const emptySecretVar = (): SecretVar => ({ value: "", ref: "" });

// trimSecretVar strips stray whitespace from a SecretVar form value's literal value and reference.
export const trimSecretVar = <T extends { value?: string; ref?: string } | undefined>(field: T): T => {
	if (!field) return field;
	return { ...field, value: field.value?.trim(), ref: field.ref?.trim() };
};

export const toSecretVarFormValue = (field?: SecretVar | string): SecretVar => {
	if (!field) return emptySecretVar();
	if (typeof field === "string") {
		const value = field.trim();
		if (!value) return emptySecretVar();
		const isSecretRef = value.startsWith("env.") || value.startsWith("vault.");
		return {
			value: isSecretRef ? "" : value,
			ref: isSecretRef ? value : "",
			type: isSecretRef ? (value.startsWith("vault.") ? "vault" : "env") : undefined,
		};
	}
	return {
		value: field.value || "",
		ref: field.ref || "",
		type: field.type ?? inferType(field.ref),
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
	const effectiveType = field.type ?? inferType(field.ref);
	if (effectiveType && effectiveType !== "plain_text") return (field.ref || "").trim();
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

export const toOptionalSecretVarPayload = (field?: SecretVar) => {
	const secretRef = field?.ref?.trim();
	const value = field?.value?.trim();
	const effectiveType = field?.type ?? inferType(field?.ref);
	const isSecret = effectiveType && effectiveType !== "plain_text";
	if (!value && !(isSecret && secretRef)) return undefined;
	return {
		value: value || "",
		ref: secretRef || "",
		type: effectiveType,
	};
};