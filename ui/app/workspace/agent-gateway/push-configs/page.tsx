import { FilterSidebarTrigger } from "@/components/filters/filterSidebarTrigger";
import { FilterSection, SearchableCheckboxList, useAutoFocusOnOpen } from "@/components/filters/primitives";
import FullPageLoader from "@/components/fullPageLoader";
import PageTitle from "@/components/pageTitle";
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
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scrollArea";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useIsMobile } from "@/hooks/use-mobile";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { getErrorMessage } from "@/lib/store";
import { useDeleteAgentPushConfigMutation, useGetAgentPushConfigsQuery } from "@/lib/store/apis/agentsApi";
import type { AgentPushConfigView } from "@/lib/types/agents";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { formatDistanceToNow } from "date-fns";
import { ChevronLeft, ChevronRight, PanelLeftClose, RotateCcw, Search, Trash2 } from "lucide-react";
import { parseAsArrayOf, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";

const COLLAPSE_STORAGE_KEY = "agent-push-config-filter-sidebar-collapsed";
const PAGE_SIZE = 25;

interface PushConfigFilters {
	agentNames: string[];
	taskID: string;
	configID: string;
	callbackURL: string;
}

const EMPTY_FILTERS: PushConfigFilters = { agentNames: [], taskID: "", configID: "", callbackURL: "" };

function InteractiveValue({
	value,
	label,
	onFilter,
	className,
}: {
	value: string;
	label: string;
	onFilter: () => void;
	className?: string;
}) {
	const { copy } = useCopyToClipboard({ successMessage: "Copied" });
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<button
					type="button"
					onClick={onFilter}
					onContextMenu={(event) => {
						event.preventDefault();
						copy(value);
					}}
					className={`focus-visible:ring-ring block max-w-full min-w-0 cursor-pointer truncate text-left underline-offset-2 transition hover:underline focus-visible:ring-2 focus-visible:outline-none ${className ?? ""}`}
					aria-label={`Filter by ${label}`}
				>
					{value}
				</button>
			</TooltipTrigger>
			<TooltipContent className="max-w-[480px] break-all">
				<div className="font-mono text-[11px]">{value}</div>
				<div>Click to filter · Right-click to copy</div>
			</TooltipContent>
		</Tooltip>
	);
}

function TextFilterInput({
	label,
	value,
	onChange,
	testId,
	resetKey,
}: {
	label: string;
	value: string;
	onChange: (value: string) => void;
	testId: string;
	resetKey: number;
}) {
	const [localValue, setLocalValue] = useState(value);
	const timeoutRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

	useEffect(() => {
		setLocalValue(value);
		if (timeoutRef.current) clearTimeout(timeoutRef.current);
	}, [value, resetKey]);
	useEffect(() => () => timeoutRef.current && clearTimeout(timeoutRef.current), []);

	return (
		<div className="space-y-1 px-3 py-2">
			<div className="text-muted-foreground text-[11px] font-medium">{label}</div>
			<div className="relative">
				<Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2" />
				<Input
					value={localValue}
					onChange={(event) => {
						const next = event.target.value;
						setLocalValue(next);
						if (timeoutRef.current) clearTimeout(timeoutRef.current);
						timeoutRef.current = setTimeout(() => onChange(next.trim()), 500);
					}}
					placeholder="Contains value"
					className="h-8 pl-8 font-mono text-xs"
					data-testid={testId}
				/>
			</div>
		</div>
	);
}

