"use client";

import MCPClientsTable from "@/app/workspace/mcp-gateway/views/mcpClientsTable";
import FullPageLoader from "@/components/fullPageLoader";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useGetMCPClientsQuery } from "@/lib/store";
import { useEffect } from "react";

export default function MCPServersPage() {
	const { data: mcpClients, error, isLoading } = useGetMCPClientsQuery();

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
			<MCPClientsTable mcpClients={mcpClients || []} />
		</div>
	);
}
