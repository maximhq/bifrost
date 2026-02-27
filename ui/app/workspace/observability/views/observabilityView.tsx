"use client";

import FullPageLoader from "@/components/fullPageLoader";
import { Badge } from "@/components/ui/badge";
import { setSelectedPlugin, useAppDispatch, useAppSelector, useGetPluginsQuery } from "@/lib/store";
import { cn } from "@/lib/utils";
import { useTheme } from "next-themes";
import Image from "next/image";
import { useQueryState } from "nuqs";
import { useEffect, useMemo } from "react";
import BigQueryView from "./plugins/bigqueryView";
import DatadogView from "./plugins/datadogView";
import MaximView from "./plugins/maximView";
import NewrelicView from "./plugins/newRelicView";
import OtelView from "./plugins/otelView";
import PrometheusView from "./plugins/prometheusView";

type SupportedPlatform = {
	id: string;
	name: string;
	icon: React.ReactNode;
	tag?: string;
	disabled?: boolean;
};

const supportedPlatformsList = (resolvedTheme: string): SupportedPlatform[] => [
	{
		id: "otel",
		name: "Open Telemetry",
		icon: (
			<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 128 128" width={21} height={21}>
				<path
					fill="#f5a800"
					d="M67.648 69.797c-5.246 5.25-5.246 13.758 0 19.008 5.25 5.246 13.758 5.246 19.004 0 5.25-5.25 5.25-13.758 0-19.008-5.246-5.246-13.754-5.246-19.004 0Zm14.207 14.219a6.649 6.649 0 0 1-9.41 0 6.65 6.65 0 0 1 0-9.407 6.649 6.649 0 0 1 9.41 0c2.598 2.586 2.598 6.809 0 9.407ZM86.43 3.672l-8.235 8.234a4.17 4.17 0 0 0 0 5.875l32.149 32.149a4.17 4.17 0 0 0 5.875 0l8.234-8.235c1.61-1.61 1.61-4.261 0-5.87L92.29 3.671a4.159 4.159 0 0 0-5.86 0ZM28.738 108.895a3.763 3.763 0 0 0 0-5.31l-4.183-4.187a3.768 3.768 0 0 0-5.313 0l-8.644 8.649-.016.012-2.371-2.375c-1.313-1.313-3.45-1.313-4.75 0-1.313 1.312-1.313 3.449 0 4.75l14.246 14.242a3.353 3.353 0 0 0 4.746 0c1.3-1.313 1.313-3.45 0-4.746l-2.375-2.375.016-.012Zm0 0"
				/>
				<path
					fill="#425cc7"
					d="M72.297 27.313 54.004 45.605c-1.625 1.625-1.625 4.301 0 5.926L65.3 62.824c7.984-5.746 19.18-5.035 26.363 2.153l9.148-9.149c1.622-1.625 1.622-4.297 0-5.922L78.22 27.313a4.185 4.185 0 0 0-5.922 0ZM60.55 67.585l-6.672-6.672c-1.563-1.562-4.125-1.562-5.684 0l-23.53 23.54a4.036 4.036 0 0 0 0 5.687l13.331 13.332a4.036 4.036 0 0 0 5.688 0l15.132-15.157c-3.199-6.609-2.625-14.593 1.735-20.73Zm0 0"
				/>
			</svg>
		),
	},
	{
		id: "prometheus",
		name: "Prometheus",
		icon: <Image alt="Prometheus" src="/images/prometheus-logo.svg" width={21} height={21} className="-ml-0.5" />,
	},
	{
		id: "maxim",
		name: "Maxim",
		icon: <Image alt="Maxim" src={`/maxim-logo${resolvedTheme === "dark" ? "-dark" : ""}.png`} width={19} height={19} />,
	},
	{
		id: "datadog",
		name: "Datadog",
		icon: <Image alt="Datadog" src="/images/datadog-logo.png" width={32} height={32} className="-ml-0.5" />,
	},
	{
		id: "bigquery",
		name: "BigQuery",
		icon: <Image alt="BigQuery" src="/images/bigquery-logo.svg" width={21} height={21} className="-ml-0.5" />,
	},
	{
		id: "newrelic",
		name: "New Relic",
		icon: (
			<svg viewBox="0 0 832.8 959.8" xmlns="http://www.w3.org/2000/svg" width="19" height="19">
				<path d="M672.6 332.3l160.2-92.4v480L416.4 959.8V775.2l256.2-147.6z" fill="#00ac69" />
				<path d="M416.4 184.6L160.2 332.3 0 239.9 416.4 0l416.4 239.9-160.2 92.4z" fill="#1ce783" />
				<path d="M256.2 572.3L0 424.6V239.9l416.4 240v479.9l-160.2-92.2z" fill="#1d252c" />
			</svg>
		),
		disabled: true,
	},
];

