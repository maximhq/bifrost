import Provider from "@/components/provider";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ModelProvider } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	ApiStructureFormFragment,
	BetaHeadersFormFragment,
	GovernanceFormFragment,
	OpenAIConfigFormFragment,
	ProxyFormFragment,
} from "../fragments";
import { DebuggingFormFragment } from "../fragments/debuggingFormFragment";
import { NetworkFormFragment } from "../fragments/networkFormFragment";
import { PerformanceFormFragment } from "../fragments/performanceFormFragment";

interface Props {
	show: boolean;
	onCancel: () => void;
	provider: ModelProvider;
}

const ANTHROPIC_FAMILY_PROVIDERS = ["anthropic", "vertex", "bedrock", "azure"];

const availableTabs = (
	t: (key: string) => string,
	hasCustomProviderConfig: boolean,
	hasGovernanceAccess: boolean,
	isOpenAI: boolean,
	isAnthropicFamily: boolean,
) => {
	const tabs = [];
	if (hasCustomProviderConfig) {
		tabs.push({
			id: "api-structure",
			label: t("workspace.providers.configSheet.apiStructure"),
		});
	}
	tabs.push({
		id: "network",
		label: t("workspace.providers.configSheet.network"),
	});
	tabs.push({
		id: "proxy",
		label: t("workspace.providers.configSheet.proxy"),
	});
	tabs.push({
		id: "performance",
		label: t("workspace.providers.configSheet.performance"),
	});
	if (hasGovernanceAccess) {
		tabs.push({
			id: "governance",
			label: t("workspace.providers.configSheet.governance"),
		});
	}
	if (isAnthropicFamily) {
		tabs.push({
			id: "beta-headers",
			label: t("workspace.providers.configSheet.betaHeaders"),
		});
	}
	tabs.push({
		id: "debugging",
		label: t("workspace.providers.configSheet.debugging"),
	});
	if (isOpenAI) {
		tabs.push({
			id: "openai-config",
			label: t("workspace.providers.configSheet.openaiConfig"),
		});
	}
	return tabs;
};

export default function ProviderConfigSheet({ show, onCancel, provider }: Props) {
	const { t } = useTranslation();
	const [selectedTab, setSelectedTab] = useState<string | undefined>(undefined);
	const hasGovernanceAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	const hasCustomProviderConfig = !!provider.custom_provider_config;
	const isOpenAI = provider.name === "openai";
	const isAnthropicFamily = ANTHROPIC_FAMILY_PROVIDERS.includes(provider.name.toLowerCase());

	const tabs = useMemo(() => {
		return availableTabs(t, hasCustomProviderConfig, hasGovernanceAccess, isOpenAI, isAnthropicFamily);
	}, [t, hasCustomProviderConfig, hasGovernanceAccess, isOpenAI, isAnthropicFamily]);

	useEffect(() => {
		setSelectedTab((previousTab) => {
			if (previousTab && tabs.some((tab) => tab.id === previousTab)) {
				return previousTab;
			}

			return tabs[0]?.id;
		});
	}, [tabs]);

	return (
		<Sheet
			open={show}
			onOpenChange={(open) => {
				if (!open) onCancel();
			}}
		>
			<SheetContent className="p-0 pt-4 sm:max-w-[50%]">
				<SheetHeader className="flex flex-col items-start px-8 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10">
					<SheetTitle>
						<div className="font-lg flex items-center gap-2">
							<div className="flex items-center">
								<Provider provider={provider.name} size={24} className="mt-0" />
							</div>
							{t("workspace.providers.configTitle")}
						</div>
					</SheetTitle>
				</SheetHeader>
				<div className="px-8 py-4">
					<div className="w-full rounded-sm border">
						<Tabs defaultValue={tabs[0]?.id} value={selectedTab} onValueChange={setSelectedTab}>
							<div className="custom-scrollbar mb-4 w-full overflow-x-auto">
								<TabsList className="h-10 w-max min-w-full justify-start rounded-tl-sm rounded-tr-sm rounded-br-none rounded-bl-none">
									{tabs.map((tab) => (
										<TabsTrigger
											key={tab.id}
											value={tab.id}
											data-testid={`provider-tab-${tab.id}`}
											className="flex-none px-3 whitespace-nowrap"
										>
											{tab.label}
										</TabsTrigger>
									))}
								</TabsList>
							</div>
							<TabsContent value="api-structure">
								<ApiStructureFormFragment provider={provider} />
							</TabsContent>
							<TabsContent value="openai-config">
								<OpenAIConfigFormFragment provider={provider} />
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
							<TabsContent value="beta-headers">
								<BetaHeadersFormFragment provider={provider} />
							</TabsContent>
							<TabsContent value="debugging">
								<DebuggingFormFragment provider={provider} />
							</TabsContent>
						</Tabs>
					</div>
				</div>
			</SheetContent>
		</Sheet>
	);
}