import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { isKnownProvider, ModelProvider } from "@/lib/types/config";
import { useEffect, useMemo, useState } from "react";
import { ApiStructureFormFragment, ProxyFormFragment } from "../fragments";
import { NetworkFormFragment } from "../fragments/networkFormFragment";
import { PerformanceFormFragment } from "../fragments/performanceFormFragment";
import ModelProviderKeysTableView from "./modelProviderKeysTableView";
import { keysRequired } from "./utils";

interface Props {
	provider: ModelProvider;
}

const availableTabs = (provider: ModelProvider) => {
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
	const isCustomProver = !isKnownProvider(provider.name);
	const tabs = useMemo(() => {
		return availableTabs(provider);
	}, [provider.name]);

	useEffect(() => {
		setSelectedTab(tabs[0]?.id);
	}, [tabs]);

	const showApiKeys = keysRequired(provider.name);

	return (
		<div className="flex w-full flex-col gap-2">
			<Accordion type="single" collapsible={showApiKeys} value={!showApiKeys || isCustomProver ? "item-1" : undefined}>
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
									<ApiStructureFormFragment provider={provider} showRestartAlert={true} />
								</TabsContent>
								<TabsContent value="network">
									<NetworkFormFragment provider={provider} showRestartAlert={true} />
								</TabsContent>
								<TabsContent value="proxy">
									<ProxyFormFragment provider={provider} showRestartAlert={true} />
								</TabsContent>
								<TabsContent value="performance">
									<PerformanceFormFragment provider={provider} />
								</TabsContent>
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
		</div>
	);
}
