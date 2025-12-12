"use client";

import FullPageLoader from "@/components/fullPageLoader";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { useGetCoreConfigQuery } from "@/lib/store";
import { cn } from "@/lib/utils";
import APIKeysView from "@enterprise/components/api-keys/APIKeysView";
import { Gauge, Globe, KeyRound, Landmark, Settings, Shield, Telescope, Zap } from "lucide-react";
import { useQueryState } from "nuqs";
import { useEffect } from "react";
import CachingView from "./views/cachingView";
import ClientSettingsView from "./views/clientSettingsView";
import GovernanceView from "./views/governanceView";
import LoggingView from "./views/loggingView";
import ObservabilityView from "./views/observabilityView";
import PerformanceTuningView from "./views/performanceTuningView";
import PricingConfigView from "./views/pricingConfigView";
import ProxyView from "./views/proxyView";
import SecurityView from "./views/securityView";

const baseTabs = [
	{
		id: "client-settings",
		label: "Client Settings",
		icon: <Settings className="size-4" />,
	},
	{
		id: "pricing-config",
		label: "Pricing Config",
		icon: <Landmark className="size-4" />,
	},
	{
		id: "logging",
		label: "Logging",
		icon: <Telescope className="size-4" />,
	},
	{
		id: "governance",
		label: "Governance",
		icon: <Landmark className="size-4" />,
	},
	{
		id: "caching",
		label: "Caching",
		icon: <Zap className="size-4" />,
	},
	{
		id: "observability",
		label: "Observability",
		icon: <Gauge className="size-4" />,
	},
	{
		id: "security",
		label: "Security",
		icon: <Shield className="size-4" />,
	},
	{
		id: "proxy",
		label: "Proxy",
		icon: <Globe className="size-4" />,
		enterpriseOnly: true,
	},
	{
		id: "api-keys",
		label: "API Keys",
		icon: <KeyRound className="size-4" />,
	},
	{
		id: "performance-tuning",
		label: "Performance Tuning",
		icon: <Zap className="size-4" />,
	},
];

const tabs = baseTabs.filter((tab) => !tab.enterpriseOnly || IS_ENTERPRISE);

export default function ConfigPage() {
	const [activeTab, setActiveTab] = useQueryState("tab");
	const { isLoading } = useGetCoreConfigQuery({ fromDB: true });

	useEffect(() => {
		const validTabIds = tabs.map((t) => t.id);
		if (!activeTab || !validTabIds.includes(activeTab)) {
			setActiveTab(tabs[0].id);
		}
	}, [activeTab, setActiveTab]);

	if (isLoading) {
		return <FullPageLoader />;
	}

	return (
		<div className="mx-auto flex w-full max-w-7xl flex-row gap-4">
			<div className="flex min-w-[250px] flex-col gap-1 rounded-md bg-zinc-50/50 p-4 dark:bg-zinc-800/20">
				{tabs.map((tab) => (
					<button
						key={tab.id}
						className={cn(
							"mb-1 flex w-full items-center gap-2 rounded-sm border px-3 py-1.5 text-sm",
							activeTab === tab.id
								? "bg-secondary opacity-100 hover:opacity-100"
								: "hover:bg-secondary cursor-pointer border-transparent opacity-100 hover:border",
						)}
						onClick={() => setActiveTab(tab.id)}
						type="button"
					>
						{tab.icon}
						<div>{tab.label}</div>
					</button>
				))}
			</div>
			<div className="w-full pt-4">
				{activeTab === "client-settings" && <ClientSettingsView />}
				{activeTab === "pricing-config" && <PricingConfigView />}
				{activeTab === "logging" && <LoggingView />}
				{activeTab === "governance" && <GovernanceView />}
				{activeTab === "caching" && <CachingView />}
				{activeTab === "observability" && <ObservabilityView />}
				{activeTab === "security" && <SecurityView />}
				{activeTab === "proxy" && IS_ENTERPRISE && <ProxyView />}
				{activeTab === "api-keys" && <APIKeysView />}
				{activeTab === "performance-tuning" && <PerformanceTuningView />}
			</div>
		</div>
	);
}
