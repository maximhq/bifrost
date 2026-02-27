"use client";

import { getErrorMessage, useGetModelConfigsQuery } from "@/lib/store";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useEffect } from "react";
import { toast } from "sonner";
import ModelLimitsTable from "./modelLimitsTable";

export default function ModelLimitsView() {
	const hasGovernanceAccess = useRbac(RbacResource.Governance, RbacOperation.View);

	// Use regular query with skip and polling
	const {
		data: modelConfigsData,
		error: modelConfigsError,
		isLoading: modelConfigsLoading,
	} = useGetModelConfigsQuery(undefined, {
		skip: !hasGovernanceAccess,
		pollingInterval: 5000,
	});

	// Handle query errors
	useEffect(() => {
		if (modelConfigsError) {
			toast.error(`Failed to load model configs: ${getErrorMessage(modelConfigsError)}`);
		}
	}, [modelConfigsError]);

	return (
		<div className="mx-auto w-full max-w-7xl">
			<ModelLimitsTable modelConfigs={modelConfigsData?.model_configs || []} />
		</div>
	);
}