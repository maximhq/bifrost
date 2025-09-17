"use client";

import FullPageLoader from "@/components/fullPageLoader";
import { getErrorMessage, useGetCoreConfigQuery, useGetCustomersQuery, useGetTeamsQuery, useGetVirtualKeysQuery } from "@/lib/store";
import { cn } from "@/lib/utils";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import CustomersTable from "./views/customerTable";
import TeamsTable from "./views/teamsTable";

export default function TeamsCustomersPage() {
	const [activeTab, setActiveTab] = useState("teams");

	// Fetch all data with RTK Query
	const { data: virtualKeysData, error: vkError, isLoading: vkLoading, refetch: refetchVirtualKeys } = useGetVirtualKeysQuery();
	const { data: teamsData, error: teamsError, isLoading: teamsLoading, refetch: refetchTeams } = useGetTeamsQuery({});
	const { data: customersData, error: customersError, isLoading: customersLoading, refetch: refetchCustomers } = useGetCustomersQuery();
	const { data: coreConfig, error: configError, isLoading: configLoading } = useGetCoreConfigQuery({ fromDB: true });

	const isLoading = vkLoading || teamsLoading || customersLoading || configLoading;

	// Handle errors
	useEffect(() => {
		if (configLoading) return;
		if (configError) {
			toast.error(`Failed to load core config: ${getErrorMessage(configError)}`);
			return;
		}

		if (coreConfig && !coreConfig?.client_config?.enable_governance) {
			toast.error("Governance is not enabled. Please enable it in the core settings.");
			return;
		}

		if (vkError) {
			toast.error(`Failed to load virtual keys: ${getErrorMessage(vkError)}`);
		}

		if (teamsError) {
			toast.error(`Failed to load teams: ${getErrorMessage(teamsError)}`);
		}

		if (customersError) {
			toast.error(`Failed to load customers: ${getErrorMessage(customersError)}`);
		}
	}, [configError, coreConfig, vkError, teamsError, customersError]);

	const handleRefresh = () => {
		refetchVirtualKeys();
		refetchTeams();
		refetchCustomers();
	};

	if (isLoading) {
		return <FullPageLoader />;
	}

	return (
		<div className="flex w-full flex-row gap-4">
			<div className="flex min-w-[200px] flex-col gap-1 rounded-md bg-zinc-50/50 p-4 dark:bg-zinc-800/20">
				{["teams", "customers"].map((tab) => (
					<div
						key={tab}
						className={cn(
							"mb-1 flex w-full items-center gap-2 rounded-sm border px-3 py-1.5 text-sm",
							activeTab === tab
								? "bg-secondary opacity-100 hover:opacity-100"
								: "hover:bg-secondary cursor-pointer border-transparent opacity-100 hover:border",
						)}
						onClick={() => setActiveTab(tab)}
					>
						{tab.replace("-", " ").charAt(0).toUpperCase() + tab.replace("-", " ").slice(1)}
					</div>
				))}
			</div>
			<div className="w-full pt-4">
				{activeTab === "teams" && (
					<TeamsTable
						teams={teamsData?.teams || []}
						customers={customersData?.customers || []}
						virtualKeys={virtualKeysData?.virtual_keys || []}
						onRefresh={handleRefresh}
					/>
				)}
				{activeTab === "customers" && (
					<CustomersTable
						customers={customersData?.customers || []}
						teams={teamsData?.teams || []}
						virtualKeys={virtualKeysData?.virtual_keys || []}
						onRefresh={handleRefresh}
					/>
				)}
			</div>
		</div>
	);
}
