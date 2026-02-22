"use client";

import FullPageLoader from "@/components/fullPageLoader";
import { Button } from "@/components/ui/button";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { getErrorMessage, setSelectedPlugin, useAppDispatch, useAppSelector, useCreatePluginMutation, useDeletePluginMutation, useGetPluginsQuery, useUpdatePluginMutation } from "@/lib/store";
import { cn } from "@/lib/utils";
import { useTheme } from "next-themes";
import Image from "next/image";
import { Plus, Settings } from "lucide-react";
import { useQueryState } from "nuqs";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { AddConnectorDialog } from "../dialogs/addConnectorDialog";
import { ConnectorsEmptyState } from "./connectorsEmptyState";
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
		id: "newrelic",
		name: "New Relic",
		icon: (
			<svg viewBox="0 0 832.8 959.8" xmlns="http://www.w3.org/2000/svg" width="19" height="19">
				<path d="M672.6 332.3l160.2-92.4v480L416.4 959.8V775.2l256.2-147.6z" fill="#00ac69" />
				<path d="M416.4 184.6L160.2 332.3 0 239.9 416.4 0l416.4 239.9-160.2 92.4z" fill="#1ce783" />
				<path d="M256.2 572.3L0 424.6V239.9l416.4 240v479.9l-160.2-92.2z" fill="#1d252c" />
			</svg>
		),
	},
];

interface ObservabilityViewProps {
	/** When set, a Settings button is shown next to Add New that opens config/observability in the sidepane */
	onOpenObservabilityConfig?: () => void;
}

