import Provider from "@/components/provider";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ModelProvider } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useEffect, useMemo, useState } from "react";
import {
	ApiStructureFormFragment,
	BetaHeadersFormFragment,
	GovernanceFormFragment,
	InjectedToolsFormFragment,
	OpenAIConfigFormFragment,
	PassthroughFormFragment,
	PricingFormFragment,
	ProxyFormFragment,
} from "../fragments";
import { sendsTypesafeHeaders } from "../fragments/betaHeadersFormFragment";
import { DebuggingFormFragment } from "../fragments/debuggingFormFragment";
import { PromptCacheFormFragment } from "../fragments/promptCacheFormFragment";
import { NetworkFormFragment } from "../fragments/networkFormFragment";
import { PerformanceFormFragment } from "../fragments/performanceFormFragment";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import { passthroughRoutePrefix } from "../fragments/passthroughForm.utils";

interface Props {
	show: boolean;
	onCancel: () => void;
	provider: ModelProvider;
}

const ANTHROPIC_FAMILY_PROVIDERS = ["anthropic", "vertex", "bedrock", "bedrock_mantle", "azure"];

// Providers whose openai_config (disable_store) is honoured, mapped to their config tab label.
const OPENAI_CONFIG_TAB_LABELS: Record<string, string> = {
	openai: "OpenAI Config",
	fireworks: "OpenAI Config",
};

const availableTabs = (
	hasCustomProviderConfig: boolean,
	hasGovernanceAccess: boolean,
	openAIConfigTabLabel: string | undefined,
	hasBetaHeaders: boolean,
	hasPassthrough: boolean,
	t: TFunction<"models">,
) => {
	const tabs = [];
	if (hasCustomProviderConfig) {
		tabs.push({
			id: "api-structure",
			label: t("providers.apiStructure"),
		});
	}
	tabs.push({
		id: "network",
		label: t("providers.networkTab"),
	});
	tabs.push({
		id: "proxy",
		label: t("providers.proxy"),
	});
	tabs.push({
		id: "performance",
		label: t("providers.performance"),
	});
	if (hasGovernanceAccess) {
		tabs.push({
			id: "governance",
			label: t("providers.governance"),
		});
	}
	if (hasBetaHeaders) {
		tabs.push({
			id: "beta-headers",
			label: t("providers.betaHeadersTab"),
		});
	}
	tabs.push({
		id: "prompt-cache",
		label: t("providers.promptCaching"),
	});
	tabs.push({
		id: "pricing",
		label: t("modelCatalog.pricing"),
	});
	tabs.push({
		id: "tools",
		label: t("providers.tools"),
	});
	if (hasPassthrough) {
		tabs.push({
			id: "passthrough",
			label: t("providers.allowedRequests.types.passthrough"),
		});
	}
	tabs.push({
		id: "debugging",
		label: t("providers.debuggingTab"),
	});
	if (openAIConfigTabLabel) {
		tabs.push({
			id: "openai-config",
			label: t("providers.openaiConfig"),
		});
	}
	return tabs;
};

export default function ProviderConfigSheet({ show, onCancel, provider }: Props) {
	const { t } = useTranslation("models");
	const [selectedTab, setSelectedTab] = useState<string | undefined>(undefined);
	const hasGovernanceAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	const hasCustomProviderConfig = !!provider.custom_provider_config;
	const openAIConfigTabLabel = OPENAI_CONFIG_TAB_LABELS[provider.name];
	const hasBetaHeaders = ANTHROPIC_FAMILY_PROVIDERS.includes(provider.name.toLowerCase()) || sendsTypesafeHeaders(provider);
	const hasPassthrough = passthroughRoutePrefix(provider) !== undefined;

	const tabs = useMemo(() => {
		return availableTabs(hasCustomProviderConfig, hasGovernanceAccess, openAIConfigTabLabel, hasBetaHeaders, hasPassthrough, t);
	}, [hasCustomProviderConfig, hasGovernanceAccess, openAIConfigTabLabel, hasBetaHeaders, hasPassthrough, t]);

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
				<SheetHeader className="flex flex-col items-start py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>
						<div className="font-lg flex items-center gap-2">
							<div className="flex items-center">
								<Provider provider={provider.name} size={24} className="mt-0" />
							</div>
							{t("providers.configurationTitle")}
						</div>
					</SheetTitle>
				</SheetHeader>
				<div className="px-4 py-4 md:px-8">
					<div className="w-full rounded-sm border">
						<Tabs defaultValue={tabs[0]?.id} value={selectedTab} onValueChange={setSelectedTab}>
							{/* TabsList collapses overflowing tabs into its own dropdown, so this
							    no longer needs to scroll horizontally. */}
							<div className="mb-4 w-full">
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
							<TabsContent value="prompt-cache">
								<PromptCacheFormFragment provider={provider} />
							</TabsContent>
							<TabsContent value="pricing">
								<PricingFormFragment provider={provider} />
							</TabsContent>
							<TabsContent value="tools">
								<InjectedToolsFormFragment provider={provider} />
							</TabsContent>
							<TabsContent value="passthrough">
								<PassthroughFormFragment provider={provider} />
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