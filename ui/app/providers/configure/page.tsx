"use client";

import { closeConfigureDialog, setSelectedProvider, useAppDispatch, useAppSelector, useGetProvidersQuery } from "@/lib/store";
import { useRouter } from "next/navigation";
import { useEffect } from "react";
import ProviderForm from "./provider-form";

export default function ConfigurePage() {
	const router = useRouter();
	const dispatch = useAppDispatch();
	const selectedProvider = useAppSelector((state) => state.provider.selectedProvider);
	const { data: providersData, refetch } = useGetProvidersQuery();

	// Auto-select first available provider when no provider is selected and data is loaded
	useEffect(() => {
		if (!selectedProvider && providersData?.providers && providersData.providers.length > 0) {
			const providerToSelect = providersData.providers[0];

			// Update Redux state to select this provider
			dispatch(setSelectedProvider(providerToSelect));
		}
	}, [selectedProvider, providersData?.providers, dispatch]);

	const handleSave = () => {
		refetch();
		dispatch(closeConfigureDialog());
		router.push("/providers");
	};

	const handleCancel = () => {
		dispatch(closeConfigureDialog());
		router.push("/providers");
	};

	return (
		<div className="container mx-auto py-8">
			<ProviderForm
				provider={selectedProvider}
				allProviders={providersData?.providers || []}
				existingProviders={providersData?.providers?.map((p) => p.name) || []}
				onSave={handleSave}
				onCancel={handleCancel}
			/>
		</div>
	);
}
