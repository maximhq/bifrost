import FullPageLoader from "@/components/fullPageLoader";
import { PIN_SHADOW_RIGHT } from "@/components/table/columnPinning";
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
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
	getErrorMessage,
	useDeleteWebhookEndpointMutation,
	useGetWebhookEndpointsQuery,
	useRotateWebhookEndpointSecretMutation,
	useTestWebhookEndpointMutation,
	useUpdateWebhookEndpointMutation,
} from "@/lib/store";
import {
	WEBHOOK_EVENT_COLORS,
	WEBHOOK_TEST_COOLDOWN_SECONDS,
	WebhookEndpoint,
	WebhookEndpointRequest,
	WebhookEvent,
} from "@/lib/types/webhooks";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { MoreHorizontal, PencilIcon, Plus, RotateCcw, Search, Trash2, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { WebhookSecretDialog, WebhookSecretReveal } from "../dialogs/webhookSecretDialog";
import { WebhookDetailsSheet } from "./webhookDetailsSheet";
import { WebhookSheet } from "./webhookSheet";
import { WebhooksEmptyState } from "./webhooksEmptyState";

const POLLING_INTERVAL = 5000;

// PUT replaces the endpoint's full editable state, so toggles resend the row
// as-is. Redacted header values round-trip untouched and the server keeps the
// stored credentials.
const toRequest = (endpoint: WebhookEndpoint, overrides: Partial<WebhookEndpointRequest>): WebhookEndpointRequest => ({
	name: endpoint.name,
	url: endpoint.url,
	events: endpoint.events,
	headers: endpoint.headers ?? {},
	include_response: endpoint.include_response,
	allow_private_network: endpoint.allow_private_network,
	disabled: endpoint.disabled,
	max_retries: endpoint.max_retries ?? 0,
	retry_backoff_initial_seconds: endpoint.retry_backoff_initial_seconds ?? 0,
	retry_backoff_max_seconds: endpoint.retry_backoff_max_seconds ?? 0,
	attempt_timeout_seconds: endpoint.attempt_timeout_seconds ?? 0,
	max_response_payload_kbs: endpoint.max_response_payload_kbs ?? 0,
	max_concurrent_deliveries: endpoint.max_concurrent_deliveries ?? 0,
	...overrides,
});

function WebhookActionsMenu({
	endpoint,
	hasUpdateAccess,
	hasDeleteAccess,
	onEdit,
	onRotate,
	onDelete,
}: {
	endpoint: WebhookEndpoint;
	hasUpdateAccess: boolean;
	hasDeleteAccess: boolean;
	onEdit: (endpoint: WebhookEndpoint) => void;
	onRotate: (endpoint: WebhookEndpoint) => void;
	onDelete: (endpoint: WebhookEndpoint) => void;
}) {
	const [isOpen, setIsOpen] = useState(false);

	return (
		<DropdownMenu open={isOpen} onOpenChange={setIsOpen}>
			<DropdownMenuTrigger asChild>
				<Button
					variant="ghost"
					size="icon"
					className="h-8 w-8"
					aria-label="Webhook actions"
					data-testid={`webhook-actions-btn-${endpoint.name}`}
				>
					<MoreHorizontal className="h-4 w-4" />
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent
				align="end"
				onCloseAutoFocus={(e) => {
					// Edit opens a Sheet; letting the dropdown restore focus to its
					// trigger fights the Sheet's autofocus, so hand focus off instead.
					e.preventDefault();
				}}
			>
				{hasUpdateAccess && (
					<DropdownMenuItem
						className="cursor-pointer"
						data-testid={`webhook-edit-btn-${endpoint.name}`}
						onSelect={(e) => {
							e.preventDefault();
							setIsOpen(false);
							onEdit(endpoint);
						}}
					>
						<PencilIcon className="h-4 w-4" /> Edit
					</DropdownMenuItem>
				)}
				{hasUpdateAccess && (
					<DropdownMenuItem
						className="cursor-pointer"
						data-testid={`webhook-rotate-btn-${endpoint.name}`}
						onSelect={(e) => {
							e.preventDefault();
							setIsOpen(false);
							onRotate(endpoint);
						}}
					>
						<RotateCcw className="h-4 w-4" /> Rotate secret
					</DropdownMenuItem>
				)}
				{hasDeleteAccess && (
					<DropdownMenuItem
						variant="destructive"
						className="cursor-pointer"
						data-testid={`webhook-delete-btn-${endpoint.name}`}
						onSelect={(e) => {
							e.preventDefault();
							setIsOpen(false);
							onDelete(endpoint);
						}}
					>
						<Trash2 className="h-4 w-4" /> Delete
					</DropdownMenuItem>
				)}
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

export default function WebhooksView() {
	const hasCreateAccess = useRbac(RbacResource.Governance, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.Governance, RbacOperation.Update);
	const hasDeleteAccess = useRbac(RbacResource.Governance, RbacOperation.Delete);

	const { data, isLoading, isError, error, refetch } = useGetWebhookEndpointsQuery(undefined, { pollingInterval: POLLING_INTERVAL });
	const [updateWebhookEndpoint] = useUpdateWebhookEndpointMutation();
	const [deleteWebhookEndpoint, { isLoading: isDeleting }] = useDeleteWebhookEndpointMutation();
	const [rotateWebhookEndpointSecret, { isLoading: isRotating }] = useRotateWebhookEndpointSecretMutation();
	const [testWebhookEndpoint] = useTestWebhookEndpointMutation();

	const [search, setSearch] = useState("");
	const [sheetOpen, setSheetOpen] = useState(false);
	const [editingEndpoint, setEditingEndpoint] = useState<WebhookEndpoint | null>(null);
	const [detailsEndpoint, setDetailsEndpoint] = useState<WebhookEndpoint | null>(null);
	const [deleteTarget, setDeleteTarget] = useState<WebhookEndpoint | null>(null);
	const [rotateTarget, setRotateTarget] = useState<WebhookEndpoint | null>(null);
	const [secretReveal, setSecretReveal] = useState<WebhookSecretReveal | null>(null);
	const [togglingIds, setTogglingIds] = useState<Set<string>>(new Set());
	const [testingIds, setTestingIds] = useState<Set<string>>(new Set());

	const endpoints = useMemo(() => data?.endpoints ?? [], [data]);
	const filteredEndpoints = useMemo(() => {
		const query = search.trim().toLowerCase();
		if (!query) return endpoints;
		return endpoints.filter((e) => e.name.toLowerCase().includes(query) || e.url.toLowerCase().includes(query));
	}, [endpoints, search]);

	// Row data refreshes every poll; keep the open details sheet in sync.
	const liveDetailsEndpoint = useMemo(
		() => (detailsEndpoint ? (endpoints.find((e) => e.id === detailsEndpoint.id) ?? detailsEndpoint) : null),
		[detailsEndpoint, endpoints],
	);

	const handleAdd = () => {
		setEditingEndpoint(null);
		setSheetOpen(true);
	};

	const handleEdit = (endpoint: WebhookEndpoint) => {
		setEditingEndpoint(endpoint);
		setSheetOpen(true);
	};

	const handleToggle = async (endpoint: WebhookEndpoint, enabled: boolean) => {
		setTogglingIds((prev) => new Set(prev).add(endpoint.id));
		try {
			await updateWebhookEndpoint({ id: endpoint.id, data: toRequest(endpoint, { disabled: !enabled }) }).unwrap();
			toast.success(`Endpoint ${enabled ? "enabled" : "disabled"} successfully`);
		} catch (err) {
			toast.error(getErrorMessage(err));
		} finally {
			setTogglingIds((prev) => {
				const next = new Set(prev);
				next.delete(endpoint.id);
				return next;
			});
		}
	};

	const handleTest = async (endpoint: WebhookEndpoint, event: WebhookEvent) => {
		setTestingIds((prev) => new Set(prev).add(endpoint.id));
		try {
			const result = await testWebhookEndpoint({ id: endpoint.id, event }).unwrap();
			if (result.delivered) {
				toast.success(`Test delivered, receiver answered ${result.receiver_status_code}`);
			} else if (result.error) {
				toast.error(`Test failed: ${result.error}`);
			} else {
				toast.error(`Test rejected, receiver answered ${result.receiver_status_code}`);
			}
		} catch (err) {
			toast.error(getErrorMessage(err));
		} finally {
			setTestingIds((prev) => {
				const next = new Set(prev);
				next.delete(endpoint.id);
				return next;
			});
		}
	};

	const handleDelete = async () => {
		if (!deleteTarget) return;
		try {
			await deleteWebhookEndpoint(deleteTarget.id).unwrap();
			toast.success("Webhook endpoint deleted successfully");
			if (detailsEndpoint?.id === deleteTarget.id) setDetailsEndpoint(null);
		} catch (err) {
			toast.error(getErrorMessage(err));
		} finally {
			setDeleteTarget(null);
		}
	};

	const handleRotate = async () => {
		if (!rotateTarget) return;
		try {
			const response = await rotateWebhookEndpointSecret(rotateTarget.id).unwrap();
			toast.success("Signing secret rotated successfully");
			setSecretReveal({ endpointName: response.endpoint.name, secret: response.secret });
		} catch (err) {
			toast.error(getErrorMessage(err));
		} finally {
			setRotateTarget(null);
		}
	};

	if (isLoading) {
		return <FullPageLoader />;
	}

	// A failed load must not masquerade as an empty workspace; the empty state
	// invites creating an endpoint, so a hidden fetch error could prompt a
	// duplicate. Only fatal when there is no data to fall back on; a transient
	// poll failure over cached rows keeps rendering the list.
	if (isError && !data) {
		return (
			<div className="flex flex-col items-center justify-center gap-3 py-16 text-center" data-testid="webhooks-load-error">
				<p className="text-sm font-medium">Failed to load webhook endpoints.</p>
				<p className="text-muted-foreground max-w-md text-sm">{getErrorMessage(error)}</p>
				<Button variant="outline" size="sm" onClick={() => void refetch()}>
					Retry
				</Button>
			</div>
		);
	}

	return (
		<div className="w-full space-y-4">
			{endpoints.length === 0 ? (
				<WebhooksEmptyState onAddClick={handleAdd} canCreate={hasCreateAccess} />
			) : (
				<>
					<div className="flex items-center justify-between">
						<div>
							<h2 className="text-lg font-semibold tracking-tight">Webhooks</h2>
							<p className="text-muted-foreground text-sm">
								Register endpoints to receive signed notifications when async inference jobs complete or fail. Pass the endpoint's name in
								the <code>x-bf-async-webhook</code> header when submitting a job.
							</p>
						</div>
						<Button onClick={handleAdd} disabled={!hasCreateAccess} data-testid="create-webhook-btn">
							<Plus className="h-4 w-4" />
							Add Endpoint
						</Button>
					</div>

					<div className="relative max-w-sm">
						<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
						<Input
							placeholder="Search by name or URL"
							value={search}
							onChange={(e) => setSearch(e.target.value)}
							className="pr-8 pl-9"
							data-testid="webhook-search-input"
						/>
						{search && (
							<Button
								variant="ghost"
								size="icon"
								className="absolute top-1/2 right-1 h-6 w-6 -translate-y-1/2"
								onClick={() => setSearch("")}
								aria-label="Clear search"
							>
								<X className="h-3 w-3" />
							</Button>
						)}
					</div>

					<div className="overflow-auto rounded-sm border">
						<Table data-testid="webhooks-table">
							<TableHeader className="bg-muted sticky top-0 z-10">
								<TableRow>
									<TableHead>Name</TableHead>
									<TableHead>URL</TableHead>
									<TableHead>Events</TableHead>
									<TableHead>Enabled</TableHead>
									<TableHead className={`bg-muted/50 sticky right-0 z-10 w-14 text-right ${PIN_SHADOW_RIGHT}`}></TableHead>
								</TableRow>
							</TableHeader>
							<TableBody>
								{filteredEndpoints.length === 0 ? (
									<TableRow>
										<TableCell colSpan={5} className="text-muted-foreground h-24 text-center">
											No matching webhook endpoints found.
										</TableCell>
									</TableRow>
								) : (
									filteredEndpoints.map((endpoint) => (
										<TableRow
											key={endpoint.id}
											className="group cursor-pointer"
											onClick={() => setDetailsEndpoint(endpoint)}
											data-testid={`webhook-row-${endpoint.name}`}
										>
											<TableCell className="font-medium">{endpoint.name}</TableCell>
											<TableCell>
												<code className="block max-w-[320px] truncate font-mono text-xs">{endpoint.url}</code>
											</TableCell>
											<TableCell>
												<div className="flex flex-wrap gap-1">
													{endpoint.events.map((event) => (
														<Badge key={event} variant="outline" className={`font-mono text-xs ${WEBHOOK_EVENT_COLORS[event]}`}>
															{event}
														</Badge>
													))}
												</div>
											</TableCell>
											<TableCell onClick={(e) => e.stopPropagation()}>
												<Switch
													checked={!endpoint.disabled}
													disabled={!hasUpdateAccess || togglingIds.has(endpoint.id)}
													onCheckedChange={(checked) => void handleToggle(endpoint, checked)}
													data-testid={`webhook-enabled-switch-${endpoint.name}`}
												/>
											</TableCell>
											<TableCell
												className={`bg-card group-hover:bg-muted/50 sticky right-0 z-10 text-right ${PIN_SHADOW_RIGHT}`}
												onClick={(e) => e.stopPropagation()}
											>
												<WebhookActionsMenu
													endpoint={endpoint}
													hasUpdateAccess={hasUpdateAccess}
													hasDeleteAccess={hasDeleteAccess}
													onEdit={handleEdit}
													onRotate={setRotateTarget}
													onDelete={setDeleteTarget}
												/>
											</TableCell>
										</TableRow>
									))
								)}
							</TableBody>
						</Table>
					</div>
				</>
			)}

			<WebhookSheet
				open={sheetOpen}
				endpoint={editingEndpoint}
				onClose={() => {
					setSheetOpen(false);
					setEditingEndpoint(null);
				}}
				onSecret={setSecretReveal}
			/>

			<WebhookDetailsSheet
				endpoint={liveDetailsEndpoint}
				isTesting={liveDetailsEndpoint ? testingIds.has(liveDetailsEndpoint.id) : false}
				canManage={hasUpdateAccess}
				onTest={(e, event) => void handleTest(e, event)}
				onClose={() => setDetailsEndpoint(null)}
			/>

			<WebhookSecretDialog reveal={secretReveal} onClose={() => setSecretReveal(null)} />

			<AlertDialog open={!!deleteTarget} onOpenChange={(open) => !open && setDeleteTarget(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete webhook endpoint</AlertDialogTitle>
						<AlertDialogDescription>
							Are you sure you want to delete <b>{deleteTarget?.name}</b>? Pending deliveries to it will be dropped and jobs referencing it
							will be rejected. This action cannot be undone.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel data-testid="webhook-delete-cancel-btn">Cancel</AlertDialogCancel>
						<AlertDialogAction
							className="bg-destructive hover:bg-destructive/90"
							onClick={() => void handleDelete()}
							disabled={isDeleting}
							data-testid="webhook-delete-confirm-btn"
						>
							Delete
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>

			<AlertDialog open={!!rotateTarget} onOpenChange={(open) => !open && setRotateTarget(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Rotate signing secret</AlertDialogTitle>
						<AlertDialogDescription>
							The current secret for <b>{rotateTarget?.name}</b> stops working immediately and deliveries are signed with the new one from
							now on. Update your receiver right after rotating.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel data-testid="webhook-rotate-cancel-btn">Cancel</AlertDialogCancel>
						<AlertDialogAction onClick={() => void handleRotate()} disabled={isRotating} data-testid="webhook-rotate-confirm-btn">
							Rotate
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</div>
	);
}