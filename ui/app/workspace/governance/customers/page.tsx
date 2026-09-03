import CustomersTable from "@/app/workspace/governance/views/customerTable";
import FullPageLoader from "@/components/fullPageLoader";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { parseAsSafeString } from "@/lib/queryParamsParser";
import { getErrorMessage, useGetCustomersQuery, useGetTeamsQuery } from "@/lib/store";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { parseAsInteger, useQueryStates } from "nuqs";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";

const POLLING_INTERVAL = 5000;
const PAGE_SIZE = 25;

export default function GovernanceCustomersPage() {
	const hasTeamsAccess = useRbac(RbacResource.Teams, RbacOperation.View);
	const hasCustomersAccess = useRbac(RbacResource.Customers, RbacOperation.View);
	const shownErrorsRef = useRef(new Set<string>());
	// Background refetches replace the rows behind an open edit sheet, which is
	// how in-progress edits used to get discarded. Hold the poll while a sheet
	// is open; it resumes as soon as the operator is done.
	const [isSheetOpen, setIsSheetOpen] = useState(false);

	const [urlState, setUrlState] = useQueryStates(
		{
			search: parseAsSafeString.withDefault(""),
			offset: parseAsInteger.withDefault(0),
		},
		{ history: "push" },
	);

	const debouncedSearch = useDebouncedValue(urlState.search, 300);

	const {
		data: teamsData,
		error: teamsError,
		isLoading: teamsLoading,
	} = useGetTeamsQuery(undefined, {
		skip: !hasTeamsAccess,
		pollingInterval: isSheetOpen ? 0 : POLLING_INTERVAL,
		skipPollingIfUnfocused: true,
	});
	const {
		data: customersData,
		error: customersError,
		isLoading: customersLoading,
	} = useGetCustomersQuery(
		{
			limit: PAGE_SIZE,
			offset: urlState.offset,
			search: debouncedSearch || undefined,
		},
		{
			skip: !hasCustomersAccess,
			pollingInterval: isSheetOpen ? 0 : POLLING_INTERVAL,
			skipPollingIfUnfocused: true,
		},
	);

	const customersTotal = customersData?.total_count ?? 0;

	// Snap offset back when total shrinks past current page (e.g. delete last item on last page)
	useEffect(() => {
		if (!customersData || urlState.offset < customersTotal) return;
		// Nothing to snap back to on an empty list, and setUrlState pushes a
		// history entry even when the value is unchanged.
		if (customersTotal === 0 && urlState.offset === 0) return;
		setUrlState({ offset: customersTotal === 0 ? 0 : Math.floor((customersTotal - 1) / PAGE_SIZE) * PAGE_SIZE });
	}, [customersTotal, urlState.offset]);

	const isLoading = teamsLoading || customersLoading;

	useEffect(() => {
		if (!teamsError && !customersError) {
			shownErrorsRef.current.clear();
			return;
		}
		const errorKey = `${!!teamsError}-${!!customersError}`;
		if (shownErrorsRef.current.has(errorKey)) return;
		shownErrorsRef.current.add(errorKey);
		if (teamsError && customersError) {
			toast.error("Failed to load governance data.");
		} else {
			if (teamsError) toast.error(`Failed to load teams: ${getErrorMessage(teamsError)}`);
			if (customersError) toast.error(`Failed to load customers: ${getErrorMessage(customersError)}`);
		}
	}, [teamsError, customersError]);

	if (isLoading) {
		return <FullPageLoader />;
	}

	return (
		<div className="no-padding-parent mx-auto flex h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] w-full flex-col p-4">
			<CustomersTable
				customers={customersData?.customers || []}
				totalCount={customersData?.total_count || 0}
				teams={teamsData?.teams || []}
				search={urlState.search}
				debouncedSearch={debouncedSearch}
				onSearchChange={(val) => setUrlState({ search: val || null, offset: 0 })}
				offset={urlState.offset}
				limit={PAGE_SIZE}
				onOffsetChange={(newOffset) => setUrlState({ offset: newOffset })}
				onSheetOpenChange={setIsSheetOpen}
			/>
		</div>
	);
}