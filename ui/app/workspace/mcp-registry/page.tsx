import FullPageLoader from "@/components/fullPageLoader";
import { useToast } from "@/hooks/use-toast";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { getErrorMessage, useGetMCPClientsQuery } from "@/lib/store";
import { parseAsArrayOf, parseAsBoolean, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo } from "react";
import { MCPClientsFilterSidebar, type MCPClientFilters } from "./views/mcpClientsFilterSidebar";
import MCPClientsTable from "./views/mcpClientsTable";

const POLLING_INTERVAL = 5000;
const PAGE_SIZE = 25;

// A boolean facet is only applied when exactly one option is selected; zero or
// both selections mean "no filter". Values are the underlying column strings.
function resolveBooleanFacet(selected: string[]): boolean | undefined {
	if (selected.length !== 1) return undefined;
	return selected[0] === "true";
}

export default function MCPServersPage() {
	const [urlState, setUrlState] = useQueryStates(
		{
			search: parseAsString.withDefault(""),
			server: parseAsString.withDefault(""),
			connection_types: parseAsArrayOf(parseAsString).withDefault([]),
			auth_types: parseAsArrayOf(parseAsString).withDefault([]),
			states: parseAsArrayOf(parseAsString).withDefault([]),
			code_mode: parseAsArrayOf(parseAsString).withDefault([]),
			status: parseAsArrayOf(parseAsString).withDefault([]),
			only_all_vks: parseAsBoolean.withDefault(false),
			virtual_keys: parseAsArrayOf(parseAsString).withDefault([]),
			offset: parseAsInteger.withDefault(0),
		},
		{ history: "replace" },
	);
	const debouncedSearch = useDebouncedValue(urlState.search, 300);

	const filters: MCPClientFilters = useMemo(
		() => ({
			connection_types: urlState.connection_types,
			auth_types: urlState.auth_types,
			states: urlState.states,
			code_mode: urlState.code_mode,
			status: urlState.status,
			only_all_vks: urlState.only_all_vks,
			virtual_keys: urlState.virtual_keys,
		}),
		[
			urlState.connection_types,
			urlState.auth_types,
			urlState.states,
			urlState.code_mode,
			urlState.status,
			urlState.only_all_vks,
			urlState.virtual_keys,
		],
	);

	const setFilters = useCallback(
		(newFilters: MCPClientFilters) => {
			void setUrlState({
				connection_types: newFilters.connection_types,
				auth_types: newFilters.auth_types,
				states: newFilters.states,
				code_mode: newFilters.code_mode,
				status: newFilters.status,
				only_all_vks: newFilters.only_all_vks,
				virtual_keys: newFilters.virtual_keys,
				offset: 0,
			});
		},
		[setUrlState],
	);

	const filtersActive =
		filters.connection_types.length > 0 ||
		filters.auth_types.length > 0 ||
		filters.states.length > 0 ||
		filters.code_mode.length > 0 ||
		filters.status.length > 0 ||
		filters.only_all_vks ||
		filters.virtual_keys.length > 0;

	const {
		data: mcpClientsData,
		error,
		isLoading,
		refetch,
	} = useGetMCPClientsQuery(
		{
			limit: PAGE_SIZE,
			offset: urlState.offset,
			search: debouncedSearch || undefined,
			server: urlState.server || undefined,
			connection_type: filters.connection_types.length > 0 ? filters.connection_types.join(",") : undefined,
			auth_type: filters.auth_types.length > 0 ? filters.auth_types.join(",") : undefined,
			state: filters.states.length > 0 ? filters.states.join(",") : undefined,
			virtual_keys: filters.virtual_keys.length > 0 ? filters.virtual_keys.join(",") : undefined,
			all_virtual_keys: filters.only_all_vks || undefined,
			code_mode: resolveBooleanFacet(filters.code_mode),
			disabled: resolveBooleanFacet(filters.status),
		},
		{
			pollingInterval: POLLING_INTERVAL,
		},
	);

	const mcpClients = mcpClientsData?.clients || [];
	const totalCount = mcpClientsData?.total_count || 0;

	// Snap offset back when total shrinks past current page (e.g. delete last item on last page)
	useEffect(() => {
		if (!mcpClientsData || urlState.offset < totalCount) return;
		void setUrlState({ offset: totalCount === 0 ? 0 : Math.floor((totalCount - 1) / PAGE_SIZE) * PAGE_SIZE });
	}, [totalCount, urlState.offset, mcpClientsData, setUrlState]);

	const { toast } = useToast();

	useEffect(() => {
		if (error) {
			const message = getErrorMessage(error);
			if (message.toLowerCase().includes("mcp is not configured in this bifrost instance")) return;
			toast({
				title: "Error",
				description: message,
				variant: "destructive",
			});
		}
	}, [error, toast]);

	if (isLoading) {
		return <FullPageLoader />;
	}

	const handleSearchChange = (value: string) => void setUrlState({ search: value, offset: 0 });
	const handleServerFilterClear = () => void setUrlState({ server: null, offset: 0 });
	const handleOffsetChange = (offset: number) => void setUrlState({ offset }, { history: "push" });

	const table = (
		<MCPClientsTable
			mcpClients={mcpClients}
			totalCount={totalCount}
			refetch={refetch}
			search={urlState.search}
			debouncedSearch={debouncedSearch}
			server={urlState.server}
			filtersActive={filtersActive}
			onSearchChange={handleSearchChange}
			onServerFilterClear={handleServerFilterClear}
			offset={urlState.offset}
			limit={PAGE_SIZE}
			onOffsetChange={handleOffsetChange}
		/>
	);

	// Onboarding empty state: no servers at all and no active filters/search.
	// Render full-width without the filter sidebar (the table renders the CTA).
	if (totalCount === 0 && !filtersActive && !debouncedSearch && !urlState.server) {
		return <div className="mx-auto flex h-[calc(100dvh-50px)] w-full max-w-7xl flex-col">{table}</div>;
	}

	return (
		<div className="dark:bg-card no-padding-parent no-border-parent h-[calc(100dvh_-_16px)]">
			<div className="bg-background flex h-full w-full grow gap-3">
				<MCPClientsFilterSidebar filters={filters} onFiltersChange={setFilters} />
				<div className="bg-card h-full w-full overflow-hidden rounded-l-md">
					<div className="flex h-full flex-col p-4">{table}</div>
				</div>
			</div>
		</div>
	);
}
