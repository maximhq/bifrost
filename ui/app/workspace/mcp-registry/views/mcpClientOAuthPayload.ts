import type { CreateMCPClientRequest, OAuthConfig, SecretVar } from "@/lib/types/mcp";
import { parseArrayFromText } from "@/lib/utils/array";

const emptySecretVar: SecretVar = { value: "", ref: "" };

/** The create request's oauth_config from the form values, with the scopes and resource kept as raw text. */
export function buildOAuthConfigPayload(
	oauth: CreateMCPClientRequest["oauth_config"],
	{ serverURL, scopesText, resourceText }: { serverURL?: string; scopesText: string; resourceText: string },
): OAuthConfig {
	// With the metadata document, the document's URL is the client ID and there is no secret.
	const viaClientMetadataDocument = oauth?.use_client_metadata_document === true;
	const secret = oauth?.client_secret;
	return {
		client_id: viaClientMetadataDocument ? emptySecretVar : (oauth?.client_id ?? emptySecretVar),
		client_secret: !viaClientMetadataDocument && (secret?.value || secret?.type === "env" || secret?.type === "vault") ? secret : undefined,
		authorize_url: oauth?.authorize_url || undefined,
		token_url: oauth?.token_url || undefined,
		registration_url: oauth?.registration_url || undefined,
		scopes: scopesText.trim() ? parseArrayFromText(scopesText) : undefined,
		server_url: serverURL || undefined,
		resource: resourceText.trim() || undefined,
		use_client_metadata_document: viaClientMetadataDocument || undefined,
	};
}