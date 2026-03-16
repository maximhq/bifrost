"use client";

import FullPageLoader from "@/components/fullPageLoader";
import { useToast } from "@/hooks/use-toast";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { getErrorMessage, useGetMCPClientsQuery } from "@/lib/store";
import { useEffect, useState } from "react";
import MCPClientsTable from "./views/mcpClientsTable";

const POLLING_INTERVAL = 5000;
const PAGE_SIZE = 25;

export default function MCPServersPage() {
	const [search, setSearch] = useState("");
	const [offset, setOffset] = useState(0);
	const debouncedSearch = useDebouncedValue(search, 300);

	// Reset to first page when search changes
	useEffect(() => {
		setOffset(0);
	}, [debouncedSearch]);

	const { data: mcpClientsData, error, isLoading, refetch } = useGetMCPClientsQuery(
		{
			limit: PAGE_SIZE,
			offset,
			search: debouncedSearch || undefined,
		},
		{
			pollingInterval: POLLING_INTERVAL,
		},
	);

	const mcpClients = mcpClientsData?.clients || [];
	const totalCount = mcpClientsData?.total_count || 0;

	// Snap offset back when total shrinks past current page (e.g. delete last item on last page)
	useEffect(() => {
		if (!mcpClientsData || offset < totalCount) return;
		setOffset(totalCount === 0 ? 0 : Math.floor((totalCount - 1) / PAGE_SIZE) * PAGE_SIZE);
	}, [totalCount, offset]);

	const { toast } = useToast();

	useEffect(() => {
		if (error) {
			toast({
				title: "Error",
				description: getErrorMessage(error),
				variant: "destructive",
			});
		}
	}, [error, toast]);

	if (isLoading) {
		return <FullPageLoader />;
	}

	return (
		<div className="mx-auto w-full max-w-7xl">
			<MCPClientsTable
				mcpClients={mcpClients}
				totalCount={totalCount}
				refetch={refetch}
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
