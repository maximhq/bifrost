import type { EnvVar } from "./schemas";

export interface GoogleSSOConfig {
	client_id: EnvVar;
	client_secret: EnvVar;
	allowed_domains?: string[];
}

export interface SAMLConfig {
	entity_id: string;
	metadata_url?: string;
	idp_sso_url?: string;
	idp_certificate?: string;
	idp_entity_id?: string;
	sign_requests: boolean;
	force_authn: boolean;
	name_id_format?: string;
}

export interface User {
	id: string;
	email: string;
	name: string;
	role: string; // "admin" | "viewer"
	auth_provider: string; // "password" | "google_sso" | "saml"
	external_id: string;
	avatar_url: string;
	is_active: boolean;
	last_login_at: string | null;
	created_at: string;
	updated_at: string;
}

export interface AuthMethodsResponse {
	is_auth_enabled: boolean;
	has_valid_token: boolean;
	enabled_methods: string[];
	google_sso?: { login_url: string };
	saml?: { login_url: string };
}
