import FullPageLoader from "@/components/fullPageLoader";
import { useToast } from "@/hooks/use-toast";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { getErrorMessage, useGetMCPClientsQuery } from "@/lib/store";
import { parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useEffect } from "react";
import MCPClientsTable from "./views/mcpClientsTable";

const POLLING_INTERVAL = 5000;
const PAGE_SIZE = 25;

export default function MCPServersPage() {
	const [urlState, setUrlState] = useQueryStates(
		{
			search: parseAsString.withDefault(""),
			server: parseAsString.withDefault(""),
			offset: parseAsInteger.withDefault(0),
		},
		{ history: "replace" },
	);
	const debouncedSearch = useDebouncedValue(urlState.search, 300);

	const {
		data: mcpClientsData,
		error,
		isLoading,
		refetch,
	} = useGetMCPClientsQuery(
		{
			limit: PAGE_SIZE,
			offset: urlState.offset,
			search: debouncedSearch || undefined,
			server: urlState.server || undefined,
		},
		{
			pollingInterval: POLLING_INTERVAL,
		},
	);

	const mcpClients = mcpClientsData?.clients || [];
	const totalCount = mcpClientsData?.total_count || 0;

	// Snap offset back when total shrinks past current page (e.g. delete last item on last page)
	useEffect(() => {
		if (!mcpClientsData || urlState.offset < totalCount) return;
		void setUrlState({ offset: totalCount === 0 ? 0 : Math.floor((totalCount - 1) / PAGE_SIZE) * PAGE_SIZE });
	}, [totalCount, urlState.offset, mcpClientsData, setUrlState]);

	const { toast } = useToast();

	useEffect(() => {
		if (error) {
			const message = getErrorMessage(error);
			if (message.toLowerCase().includes("mcp is not configured in this bifrost instance")) return;
			toast({
				title: "Error",
				description: message,
				variant: "destructive",
			});
		}
	}, [error, toast]);

	if (isLoading) {
		return <FullPageLoader />;
	}

	const handleSearchChange = (value: string) => void setUrlState({ search: value, offset: 0 });
	const handleServerFilterClear = () => void setUrlState({ server: null, offset: 0 });
	const handleOffsetChange = (offset: number) => void setUrlState({ offset }, { history: "push" });

	return (
		<div className="mx-auto flex h-[calc(100dvh-50px)] w-full max-w-7xl flex-col">
			<MCPClientsTable
				mcpClients={mcpClients}
				totalCount={totalCount}
				refetch={refetch}
				search={urlState.search}
				debouncedSearch={debouncedSearch}
				server={urlState.server}
				onSearchChange={handleSearchChange}
				onServerFilterClear={handleServerFilterClear}
				offset={urlState.offset}
				limit={PAGE_SIZE}
				onOffsetChange={handleOffsetChange}
			/>
		</div>
	);
}