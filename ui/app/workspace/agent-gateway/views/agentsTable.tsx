import { getExternalBaseUrl } from "@/app/workspace/mcp-registry/views/mcpUsageGuide/utils";
import PageTitle from "@/components/pageTitle";
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
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useToast } from "@/hooks/use-toast";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { getErrorMessage, useGetCoreConfigQuery } from "@/lib/store";
import { useDeleteAgentMutation, useUpdateAgentMutation } from "@/lib/store/apis/agentsApi";
import type { AgentRegistrationView } from "@/lib/types/agents";
import AgentAccessSummary from "@enterprise/components/agent-gateway/agentAccessSummary";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { formatDistanceToNow } from "date-fns";
import { Check, ChevronLeft, ChevronRight, Copy, Info, MoreHorizontal, PencilIcon, Plus, Search, Trash2 } from "lucide-react";
import { useState, type ReactNode } from "react";
import AgentSheet from "./agentSheet";
import { AgentsEmptyState } from "./agentsEmptyState";

function AgentActionsMenu({
	agent,
	hasUpdateAccess,
	hasDeleteAccess,
	onEdit,
	onDelete,
}: {
	agent: AgentRegistrationView;
	hasUpdateAccess: boolean;
	hasDeleteAccess: boolean;
	onEdit: (agent: AgentRegistrationView) => void;
	onDelete: (agent: AgentRegistrationView) => void;
}) {
	const [isOpen, setIsOpen] = useState(false);
	return (
		<DropdownMenu open={isOpen} onOpenChange={setIsOpen}>
			<DropdownMenuTrigger asChild>
				<Button variant="ghost" size="icon" className="h-8 w-8" aria-label="Agent actions" data-testid={`agent-actions-${agent.name}-btn`}>
					<MoreHorizontal className="h-4 w-4" />
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent
				align="end"
				onCloseAutoFocus={(e) => {
					// Edit opens a Sheet; skipping the dropdown's focus restore hands
					// focus to the Sheet so ESC-to-close keeps working (same as MCP).
					e.preventDefault();
				}}
			>
				{hasUpdateAccess && (
					<DropdownMenuItem
						className="cursor-pointer"
						data-testid={`agent-edit-${agent.name}-menu-item`}
						onSelect={(e) => {
							e.preventDefault();
							onEdit(agent);
							setIsOpen(false);
						}}
					>
						<PencilIcon className="h-4 w-4" />
						Edit
					</DropdownMenuItem>
				)}
				{hasDeleteAccess && (
					<DropdownMenuItem
						variant="destructive"
						className="cursor-pointer"
						data-testid={`agent-delete-${agent.name}-menu-item`}
						onSelect={(e) => {
							e.preventDefault();
							onDelete(agent);
							setIsOpen(false);
						}}
					>
						<Trash2 className="h-4 w-4" />
						Delete
					</DropdownMenuItem>
				)}
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

// AgentEndpointCell copies the gateway-hosted public Agent Card URL.
function AgentEndpointCell({ name, baseUrl }: { name: string; baseUrl: string }) {
	const { copy, copied } = useCopyToClipboard({ successMessage: "Agent Card URL copied" });
	const agentCardUrl = `${baseUrl}/agents/a2a/${name}/.well-known/agent-card.json`;

	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<button
					type="button"
					className="text-muted-foreground hover:text-foreground flex w-full min-w-0 cursor-pointer items-center gap-1.5 font-mono text-sm transition-colors"
					aria-label="Copy Agent Card URL"
					data-testid={`agent-endpoint-copy-${name}`}
					onClick={(event) => {
						event.stopPropagation();
						copy(agentCardUrl);
					}}
				>
					<span className="truncate">/{name}</span>
					<span className="shrink-0 opacity-0 transition-opacity group-hover:opacity-100">
						{copied ? <Check className="size-3.5 shrink-0" /> : <Copy className="size-3.5 shrink-0" />}
					</span>
				</button>
			</TooltipTrigger>
			<TooltipContent className="font-mono">{agentCardUrl}</TooltipContent>
		</Tooltip>
	);
}

function formatAgentAccess(agent: AgentRegistrationView) {
	if (agent.allow_by_default) return "Allowed by default";

	const virtualKeyCount = agent.virtual_key_ids?.length ?? 0;
	return virtualKeyCount > 0 ? `${virtualKeyCount} ${virtualKeyCount === 1 ? "VK" : "VKs"}` : "None";
}

function HeaderWithTooltip({ label, tooltip, testId }: { label: string; tooltip: ReactNode; testId?: string }) {
	return (
		<Popover>
			<PopoverTrigger asChild>
				<button
					type="button"
					aria-label={`${label} column guidance`}
					data-testid={testId}
					className="inline-flex cursor-help items-center gap-2"
				>
					{label}
					<Info className="text-muted-foreground size-3" />
				</button>
			</PopoverTrigger>
			<PopoverContent className="w-xs text-xs" align="start">
				{tooltip}
			</PopoverContent>
		</Popover>
	);
}

interface AgentsTableProps {
	agents: AgentRegistrationView[];
	/** Unfiltered registration count, used to distinguish the onboarding empty state. */
	totalAgents: number;
	/** Filtered count driving pagination. */
	totalCount: number;
	refetch?: () => void;
	search: string;
	debouncedSearch: string;
	/** Whether any sidebar facet filter (status/upstream auth/access) is active. */
	filtersActive?: boolean;
	onSearchChange: (value: string) => void;
	offset: number;
	limit: number;
	onOffsetChange: (offset: number) => void;
}

export default function AgentsTable({
	agents,
	totalAgents,
	totalCount,
	refetch,
	search,
	debouncedSearch,
	filtersActive = false,
	onSearchChange,
	offset,
	limit,
	onOffsetChange,
}: AgentsTableProps) {
	const hasCreateAccess = useRbac(RbacResource.AgentGateway, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.AgentGateway, RbacOperation.Update);
	const hasDeleteAccess = useRbac(RbacResource.AgentGateway, RbacOperation.Delete);

	const [sheetOpen, setSheetOpen] = useState(false);
	const [selectedAgent, setSelectedAgent] = useState<AgentRegistrationView | null>(null);
	const [agentToDelete, setAgentToDelete] = useState<AgentRegistrationView | null>(null);
	const [togglingAgents, setTogglingAgents] = useState<Set<string>>(new Set());
	const [deleteAgent] = useDeleteAgentMutation();
	const [updateAgent] = useUpdateAgentMutation();
	const { toast } = useToast();

	// Externally reachable base URL, so the endpoint cell copies the full URL callers use.
	const { data: coreConfig } = useGetCoreConfigQuery({ fromDB: true });
	const baseUrl = getExternalBaseUrl(coreConfig?.client_config);

	const handleCreate = () => {
		setSelectedAgent(null);
		setSheetOpen(true);
	};

	const handleEdit = (agent: AgentRegistrationView) => {
		setSelectedAgent(agent);
		setSheetOpen(true);
	};

	const handleSaved = () => {
		setSheetOpen(false);
		setSelectedAgent(null);
		refetch?.();
	};

	const handleDelete = async (agent: AgentRegistrationView) => {
		try {
			await deleteAgent(agent.name).unwrap();
			toast({ title: "Deleted", description: `Agent ${agent.name} removed successfully.` });
			refetch?.();
		} catch (error) {
			toast({ title: "Error", description: getErrorMessage(error), variant: "destructive" });
		}
	};

	// PUT is a full replacement, so the toggle resubmits every field — including
	// virtual_key_ids — with only `enabled` changed. Masked credential secrets
	// round-trip verbatim and the backend keeps the stored values.
	const handleToggleEnabled = async (agent: AgentRegistrationView, enabled: boolean) => {
		setTogglingAgents((prev) => new Set(prev).add(agent.name));
		try {
			await updateAgent({
				name: agent.name,
				data: {
					agent_card_url: agent.agent_card_url,
					tenant: agent.tenant,
					enabled,
					allow_by_default: agent.allow_by_default,
					forward_accepted_credential: agent.forward_accepted_credential,
					forward_accepted_credential_overrides_auth: agent.forward_accepted_credential_overrides_auth,
					discovery_auth: agent.discovery_auth,
					runtime_auth: agent.runtime_auth,
					virtual_key_ids: agent.virtual_key_ids ?? [],
					extension_uris: agent.extension_uris ?? [],
				},
			}).unwrap();
			toast({ title: `Agent ${enabled ? "enabled" : "disabled"} successfully` });
			refetch?.();
		} catch (error) {
			toast({ title: "Error", description: getErrorMessage(error), variant: "destructive" });
		} finally {
			setTogglingAgents((prev) => {
				const next = new Set(prev);
				next.delete(agent.name);
				return next;
			});
		}
	};

	// Rendered on the empty branch too so the topbar keeps its title.
	const pageTitle = <PageTitle title="Agent Catalog">Manage upstream A2A agents served through the gateway.</PageTitle>;

	const sheet = sheetOpen && <AgentSheet agent={selectedAgent ?? undefined} onClose={() => setSheetOpen(false)} onSaved={handleSaved} />;

	const hasActiveFilters = Boolean(debouncedSearch) || filtersActive;

	// True empty state: no agents at all (not just filtered to zero).
	if (totalAgents === 0 && !hasActiveFilters) {
		return (
			<>
				{pageTitle}
				{sheet}
				<AgentsEmptyState onAddClick={handleCreate} canCreate={hasCreateAccess} />
			</>
		);
	}

	return (
		<div className="flex grow flex-col overflow-auto">
			{sheet}
			<AlertDialog open={!!agentToDelete} onOpenChange={(open) => !open && setAgentToDelete(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Remove Agent</AlertDialogTitle>
						<AlertDialogDescription>
							Are you sure you want to remove agent {agentToDelete?.name}? Its gateway endpoints stop resolving and its virtual key grants
							are removed.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction
							onClick={() => {
								if (agentToDelete) void handleDelete(agentToDelete);
							}}
							className="bg-destructive hover:bg-destructive/90"
						>
							Delete
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>

			{/* Toolbar: Search + Actions */}
			<div className="mb-4 flex flex-wrap items-center gap-3">
				{pageTitle}
				<div className="relative max-w-sm flex-1">
					<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
					<Input
						aria-label="Search agents by name"
						placeholder="Search by name..."
						value={search}
						onChange={(e) => onSearchChange(e.target.value)}
						className="pl-9"
						data-testid="agents-search-input"
					/>
				</div>
				<div className="flex gap-2 sm:ml-auto">
					<Button
						onClick={handleCreate}
						disabled={!hasCreateAccess}
						data-testid="create-agent-btn"
						aria-label="New Agent"
						className="h-8 gap-2"
					>
						<Plus />
						<span className="hidden sm:inline">New Agent</span>
					</Button>
				</div>
			</div>

			<div className="flex grow flex-col overflow-hidden">
				<div className="mb-2 grow overflow-hidden rounded-sm border">
					<Table data-testid="agents-table" containerClassName="h-full overflow-auto" className="w-full min-w-[1146px] table-fixed">
						<TableHeader className="bg-muted sticky top-0 z-20">
							<TableRow>
								<TableHead className="w-[200px] font-semibold">Name</TableHead>
								<TableHead className="w-[200px] font-semibold">Endpoint</TableHead>
								<TableHead className="w-[300px] font-semibold">Agent Card URL</TableHead>
								<TableHead className="w-[160px] font-semibold">
									<HeaderWithTooltip
										label="Access"
										testId="agent-access-info-trigger"
										tooltip={
											<p>
												Which callers may reach this agent. "Allowed by default" lets any valid virtual key through; otherwise only virtual
												keys with direct grants may reach it.
											</p>
										}
									/>
								</TableHead>
								<TableHead className="w-[150px] pr-6 font-semibold">Updated</TableHead>
								<TableHead className="w-[90px] font-semibold">Status</TableHead>
								<TableHead className={`bg-muted sticky right-0 z-10 w-14 text-right ${PIN_SHADOW_RIGHT}`}></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{agents.length === 0 ? (
								<TableRow>
									<TableCell colSpan={7} className="h-24 text-center">
										<span className="text-muted-foreground text-sm">No matching agents found.</span>
									</TableCell>
								</TableRow>
							) : (
								agents.map((agent) => (
									<TableRow
										key={agent.name}
										className="group hover:bg-muted/50 cursor-pointer transition-colors"
										onClick={() => handleEdit(agent)}
									>
										<TableCell className="font-medium">
											<div className="truncate" title={agent.name}>
												{agent.name}
											</div>
										</TableCell>
										<TableCell onClick={(e) => e.stopPropagation()}>
											<AgentEndpointCell name={agent.name} baseUrl={baseUrl} />
										</TableCell>
										<TableCell>
											<div className="text-muted-foreground truncate font-mono text-sm" title={agent.agent_card_url}>
												{agent.agent_card_url}
											</div>
										</TableCell>
										<TableCell data-testid={`agent-access-${agent.name}`}>
											<AgentAccessSummary agent={agent} fallback={formatAgentAccess(agent)} />
										</TableCell>
										<TableCell className="text-muted-foreground pr-6 text-sm">
											{formatDistanceToNow(new Date(agent.updated_at), { addSuffix: true })}
										</TableCell>
										<TableCell data-testid={`agent-status-${agent.name}`} onClick={(e) => e.stopPropagation()}>
											<Switch
												data-testid={`agent-enabled-switch-${agent.name}`}
												checked={agent.enabled}
												size="md"
												disabled={!hasUpdateAccess || togglingAgents.has(agent.name)}
												onAsyncCheckedChange={(checked) => handleToggleEnabled(agent, checked)}
											/>
										</TableCell>
										<TableCell
											className={`bg-card group-hover:bg-muted/50 sticky right-0 z-10 text-right ${PIN_SHADOW_RIGHT}`}
											onClick={(e) => e.stopPropagation()}
										>
											<AgentActionsMenu
												agent={agent}
												hasUpdateAccess={hasUpdateAccess}
												hasDeleteAccess={hasDeleteAccess}
												onEdit={handleEdit}
												onDelete={setAgentToDelete}
											/>
										</TableCell>
									</TableRow>
								))
							)}
						</TableBody>
					</Table>
				</div>

				{/* Pagination */}
				{totalCount > 0 && (
					<div className="flex shrink-0 items-center justify-between text-xs" data-testid="pagination">
						<div className="text-muted-foreground flex items-center gap-2">
							{(offset + 1).toLocaleString()}-{Math.min(offset + limit, totalCount).toLocaleString()} of {totalCount.toLocaleString()}{" "}
							entries
						</div>
						<div className="flex items-center gap-2">
							<Button
								variant="ghost"
								size="sm"
								onClick={() => onOffsetChange(Math.max(0, offset - limit))}
								disabled={offset === 0}
								data-testid="agents-pagination-prev-btn"
								aria-label="Previous page"
							>
								<ChevronLeft className="size-3" />
							</Button>
							<div className="flex items-center gap-1">
								<span>Page</span>
								<span>{Math.floor(offset / limit) + 1}</span>
								<span>of {Math.ceil(totalCount / limit)}</span>
							</div>
							<Button
								variant="ghost"
								size="sm"
								onClick={() => onOffsetChange(offset + limit)}
								disabled={offset + limit >= totalCount}
								data-testid="agents-pagination-next-btn"
								aria-label="Next page"
							>
								<ChevronRight className="size-3" />
							</Button>
						</div>
					</div>
				)}
			</div>
		</div>
	);
}