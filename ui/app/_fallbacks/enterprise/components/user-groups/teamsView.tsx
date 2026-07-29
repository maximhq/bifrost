import TeamsTable from "@/app/workspace/governance/views/teamsTable";
import FullPageLoader from "@/components/fullPageLoader";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { parseAsSafeString } from "@/lib/queryParamsParser";
import { getErrorMessage, useGetTeamsQuery } from "@/lib/store";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useEffect, useRef } from "react";
import { toast } from "sonner";

const POLLING_INTERVAL = 5000;
const PAGE_SIZE = 25;

// The teams list is the only thing fetched here. Customer names ride along on
// each team row (`customer`) and the virtual-key tally is server-computed
// (`virtual_key_count`), so neither needs its own unpaginated list request; the
// customer picker in the sheet fetches its own page on open.
export function TeamsView() {
	const hasTeamsAccess = useRbac(RbacResource.Teams, RbacOperation.View);
	const shownErrorsRef = useRef(new Set<string>());

	const [urlState, setUrlState] = useQueryStates(
		{
			search: parseAsSafeString.withDefault(""),
			offset: parseAsInteger.withDefault(0),
			selected_team: parseAsString.withDefault(""),
		},
		{ history: "push" },
	);

	const debouncedSearch = useDebouncedValue(urlState.search, 300);

	const {
		data: teamsData,
		error: teamsError,
		isLoading: teamsLoading,
		isFetching,
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

	useEffect(() => {
		if (!teamsError) {
			shownErrorsRef.current.clear();
			return;
		}
		const errorKey = `${!!teamsError}`;
		if (shownErrorsRef.current.has(errorKey)) return;
		shownErrorsRef.current.add(errorKey);
		toast.error(`Failed to load teams: ${getErrorMessage(teamsError)}`);
	}, [teamsError]);

	if (teamsLoading) {
		return <FullPageLoader />;
	}

	return (
		<TeamsTable
			teams={teamsData?.teams || []}
			totalCount={teamsData?.total_count || 0}
			search={urlState.search}
			debouncedSearch={debouncedSearch}
			onSearchChange={(val) => setUrlState({ search: val || null, offset: 0 }, { history: "replace" })}
			offset={urlState.offset}
			limit={PAGE_SIZE}
			onOffsetChange={(newOffset) => setUrlState({ offset: newOffset })}
			selectedTeamId={urlState.selected_team || null}
			onTeamAdd={() => setUrlState({ selected_team: "new" })}
			onTeamSelect={(team) => {
				setUrlState({ selected_team: team?.id ?? null });
			}}
			onDialogClose={() => setUrlState({ selected_team: null })}
			isLoading={isFetching}
		/>
	);
}
