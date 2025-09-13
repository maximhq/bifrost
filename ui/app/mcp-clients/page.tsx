"use client";

import MCPClientsList from "@/app/config/views/mcp-clients-lists";
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
		<div>
			<MCPClientsList mcpClients={mcpClients || []} />
		</div>
	);
}
