import Provider from "@/components/provider";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ModelProvider } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useEffect, useMemo, useState } from "react";
import { ApiStructureFormFragment, GovernanceFormFragment, ProxyFormFragment } from "../fragments";
import { DebuggingFormFragment } from "../fragments/debuggingFormFragment";
import { NetworkFormFragment } from "../fragments/networkFormFragment";
import { PerformanceFormFragment } from "../fragments/performanceFormFragment";

interface Props {
	show: boolean;
	onCancel: () => void;
	provider: ModelProvider;
}

const availableTabs = (provider: ModelProvider, hasGovernanceAccess: boolean) => {
	const tabs = [];
	if (provider?.custom_provider_config) {
		tabs.push({
			id: "api-structure",
			label: "API Structure",
		});
	}
	tabs.push({
		id: "network",
		label: "Network",
	});
	tabs.push({
		id: "proxy",
		label: "Proxy",
	});
	tabs.push({
		id: "performance",
		label: "Performance",
	});
	if (hasGovernanceAccess) {
		tabs.push({
			id: "governance",
			label: "Governance",
		});
	}
	tabs.push({
		id: "debugging",
		label: "Debugging",
	});
	return tabs;
};

export default function ProviderConfigSheet({ show, onCancel, provider }: Props) {
	const [selectedTab, setSelectedTab] = useState<string | undefined>(undefined);
	const hasGovernanceAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	
	const tabs = useMemo(() => {
		return availableTabs(provider, hasGovernanceAccess);
	}, [provider.name, provider.custom_provider_config, hasGovernanceAccess]);

	useEffect(() => {
		setSelectedTab(tabs[0]?.id);
	}, [tabs]);

	return (
		<Sheet
			open={show}
			onOpenChange={(open) => {
				if (!open) onCancel();
			}}
		>
			<SheetContent className="custom-scrollbar dark:bg-card bg-white p-8 sm:max-w-[50%]">
				<SheetHeader className="flex flex-col items-start">
					<SheetTitle>
						<div className="font-lg flex items-center gap-2">
							<div className="flex items-center">
								<Provider provider={provider.name} size={24} />
							</div>
							Provider configuration
						</div>
					</SheetTitle>
				</SheetHeader>
				<div className="w-full rounded-sm border">
					<Tabs defaultValue={tabs[0]?.id} value={selectedTab} onValueChange={setSelectedTab} className="space-y-6">
						<TabsList
							style={{ gridTemplateColumns: `repeat(${tabs.length}, 1fr)` }}
							className="mb-4 grid h-10 w-full rounded-tl-sm rounded-tr-sm rounded-br-none rounded-bl-none"
						>
							{tabs.map((tab) => (
								<TabsTrigger key={tab.id} value={tab.id} data-testid={`provider-tab-${tab.id}`} className="flex items-center gap-2">
									{tab.label}
								</TabsTrigger>
							))}
						</TabsList>
						<TabsContent value="api-structure">
							<ApiStructureFormFragment provider={provider} />
						</TabsContent>
						<TabsContent value="network">
							<NetworkFormFragment provider={provider} />
						</TabsContent>
						<TabsContent value="proxy">
							<ProxyFormFragment provider={provider} />
						</TabsContent>
						<TabsContent value="performance">
							<PerformanceFormFragment provider={provider} />
						</TabsContent>
						<TabsContent value="governance">
							<GovernanceFormFragment provider={provider} />
						</TabsContent>
						<TabsContent value="debugging">
							<DebuggingFormFragment provider={provider} />
						</TabsContent>
					</Tabs>
				</div>
			</SheetContent>
		</Sheet>
	);
}
