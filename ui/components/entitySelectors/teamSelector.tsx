// Unified async team selector — the single way to pick a governance team
// (routing rules, model limits, pricing overrides).
//
// Lives in OSS because the governance teams endpoint (/governance/teams) is
// OSS. It reads the same teams table as the enterprise /teams list — the ids
// are interchangeable — so this is also what the Users & Groups filters pick
// with; /teams only differs in returning membership and business-unit fields
// that a picker has no use for.
//
// Single mode is prop-compatible with the model limit scope picker contract
// ({ value, onChange, disabled, fallbackOption }).

import {
	EntitySelector,
	ENTITY_SELECTOR_PAGE_SIZE,
	type EntitySelectorCommonProps,
	type EntityLabelResolverProps,
	type EntitySelectorModeProps,
	useEntitySelectorSearch,
} from "@/components/entitySelectors/entitySelector";
import { useGetTeamQuery, useGetTeamsQuery } from "@/lib/store";
import type { GetTeamsParams } from "@/lib/types/governance";
import { useEffect, useMemo } from "react";

function TeamLabelResolver({ id, onResolved }: EntityLabelResolverProps) {
	const { data } = useGetTeamQuery(id);
	const team = data?.team;
	useEffect(() => {
		if (team) onResolved({ value: id, label: team.name || id });
	}, [team, id, onResolved]);
	return null;
}

/** Server-side filters beyond search/limit, e.g. scoping to one customer. */
export type TeamSelectorFilters = Omit<GetTeamsParams, "limit" | "offset" | "search">;

interface TeamSelectorOwnProps extends EntitySelectorCommonProps {
	/** Page size for each search request. */
	limit?: number;
	/** Extra server-side filters merged into every request. */
	filters?: TeamSelectorFilters;
}

export type TeamSelectorProps = TeamSelectorOwnProps & EntitySelectorModeProps;

export function TeamSelector({ limit = ENTITY_SELECTOR_PAGE_SIZE, filters, ...props }: TeamSelectorProps) {
	const { open, setOpen, setSearch, debouncedSearch, skip, isDebouncing } = useEntitySelectorSearch();

	const { data: teamsData, isFetching, isError } = useGetTeamsQuery({ ...filters, limit, search: debouncedSearch || undefined }, { skip });

	// Memoized because multi mode hands this to react-select as defaultOptions,
	// which re-syncs on identity change.
	const options = useMemo(
		() =>
			(teamsData?.teams ?? []).map((team) => ({
				value: team.id,
				label: team.name || team.id,
				// The customer a team rolls up to is the only thing that
				// disambiguates two teams sharing a name.
				description: team.customer?.name,
			})),
		[teamsData],
	);

	return (
		<EntitySelector
			{...props}
			LabelResolver={TeamLabelResolver}
			entityLabel="team"
			entityLabelPlural="teams"
			options={options}
			isFetching={isFetching}
			isError={isError}
			open={open}
			onOpenChange={setOpen}
			onSearchChange={setSearch}
			isSearching={isDebouncing || (isFetching && !!debouncedSearch)}
			debouncedSearch={debouncedSearch}
		/>
	);
}