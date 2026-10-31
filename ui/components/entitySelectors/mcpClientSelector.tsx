// Server-backed searchable MCP client selector; scales past one page of clients.

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
import { useGetMCPClientsQuery } from "@/lib/store";
import { useEffect, useMemo } from "react";

export type MCPClientSelectorOption = EntitySelectorOption;

// Resolves a selected client id to its name when it's outside the search page.
function MCPClientLabelResolver({ id, onResolved }: EntityLabelResolverProps) {
	const { data } = useGetMCPClientsQuery({ server: id, limit: 1 });
	const client = data?.clients?.[0];
	useEffect(() => {
		if (client) onResolved({ value: id, label: client.config.name || id });
	}, [client, id, onResolved]);
	return null;
}

interface MCPClientSelectorOwnProps extends EntitySelectorCommonProps {
	/** Page size for each search request. */
	limit?: number;
}

export type MCPClientSelectorProps = MCPClientSelectorOwnProps & EntitySelectorModeProps;

export function MCPClientSelector({ limit = ENTITY_SELECTOR_PAGE_SIZE, ...props }: MCPClientSelectorProps) {
	const search = useEntitySelectorSearch();

	const { currentData, isFetching, isError, refetch } = useGetMCPClientsQuery(
		{ limit, offset: search.offset, search: search.debouncedSearch || undefined },
		{ skip: search.skip },
	);

	const entries = useMemo(
		() =>
			currentData?.clients?.map((client) => ({
				value: client.config.client_id,
				label: client.config.name || client.config.client_id,
			})),
		[currentData],
	);

	const listProps = useEntitySelectorPages(search, { entries, totalCount: currentData?.total_count, isFetching, isError, refetch });

	return (
		<EntitySelector
			{...props}
			LabelResolver={MCPClientLabelResolver}
			entityLabel="MCP server"
			entityLabelPlural="MCP servers"
			{...listProps}
		/>
	);
}