function PushConfigFilterSidebar({
	filters,
	agentNames,
	onChange,
	onTextChange,
}: {
	filters: PushConfigFilters;
	agentNames: string[];
	onChange: (filters: PushConfigFilters) => void;
	onTextChange: (key: "taskID" | "configID" | "callbackURL", value: string) => void;
}) {
	const isMobile = useIsMobile();
	const [collapsed, setCollapsed] = useState(false);
	const [resetKey, setResetKey] = useState(0);
	const [agentFilterOpen, setAgentFilterOpen] = useState(true);
	const agentSearchInputRef = useAutoFocusOnOpen(agentFilterOpen);

	useEffect(() => {
		if (typeof window === "undefined") return;
		if (isMobile) {
			setCollapsed(true);
			return;
		}
		setCollapsed(window.localStorage.getItem(COLLAPSE_STORAGE_KEY) === "true");
	}, [isMobile]);

	const toggleCollapsed = useCallback(() => {
		setCollapsed((current) => {
			const next = !current;
			window.localStorage.setItem(COLLAPSE_STORAGE_KEY, String(next));
			return next;
		});
	}, []);
	const activeFilterCount =
		filters.agentNames.length + Number(Boolean(filters.taskID)) + Number(Boolean(filters.configID)) + Number(Boolean(filters.callbackURL));

	if (collapsed) {
		return (
			<FilterSidebarTrigger activeFilterCount={activeFilterCount} onClick={toggleCollapsed} testId="pushConfigFilterSidebar-toggle-show" />
		);
	}

	return (
		<div className="bg-card fixed inset-y-2 left-2 z-40 flex h-auto w-[calc(100vw-1rem)] max-w-72 shrink-0 flex-col rounded-md border shadow-xl md:static md:h-full md:w-64 md:max-w-none md:shadow-none">
			<div className="flex h-11 items-center justify-between border-b pr-2 pl-5">
				<span className="text-sm font-semibold">Filters</span>
				<div className="flex items-center gap-1">
					{activeFilterCount > 0 ? (
						<Button
							variant="outline"
							size="sm"
							className="text-muted-foreground h-7 px-2 text-xs"
							onClick={() => {
								setResetKey((current) => current + 1);
								onChange(EMPTY_FILTERS);
							}}
						>
							<RotateCcw className="size-3" /> Reset
						</Button>
					) : null}
					<Button variant="ghost" size="icon" className="size-7" onClick={toggleCollapsed} title="Hide filters" aria-label="Hide filters">
						<PanelLeftClose className="size-4" />
					</Button>
				</div>
			</div>
			<ScrollArea className="flex flex-1 overflow-y-auto p-2 pb-0" viewportClassName="no-table">
				<div className="flex grow flex-col gap-1">
					<FilterSection title="Agent" defaultOpen onOpenChange={setAgentFilterOpen}>
						<SearchableCheckboxList
							inputRef={agentSearchInputRef}
							placeholder="Search agents"
							items={agentNames.map((name) => ({ key: name, label: name }))}
							isSelected={(name) => filters.agentNames.includes(name)}
							onToggle={(name) =>
								onChange({
									...filters,
									agentNames: filters.agentNames.includes(name)
										? filters.agentNames.filter((agentName) => agentName !== name)
										: [...filters.agentNames, name],
								})
							}
							testIdPrefix="push-config-agent-filter"
							normalizeTestIdKey
						/>
					</FilterSection>
					<FilterSection title="Identifiers" defaultOpen={Boolean(filters.taskID || filters.configID)}>
						<TextFilterInput
							label="Task ID"
							value={filters.taskID}
							onChange={(taskID) => onTextChange("taskID", taskID)}
							testId="push-config-task-id-filter"
							resetKey={resetKey}
						/>
						<TextFilterInput
							label="Config ID"
							value={filters.configID}
							onChange={(configID) => onTextChange("configID", configID)}
							testId="push-config-config-id-filter"
							resetKey={resetKey}
						/>
					</FilterSection>
					<FilterSection title="Callback URL" defaultOpen={Boolean(filters.callbackURL)}>
						<TextFilterInput
							label="URL"
							value={filters.callbackURL}
							onChange={(callbackURL) => onTextChange("callbackURL", callbackURL)}
							testId="push-config-url-filter"
							resetKey={resetKey}
						/>
					</FilterSection>
				</div>
			</ScrollArea>
		</div>
	);
}

