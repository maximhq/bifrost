import { type EnvVar } from "@/lib/types/schemas";

export const emptyEnvVar = (): EnvVar => ({ value: "", env_var: "", from_env: false });

export const toEnvVarFormValue = (field?: EnvVar | string): EnvVar => {
	if (!field) return emptyEnvVar();
	if (typeof field === "string") {
		const value = field.trim();
		if (!value) return emptyEnvVar();
		const isEnvRef = value.startsWith("env.");
		return {
			value,
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