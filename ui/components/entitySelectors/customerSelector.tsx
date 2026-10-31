// Unified async customer selector — the single way to pick a governance
// customer (routing rules, model limits, pricing overrides).
//
// Lives in OSS because /governance/customers is an OSS endpoint; there is no
// enterprise customers API.
//
// Single mode is prop-compatible with the model limit scope picker contract
// ({ value, onChange, disabled, fallbackOption }).

import {
	EntitySelector,
	ENTITY_SELECTOR_PAGE_SIZE,
	type EntitySelectorCommonProps,
	type EntityLabelResolverProps,
	type EntitySelectorModeProps,
	useEntitySelectorPages,
	useEntitySelectorSearch,
} from "@/components/entitySelectors/entitySelector";
import { useGetCustomerQuery, useGetCustomersQuery } from "@/lib/store";
import { useEffect, useMemo } from "react";

function CustomerLabelResolver({ id, onResolved }: EntityLabelResolverProps) {
	const { data } = useGetCustomerQuery(id);
	const customer = data?.customer;
	useEffect(() => {
		if (customer) onResolved({ value: id, label: customer.name || id });
	}, [customer, id, onResolved]);
	return null;
}

interface CustomerSelectorOwnProps extends EntitySelectorCommonProps {
	/** Page size for each search request. */
	limit?: number;
}

export type CustomerSelectorProps = CustomerSelectorOwnProps & EntitySelectorModeProps;

export function CustomerSelector({ limit = ENTITY_SELECTOR_PAGE_SIZE, ...props }: CustomerSelectorProps) {
	const search = useEntitySelectorSearch();

	const { currentData, isFetching, isError, refetch } = useGetCustomersQuery(
		{ limit, offset: search.offset, search: search.debouncedSearch || undefined },
		{ skip: search.skip },
	);

	// Memoized because multi mode hands the merged list to react-select as
	// defaultOptions, which re-syncs on identity change.
	const entries = useMemo(
		() =>
			currentData?.customers?.map((customer) => ({
				value: customer.id,
				label: customer.name || customer.id,
			})),
		[currentData],
	);

	const listProps = useEntitySelectorPages(search, { entries, totalCount: currentData?.total_count, isFetching, isError, refetch });

	return (
		<EntitySelector {...props} LabelResolver={CustomerLabelResolver} entityLabel="customer" entityLabelPlural="customers" {...listProps} />
	);
}