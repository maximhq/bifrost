// Async relay selector, the single way to pick a relay (virtual key and access profile grants).
// Search and paging are server-side, through /api/relays?search=&limit=&offset=.

import {
	EntitySelector,
	ENTITY_SELECTOR_PAGE_SIZE,
	type EntityLabelResolverProps,
	type EntitySelectorCommonProps,
	type EntitySelectorModeProps,
	useEntitySelectorPages,
	useEntitySelectorSearch,
} from "@/components/entitySelectors/entitySelector";
import { useGetRelayQuery, useGetRelaysQuery } from "@/lib/store";
import { useEffect, useMemo } from "react";

function RelayLabelResolver({ id, onResolved }: EntityLabelResolverProps) {
	const { data } = useGetRelayQuery(id);
	useEffect(() => {
		if (data) onResolved({ value: id, label: data.name });
	}, [data, id, onResolved]);
	return null;
}

interface RelaySelectorOwnProps extends EntitySelectorCommonProps {
	/** Page size for each search request. */
	limit?: number;
}

export type RelaySelectorProps = RelaySelectorOwnProps & EntitySelectorModeProps;

export function RelaySelector({ limit = ENTITY_SELECTOR_PAGE_SIZE, ...props }: RelaySelectorProps) {
	const search = useEntitySelectorSearch();

	const { currentData, isFetching, isError, refetch } = useGetRelaysQuery(
		{ limit, offset: search.offset, search: search.debouncedSearch || undefined },
		{ skip: search.skip },
	);

	// Memoized because multi mode hands the merged list to react-select as
	// defaultOptions, which re-syncs on identity change.
	const entries = useMemo(
		() =>
			currentData?.relays?.map((relay) => ({
				value: relay.name,
				label: relay.name,
			})),
		[currentData],
	);

	const listProps = useEntitySelectorPages(search, { entries, totalCount: currentData?.total_count, isFetching, isError, refetch });

	return <EntitySelector {...props} LabelResolver={RelayLabelResolver} entityLabel="relay" entityLabelPlural="relays" {...listProps} />;
}