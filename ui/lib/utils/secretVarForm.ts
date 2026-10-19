import { type SecretVar } from "@/lib/types/schemas";

function inferType(ref: string | undefined): SecretVar["type"] | undefined {
	if (!ref) return undefined;
	if (ref.startsWith("vault.")) return "vault";
	if (ref.startsWith("env.")) return "env";
	return undefined;
}

export const emptySecretVar = (): SecretVar => ({ value: "", ref: "" });

// isSecretVarSet reports whether a SecretVar carries a non-blank literal value or reference.
export const isSecretVarSet = (secret: SecretVar | undefined): boolean => Boolean(secret?.value?.trim() || secret?.ref?.trim());

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
// isMaskedHeaderSecretVar reports whether a header value is the "<REDACTED>" mask the API returns for literals.
const isMaskedHeaderSecretVar = (value: SecretVar): boolean =>
	!value.ref?.trim() && (value.value ?? "").trim().toLowerCase() === "<redacted>";

// storedHeaderSpelling returns the stored header name matching name case-insensitively, if any.
const storedHeaderSpelling = (name: string, storedNames: ReadonlySet<string>): string | undefined => {
	const lower = name.toLowerCase();
	for (const stored of storedNames) if (stored.toLowerCase() === lower) return stored;
	return undefined;
};

// Clears a masked value ("<REDACTED>") that sits under a header name the server has not
// stored, e.g. after a rename: the server cannot restore it there and would reject the save.
// A case-only rename keeps the mask, since toExtraHeadersPayload saves it under the stored spelling.
export const clearUnrestorableMaskedHeaders = (
	headers: Record<string, SecretVar>,
	storedNames: ReadonlySet<string>,
): Record<string, SecretVar> => {
	let changed = false;
	const out: Record<string, SecretVar> = {};
	for (const [name, value] of Object.entries(headers)) {
		const clear = isMaskedHeaderSecretVar(value) && !storedHeaderSpelling(name, storedNames);
		out[name] = clear ? emptySecretVar() : value;
		changed ||= clear;
	}
	return changed ? out : headers;
};

// headerSecretVarsEqual compares header maps by their persisted string form. react-hook-form's
// dirty check skips keys named "ref", so an env/vault-only edit is invisible to isDirty.
export const headerSecretVarsEqual = (a?: Record<string, SecretVar>, b?: Record<string, SecretVar>): boolean => {
	const entries = Object.entries(a ?? {});
	const other = b ?? {};
	if (entries.length !== Object.keys(other).length) return false;
	return entries.every(([name, value]) => Object.hasOwn(other, name) && toEnvRefString(value) === toEnvRefString(other[name]));
};

// toExtraHeadersPayload flattens form headers to the string map the API stores, saving a masked
// value under the stored spelling of its name (header names are case-insensitive on the wire) so
// the server can restore it. missingValues lists named headers left without a value.
export const toExtraHeadersPayload = (
	headers: Record<string, SecretVar>,
	storedNames: ReadonlySet<string>,
): { headers: Record<string, string>; missingValues: string[] } => {
	const named: Record<string, SecretVar> = {};
	const missingValues: string[] = [];
	for (const [rawName, value] of Object.entries(headers)) {
		const name = rawName.trim();
		if (!name) continue;
		if (!isSecretVarSet(value)) {
			missingValues.push(name);
			continue;
		}
		named[isMaskedHeaderSecretVar(value) ? (storedHeaderSpelling(name, storedNames) ?? name) : name] = value;
	}
	return { headers: toHeaderStringMap(named), missingValues };
};

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