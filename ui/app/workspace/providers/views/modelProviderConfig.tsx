import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { isKnownProvider, ModelProvider } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useEffect, useMemo, useState } from "react";
import { ApiStructureFormFragment, ProxyFormFragment } from "../fragments";
import { NetworkFormFragment } from "../fragments/networkFormFragment";
import { PerformanceFormFragment } from "../fragments/performanceFormFragment";
import ModelProviderKeysTableView from "./modelProviderKeysTableView";
import ProviderGovernanceTable from "./providerGovernanceTable";
import { keysRequired } from "./utils";

interface Props {
	provider: ModelProvider;
}

const availableTabs = (provider: ModelProvider, hasGovernanceAccess: boolean) => {
	const availableTabs = [];
	// Custom Settings tab is available for custom providers
	if (provider?.custom_provider_config) {
		availableTabs.push({
			id: "api-structure",
			label: "API Structure",
		});
	}
	// Network tab is always available
	availableTabs.push({
		id: "network",
		label: "Network config",
	});

	availableTabs.push({
		id: "proxy",
		label: "Proxy config",
	});

	// Performance tab is always available
	availableTabs.push({
		id: "performance",
		label: "Performance tuning",
	});
	return availableTabs;
};

export default function ModelProviderConfig({ provider }: Props) {
	const [selectedTab, setSelectedTab] = useState<string | undefined>(undefined);
	const [accordionValue, setAccordionValue] = useState<string | undefined>(undefined);
	const isCustomProvider = !isKnownProvider(provider.name);
	const hasGovernanceAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	const tabs = useMemo(() => {
		return availableTabs(provider, hasGovernanceAccess);
	}, [provider.name, provider.custom_provider_config, hasGovernanceAccess]);

	const showApiKeys = useMemo(() => {
		if (provider.custom_provider_config) {
			return !(provider.custom_provider_config?.is_key_less ?? false);
		}
		return keysRequired(provider.name);
	}, [provider.name, provider.custom_provider_config?.is_key_less]);

	useEffect(() => {
		setSelectedTab(tabs[0]?.id);
	}, [tabs]);

	useEffect(() => {
		setAccordionValue(!showApiKeys || isCustomProvider ? "item-1" : undefined);
	}, [showApiKeys, isCustomProvider]);

	return (
		<div className="flex w-full flex-col gap-2">
			<Accordion type="single" collapsible={true} value={accordionValue} onValueChange={setAccordionValue}>
				<AccordionItem value="item-1">
					<AccordionTrigger className="flex cursor-pointer items-center text-[17px] font-semibold">
						Provider level configuration
					</AccordionTrigger>
					<AccordionContent>
						<div className="mb-2 w-full rounded-sm border">
							<Tabs defaultValue={tabs[0]?.id} value={selectedTab} onValueChange={setSelectedTab} className="space-y-6">
								<TabsList
									style={{ gridTemplateColumns: `repeat(${tabs.length + 3}, 1fr)` }}
									className={`mb-4 grid h-10 w-full rounded-tl-sm rounded-tr-sm rounded-br-none rounded-bl-none`}
								>
									{tabs.map((tab) => (
										<TabsTrigger key={tab.id} value={tab.id} className="flex items-center gap-2">
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
								{/* <TabsContent value="governance">
									<GovernanceFormFragment provider={provider} />
								</TabsContent> */}
							</Tabs>
						</div>
					</AccordionContent>
				</AccordionItem>
			</Accordion>
			{showApiKeys && (
				<>
					<div className="bg-accent h-[1px] w-full" />
					<ModelProviderKeysTableView className="mt-4" provider={provider} />
				</>
			)}
			{hasGovernanceAccess ? <ProviderGovernanceTable className="mt-4" provider={provider} /> : null}
		</div>
	);
}
