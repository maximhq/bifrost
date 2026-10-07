// Server-backed searchable Virtual MCP selector; scales past one page of Virtual MCPs.

import {
	EntitySelector,
	ENTITY_SELECTOR_PAGE_SIZE,
	type EntitySelectorCommonProps,
	type EntityLabelResolverProps,
	type EntitySelectorModeProps,
	type EntitySelectorOption,
	useEntitySelectorPages,
	useEntitySelectorSearch,
} from "@/components/entitySelectors/entitySelector";
import { useGetVirtualMCPQuery, useGetVirtualMCPsQuery } from "@/lib/store";
import { useEffect, useMemo } from "react";

export type VirtualMCPSelectorOption = EntitySelectorOption;

// Resolves a selected vMCP id to its name when it's outside the search page.
function VirtualMCPLabelResolver({ id, onResolved }: EntityLabelResolverProps) {
	const { data } = useGetVirtualMCPQuery(Number(id));
	useEffect(() => {
		if (data) onResolved({ value: id, label: data.name || id });
	}, [data, id, onResolved]);
	return null;
}

interface VirtualMCPSelectorOwnProps extends EntitySelectorCommonProps {
	/** Page size for each search request. */
	limit?: number;
}

export type VirtualMCPSelectorProps = VirtualMCPSelectorOwnProps & EntitySelectorModeProps;

export function VirtualMCPSelector({ limit = ENTITY_SELECTOR_PAGE_SIZE, ...props }: VirtualMCPSelectorProps) {
	const search = useEntitySelectorSearch();

	const { currentData, isFetching, isError, refetch } = useGetVirtualMCPsQuery(
		{ limit, offset: search.offset, search: search.debouncedSearch || undefined },
		{ skip: search.skip },
	);

	const entries = useMemo(
		() =>
			currentData?.virtual_mcps?.map((vmcp) => ({
				value: String(vmcp.id),
				label: vmcp.name || String(vmcp.id),
				description: vmcp.endpoint_slug ? `/mcp/${vmcp.endpoint_slug}` : undefined,
			})),
		[currentData],
	);

	const listProps = useEntitySelectorPages(search, { entries, totalCount: currentData?.total_count, isFetching, isError, refetch });

	return (
		<EntitySelector
			{...props}
			LabelResolver={VirtualMCPLabelResolver}
			entityLabel="Virtual MCP"
			entityLabelPlural="Virtual MCPs"
			{...listProps}
		/>
	);
}