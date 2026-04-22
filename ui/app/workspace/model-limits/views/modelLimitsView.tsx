import { getErrorMessage, useGetModelConfigsQuery } from "@/lib/store";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import ModelLimitsTable from "./modelLimitsTable";

const POLLING_INTERVAL = 5000;
const PAGE_SIZE = 25;

export default function ModelLimitsView() {
	const hasGovernanceAccess = useRbac(RbacResource.Governance, RbacOperation.View);

	const [search, setSearch] = useState("");
	const [offset, setOffset] = useState(0);

	const debouncedSearch = useDebouncedValue(search, 300);

	// Reset to first page when search changes
	useEffect(() => {
		setOffset(0);
	}, [debouncedSearch]);

	const { data: modelConfigsData, error: modelConfigsError } = useGetModelConfigsQuery(
		{
			limit: PAGE_SIZE,
			offset,
			search: debouncedSearch || undefined,
		},
		{
			skip: !hasGovernanceAccess,
			pollingInterval: POLLING_INTERVAL,
		},
	);

	const totalCount = modelConfigsData?.total_count ?? 0;

	// Snap offset back when total shrinks past current page (e.g. delete last item on last page)
	useEffect(() => {
		if (!modelConfigsData || offset < totalCount) return;
		setOffset(totalCount === 0 ? 0 : Math.floor((totalCount - 1) / PAGE_SIZE) * PAGE_SIZE);
	}, [totalCount, offset]);

	// Handle query errors
	useEffect(() => {
		if (modelConfigsError) {
			toast.error(`Failed to load model configs: ${getErrorMessage(modelConfigsError)}`);
		}
	}, [modelConfigsError]);

	return (
		<div className="mx-auto w-full max-w-7xl">
			<ModelLimitsTable
				modelConfigs={modelConfigsData?.model_configs || []}
				totalCount={modelConfigsData?.total_count || 0}
				search={search}
				debouncedSearch={debouncedSearch}
				onSearchChange={setSearch}
				offset={offset}
				limit={PAGE_SIZE}
				onOffsetChange={setOffset}
			/>
		</div>
	);
}