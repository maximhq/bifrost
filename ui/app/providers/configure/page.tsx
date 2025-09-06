"use client";

import {
	Breadcrumb,
	BreadcrumbItem,
	BreadcrumbLink,
	BreadcrumbList,
	BreadcrumbPage,
	BreadcrumbSeparator,
} from "@/components/ui/breadcrumb";
import { ProviderSidebar } from "./provider-sidebar";

import { closeConfigureDialog, setSelectedProvider, useAppDispatch, useAppSelector, useGetProvidersQuery } from "@/lib/store";
import { useRouter } from "next/navigation";

export default function ConfigurePage() {
	const router = useRouter();
	const dispatch = useAppDispatch();
	const selectedProvider = useAppSelector((state) => state.provider.selectedProvider);
	const { data: providersData, refetch } = useGetProvidersQuery();

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
			<div className="-mt-6 flex w-full flex-col gap-6">
				<Breadcrumb>
					<BreadcrumbList>
						<BreadcrumbItem>
							<BreadcrumbLink href="/providers">Providers</BreadcrumbLink>
						</BreadcrumbItem>
						<BreadcrumbSeparator />
						<BreadcrumbItem>
							<BreadcrumbPage>Configure provider</BreadcrumbPage>
						</BreadcrumbItem>
					</BreadcrumbList>
				</Breadcrumb>
				<div className="flex gap-4">
					<ProviderSidebar
						selectedProvider={selectedProvider?.name || ""}
						allProviders={providersData?.providers || []}
						onProviderSelect={(provider, providerName) => {
							dispatch(setSelectedProvider(provider));
						}}
						onCreateCustomProvider={() => {}}
					/>
				</div>
			</div>
		</div>
	);
}
