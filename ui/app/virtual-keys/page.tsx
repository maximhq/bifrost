"use client";

import FullPageLoader from "@/components/fullPageLoader";
import { getErrorMessage, useGetCustomersQuery, useGetTeamsQuery, useGetVirtualKeysQuery } from "@/lib/store";
import { useEffect } from "react";
import { toast } from "sonner";
import VirtualKeysTable from "./views/virtualKeysTable";

export default function VirtualKeysPage() {
	const { data: virtualKeysData, error: vkError, isLoading: vkLoading, refetch: refetchVirtualKeys } = useGetVirtualKeysQuery();
	const { data: teamsData, error: teamsError, isLoading: teamsLoading, refetch: refetchTeams } = useGetTeamsQuery({});
	const { data: customersData, error: customersError, isLoading: customersLoading, refetch: refetchCustomers } = useGetCustomersQuery();

	const isLoading = vkLoading || teamsLoading || customersLoading;

	useEffect(() => {
		if (vkError) {
			toast.error(`Failed to load virtual keys: ${getErrorMessage(vkError)}`);
		}
	}, [vkError]);

	useEffect(() => {
		if (teamsError) {
			toast.error(`Failed to load teams: ${getErrorMessage(teamsError)}`);
		}
	}, [teamsError]);

	useEffect(() => {
		if (customersError) {
			toast.error(`Failed to load customers: ${getErrorMessage(customersError)}`);
		}
	}, [customersError]);

	const handleRefresh = () => {
		refetchVirtualKeys();
		refetchTeams();
		refetchCustomers();
	};

	if (isLoading) {
		return <FullPageLoader />;
	}

	return (
		<VirtualKeysTable
			virtualKeys={virtualKeysData?.virtual_keys || []}
			teams={teamsData?.teams || []}
			customers={customersData?.customers || []}
			onRefresh={handleRefresh}
		/>
	);
}
