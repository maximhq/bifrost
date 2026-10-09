import { databricksKeyConfigSchema, oauthKeyConfigComplete, oauthKeyConfigSchema } from "@/lib/types/schemas";
import { z } from "zod";

type DatabricksKeyConfigForm = z.input<typeof databricksKeyConfigSchema>;
type OAuthKeyConfigForm = z.input<typeof oauthKeyConfigSchema>;
type OAuthKeyConfigPayload = Omit<OAuthKeyConfigForm, "_auth_type">;
type DatabricksKeyConfigPayload = Omit<DatabricksKeyConfigForm, "_auth_type">;

const isSet = (v: { value?: string; ref?: string } | undefined) => Boolean(v?.value || v?.ref);

// stripDatabricksAuthDiscriminator removes the UI-only auth discriminator before the key is
// sent to the API. On the personal access token path the service principal fields are inert,
// so they are dropped: explicitly when the user chose that tab, and otherwise only when the
// pair is incomplete. A complete pair with no discriminator is a valid OAuth M2M key whose
// discriminator was never re-seeded (it is set on mount and after the key resolves), and
// stripping its credentials would silently break the key on save.
export const stripDatabricksAuthDiscriminator = (config: DatabricksKeyConfigForm): DatabricksKeyConfigPayload => {
	const { _auth_type, ...rest } = config;
	const hasPair = isSet(rest.client_id) && isSet(rest.client_secret);
	if (_auth_type === "pat" || (_auth_type !== "oauth_m2m" && !hasPair)) {
		delete rest.client_id;
		delete rest.client_secret;
	}
	return rest;
};

// stripOAuthAuthDiscriminator removes the UI-only auth discriminator before the key is sent
// to the API, and drops the whole block on the API-key path: the form registers it for every
// openai-based key, and an inert block would be refused by the API as half-configured. A whole
// block with no discriminator is a valid OAuth key whose discriminator was never re-seeded, and
// is kept.
export const stripOAuthAuthDiscriminator = (config: OAuthKeyConfigForm | undefined): OAuthKeyConfigPayload | undefined => {
	if (!config) return undefined;
	const { _auth_type, ...rest } = config;
	if (_auth_type === "api_key" || (_auth_type !== "oauth" && !oauthKeyConfigComplete(rest))) {
		return undefined;
	}
	if (rest.grant_type === "client_credentials") {
		delete rest.private_key;
		delete rest.issuer;
		delete rest.subject;
		delete rest.key_id;
		delete rest.signing_algorithm;
		delete rest.assertion_lifetime_seconds;
	} else if (rest.grant_type === "jwt_bearer") {
		delete rest.client_id;
		delete rest.client_secret;
		delete rest.auth_style;
	}
	if (rest.scopes && rest.scopes.length === 0) delete rest.scopes;
	if (rest.extra_params && Object.keys(rest.extra_params).length === 0) delete rest.extra_params;
	for (const field of ["audience", "issuer", "subject", "key_id"] as const) {
		if (rest[field] !== undefined && !String(rest[field]).trim()) delete rest[field];
	}
	return rest;
};

type AuthBlock = { _auth_type?: string } & Record<string, unknown>;
type KeyPayload = Record<string, unknown> & { value?: { value?: string; ref?: string }; oauth_key_config?: OAuthKeyConfigForm };

// The auth tab of each provider block whose method is the static key value. Every other tab of
// that block authenticates some other way, so a key saved from it must not keep a stored value.
const staticKeyMethodBySection: Record<string, string> = {
	azure_key_config: "api_key",
	vertex_key_config: "api_key",
	bedrock_key_config: "api_key",
	bedrock_mantle_key_config: "api_key",
	databricks_key_config: "pat",
};

// applyAuthMethod shapes the key payload for the chosen authentication method. It drops every
// UI-only discriminator, and because the update API keeps every field the payload omits, a
// switch has to say what it clears: a tab that does not use the static key sends an explicit
// empty value (otherwise the stored key survives and, since the server prefers a value, keeps
// being used until it expires), and the OAuth block is sent whole or, when editing, as an
// explicit null (a create simply omits it). A block with no discriminator was never switched by
// the user, so its value is left alone.
export const applyAuthMethod = (key: KeyPayload, isEditing: boolean): Record<string, unknown> => {
	const out: Record<string, unknown> = { ...key };
	let usesStaticKey = true;
	for (const [section, staticMethod] of Object.entries(staticKeyMethodBySection)) {
		const block = out[section];
		if (!block || typeof block !== "object") continue;
		const { _auth_type, ...rest } = block as AuthBlock;
		out[section] = section === "databricks_key_config" ? stripDatabricksAuthDiscriminator(block as DatabricksKeyConfigForm) : rest;
		if (_auth_type !== undefined && _auth_type !== staticMethod) usesStaticKey = false;
	}
	if ("oauth_key_config" in key) {
		const stripped = stripOAuthAuthDiscriminator(key.oauth_key_config);
		delete out.oauth_key_config;
		if (stripped) {
			out.oauth_key_config = stripped;
			usesStaticKey = false;
		} else if (isEditing) {
			out.oauth_key_config = null;
		}
	}
	if (!usesStaticKey && !isSet(key.value)) {
		out.value = { value: "" };
	}
	return out;
};