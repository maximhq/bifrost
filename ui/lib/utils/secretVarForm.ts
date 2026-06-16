import { type SecretVar } from "@/lib/types/schemas";

export const emptySecretVar = (): SecretVar => ({ value: "", env_var: "", from_env: false, vault_var: "", from_vault: false });

export const toSecretVarFormValue = (field?: SecretVar | string): SecretVar => {
	if (!field) return emptySecretVar();
	if (typeof field === "string") {
		const value = field.trim();
		if (!value) return emptySecretVar();
		if (value.startsWith("vault.")) {
			return { value: "", env_var: "", from_env: false, vault_var: value, from_vault: true };
		}
		const isEnvRef = value.startsWith("env.");
		return {
			value: isEnvRef ? "" : value,
			env_var: isEnvRef ? value : "",
			from_env: isEnvRef,
			vault_var: "",
			from_vault: false,
		};
	}
	return {
		value: field.value || "",
		env_var: field.env_var || "",
		from_env: field.from_env ?? false,
		vault_var: field.vault_var || "",
		from_vault: field.from_vault ?? false,
	};
};

export const toSecretVarMapFormValue = (map?: Record<string, string | SecretVar>): Record<string, SecretVar> => {
	if (!map) return {};
	return Object.fromEntries(Object.entries(map).map(([k, v]) => [k, toSecretVarFormValue(v)]));
};

// toEnvRefString flattens an SecretVar form value to its persisted string form:
// the "vault.path" reference when vault-backed, the "env.VAR" reference when sourced
// from the environment, otherwise the literal value.
export const toEnvRefString = (field?: SecretVar): string => {
	if (!field) return "";
	if (field.from_vault) return (field.vault_var || "").trim();
	if (field.from_env) return (field.env_var || "").trim();
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
	env_var?: string;
	from_env?: boolean;
	vault_var?: string;
	from_vault?: boolean;
}) => {
	const secretVar = field?.env_var?.trim();
	const vaultVar = field?.vault_var?.trim();
	const value = field?.value?.trim();
	if (!value && !(field?.from_env && secretVar) && !(field?.from_vault && vaultVar)) return undefined;
	return {
		value: value || "",
		env_var: secretVar || "",
		from_env: field?.from_env ?? false,
		vault_var: vaultVar || "",
		from_vault: field?.from_vault ?? false,
	};
};
