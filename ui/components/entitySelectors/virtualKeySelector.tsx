// Unified async virtual key selector — the single way to pick virtual keys
// across governance surfaces (model limits, pricing overrides, routing rules,
// MCP clients).
//
// Single mode is prop-compatible with the model limit scope picker contract
// ({ value, onChange, disabled, fallbackOption }), so it can be registered
// as-is in lib/registries/modelLimitScopes.tsx.

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
import { useGetVirtualKeyQuery, useGetVirtualKeysQuery } from "@/lib/store";
import type { GetVirtualKeysParams } from "@/lib/types/governance";
import { useEffect, useMemo } from "react";

function VirtualKeyLabelResolver({ id, onResolved }: EntityLabelResolverProps) {
	const { data } = useGetVirtualKeyQuery(id);
	const virtualKey = data?.virtual_key;
	useEffect(() => {
		if (virtualKey) onResolved({ value: id, label: virtualKey.name || id });
	}, [virtualKey, id, onResolved]);
	return null;
}

export type VirtualKeySelectorOption = EntitySelectorOption;

/** Server-side filters beyond search/limit, e.g. scoping to a team or customer. */
export type VirtualKeySelectorFilters = Omit<GetVirtualKeysParams, "limit" | "offset" | "search" | "export">;

interface VirtualKeySelectorOwnProps extends EntitySelectorCommonProps {
	/** Page size for each search request. */
	limit?: number;
	/** Extra server-side filters merged into every request. */
	filters?: VirtualKeySelectorFilters;
}

export type VirtualKeySelectorProps = VirtualKeySelectorOwnProps & EntitySelectorModeProps;

export function VirtualKeySelector({ limit = ENTITY_SELECTOR_PAGE_SIZE, filters, ...props }: VirtualKeySelectorProps) {
	const search = useEntitySelectorSearch();

	const { currentData, isFetching, isError, refetch } = useGetVirtualKeysQuery(
		{ ...filters, limit, offset: search.offset, search: search.debouncedSearch || undefined },
		{ skip: search.skip },
	);

	// Memoized because multi mode hands the merged list to react-select as
	// defaultOptions, which re-syncs on identity change. vk.value is the secret
	// itself and is deliberately never rendered.
	const entries = useMemo(
		() =>
			currentData?.virtual_keys?.map((vk) => ({
				value: vk.id,
				label: vk.name || vk.id,
				description: vk.description,
			})),
		[currentData],
	);

	const listProps = useEntitySelectorPages(search, { entries, totalCount: currentData?.total_count, isFetching, isError, refetch });

	return (
		<EntitySelector
			{...props}
			LabelResolver={VirtualKeyLabelResolver}
			entityLabel="virtual key"
			entityLabelPlural="virtual keys"
			{...listProps}
		/>
	);
}