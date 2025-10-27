"use client";

import FullPageLoader from "@/components/fullPageLoader";
import { useGetCoreConfigQuery } from "@/lib/store";
import { cn } from "@/lib/utils";
import { Gauge, Landmark, Settings, Shield, Sliders, Zap } from "lucide-react";
import { useQueryState } from "nuqs";
import { useEffect } from "react";
import ClientSettingsView from "./views/clientSettingsView";
import FeatureTogglesView from "./views/featureTogglesView";
import ObservabilityView from "./views/observabilityView";
import PerformanceTuningView from "./views/performanceTuningView";
import PricingConfigView from "./views/pricingConfigView";
import SecurityView from "./views/securityView";

const tabs = [
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
		id: "feature-toggles",
		label: "Feature Toggles",
		icon: <Sliders className="size-4" />,
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
		id: "performance-tuning",
		label: "Performance Tuning",
		icon: <Zap className="size-4" />,
	},
];

export default function ConfigPage() {
	const [activeTab, setActiveTab] = useQueryState("tab");
	const { isLoading } = useGetCoreConfigQuery({ fromDB: true });

	useEffect(() => {
		if (!activeTab) {
			setActiveTab(tabs[0].id);
		}
	}, [activeTab, setActiveTab]);

	if (isLoading) {
		return <FullPageLoader />;
	}

	return (
		<div className="flex w-full flex-row gap-4 max-w-7xl mx-auto">
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
				{activeTab === "feature-toggles" && <FeatureTogglesView />}
				{activeTab === "observability" && <ObservabilityView />}
				{activeTab === "security" && <SecurityView />}
				{activeTab === "performance-tuning" && <PerformanceTuningView />}
			</div>
		</div>
	);
}
