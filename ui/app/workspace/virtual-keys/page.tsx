"use client";

import FullPageLoader from "@/components/fullPageLoader";
import {
	getErrorMessage,
	useLazyGetCoreConfigQuery,
	useGetVirtualKeysQuery,
	useGetTeamsQuery,
	useGetCustomersQuery,
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

	// Use regular queries with polling, skip until governance is enabled
	const {
		data: virtualKeysData,
		error: vkError,
		isLoading: vkLoading,
		refetch: refetchVirtualKeys,
	} = useGetVirtualKeysQuery(undefined, {
		skip: !governanceEnabled || !hasVirtualKeysAccess,
		pollingInterval: governanceEnabled && hasVirtualKeysAccess ? 10000 : 0,
		refetchOnFocus: true,
		skipPollingIfUnfocused: true,
	});

	const {
		data: teamsData,
		error: teamsError,
		isLoading: teamsLoading,
		refetch: refetchTeams,
	} = useGetTeamsQuery({}, {
		skip: !governanceEnabled || !hasTeamsAccess,
		pollingInterval: governanceEnabled && hasTeamsAccess ? 10000 : 0,
		refetchOnFocus: true,
		skipPollingIfUnfocused: true,
	});

	const {
		data: customersData,
		error: customersError,
		isLoading: customersLoading,
		refetch: refetchCustomers,
	} = useGetCustomersQuery(undefined, {
		skip: !governanceEnabled || !hasCustomersAccess,
		pollingInterval: governanceEnabled && hasCustomersAccess ? 10000 : 0,
		refetchOnFocus: true,
		skipPollingIfUnfocused: true,
	});

	const shownErrorsRef = useRef(new Set<string>());
	const isLoading = vkLoading || teamsLoading || customersLoading || governanceEnabled === null;

	const [triggerGetConfig] = useLazyGetCoreConfigQuery();

	useEffect(() => {
		triggerGetConfig({ fromDB: true })
			.then((res) => {
				if (res.data?.client_config?.enable_governance) {
					setGovernanceEnabled(true);
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
	}, [triggerGetConfig]);

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
			if (hasVirtualKeysAccess) {
				refetchVirtualKeys();
			}
			if (hasTeamsAccess) {
				refetchTeams();
			}
			if (hasCustomersAccess) {
				refetchCustomers();
			}
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
