import VirtualKeysTable from "@/app/workspace/virtual-keys/views/virtualKeysTable";
import FullPageLoader from "@/components/fullPageLoader";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { getErrorMessage, useGetCustomersQuery, useGetTeamsQuery, useGetVirtualKeysQuery } from "@/lib/store";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useEffect, useRef } from "react";
import { toast } from "sonner";

const POLLING_INTERVAL = 5000;
const PAGE_SIZE = 25;

export default function GovernanceVirtualKeysPage() {
	const hasVirtualKeysAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.View);
	const hasTeamsAccess = useRbac(RbacResource.Teams, RbacOperation.View);
	const hasCustomersAccess = useRbac(RbacResource.Customers, RbacOperation.View);
	const shownErrorsRef = useRef(new Set<string>());

	const [urlState, setUrlState] = useQueryStates(
		{
			search: parseAsString.withDefault(""),
			customer_id: parseAsString.withDefault(""),
			team_id: parseAsString.withDefault(""),
			offset: parseAsInteger.withDefault(0),
			sort_by: parseAsString.withDefault(""),
			order: parseAsString.withDefault(""),
		},
		{ history: "push" },
	);

	const debouncedSearch = useDebouncedValue(urlState.search, 300);

	const {
		data: virtualKeysData,
		error: vkError,
		isLoading: vkLoading,
	} = useGetVirtualKeysQuery(
		{
			limit: PAGE_SIZE,
			offset: urlState.offset,
			search: debouncedSearch || undefined,
			customer_id: urlState.customer_id || undefined,
			team_id: urlState.team_id || undefined,
			sort_by: (urlState.sort_by as "name" | "budget_spent" | "created_at" | "status") || undefined,
			order: (urlState.order as "asc" | "desc") || undefined,
		},
		{
			skip: !hasVirtualKeysAccess,
			pollingInterval: POLLING_INTERVAL,
		},
	);

	const {
		data: teamsData,
		error: teamsError,
		isLoading: teamsLoading,
	} = useGetTeamsQuery(undefined, {
		skip: !hasTeamsAccess,
		pollingInterval: POLLING_INTERVAL,
	});

	const {
		data: customersData,
		error: customersError,
		isLoading: customersLoading,
	} = useGetCustomersQuery(undefined, {
		skip: !hasCustomersAccess,
		pollingInterval: POLLING_INTERVAL,
	});

	const vkTotal = virtualKeysData?.total_count ?? 0;

	// Snap offset back when total shrinks past current page (e.g. delete last item on last page)
	useEffect(() => {
		if (!virtualKeysData || urlState.offset < vkTotal) return;
		setUrlState({ offset: vkTotal === 0 ? 0 : Math.floor((vkTotal - 1) / PAGE_SIZE) * PAGE_SIZE });
	}, [vkTotal, urlState.offset]);

	const isLoading = vkLoading || teamsLoading || customersLoading;

	useEffect(() => {
		if (!vkError && !teamsError && !customersError) {
			shownErrorsRef.current.clear();
			return;
		}
		const errorKey = `${!!vkError}-${!!teamsError}-${!!customersError}`;
		if (shownErrorsRef.current.has(errorKey)) return;
		shownErrorsRef.current.add(errorKey);
		if (vkError && teamsError && customersError) {
			toast.error("Failed to load governance data.");
		} else {
			if (vkError) toast.error(`Failed to load virtual keys: ${getErrorMessage(vkError)}`);
			if (teamsError) toast.error(`Failed to load teams: ${getErrorMessage(teamsError)}`);
			if (customersError) toast.error(`Failed to load customers: ${getErrorMessage(customersError)}`);
		}
	}, [vkError, teamsError, customersError]);

	if (isLoading) {
		return <FullPageLoader />;
	}

	const handleSearchChange = (value: string) => {
		setUrlState({ search: value || null, offset: 0 });
	};

	const handleCustomerFilterChange = (value: string) => {
		setUrlState({ customer_id: value || null, offset: 0 });
	};

	const handleTeamFilterChange = (value: string) => {
		setUrlState({ team_id: value || null, offset: 0 });
	};

	const handleOffsetChange = (newOffset: number) => {
		setUrlState({ offset: newOffset });
	};

	const handleSortChange = (newSortBy: string, newOrder: string) => {
		setUrlState({ sort_by: newSortBy || null, order: newOrder || null, offset: 0 });
	};

	return (
		<div className="mx-auto w-full max-w-7xl">
			<VirtualKeysTable
				virtualKeys={virtualKeysData?.virtual_keys || []}
				totalCount={virtualKeysData?.total_count || 0}
				teams={teamsData?.teams || []}
				customers={customersData?.customers || []}
				search={urlState.search}
				debouncedSearch={debouncedSearch}
				onSearchChange={handleSearchChange}
				customerFilter={urlState.customer_id}
				onCustomerFilterChange={handleCustomerFilterChange}
				teamFilter={urlState.team_id}
				onTeamFilterChange={handleTeamFilterChange}
				offset={urlState.offset}
				limit={PAGE_SIZE}
				onOffsetChange={handleOffsetChange}
				sortBy={urlState.sort_by}
				order={urlState.order}
				onSortChange={handleSortChange}
			/>
		</div>
	);
}