export default function ObservabilityView({ onOpenObservabilityConfig }: ObservabilityViewProps) {
	const dispatch = useAppDispatch();
	const { data: plugins, isLoading } = useGetPluginsQuery();
	const [createPlugin, { isLoading: isCreating }] = useCreatePluginMutation();
	const [deletePlugin, { isLoading: isDeleting }] = useDeletePluginMutation();
	const [updatePlugin, { isLoading: isUpdating }] = useUpdatePluginMutation();
	const [selectedPluginId, setSelectedPluginId] = useQueryState("plugin");
	const selectedPlugin = useAppSelector((state) => state.plugin.selectedPlugin);
	const [showAddConnectorDialog, setShowAddConnectorDialog] = useState(false);

	const { resolvedTheme } = useTheme();

	const supportedPlatforms = useMemo(() => supportedPlatformsList(resolvedTheme || "light"), [resolvedTheme]);

	// Map UI tab IDs to actual plugin names (prometheus tab uses telemetry plugin)
	const getPluginNameForTab = (tabId: string) => (tabId === "prometheus" ? "telemetry" : tabId);
	const getTabIdForPluginName = (pluginName: string) => (pluginName === "telemetry" ? "prometheus" : pluginName);

	// Only show connectors that are configured (exist in plugins); each card shows green/red dot for enabled/disabled
	const configuredConnectors = useMemo(() => {
		if (!plugins) return [];
		return plugins
			.map((plugin) => {
				const tabId = getTabIdForPluginName(plugin.name);
				const platform = supportedPlatforms.find((p) => p.id === tabId && !p.disabled);
				if (!platform) return null;
				return { ...platform, enabled: plugin.enabled, plugin };
			})
			.filter((c): c is NonNullable<typeof c> => c !== null);
	}, [plugins, supportedPlatforms]);

	// Connector types that can be added (not yet configured)
	const availableToAdd = useMemo(() => {
		const configuredIds = new Set(configuredConnectors.map((c) => c.id));
		return supportedPlatforms.filter((p) => !p.disabled && !configuredIds.has(p.id));
	}, [supportedPlatforms, configuredConnectors]);

	const handleAddConnector = async (tabId: string) => {
		const pluginName = getPluginNameForTab(tabId);
		try {
			await createPlugin({
				name: pluginName,
				path: "",
				enabled: false,
				config: {},
			}).unwrap();
			setSelectedPluginId(tabId);
			toast.success("Connector added.");
		} catch (err) {
			toast.error(getErrorMessage(err));
			throw err;
		}
	};

	const handleAddConnectorFromDialog = async (tabId: string) => {
		await handleAddConnector(tabId);
	};

	const handleDeleteConnectorById = async (tabId: string) => {
		const pluginName = getPluginNameForTab(tabId);
		try {
			await deletePlugin(pluginName).unwrap();
			const remaining = configuredConnectors.filter((c) => c.id !== tabId);
			setSelectedPluginId(remaining[0]?.id ?? null);
			toast.success("Connector removed.");
		} catch (err) {
			toast.error(getErrorMessage(err));
		}
	};

	const handleToggleEnabled = async (connector: (typeof configuredConnectors)[number]) => {
		try {
			await updatePlugin({
				name: getPluginNameForTab(connector.id),
				data: {
					enabled: !connector.enabled,
					config: connector.plugin.config ?? {},
				},
			}).unwrap();
			toast.success(connector.enabled ? "Connector disabled." : "Connector enabled.");
		} catch (err) {
			toast.error(getErrorMessage(err));
		}
	};

	useEffect(() => {
		if (!plugins || plugins.length === 0) return;
		if (!selectedPluginId && configuredConnectors.length > 0) {
			setSelectedPluginId(configuredConnectors[0].id);
		} else if (selectedPluginId) {
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
	}, [plugins, configuredConnectors.length]);

	useEffect(() => {
		if (selectedPluginId && plugins) {
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
	}, [selectedPluginId, plugins, dispatch]);

	if (isLoading) {
		return <FullPageLoader />;
	}

	const currentId = selectedPluginId ?? configuredConnectors[0]?.id ?? "";

	return (
		<div className="flex flex-col gap-6 -mt-5">
			<div className="flex w-full flex-col gap-3">
				{configuredConnectors.length > 0 && (
					<div className="flex w-full items-center justify-between gap-2">
						<span className="text-muted-foreground text-sm font-medium">Configure Connectors</span>
						<div className="flex items-center gap-2">
							{onOpenObservabilityConfig && (
								<Button variant="outline" size="sm" onClick={onOpenObservabilityConfig}>
									<Settings className="size-4" />
									Settings
								</Button>
							)}
							{availableToAdd.length > 0 && (
								<Button
									variant="outline"
									size="sm"
									disabled={isCreating}
									onClick={() => setShowAddConnectorDialog(true)}
								>
									<Plus className="size-4" />
									Add new
								</Button>
							)}
						</div>
					</div>
				)}
				{configuredConnectors.length === 0 ? (
					<ConnectorsEmptyState onOpenAddConnector={() => setShowAddConnectorDialog(true)} />
				) : (
					<div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
						{configuredConnectors.map((connector) => {
							const isDatadogOssOnly = connector.id === "datadog" && !IS_ENTERPRISE;
							const isUnclickable = connector.id === "newrelic" || isDatadogOssOnly;
							return (
							<div
								key={connector.id}
								{...(isUnclickable ? {} : { role: "button" as const, tabIndex: 0, onClick: () => setSelectedPluginId(connector.id), onKeyDown: (e: React.KeyboardEvent) => {
									if (e.key === "Enter" || e.key === " ") {
										e.preventDefault();
										setSelectedPluginId(connector.id);
									}
								} })}
								className={cn(
									"group flex items-center gap-3 rounded-lg border bg-card px-4 py-3 text-left transition-colors",
									isUnclickable ? "cursor-default opacity-90" : "cursor-pointer hover:bg-muted/50",
									currentId === connector.id && !isUnclickable && "border-primary ring-1 ring-primary",
								)}
							>
								<div className="flex min-w-0 flex-1 items-center gap-3">
									<div className="flex shrink-0 items-center [&>svg]:size-5 [&>img]:size-5">
										{connector.icon}
									</div>
									<span className="min-w-0 truncate text-sm font-medium">{connector.name}</span>
								</div>
								{connector.id === "newrelic" ? (
									<span className="text-muted-foreground text-sm">Coming soon</span>
								) : isDatadogOssOnly ? (
									<span className="text-muted-foreground text-sm">Enterprise Exclusive</span>
								) : null}
							</div>
							);
						})}
					</div>
				)}
			</div>
			{configuredConnectors.length > 0 && (
				<div className="min-h-0 flex-1">
					{(() => {
						const currentConnector = configuredConnectors.find((c) => c.id === currentId);
						const enableToggle =
							currentConnector &&
							currentConnector.id !== "newrelic" &&
							!(currentConnector.id === "datadog" && !IS_ENTERPRISE)
								? {
										enabled: currentConnector.enabled,
										onToggle: () => handleToggleEnabled(currentConnector),
										disabled: isUpdating,
									}
								: undefined;
						return (
							<>
								{currentId === "prometheus" && (
									<PrometheusView
										onDelete={() => handleDeleteConnectorById("prometheus")}
										isDeleting={isDeleting}
										enableToggle={enableToggle}
									/>
								)}
								{currentId === "otel" && (
									<OtelView
										onDelete={() => handleDeleteConnectorById("otel")}
										isDeleting={isDeleting}
										enableToggle={enableToggle}
									/>
								)}
								{currentId === "maxim" && (
									<MaximView
										onDelete={() => handleDeleteConnectorById("maxim")}
										isDeleting={isDeleting}
										enableToggle={enableToggle}
									/>
								)}
								{currentId === "datadog" && <DatadogView onDelete={() => handleDeleteConnectorById("datadog")} isDeleting={isDeleting} enableToggle={enableToggle} />}
								{currentId === "newrelic" && <NewrelicView />}
							</>
						);
					})()}
				</div>
			)}
			<AddConnectorDialog
				open={showAddConnectorDialog}
				onOpenChange={setShowAddConnectorDialog}
				availableToAdd={availableToAdd}
				onAdd={handleAddConnectorFromDialog}
				isAdding={isCreating}
			/>
		</div>
	);
}