export default function PushConfigsPage() {
	const hasDeleteAccess = useRbac(RbacResource.AgentGateway, RbacOperation.Delete);
	const [urlState, setUrlState] = useQueryStates(
		{
			agent_names: parseAsArrayOf(parseAsString).withDefault([]),
			task_id: parseAsString.withDefault(""),
			config_id: parseAsString.withDefault(""),
			url: parseAsString.withDefault(""),
			offset: parseAsInteger.withDefault(0),
		},
		{ history: "push" },
	);
	const filters = useMemo<PushConfigFilters>(
		() => ({
			agentNames: urlState.agent_names,
			taskID: urlState.task_id,
			configID: urlState.config_id,
			callbackURL: urlState.url,
		}),
		[urlState.agent_names, urlState.task_id, urlState.config_id, urlState.url],
	);
	const offset = urlState.offset;
	const query = useMemo(
		() => ({
			agent_names: filters.agentNames.length > 0 ? filters.agentNames : undefined,
			task_id: filters.taskID || undefined,
			config_id: filters.configID || undefined,
			url: filters.callbackURL || undefined,
			limit: PAGE_SIZE,
			offset,
		}),
		[filters, offset],
	);
	const { data, currentData, isLoading, isFetching, error } = useGetAgentPushConfigsQuery(query);
	const [deletePushConfig, { isLoading: isDeleting }] = useDeleteAgentPushConfigMutation();
	const [deleteTarget, setDeleteTarget] = useState<AgentPushConfigView | null>(null);
	const configs = currentData?.push_configs ?? [];
	const agentNames = data?.agent_names ?? [];
	const totalCount = currentData?.total_count ?? 0;
	const updateFilters = useCallback(
		(next: PushConfigFilters) => {
			void setUrlState({
				agent_names: next.agentNames,
				task_id: next.taskID,
				config_id: next.configID,
				url: next.callbackURL,
				offset: 0,
			});
		},
		[setUrlState],
	);
	const updateTextFilter = useCallback(
		(key: "taskID" | "configID" | "callbackURL", value: string) => {
			const urlKey = key === "taskID" ? "task_id" : key === "configID" ? "config_id" : "url";
			void setUrlState({ [urlKey]: value, offset: 0 });
		},
		[setUrlState],
	);
	const setOffset = useCallback(
		(nextOffset: number) => {
			void setUrlState({ offset: nextOffset });
		},
		[setUrlState],
	);

	useEffect(() => {
		if (isFetching || !currentData || offset < currentData.total_count) return;
		void setUrlState(
			{ offset: currentData.total_count === 0 ? 0 : Math.floor((currentData.total_count - 1) / PAGE_SIZE) * PAGE_SIZE },
			{ history: "replace" },
		);
	}, [currentData, isFetching, offset, setUrlState]);

	if (isLoading) return <FullPageLoader />;
	const hasFilters = filters.agentNames.length > 0 || Boolean(filters.taskID || filters.configID || filters.callbackURL);

	return (
		<div className="dark:bg-card no-padding-parent no-border-parent h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))]">
			<div className="bg-background flex h-full w-full grow gap-3">
				<PushConfigFilterSidebar filters={filters} agentNames={agentNames} onChange={updateFilters} onTextChange={updateTextFilter} />
				<div className="bg-card h-full w-full overflow-hidden rounded-md border">
					<div className="flex h-full flex-col p-4">
						<div className="mb-4 flex items-center">
							<PageTitle title="Push Configurations">
								View callback configurations registered through A2A and remove stale entries.
							</PageTitle>
						</div>
						{error ? <div className="text-destructive mb-3 text-sm">{getErrorMessage(error)}</div> : null}
						<div className="flex grow flex-col overflow-hidden">
							<div className="mb-2 grow overflow-hidden rounded-sm border">
								<Table containerClassName="h-full overflow-auto" className="w-full min-w-[1140px] table-fixed">
									<TableHeader className="bg-muted sticky top-0 z-20">
										<TableRow>
											<TableHead className="w-[180px] font-semibold">Agent</TableHead>
											<TableHead className="w-[140px] font-semibold">Tenant</TableHead>
											<TableHead className="w-[220px] font-semibold">Task ID</TableHead>
											<TableHead className="w-[220px] font-semibold">Config ID</TableHead>
											<TableHead className="w-[320px] font-semibold">Callback URL</TableHead>
											<TableHead className="w-[150px] font-semibold">Updated</TableHead>
											{hasDeleteAccess ? <TableHead className="w-14" /> : null}
										</TableRow>
									</TableHeader>
									<TableBody>
										{configs.length === 0 ? (
											<TableRow>
												<TableCell colSpan={hasDeleteAccess ? 7 : 6} className="text-muted-foreground h-24 text-center text-sm">
													{hasFilters ? "No push configurations match these filters." : "No push configurations stored."}
												</TableCell>
											</TableRow>
										) : (
											configs.map((config) => (
												<TableRow
													key={`${config.agent_name}:${config.task_id}:${config.config_id}`}
													className="hover:bg-muted/50 transition-colors"
												>
													<TableCell>
														<InteractiveValue
															value={config.agent_name}
															label="Agent"
															onFilter={() => updateFilters({ ...filters, agentNames: [config.agent_name] })}
															className="font-medium"
														/>
													</TableCell>
													<TableCell className="font-mono text-xs" data-testid="push-config-tenant">
														{config.tenant ? config.tenant : <span className="text-muted-foreground">—</span>}
													</TableCell>
													<TableCell>
														<InteractiveValue
															value={config.task_id}
															label="Task ID"
															onFilter={() => updateFilters({ ...filters, taskID: config.task_id })}
															className="font-mono text-xs"
														/>
													</TableCell>
													<TableCell>
														<InteractiveValue
															value={config.config_id}
															label="Config ID"
															onFilter={() => updateFilters({ ...filters, configID: config.config_id })}
															className="font-mono text-xs"
														/>
													</TableCell>
													<TableCell>
														<InteractiveValue
															value={config.url}
															label="Callback URL"
															onFilter={() => updateFilters({ ...filters, callbackURL: config.url })}
															className="text-muted-foreground font-mono text-sm"
														/>
													</TableCell>
													<TableCell className="text-muted-foreground text-sm">
														{formatDistanceToNow(new Date(config.updated_at), { addSuffix: true })}
													</TableCell>
													{hasDeleteAccess ? (
														<TableCell>
															<Button
																variant="ghost"
																size="icon"
																aria-label="Delete push configuration"
																onClick={() => setDeleteTarget(config)}
															>
																<Trash2 className="h-4 w-4" />
															</Button>
														</TableCell>
													) : null}
												</TableRow>
											))
										)}
									</TableBody>
								</Table>
							</div>
							{totalCount > 0 ? (
								<div className="flex shrink-0 items-center justify-between text-xs" data-testid="pagination">
									<div className="text-muted-foreground">
										{offset + 1}-{Math.min(offset + PAGE_SIZE, totalCount)} of {totalCount} entries
									</div>
									<div className="flex items-center gap-2">
										<Button
											variant="ghost"
											size="sm"
											onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
											disabled={offset === 0}
											aria-label="Previous page"
										>
											<ChevronLeft className="size-3" />
										</Button>
										<span>
											Page {Math.floor(offset / PAGE_SIZE) + 1} of {Math.ceil(totalCount / PAGE_SIZE)}
										</span>
										<Button
											variant="ghost"
											size="sm"
											onClick={() => setOffset(offset + PAGE_SIZE)}
											disabled={offset + PAGE_SIZE >= totalCount}
											aria-label="Next page"
										>
											<ChevronRight className="size-3" />
										</Button>
									</div>
								</div>
							) : null}
						</div>
					</div>
				</div>
			</div>
			<AlertDialog open={deleteTarget !== null} onOpenChange={(open) => !open && setDeleteTarget(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete this push configuration?</AlertDialogTitle>
						<AlertDialogDescription>
							Bifrost will stop accepting and delivering push notifications for this callback. The upstream agent must register it again to
							restore delivery.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>Cancel</AlertDialogCancel>
						<AlertDialogAction
							disabled={isDeleting}
							onClick={async (event) => {
								event.preventDefault();
								if (!deleteTarget) return;
								try {
									await deletePushConfig(deleteTarget).unwrap();
									toast.success("Push configuration deleted");
									setDeleteTarget(null);
								} catch (deleteError) {
									toast.error(getErrorMessage(deleteError));
								}
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