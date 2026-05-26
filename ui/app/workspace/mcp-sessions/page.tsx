import FullPageLoader from "@/components/fullPageLoader";
import { getErrorMessage, useGetMCPSessionsQuery } from "@/lib/store";
import SessionsTable from "./views/sessionsTable";

export default function MCPSessionsPage() {
	const { data, isLoading, isError, error } = useGetMCPSessionsQuery();

	if (isLoading) {
		return <FullPageLoader />;
	}

	if (isError) {
		return (
			<div className="mx-auto w-full max-w-7xl">
				<div className="rounded-lg border border-destructive bg-destructive/10 p-6 text-sm text-destructive">
					Failed to load MCP sessions: {getErrorMessage(error)}
				</div>
			</div>
		);
	}

	return (
		<div className="mx-auto w-full max-w-7xl">
			<SessionsTable sessions={data?.sessions ?? []} />
		</div>
	);
}
