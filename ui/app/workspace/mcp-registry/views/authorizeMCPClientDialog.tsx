import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SecretVarInput } from "@/components/ui/secretVarInput";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useAuthorizeMCPClientMutation } from "@/lib/store";
import { SecretVar } from "@/lib/types/mcp";
import { mcpAuthorizeOAuthConfigSchema } from "@/lib/types/schemas";
import { parseArrayFromText } from "@/lib/utils/array";
import { useState } from "react";
import { OAuth2Authorizer } from "./oauth2Authorizer";

interface AuthorizeMCPClientDialogProps {
	open: boolean;
	onClose: () => void;
	onAuthorized: () => void;
	mcpClientId: string;
	mcpClientName: string;
	hasConnectionString: boolean;
}

const emptySecretVar: SecretVar = { value: "", ref: "" };

type UrlFieldErrors = Partial<Record<"authorize_url" | "token_url" | "registration_url", string>>;

// One-time OAuth setup for a per_user_oauth client stuck in "pending_tools" (see authorizeMCPClient in mcp.go for why).
export function AuthorizeMCPClientDialog({
	open,
	onClose,
	onAuthorized,
	mcpClientId,
	mcpClientName,
	hasConnectionString,
}: AuthorizeMCPClientDialogProps) {
	const { toast } = useToast();
	const [authorizeMCPClient, { isLoading }] = useAuthorizeMCPClientMutation();
	const [clientId, setClientId] = useState<SecretVar>(emptySecretVar);
	const [clientSecret, setClientSecret] = useState<SecretVar>(emptySecretVar);
	const [authorizeUrl, setAuthorizeUrl] = useState("");
	const [tokenUrl, setTokenUrl] = useState("");
	const [registrationUrl, setRegistrationUrl] = useState("");
	const [scopesText, setScopesText] = useState("");
	const [formError, setFormError] = useState<string | null>(null);
	const [urlErrors, setUrlErrors] = useState<UrlFieldErrors>({});
	const [oauthFlow, setOauthFlow] = useState<{ authorizeUrl: string; oauthConfigId: string; mcpClientId: string } | null>(null);

	const handleSubmit = async () => {
		setFormError(null);
		setUrlErrors({});
		const result = mcpAuthorizeOAuthConfigSchema.safeParse({
			authorize_url: authorizeUrl,
			token_url: tokenUrl,
			registration_url: registrationUrl,
		});
		if (!result.success) {
			const errors: UrlFieldErrors = {};
			for (const issue of result.error.issues) {
				const field = issue.path[0] as keyof UrlFieldErrors;
				if (!errors[field]) errors[field] = issue.message;
			}
			setUrlErrors(errors);
			return;
		}
		if (!clientId.value && !clientId.ref && !hasConnectionString) {
			setFormError("Either an OAuth client ID or a connection string on this client is required for OAuth discovery.");
			return;
		}
		try {
			const response = await authorizeMCPClient({
				id: mcpClientId,
				data: {
					oauth_config: {
						client_id: clientId,
						client_secret:
							clientSecret.value || clientSecret.type === "env" || clientSecret.type === "vault" ? clientSecret : emptySecretVar,
						authorize_url: authorizeUrl || undefined,
						token_url: tokenUrl || undefined,
						registration_url: registrationUrl || undefined,
						scopes: parseArrayFromText(scopesText),
					},
				},
			}).unwrap();
			setOauthFlow({ authorizeUrl: response.authorize_url, oauthConfigId: response.oauth_config_id, mcpClientId: response.mcp_client_id });
		} catch (error) {
			toast({ title: "Error", description: getErrorMessage(error), variant: "destructive" });
		}
	};

	if (oauthFlow) {
		return (
			<OAuth2Authorizer
				open
				onClose={() => {
					setOauthFlow(null);
					onClose();
				}}
				onSuccess={() => {
					setOauthFlow(null);
					onAuthorized();
				}}
				onError={(error) => {
					setOauthFlow(null);
					toast({ title: "Error", description: error, variant: "destructive" });
				}}
				authorizeUrl={oauthFlow.authorizeUrl}
				oauthConfigId={oauthFlow.oauthConfigId}
				mcpClientId={oauthFlow.mcpClientId}
				isPerUserOauth
			/>
		);
	}

	return (
		<Dialog open={open} onOpenChange={(next) => !next && onClose()}>
			<DialogContent className="sm:max-w-md">
				<DialogHeader>
					<DialogTitle>Connect {mcpClientName}</DialogTitle>
					<DialogDescription>
						This client was registered without completing OAuth, so no tools have been discovered yet. Provide the OAuth app
						credentials to authorize it now.
					</DialogDescription>
				</DialogHeader>
				<div className="space-y-4">
					<div className="space-y-2">
						<Label>OAuth Client ID (optional)</Label>
						<SecretVarInput
							value={clientId}
							onChange={setClientId}
							placeholder="your-client-id (auto-generated if empty)"
							data-testid="authorize-oauth-client-id"
						/>
					</div>
					<div className="space-y-2">
						<Label>OAuth Client Secret (optional for PKCE)</Label>
						<SecretVarInput
							value={clientSecret}
							onChange={setClientSecret}
							placeholder="your-client-secret"
							hideValueWhenEnv
							maskNonEnvValue
							data-testid="authorize-oauth-client-secret"
						/>
					</div>
					<div className="space-y-2">
						<Label>Authorization URL (optional, auto-discovered)</Label>
						<Input
							value={authorizeUrl}
							onChange={(e) => setAuthorizeUrl(e.target.value)}
							placeholder="https://provider.com/oauth/authorize"
							data-testid="authorize-oauth-authorize-url"
						/>
						{urlErrors.authorize_url && <p className="text-destructive text-xs">{urlErrors.authorize_url}</p>}
					</div>
					<div className="space-y-2">
						<Label>Token URL (optional, auto-discovered)</Label>
						<Input
							value={tokenUrl}
							onChange={(e) => setTokenUrl(e.target.value)}
							placeholder="https://provider.com/oauth/token"
							data-testid="authorize-oauth-token-url"
						/>
						{urlErrors.token_url && <p className="text-destructive text-xs">{urlErrors.token_url}</p>}
					</div>
					<div className="space-y-2">
						<Label>Registration URL (optional, auto-discovered)</Label>
						<Input
							value={registrationUrl}
							onChange={(e) => setRegistrationUrl(e.target.value)}
							placeholder="https://provider.com/oauth/register"
							data-testid="authorize-oauth-registration-url"
						/>
						{urlErrors.registration_url && <p className="text-destructive text-xs">{urlErrors.registration_url}</p>}
					</div>
					<div className="space-y-2">
						<Label>Scopes (optional, comma-separated)</Label>
						<Input
							value={scopesText}
							onChange={(e) => setScopesText(e.target.value)}
							placeholder="read, write"
							data-testid="authorize-oauth-scopes"
						/>
					</div>
					{formError && <p className="text-destructive text-sm">{formError}</p>}
				</div>
				<DialogFooter>
					<Button variant="outline" onClick={onClose} data-testid="authorize-cancel-btn">
						Cancel
					</Button>
					<Button onClick={handleSubmit} disabled={isLoading} data-testid="authorize-submit-btn">
						Continue
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
