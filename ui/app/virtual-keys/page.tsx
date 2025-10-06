"use client";

import FullPageLoader from "@/components/fullPageLoader";
import {
	getErrorMessage,
	useLazyGetCustomersQuery,
	useLazyGetTeamsQuery,
	useLazyGetVirtualKeysQuery,
	useLazyGetCoreConfigQuery,
} from "@/lib/store";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import VirtualKeysTable from "./views/virtualKeysTable";

export default function VirtualKeysPage() {
	const [governanceEnabled, setGovernanceEnabled] = useState<boolean | null>(null);

	const [triggerGetVirtualKeys, { data: virtualKeysData, error: vkError, isLoading: vkLoading }] = useLazyGetVirtualKeysQuery();
	const [triggerGetTeams, { data: teamsData, error: teamsError, isLoading: teamsLoading }] = useLazyGetTeamsQuery();
	const [triggerGetCustomers, { data: customersData, error: customersError, isLoading: customersLoading }] = useLazyGetCustomersQuery();

	const isLoading = vkLoading || teamsLoading || customersLoading || governanceEnabled === null;

	const [triggerGetConfig] = useLazyGetCoreConfigQuery();

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
		<VirtualKeysTable
			virtualKeys={virtualKeysData?.virtual_keys || []}
			teams={teamsData?.teams || []}
			customers={customersData?.customers || []}
			onRefresh={handleRefresh}
		/>
	);
}
