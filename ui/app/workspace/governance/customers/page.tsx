import CustomersTable from "@/app/workspace/governance/views/customerTable";
import FullPageLoader from "@/components/fullPageLoader";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { parseAsSafeString } from "@/lib/queryParamsParser";
import { getErrorMessage, useGetCustomersQuery, useGetTeamsQuery, useGetVirtualKeysQuery } from "@/lib/store";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { parseAsInteger, useQueryStates } from "nuqs";
import { useEffect, useRef } from "react";
import { toast } from "sonner";

const POLLING_INTERVAL = 5000;
const PAGE_SIZE = 25;

export default function GovernanceCustomersPage() {
	const hasVirtualKeysAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.View);
	const hasTeamsAccess = useRbac(RbacResource.Teams, RbacOperation.View);
	const hasCustomersAccess = useRbac(RbacResource.Customers, RbacOperation.View);
	const shownErrorsRef = useRef(new Set<string>());

	const [urlState, setUrlState] = useQueryStates(
		{
			search: parseAsSafeString.withDefault(""),
			offset: parseAsInteger.withDefault(0),
		},
		{ history: "push" },
	);

	const debouncedSearch = useDebouncedValue(urlState.search, 300);

	const {
		data: virtualKeysData,
		error: vkError,
		isLoading: vkLoading,
	} = useGetVirtualKeysQuery(undefined, {
		skip: !hasVirtualKeysAccess,
		pollingInterval: POLLING_INTERVAL,
	});
	const {
		data: teamsData,
		error: teamsError,
		isLoading: teamsLoading,
	} = useGetTeamsQuery(undefined, { skip: !hasTeamsAccess, pollingInterval: POLLING_INTERVAL });
	const {
		data: customersData,
		error: customersError,
		isLoading: customersLoading,
		isFetching,
	} = useGetCustomersQuery(
		{
			limit: PAGE_SIZE,
			offset: urlState.offset,
			search: debouncedSearch || undefined,
		},
		{
			skip: !hasCustomersAccess,
			pollingInterval: POLLING_INTERVAL,
		},
	);

	const customersTotal = customersData?.total_count ?? 0;

	// Snap offset back when total shrinks past current page (e.g. delete last item on last page)
	useEffect(() => {
		if (!customersData || urlState.offset < customersTotal) return;
		setUrlState({ offset: customersTotal === 0 ? 0 : Math.floor((customersTotal - 1) / PAGE_SIZE) * PAGE_SIZE });
	}, [customersTotal, urlState.offset]);

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

	return (
		<div className="no-padding-parent mx-auto flex h-[calc(100dvh-1rem)] w-full flex-col p-4">
			<CustomersTable
				customers={customersData?.customers || []}
				totalCount={customersData?.total_count || 0}
				teams={teamsData?.teams || []}
				virtualKeys={virtualKeysData?.virtual_keys || []}
				search={urlState.search}
				debouncedSearch={debouncedSearch}
				onSearchChange={(val) => setUrlState({ search: val || null, offset: 0 })}
				offset={urlState.offset}
				limit={PAGE_SIZE}
				onOffsetChange={(newOffset) => setUrlState({ offset: newOffset })}
				isFetching={isFetching}
			/>
		</div>
	);
}