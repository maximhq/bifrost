import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { TriStateCheckbox } from "@/components/ui/tristateCheckbox";
import { getErrorMessage, useGetLoadedPluginsQuery, useGetPluginQuery, useUpdatePluginMutation } from "@/lib/store";
import { PluginSpanFilter } from "@/lib/types/config";
import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";

interface PluginTracingSheetProps {
	open: boolean;
	onClose: () => void;
	/**
	 * Backend plugin name of the observability connector whose span filter is being edited
	 * (e.g. "otel", "datadog", "bigquery"). The sheet reads/writes only this plugin's
	 * `plugin_span_filter`; the backend merges it over the rest of the connector config.
	 */
	pluginName: string;
	/** Human-readable destination used in the copy, e.g. "the OTEL collector", "Datadog". */
	destination: string;
}

function resolveToggleState(filter: PluginSpanFilter | null | undefined, allPlugins: string[]): Record<string, boolean> {
	const state: Record<string, boolean> = {};
	for (const name of allPlugins) {
		state[name] = true;
	}
	if (!filter) return state;

	if (filter.mode === "exclude") {
		for (const name of filter.plugins) {
			state[name] = false;
		}
	} else {
		for (const name of allPlugins) {
			state[name] = filter.plugins.includes(name);
		}
	}
	return state;
}

function buildFilter(toggles: Record<string, boolean>): PluginSpanFilter | null {
	const excluded = Object.entries(toggles)
		.filter(([, on]) => !on)
		.map(([name]) => name);
	if (excluded.length === 0) return null;
	return { mode: "exclude", plugins: excluded };
}

function PluginRow({ name, checked, onChange }: { name: string; checked: boolean; onChange: (v: boolean) => void }) {
	return (
		<div className="flex items-center justify-between rounded-md border px-3 py-2.5">
			<span className="font-mono text-sm">{name}</span>
			<div className="flex items-center gap-2">
				<Switch checked={checked} onCheckedChange={onChange} data-testid={`plugin-tracing-toggle-${name}`} />
			</div>
		</div>
	);
}

export default function PluginTracingSheet({ open, onClose, pluginName, destination }: PluginTracingSheetProps) {
	// All currently loaded plugins (built-in, enterprise, custom, and auto-loaded) that can
	// emit spans, named to match the connector's span filter. One flat list — the backend
	// already returns the complete set, so there's no built-in/custom split to maintain.
	const { data: allPlugins = [], isLoading: isLoadingLoadedPlugins } = useGetLoadedPluginsQuery();
	const { data: targetPlugin } = useGetPluginQuery(pluginName);
	const [updatePlugin, { isLoading }] = useUpdatePluginMutation();
	const [toggles, setToggles] = useState<Record<string, boolean>>({});
	const wasOpenRef = useRef(false);

	useEffect(() => {
		if (open && !wasOpenRef.current) {
			if (!targetPlugin) return; // wait until persisted config is available
			const filter = (targetPlugin.config?.plugin_span_filter as PluginSpanFilter | undefined) ?? null;
			if (isLoadingLoadedPlugins || allPlugins.length === 0) return;
			setToggles(resolveToggleState(filter, allPlugins));
			wasOpenRef.current = true;
		}
		if (!open) wasOpenRef.current = false;
	}, [open, targetPlugin, allPlugins, isLoadingLoadedPlugins]);

	const setToggle = useCallback((name: string, value: boolean) => {
		setToggles((prev) => ({ ...prev, [name]: value }));
	}, []);

	const handleSave = useCallback(async () => {
		if (!wasOpenRef.current) {
			// Toggles haven't been initialized from persisted config yet (e.g. the plugin list
			// is still loading for an include-mode filter). Saving now would build an empty
			// filter and wipe the stored plugin_span_filter, so block until init completes.
			toast.error("Plugin list is still loading. Please wait before saving.");
			return;
		}
		if (!targetPlugin) {
			toast.error(`${destination} is not configured yet. Save its configuration before configuring plugin tracing.`);
			return;
		}
		const filter = buildFilter(toggles);
		try {
			await updatePlugin({
				name: pluginName,
				data: {
					enabled: targetPlugin.enabled,
					config: { plugin_span_filter: filter },
				},
			}).unwrap();
			toast.success("Plugin tracing configuration saved");
			onClose();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	}, [toggles, targetPlugin, updatePlugin, onClose, pluginName, destination]);

	return (
		<Sheet open={open} onOpenChange={onClose}>
			<SheetContent className="flex w-full flex-col overflow-hidden p-8">
				<SheetHeader className="flex flex-col items-start p-0">
					<SheetTitle>Configure Plugin Tracing</SheetTitle>
					<SheetDescription>
						Choose which plugin hook spans are exported to {destination}. Disabling a plugin removes its spans from traces without affecting
						execution.
					</SheetDescription>
				</SheetHeader>

				<div className="mt-4 flex-1 overflow-y-auto">
					<div className="flex flex-col gap-4">
						<div>
							<div className="mb-2 flex items-center justify-between">
								<p className="text-muted-foreground text-xs font-medium tracking-wide uppercase">Plugins</p>
								<TriStateCheckbox
									allIds={allPlugins}
									selectedIds={allPlugins.filter((n) => toggles[n] ?? true)}
									onChange={(next) => {
										const nextSet = new Set(next);
										setToggles((prev) => {
											const updated = { ...prev };
											for (const n of allPlugins) updated[n] = nextSet.has(n);
											return updated;
										});
									}}
									ariaLabel="Toggle all plugin tracing"
									data-testid="plugin-tracing-select-all"
								/>
							</div>
							<div className="flex flex-col gap-1.5">
								{allPlugins.map((name) => (
									<PluginRow key={name} name={name} checked={toggles[name] ?? true} onChange={(v) => setToggle(name, v)} />
								))}
							</div>
						</div>
					</div>
				</div>

				<div className="flex flex-col gap-2 pt-4">
					<Alert variant="info">
						<AlertDescription>
							<span>
								If <strong className="inline">plugin_span_filter</strong> is set in the <strong className="inline">{pluginName}</strong>{" "}
								plugin config in config.json, it takes precedence over these settings after restarting Bifrost.
							</span>
						</AlertDescription>
					</Alert>
					<div className="flex justify-end gap-2 pt-2">
						<Button type="button" variant="outline" onClick={onClose} disabled={isLoading} data-testid="plugin-tracing-cancel-button">
							Cancel
						</Button>
						<Button
							onClick={handleSave}
							disabled={isLoading || !wasOpenRef.current}
							isLoading={isLoading}
							data-testid="plugin-tracing-save-button"
							type="button"
						>
							Save
						</Button>
					</div>
				</div>
			</SheetContent>
		</Sheet>
	);
}