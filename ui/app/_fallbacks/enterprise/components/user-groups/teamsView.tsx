import TeamsTable from "@/app/workspace/governance/views/teamsTable";
import FullPageLoader from "@/components/fullPageLoader";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { getErrorMessage, useGetCustomersQuery, useGetTeamsQuery, useGetVirtualKeysQuery } from "@/lib/store";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useEffect, useRef } from "react";
import { toast } from "sonner";

const POLLING_INTERVAL = 5000;
const PAGE_SIZE = 25;

export function TeamsView() {
	const hasVirtualKeysAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.View);
	const hasCustomersAccess = useRbac(RbacResource.Customers, RbacOperation.View);
	const hasTeamsAccess = useRbac(RbacResource.Teams, RbacOperation.View);
	const shownErrorsRef = useRef(new Set<string>());

	const [urlState, setUrlState] = useQueryStates(
		{
			search: parseAsString.withDefault(""),
			offset: parseAsInteger.withDefault(0),
			selected_team: parseAsString.withDefault(""),
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
		data: customersData,
		error: customersError,
		isLoading: customersLoading,
	} = useGetCustomersQuery(undefined, {
		skip: !hasCustomersAccess,
		pollingInterval: POLLING_INTERVAL,
	});
	const {
		data: teamsData,
		error: teamsError,
		isLoading: teamsLoading,
	} = useGetTeamsQuery(
		{
			limit: PAGE_SIZE,
			offset: urlState.offset,
			search: debouncedSearch || undefined,
		},
		{
			skip: !hasTeamsAccess,
			pollingInterval: POLLING_INTERVAL,
		},
	);

	const teamsTotal = teamsData?.total_count ?? 0;

	// Snap offset back when total shrinks past current page (e.g. delete last item on last page)
	useEffect(() => {
		if (!teamsData || urlState.offset < teamsTotal) return;
		setUrlState({ offset: teamsTotal === 0 ? 0 : Math.floor((teamsTotal - 1) / PAGE_SIZE) * PAGE_SIZE });
	}, [teamsTotal, urlState.offset]);

	const isLoading = vkLoading || customersLoading || teamsLoading;

	useEffect(() => {
		if (!vkError && !customersError && !teamsError) {
			shownErrorsRef.current.clear();
			return;
		}
		const errorKey = `${!!vkError}-${!!customersError}-${!!teamsError}`;
		if (shownErrorsRef.current.has(errorKey)) return;
		shownErrorsRef.current.add(errorKey);
		if (vkError && customersError && teamsError) {
			toast.error("Failed to load governance data.");
		} else {
			if (vkError) toast.error(`Failed to load virtual keys: ${getErrorMessage(vkError)}`);
			if (customersError) toast.error(`Failed to load customers: ${getErrorMessage(customersError)}`);
			if (teamsError) toast.error(`Failed to load teams: ${getErrorMessage(teamsError)}`);
		}
	}, [vkError, customersError, teamsError]);

	if (isLoading) {
		return <FullPageLoader />;
	}

	return (
		<div className="mx-auto w-full max-w-7xl">
			<TeamsTable
				teams={teamsData?.teams || []}
				totalCount={teamsData?.total_count || 0}
				customers={customersData?.customers || []}
				virtualKeys={virtualKeysData?.virtual_keys || []}
				search={urlState.search}
				debouncedSearch={debouncedSearch}
				onSearchChange={(val) => setUrlState({ search: val || null, offset: 0 }, { history: "replace" })}
				offset={urlState.offset}
				limit={PAGE_SIZE}
				onOffsetChange={(newOffset) => setUrlState({ offset: newOffset })}
				selectedTeamId={urlState.selected_team || null}
				onTeamAdd={() => setUrlState({ selected_team: "new" })}
				onTeamSelect={(team) => { setUrlState({ selected_team: team?.id ?? null }) }}
				onDialogClose={() => setUrlState({ selected_team: null })}
			/>
		</div>
	);
}