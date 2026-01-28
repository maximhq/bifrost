"use client";

import FullPageLoader from "@/components/fullPageLoader";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useGetMCPClientsQuery } from "@/lib/store";
import { useEffect } from "react";
import MCPClientsTable from "./views/mcpClientsTable";

export default function MCPServersPage() {
	const { data: mcpClients, error, isLoading, refetch } = useGetMCPClientsQuery();

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
			<MCPClientsTable mcpClients={mcpClients || []} refetch={refetch} />
		</div>
	);
}
