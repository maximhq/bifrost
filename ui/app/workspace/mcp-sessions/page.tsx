import FullPageLoader from "@/components/fullPageLoader";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { getErrorMessage, useGetMCPSessionsQuery } from "@/lib/store";
import { AuthMode, MCPSessionKind, MCPSessionStatus } from "@/lib/types/mcpSessions";
import { parseAsArrayOf, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useEffect } from "react";
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
			offset: parseAsInteger.withDefault(0),
		},
		{ history: "push" },
	);

	const debouncedSearch = useDebouncedValue(urlState.q, 300);

	const { data, isLoading, isFetching, isError, error } = useGetMCPSessionsQuery({
		q: debouncedSearch || undefined,
		kind: urlState.kind.length ? (urlState.kind as MCPSessionKind[]) : undefined,
		status: urlState.status.length ? (urlState.status as MCPSessionStatus[]) : undefined,
		auth_mode: urlState.auth_mode.length ? (urlState.auth_mode as AuthMode[]) : undefined,
		mcp_client_id: urlState.mcp_client_id.length ? urlState.mcp_client_id : undefined,
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

	if (isLoading) {
		return <FullPageLoader />;
	}

	if (isError) {
		return (
			<div className="mx-auto w-full max-w-7xl">
				<div className="border-destructive bg-destructive/10 text-destructive rounded-lg border p-6 text-sm">
					Failed to load MCP sessions: {getErrorMessage(error)}
				</div>
			</div>
		);
	}

	const hasActiveFilters =
		!!urlState.q ||
		urlState.kind.length > 0 ||
		urlState.status.length > 0 ||
		urlState.auth_mode.length > 0 ||
		urlState.mcp_client_id.length > 0;

	const handleSearchChange = (value: string) => setUrlState({ q: value || null, offset: 0 });
	const handleKindChange = (value: string[]) => setUrlState({ kind: value.length ? value : null, offset: 0 });
	const handleStatusChange = (value: string[]) => setUrlState({ status: value.length ? value : null, offset: 0 });
	const handleAuthModeChange = (value: string[]) => setUrlState({ auth_mode: value.length ? value : null, offset: 0 });
	const handleOffsetChange = (offset: number) => setUrlState({ offset });
	const handleClearFilters = () => setUrlState({ q: null, kind: null, status: null, auth_mode: null, mcp_client_id: null, offset: 0 });

	return (
		<div className="mx-auto w-full max-w-7xl">
			<SessionsTable
				sessions={data?.sessions ?? []}
				totalCount={totalCount}
				isFetching={isFetching}
				search={urlState.q}
				onSearchChange={handleSearchChange}
				kindFilter={urlState.kind}
				onKindFilterChange={handleKindChange}
				statusFilter={urlState.status}
				onStatusFilterChange={handleStatusChange}
				authModeFilter={urlState.auth_mode}
				onAuthModeFilterChange={handleAuthModeChange}
				hasActiveFilters={hasActiveFilters}
				onClearFilters={handleClearFilters}
				offset={urlState.offset}
				limit={PAGE_SIZE}
				onOffsetChange={handleOffsetChange}
			/>
		</div>
	);
}