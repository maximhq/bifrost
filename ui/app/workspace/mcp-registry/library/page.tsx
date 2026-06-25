import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useToast } from "@/hooks/use-toast";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { parseAsSafeString } from "@/lib/queryParamsParser";
import { getErrorMessage, useGetMCPClientsQuery, useGetMCPLibraryQuery } from "@/lib/store";
import type { MCPLibraryEntry } from "@/lib/types/mcp";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { ChevronLeft, ChevronRight, LayoutGrid, Library, List, Plus, Search, Settings } from "lucide-react";
import { parseAsArrayOf, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo, useState } from "react";
import { MCPLibraryAddServerSheet } from "./views/mcpLibraryAddServerSheet";
import { MCPLibraryFilterSidebar, type MCPLibraryFilters } from "./views/mcpLibraryFilterSidebar";
import { MCPLibraryInstallSheet, sanitizeServerName } from "./views/mcpLibraryInstallSheet";
import { MCPLibraryServerCard, MCPLibraryServerCardSkeleton } from "./views/mcpLibraryServerCard";
import { MCPLibraryServersTable, MCPLibraryServersTableSkeleton } from "./views/mcpLibraryServersTable";
import { MCPLibrarySettingsSheet } from "./views/mcpLibrarySettingsSheet";

const PAGE_SIZE = 24;
const VIEW_MODE_STORAGE_KEY = "mcp-library-view-mode";
type MCPLibraryViewMode = "grid" | "table";

function getInitialViewMode(): MCPLibraryViewMode {
	if (typeof window === "undefined") return "table";
	try {
		const savedViewMode = window.localStorage.getItem(VIEW_MODE_STORAGE_KEY);
		return savedViewMode === "grid" || savedViewMode === "table" ? savedViewMode : "table";
	} catch {
		return "table";
	}
}

