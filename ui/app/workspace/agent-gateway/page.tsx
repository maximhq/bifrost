import FullPageLoader from "@/components/fullPageLoader";
import { Button } from "@/components/ui/button";
import { useToast } from "@/hooks/use-toast";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { getErrorMessage } from "@/lib/store";
import { useGetAgentsQuery } from "@/lib/store/apis/agentsApi";
import type { AgentRegistrationView } from "@/lib/types/agents";
import { parseAsArrayOf, parseAsBoolean, parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useEffect, useMemo } from "react";
import { AgentsFilterSidebar, type AgentFilters } from "./views/agentsFilterSidebar";
import AgentsTable from "./views/agentsTable";

const PAGE_SIZE = 25;

// The /api/agents list has no server-side pagination, search, or filtering, so
// all three happen client-side over the full list.
function matchesFilters(agent: AgentRegistrationView, filters: AgentFilters): boolean {
	if (filters.status.length === 1 && String(agent.enabled) !== filters.status[0]) return false;

	if (filters.upstream_auth.length > 0) {
		const kinds: string[] = [];
		if (agent.discovery_auth) kinds.push("discovery");
		if (agent.runtime_auth) kinds.push("runtime");
		if (kinds.length === 0) kinds.push("none");
		if (!filters.upstream_auth.some((kind) => kinds.includes(kind))) return false;
	}

	// Access ORs together, matching the MCP catalog: allowed by default, or
	// granted one of the selected keys.
	if (filters.only_allowed_by_default || filters.virtual_keys.length > 0) {
		const allowedByDefault = filters.only_allowed_by_default && agent.allow_by_default;
		const granted = filters.virtual_keys.some((id) => agent.virtual_key_ids?.includes(id));
		if (!allowedByDefault && !granted) return false;
	}

	return true;
}

export default function AgentGatewayPage() {
	const [urlState, setUrlState] = useQueryStates(
		{
			search: parseAsString.withDefault(""),
			status: parseAsArrayOf(parseAsString).withDefault([]),
			upstream_auth: parseAsArrayOf(parseAsString).withDefault([]),
			only_allowed_by_default: parseAsBoolean.withDefault(false),
			virtual_keys: parseAsArrayOf(parseAsString).withDefault([]),
			offset: parseAsInteger.withDefault(0),
		},
		{ history: "push" },
	);
	const debouncedSearch = useDebouncedValue(urlState.search, 300);

	const filters: AgentFilters = useMemo(
		() => ({
			status: urlState.status,
			upstream_auth: urlState.upstream_auth,
			only_allowed_by_default: urlState.only_allowed_by_default,
			virtual_keys: urlState.virtual_keys,
		}),
		[urlState.status, urlState.upstream_auth, urlState.only_allowed_by_default, urlState.virtual_keys],
	);

	const setFilters = useCallback(
		(newFilters: AgentFilters) => {
			void setUrlState({
				status: newFilters.status,
				upstream_auth: newFilters.upstream_auth,
				only_allowed_by_default: newFilters.only_allowed_by_default,
				virtual_keys: newFilters.virtual_keys,
				offset: 0,
			});
		},
		[setUrlState],
	);

	const filtersActive =
		filters.status.length > 0 || filters.upstream_auth.length > 0 || filters.only_allowed_by_default || filters.virtual_keys.length > 0;

	const { data, currentData, error, isLoading, isFetching, refetch } = useGetAgentsQuery();
	const agents = useMemo(() => data?.agents ?? [], [data]);

	const filteredAgents = useMemo(() => {
		const q = debouncedSearch.trim().toLowerCase();
		return agents.filter((agent) => {
			if (q && !agent.name.toLowerCase().includes(q) && !agent.agent_card_url.toLowerCase().includes(q)) return false;
			return matchesFilters(agent, filters);
		});
	}, [agents, debouncedSearch, filters]);

	const totalCount = filteredAgents.length;
	const pageAgents = useMemo(() => filteredAgents.slice(urlState.offset, urlState.offset + PAGE_SIZE), [filteredAgents, urlState.offset]);

	// Snap offset back when the filtered list shrinks past the current page.
	useEffect(() => {
		if (isFetching || !currentData || urlState.offset === 0 || urlState.offset < totalCount) return;
		void setUrlState({ offset: totalCount === 0 ? 0 : Math.floor((totalCount - 1) / PAGE_SIZE) * PAGE_SIZE }, { history: "replace" });
	}, [currentData, isFetching, totalCount, urlState.offset, setUrlState]);
	const { toast } = useToast();

	useEffect(() => {
		if (error) {
			toast({ title: "Error", description: getErrorMessage(error), variant: "destructive" });
		}
	}, [error, toast]);

	if (isLoading) {
		return <FullPageLoader />;
	}

	if (error && !data) {
		return (
			<div className="flex h-full flex-col items-center justify-center gap-3 text-center" data-testid="agents-load-error">
				<p className="text-sm font-medium">Failed to load agents.</p>
				<p className="text-muted-foreground max-w-md text-sm">{getErrorMessage(error)}</p>
				<Button variant="outline" size="sm" onClick={() => void refetch()}>
					Retry
				</Button>
			</div>
		);
	}

	const table = (
		<AgentsTable
			agents={pageAgents}
			totalAgents={agents.length}
			totalCount={totalCount}
			refetch={refetch}
			search={urlState.search}
			debouncedSearch={debouncedSearch}
			filtersActive={filtersActive}
			onSearchChange={(value) => void setUrlState({ search: value, offset: 0 })}
			offset={urlState.offset}
			limit={PAGE_SIZE}
			onOffsetChange={(offset) => void setUrlState({ offset }, { history: "push" })}
		/>
	);

	// Onboarding empty state: no agents at all and nothing narrowing the list.
	// Render full-width without the filter sidebar (the table renders the CTA).
	if (agents.length === 0 && !filtersActive && !debouncedSearch) {
		return <div className="mx-auto flex h-[calc(var(--app-content-viewport)_-_50px)] w-full max-w-7xl flex-col">{table}</div>;
	}

	return (
		<div className="dark:bg-card no-padding-parent no-border-parent h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))]">
			<div className="bg-background flex h-full w-full grow gap-3">
				<AgentsFilterSidebar filters={filters} onFiltersChange={setFilters} />
				<div className="bg-card h-full w-full overflow-hidden rounded-md border">
					<div className="flex h-full flex-col p-4">{table}</div>
				</div>
			</div>
		</div>
	);
}