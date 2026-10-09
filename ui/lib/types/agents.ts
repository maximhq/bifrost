import type { SecretVar } from "./schemas";

export type AgentAuthType = "none" | "headers" | "oauth";

export interface AgentOAuthConfig {
	provider_url?: string;
	discovery_url?: string;
	token_url?: string;
	client_id?: SecretVar;
	client_secret?: SecretVar;
	scopes?: string[];
	resource?: string;
}

export interface AgentTLSConfig {
	insecure_skip_verify?: boolean;
	ca_cert_pem?: SecretVar;
	client_cert_pem?: SecretVar;
	client_key_pem?: SecretVar;
}

// Mirrors core/schemas.UpstreamAuth. Card and runtime authentication use the
// same shape but remain independent fields on every request.
export interface AgentUpstreamAuth {
	type: AgentAuthType;
	headers?: Record<string, SecretVar>;
	oauth?: AgentOAuthConfig;
	security_schemes?: string[];
	advanced?: boolean;
	tls?: AgentTLSConfig;
}

export interface AgentSecurityScheme {
	name: string;
	type: "apiKey" | "http" | "oauth2" | "openIdConnect" | "mutualTLS" | string;
	description?: string;
	in?: "header" | "query" | "cookie";
	header?: string;
	scheme?: string;
	bearer_format?: string;
	authorization_url?: string;
	token_url?: string;
	scopes?: string[];
}

// Outer entries are alternatives (OR). Scheme names within one entry are a
// compound requirement (AND), matching normalized A2A security requirements.
export type AgentSecurityRequirements = string[][];

export interface InspectAgentCardRequest {
	agent_card_url: string;
	discovery_auth?: AgentUpstreamAuth;
}

export interface InspectedAgentInterface {
	url: string;
	protocol_binding: string;
	protocol_version: string;
	tenant?: string;
}

export interface InspectedAgentSkill {
	id: string;
	name: string;
	description?: string;
	tags?: string[];
	examples?: string[];
	input_modes?: string[];
	output_modes?: string[];
	security_requirements?: AgentSecurityRequirements;
}

export interface InspectedAgentExtension {
	uri: string;
	description?: string;
	required?: boolean;
	params?: Record<string, unknown>;
}

export interface InspectAgentCardResponse {
	name?: string;
	description?: string;
	version?: string;
	provider?: string;
	provider_url?: string;
	documentation_url?: string;
	icon_url?: string;
	interfaces?: InspectedAgentInterface[];
	capabilities: Record<string, boolean>;
	default_input_modes?: string[];
	default_output_modes?: string[];
	skills?: InspectedAgentSkill[];
	extensions?: InspectedAgentExtension[];
	signature_count: number;
	security_schemes: AgentSecurityScheme[];
	security_requirements: AgentSecurityRequirements;
	warnings?: string[];
}

export interface AgentRegistrationView {
	name: string;
	agent_card_url: string;
	tenant?: string;
	enabled: boolean;
	allow_by_default: boolean;
	forward_accepted_credential: boolean;
	forward_accepted_credential_overrides_auth: boolean;
	discovery_auth?: AgentUpstreamAuth;
	runtime_auth?: AgentUpstreamAuth;
	extension_uris?: string[];
	virtual_key_ids?: string[];
	created_at: string;
	updated_at: string;
}

export interface AgentPushConfigView {
	agent_name: string;
	tenant?: string;
	task_id: string;
	config_id: string;
	url: string;
	created_at: string;
	updated_at: string;
}

export interface AgentPushConfigFilters {
	agent_names?: string[];
	task_id?: string;
	config_id?: string;
	url?: string;
	limit: number;
	offset: number;
}

export interface GetAgentPushConfigsResponse {
	push_configs: AgentPushConfigView[];
	agent_names: string[];
	count: number;
	total_count: number;
	limit: number;
	offset: number;
}

export interface GetAgentsResponse {
	agents: AgentRegistrationView[];
	count: number;
}

export interface CreateAgentRequest {
	name: string;
	agent_card_url: string;
	tenant?: string;
	enabled?: boolean;
	allow_by_default: boolean;
	forward_accepted_credential: boolean;
	forward_accepted_credential_overrides_auth: boolean;
	discovery_auth?: AgentUpstreamAuth;
	runtime_auth?: AgentUpstreamAuth;
	virtual_key_ids?: string[];
	extension_uris?: string[];
}

// PUT is a full replacement. Every field, including empty collections, must be
// sent or the stored value is removed.
export type UpdateAgentRequest = Omit<CreateAgentRequest, "name" | "enabled"> & {
	agent_card_url: string;
	enabled: boolean;
};