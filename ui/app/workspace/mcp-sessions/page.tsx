import FullPageLoader from "@/components/fullPageLoader";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { getErrorMessage, useGetMCPSessionsQuery } from "@/lib/store";
import { AuthMode, MCPSessionKind, MCPSessionStatus } from "@/lib/types/mcpSessions";
import { parseAsArrayOf, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo } from "react";
import { MCPSessionFilters, MCPSessionsFilterSidebar } from "./views/mcpSessionsFilterSidebar";
import SessionsTable from "./views/sessionsTable";

// Page size larger than the governance default (25) since session rows are
// denser than VK rows and the page is the only screen of MCP-auth content.
const PAGE_SIZE = 50;

export default function MCPSessionsPage() {
	const [urlState, setUrlState] = useQueryStates(
		{
			q: parseAsString.withDefault(""),
			kind: parseAsArrayOf(parseAsString).withDefault([]),
			status: parseAsArrayOf(parseAsString).withDefault([]),
			auth_mode: parseAsArrayOf(parseAsString).withDefault([]),
			mcp_client_id: parseAsArrayOf(parseAsString).withDefault([]),
			virtual_key_id: parseAsArrayOf(parseAsString).withDefault([]),
			user_id: parseAsArrayOf(parseAsString).withDefault([]),
			identity: parseAsString.withDefault(""),
			offset: parseAsInteger.withDefault(0),
		},
		{ history: "push" },
	);

	const debouncedSearch = useDebouncedValue(urlState.q, 300);
	const normalizedIdentity = urlState.identity.trim();

	const filters: MCPSessionFilters = useMemo(
		() => ({
			kind: urlState.kind,
			status: urlState.status,
			auth_mode: urlState.auth_mode,
			mcp_client_id: urlState.mcp_client_id,
			virtual_key_id: urlState.virtual_key_id,
			user_id: urlState.user_id,
		}),
		[urlState.kind, urlState.status, urlState.auth_mode, urlState.mcp_client_id, urlState.virtual_key_id, urlState.user_id],
	);

	const setFilters = useCallback(
		(newFilters: MCPSessionFilters) => {
			void setUrlState({
				kind: newFilters.kind,
				status: newFilters.status,
				auth_mode: newFilters.auth_mode,
				mcp_client_id: newFilters.mcp_client_id,
				virtual_key_id: newFilters.virtual_key_id,
				user_id: newFilters.user_id,
				offset: 0,
			});
		},
		[setUrlState],
	);

	const { data, isLoading, isFetching, isError, error } = useGetMCPSessionsQuery({
		q: debouncedSearch || undefined,
		kind: filters.kind.length ? (filters.kind as MCPSessionKind[]) : undefined,
		status: filters.status.length ? (filters.status as MCPSessionStatus[]) : undefined,
		auth_mode: filters.auth_mode.length ? (filters.auth_mode as AuthMode[]) : undefined,
		mcp_client_id: filters.mcp_client_id.length ? filters.mcp_client_id : undefined,
		virtual_key_id: filters.virtual_key_id.length ? filters.virtual_key_id : undefined,
		user_id: filters.user_id.length ? filters.user_id : undefined,
		identity: normalizedIdentity || undefined,
		limit: PAGE_SIZE,
		offset: urlState.offset,
	});

	const totalCount = data?.total_count ?? 0;

	// Snap offset back if the total shrinks past the current page (e.g. a
	// revoke removed the last row on the last page). Same logic as VKs.
	useEffect(() => {
		if (!data || urlState.offset < totalCount) return;
		setUrlState({
			offset: totalCount === 0 ? 0 : Math.floor((totalCount - 1) / PAGE_SIZE) * PAGE_SIZE,
		});
	}, [totalCount, urlState.offset, data, setUrlState]);

	if (isLoading) return <FullPageLoader />;

	if (isError) {
		return (
			<div className="mx-auto w-full max-w-7xl">
				<div className="border-destructive bg-destructive/10 text-destructive rounded-lg border p-6 text-sm">
					Failed to load MCP sessions: {getErrorMessage(error)}
				</div>
			</div>
		);
	}

	const filtersActive =
		filters.kind.length > 0 ||
		filters.status.length > 0 ||
		filters.auth_mode.length > 0 ||
		filters.mcp_client_id.length > 0 ||
		filters.virtual_key_id.length > 0 ||
		filters.user_id.length > 0;
	const hasActiveFilters = !!urlState.q || filtersActive || !!normalizedIdentity;

	const handleSearchChange = (value: string) => setUrlState({ q: value || null, offset: 0 });
	const handleOffsetChange = (offset: number) => setUrlState({ offset });

	const table = (
		<SessionsTable
			sessions={data?.sessions ?? []}
			totalCount={totalCount}
			isFetching={isFetching}
			search={urlState.q}
			onSearchChange={handleSearchChange}
			hasActiveFilters={hasActiveFilters}
			offset={urlState.offset}
			limit={PAGE_SIZE}
			onOffsetChange={handleOffsetChange}
		/>
	);

	// No sessions at all and no active filters/search: render full-width
	// without the filter sidebar, mirroring the MCP clients onboarding state.
	if (totalCount === 0 && !hasActiveFilters) {
		return <div className="mx-auto flex h-[calc(100dvh-50px)] w-full max-w-7xl flex-col">{table}</div>;
	}

	return (
		<div className="dark:bg-card no-padding-parent no-border-parent h-[calc(100dvh_-_16px)]">
			<div className="bg-background flex h-full w-full grow gap-3">
				<MCPSessionsFilterSidebar filters={filters} onFiltersChange={setFilters} />
				<div className="bg-card h-full w-full overflow-hidden rounded-l-md">
					<div className="flex h-full flex-col p-4">{table}</div>
				</div>
			</div>
		</div>
	);
}
