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
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
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
import { isHTTPSOrLoopbackURL } from "@/lib/types/schemas";
import { formatRelativePast } from "@/lib/utils/mcpCredential";
import { Copy, MoreHorizontal, RotateCcw, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "sonner";

export const DEFAULT_MANAGED_AUTH_TTL = 86400;
const MIN_MANAGED_AUTH_TTL = 60;
const MAX_MANAGED_AUTH_TTL = 86400;

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
		if (url && !isHTTPSOrLoopbackURL(url)) {
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
		(a?.provision_users ?? false) === (b?.provision_users ?? false)
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
	const { data, isLoading, isError, error } = useGetOAuth2ManagedClientsQuery();
	const [ttlText, setTTLText] = useState(String(value?.access_token_ttl || DEFAULT_MANAGED_AUTH_TTL));
	// Re-sync only when the text no longer stands for the staged value, so an
	// emptied field (staged as 0, the backend default) is left alone.
	useEffect(() => {
		const staged = value?.access_token_ttl ?? 0;
		setTTLText((text) => {
			const shown = Number.parseInt(text);
			return (isNaN(shown) ? 0 : shown) === staged ? text : String(staged || DEFAULT_MANAGED_AUTH_TTL);
		});
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
									For clients whose tokens act as the signed-in user: when an assertion names someone with no Bifrost user yet, create them
									instead of refusing them. Off by default. The assertion carries only the user's ID and email, so the new user gets a role
									only if your role mapping matches their identity provider profile; otherwise they are still refused. When off, they need
									to sign in to Bifrost once first.
								</p>
							</div>
							<Switch
								id="managed-auth-provision-users"
								data-testid="managed-auth-provision-users-switch"
								size="md"
								checked={!!value?.provision_users}
								onCheckedChange={(on) => update({ provision_users: on || undefined })}
								disabled={!canEdit}
							/>
						</div>
					)}
					<Alert>
						<AlertTitle>Identity provider setup</AlertTitle>
						<AlertDescription>
							<p>
								In your identity provider, enable Cross App Access on the app that represents Bifrost and set its issuer URL to{" "}
								<code className="text-xs">{issuerURLHint}</code>. In the requesting app's resource connection, enter the client ID of a
								managed client below.
							</p>
						</AlertDescription>
					</Alert>
					{isLoading ? (
						<p className="text-muted-foreground text-sm" data-testid="managed-clients-loading">
							Loading managed clients…
						</p>
					) : isError ? (
						<Alert variant="destructive" data-testid="managed-clients-error">
							<AlertTitle>Could not load managed clients</AlertTitle>
							<AlertDescription>{getErrorMessage(error)}</AlertDescription>
						</Alert>
					) : (
						<ManagedClients
							canEdit={canEdit}
							userIdentityAvailable={data?.user_identity_available ?? false}
							clients={data?.clients ?? []}
						/>
					)}
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
	const [pendingRotate, setPendingRotate] = useState<OAuth2ManagedClient | null>(null);
	const [updateClient] = useUpdateOAuth2ManagedClientMutation();
	const [rotateSecret, { isLoading: rotating }] = useRotateOAuth2ManagedClientSecretMutation();
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
				<div className="overflow-auto rounded-sm border">
					<Table data-testid="managed-clients-table">
						<TableHeader className="bg-muted">
							<TableRow>
								<TableHead>Name</TableHead>
								<TableHead>Client ID</TableHead>
								<TableHead>Acts as</TableHead>
								<TableHead>Secret</TableHead>
								<TableHead>Last used</TableHead>
								<TableHead>Enabled</TableHead>
								<TableHead className="w-14" />
							</TableRow>
						</TableHeader>
						<TableBody>
							{clients.map((client) => (
								<TableRow key={client.id} data-testid={`managed-client-row-${client.id}`}>
									<TableCell className="font-medium">{client.name || "Unnamed client"}</TableCell>
									<TableCell>
										<div className="flex items-center gap-1">
											<code className="block max-w-[260px] truncate font-mono text-xs">{client.client_id}</code>
											<Button
												variant="ghost"
												size="icon"
												className="h-6 w-6 shrink-0"
												aria-label="Copy client ID"
												onClick={() => copyClientID(client.client_id)}
											>
												<Copy className="h-3 w-3" />
											</Button>
										</div>
									</TableCell>
									<TableCell className="text-sm">{client.identity === "user" ? "The signed-in user" : "Bound virtual key"}</TableCell>
									<TableCell className="text-muted-foreground text-sm">{client.has_secret ? "Required" : "None (public)"}</TableCell>
									<TableCell className="text-muted-foreground text-sm">
										{client.last_used_at ? formatRelativePast(client.last_used_at) : "Never"}
									</TableCell>
									<TableCell>
										<Switch
											aria-label="Client enabled"
											checked={!client.disabled}
											disabled={!canEdit}
											data-testid={`managed-client-enabled-switch-${client.id}`}
											onCheckedChange={(enabled) =>
												run(
													() => updateClient({ id: client.id, disabled: !enabled }).unwrap(),
													enabled ? "Client enabled." : "Client disabled.",
												)
											}
										/>
									</TableCell>
									<TableCell className="text-right">
										<ManagedClientActionsMenu
											client={client}
											disabled={!canEdit}
											onRotate={() => setPendingRotate(client)}
											onDelete={() => setPendingDelete(client)}
										/>
									</TableCell>
								</TableRow>
							))}
						</TableBody>
					</Table>
				</div>
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

			<AlertDialog open={!!pendingRotate} onOpenChange={(open) => !open && setPendingRotate(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Rotate client secret?</AlertDialogTitle>
						<AlertDialogDescription>
							The current secret stops working immediately. Update it in your identity provider's resource connection and in the MCP
							client's connector settings, or the client can no longer connect.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction
							disabled={rotating}
							onClick={() => {
								const target = pendingRotate;
								setPendingRotate(null);
								if (target) void run(async () => setRevealed(await rotateSecret(target.id).unwrap()), "New client secret generated.");
							}}
						>
							Rotate
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>

			<AlertDialog open={!!pendingDelete} onOpenChange={(open) => !open && setPendingDelete(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete managed client?</AlertDialogTitle>
						<AlertDialogDescription>
							The client can no longer exchange identity assertions, and the tokens it already received stop working within 30 seconds.
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

// ManagedClientActionsMenu holds a managed client's row actions, as other tables do.
function ManagedClientActionsMenu({
	client,
	disabled,
	onRotate,
	onDelete,
}: {
	client: OAuth2ManagedClient;
	disabled: boolean;
	onRotate: () => void;
	onDelete: () => void;
}) {
	const [isOpen, setIsOpen] = useState(false);
	// Close the menu before opening a dialog from it, so the two never hold focus at once.
	const select = (action: () => void) => (e: Event) => {
		e.preventDefault();
		setIsOpen(false);
		action();
	};
	return (
		<DropdownMenu open={isOpen} onOpenChange={setIsOpen}>
			<DropdownMenuTrigger asChild>
				<Button
					variant="ghost"
					size="icon"
					className="h-8 w-8"
					disabled={disabled}
					aria-label="Managed client actions"
					data-testid={`managed-client-actions-btn-${client.id}`}
				>
					<MoreHorizontal className="h-4 w-4" />
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end" onCloseAutoFocus={(e) => e.preventDefault()}>
				{client.has_secret && (
					<DropdownMenuItem className="cursor-pointer" onSelect={select(onRotate)} data-testid={`managed-client-rotate-btn-${client.id}`}>
						<RotateCcw className="h-4 w-4" /> Rotate secret
					</DropdownMenuItem>
				)}
				<DropdownMenuItem
					variant="destructive"
					className="cursor-pointer"
					onSelect={select(onDelete)}
					data-testid={`managed-client-delete-btn-${client.id}`}
				>
					<Trash2 className="h-4 w-4" /> Delete
				</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
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
				<Button variant="outline" size="icon" aria-label={`Copy ${label}`} onClick={() => copy(value)}>
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
							Leave empty to generate one, or enter the identifier the client uses for itself.
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
							<p className="text-muted-foreground text-xs">Turn off for public clients that cannot keep a secret.</p>
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