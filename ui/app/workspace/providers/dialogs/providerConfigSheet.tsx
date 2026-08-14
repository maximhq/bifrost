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

const ANTHROPIC_FAMILY_PROVIDERS = ["anthropic", "vertex", "bedrock", "bedrock_mantle", "azure"];

const availableTabs = (hasCustomProviderConfig: boolean, hasGovernanceAccess: boolean, isOpenAI: boolean, isAnthropicFamily: boolean) => {
	const tabs = [];
	if (hasCustomProviderConfig) {
		tabs.push({
			id: "api-structure",
			label: "API 结构",
		});
	}
	tabs.push({
		id: "network",
		label: "网络",
	});
	tabs.push({
		id: "proxy",
		label: "代理",
	});
	tabs.push({
		id: "performance",
		label: "性能",
	});
	if (hasGovernanceAccess) {
		tabs.push({
			id: "governance",
			label: "治理",
		});
	}
	if (isAnthropicFamily) {
		tabs.push({
			id: "beta-headers",
			label: "测试版请求头",
		});
	}
	tabs.push({
		id: "debugging",
		label: "调试",
	});
	if (isOpenAI) {
		tabs.push({
			id: "openai-config",
			label: "OpenAI 配置",
		});
	}
	return tabs;
};

export default function ProviderConfigSheet({ show, onCancel, provider }: Props) {
	const [selectedTab, setSelectedTab] = useState<string | undefined>(undefined);
	const hasGovernanceAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	const hasCustomProviderConfig = !!provider.custom_provider_config;
	const isOpenAI = provider.name === "openai";
	const isAnthropicFamily = ANTHROPIC_FAMILY_PROVIDERS.includes(provider.name.toLowerCase());

	const tabs = useMemo(() => {
		return availableTabs(hasCustomProviderConfig, hasGovernanceAccess, isOpenAI, isAnthropicFamily);
	}, [hasCustomProviderConfig, hasGovernanceAccess, isOpenAI, isAnthropicFamily]);

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
							</div>提供商配置</div>
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