export default function MCPLibraryPage() {
	const hasCreateMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Create);
	const hasDeleteMCPLibraryAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Delete);
	const hasSettingsAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const [selectedServer, setSelectedServer] = useState<MCPLibraryEntry | null>(null);
	const [settingsOpen, setSettingsOpen] = useState(false);
	const [addServerOpen, setAddServerOpen] = useState(false);
	const [viewMode, setViewMode] = useState<MCPLibraryViewMode>(getInitialViewMode);
	const { toast } = useToast();

	// URL state management with nuqs — search, filters, pagination all in query params
	const [urlState, setUrlState] = useQueryStates(
		{
			search: parseAsSafeString.withDefault(""),
			categories: parseAsArrayOf(parseAsString).withDefault([]),
			connection_types: parseAsArrayOf(parseAsString).withDefault([]),
			auth_types: parseAsArrayOf(parseAsString).withDefault([]),
			tags: parseAsArrayOf(parseAsString).withDefault([]),
			offset: parseAsInteger.withDefault(0),
		},
		// Live search/filter changes use replace (don't pollute history per keystroke);
		// pagination opts into push per-call so back/forward steps by page.
		{ history: "replace" },
	);

	const debouncedSearch = useDebouncedValue(urlState.search, 300);

	// Derive filters object for the sidebar
	const filters: MCPLibraryFilters = useMemo(
		() => ({
			categories: urlState.categories,
			connection_types: urlState.connection_types,
			auth_types: urlState.auth_types,
			tags: urlState.tags,
		}),
		[urlState.categories, urlState.connection_types, urlState.auth_types, urlState.tags],
	);

	const setFilters = useCallback(
		(newFilters: MCPLibraryFilters) => {
			setUrlState({
				categories: newFilters.categories,
				connection_types: newFilters.connection_types,
				auth_types: newFilters.auth_types,
				tags: newFilters.tags,
				offset: 0,
			});
		},
		[setUrlState],
	);

	const queryParams = useMemo(
		() => ({
			search: debouncedSearch || undefined,
			category: filters.categories.length > 0 ? filters.categories.join(",") : undefined,
			connection_type: filters.connection_types.length > 0 ? filters.connection_types.join(",") : undefined,
			auth_type: filters.auth_types.length > 0 ? filters.auth_types.join(",") : undefined,
			tags: filters.tags.length > 0 ? filters.tags.join(",") : undefined,
			limit: PAGE_SIZE,
			offset: urlState.offset,
		}),
		[debouncedSearch, filters, urlState.offset],
	);

	const { data: libraryData, error: libraryError, isFetching, refetch } = useGetMCPLibraryQuery(queryParams);

	const servers = useMemo(() => libraryData?.servers || [], [libraryData?.servers]);
	const totalCount = libraryData?.total_count || 0;

	// Installed-detection: match on connection_url or name (case-insensitive)
	const { data: mcpClientsData, error: mcpClientsError } = useGetMCPClientsQuery({ limit: 100, offset: 0 });

	useEffect(() => {
		if (!libraryError && !mcpClientsError) return;
		const err = libraryError || mcpClientsError;
		if (!err) return;
		const message = getErrorMessage(err);
		if (message.toLowerCase().includes("mcp is not configured in this bifrost instance")) return;
		toast({ title: "Error", description: message, variant: "destructive" });
	}, [libraryError, mcpClientsError, toast]);

	const installedServerSlugs = useMemo(() => {
		const clients = mcpClientsData?.clients || [];
		return new Set(
			servers
				.filter((server) =>
					clients.some((client) => {
						const connectionString = client.config.connection_string;
						const connectionUrl =
							connectionString?.type === "env" || connectionString?.type === "vault" ? connectionString.ref : connectionString?.value;
						return (
							(server.connection_url && connectionUrl === server.connection_url) ||
							client.config.name.toLowerCase() === sanitizeServerName(server.name).toLowerCase()
						);
					}),
				)
				.map((server) => server.slug),
		);
	}, [mcpClientsData?.clients, servers]);

	const handleInstalled = useCallback(async () => {
		await refetch();
	}, [refetch]);

	const handleViewModeChange = useCallback((mode: MCPLibraryViewMode) => {
		setViewMode(mode);
		try {
			window.localStorage.setItem(VIEW_MODE_STORAGE_KEY, mode);
		} catch {
			// Keep the in-memory preference when browser storage is unavailable.
		}
	}, []);

	// Pagination
	const totalPages = Math.max(1, Math.ceil(totalCount / PAGE_SIZE));
	const currentPage = Math.floor(urlState.offset / PAGE_SIZE) + 1;

	const hasActiveFilters =
		filters.categories.length > 0 || filters.connection_types.length > 0 || filters.auth_types.length > 0 || filters.tags.length > 0;
	const isCatalogEmpty = !isFetching && totalCount === 0 && !debouncedSearch && !hasActiveFilters;

	return (
		<div className="dark:bg-card no-padding-parent no-border-parent h-[calc(100dvh_-_16px)]">
			<div className="bg-background flex h-full w-full grow gap-3">
				{/* Sidebar Filters */}
				<MCPLibraryFilterSidebar filters={filters} onFiltersChange={setFilters} />

				{/* Main Content */}
				<div className="bg-card h-full w-full rounded-l-md">
					<div className="flex h-full flex-col gap-4 p-4 pb-2">
						{/* Header */}
						<div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
							<div className="space-y-1">
								<h2 className="text-lg font-semibold tracking-tight">MCP Server Library</h2>
								<p className="text-muted-foreground max-w-2xl text-sm">Browse and install MCP servers from the synced catalog.</p>
							</div>
							<div className="flex items-center gap-2">
								{hasCreateMCPClientAccess && (
									<Button variant="outline" size="sm" onClick={() => setAddServerOpen(true)} data-testid="mcp-library-add-server-btn">
										<Plus className="h-4 w-4" />
										Add Server
									</Button>
								)}
								{hasSettingsAccess && (
									<Button variant="outline" size="sm" onClick={() => setSettingsOpen(true)} data-testid="mcp-library-settings-btn">
										<Settings className="h-4 w-4" />
										Settings
									</Button>
								)}
							</div>
						</div>

						{/* Search */}
						{!isCatalogEmpty && (
							<div className="-mx-2 flex flex-col gap-3 px-2 py-2 sm:flex-row sm:items-center sm:justify-between">
								<div className="relative max-w-md flex-1">
									<Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
									<Input
										value={urlState.search}
										onChange={(e) => setUrlState({ search: e.target.value, offset: 0 })}
										placeholder="Search servers..."
										className="h-9 pl-9"
										data-testid="mcp-library-search-input"
									/>
								</div>
								<div className="border-border flex w-fit overflow-hidden rounded-sm border p-0.5" aria-label="Library view mode">
									<Button
										type="button"
										variant="ghost"
										size="sm"
										className={cn(
											"h-8 rounded-xs border border-transparent px-2.5 shadow-none",
											viewMode === "table" &&
												"border-primary bg-primary text-primary-foreground hover:bg-primary/90 hover:text-primary-foreground",
										)}
										onClick={() => handleViewModeChange("table")}
										aria-pressed={viewMode === "table"}
										data-testid="mcp-library-table-view-toggle"
									>
										<List className="h-4 w-4" />
										<span className="hidden sm:inline">Table</span>
									</Button>
									<Button
										type="button"
										variant="ghost"
										size="sm"
										className={cn(
											"h-8 rounded-xs border border-transparent px-2.5 shadow-none",
											viewMode === "grid" &&
												"border-primary bg-primary text-primary-foreground hover:bg-primary/90 hover:text-primary-foreground",
										)}
										onClick={() => handleViewModeChange("grid")}
										aria-pressed={viewMode === "grid"}
										data-testid="mcp-library-grid-view-toggle"
									>
										<LayoutGrid className="h-4 w-4" />
										<span className="hidden sm:inline">Grid</span>
									</Button>
								</div>
							</div>
						)}
						<div className="flex grow flex-col overflow-hidden">
							{/* Loading skeletons */}
							{isFetching && servers.length === 0 ? (
								viewMode === "grid" ? (
									<ScrollArea className="mb-2 overflow-y-auto">
										<div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3" data-testid="mcp-library-grid-skeleton">
											{Array.from({ length: 6 }).map((_, i) => (
												// biome-ignore lint/suspicious/noArrayIndexKey: static skeleton placeholders have no stable id
												<MCPLibraryServerCardSkeleton key={i} />
											))}
										</div>
									</ScrollArea>
								) : (
									<MCPLibraryServersTableSkeleton />
								)
							) : servers.length === 0 ? (
								<div
									className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center"
									data-testid="mcp-library-empty-state"
								>
									<div className="text-muted-foreground">
										<Library className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
									</div>
									<div className="flex flex-col gap-1">
										<h1 className="text-muted-foreground text-xl font-medium">
											{isCatalogEmpty ? "No synced servers yet" : "No servers found"}
										</h1>
										<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">
											{isCatalogEmpty
												? "Configure the library sync source in Settings to populate this catalog."
												: "Try adjusting your search or filters."}
										</div>
										{isCatalogEmpty && hasSettingsAccess && (
											<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
												<Button onClick={() => setSettingsOpen(true)} data-testid="mcp-library-empty-settings-btn">
													<Settings className="h-4 w-4" />
													Configure sync
												</Button>
											</div>
										)}
									</div>
								</div>
							) : (
								<>
									{viewMode === "grid" ? (
										<ScrollArea className="mb-2 overflow-y-auto">
											<div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3" data-testid="mcp-library-grid-view">
												{servers.map((server) => {
													const isInstalled = installedServerSlugs.has(server.slug);
													return (
														<MCPLibraryServerCard
															key={server.slug}
															server={server}
															isInstalled={isInstalled}
															canCreateMCPClient={hasCreateMCPClientAccess}
															canDelete={hasDeleteMCPLibraryAccess}
															onInstall={setSelectedServer}
														/>
													);
												})}
											</div>
										</ScrollArea>
									) : (
										<MCPLibraryServersTable
											servers={servers}
											installedServerSlugs={installedServerSlugs}
											canCreateMCPClient={hasCreateMCPClientAccess}
											canDelete={hasDeleteMCPLibraryAccess}
											onInstall={setSelectedServer}
										/>
									)}

									{/* Pagination */}
									{totalCount > 0 && (
										<div className="mt-auto flex shrink-0 items-center justify-between text-xs" data-testid="pagination">
											<div className="text-muted-foreground flex items-center gap-2">
												{(urlState.offset + 1).toLocaleString()}-{Math.min(urlState.offset + PAGE_SIZE, totalCount).toLocaleString()} of{" "}
												{totalCount.toLocaleString()} entries
											</div>

											<div className="flex items-center gap-2">
												<Button
													variant="ghost"
													size="sm"
													onClick={() => setUrlState({ offset: Math.max(0, urlState.offset - PAGE_SIZE) }, { history: "push" })}
													disabled={urlState.offset === 0}
													data-testid="mcp-library-pagination-prev-btn"
													aria-label="Previous page"
												>
													<ChevronLeft className="size-3" />
												</Button>

												<div className="flex items-center gap-1">
													<span>Page</span>
													<span>{currentPage}</span>
													<span>of {totalPages}</span>
												</div>

												<Button
													variant="ghost"
													size="sm"
													onClick={() => setUrlState({ offset: urlState.offset + PAGE_SIZE }, { history: "push" })}
													disabled={urlState.offset + PAGE_SIZE >= totalCount}
													data-testid="mcp-library-pagination-next-btn"
													aria-label="Next page"
												>
													<ChevronRight className="size-3" />
												</Button>
											</div>
										</div>
									)}
								</>
							)}
						</div>
					</div>
				</div>
			</div>

			{/* Install sheet */}
			{selectedServer && (
				<MCPLibraryInstallSheet
					server={selectedServer}
					open={!!selectedServer}
					onClose={() => setSelectedServer(null)}
					onInstalled={handleInstalled}
				/>
			)}

			{/* Settings sheet */}
			<MCPLibrarySettingsSheet open={settingsOpen} onClose={() => setSettingsOpen(false)} />

			{/* Add Server sheet */}
			<MCPLibraryAddServerSheet open={addServerOpen} onClose={() => setAddServerOpen(false)} />
		</div>
	);
}