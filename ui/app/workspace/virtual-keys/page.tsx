"use client";

import FullPageLoader from "@/components/fullPageLoader";
import {
	getErrorMessage,
	useLazyGetCoreConfigQuery,
	useLazyGetCustomersQuery,
	useLazyGetTeamsQuery,
	useLazyGetVirtualKeysQuery,
} from "@/lib/store";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import VirtualKeysTable from "./views/virtualKeysTable";

export default function VirtualKeysPage() {
	const [governanceEnabled, setGovernanceEnabled] = useState<boolean | null>(null);
	const hasVirtualKeysAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.View);
	const hasTeamsAccess = useRbac(RbacResource.Teams, RbacOperation.View);
	const hasCustomersAccess = useRbac(RbacResource.Customers, RbacOperation.View);
	const [triggerGetVirtualKeys, { data: virtualKeysData, error: vkError, isLoading: vkLoading }] = useLazyGetVirtualKeysQuery();
	const [triggerGetTeams, { data: teamsData, error: teamsError, isLoading: teamsLoading }] = useLazyGetTeamsQuery();
	const [triggerGetCustomers, { data: customersData, error: customersError, isLoading: customersLoading }] = useLazyGetCustomersQuery();
	const shownErrorsRef = useRef(new Set<string>());
	const isLoading = vkLoading || teamsLoading || customersLoading || governanceEnabled === null;

	const [triggerGetConfig] = useLazyGetCoreConfigQuery();

	useEffect(() => {
		triggerGetConfig({ fromDB: true })
			.then((res) => {
				if (res.data?.client_config?.enable_governance) {
					setGovernanceEnabled(true);
					// Trigger lazy queries when governance is enabled
					if (hasVirtualKeysAccess) triggerGetVirtualKeys();
					if (hasTeamsAccess) triggerGetTeams({});
					if (hasCustomersAccess) triggerGetCustomers();
				} else {
					setGovernanceEnabled(false);
					toast.error("Governance is not enabled. Please enable it in the config.");
				}
			})
			.catch((err) => {
				console.error("Failed to fetch config:", err);
				setGovernanceEnabled(false);
				toast.error(getErrorMessage(err) || "Failed to load configuration");
			});
	}, [
		triggerGetConfig,
		triggerGetVirtualKeys,
		triggerGetTeams,
		triggerGetCustomers,
		hasVirtualKeysAccess,
		hasTeamsAccess,
		hasCustomersAccess,
	]);

	// Handle query errors - show consolidated error if all APIs fail
	useEffect(() => {
		const errorKey = `${!!vkError}-${!!teamsError}-${!!customersError}`;
		if (shownErrorsRef.current.has(errorKey)) return;
		shownErrorsRef.current.add(errorKey);
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
		<div className="mx-auto w-full max-w-7xl">
			<VirtualKeysTable
				virtualKeys={virtualKeysData?.virtual_keys || []}
				teams={teamsData?.teams || []}
				customers={customersData?.customers || []}
				onRefresh={handleRefresh}
			/>
		</div>
	);
}