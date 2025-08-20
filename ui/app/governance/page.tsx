"use client";

import FullPageLoader from "@/components/full-page-loader";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { getErrorMessage, useGetCoreConfigQuery, useGetCustomersQuery, useGetTeamsQuery, useGetVirtualKeysQuery } from "@/lib/store";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import CustomersTable from "./views/customers-table";
import TeamsTable from "./views/teams-table";
import VirtualKeysTable from "./views/virtual-keys-table";

export default function GovernancePage() {
	const [activeTab, setActiveTab] = useState("virtual-keys");

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
		<div className="">
			<Tabs value={activeTab} onValueChange={setActiveTab} className="w-full">
				<TabsList className="mb-4 grid h-12 w-full grid-cols-3">
					{["virtual-keys", "teams", "customers"].map((tab) => (
						<TabsTrigger key={tab} value={tab} className="flex items-center gap-2 capitalize transition-all duration-200 ease-in-out">
							{tab.replace("-", " ")}
						</TabsTrigger>
					))}
				</TabsList>

				<div className="">
					<TabsContent value="virtual-keys" className="mt-0">
						<VirtualKeysTable
							virtualKeys={virtualKeysData?.virtual_keys || []}
							teams={teamsData?.teams || []}
							customers={customersData?.customers || []}
							onRefresh={handleRefresh}
						/>
					</TabsContent>
					<TabsContent value="teams" className="mt-0">
						<TeamsTable
							teams={teamsData?.teams || []}
							customers={customersData?.customers || []}
							virtualKeys={virtualKeysData?.virtual_keys || []}
							onRefresh={handleRefresh}
						/>
					</TabsContent>
					<TabsContent value="customers" className="mt-0">
						<CustomersTable
							customers={customersData?.customers || []}
							teams={teamsData?.teams || []}
							virtualKeys={virtualKeysData?.virtual_keys || []}
							onRefresh={handleRefresh}
						/>
					</TabsContent>
				</div>
			</Tabs>
		</div>
	);
}
