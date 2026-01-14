"use client";

import FullPageLoader from "@/components/fullPageLoader";
import {
    getErrorMessage,
    useLazyGetCoreConfigQuery,
    useLazyGetCustomersQuery,
    useLazyGetTeamsQuery,
    useLazyGetVirtualKeysQuery,
} from "@/lib/store";
import { cn } from "@/lib/utils";
import UsersView from "@enterprise/components/user-groups/usersView";
import { Building, Users, WalletCards } from "lucide-react";
import { useQueryState } from "nuqs";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import CustomersTable from "./views/customerTable";
import TeamsTable from "./views/teamsTable";

const tabs = [
	{
		id: "users",
		label: "Users",
		icon: <Users className="size-4" />,
	},
	{
		id: "teams",
		label: "Teams",
		icon: <Building className="size-4" />,
	},
	{
		id: "customers",
		label: "Customers",
		icon: <WalletCards className="size-4" />,
	},
];

export default function TeamsCustomersPage() {
	const [activeTab, setActiveTab] = useQueryState("tab");

	const [governanceEnabled, setGovernanceEnabled] = useState<boolean | null>(null);

	// Lazy query hooks
	const [triggerGetVirtualKeys, { data: virtualKeysData, error: vkError, isLoading: vkLoading }] = useLazyGetVirtualKeysQuery();
	const [triggerGetTeams, { data: teamsData, error: teamsError, isLoading: teamsLoading }] = useLazyGetTeamsQuery();
	const [triggerGetCustomers, { data: customersData, error: customersError, isLoading: customersLoading }] = useLazyGetCustomersQuery();
	const [triggerGetConfig] = useLazyGetCoreConfigQuery();

	const isLoading = vkLoading || teamsLoading || customersLoading || governanceEnabled === null;

	// Check governance and trigger queries conditionally
	useEffect(() => {
		triggerGetConfig({ fromDB: true }).then((res) => {
			if (res.data && res.data.client_config.enable_governance) {
				setGovernanceEnabled(true);
				// Trigger lazy queries only when governance is enabled
				triggerGetVirtualKeys();
				triggerGetTeams({});
				triggerGetCustomers();
			} else {
				setGovernanceEnabled(false);
				toast.error("Governance is not enabled. Please enable it in the config.");
			}
		});
	}, [triggerGetConfig, triggerGetVirtualKeys, triggerGetTeams, triggerGetCustomers]);

	// Handle query errors - show consolidated error if all APIs fail
	useEffect(() => {
		if (vkError && teamsError && customersError) {
			// If all three APIs fail, suggest resetting bifrost
			toast.error("Failed to load governance data. Please reset Bifrost to enable governance properly.");
		} else {
			// Show individual errors if only some APIs fail
			if (vkError) {
				toast.error(`Failed to load virtual keys: ${getErrorMessage(vkError)}`);
			}
			if (teamsError) {
				toast.error(`Failed to load teams: ${getErrorMessage(teamsError)}`);
			}
			if (customersError) {
				toast.error(`Failed to load customers: ${getErrorMessage(customersError)}`);
			}
		}
	}, [vkError, teamsError, customersError]);

	useEffect(() => {
		if (!activeTab) {
			setActiveTab(tabs[0].id);
		}
	}, [activeTab, setActiveTab]);

	const handleRefresh = () => {
		if (governanceEnabled) {
			triggerGetVirtualKeys();
			triggerGetTeams({});
			triggerGetCustomers();
		}
	};

	if (isLoading) {
		return <FullPageLoader />;
	}

	return (
		<div className="flex w-full flex-row gap-4 max-w-7xl mx-auto">
			<div className="flex min-w-[200px] flex-col gap-1 rounded-md bg-zinc-50/50 p-4 dark:bg-zinc-800/20">
				{tabs.map((tab) => (
					<button
						key={tab.id}
						className={cn(
							"mb-1 flex w-full items-center gap-2 rounded-sm border px-3 py-1.5 text-sm",
							activeTab === tab.id
								? "bg-secondary opacity-100 hover:opacity-100"
								: "hover:bg-secondary cursor-pointer border-transparent opacity-100 hover:border",
						)}
						onClick={() => setActiveTab(tab.id)}
						type="button"
					>
						{tab.icon}
						{tab.label}
					</button>
				))}
			</div>
			<div className="w-full pt-4">
				{activeTab === "users" && <UsersView />}
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
