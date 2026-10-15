import { VirtualKeySelector } from "@/components/entitySelectors/virtualKeySelector";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { getErrorMessage } from "@/lib/store";
import {
	type ManagedClientIdentity,
	type OAuth2ManagedClient,
	useCreateOAuth2ManagedClientMutation,
	useDeleteOAuth2ManagedClientMutation,
	useGetOAuth2ManagedClientsQuery,
	useRotateOAuth2ManagedClientSecretMutation,
	useUpdateOAuth2ManagedClientMutation,
} from "@/lib/store/apis/oauth2ManagedClientsApi";
import type { ManagedAuthConfig } from "@/lib/types/config";
import { Copy, KeyRound, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "sonner";

export const DEFAULT_MANAGED_AUTH_TTL = 86400;
const MIN_MANAGED_AUTH_TTL = 60;
const MAX_MANAGED_AUTH_TTL = 86400;
const CLAUDE_CODE_CLIENT_ID = "https://claude.ai/oauth/claude-code-client-metadata";

// validateManagedAuth mirrors the backend's ManagedAuthConfig.Validate so the
// page can refuse a save that would come back as a 400.
export function validateManagedAuth(cfg?: ManagedAuthConfig): string | null {
	if (!cfg) return null;
	const ttl = cfg.access_token_ttl ?? 0;
	if (ttl !== 0 && (ttl < MIN_MANAGED_AUTH_TTL || ttl > MAX_MANAGED_AUTH_TTL)) {
		return `Managed auth access token TTL must be between ${MIN_MANAGED_AUTH_TTL} and ${MAX_MANAGED_AUTH_TTL} seconds.`;
	}
	if (cfg.enabled && !cfg.trusted_issuer?.trim()) {
		return "Trusted issuer is required when enterprise-managed authorization is enabled.";
	}
	for (const [label, url] of [
		["Trusted issuer", cfg.trusted_issuer],
		["JWKS URL", cfg.jwks_url],
	] as const) {
		if (url && !/^https:\/\//i.test(url) && !/^http:\/\/(localhost|127\.0\.0\.1|\[::1\])(:|\/|$)/i.test(url)) {
			return `${label} must be an https URL.`;
		}
	}
	return null;
}

// managedAuthEquals compares two managed-auth configs field by field for dirty tracking.
export function managedAuthEquals(a?: ManagedAuthConfig, b?: ManagedAuthConfig): boolean {
	return (
		(a?.enabled ?? false) === (b?.enabled ?? false) &&
		(a?.trusted_issuer ?? "") === (b?.trusted_issuer ?? "") &&
		(a?.jwks_url ?? "") === (b?.jwks_url ?? "") &&
		(a?.access_token_ttl ?? 0) === (b?.access_token_ttl ?? 0) &&
		(a?.disable_user_provisioning ?? false) === (b?.disable_user_provisioning ?? false)
	);
}

interface ManagedAuthSectionProps {
	value?: ManagedAuthConfig;
	onChange: (next: ManagedAuthConfig) => void;
	canEdit: boolean;
	issuerURLHint: string;
}

// ManagedAuthSection configures the jwt-bearer (ID-JAG) grant used by MCP
// Enterprise-Managed Authorization (Okta Cross App Access). Settings save with
// the page; managed clients are created and changed immediately.
export function ManagedAuthSection({ value, onChange, canEdit, issuerURLHint }: ManagedAuthSectionProps) {
	const { data } = useGetOAuth2ManagedClientsQuery();
	const [ttlText, setTTLText] = useState(String(value?.access_token_ttl || DEFAULT_MANAGED_AUTH_TTL));
	// 0 means "backend default" and is what an emptied field stages, so only a
	// real value re-syncs the text; otherwise clearing the field would snap back.
	useEffect(() => {
		if (value?.access_token_ttl) setTTLText(String(value.access_token_ttl));
	}, [value?.access_token_ttl]);
	const update = (patch: Partial<ManagedAuthConfig>) => onChange({ ...value, ...patch });
	const suggestedIssuer = data?.suggested_trusted_issuer;

	return (
		<div className="mt-4 space-y-4 border-t pt-4" data-testid="managed-auth-section">
			<div className="flex items-center justify-between space-x-2">
				<div className="space-y-0.5">
					<label htmlFor="managed-auth-enabled" className="text-sm font-medium">
						Enterprise-managed authorization (Cross App Access)
					</label>
					<p className="text-muted-foreground text-sm">
						Lets MCP clients such as Claude connect without a consent screen by presenting an identity assertion (ID-JAG) issued by your
						identity provider, for example Okta Cross App Access. Only clients registered below can use it, and no refresh token is issued:
						the client asks the identity provider again, so removing a user there takes effect when their token expires.
					</p>
				</div>
				<Switch
					id="managed-auth-enabled"
					data-testid="managed-auth-enabled-switch"
					size="md"
					checked={value?.enabled ?? false}
					onCheckedChange={(enabled) =>
						update({ enabled, trusted_issuer: enabled && !value?.trusted_issuer ? suggestedIssuer : value?.trusted_issuer })
					}
					disabled={!canEdit}
				/>
			</div>

			{value?.enabled && (
				<>
					<div className="space-y-1.5">
						<label htmlFor="managed-auth-trusted-issuer" className="text-sm font-medium">
							Trusted issuer <span className="text-destructive">*</span>
						</label>
						<p className="text-muted-foreground text-sm">
							The only identity provider whose assertions are accepted. For Okta, use the org authorization server, e.g.{" "}
							<code className="text-xs">https://acme.okta.com</code>, not a custom authorization server.
						</p>
						<Input
							id="managed-auth-trusted-issuer"
							data-testid="managed-auth-trusted-issuer-input"
							placeholder={suggestedIssuer || "https://acme.okta.com"}
							value={value?.trusted_issuer ?? ""}
							onChange={(e) => update({ trusted_issuer: e.target.value.trim() })}
							disabled={!canEdit}
						/>
						{suggestedIssuer && value?.trusted_issuer && value.trusted_issuer !== suggestedIssuer && (
							<p className="text-muted-foreground text-xs">
								Your identity provider's org issuer is <code>{suggestedIssuer}</code>.
							</p>
						)}
					</div>
					<div className="flex gap-6">
						<div className="flex-1 space-y-1.5">
							<label htmlFor="managed-auth-jwks-url" className="text-sm font-medium">
								JWKS URL (optional)
							</label>
							<p className="text-muted-foreground text-xs">Overrides the signing keys discovered from the trusted issuer.</p>
							<Input
								id="managed-auth-jwks-url"
								data-testid="managed-auth-jwks-url-input"
								placeholder="Discovered from the issuer"
								value={value?.jwks_url ?? ""}
								onChange={(e) => update({ jwks_url: e.target.value.trim() || undefined })}
								disabled={!canEdit}
							/>
						</div>
						<div className="space-y-1.5">
							<label htmlFor="managed-auth-ttl" className="text-sm font-medium">
								Access token TTL (seconds)
							</label>
							<p className="text-muted-foreground text-xs">Default 86400 (24 hours). Okta caps ID-JAGs per user per month.</p>
							<Input
								id="managed-auth-ttl"
								data-testid="managed-auth-ttl-input"
								type="number"
								className="w-28"
								min={MIN_MANAGED_AUTH_TTL}
								max={MAX_MANAGED_AUTH_TTL}
								value={ttlText}
								placeholder={String(DEFAULT_MANAGED_AUTH_TTL)}
								onChange={(e) => {
									setTTLText(e.target.value);
									const num = Number.parseInt(e.target.value);
									update({ access_token_ttl: isNaN(num) ? 0 : num });
								}}
								disabled={!canEdit}
							/>
						</div>
					</div>
					{data?.user_identity_available && (
						<div className="flex items-center justify-between space-x-2">
							<div className="space-y-0.5">
								<label htmlFor="managed-auth-provision-users" className="text-sm font-medium">
									Create users on first use
								</label>
								<p className="text-muted-foreground text-sm">
									For clients whose tokens act as the signed-in user: when an assertion names someone with no Bifrost user yet, create
									them from the identity provider, as their first SSO sign-in would. When off, such users are refused until they sign in
									to Bifrost once.
								</p>
							</div>
							<Switch
								id="managed-auth-provision-users"
								data-testid="managed-auth-provision-users-switch"
								size="md"
								checked={!value?.disable_user_provisioning}
								onCheckedChange={(on) => update({ disable_user_provisioning: !on || undefined })}
								disabled={!canEdit}
							/>
						</div>
					)}
					<Alert>
						<AlertTitle>Identity provider setup</AlertTitle>
						<AlertDescription>
							In your identity provider, enable Cross App Access on the app that represents Bifrost and set its issuer URL to{" "}
							<code className="text-xs">{issuerURLHint}</code>. In the requesting app's resource connection, enter the client ID of a
							managed client below. Claude Code identifies itself as <code className="text-xs">{CLAUDE_CODE_CLIENT_ID}</code>.
						</AlertDescription>
					</Alert>
					<ManagedClients canEdit={canEdit} userIdentityAvailable={data?.user_identity_available ?? false} clients={data?.clients ?? []} />
				</>
			)}
		</div>
	);
}

interface ManagedClientsProps {
	canEdit: boolean;
	userIdentityAvailable: boolean;
	clients: OAuth2ManagedClient[];
}

// ManagedClients lists the registered managed clients and their actions.
function ManagedClients({ canEdit, userIdentityAvailable, clients }: ManagedClientsProps) {
	const { copy: copyClientID } = useCopyToClipboard({ successMessage: "Client ID copied." });
	const [createOpen, setCreateOpen] = useState(false);
	const [revealed, setRevealed] = useState<OAuth2ManagedClient | null>(null);
	const [pendingDelete, setPendingDelete] = useState<OAuth2ManagedClient | null>(null);
	const [updateClient] = useUpdateOAuth2ManagedClientMutation();
	const [rotateSecret] = useRotateOAuth2ManagedClientSecretMutation();
	const [deleteClient] = useDeleteOAuth2ManagedClientMutation();

	const run = async (action: () => Promise<unknown>, success: string) => {
		try {
			await action();
			toast.success(success);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<div className="space-y-2">
			<div className="flex items-center justify-between">
				<p className="text-sm font-medium">Managed clients</p>
				<Button size="sm" variant="outline" onClick={() => setCreateOpen(true)} disabled={!canEdit} data-testid="managed-client-add-btn">
					Add client
				</Button>
			</div>
			{clients.length === 0 ? (
				<p className="text-muted-foreground text-sm">
					No managed clients yet. Add one for each MCP client your identity provider connects.
				</p>
			) : (
				<Table data-testid="managed-clients-table">
					<TableHeader>
						<TableRow>
							<TableHead>Client</TableHead>
							<TableHead>Acts as</TableHead>
							<TableHead>Last used</TableHead>
							<TableHead className="text-right">Actions</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{clients.map((client) => (
							<TableRow key={client.id} data-testid={`managed-client-row-${client.id}`}>
								<TableCell className="max-w-72">
									<div className="truncate font-medium">{client.name || "Unnamed client"}</div>
									<div className="flex items-center gap-1">
										<code className="text-muted-foreground truncate text-xs">{client.client_id}</code>
										<Button
											size="icon"
											variant="ghost"
											className="size-6"
											aria-label="Copy client ID"
											onClick={() => copyClientID(client.client_id)}
										>
											<Copy className="size-3" />
										</Button>
									</div>
									<div className="mt-1 flex gap-1">
										<Badge variant="outline">{client.has_secret ? "Confidential" : "Public"}</Badge>
										{client.disabled && <Badge variant="warning">Disabled</Badge>}
									</div>
								</TableCell>
								<TableCell className="text-sm">{client.identity === "user" ? "The signed-in user" : "Bound virtual key"}</TableCell>
								<TableCell className="text-muted-foreground text-sm">
									{client.last_used_at ? new Date(client.last_used_at).toLocaleString() : "Never"}
								</TableCell>
								<TableCell className="space-x-1 text-right">
									<Switch
										size="md"
										aria-label="Client enabled"
										checked={!client.disabled}
										disabled={!canEdit}
										onCheckedChange={(enabled) =>
											run(
												() => updateClient({ id: client.id, disabled: !enabled }).unwrap(),
												enabled ? "Client enabled." : "Client disabled.",
											)
										}
									/>
									<Button
										size="icon"
										variant="ghost"
										aria-label="Rotate secret"
										title={client.has_secret ? "Rotate secret" : "Public clients have no secret"}
										disabled={!canEdit || !client.has_secret}
										onClick={() => run(async () => setRevealed(await rotateSecret(client.id).unwrap()), "New client secret generated.")}
									>
										<KeyRound className="size-4" />
									</Button>
									<Button
										size="icon"
										variant="ghost"
										aria-label="Delete client"
										disabled={!canEdit}
										onClick={() => setPendingDelete(client)}
									>
										<Trash2 className="size-4" />
									</Button>
								</TableCell>
							</TableRow>
						))}
					</TableBody>
				</Table>
			)}

			<CreateManagedClientDialog
				open={createOpen}
				onOpenChange={setCreateOpen}
				userIdentityAvailable={userIdentityAvailable}
				onCreated={(client) => {
					setCreateOpen(false);
					setRevealed(client);
				}}
			/>

			<Dialog open={!!revealed} onOpenChange={(open) => !open && setRevealed(null)}>
				<DialogContent data-testid="managed-client-credentials-dialog">
					<DialogHeader>
						<DialogTitle>Client credentials</DialogTitle>
						<DialogDescription>
							Enter these in your identity provider's resource connection and in the MCP client's connector settings.
							{revealed?.client_secret && " The secret is shown only once."}
						</DialogDescription>
					</DialogHeader>
					<CredentialRow label="Client ID" value={revealed?.client_id ?? ""} />
					{revealed?.client_secret && <CredentialRow label="Client secret" value={revealed.client_secret} testId="managed-client-secret" />}
					<DialogFooter>
						<Button onClick={() => setRevealed(null)}>Done</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>

			<AlertDialog open={!!pendingDelete} onOpenChange={(open) => !open && setPendingDelete(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete managed client?</AlertDialogTitle>
						<AlertDialogDescription>
							The client can no longer exchange identity assertions. Tokens it already received keep working until they expire.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction
							onClick={() => {
								const target = pendingDelete;
								setPendingDelete(null);
								if (target) void run(() => deleteClient(target.id).unwrap(), "Managed client deleted.");
							}}
						>
							Delete
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}

// CredentialRow shows one read-only credential with a copy button.
function CredentialRow({ label, value, testId }: { label: string; value: string; testId?: string }) {
	const { copy } = useCopyToClipboard({ successMessage: `${label} copied.` });
	return (
		<div className="space-y-1">
			<p className="text-sm font-medium">{label}</p>
			<div className="flex gap-2">
				<Input readOnly value={value} data-testid={testId} className="font-mono text-xs" />
				<Button
					variant="outline"
					size="icon"
					aria-label={`Copy ${label}`}
					onClick={() => copy(value)}
				>
					<Copy className="size-4" />
				</Button>
			</div>
		</div>
	);
}

interface CreateManagedClientDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	userIdentityAvailable: boolean;
	onCreated: (client: OAuth2ManagedClient) => void;
}

// CreateManagedClientDialog registers a new managed client.
function CreateManagedClientDialog({ open, onOpenChange, userIdentityAvailable, onCreated }: CreateManagedClientDialogProps) {
	const [createClient, { isLoading }] = useCreateOAuth2ManagedClientMutation();
	const [name, setName] = useState("");
	const [clientID, setClientID] = useState("");
	const [confidential, setConfidential] = useState(true);
	const [identity, setIdentity] = useState<ManagedClientIdentity>(userIdentityAvailable ? "user" : "vk");
	const [virtualKeyID, setVirtualKeyID] = useState("");

	useEffect(() => {
		if (open) {
			setName("");
			setClientID("");
			setConfidential(true);
			setIdentity(userIdentityAvailable ? "user" : "vk");
			setVirtualKeyID("");
		}
	}, [open, userIdentityAvailable]);

	const submit = async () => {
		if (identity === "vk" && !virtualKeyID) {
			toast.error("Select the virtual key this client acts as.");
			return;
		}
		try {
			const created = await createClient({
				name: name.trim() || undefined,
				client_id: clientID.trim() || undefined,
				public: !confidential,
				identity,
				virtual_key_id: identity === "vk" ? virtualKeyID : undefined,
			}).unwrap();
			onCreated(created);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent data-testid="managed-client-create-dialog">
				<DialogHeader>
					<DialogTitle>Add managed client</DialogTitle>
					<DialogDescription>Register an MCP client that may exchange identity assertions for Bifrost tokens.</DialogDescription>
				</DialogHeader>
				<div className="space-y-3">
					<div className="space-y-1.5">
						<label htmlFor="managed-client-name" className="text-sm font-medium">
							Name
						</label>
						<Input id="managed-client-name" placeholder="Claude" value={name} onChange={(e) => setName(e.target.value)} />
					</div>
					<div className="space-y-1.5">
						<label htmlFor="managed-client-id" className="text-sm font-medium">
							Client ID (optional)
						</label>
						<p className="text-muted-foreground text-xs">
							Leave empty to generate one. Use the client's own identifier when it has one, e.g. <code>{CLAUDE_CODE_CLIENT_ID}</code> for
							Claude Code.
						</p>
						<Input
							id="managed-client-id"
							data-testid="managed-client-id-input"
							placeholder="Generated"
							value={clientID}
							onChange={(e) => setClientID(e.target.value)}
						/>
					</div>
					<div className="flex items-center justify-between">
						<div>
							<p className="text-sm font-medium">Require a client secret</p>
							<p className="text-muted-foreground text-xs">Turn off for public clients that cannot keep a secret, such as Claude Code.</p>
						</div>
						<Switch size="md" checked={confidential} onCheckedChange={setConfidential} data-testid="managed-client-confidential-switch" />
					</div>
					<div className="space-y-1.5">
						<p className="text-sm font-medium">Tokens act as</p>
						<Select value={identity} onValueChange={(v) => setIdentity(v as ManagedClientIdentity)}>
							<SelectTrigger data-testid="managed-client-identity-select">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{userIdentityAvailable && <SelectItem value="user">The signed-in user (matched from the assertion)</SelectItem>}
								<SelectItem value="vk">A virtual key</SelectItem>
							</SelectContent>
						</Select>
						{identity === "vk" && (
							<>
								<p className="text-muted-foreground text-xs">
									Every user the identity provider admits shares this key's access and budgets.
								</p>
								<VirtualKeySelector value={virtualKeyID} onChange={setVirtualKeyID} />
							</>
						)}
					</div>
				</div>
				<DialogFooter>
					<Button variant="outline" onClick={() => onOpenChange(false)}>
						Cancel
					</Button>
					<Button onClick={submit} disabled={isLoading} data-testid="managed-client-create-submit">
						{isLoading ? "Creating..." : "Create"}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}