export default function ObservabilityView() {
	const dispatch = useAppDispatch();
	const { data: plugins, isLoading } = useGetPluginsQuery();
	const [selectedPluginId, setSelectedPluginId] = useQueryState("plugin");
	const selectedPlugin = useAppSelector((state) => state.plugin.selectedPlugin);

	const { resolvedTheme } = useTheme();

	const supportedPlatforms = useMemo(() => supportedPlatformsList(resolvedTheme || "light"), [resolvedTheme]);

	// Map UI tab IDs to actual plugin names (prometheus tab uses telemetry plugin)
	const getPluginNameForTab = (tabId: string) => (tabId === "prometheus" ? "telemetry" : tabId);

	useEffect(() => {
		if (!plugins || plugins.length === 0) return;
		if (!selectedPluginId) {
			setSelectedPluginId(supportedPlatforms[0].id);
		} else {
			const pluginName = getPluginNameForTab(selectedPluginId);
			const plugin = plugins.find((plugin) => plugin.name === pluginName) ?? {
				name: selectedPluginId,
				enabled: false,
				config: {},
				isCustom: false,
				path: "",
			};
			dispatch(setSelectedPlugin(plugin));
		}
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [plugins]);

	useEffect(() => {
		if (selectedPluginId) {
			const pluginName = getPluginNameForTab(selectedPluginId);
			const plugin = plugins?.find((plugin) => plugin.name === pluginName) ?? {
				name: selectedPluginId,
				enabled: false,
				config: {},
				isCustom: false,
				path: "",
			};
			dispatch(setSelectedPlugin(plugin));
		} else {
			setSelectedPluginId(supportedPlatforms[0].id);
		}
	}, [selectedPluginId]);

	if (isLoading) {
		return <FullPageLoader />;
	}

	return (
		<div className="flex h-full flex-row gap-4">
			<div className="flex flex-col">
				<div className="flex w-[270px] flex-col gap-2 pb-10">
					<div className="rounded-md bg-zinc-100/10 p-4 dark:bg-zinc-800/20">
						<div className="flex flex-col gap-1">
							<div className="text-muted-foreground mb-2 text-xs font-medium">Providers</div>
							{supportedPlatforms.map((tab) => (
								<button
									type="button"
									key={tab.id}
									disabled={!!tab.disabled}
									data-testid={`observability-provider-btn-${tab.id}`}
									aria-disabled={tab.disabled ? true : undefined}
									aria-current={selectedPluginId === tab.id ? "page" : undefined}
									className={cn(
										"mb-1 flex max-h-[32px] w-full items-center gap-2 rounded-sm border px-3 py-1.5 text-sm",
										tab.disabled ? "opacity-50" : "",
										selectedPluginId === tab.id
											? "bg-secondary opacity-100 hover:opacity-100"
											: tab.disabled
												? "border-none"
												: "hover:bg-secondary cursor-pointer border-transparent opacity-100 hover:border",
									)}
									onClick={() => {
										if (tab.disabled) {
											return;
										}
										setSelectedPluginId(tab.id ?? supportedPlatforms[0].id);
									}}
								>
									<div className="w-[24px]">{tab.icon}</div> {tab.name}
									{tab.tag && (
										<Badge variant="secondary" className="text-muted-foreground ml-auto text-[10px] font-medium">
											{tab.tag.toUpperCase()}
										</Badge>
									)}
									{tab.disabled && (
										<Badge variant="secondary" className="text-muted-foreground ml-auto text-[10px] font-medium">
											{"Coming soon".toUpperCase()}
										</Badge>
									)}
								</button>
							))}
						</div>
					</div>
				</div>
			</div>
			<div className="w-full pt-4">
				{selectedPluginId === "prometheus" && <PrometheusView />}
				{selectedPluginId === "otel" && <OtelView />}
				{selectedPluginId === "maxim" && <MaximView />}
				{selectedPluginId === "datadog" && <DatadogView />}
				{selectedPluginId === "bigquery" && <BigQueryView />}
				{selectedPluginId === "newrelic" && <NewrelicView />}
			</div>
		</div>
	);
}
